package store

import (
    "context"
    "database/sql"
    "encoding/json"
    "fmt"
    "time"

    "github.com/google/uuid"
    "github.com/jmoiron/sqlx"
    _ "github.com/lib/pq"
)

type PostgresStore struct {
    db *sqlx.DB
}

func NewPostgresStore(db *sqlx.DB) *PostgresStore {
    return &PostgresStore{db: db}
}

// AppendEvents atomically appends events and updates the snapshot.
func (s *PostgresStore) AppendEvents(ctx context.Context, instanceID uuid.UUID, expectedVersion int64, events []HistoryEvent) error {
    tx, err := s.db.BeginTxx(ctx, nil)
    if err != nil {
        return fmt.Errorf("begin transaction: %w", err)
    }
    defer tx.Rollback()

    // 1. Update workflow instance snapshot with optimistic locking
    var currentVersion int64
    query := `UPDATE workflow_instances 
              SET version = version + 1, updated_at = NOW() 
              WHERE id = $1 AND version = $2 
              RETURNING version`
    err = tx.GetContext(ctx, &currentVersion, query, instanceID, expectedVersion)
    if err != nil {
        if err == sql.ErrNoRows {
            return ErrConcurrencyConflict
        }
        return fmt.Errorf("update workflow instance: %w", err)
    }

    // 2. Insert history events
    for _, event := range events {
        payloadJSON, err := json.Marshal(event.Payload)
        if err != nil {
            return fmt.Errorf("marshal event payload: %w", err)
        }
        _, err = tx.ExecContext(ctx, `
            INSERT INTO workflow_history (workflow_instance_id, sequence_num, event_type, payload)
            VALUES ($1, $2, $3, $4)
        `, instanceID, event.SequenceNum, event.EventType, payloadJSON)
        if err != nil {
            return fmt.Errorf("insert history event: %w", err)
        }
    }

    return tx.Commit()
}

// LoadInstance retrieves the snapshot and all historical events.
func (s *PostgresStore) LoadInstance(ctx context.Context, instanceID uuid.UUID) (*WorkflowInstance, []HistoryEvent, error) {
    // Load snapshot
    var instance WorkflowInstance
    query := `SELECT id, workflow_definition_id, tenant, status, current_phase, dimensional_state, version, created_at, updated_at
              FROM workflow_instances WHERE id = $1`
    err := s.db.GetContext(ctx, &instance, query, instanceID)
    if err != nil {
        if err == sql.ErrNoRows {
            return nil, nil, ErrInstanceNotFound
        }
        return nil, nil, fmt.Errorf("query workflow instance: %w", err)
    }

    // Load history
    var events []HistoryEvent
    query = `SELECT sequence_num, event_type, payload, recorded_at
             FROM workflow_history 
             WHERE workflow_instance_id = $1 
             ORDER BY sequence_num ASC`
    rows, err := s.db.QueryContext(ctx, query, instanceID)
    if err != nil {
        return nil, nil, fmt.Errorf("query history: %w", err)
    }
    defer rows.Close()

    for rows.Next() {
        var e HistoryEvent
        var payloadBytes []byte
        if err := rows.Scan(&e.SequenceNum, &e.EventType, &payloadBytes, &e.RecordedAt); err != nil {
            return nil, nil, fmt.Errorf("scan history row: %w", err)
        }
        if err := json.Unmarshal(payloadBytes, &e.Payload); err != nil {
            return nil, nil, fmt.Errorf("unmarshal payload: %w", err)
        }
        events = append(events, e)
    }

    return &instance, events, nil
}

// CreateWorkflowInstance inserts a new workflow instance.
func (s *PostgresStore) CreateWorkflowInstance(ctx context.Context, instance *WorkflowInstance) error {
    dimensionalJSON, err := json.Marshal(instance.DimensionalState)
    if err != nil {
        return fmt.Errorf("marshal dimensional state: %w", err)
    }

    query := `INSERT INTO workflow_instances 
              (id, workflow_definition_id, tenant, status, current_phase, dimensional_state, version, created_at, updated_at)
              VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`
    _, err = s.db.ExecContext(ctx, query,
        instance.ID,
        instance.WorkflowDefinitionID,
        instance.Tenant,
        instance.Status,
        instance.CurrentPhase,
        dimensionalJSON,
        instance.Version,
        instance.CreatedAt,
        instance.UpdatedAt,
    )
    return err
}

// UpdateWorkflowStatus updates only the status field.
func (s *PostgresStore) UpdateWorkflowStatus(ctx context.Context, instanceID uuid.UUID, status string) error {
    query := `UPDATE workflow_instances SET status = $1, updated_at = NOW() WHERE id = $2`
    _, err := s.db.ExecContext(ctx, query, status, instanceID)
    return err
}

// AcquireLease is a placeholder; will be implemented with Redis or advisory locks.
func (s *PostgresStore) AcquireLease(ctx context.Context, instanceID uuid.UUID, ownerID string, duration time.Duration) (bool, error) {
    // For initial implementation, we can use PostgreSQL advisory locks or skip leasing.
    // Return true for now to allow development.
    return true, nil
}

