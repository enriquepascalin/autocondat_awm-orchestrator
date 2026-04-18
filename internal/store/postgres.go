package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
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

// ── Event sourcing ────────────────────────────────────────────────────────────

func (s *PostgresStore) AppendEvents(ctx context.Context, instanceID uuid.UUID, expectedVersion int64, events []HistoryEvent) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	var currentVersion int64
	err = tx.GetContext(ctx, &currentVersion,
		`UPDATE workflow_instances SET version = version + 1, updated_at = NOW()
		 WHERE id = $1 AND version = $2 RETURNING version`,
		instanceID, expectedVersion)
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
		_, err = tx.ExecContext(ctx,
			`INSERT INTO workflow_history (workflow_instance_id, sequence_num, event_type, payload)
			 VALUES ($1, $2, $3, $4)`,
			instanceID, event.SequenceNum, event.EventType, payloadJSON)
		if err != nil {
			return fmt.Errorf("insert history event: %w", err)
		}
	}

	return tx.Commit()
}

func (s *PostgresStore) LoadInstance(ctx context.Context, instanceID uuid.UUID) (*WorkflowInstance, []HistoryEvent, error) {
	inst, err := s.scanInstance(s.db.QueryRowContext(ctx,
		`SELECT id, workflow_definition_id, tenant, status, current_phase, dimensional_state, version, created_at, updated_at
		 FROM workflow_instances WHERE id = $1`, instanceID))
	if err != nil {
		return nil, nil, err
	}

	events, err := s.queryHistory(ctx, instanceID, 0)
	return inst, events, err
}

func (s *PostgresStore) GetHistoryEvents(ctx context.Context, instanceID uuid.UUID, afterSeq int64) ([]HistoryEvent, error) {
	return s.queryHistory(ctx, instanceID, afterSeq)
}

func (s *PostgresStore) queryHistory(ctx context.Context, instanceID uuid.UUID, afterSeq int64) ([]HistoryEvent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT sequence_num, event_type, payload, recorded_at
		 FROM workflow_history WHERE workflow_instance_id = $1 AND sequence_num > $2
		 ORDER BY sequence_num ASC`, instanceID, afterSeq)
	if err != nil {
		return nil, fmt.Errorf("query history: %w", err)
	}
	defer rows.Close()

	var events []HistoryEvent
	for rows.Next() {
		var e HistoryEvent
		var payloadBytes []byte
		if err := rows.Scan(&e.SequenceNum, &e.EventType, &payloadBytes, &e.RecordedAt); err != nil {
			return nil, fmt.Errorf("scan history row: %w", err)
		}
		if err := json.Unmarshal(payloadBytes, &e.Payload); err != nil {
			return nil, fmt.Errorf("unmarshal payload: %w", err)
		}
		events = append(events, e)
	}
	return events, nil
}

// ── Workflow instances ────────────────────────────────────────────────────────

func (s *PostgresStore) CreateWorkflowInstance(ctx context.Context, instance *WorkflowInstance) error {
	dimensionalJSON, err := json.Marshal(instance.DimensionalState)
	if err != nil {
		return fmt.Errorf("marshal dimensional state: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO workflow_instances
		 (id, workflow_definition_id, tenant, status, current_phase, dimensional_state, version, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		instance.ID, instance.WorkflowDefinitionID, instance.Tenant,
		instance.Status, instance.CurrentPhase, dimensionalJSON,
		instance.Version, instance.CreatedAt, instance.UpdatedAt)
	return err
}

func (s *PostgresStore) UpdateWorkflowStatus(ctx context.Context, instanceID uuid.UUID, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE workflow_instances SET status = $1, updated_at = NOW() WHERE id = $2`, status, instanceID)
	return err
}

