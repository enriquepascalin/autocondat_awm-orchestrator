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

type MockStore struct {
	mock.Mock
}

func (m *MockStore) CreateWorkflowInstance(ctx context.Context, instance *store.WorkflowInstance) error {
	args := m.Called(ctx, instance)
	return args.Error(0)
}

func (m *MockStore) AppendEvents(ctx context.Context, instanceID uuid.UUID, expectedVersion int64, events []store.HistoryEvent) error {
	args := m.Called(ctx, instanceID, expectedVersion, events)
	return args.Error(0)
}

func (m *MockStore) LoadInstance(ctx context.Context, instanceID uuid.UUID) (*store.WorkflowInstance, []store.HistoryEvent, error) {
	args := m.Called(ctx, instanceID)
	if args.Get(0) == nil {
		return nil, nil, args.Error(2)
	}
	return args.Get(0).(*store.WorkflowInstance), args.Get(1).([]store.HistoryEvent), args.Error(2)
}

func (m *MockStore) ListActiveInstances(ctx context.Context) ([]store.WorkflowInstance, error) {
	args := m.Called(ctx)
	return args.Get(0).([]store.WorkflowInstance), args.Error(1)
}

func (m *MockStore) UpdateWorkflowStatus(ctx context.Context, instanceID uuid.UUID, status string) error {
	args := m.Called(ctx, instanceID, status)
	return args.Error(0)
}

func (m *MockStore) GetTask(ctx context.Context, taskID uuid.UUID) (*store.Task, error) {
	args := m.Called(ctx, taskID)
	return args.Get(0).(*store.Task), args.Error(1)
}

func (m *MockStore) CompleteTask(ctx context.Context, taskID uuid.UUID, result map[string]interface{}) error {
	args := m.Called(ctx, taskID, result)
	return args.Error(0)
}

// Implement remaining Store interface methods for compilation
func (m *MockStore) AcquireLease(ctx context.Context, instanceID uuid.UUID, ownerID string, duration time.Duration) (bool, error) { return true, nil }
func (m *MockStore) RenewLease(ctx context.Context, instanceID uuid.UUID, ownerID string, duration time.Duration) error { return nil }
func (m *MockStore) CreateTimer(ctx context.Context, instanceID uuid.UUID, fireAt time.Time, timerType string, payload map[string]interface{}) (uuid.UUID, error) { return uuid.New(), nil }
func (m *MockStore) GetPendingTimers(ctx context.Context, before time.Time) ([]store.Timer, error) { return nil, nil }
func (m *MockStore) MarkTimerFired(ctx context.Context, timerID uuid.UUID) error { return nil }
func (m *MockStore) CreateTask(ctx context.Context, task *store.Task) error { return nil }
func (m *MockStore) GetPendingTasks(ctx context.Context, capabilities []string, limit int) ([]store.Task, error) { return nil, nil }
func (m *MockStore) AssignTask(ctx context.Context, taskID uuid.UUID, agentID string, deadline time.Time) error { return nil }
func (m *MockStore) FailTask(ctx context.Context, taskID uuid.UUID, errorDetails map[string]interface{}) error { return nil }

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

	instanceID := uuid.New()

	mockStore.On("CreateWorkflowInstance", mock.Anything, mock.MatchedBy(func(i *store.WorkflowInstance) bool {
		return i.WorkflowDefinitionID == def.ID && i.Tenant == "acme"
	})).Return(nil).Run(func(args mock.Arguments) {
		inst := args.Get(1).(*store.WorkflowInstance)
		inst.ID = instanceID
	})

	mockStore.On("AppendEvents", mock.Anything, instanceID, int64(0), mock.Anything).Return(nil)

	id, err := engine.StartWorkflow(context.Background(), def, "acme", map[string]interface{}{"key": "value"})
	assert.NoError(t, err)
	assert.Equal(t, instanceID, id)
	mockStore.AssertExpectations(t)
}