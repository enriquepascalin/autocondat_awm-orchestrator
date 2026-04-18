//go:build integration

package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enriquepascalin/awm-orchestrator/internal/store"
)

// newInstance is a convenience builder for a minimal WorkflowInstance.
func newInstance(definitionID, tenant, phase string) *store.WorkflowInstance {
	return &store.WorkflowInstance{
		ID:                   uuid.New(),
		WorkflowDefinitionID: definitionID,
		Tenant:               tenant,
		Status:               "RUNNING",
		CurrentPhase:         phase,
		DimensionalState:     map[string]interface{}{},
		Version:              0,
		CreatedAt:            time.Now().UTC(),
		UpdatedAt:            time.Now().UTC(),
	}
}

// newTask builds a minimal Task for the given instance and state.
func newTask(instanceID uuid.UUID, stateName, activity string, caps []string) *store.Task {
	return &store.Task{
		ID:                 uuid.New(),
		WorkflowInstanceID: instanceID,
		StateName:          stateName,
		ActivityName:       activity,
		Capabilities:       pq.StringArray(caps),
		Roles:              pq.StringArray{},
		Input:              map[string]interface{}{"created": true},
		Status:             "PENDING",
	}
}

// ── Workflow instance lifecycle ───────────────────────────────────────────────

func TestIntegration_CreateAndLoadWorkflowInstance(t *testing.T) {
	truncateAll(t)
	s := store.NewPostgresStore(integrationDB)
	ctx := context.Background()

	inst := newInstance("wf-create-load", "acme", "start")
	require.NoError(t, s.CreateWorkflowInstance(ctx, inst))

	events := []store.HistoryEvent{
		{SequenceNum: 1, EventType: "WorkflowStarted", Payload: map[string]interface{}{"input": "hello"}, RecordedAt: time.Now().UTC()},
	}
	require.NoError(t, s.AppendEvents(ctx, inst.ID, 0, events))

	loaded, history, err := s.LoadInstance(ctx, inst.ID)
	require.NoError(t, err)

	assert.Equal(t, inst.ID, loaded.ID)
	assert.Equal(t, "RUNNING", loaded.Status)
	assert.Equal(t, int64(1), loaded.Version)
	require.Len(t, history, 1)
	assert.Equal(t, "WorkflowStarted", history[0].EventType)
}

func TestIntegration_AppendEvents_ConcurrencyConflict(t *testing.T) {
	truncateAll(t)
	s := store.NewPostgresStore(integrationDB)
	ctx := context.Background()

	inst := newInstance("wf-conflict", "acme", "start")
	require.NoError(t, s.CreateWorkflowInstance(ctx, inst))

	ev := []store.HistoryEvent{{SequenceNum: 1, EventType: "E1", Payload: map[string]interface{}{}, RecordedAt: time.Now()}}
	require.NoError(t, s.AppendEvents(ctx, inst.ID, 0, ev)) // version advances to 1

	// Re-using expectedVersion=0 on a version-1 instance must conflict.
	err := s.AppendEvents(ctx, inst.ID, 0, ev)
	assert.ErrorIs(t, err, store.ErrConcurrencyConflict)
}

func TestIntegration_UpdateWorkflowStatus(t *testing.T) {
	truncateAll(t)
	s := store.NewPostgresStore(integrationDB)
	ctx := context.Background()

	inst := newInstance("wf-status", "acme", "step1")
	require.NoError(t, s.CreateWorkflowInstance(ctx, inst))
	require.NoError(t, s.UpdateWorkflowStatus(ctx, inst.ID, "COMPLETED"))

	loaded, _, err := s.LoadInstance(ctx, inst.ID)
	require.NoError(t, err)
	assert.Equal(t, "COMPLETED", loaded.Status)
}

