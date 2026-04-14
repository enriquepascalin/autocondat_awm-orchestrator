package main

import (
	"log"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"github.com/enriquepascalin/awm-orchestrator/internal/proto/awm/v1"
)

type orchestratorServer struct {
	awmv1.UnimplementedOrchestratorServer
}

func main() {
	lis, err := net.Listen("tcp", ":9091")
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	s := grpc.NewServer()
	awmv1.RegisterOrchestratorServer(s, &orchestratorServer{})
	reflection.Register(s) // optional, helps with debugging

	log.Println("Orchestrator gRPC server listening on :9091")
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}