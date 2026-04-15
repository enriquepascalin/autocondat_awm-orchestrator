package supervisor_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enriquepascalin/awm-orchestrator/internal/supervisor"
)

func TestSupervisor_StartWorker_DuplicateFails(t *testing.T) {
	sup := supervisor.NewSupervisor("/bin/true")

	instanceID := uuid.New()

	// First start should succeed
	err := sup.StartWorker(instanceID, "test-workflow", "acme")
	require.NoError(t, err)

	// Second start should fail because worker is already running
	err = sup.StartWorker(instanceID, "test-workflow", "acme")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	// Clean up
	err = sup.StopWorker(instanceID)
	assert.NoError(t, err)
}