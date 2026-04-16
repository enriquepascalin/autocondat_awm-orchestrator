package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rabbitmq/amqp091-go"

	"github.com/enriquepascalin/awm-orchestrator/internal/model"
	"github.com/enriquepascalin/awm-orchestrator/internal/store"
)

// Engine executes workflow instances and coordinates tasks.
type Engine struct {
	store    store.Store
	registry *DefinitionRegistry
	rabbitCh *amqp091.Channel // optional; if set, tasks are published to RabbitMQ
	mu       sync.Mutex
	wg       sync.WaitGroup
}

// NewEngine creates a new workflow engine.
func NewEngine(s store.Store, r *DefinitionRegistry) *Engine {
	return &Engine{store: s, registry: r}
}

// SetRabbitMQChannel configures the engine to publish tasks to RabbitMQ.
// If not set, tasks are only stored in the database.
func (e *Engine) SetRabbitMQChannel(ch *amqp091.Channel) {
	e.rabbitCh = ch
}

// StartWorkflow creates a new workflow instance, persists the initial event, and
// returns the instance ID. Execution is owned entirely by the worker subprocess;
// this method does NOT start a goroutine.
func (e *Engine) StartWorkflow(ctx context.Context, def *model.WorkflowDefinition, tenant string, input map[string]interface{}) (uuid.UUID, error) {
	instanceID := uuid.New()
	now := time.Now()
	instance := &store.WorkflowInstance{
		ID:                   instanceID,
		WorkflowDefinitionID: def.ID,
		Tenant:               tenant,
		Status:               "RUNNING",
		CurrentPhase:         def.Start,
		DimensionalState:     input,
		Version:              0,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if err := e.store.CreateWorkflowInstance(ctx, instance); err != nil {
		return uuid.Nil, fmt.Errorf("create instance: %w", err)
	}
	event := store.HistoryEvent{
		SequenceNum: 1,
		EventType:   "WorkflowStarted",
		Payload: map[string]interface{}{
			"definition_id": def.ID,
			"tenant":        tenant,
			"input":         input,
		},
		RecordedAt: now,
	}
	if err := e.store.AppendEvents(ctx, instanceID, 0, []store.HistoryEvent{event}); err != nil {
		return uuid.Nil, fmt.Errorf("append start event: %w", err)
	}
	return instanceID, nil
}

// ResumeWorkflow continues execution of a workflow from its current state.
// Called by the worker subprocess on startup, and after timers fire.
func (e *Engine) ResumeWorkflow(ctx context.Context, instanceID uuid.UUID, def *model.WorkflowDefinition) error {
	e.executeWorkflow(ctx, instanceID, def)
	return nil
}

// executeWorkflow runs the state machine until completion or blocking on tasks/timers.
func (e *Engine) executeWorkflow(ctx context.Context, instanceID uuid.UUID, def *model.WorkflowDefinition) {
	log.Printf("Executing workflow %s", instanceID)
	instance, events, err := e.store.LoadInstance(ctx, instanceID)
	if err != nil {
		log.Printf("Failed to load instance %s: %v", instanceID, err)
		return
	}
	currentStateName := instance.CurrentPhase
	if currentStateName == "" {
		e.store.UpdateWorkflowStatus(ctx, instanceID, "FAILED")
		return
	}
	state := e.findState(def, currentStateName)
	if state == nil {
		log.Printf("State %q not found in definition %s", currentStateName, def.ID)
		e.store.UpdateWorkflowStatus(ctx, instanceID, "FAILED")
		return
	}
	e.processState(ctx, instance, events, state, def)
}

// processState dispatches based on the state type.
func (e *Engine) processState(ctx context.Context, instance *store.WorkflowInstance, events []store.HistoryEvent, state model.State, def *model.WorkflowDefinition) {
	switch s := state.(type) {
	case *model.OperationState:
		e.processOperationState(ctx, instance, events, s, def)
	case *model.DelayState:
		e.processDelayState(ctx, instance, events, s, def)
	default:
		e.store.UpdateWorkflowStatus(ctx, instance.ID, "FAILED")
	}
}

// processOperationState creates tasks for each action and, if there are no actions,
// advances immediately (handles end-states with empty action lists).
func (e *Engine) processOperationState(ctx context.Context, instance *store.WorkflowInstance, events []store.HistoryEvent, state *model.OperationState, def *model.WorkflowDefinition) {
	for i, action := range state.Actions {
		taskID := uuid.New()
		task := &store.Task{
			ID:                 taskID,
			WorkflowInstanceID: instance.ID,
			StateName:          state.Name,
			ActivityName:       action.Name,
			Capabilities:       action.Capabilities,
			Roles:              action.Roles,
			Input:              action.Arguments,
			Status:             "PENDING",
		}
		if len(task.Capabilities) == 0 {
			task.Capabilities = []string{}
		}
		if len(task.Roles) == 0 {
			task.Roles = []string{}
		}
		if err := e.store.CreateTask(ctx, task); err != nil {
			log.Printf("Failed to create task: %v", err)
			e.store.UpdateWorkflowStatus(ctx, instance.ID, "FAILED")
			return
		}
		seq := int64(len(events) + i + 1)
		event := store.HistoryEvent{
			SequenceNum: seq,
			EventType:   "TaskScheduled",
			Payload: map[string]interface{}{
				"task_id":      taskID.String(),
				"activity":     action.Name,
				"capabilities": action.Capabilities,
				"roles":        action.Roles,
				"state":        state.Name,
			},
			RecordedAt: time.Now(),
		}
		e.store.AppendEvents(ctx, instance.ID, instance.Version, []store.HistoryEvent{event})

		if e.rabbitCh != nil {
			e.publishTask(task)
		}
	}

	// If this state has no actions, advance immediately — no tasks to wait for.
	if len(state.Actions) == 0 {
		if err := e.advanceFromState(ctx, instance, events, state, def); err != nil {
			log.Printf("Failed to advance from state %s: %v", state.Name, err)
		}
	}
}

// advanceFromState either marks the workflow COMPLETED (if end state) or
// transitions to the next state and continues execution.
func (e *Engine) advanceFromState(ctx context.Context, instance *store.WorkflowInstance, events []store.HistoryEvent, state model.State, def *model.WorkflowDefinition) error {
	if state.End() {
		completedEvent := store.HistoryEvent{
			SequenceNum: int64(len(events) + 1),
			EventType:   "WorkflowCompleted",
			Payload:     map[string]interface{}{"state": state.GetName()},
			RecordedAt:  time.Now(),
		}
		// Non-critical: log but don't fail the completion if the event append has a conflict.
		if err := e.store.AppendEvents(ctx, instance.ID, instance.Version, []store.HistoryEvent{completedEvent}); err != nil {
			log.Printf("Warning: could not append WorkflowCompleted event for %s: %v", instance.ID, err)
		}
		if err := e.store.UpdateWorkflowStatus(ctx, instance.ID, "COMPLETED"); err != nil {
			return fmt.Errorf("mark COMPLETED: %w", err)
		}
		log.Printf("Workflow %s COMPLETED at state %s", instance.ID, state.GetName())
		return nil
	}

	nextStateName := state.GetTransition()
	if nextStateName == "" {
		log.Printf("State %s has no transition and is not an end state — marking FAILED", state.GetName())
		return e.store.UpdateWorkflowStatus(ctx, instance.ID, "FAILED")
	}

	transitionEvent := store.HistoryEvent{
		SequenceNum: int64(len(events) + 1),
		EventType:   "StateTransitioned",
		Payload:     map[string]interface{}{"from": state.GetName(), "to": nextStateName},
		RecordedAt:  time.Now(),
	}
	if err := e.store.AppendEvents(ctx, instance.ID, instance.Version, []store.HistoryEvent{transitionEvent}); err != nil {
		log.Printf("Warning: could not append StateTransitioned event for %s: %v", instance.ID, err)
	}
	if err := e.store.UpdateCurrentPhase(ctx, instance.ID, nextStateName); err != nil {
		return fmt.Errorf("update current phase: %w", err)
	}
	log.Printf("Workflow %s transitioned from %s → %s", instance.ID, state.GetName(), nextStateName)

	// Continue execution in this goroutine (already running in background context).
	e.executeWorkflow(ctx, instance.ID, def)
	return nil
}

// publishTask sends a task creation message to RabbitMQ.
func (e *Engine) publishTask(task *store.Task) {
	msg := map[string]interface{}{
		"task_id":      task.ID.String(),
		"workflow_id":  task.WorkflowInstanceID.String(),
		"activity":     task.ActivityName,
		"capabilities": task.Capabilities,
		"roles":        task.Roles,
		"input":        task.Input,
	}
	body, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Failed to marshal task message: %v", err)
		return
	}

	routingKey := "task.generic"
	if len(task.Capabilities) > 0 {
		routingKey = "task." + task.Capabilities[0]
	}

	err = e.rabbitCh.Publish(
		"awm.tasks",
		routingKey,
		false,
		false,
		amqp091.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp091.Persistent,
			Body:         body,
		},
	)
	if err != nil {
		log.Printf("Failed to publish task to RabbitMQ: %v", err)
	}
}