func (s *PostgresStore) UpdateCurrentPhase(ctx context.Context, instanceID uuid.UUID, phase string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE workflow_instances SET current_phase = $1, updated_at = NOW() WHERE id = $2`, phase, instanceID)
	return err
}

func (s *PostgresStore) ListActiveInstances(ctx context.Context) ([]WorkflowInstance, error) {
	return s.queryInstances(ctx,
		`SELECT id, workflow_definition_id, tenant, status, current_phase, dimensional_state, version, created_at, updated_at
		 FROM workflow_instances WHERE status = 'RUNNING'`)
}

func (s *PostgresStore) ListWorkflowInstances(ctx context.Context, f ListInstancesFilter) ([]WorkflowInstance, error) {
	q := `SELECT id, workflow_definition_id, tenant, status, current_phase, dimensional_state, version, created_at, updated_at
	      FROM workflow_instances WHERE 1=1`
	args := []interface{}{}
	n := 1

	if f.WorkflowDefinitionID != "" {
		q += fmt.Sprintf(" AND workflow_definition_id = $%d", n)
		args = append(args, f.WorkflowDefinitionID)
		n++
	}
	if f.Status != "" {
		q += fmt.Sprintf(" AND status = $%d", n)
		args = append(args, f.Status)
		n++
	}
	q += " ORDER BY created_at DESC"
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT $%d OFFSET $%d", n, n+1)
		args = append(args, f.Limit, f.Offset)
	}
	return s.queryInstances(ctx, q, args...)
}

func (s *PostgresStore) queryInstances(ctx context.Context, q string, args ...interface{}) ([]WorkflowInstance, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query instances: %w", err)
	}
	defer rows.Close()

	var instances []WorkflowInstance
	for rows.Next() {
		inst, err := s.scanInstance(rows)
		if err != nil {
			return nil, err
		}
		instances = append(instances, *inst)
	}
	return instances, nil
}

type instanceScanner interface{ Scan(...interface{}) error }

func (s *PostgresStore) scanInstance(row instanceScanner) (*WorkflowInstance, error) {
	var inst WorkflowInstance
	var dimBytes []byte
	err := row.Scan(&inst.ID, &inst.WorkflowDefinitionID, &inst.Tenant, &inst.Status,
		&inst.CurrentPhase, &dimBytes, &inst.Version, &inst.CreatedAt, &inst.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrInstanceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan instance: %w", err)
	}
	if err := json.Unmarshal(dimBytes, &inst.DimensionalState); err != nil {
		return nil, fmt.Errorf("unmarshal dimensional state: %w", err)
	}
	return &inst, nil
}

// ── Workflow definitions ──────────────────────────────────────────────────────

func (s *PostgresStore) ListWorkflowDefinitions(ctx context.Context, f ListDefinitionsFilter) ([]WorkflowDefinitionRow, error) {
	q := `SELECT id, tenant_id, name, definition_id, version, yaml_content,
		         COALESCE(created_by,'') AS created_by, created_at, updated_at
		  FROM workflow_definitions WHERE 1=1`
	args := []interface{}{}
	n := 1

	if f.TenantID != "" {
		q += fmt.Sprintf(" AND tenant_id = $%d", n)
		args = append(args, f.TenantID)
		n++
	}
	if f.NameFilter != "" {
		q += fmt.Sprintf(" AND name ILIKE $%d", n)
		args = append(args, "%"+f.NameFilter+"%")
		n++
	}
	q += " ORDER BY created_at DESC"
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT $%d OFFSET $%d", n, n+1)
		args = append(args, f.Limit, f.Offset)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list workflow definitions: %w", err)
	}
	defer rows.Close()

	var defs []WorkflowDefinitionRow
	for rows.Next() {
		var d WorkflowDefinitionRow
		if err := rows.Scan(&d.ID, &d.TenantID, &d.Name, &d.DefinitionID,
			&d.Version, &d.YAMLContent, &d.CreatedBy, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan definition row: %w", err)
		}
		defs = append(defs, d)
	}
	return defs, nil
}

func (s *PostgresStore) DeleteWorkflowDefinition(ctx context.Context, id uuid.UUID) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM workflow_definitions WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete workflow definition: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("workflow definition not found")
	}
	return nil
}

func (s *PostgresStore) UpdateWorkflowDefinitionYAML(ctx context.Context, id uuid.UUID, yaml string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE workflow_definitions SET yaml_content = $1, version = version + 1, updated_at = NOW()
		 WHERE id = $2`, yaml, id)
	return err
}

// ── Leases ────────────────────────────────────────────────────────────────────

func (s *PostgresStore) AcquireLease(ctx context.Context, instanceID uuid.UUID, ownerID string, duration time.Duration) (bool, error) {
	var locked bool
	row := s.db.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, instanceIDToInt64(instanceID))
	if err := row.Scan(&locked); err != nil {
		return false, err
	}
	return locked, nil
}

func (s *PostgresStore) RenewLease(ctx context.Context, instanceID uuid.UUID, ownerID string, duration time.Duration) error {
	return nil
}

// ── Timers ────────────────────────────────────────────────────────────────────

func (s *PostgresStore) CreateTimer(ctx context.Context, instanceID uuid.UUID, fireAt time.Time, timerType string, payload map[string]interface{}) (uuid.UUID, error) {
	timerID := uuid.New()
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return uuid.Nil, fmt.Errorf("marshal payload: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO timers (id, workflow_instance_id, fire_at, timer_type, payload) VALUES ($1, $2, $3, $4, $5)`,
		timerID, instanceID, fireAt, timerType, payloadJSON)
	return timerID, err
}

