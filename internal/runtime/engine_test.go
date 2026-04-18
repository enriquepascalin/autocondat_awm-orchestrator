package runtime_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/enriquepascalin/awm-orchestrator/internal/model"
	"github.com/enriquepascalin/awm-orchestrator/internal/runtime"
	"github.com/enriquepascalin/awm-orchestrator/internal/store"
)

// MockStore implements store.Store for unit tests.
type MockStore struct {
	mock.Mock
}

// ── Event sourcing ────────────────────────────────────────────────────────────

func (m *MockStore) AppendEvents(ctx context.Context, instanceID uuid.UUID, expectedVersion int64, events []store.HistoryEvent) error {
	return m.Called(ctx, instanceID, expectedVersion, events).Error(0)
}

func (m *MockStore) LoadInstance(ctx context.Context, instanceID uuid.UUID) (*store.WorkflowInstance, []store.HistoryEvent, error) {
	args := m.Called(ctx, instanceID)
	if args.Get(0) == nil {
		return nil, nil, args.Error(2)
	}
	return args.Get(0).(*store.WorkflowInstance), args.Get(1).([]store.HistoryEvent), args.Error(2)
}

func (m *MockStore) GetHistoryEvents(ctx context.Context, instanceID uuid.UUID, afterSeq int64) ([]store.HistoryEvent, error) {
	args := m.Called(ctx, instanceID, afterSeq)
	return args.Get(0).([]store.HistoryEvent), args.Error(1)
}

// ── Workflow instances ────────────────────────────────────────────────────────

func (m *MockStore) CreateWorkflowInstance(ctx context.Context, instance *store.WorkflowInstance) error {
	return m.Called(ctx, instance).Error(0)
}

func (m *MockStore) UpdateWorkflowStatus(ctx context.Context, instanceID uuid.UUID, status string) error {
	return m.Called(ctx, instanceID, status).Error(0)
}

func (m *MockStore) UpdateCurrentPhase(ctx context.Context, instanceID uuid.UUID, phase string) error {
	return m.Called(ctx, instanceID, phase).Error(0)
}

func (m *MockStore) ListActiveInstances(ctx context.Context) ([]store.WorkflowInstance, error) {
	args := m.Called(ctx)
	return args.Get(0).([]store.WorkflowInstance), args.Error(1)
}

func (m *MockStore) ListWorkflowInstances(ctx context.Context, filter store.ListInstancesFilter) ([]store.WorkflowInstance, error) {
	args := m.Called(ctx, filter)
	return args.Get(0).([]store.WorkflowInstance), args.Error(1)
}

// ── Workflow definitions ──────────────────────────────────────────────────────

func (m *MockStore) ListWorkflowDefinitions(ctx context.Context, filter store.ListDefinitionsFilter) ([]store.WorkflowDefinitionRow, error) {
	args := m.Called(ctx, filter)
	return args.Get(0).([]store.WorkflowDefinitionRow), args.Error(1)
}

func (m *MockStore) DeleteWorkflowDefinition(ctx context.Context, id uuid.UUID) error {
	return m.Called(ctx, id).Error(0)
}

func (m *MockStore) UpdateWorkflowDefinitionYAML(ctx context.Context, id uuid.UUID, yaml string) error {
	return m.Called(ctx, id, yaml).Error(0)
}

// ── Leases ────────────────────────────────────────────────────────────────────

func (m *MockStore) AcquireLease(ctx context.Context, instanceID uuid.UUID, ownerID string, duration time.Duration) (bool, error) {
	args := m.Called(ctx, instanceID, ownerID, duration)
	return args.Bool(0), args.Error(1)
}

func (m *MockStore) RenewLease(ctx context.Context, instanceID uuid.UUID, ownerID string, duration time.Duration) error {
	return m.Called(ctx, instanceID, ownerID, duration).Error(0)
}

// ── Timers ────────────────────────────────────────────────────────────────────

func (m *MockStore) CreateTimer(ctx context.Context, instanceID uuid.UUID, fireAt time.Time, timerType string, payload map[string]interface{}) (uuid.UUID, error) {
	args := m.Called(ctx, instanceID, fireAt, timerType, payload)
	return args.Get(0).(uuid.UUID), args.Error(1)
}

func (m *MockStore) GetPendingTimers(ctx context.Context, before time.Time) ([]store.Timer, error) {
	args := m.Called(ctx, before)
	return args.Get(0).([]store.Timer), args.Error(1)
}

func (m *MockStore) MarkTimerFired(ctx context.Context, timerID uuid.UUID) error {
	return m.Called(ctx, timerID).Error(0)
}

// ── Tasks ─────────────────────────────────────────────────────────────────────

func (m *MockStore) CreateTask(ctx context.Context, task *store.Task) error {
	return m.Called(ctx, task).Error(0)
}

func (m *MockStore) GetTask(ctx context.Context, taskID uuid.UUID) (*store.Task, error) {
	args := m.Called(ctx, taskID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*store.Task), args.Error(1)
}

func (m *MockStore) GetPendingTasks(ctx context.Context, capabilities []string, limit int) ([]store.Task, error) {
	args := m.Called(ctx, capabilities, limit)
	return args.Get(0).([]store.Task), args.Error(1)
}

