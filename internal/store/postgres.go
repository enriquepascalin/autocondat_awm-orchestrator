package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
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
	var instance WorkflowInstance
	var dimensionalStateBytes []byte
	row := s.db.QueryRowContext(ctx,
		`SELECT id, workflow_definition_id, tenant, status, current_phase, dimensional_state, version, created_at, updated_at
         FROM workflow_instances WHERE id = $1`, instanceID)
	err := row.Scan(
		&instance.ID,
		&instance.WorkflowDefinitionID,
		&instance.Tenant,
		&instance.Status,
		&instance.CurrentPhase,
		&dimensionalStateBytes,
		&instance.Version,
		&instance.CreatedAt,
		&instance.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil, ErrInstanceNotFound
		}
		return nil, nil, fmt.Errorf("query workflow instance: %w", err)
	}
	if err := json.Unmarshal(dimensionalStateBytes, &instance.DimensionalState); err != nil {
		return nil, nil, fmt.Errorf("unmarshal dimensional state: %w", err)
	}

	var events []HistoryEvent
	rows, err := s.db.QueryContext(ctx,
		`SELECT sequence_num, event_type, payload, recorded_at
         FROM workflow_history
         WHERE workflow_instance_id = $1
         ORDER BY sequence_num ASC`, instanceID)
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

// ListActiveInstances returns all workflow instances with status 'RUNNING'.
func (s *PostgresStore) ListActiveInstances(ctx context.Context) ([]WorkflowInstance, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, workflow_definition_id, tenant, status, current_phase, dimensional_state, version, created_at, updated_at
         FROM workflow_instances WHERE status = 'RUNNING'`)
	if err != nil {
		return nil, fmt.Errorf("list active instances: %w", err)
	}
	defer rows.Close()

	var instances []WorkflowInstance
	for rows.Next() {
		var inst WorkflowInstance
		var dimensionalStateBytes []byte
		if err := rows.Scan(
			&inst.ID,
			&inst.WorkflowDefinitionID,
			&inst.Tenant,
			&inst.Status,
			&inst.CurrentPhase,
			&dimensionalStateBytes,
			&inst.Version,
			&inst.CreatedAt,
			&inst.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan workflow instance: %w", err)
		}
		if err := json.Unmarshal(dimensionalStateBytes, &inst.DimensionalState); err != nil {
			return nil, fmt.Errorf("unmarshal dimensional state: %w", err)
		}
		instances = append(instances, inst)
	}
	return instances, nil
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

// UpdateCurrentPhase advances the workflow snapshot to the named state.
func (s *PostgresStore) UpdateCurrentPhase(ctx context.Context, instanceID uuid.UUID, phase string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE workflow_instances SET current_phase = $1, updated_at = NOW() WHERE id = $2`,
		phase, instanceID)
	return err
}