func (s *PostgresStore) GetPendingTimers(ctx context.Context, before time.Time) ([]Timer, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, workflow_instance_id, fire_at, timer_type, payload
		 FROM timers WHERE NOT fired AND fire_at <= $1`, before)
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

func (s *PostgresStore) MarkTimerFired(ctx context.Context, timerID uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, `UPDATE timers SET fired = TRUE WHERE id = $1`, timerID)
	return err
}

// ── Tasks ─────────────────────────────────────────────────────────────────────

const taskCols = `id, workflow_instance_id, state_name, activity_name,
	capabilities, roles, input, COALESCE(output,'null'::jsonb) AS output,
	COALESCE(evidence,'null'::jsonb) AS evidence,
	status, assigned_agent_id, deadline, parent_task_id,
	created_at, updated_at`

func (s *PostgresStore) CreateTask(ctx context.Context, task *Task) error {
	inputJSON, err := json.Marshal(task.Input)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO tasks
		 (id, workflow_instance_id, state_name, activity_name, capabilities, roles, input, status, deadline, parent_task_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		task.ID, task.WorkflowInstanceID, task.StateName, task.ActivityName,
		pq.Array(task.Capabilities), pq.Array(task.Roles), inputJSON,
		task.Status, task.Deadline, task.ParentTaskID)
	return err
}

func (s *PostgresStore) GetTask(ctx context.Context, taskID uuid.UUID) (*Task, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+taskCols+` FROM tasks WHERE id = $1`, taskID)
	if err != nil {
		return nil, fmt.Errorf("query task: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, ErrTaskNotFound
	}
	return scanTask(rows)
}

func (s *PostgresStore) GetPendingTasks(ctx context.Context, capabilities []string, limit int) ([]Task, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if len(capabilities) == 0 {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+taskCols+` FROM tasks WHERE status = 'PENDING' ORDER BY created_at ASC LIMIT $1`, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+taskCols+` FROM tasks WHERE status = 'PENDING' AND capabilities && $1 ORDER BY created_at ASC LIMIT $2`,
			pq.Array(capabilities), limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

func (s *PostgresStore) ListTasksForInstance(ctx context.Context, instanceID uuid.UUID, statusFilter string, limit, offset int) ([]Task, error) {
	q := `SELECT ` + taskCols + ` FROM tasks WHERE workflow_instance_id = $1`
	args := []interface{}{instanceID}
	if statusFilter != "" {
		q += ` AND status = $2 ORDER BY created_at ASC LIMIT $3 OFFSET $4`
		args = append(args, statusFilter, limit, offset)
	} else {
		q += ` ORDER BY created_at ASC LIMIT $2 OFFSET $3`
		args = append(args, limit, offset)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

func (s *PostgresStore) ListTasksForAgent(ctx context.Context, agentID, tenant, statusFilter string) ([]Task, error) {
	q := `SELECT ` + taskCols + ` FROM tasks WHERE assigned_agent_id = $1`
	args := []interface{}{agentID}
	if statusFilter != "" {
		q += ` AND status = $2 ORDER BY created_at ASC`
		args = append(args, statusFilter)
	} else {
		q += ` AND status IN ('PENDING','ASSIGNED') ORDER BY created_at ASC`
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

func (s *PostgresStore) AssignTask(ctx context.Context, taskID uuid.UUID, agentID string, deadline time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET status = 'ASSIGNED', assigned_agent_id = $1, deadline = $2, updated_at = NOW()
		 WHERE id = $3 AND status = 'PENDING'`,
		agentID, deadline, taskID)
	return err
}

func (s *PostgresStore) ClaimTask(ctx context.Context, taskID uuid.UUID, agentID string, deadline time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET status = 'ASSIGNED', assigned_agent_id = $1, deadline = $2, updated_at = NOW()
		 WHERE id = $3 AND status = 'PENDING'`,
		agentID, deadline, taskID)
	if err != nil {
		return fmt.Errorf("claim task: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrTaskNotClaimable
	}
	return nil
}

func (s *PostgresStore) ReleaseTask(ctx context.Context, taskID uuid.UUID, agentID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET status = 'PENDING', assigned_agent_id = NULL, deadline = NULL, updated_at = NOW()
		 WHERE id = $1 AND assigned_agent_id = $2 AND status = 'ASSIGNED'`,
		taskID, agentID)
	if err != nil {
		return fmt.Errorf("release task: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("task not found or not assigned to agent %s", agentID)
	}
	return nil
}

func (s *PostgresStore) CompleteTask(ctx context.Context, taskID uuid.UUID, result map[string]interface{}) error {
	outputJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal task output: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE tasks SET status = 'COMPLETED', output = $1, updated_at = NOW() WHERE id = $2`,
		outputJSON, taskID)
	return err
}

func (s *PostgresStore) CompleteTaskWithEvidence(ctx context.Context, taskID uuid.UUID, result map[string]interface{}, evidence map[string]interface{}) error {
	outputJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal task output: %w", err)
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return fmt.Errorf("marshal evidence: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE tasks SET status = 'COMPLETED', output = $1, evidence = $2, updated_at = NOW() WHERE id = $3`,
		outputJSON, evidenceJSON, taskID)
	return err
}

