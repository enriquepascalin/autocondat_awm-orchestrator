package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// HistoryEvent represents a single event in the workflow log.
type HistoryEvent struct {
	SequenceNum int64                  `json:"sequence_num"`
	EventType   string                 `json:"event_type"`
	Payload     map[string]interface{} `json:"payload"`
	RecordedAt  time.Time              `json:"recorded_at"`
}

// WorkflowInstance is the current snapshot of a workflow.
type WorkflowInstance struct {
	ID                   uuid.UUID              `db:"id"`
	WorkflowDefinitionID string                 `db:"workflow_definition_id"`
	Tenant               string                 `db:"tenant"`
	Status               string                 `db:"status"`
	CurrentPhase         string                 `db:"current_phase"`
	DimensionalState     map[string]interface{} `db:"dimensional_state"`
	Version              int64                  `db:"version"`
	CreatedAt            time.Time              `db:"created_at"`
	UpdatedAt            time.Time              `db:"updated_at"`
}

// WorkflowDefinitionRow is a row from the workflow_definitions table.
type WorkflowDefinitionRow struct {
	ID           uuid.UUID `db:"id"`
	TenantID     string    `db:"tenant_id"`
	Name         string    `db:"name"`
	DefinitionID string    `db:"definition_id"`
	Version      int       `db:"version"`
	YAMLContent  string    `db:"yaml_content"`
	CreatedBy    string    `db:"created_by"`
	CreatedAt    time.Time `db:"created_at"`
	UpdatedAt    time.Time `db:"updated_at"`
}

// Timer represents a durable timer.
type Timer struct {
	ID                 uuid.UUID              `db:"id"`
	WorkflowInstanceID uuid.UUID              `db:"workflow_instance_id"`
	FireAt             time.Time              `db:"fire_at"`
	TimerType          string                 `db:"timer_type"`
	Payload            map[string]interface{} `db:"payload"`
}

// Task represents a unit of work for an agent.
type Task struct {
	ID                 uuid.UUID              `db:"id"`
	WorkflowInstanceID uuid.UUID              `db:"workflow_instance_id"`
	StateName          string                 `db:"state_name"`
	ActivityName       string                 `db:"activity_name"`
	Capabilities       pq.StringArray         `db:"capabilities"`
	Roles              pq.StringArray         `db:"roles"`
	Input              map[string]interface{} `db:"input"`
	Output             map[string]interface{} `db:"output"`
	Evidence           map[string]interface{} `db:"evidence"`
	Status             string                 `db:"status"`
	AssignedAgentID    *string                `db:"assigned_agent_id"`
	Deadline           *time.Time             `db:"deadline"`
	ParentTaskID       *uuid.UUID             `db:"parent_task_id"`
	CreatedAt          time.Time              `db:"created_at"`
	UpdatedAt          time.Time              `db:"updated_at"`
}

// TaskAuditEntry is one row in the task_audit_log table.
type TaskAuditEntry struct {
	ID         uuid.UUID              `db:"id"`
	TaskID     uuid.UUID              `db:"task_id"`
	EventType  string                 `db:"event_type"`
	AgentID    string                 `db:"agent_id"`
	Message    string                 `db:"message"`
	Data       map[string]interface{} `db:"data"`
	OccurredAt time.Time              `db:"occurred_at"`
}

// ListInstancesFilter constrains ListWorkflowInstances queries.
type ListInstancesFilter struct {
	WorkflowDefinitionID string
	Status               string
	Limit                int
	Offset               int
}

// ListDefinitionsFilter constrains ListWorkflowDefinitions queries.
type ListDefinitionsFilter struct {
	TenantID   string
	NameFilter string
	Limit      int
	Offset     int
}

