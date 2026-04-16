package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/enriquepascalin/awm-orchestrator/internal/worker"
)

func main() {
	var (
		instanceID    string
		workflowDefID string
		tenant        string
		dbDSN         string
		rabbitMQURL   string
	)
	flag.StringVar(&instanceID, "instance-id", "", "Workflow instance ID")
	flag.StringVar(&workflowDefID, "workflow-def-id", "", "Workflow definition ID")
	flag.StringVar(&tenant, "tenant", "", "Tenant ID")
	flag.StringVar(&dbDSN, "db-dsn", os.Getenv("AWM_DB_DSN"), "PostgreSQL DSN")
	flag.StringVar(&rabbitMQURL, "rabbitmq-url", os.Getenv("AWM_RABBITMQ_URL"), "RabbitMQ URL")
	flag.Parse()

	if instanceID == "" || workflowDefID == "" || tenant == "" {
		log.Fatal("instance-id, workflow-def-id, and tenant are required")
	}
	if dbDSN == "" {
		dbDSN = "postgres://awm:awm_dev@localhost:5433/awm_meta?sslmode=disable"
	}
	if rabbitMQURL == "" {
		rabbitMQURL = "amqp://awm:awm_dev@localhost:5673/awm"
	}

	cfg := worker.Config{
		InstanceID:    instanceID,
		WorkflowDefID: workflowDefID,
		Tenant:        tenant,
		DBDSN:         dbDSN,
		RabbitMQURL:   rabbitMQURL,
	}

	w, err := worker.New(cfg)
	if err != nil {
		log.Fatalf("Failed to create worker: %v", err)
	}
	defer w.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Worker received shutdown signal")
		cancel()
	}()

	if err := w.Start(ctx); err != nil {
		log.Fatalf("Worker failed: %v", err)
	}
}