// processDelayState schedules a durable timer.
func (e *Engine) processDelayState(ctx context.Context, instance *store.WorkflowInstance, events []store.HistoryEvent, state *model.DelayState, def *model.WorkflowDefinition) {
	duration, err := model.ParseDuration(state.Duration)
	if err != nil {
		e.store.UpdateWorkflowStatus(ctx, instance.ID, "FAILED")
		return
	}
	fireAt := time.Now().Add(duration)
	_, err = e.store.CreateTimer(ctx, instance.ID, fireAt, "delay", map[string]interface{}{
		"next_state": state.Transition,
	})
	if err != nil {
		e.store.UpdateWorkflowStatus(ctx, instance.ID, "FAILED")
		return
	}
	event := store.HistoryEvent{
		SequenceNum: int64(len(events) + 1),
		EventType:   "TimerScheduled",
		Payload: map[string]interface{}{
			"fire_at":    fireAt,
			"next_state": state.Transition,
		},
		RecordedAt: time.Now(),
	}
	e.store.AppendEvents(ctx, instance.ID, instance.Version, []store.HistoryEvent{event})
}

// ResumeAfterTimer is called by the timer service when a timer fires.
func (e *Engine) ResumeAfterTimer(ctx context.Context, timer *store.Timer) error {
	instance, events, err := e.store.LoadInstance(ctx, timer.WorkflowInstanceID)
	if err != nil {
		return err
	}
	def, err := e.registry.Get(ctx, instance.WorkflowDefinitionID)
	if err != nil {
		return err
	}
	nextStateName, _ := timer.Payload["next_state"].(string)
	if nextStateName == "" {
		nextStateName = def.Start
	}

	timerEvent := store.HistoryEvent{
		SequenceNum: int64(len(events) + 1),
		EventType:   "TimerFired",
		Payload:     map[string]interface{}{"timer_id": timer.ID.String(), "next_state": nextStateName},
		RecordedAt:  time.Now(),
	}
	if err := e.store.AppendEvents(ctx, instance.ID, instance.Version, []store.HistoryEvent{timerEvent}); err != nil {
		log.Printf("Warning: could not append TimerFired event for %s: %v", instance.ID, err)
	}
	if err := e.store.UpdateCurrentPhase(ctx, instance.ID, nextStateName); err != nil {
		return err
	}

	// Use Background so timer goroutine outlives the timer-service tick context.
	go e.executeWorkflow(context.Background(), instance.ID, def)
	return nil
}