// RenewLease is a placeholder.
func (s *PostgresStore) RenewLease(ctx context.Context, instanceID uuid.UUID, ownerID string, duration time.Duration) error {
    return nil
}

// CreateTimer inserts a durable timer.
func (s *PostgresStore) CreateTimer(ctx context.Context, instanceID uuid.UUID, fireAt time.Time, timerType string, payload map[string]interface{}) (uuid.UUID, error) {
    timerID := uuid.New()
    payloadJSON, err := json.Marshal(payload)
    if err != nil {
        return uuid.Nil, fmt.Errorf("marshal payload: %w", err)
    }

    query := `INSERT INTO timers (id, workflow_instance_id, fire_at, timer_type, payload)
              VALUES ($1, $2, $3, $4, $5)`
    _, err = s.db.ExecContext(ctx, query, timerID, instanceID, fireAt, timerType, payloadJSON)
    return timerID, err
}

// GetPendingTimers returns unfired timers up to before time.
func (s *PostgresStore) GetPendingTimers(ctx context.Context, before time.Time) ([]Timer, error) {
    query := `SELECT id, workflow_instance_id, fire_at, timer_type, payload
              FROM timers WHERE NOT fired AND fire_at <= $1`
    rows, err := s.db.QueryContext(ctx, query, before)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var timers []Timer
    for rows.Next() {
        var t Timer
        var payloadBytes []byte
        if err := rows.Scan(&t.ID, &t.WorkflowInstanceID, &t.FireAt, &t.TimerType, &payloadBytes); err != nil {
            return nil, err
        }
        if err := json.Unmarshal(payloadBytes, &t.Payload); err != nil {
            return nil, err
        }
        timers = append(timers, t)
    }
    return timers, nil
}

// MarkTimerFired marks a timer as fired.
func (s *PostgresStore) MarkTimerFired(ctx context.Context, timerID uuid.UUID) error {
    _, err := s.db.ExecContext(ctx, `UPDATE timers SET fired = TRUE WHERE id = $1`, timerID)
    return err
}

// CreateTask inserts a new pending task.
func (s *PostgresStore) CreateTask(ctx context.Context, task *Task) error {
    inputJSON, err := json.Marshal(task.Input)
    if err != nil {
        return err
    }

    query := `INSERT INTO tasks (id, workflow_instance_id, activity_name, capabilities, input, status, deadline)
              VALUES ($1, $2, $3, $4, $5, $6, $7)`
    _, err = s.db.ExecContext(ctx, query,
        task.ID,
        task.WorkflowInstanceID,
        task.ActivityName,
        task.Capabilities,
        inputJSON,
        task.Status,
        task.Deadline,
    )
    return err
}

// GetPendingTasks returns tasks that match any of the given capabilities.
func (s *PostgresStore) GetPendingTasks(ctx context.Context, capabilities []string, limit int) ([]Task, error) {
    query := `SELECT id, workflow_instance_id, activity_name, capabilities, input, status, assigned_agent_id, deadline
              FROM tasks WHERE status = 'PENDING' AND capabilities && $1
              LIMIT $2`
    rows, err := s.db.QueryContext(ctx, query, capabilities, limit)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var tasks []Task
    for rows.Next() {
        var t Task
        var inputBytes []byte
        var assignedAgentID sql.NullString
        if err := rows.Scan(&t.ID, &t.WorkflowInstanceID, &t.ActivityName, &t.Capabilities, &inputBytes, &t.Status, &assignedAgentID, &t.Deadline); err != nil {
            return nil, err
        }
        if assignedAgentID.Valid {
            t.AssignedAgentID = &assignedAgentID.String
        }
        if err := json.Unmarshal(inputBytes, &t.Input); err != nil {
            return nil, err
        }
        tasks = append(tasks, t)
    }
    return tasks, nil
}

// AssignTask assigns a task to an agent.
func (s *PostgresStore) AssignTask(ctx context.Context, taskID uuid.UUID, agentID string, deadline time.Time) error {
    _, err := s.db.ExecContext(ctx, `
        UPDATE tasks SET status = 'ASSIGNED', assigned_agent_id = $1, deadline = $2, updated_at = NOW()
        WHERE id = $3 AND status = 'PENDING'`,
        agentID, deadline, taskID)
    return err
}

// CompleteTask marks a task as completed.
func (s *PostgresStore) CompleteTask(ctx context.Context, taskID uuid.UUID, result map[string]interface{}) error {
    // Could store result in a separate field; here we just update status.
    _, err := s.db.ExecContext(ctx, `
        UPDATE tasks SET status = 'COMPLETED', updated_at = NOW()
        WHERE id = $1`, taskID)
    return err
}

// FailTask marks a task as failed.
func (s *PostgresStore) FailTask(ctx context.Context, taskID uuid.UUID, errorDetails map[string]interface{}) error {
    // Could store error details in a separate field.
    _, err := s.db.ExecContext(ctx, `
        UPDATE tasks SET status = 'FAILED', updated_at = NOW()
        WHERE id = $1`, taskID)
    return err
}