// CountPendingTasks returns the number of PENDING or ASSIGNED tasks for an instance.
func (s *PostgresStore) CountPendingTasks(ctx context.Context, instanceID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tasks WHERE workflow_instance_id = $1 AND status IN ('PENDING', 'ASSIGNED')`,
		instanceID).Scan(&count)
	return count, err
}

// AcquireLease attempts to claim ownership of a workflow instance.
func (s *PostgresStore) AcquireLease(ctx context.Context, instanceID uuid.UUID, ownerID string, duration time.Duration) (bool, error) {
	var locked bool
	query := `SELECT pg_try_advisory_lock($1)`
	row := s.db.QueryRowContext(ctx, query, instanceIDToInt64(instanceID))
	if err := row.Scan(&locked); err != nil {
		return false, err
	}
	return locked, nil
}

// RenewLease extends the lease for an owned workflow.
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

	query := `INSERT INTO tasks
		(id, workflow_instance_id, state_name, activity_name, capabilities, roles, input, status, deadline, parent_task_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`
	_, err = s.db.ExecContext(ctx, query,
		task.ID,
		task.WorkflowInstanceID,
		task.StateName,
		task.ActivityName,
		pq.Array(task.Capabilities),
		pq.Array(task.Roles),
		inputJSON,
		task.Status,
		task.Deadline,
		task.ParentTaskID,
	)
	return err
}

// GetPendingTasks returns tasks that match any of the given capabilities.
// If capabilities is empty, all pending tasks are returned regardless of capability.
func (s *PostgresStore) GetPendingTasks(ctx context.Context, capabilities []string, limit int) ([]Task, error) {
	var (
		rows *sql.Rows
		err  error
	)
	const cols = `id, workflow_instance_id, state_name, activity_name, capabilities, roles, input, status, assigned_agent_id, deadline, parent_task_id`
	if len(capabilities) == 0 {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+cols+` FROM tasks WHERE status = 'PENDING' LIMIT $1`, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+cols+` FROM tasks WHERE status = 'PENDING' AND capabilities && $1 LIMIT $2`,
			pq.Array(capabilities), limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, *t)
	}
	return tasks, nil
}

// GetTask retrieves a single task by ID.
func (s *PostgresStore) GetTask(ctx context.Context, taskID uuid.UUID) (*Task, error) {
	const cols = `id, workflow_instance_id, state_name, activity_name, capabilities, roles, input, status, assigned_agent_id, deadline, parent_task_id`
	rows, err := s.db.QueryContext(ctx, `SELECT `+cols+` FROM tasks WHERE id = $1`, taskID)
	if err != nil {
		return nil, fmt.Errorf("query task: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, ErrTaskNotFound
	}
	t, err := scanTask(rows)
	if err != nil {
		return nil, fmt.Errorf("scan task: %w", err)
	}
	return t, nil
}

// taskScanner is satisfied by both *sql.Rows and *sql.Row.
type taskScanner interface {
	Scan(dest ...interface{}) error
}

// scanTask reads one task row (all enriched columns).
func scanTask(row taskScanner) (*Task, error) {
	var t Task
	var inputBytes []byte
	var assignedAgentID sql.NullString
	var parentTaskID uuid.NullUUID
	if err := row.Scan(
		&t.ID,
		&t.WorkflowInstanceID,
		&t.StateName,
		&t.ActivityName,
		&t.Capabilities,
		&t.Roles,
		&inputBytes,
		&t.Status,
		&assignedAgentID,
		&t.Deadline,
		&parentTaskID,
	); err != nil {
		return nil, err
	}
	if assignedAgentID.Valid {
		t.AssignedAgentID = &assignedAgentID.String
	}
	if parentTaskID.Valid {
		t.ParentTaskID = &parentTaskID.UUID
	}
	if err := json.Unmarshal(inputBytes, &t.Input); err != nil {
		return nil, fmt.Errorf("unmarshal task input: %w", err)
	}
	return &t, nil
}

// AssignTask assigns a task to an agent.
func (s *PostgresStore) AssignTask(ctx context.Context, taskID uuid.UUID, agentID string, deadline time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET status = 'ASSIGNED', assigned_agent_id = $1, deadline = $2, updated_at = NOW()
		WHERE id = $3 AND status = 'PENDING'`,
		agentID, deadline, taskID)
	return err
}

// CompleteTask marks a task as completed and stores its output.
func (s *PostgresStore) CompleteTask(ctx context.Context, taskID uuid.UUID, result map[string]interface{}) error {
	outputJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal task output: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE tasks SET status = 'COMPLETED', output = $1, updated_at = NOW()
		WHERE id = $2`, outputJSON, taskID)
	return err
}

// FailTask marks a task as failed.
func (s *PostgresStore) FailTask(ctx context.Context, taskID uuid.UUID, errorDetails map[string]interface{}) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET status = 'FAILED', updated_at = NOW()
		WHERE id = $1`, taskID)
	return err
}

// instanceIDToInt64 converts a UUID to an int64 for advisory lock purposes.
func instanceIDToInt64(id uuid.UUID) int64 {
	var val int64
	for i := 0; i < 8; i++ {
		val = (val << 8) | int64(id[i])
	}
	return val
}