func (m *MockStore) ListTasksForInstance(ctx context.Context, instanceID uuid.UUID, statusFilter string, limit, offset int) ([]store.Task, error) {
	args := m.Called(ctx, instanceID, statusFilter, limit, offset)
	return args.Get(0).([]store.Task), args.Error(1)
}

func (m *MockStore) ListTasksForAgent(ctx context.Context, agentID, tenant, statusFilter string) ([]store.Task, error) {
	args := m.Called(ctx, agentID, tenant, statusFilter)
	return args.Get(0).([]store.Task), args.Error(1)
}

func (m *MockStore) AssignTask(ctx context.Context, taskID uuid.UUID, agentID string, deadline time.Time) error {
	return m.Called(ctx, taskID, agentID, deadline).Error(0)
}

func (m *MockStore) ClaimTask(ctx context.Context, taskID uuid.UUID, agentID string, deadline time.Time) error {
	return m.Called(ctx, taskID, agentID, deadline).Error(0)
}

func (m *MockStore) ReleaseTask(ctx context.Context, taskID uuid.UUID, agentID string) error {
	return m.Called(ctx, taskID, agentID).Error(0)
}

func (m *MockStore) CompleteTask(ctx context.Context, taskID uuid.UUID, result map[string]interface{}) error {
	return m.Called(ctx, taskID, result).Error(0)
}

func (m *MockStore) CompleteTaskWithEvidence(ctx context.Context, taskID uuid.UUID, result map[string]interface{}, evidence map[string]interface{}) error {
	return m.Called(ctx, taskID, result, evidence).Error(0)
}

func (m *MockStore) FailTask(ctx context.Context, taskID uuid.UUID, errorDetails map[string]interface{}) error {
	return m.Called(ctx, taskID, errorDetails).Error(0)
}

func (m *MockStore) ExtendTaskDeadline(ctx context.Context, taskID uuid.UUID, newDeadline time.Time) error {
	return m.Called(ctx, taskID, newDeadline).Error(0)
}

func (m *MockStore) ReassignTask(ctx context.Context, taskID uuid.UUID, fromAgentID, toAgentID string) error {
	return m.Called(ctx, taskID, fromAgentID, toAgentID).Error(0)
}

func (m *MockStore) CountPendingTasks(ctx context.Context, instanceID uuid.UUID) (int, error) {
	args := m.Called(ctx, instanceID)
	return args.Int(0), args.Error(1)
}

// ── Task audit log ────────────────────────────────────────────────────────────

func (m *MockStore) AppendTaskAudit(ctx context.Context, entry *store.TaskAuditEntry) error {
	return m.Called(ctx, entry).Error(0)
}

func (m *MockStore) GetTaskAuditLog(ctx context.Context, taskID uuid.UUID) ([]store.TaskAuditEntry, error) {
	args := m.Called(ctx, taskID)
	return args.Get(0).([]store.TaskAuditEntry), args.Error(1)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestEngine_StartWorkflow(t *testing.T) {
	mockStore := new(MockStore)
	registry := runtime.NewDefinitionRegistry(nil)
	engine := runtime.NewEngine(mockStore, registry)

	def := &model.WorkflowDefinition{
		ID:    "test-workflow",
		Name:  "Test Workflow",
		Start: "start",
		States: []model.State{
			&model.OperationState{
				BaseState: model.BaseState{Name: "start", Type: "operation", EndFlag: true},
				Actions:   []model.Action{},
			},
		},
	}

	mockStore.On("CreateWorkflowInstance", mock.Anything, mock.MatchedBy(func(i *store.WorkflowInstance) bool {
		return i.WorkflowDefinitionID == def.ID && i.Tenant == "acme"
	})).Return(nil).Run(func(args mock.Arguments) {
		inst := args.Get(1).(*store.WorkflowInstance)
		inst.ID = uuid.New()
	})

	mockStore.On("AppendEvents", mock.Anything, mock.Anything, int64(0), mock.Anything).Return(nil)

	id, err := engine.StartWorkflow(context.Background(), def, "acme", map[string]interface{}{"key": "value"})
	assert.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, id)
	mockStore.AssertExpectations(t)
}

func TestEngine_SubmitTaskResult_TasksPending(t *testing.T) {
	mockStore := new(MockStore)
	registry := runtime.NewDefinitionRegistry(nil)
	engine := runtime.NewEngine(mockStore, registry)

	taskID := uuid.New()
	instanceID := uuid.New()
	task := &store.Task{
		ID:                 taskID,
		WorkflowInstanceID: instanceID,
		StateName:          "review",
		Status:             "ASSIGNED",
	}

	mockStore.On("GetTask", mock.Anything, taskID).Return(task, nil)
	mockStore.On("CompleteTaskWithEvidence", mock.Anything, taskID, mock.Anything, mock.Anything).Return(nil)
	mockStore.On("CountPendingTasks", mock.Anything, instanceID).Return(2, nil) // still 2 pending

	effect, err := engine.SubmitTaskResult(context.Background(), taskID, map[string]interface{}{"ok": true}, nil)
	assert.NoError(t, err)
	assert.Equal(t, runtime.EffectTasksPending, effect)
	mockStore.AssertExpectations(t)
}
