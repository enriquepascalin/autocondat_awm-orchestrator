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
	ID                   uuid.UUID              `json:"id"`
	WorkflowDefinitionID string                 `json:"workflow_definition_id"`
	Tenant               string                 `json:"tenant"`
	Status               string                 `json:"status"`
	CurrentPhase         string                 `json:"current_phase"`
	DimensionalState     map[string]interface{} `json:"dimensional_state"`
	Version              int64                  `json:"version"`
	CreatedAt            time.Time              `json:"created_at"`
	UpdatedAt            time.Time              `json:"updated_at"`
}

// Timer represents a durable timer.
type Timer struct {
	ID                 uuid.UUID
	WorkflowInstanceID uuid.UUID
	FireAt             time.Time
	TimerType          string
	Payload            map[string]interface{}
}

// Task represents a unit of work for an agent.
type Task struct {
	ID                 uuid.UUID
	WorkflowInstanceID uuid.UUID
	ActivityName       string
	Capabilities       pq.StringArray // PostgreSQL array scanning
	Input              map[string]interface{}
	Status             string
	AssignedAgentID    *string
	Deadline           *time.Time
}

// Store defines the persistence operations required for event sourcing.
type Store interface {
	// AppendEvents atomically appends events to the history and updates the snapshot.
	AppendEvents(ctx context.Context, instanceID uuid.UUID, expectedVersion int64, events []HistoryEvent) error

	// LoadInstance retrieves the snapshot and all historical events for a workflow.
	LoadInstance(ctx context.Context, instanceID uuid.UUID) (*WorkflowInstance, []HistoryEvent, error)

	// ListActiveInstances returns all workflow instances with status 'RUNNING'.
	ListActiveInstances(ctx context.Context) ([]WorkflowInstance, error)

	// CreateWorkflowInstance inserts a new workflow instance snapshot.
	CreateWorkflowInstance(ctx context.Context, instance *WorkflowInstance) error

	// UpdateWorkflowStatus updates only the status field.
	UpdateWorkflowStatus(ctx context.Context, instanceID uuid.UUID, status string) error

	// AcquireLease attempts to claim ownership of a workflow instance.
	AcquireLease(ctx context.Context, instanceID uuid.UUID, ownerID string, duration time.Duration) (bool, error)

	// RenewLease extends the lease for an owned workflow.
	RenewLease(ctx context.Context, instanceID uuid.UUID, ownerID string, duration time.Duration) error

	// CreateTimer schedules a durable timer.
	CreateTimer(ctx context.Context, instanceID uuid.UUID, fireAt time.Time, timerType string, payload map[string]interface{}) (uuid.UUID, error)

	// GetPendingTimers returns all unfired timers up to a given time.
	GetPendingTimers(ctx context.Context, before time.Time) ([]Timer, error)

	// MarkTimerFired marks a timer as fired.
	MarkTimerFired(ctx context.Context, timerID uuid.UUID) error

	// CreateTask creates a pending task for agent assignment.
	CreateTask(ctx context.Context, task *Task) error

	// GetPendingTasks returns tasks that match given capabilities.
	GetPendingTasks(ctx context.Context, capabilities []string, limit int) ([]Task, error)

	// GetTask retrieves a single task by ID.
	GetTask(ctx context.Context, taskID uuid.UUID) (*Task, error)

	// AssignTask assigns a task to an agent.
	AssignTask(ctx context.Context, taskID uuid.UUID, agentID string, deadline time.Time) error

	// CompleteTask marks a task as completed.
	CompleteTask(ctx context.Context, taskID uuid.UUID, result map[string]interface{}) error

	// FailTask marks a task as failed.
	FailTask(ctx context.Context, taskID uuid.UUID, errorDetails map[string]interface{}) error
}

// ErrConcurrencyConflict is returned when an optimistic lock fails.
var ErrConcurrencyConflict = errors.New("concurrency conflict: workflow version mismatch")

// ErrInstanceNotFound is returned when a workflow instance does not exist.
var ErrInstanceNotFound = errors.New("workflow instance not found")

// ErrTaskNotFound is returned when a task does not exist.
var ErrTaskNotFound = errors.New("task not found")