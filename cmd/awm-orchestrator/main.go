package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"github.com/enriquepascalin/awm-orchestrator/internal/api"
	awmv1 "github.com/enriquepascalin/awm-orchestrator/internal/proto/awm/v1"
	"github.com/enriquepascalin/awm-orchestrator/internal/runtime"
	"github.com/enriquepascalin/awm-orchestrator/internal/store"
	"github.com/enriquepascalin/awm-orchestrator/internal/supervisor"
)

func main() {
	dsn := os.Getenv("AWM_DB_DSN")
	if dsn == "" {
		dsn = "postgres://awm:awm_dev@localhost:5433/awm_meta?sslmode=disable"
	}

	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()
	log.Println("Connected to database")

	st := store.NewPostgresStore(db)
	definitionRegistry := runtime.NewDefinitionRegistry(db)
	engine := runtime.NewEngine(st, definitionRegistry)

	workerBinary := os.Getenv("AWM_WORKER_BINARY")
	if workerBinary == "" {
		workerBinary = "./bin/awm-worker"
	}
	sup := supervisor.NewSupervisor(workerBinary)

	// Recover active workflows from DB on startup
	instances, err := st.ListActiveInstances(context.Background())
	if err != nil {
		log.Printf("Warning: failed to list active instances: %v", err)
	} else {
		for _, inst := range instances {
			if err := sup.StartWorker(inst.ID, inst.WorkflowDefinitionID, inst.Tenant); err != nil {
				log.Printf("Failed to restart worker for %s: %v", inst.ID, err)
			}
		}
	}

	grpcServer := grpc.NewServer()
	orchestratorServer := api.NewOrchestratorServer(engine, sup)
	awmv1.RegisterOrchestratorServer(grpcServer, orchestratorServer)
	publicServer := api.NewPublicServer(engine, definitionRegistry, sup, st)
	awmv1.RegisterPublicServer(grpcServer, publicServer)
	reflection.Register(grpcServer)

	lis, err := net.Listen("tcp", ":9091")
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("Shutting down gracefully...")
		sup.StopAll()
		grpcServer.GracefulStop()
		time.Sleep(2 * time.Second)
		os.Exit(0)
	}()

	log.Println("Orchestrator gRPC server listening on :9091")
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}