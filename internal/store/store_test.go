package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enriquepascalin/awm-orchestrator/internal/store"
)

func TestPostgresStore_CreateWorkflowInstance(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	sqlxDB := sqlx.NewDb(db, "postgres")
	s := store.NewPostgresStore(sqlxDB)

	instanceID := uuid.New()
	now := time.Now().UTC()
	instance := &store.WorkflowInstance{
		ID:                   instanceID,
		WorkflowDefinitionID: "test-workflow",
		Tenant:               "acme",
		Status:               "RUNNING",
		CurrentPhase:         "start",
		DimensionalState:     map[string]interface{}{"foo": "bar"},
		Version:              0,
		CreatedAt:            now,
		UpdatedAt:            now,
	}

	mock.ExpectExec(`INSERT INTO workflow_instances`).
		WithArgs(
			instanceID,
			"test-workflow",
			"acme",
			"RUNNING",
			"start",
			sqlmock.AnyArg(),
			int64(0),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(1, 1))

	err = s.CreateWorkflowInstance(context.Background(), instance)
	assert.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestPostgresStore_AppendEvents_ConcurrencyConflict(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	sqlxDB := sqlx.NewDb(db, "postgres")
	s := store.NewPostgresStore(sqlxDB)

	instanceID := uuid.New()
	events := []store.HistoryEvent{
		{
			SequenceNum: 2,
			EventType:   "TaskScheduled",
			Payload:     map[string]interface{}{"task_id": uuid.New().String()},
			RecordedAt:  time.Now(),
		},
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE workflow_instances`).
		WithArgs(instanceID, int64(0)).
		WillReturnRows(sqlmock.NewRows([]string{"version"})) // empty result = no matching row
	mock.ExpectRollback()

	err = s.AppendEvents(context.Background(), instanceID, 0, events)
	assert.ErrorIs(t, err, store.ErrConcurrencyConflict)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestPostgresStore_GetTask(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	sqlxDB := sqlx.NewDb(db, "postgres")
	s := store.NewPostgresStore(sqlxDB)

	taskID := uuid.New()
	workflowID := uuid.New()
	expectedTask := &store.Task{
		ID:                 taskID,
		WorkflowInstanceID: workflowID,
		ActivityName:       "test_activity",
		Capabilities:       []string{"test"},
		Input:              map[string]interface{}{"key": "value"},
		Status:             "PENDING",
	}

	rows := sqlmock.NewRows([]string{
		"id", "workflow_instance_id", "activity_name", "capabilities",
		"input", "status", "assigned_agent_id", "deadline",
	}).AddRow(
		taskID, workflowID, "test_activity", "{test}",
		`{"key":"value"}`, "PENDING", nil, nil,
	)

	mock.ExpectQuery(`SELECT id, workflow_instance_id, activity_name, capabilities, input, status, assigned_agent_id, deadline FROM tasks WHERE id = \$1`).
		WithArgs(taskID).
		WillReturnRows(rows)

	task, err := s.GetTask(context.Background(), taskID)
	assert.NoError(t, err)
	assert.Equal(t, expectedTask.ID, task.ID)
	assert.Equal(t, expectedTask.ActivityName, task.ActivityName)
	assert.NoError(t, mock.ExpectationsWereMet())
}