// CompleteTask marks a task as completed and checks if the workflow can advance.
func (e *Engine) CompleteTask(ctx context.Context, taskID uuid.UUID, result map[string]interface{}) error {
	task, err := e.store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if err := e.store.CompleteTask(ctx, taskID, result); err != nil {
		return err
	}
	// Use Background: gRPC request context may be cancelled before the async advance finishes.
	go func() {
		if err := e.resumeWorkflowAfterTask(context.Background(), task.WorkflowInstanceID); err != nil {
			log.Printf("resumeWorkflowAfterTask error for instance %s: %v", task.WorkflowInstanceID, err)
		}
	}()
	return nil
}

// FailTask marks a task as failed and checks if the workflow can advance.
func (e *Engine) FailTask(ctx context.Context, taskID uuid.UUID, errorDetails interface{}) error {
	task, err := e.store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if err := e.store.FailTask(ctx, taskID, nil); err != nil {
		return err
	}
	go func() {
		if err := e.resumeWorkflowAfterTask(context.Background(), task.WorkflowInstanceID); err != nil {
			log.Printf("resumeWorkflowAfterTask error for instance %s: %v", task.WorkflowInstanceID, err)
		}
	}()
	return nil
}

// resumeWorkflowAfterTask checks whether all tasks for the instance are done,
// and if so, advances to the next state (or marks COMPLETED).
func (e *Engine) resumeWorkflowAfterTask(ctx context.Context, instanceID uuid.UUID) error {
	pending, err := e.store.CountPendingTasks(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("count pending tasks: %w", err)
	}
	if pending > 0 {
		// Still waiting for other tasks in this state.
		return nil
	}

	instance, events, err := e.store.LoadInstance(ctx, instanceID)
	if err != nil {
		return err
	}
	def, err := e.registry.Get(ctx, instance.WorkflowDefinitionID)
	if err != nil {
		return err
	}
	state := e.findState(def, instance.CurrentPhase)
	if state == nil {
		return e.store.UpdateWorkflowStatus(ctx, instanceID, "FAILED")
	}
	return e.advanceFromState(ctx, instance, events, state, def)
}