func TestIntegration_ListWorkflowInstances(t *testing.T) {
	truncateAll(t)
	s := store.NewPostgresStore(integrationDB)
	ctx := context.Background()

	inst1 := newInstance("wf-list", "acme", "start")
	inst2 := newInstance("wf-list", "acme", "start")
	require.NoError(t, s.CreateWorkflowInstance(ctx, inst1))
	require.NoError(t, s.CreateWorkflowInstance(ctx, inst2))
	require.NoError(t, s.UpdateWorkflowStatus(ctx, inst2.ID, "COMPLETED"))

	running, err := s.ListWorkflowInstances(ctx, store.ListInstancesFilter{Status: "RUNNING"})
	require.NoError(t, err)
	assert.Len(t, running, 1)
	assert.Equal(t, inst1.ID, running[0].ID)

	all, err := s.ListWorkflowInstances(ctx, store.ListInstancesFilter{})
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

// ── Task lifecycle ────────────────────────────────────────────────────────────

func TestIntegration_TaskLifecycle_ClaimAndComplete(t *testing.T) {
	truncateAll(t)
	s := store.NewPostgresStore(integrationDB)
	ctx := context.Background()

	inst := newInstance("wf-task", "acme", "review")
	require.NoError(t, s.CreateWorkflowInstance(ctx, inst))

	task := newTask(inst.ID, "review", "human_review", []string{"review"})
	require.NoError(t, s.CreateTask(ctx, task))

	// Claim
	deadline := time.Now().Add(10 * time.Minute)
	require.NoError(t, s.ClaimTask(ctx, task.ID, "agent-1", deadline))

	fetched, err := s.GetTask(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, "ASSIGNED", fetched.Status)
	require.NotNil(t, fetched.AssignedAgentID)
	assert.Equal(t, "agent-1", *fetched.AssignedAgentID)

	// Complete with evidence
	result := map[string]interface{}{"approved": true}
	evidence := map[string]interface{}{"summary": "looks good", "agent_id": "agent-1"}
	require.NoError(t, s.CompleteTaskWithEvidence(ctx, task.ID, result, evidence))

	completed, err := s.GetTask(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, "COMPLETED", completed.Status)
	assert.Equal(t, true, completed.Output["approved"])
	assert.Equal(t, "looks good", completed.Evidence["summary"])
}

func TestIntegration_TaskLifecycle_ClaimAndRelease(t *testing.T) {
	truncateAll(t)
	s := store.NewPostgresStore(integrationDB)
	ctx := context.Background()

	inst := newInstance("wf-release", "acme", "step1")
	require.NoError(t, s.CreateWorkflowInstance(ctx, inst))

	task := newTask(inst.ID, "step1", "act", []string{})
	require.NoError(t, s.CreateTask(ctx, task))

	require.NoError(t, s.ClaimTask(ctx, task.ID, "agent-1", time.Now().Add(5*time.Minute)))
	require.NoError(t, s.ReleaseTask(ctx, task.ID, "agent-1"))

	released, err := s.GetTask(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, "PENDING", released.Status)
	assert.Nil(t, released.AssignedAgentID)
}

func TestIntegration_TaskLifecycle_Fail(t *testing.T) {
	truncateAll(t)
	s := store.NewPostgresStore(integrationDB)
	ctx := context.Background()

	inst := newInstance("wf-fail", "acme", "step1")
	require.NoError(t, s.CreateWorkflowInstance(ctx, inst))

	task := newTask(inst.ID, "step1", "risky_act", []string{})
	require.NoError(t, s.CreateTask(ctx, task))
	require.NoError(t, s.ClaimTask(ctx, task.ID, "agent-1", time.Now().Add(5*time.Minute)))
	require.NoError(t, s.FailTask(ctx, task.ID, map[string]interface{}{"code": "ERR_TIMEOUT", "message": "timed out"}))

	failed, err := s.GetTask(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, "FAILED", failed.Status)
}

func TestIntegration_TaskLifecycle_Reassign(t *testing.T) {
	truncateAll(t)
	s := store.NewPostgresStore(integrationDB)
	ctx := context.Background()

	inst := newInstance("wf-reassign", "acme", "step1")
	require.NoError(t, s.CreateWorkflowInstance(ctx, inst))

	task := newTask(inst.ID, "step1", "act", []string{})
	require.NoError(t, s.CreateTask(ctx, task))
	require.NoError(t, s.ClaimTask(ctx, task.ID, "agent-1", time.Now().Add(5*time.Minute)))
	require.NoError(t, s.ReassignTask(ctx, task.ID, "agent-1", "agent-2"))

	reassigned, err := s.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, reassigned.AssignedAgentID)
	assert.Equal(t, "agent-2", *reassigned.AssignedAgentID)
}

// ── CountPendingTasks ─────────────────────────────────────────────────────────

func TestIntegration_CountPendingTasks(t *testing.T) {
	truncateAll(t)
	s := store.NewPostgresStore(integrationDB)
	ctx := context.Background()

	inst := newInstance("wf-count", "acme", "parallel")
	require.NoError(t, s.CreateWorkflowInstance(ctx, inst))

	for i := 0; i < 3; i++ {
		require.NoError(t, s.CreateTask(ctx, newTask(inst.ID, "parallel", "act", []string{})))
	}

	count, err := s.CountPendingTasks(ctx, inst.ID)
	require.NoError(t, err)
	assert.Equal(t, 3, count)

	// Completing one task should reduce the count.
	tasks, err := s.ListTasksForInstance(ctx, inst.ID, "PENDING", 10, 0)
	require.NoError(t, err)
	require.NoError(t, s.ClaimTask(ctx, tasks[0].ID, "agent-1", time.Now().Add(5*time.Minute)))
	require.NoError(t, s.CompleteTask(ctx, tasks[0].ID, map[string]interface{}{"done": true}))

	count2, err := s.CountPendingTasks(ctx, inst.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, count2)
}

