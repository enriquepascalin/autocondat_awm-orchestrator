package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/rabbitmq/amqp091-go"

	"github.com/enriquepascalin/awm-orchestrator/internal/model"
	"github.com/enriquepascalin/awm-orchestrator/internal/runtime"
	"github.com/enriquepascalin/awm-orchestrator/internal/store"
)

// Worker encapsulates the execution environment for a single workflow instance.
type Worker struct {
	instanceID  uuid.UUID
	workflowDef *model.WorkflowDefinition
	tenant      string
	db          *sqlx.DB
	rabbitConn  *amqp091.Connection
	rabbitChan  *amqp091.Channel
	store       store.Store
	engine      *runtime.Engine
	registry    *runtime.DefinitionRegistry
	taskQueue   string
	shutdownCh  chan struct{}
}

// Config holds the configuration for creating a Worker.
type Config struct {
	InstanceID    string
	WorkflowDefID string
	Tenant        string
	DBDSN         string
	RabbitMQURL   string
}

// New creates a new Worker instance.
func New(cfg Config) (*Worker, error) {
	instanceID, err := uuid.Parse(cfg.InstanceID)
	if err != nil {
		return nil, fmt.Errorf("invalid instance ID: %w", err)
	}

	db, err := sqlx.Connect("postgres", cfg.DBDSN)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	// Load workflow definition from the registry (DB-backed with cache + filesystem fallback).
	registry := runtime.NewDefinitionRegistry(db)
	def, err := registry.Get(context.Background(), cfg.WorkflowDefID)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("load workflow definition %q: %w", cfg.WorkflowDefID, err)
	}

	conn, err := amqp091.Dial(cfg.RabbitMQURL)
	if err != nil {
		return nil, fmt.Errorf("connect to RabbitMQ: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("open RabbitMQ channel: %w", err)
	}

	// Declare the task queue for this workflow instance
	taskQueue := fmt.Sprintf("workflow.%s.tasks", instanceID)
	_, err = ch.QueueDeclare(
		taskQueue,
		true,  // durable
		false, // autoDelete
		false, // exclusive
		false, // noWait
		nil,   // args
	)
	if err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("declare task queue: %w", err)
	}

	// Set QoS to ensure fair dispatch
	err = ch.Qos(1, 0, false)
	if err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("set QoS: %w", err)
	}

	st := store.NewPostgresStore(db)
	engine := runtime.NewEngine(st, registry)

	return &Worker{
		instanceID:  instanceID,
		workflowDef: def,
		tenant:      cfg.Tenant,
		db:          db,
		rabbitConn:  conn,
		rabbitChan:  ch,
		store:       st,
		engine:      engine,
		registry:    registry,
		taskQueue:   taskQueue,
		shutdownCh:  make(chan struct{}),
	}, nil
}

// Start begins the worker's main loops: state machine execution and task consumption.
func (w *Worker) Start(ctx context.Context) error {
	log.Printf("Worker for workflow %s starting (PID %d)", w.instanceID, os.Getpid())

	// Start consuming task completion messages
	msgs, err := w.rabbitChan.Consume(
		w.taskQueue,
		"",    // consumer tag
		false, // autoAck
		false, // exclusive
		false, // noLocal
		false, // noWait
		nil,   // args
	)
	if err != nil {
		return fmt.Errorf("consume from task queue: %w", err)
	}

	// Goroutine to handle incoming task results
	go func() {
		for {
			select {
			case <-w.shutdownCh:
				return
			case d, ok := <-msgs:
				if !ok {
					return
				}
				w.handleTaskMessage(d)
			}
		}
	}()

	// Start the workflow state machine
	go func() {
		if err := w.engine.ResumeWorkflow(ctx, w.instanceID, w.workflowDef); err != nil {
			log.Printf("Workflow execution error: %v", err)
		}
	}()

	// Wait for shutdown signal
	<-w.shutdownCh
	return nil
}

// handleTaskMessage processes a single task completion message from RabbitMQ.
func (w *Worker) handleTaskMessage(d amqp091.Delivery) {
	var taskResult struct {
		TaskID string                 `json:"task_id"`
		Status string                 `json:"status"` // "completed" or "failed"
		Result map[string]interface{} `json:"result,omitempty"`
		Error  map[string]interface{} `json:"error,omitempty"`
	}
	if err := json.Unmarshal(d.Body, &taskResult); err != nil {
		log.Printf("Invalid task message: %v", err)
		d.Nack(false, false)
		return
	}

	taskID, err := uuid.Parse(taskResult.TaskID)
	if err != nil {
		log.Printf("Invalid task ID in message: %s", taskResult.TaskID)
		d.Nack(false, false)
		return
	}

	ctx := context.Background()
	switch taskResult.Status {
	case "completed":
		if err := w.engine.CompleteTask(ctx, taskID, taskResult.Result); err != nil {
			log.Printf("Failed to complete task %s: %v", taskID, err)
			d.Nack(false, true)
			return
		}
	case "failed":
		if err := w.engine.FailTask(ctx, taskID, taskResult.Error); err != nil {
			log.Printf("Failed to mark task %s as failed: %v", taskID, err)
			d.Nack(false, true)
			return
		}
	default:
		log.Printf("Unknown task status: %s", taskResult.Status)
		d.Nack(false, false)
		return
	}

	d.Ack(false)
}

// Stop gracefully shuts down the worker.
func (w *Worker) Stop() {
	log.Printf("Worker for workflow %s shutting down", w.instanceID)
	close(w.shutdownCh)

	if w.rabbitChan != nil {
		w.rabbitChan.Close()
	}
	if w.rabbitConn != nil {
		w.rabbitConn.Close()
	}
	if w.db != nil {
		w.db.Close()
	}
}