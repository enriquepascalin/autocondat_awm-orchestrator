package supervisor_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enriquepascalin/awm-orchestrator/internal/supervisor"
)

func TestSupervisor_StartWorker_DuplicateFails(t *testing.T) {
	sup := supervisor.NewSupervisor("/bin/true")

	instanceID := uuid.New()
	err := sup.StartWorker(instanceID, "test-workflow", "acme")
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	// Starting a second worker for the same instance should fail
	err = sup.StartWorker(instanceID, "test-workflow", "acme")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestSupervisor_StartAndStopWorker(t *testing.T) {
	sup := supervisor.NewSupervisor("/bin/sleep")

	instanceID := uuid.New()
	err := sup.StartWorker(instanceID, "test-workflow", "acme")
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	err = sup.StopWorker(instanceID)
	assert.NoError(t, err)
}