// ── GetPendingTasks with capabilities ────────────────────────────────────────

func TestIntegration_GetPendingTasks_ByCapability(t *testing.T) {
	truncateAll(t)
	s := store.NewPostgresStore(integrationDB)
	ctx := context.Background()

	inst := newInstance("wf-caps", "acme", "s1")
	require.NoError(t, s.CreateWorkflowInstance(ctx, inst))

	require.NoError(t, s.CreateTask(ctx, newTask(inst.ID, "s1", "legal_review", []string{"legal", "review"})))
	require.NoError(t, s.CreateTask(ctx, newTask(inst.ID, "s1", "data_entry", []string{"data-entry"})))

	legal, err := s.GetPendingTasks(ctx, []string{"legal"}, 10)
	require.NoError(t, err)
	require.Len(t, legal, 1)
	assert.Equal(t, "legal_review", legal[0].ActivityName)

	all, err := s.GetPendingTasks(ctx, []string{}, 10)
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

// ── Task audit log ────────────────────────────────────────────────────────────

func TestIntegration_TaskAuditLog(t *testing.T) {
	truncateAll(t)
	s := store.NewPostgresStore(integrationDB)
	ctx := context.Background()

	inst := newInstance("wf-audit", "acme", "s1")
	require.NoError(t, s.CreateWorkflowInstance(ctx, inst))

	task := newTask(inst.ID, "s1", "act", []string{})
	require.NoError(t, s.CreateTask(ctx, task))

	require.NoError(t, s.AppendTaskAudit(ctx, &store.TaskAuditEntry{
		TaskID:    task.ID,
		EventType: "CLAIMED",
		AgentID:   "agent-1",
		Message:   "task claimed",
	}))
	require.NoError(t, s.AppendTaskAudit(ctx, &store.TaskAuditEntry{
		TaskID:    task.ID,
		EventType: "COMPLETED",
		AgentID:   "agent-1",
		Message:   "work done",
		Data:      map[string]interface{}{"score": 99},
	}))

	entries, err := s.GetTaskAuditLog(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "CLAIMED", entries[0].EventType)
	assert.Equal(t, "COMPLETED", entries[1].EventType)
	assert.Equal(t, float64(99), entries[1].Data["score"])
}

// ── Timers ────────────────────────────────────────────────────────────────────

func TestIntegration_Timer_CreateFireAndMark(t *testing.T) {
	truncateAll(t)
	s := store.NewPostgresStore(integrationDB)
	ctx := context.Background()

	inst := newInstance("wf-timer", "acme", "wait")
	require.NoError(t, s.CreateWorkflowInstance(ctx, inst))

	fireAt := time.Now().Add(-1 * time.Second) // already past
	timerID, err := s.CreateTimer(ctx, inst.ID, fireAt, "TIMEOUT", map[string]interface{}{"state": "wait"})
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, timerID)

	pending, err := s.GetPendingTimers(ctx, time.Now().Add(10*time.Second))
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, timerID, pending[0].ID)
	assert.Equal(t, "TIMEOUT", pending[0].TimerType)

	require.NoError(t, s.MarkTimerFired(ctx, timerID))

	afterFire, err := s.GetPendingTimers(ctx, time.Now().Add(10*time.Second))
	require.NoError(t, err)
	assert.Len(t, afterFire, 0)
}

// ── ExtendTaskDeadline ────────────────────────────────────────────────────────

func TestIntegration_ExtendTaskDeadline(t *testing.T) {
	truncateAll(t)
	s := store.NewPostgresStore(integrationDB)
	ctx := context.Background()

	inst := newInstance("wf-extend", "acme", "s1")
	require.NoError(t, s.CreateWorkflowInstance(ctx, inst))

	task := newTask(inst.ID, "s1", "act", []string{})
	require.NoError(t, s.CreateTask(ctx, task))

	original := time.Now().Add(5 * time.Minute)
	require.NoError(t, s.ClaimTask(ctx, task.ID, "agent-1", original))

	extended := time.Now().Add(30 * time.Minute)
	require.NoError(t, s.ExtendTaskDeadline(ctx, task.ID, extended))

	fetched, err := s.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, fetched.Deadline)
	assert.True(t, fetched.Deadline.After(original), "extended deadline should be later than original")
}
