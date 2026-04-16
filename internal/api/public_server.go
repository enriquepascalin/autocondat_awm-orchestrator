package api

import (
	"context"
	"database/sql"
	"errors"
	"log"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/enriquepascalin/awm-orchestrator/internal/parser"
	awmv1 "github.com/enriquepascalin/awm-orchestrator/internal/proto/awm/v1"
	"github.com/enriquepascalin/awm-orchestrator/internal/runtime"
	"github.com/enriquepascalin/awm-orchestrator/internal/store"
	"github.com/enriquepascalin/awm-orchestrator/internal/supervisor"
)

type PublicServer struct {
	awmv1.UnimplementedPublicServer
	engine     *runtime.Engine
	registry   *runtime.DefinitionRegistry
	supervisor *supervisor.Supervisor
	store      store.Store
	db         *sqlx.DB
}

func NewPublicServer(engine *runtime.Engine, registry *runtime.DefinitionRegistry, sup *supervisor.Supervisor, st store.Store, db *sqlx.DB) *PublicServer {
	return &PublicServer{engine: engine, registry: registry, supervisor: sup, store: st, db: db}
}

func (s *PublicServer) StartWorkflow(ctx context.Context, req *awmv1.StartWorkflowRequest) (*awmv1.StartWorkflowResponse, error) {
	log.Printf("StartWorkflow called: definition=%s tenant=%s", req.WorkflowDefinitionId, req.Tenant)

	def, err := s.registry.Get(ctx, req.WorkflowDefinitionId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "workflow definition not found: %v", err)
	}

	input := req.Input.AsMap()
	instanceID, err := s.engine.StartWorkflow(ctx, def, req.Tenant, input)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to start workflow: %v", err)
	}

	if err := s.supervisor.StartWorker(instanceID, def.ID, req.Tenant); err != nil {
		log.Printf("Failed to start worker for workflow %s: %v", instanceID, err)
		_ = s.store.UpdateWorkflowStatus(ctx, instanceID, "DEGRADED")
		return nil, status.Errorf(codes.Internal, "failed to start worker: %v", err)
	}

	return &awmv1.StartWorkflowResponse{
		WorkflowInstanceId:   instanceID.String(),
		OrchestratorEndpoint: "localhost:9091",
	}, nil
}

func (s *PublicServer) SignalWorkflow(ctx context.Context, req *awmv1.SignalWorkflowRequest) (*awmv1.SignalWorkflowResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "SignalWorkflow not implemented")
}

func (s *PublicServer) CancelWorkflow(ctx context.Context, req *awmv1.CancelWorkflowRequest) (*awmv1.CancelWorkflowResponse, error) {
	instanceID, err := uuid.Parse(req.WorkflowInstanceId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid workflow_instance_id")
	}
	if err := s.supervisor.StopWorker(instanceID); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to stop worker: %v", err)
	}
	if err := s.store.UpdateWorkflowStatus(ctx, instanceID, "CANCELED"); err != nil {
		return nil, err
	}
	return &awmv1.CancelWorkflowResponse{Accepted: true}, nil
}

func (s *PublicServer) CreateWorkflowDefinition(ctx context.Context, req *awmv1.CreateWorkflowDefinitionRequest) (*awmv1.WorkflowDefinition, error) {
	if req.YamlContent == "" {
		return nil, status.Errorf(codes.InvalidArgument, "yaml_content is required")
	}

	// Parse and validate YAML first (no DB needed)
	def, err := parser.Parse([]byte(req.YamlContent))
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid YAML: %v", err)
	}

	// Resolve tenant ID by name
	var tenantID string
	err = s.db.QueryRowContext(ctx, "SELECT id FROM tenants WHERE name = $1", req.Tenant).Scan(&tenantID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "tenant %q not found", req.Tenant)
		}
		log.Printf("Failed to resolve tenant %q: %v", req.Tenant, err)
		return nil, status.Errorf(codes.Internal, "failed to resolve tenant")
	}

	if def.ID == "" {
		def.ID = uuid.New().String()
	}
	def.Tenant = tenantID
	def.Name = req.Name
	def.Version = "1"
	def.YAML = req.YamlContent

	if err := s.registry.Store(ctx, def); err != nil {
		log.Printf("Failed to store workflow definition: %v", err)
		return nil, status.Errorf(codes.Internal, "failed to store definition: %v", err)
	}

	return &awmv1.WorkflowDefinition{
		Id:           def.ID,
		Tenant:       req.Tenant,
		Name:         def.Name,
		DefinitionId: def.ID,
		Version:      1,
		YamlContent:  def.YAML,
	}, nil
}

func (s *PublicServer) GetWorkflowDefinition(ctx context.Context, req *awmv1.GetWorkflowDefinitionRequest) (*awmv1.WorkflowDefinition, error) {
	def, err := s.registry.Get(ctx, req.Id)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "workflow definition not found: %v", err)
	}
	return &awmv1.WorkflowDefinition{
		Id:           def.ID,
		Tenant:       def.Tenant,
		Name:         def.Name,
		DefinitionId: def.ID,
		Version:      1,
		YamlContent:  def.YAML,
	}, nil
}

func (s *PublicServer) UpdateWorkflowDefinition(ctx context.Context, req *awmv1.UpdateWorkflowDefinitionRequest) (*awmv1.WorkflowDefinition, error) {
	return nil, status.Errorf(codes.Unimplemented, "UpdateWorkflowDefinition not implemented")
}

func (s *PublicServer) DeleteWorkflowDefinition(ctx context.Context, req *awmv1.DeleteWorkflowDefinitionRequest) (*awmv1.DeleteWorkflowDefinitionResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "DeleteWorkflowDefinition not implemented")
}

func (s *PublicServer) ListWorkflowDefinitions(ctx context.Context, req *awmv1.ListWorkflowDefinitionsRequest) (*awmv1.ListWorkflowDefinitionsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "ListWorkflowDefinitions not implemented")
}

func (s *PublicServer) DeployWorkflow(ctx context.Context, req *awmv1.DeployWorkflowRequest) (*awmv1.DeployWorkflowResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "DeployWorkflow not implemented")
}

func (s *PublicServer) GetInstance(ctx context.Context, req *awmv1.GetInstanceRequest) (*awmv1.OrchestratorInstance, error) {
	return nil, status.Errorf(codes.Unimplemented, "GetInstance not implemented")
}

func (s *PublicServer) ListInstances(ctx context.Context, req *awmv1.ListInstancesRequest) (*awmv1.ListInstancesResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "ListInstances not implemented")
}

func (s *PublicServer) StopInstance(ctx context.Context, req *awmv1.StopInstanceRequest) (*awmv1.StopInstanceResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "StopInstance not implemented")
}