func (s *PostgresStore) FailTask(ctx context.Context, taskID uuid.UUID, errorDetails map[string]interface{}) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET status = 'FAILED', updated_at = NOW() WHERE id = $1`, taskID)
	return err
}

func (s *PostgresStore) ExtendTaskDeadline(ctx context.Context, taskID uuid.UUID, newDeadline time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET deadline = $1, updated_at = NOW() WHERE id = $2 AND status = 'ASSIGNED'`,
		newDeadline, taskID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("task not found or not in ASSIGNED state")
	}
	return nil
}

func (s *PostgresStore) ReassignTask(ctx context.Context, taskID uuid.UUID, fromAgentID, toAgentID string) error {
	var q string
	var args []interface{}
	if toAgentID == "" {
		// Return to pool
		q = `UPDATE tasks SET status = 'PENDING', assigned_agent_id = NULL, deadline = NULL, updated_at = NOW()
		     WHERE id = $1 AND assigned_agent_id = $2`
		args = []interface{}{taskID, fromAgentID}
	} else {
		q = `UPDATE tasks SET assigned_agent_id = $1, updated_at = NOW()
		     WHERE id = $2 AND assigned_agent_id = $3`
		args = []interface{}{toAgentID, taskID, fromAgentID}
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("task not found or not assigned to agent %s", fromAgentID)
	}
	return nil
}

func (s *PostgresStore) CountPendingTasks(ctx context.Context, instanceID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tasks WHERE workflow_instance_id = $1 AND status IN ('PENDING', 'ASSIGNED')`,
		instanceID).Scan(&count)
	return count, err
}

// ── Task audit log ────────────────────────────────────────────────────────────

func (s *PostgresStore) AppendTaskAudit(ctx context.Context, entry *TaskAuditEntry) error {
	dataJSON, err := json.Marshal(entry.Data)
	if err != nil {
		return fmt.Errorf("marshal audit data: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO task_audit_log (task_id, event_type, agent_id, message, data)
		 VALUES ($1, $2, $3, $4, $5)`,
		entry.TaskID, entry.EventType, entry.AgentID, entry.Message, dataJSON)
	return err
}

func (s *PostgresStore) GetTaskAuditLog(ctx context.Context, taskID uuid.UUID) ([]TaskAuditEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, event_type, COALESCE(agent_id,'') AS agent_id,
		        COALESCE(message,'') AS message, COALESCE(data,'null'::jsonb) AS data, occurred_at
		 FROM task_audit_log WHERE task_id = $1 ORDER BY occurred_at ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []TaskAuditEntry
	for rows.Next() {
		var e TaskAuditEntry
		var dataBytes []byte
		if err := rows.Scan(&e.ID, &e.TaskID, &e.EventType, &e.AgentID, &e.Message, &dataBytes, &e.OccurredAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(dataBytes, &e.Data); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// ── Internal scan helpers ─────────────────────────────────────────────────────

type taskScanner interface{ Scan(...interface{}) error }

func scanTask(row taskScanner) (*Task, error) {
	var t Task
	var inputBytes, outputBytes, evidenceBytes []byte
	var assignedAgentID sql.NullString
	var parentTaskID uuid.NullUUID

	if err := row.Scan(
		&t.ID, &t.WorkflowInstanceID, &t.StateName, &t.ActivityName,
		&t.Capabilities, &t.Roles,
		&inputBytes, &outputBytes, &evidenceBytes,
		&t.Status, &assignedAgentID, &t.Deadline, &parentTaskID,
		&t.CreatedAt, &t.UpdatedAt,
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
	if len(outputBytes) > 0 && string(outputBytes) != "null" {
		_ = json.Unmarshal(outputBytes, &t.Output)
	}
	if len(evidenceBytes) > 0 && string(evidenceBytes) != "null" {
		_ = json.Unmarshal(evidenceBytes, &t.Evidence)
	}
	return &t, nil
}

func scanTasks(rows *sql.Rows) ([]Task, error) {
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

// instanceIDToInt64 converts a UUID to int64 for PostgreSQL advisory locks.
func instanceIDToInt64(id uuid.UUID) int64 {
	var val int64
	for i := 0; i < 8; i++ {
		val = (val << 8) | int64(id[i])
	}
	return val
}

// buildPlaceholders generates $1,$2,...$n for SQL IN clauses.
func buildPlaceholders(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("$%d", i+1)
	}
	return strings.Join(parts, ",")
}