// Store defines all persistence operations.
type Store interface {

	// ── Event sourcing ────────────────────────────────────────────────────────

	// AppendEvents atomically appends events and updates the instance snapshot.
	AppendEvents(ctx context.Context, instanceID uuid.UUID, expectedVersion int64, events []HistoryEvent) error

	// LoadInstance retrieves the snapshot and full event history for a workflow.
	LoadInstance(ctx context.Context, instanceID uuid.UUID) (*WorkflowInstance, []HistoryEvent, error)

	// GetHistoryEvents returns events for a workflow, optionally starting after a sequence number.
	GetHistoryEvents(ctx context.Context, instanceID uuid.UUID, afterSeq int64) ([]HistoryEvent, error)

	// ── Workflow instances ────────────────────────────────────────────────────

	// CreateWorkflowInstance inserts a new workflow instance.
	CreateWorkflowInstance(ctx context.Context, instance *WorkflowInstance) error

	// UpdateWorkflowStatus updates the status field.
	UpdateWorkflowStatus(ctx context.Context, instanceID uuid.UUID, status string) error

	// UpdateCurrentPhase advances the workflow to the named state.
	UpdateCurrentPhase(ctx context.Context, instanceID uuid.UUID, phase string) error

	// ListActiveInstances returns all instances with status 'RUNNING'.
	ListActiveInstances(ctx context.Context) ([]WorkflowInstance, error)

	// ListWorkflowInstances returns instances matching the filter.
	ListWorkflowInstances(ctx context.Context, filter ListInstancesFilter) ([]WorkflowInstance, error)

	// ── Workflow definitions ──────────────────────────────────────────────────

	// ListWorkflowDefinitions returns definitions matching the filter.
	ListWorkflowDefinitions(ctx context.Context, filter ListDefinitionsFilter) ([]WorkflowDefinitionRow, error)

	// DeleteWorkflowDefinition removes a definition row by its internal UUID.
	DeleteWorkflowDefinition(ctx context.Context, id uuid.UUID) error

	// UpdateWorkflowDefinitionYAML replaces the YAML content, bumping version.
	UpdateWorkflowDefinitionYAML(ctx context.Context, id uuid.UUID, yaml string) error

	// ── Leases ────────────────────────────────────────────────────────────────

	// AcquireLease attempts to claim ownership of a workflow instance.
	AcquireLease(ctx context.Context, instanceID uuid.UUID, ownerID string, duration time.Duration) (bool, error)

	// RenewLease extends the lease for an already-owned workflow.
	RenewLease(ctx context.Context, instanceID uuid.UUID, ownerID string, duration time.Duration) error

	// ── Timers ────────────────────────────────────────────────────────────────

	// CreateTimer schedules a durable timer.
	CreateTimer(ctx context.Context, instanceID uuid.UUID, fireAt time.Time, timerType string, payload map[string]interface{}) (uuid.UUID, error)

	// GetPendingTimers returns all unfired timers up to the given time.
	GetPendingTimers(ctx context.Context, before time.Time) ([]Timer, error)

	// MarkTimerFired marks a timer as fired so it won't be returned again.
	MarkTimerFired(ctx context.Context, timerID uuid.UUID) error

	// ── Tasks ─────────────────────────────────────────────────────────────────

	// CreateTask creates a pending task for agent assignment.
	CreateTask(ctx context.Context, task *Task) error

	// GetTask retrieves a single task by ID.
	GetTask(ctx context.Context, taskID uuid.UUID) (*Task, error)

	// GetPendingTasks returns unassigned tasks matching the given capabilities.
	GetPendingTasks(ctx context.Context, capabilities []string, limit int) ([]Task, error)

	// ListTasksForInstance returns all tasks belonging to a workflow instance.
	ListTasksForInstance(ctx context.Context, instanceID uuid.UUID, statusFilter string, limit, offset int) ([]Task, error)

	// ListTasksForAgent returns tasks currently assigned to a specific agent.
	ListTasksForAgent(ctx context.Context, agentID, tenant, statusFilter string) ([]Task, error)

	// AssignTask assigns a pending task to an agent.
	AssignTask(ctx context.Context, taskID uuid.UUID, agentID string, deadline time.Time) error

	// ClaimTask atomically moves a task from PENDING to ASSIGNED for the given agent.
	// Returns ErrTaskNotFound if the task does not exist or is not PENDING.
	ClaimTask(ctx context.Context, taskID uuid.UUID, agentID string, deadline time.Time) error

	// ReleaseTask returns an ASSIGNED task to PENDING status, clearing the agent.
	ReleaseTask(ctx context.Context, taskID uuid.UUID, agentID string) error

	// CompleteTask marks a task COMPLETED and stores the result output.
	CompleteTask(ctx context.Context, taskID uuid.UUID, result map[string]interface{}) error

	// CompleteTaskWithEvidence marks a task COMPLETED, storing output and evidence separately.
	CompleteTaskWithEvidence(ctx context.Context, taskID uuid.UUID, result map[string]interface{}, evidence map[string]interface{}) error

	// FailTask marks a task FAILED with error details.
	FailTask(ctx context.Context, taskID uuid.UUID, errorDetails map[string]interface{}) error

	// ExtendTaskDeadline updates the deadline for an assigned task.
	ExtendTaskDeadline(ctx context.Context, taskID uuid.UUID, newDeadline time.Time) error

	// ReassignTask atomically moves a task from one agent to another (or back to pool).
	ReassignTask(ctx context.Context, taskID uuid.UUID, fromAgentID, toAgentID string) error

	// CountPendingTasks returns the count of PENDING or ASSIGNED tasks for an instance.
	CountPendingTasks(ctx context.Context, instanceID uuid.UUID) (int, error)

	// ── Task audit log ────────────────────────────────────────────────────────

	// AppendTaskAudit inserts a new audit entry for a task.
	AppendTaskAudit(ctx context.Context, entry *TaskAuditEntry) error

	// GetTaskAuditLog returns all audit entries for a task, ordered by time.
	GetTaskAuditLog(ctx context.Context, taskID uuid.UUID) ([]TaskAuditEntry, error)
}

// ErrConcurrencyConflict is returned when an optimistic lock fails.
var ErrConcurrencyConflict = errors.New("concurrency conflict: workflow version mismatch")

// ErrInstanceNotFound is returned when a workflow instance does not exist.
var ErrInstanceNotFound = errors.New("workflow instance not found")

// ErrTaskNotFound is returned when a task does not exist.
var ErrTaskNotFound = errors.New("task not found")

// ErrTaskNotClaimable is returned when a task cannot be claimed (already assigned, completed, etc.).
var ErrTaskNotClaimable = errors.New("task is not available to claim")
