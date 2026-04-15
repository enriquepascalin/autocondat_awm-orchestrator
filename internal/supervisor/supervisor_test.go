package supervisor_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enriquepascalin/awm-orchestrator/internal/store"
	"github.com/enriquepascalin/awm-orchestrator/internal/supervisor"
)

type mockStore struct {
	claimFunc   func(ctx context.Context, instanceID uuid.UUID, ownerID string) (bool, error)
	releaseFunc func(ctx context.Context, instanceID uuid.UUID, ownerID string) error
}

func (m *mockStore) ClaimWorkflowInstance(ctx context.Context, instanceID uuid.UUID, ownerID string) (bool, error) {
	return m.claimFunc(ctx, instanceID, ownerID)
}

func (m *mockStore) ReleaseWorkflowInstance(ctx context.Context, instanceID uuid.UUID, ownerID string) error {
	return m.releaseFunc(ctx, instanceID, ownerID)
}

func (m *mockStore) UpdateWorkerLease(ctx context.Context, instanceID uuid.UUID, pid int, ownerID string) error {
	return nil
}

func (m *mockStore) ListActiveInstances(ctx context.Context) ([]store.WorkflowInstance, error) {
	return nil, nil
}

func TestSupervisor_StartWorker_ClaimFails(t *testing.T) {
	st := &mockStore{
		claimFunc: func(ctx context.Context, instanceID uuid.UUID, ownerID string) (bool, error) {
			return false, nil
		},
	}
	sup := supervisor.NewSupervisor("/fake/worker", st)

	err := sup.StartWorker(uuid.New(), "test-workflow", "acme")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already running")
}

func TestSupervisor_RecoverActiveWorkflows(t *testing.T) {
	instanceID := uuid.New()
	st := &mockStore{
		claimFunc: func(ctx context.Context, id uuid.UUID, ownerID string) (bool, error) {
			return true, nil
		},
		releaseFunc: func(ctx context.Context, id uuid.UUID, ownerID string) error {
			return nil
		},
	}
	sup := supervisor.NewSupervisor("/bin/true", st)
	err := sup.StartWorker(instanceID, "test-workflow", "acme")
	require.NoError(t, err)
	// Allow worker to exit
	time.Sleep(100 * time.Millisecond)
	err = sup.StopWorker(instanceID)
	assert.NoError(t, err)
}