// ClaimPendingTask assigns a pending task to an agent.
func (e *Engine) ClaimPendingTask(ctx context.Context, capabilities []string, agentID string) (*store.Task, error) {
	tasks, err := e.store.GetPendingTasks(ctx, capabilities, 1)
	if err != nil || len(tasks) == 0 {
		return nil, err
	}
	task := &tasks[0]
	if err := e.store.AssignTask(ctx, task.ID, agentID, time.Now().Add(5*time.Minute)); err != nil {
		return nil, err
	}
	return task, nil
}

// GetWorkflowState returns the current snapshot of a workflow.
func (e *Engine) GetWorkflowState(ctx context.Context, instanceID uuid.UUID) (*store.WorkflowInstance, error) {
	instance, _, err := e.store.LoadInstance(ctx, instanceID)
	return instance, err
}

// RecoverIncompleteWorkflows resumes all workflows that were left in RUNNING state.
// Called on orchestrator startup; each instance gets its own worker subprocess via supervisor.
func (e *Engine) RecoverIncompleteWorkflows(ctx context.Context) error {
	instances, err := e.store.ListActiveInstances(ctx)
	if err != nil {
		return err
	}
	for _, inst := range instances {
		def, err := e.registry.Get(ctx, inst.WorkflowDefinitionID)
		if err != nil {
			log.Printf("Failed to load definition for instance %s: %v", inst.ID, err)
			continue
		}
		go e.ResumeWorkflow(ctx, inst.ID, def)
	}
	return nil
}

// findState locates a state by name within the workflow definition.
func (e *Engine) findState(def *model.WorkflowDefinition, name string) model.State {
	for _, s := range def.States {
		if s.GetName() == name {
			return s
		}
	}
	return nil
}
