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
	"google.golang.org/protobuf/types/known/timestamppb"

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

// ── Workflow definition management ───────────────────────────────────────────

func (s *PublicServer) CreateWorkflowDefinition(ctx context.Context, req *awmv1.CreateWorkflowDefinitionRequest) (*awmv1.WorkflowDefinition, error) {
	if req.YamlContent == "" {
		return nil, status.Errorf(codes.InvalidArgument, "yaml_content is required")
	}

	def, err := parser.Parse([]byte(req.YamlContent))
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid YAML: %v", err)
	}

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
		CreatedBy:    req.CreatedBy,
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
	if req.Id == "" || req.YamlContent == "" {
		return nil, status.Errorf(codes.InvalidArgument, "id and yaml_content are required")
	}
	id, err := uuid.Parse(req.Id)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid id: %v", err)
	}
	if _, err := parser.Parse([]byte(req.YamlContent)); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid YAML: %v", err)
	}
	if err := s.store.UpdateWorkflowDefinitionYAML(ctx, id, req.YamlContent); err != nil {
		return nil, status.Errorf(codes.Internal, "update failed: %v", err)
	}

	var row store.WorkflowDefinitionRow
	err = s.db.GetContext(ctx, &row,
		`SELECT id, tenant_id, name, definition_id, version, yaml_content,
		        COALESCE(created_by, '') AS created_by, created_at, updated_at
		   FROM workflow_definitions WHERE id = $1`, id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to reload definition: %v", err)
	}
	return definitionRowToProto(row), nil
}

func (s *PublicServer) DeleteWorkflowDefinition(ctx context.Context, req *awmv1.DeleteWorkflowDefinitionRequest) (*awmv1.DeleteWorkflowDefinitionResponse, error) {
	if req.Id == "" {
		return nil, status.Errorf(codes.InvalidArgument, "id is required")
	}
	id, err := uuid.Parse(req.Id)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid id: %v", err)
	}
	if err := s.store.DeleteWorkflowDefinition(ctx, id); err != nil {
		return nil, status.Errorf(codes.Internal, "delete failed: %v", err)
	}
	return &awmv1.DeleteWorkflowDefinitionResponse{Deleted: true}, nil
}

func (s *PublicServer) ListWorkflowDefinitions(ctx context.Context, req *awmv1.ListWorkflowDefinitionsRequest) (*awmv1.ListWorkflowDefinitionsResponse, error) {
	var tenantID string
	if req.Tenant != "" {
		err := s.db.QueryRowContext(ctx, "SELECT id FROM tenants WHERE name = $1", req.Tenant).Scan(&tenantID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.Internal, "failed to resolve tenant: %v", err)
		}
	}

	limit := int(req.PageSize)
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.store.ListWorkflowDefinitions(ctx, store.ListDefinitionsFilter{
		TenantID:   tenantID,
		NameFilter: req.NameFilter,
		Limit:      limit,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list failed: %v", err)
	}

	defs := make([]*awmv1.WorkflowDefinition, len(rows))
	for i, r := range rows {
		defs[i] = definitionRowToProto(r)
	}
	return &awmv1.ListWorkflowDefinitionsResponse{Definitions: defs}, nil
}

// ── Workflow lifecycle ────────────────────────────────────────────────────────

func (s *PublicServer) DeployWorkflow(ctx context.Context, req *awmv1.DeployWorkflowRequest) (*awmv1.DeployWorkflowResponse, error) {
	def, err := s.registry.Get(ctx, req.WorkflowDefinitionId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "workflow definition not found: %v", err)
	}

	instanceID, err := s.engine.StartWorkflow(ctx, def, def.Tenant, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create workflow instance: %v", err)
	}

	if err := s.supervisor.StartWorker(instanceID, def.ID, def.Tenant); err != nil {
		log.Printf("Failed to start worker for instance %s: %v", instanceID, err)
		_ = s.store.UpdateWorkflowStatus(ctx, instanceID, "DEGRADED")
		return nil, status.Errorf(codes.Internal, "failed to start worker: %v", err)
	}

	inst := &awmv1.OrchestratorInstance{
		Id:                   instanceID.String(),
		WorkflowDefinitionId: def.ID,
		Status:               awmv1.InstanceStatus_INSTANCE_STATUS_PROVISIONING,
		Endpoints: &awmv1.InstanceEndpoints{
			Grpc:   "localhost:9091",
			Health: "http://localhost:8080/healthz",
		},
	}
	return &awmv1.DeployWorkflowResponse{
		InstanceId: instanceID.String(),
		Instance:   inst,
	}, nil
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

func (s *PublicServer) SignalWorkflow(ctx context.Context, req *awmv1.SignalWorkflowRequest) (*awmv1.SignalWorkflowResponse, error) {
	if req.WorkflowInstanceId == "" || req.SignalName == "" {
		return nil, status.Errorf(codes.InvalidArgument, "workflow_instance_id and signal_name are required")
	}
	instanceID, err := uuid.Parse(req.WorkflowInstanceId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid workflow_instance_id")
	}

	instance, _, err := s.store.LoadInstance(ctx, instanceID)
	if err != nil {
		if errors.Is(err, store.ErrInstanceNotFound) {
			return nil, status.Errorf(codes.NotFound, "instance not found")
		}
		return nil, status.Errorf(codes.Internal, "failed to load instance: %v", err)
	}

	payload := make(map[string]interface{})
	if req.Payload != nil {
		payload = req.Payload.AsMap()
	}

	event := store.HistoryEvent{
		EventType: "SIGNAL_RECEIVED",
		Payload: map[string]interface{}{
			"signal_name": req.SignalName,
			"data":        payload,
		},
	}
	if err := s.store.AppendEvents(ctx, instanceID, instance.Version, []store.HistoryEvent{event}); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to append signal event: %v", err)
	}
	return &awmv1.SignalWorkflowResponse{Accepted: true}, nil
}

// ── Instance queries ──────────────────────────────────────────────────────────

func (s *PublicServer) GetInstance(ctx context.Context, req *awmv1.GetInstanceRequest) (*awmv1.OrchestratorInstance, error) {
	if req.Id == "" {
		return nil, status.Errorf(codes.InvalidArgument, "id is required")
	}
	instanceID, err := uuid.Parse(req.Id)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid id")
	}
	instance, _, err := s.store.LoadInstance(ctx, instanceID)
	if err != nil {
		if errors.Is(err, store.ErrInstanceNotFound) {
			return nil, status.Errorf(codes.NotFound, "instance not found")
		}
		return nil, status.Errorf(codes.Internal, "failed to load instance: %v", err)
	}
	return instanceToProto(instance), nil
}

func (s *PublicServer) ListInstances(ctx context.Context, req *awmv1.ListInstancesRequest) (*awmv1.ListInstancesResponse, error) {
	limit := int(req.PageSize)
	if limit <= 0 {
		limit = 50
	}
	filter := store.ListInstancesFilter{
		WorkflowDefinitionID: req.WorkflowDefinitionId,
		Status:               protoInstanceStatusToString(req.StatusFilter),
		Limit:                limit,
	}
	instances, err := s.store.ListWorkflowInstances(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list failed: %v", err)
	}

	protos := make([]*awmv1.OrchestratorInstance, len(instances))
	for i := range instances {
		protos[i] = instanceToProto(&instances[i])
	}
	return &awmv1.ListInstancesResponse{Instances: protos}, nil
}

func (s *PublicServer) StopInstance(ctx context.Context, req *awmv1.StopInstanceRequest) (*awmv1.StopInstanceResponse, error) {
	if req.Id == "" {
		return nil, status.Errorf(codes.InvalidArgument, "id is required")
	}
	instanceID, err := uuid.Parse(req.Id)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid id")
	}

	// StopWorker may return an error if the worker is not running; that's acceptable.
	if err := s.supervisor.StopWorker(instanceID); err != nil {
		log.Printf("StopInstance: worker not running for %s: %v", instanceID, err)
	}

	finalStatus := "STOPPED"
	if req.Archive {
		finalStatus = "ARCHIVED"
	}
	if err := s.store.UpdateWorkflowStatus(ctx, instanceID, finalStatus); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update instance status: %v", err)
	}
	return &awmv1.StopInstanceResponse{Stopped: true}, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func instanceToProto(inst *store.WorkflowInstance) *awmv1.OrchestratorInstance {
	return &awmv1.OrchestratorInstance{
		Id:                   inst.ID.String(),
		WorkflowDefinitionId: inst.WorkflowDefinitionID,
		Status:               workflowStatusToInstanceStatus(inst.Status),
		CreatedAt:            timestamppb.New(inst.CreatedAt),
		Endpoints: &awmv1.InstanceEndpoints{
			Grpc:   "localhost:9091",
			Health: "http://localhost:8080/healthz",
		},
	}
}

func definitionRowToProto(row store.WorkflowDefinitionRow) *awmv1.WorkflowDefinition {
	return &awmv1.WorkflowDefinition{
		Id:           row.ID.String(),
		Tenant:       row.TenantID,
		Name:         row.Name,
		DefinitionId: row.DefinitionID,
		Version:      int32(row.Version),
		YamlContent:  row.YAMLContent,
		CreatedBy:    row.CreatedBy,
		CreatedAt:    timestamppb.New(row.CreatedAt),
		UpdatedAt:    timestamppb.New(row.UpdatedAt),
	}
}

func workflowStatusToInstanceStatus(s string) awmv1.InstanceStatus {
	switch s {
	case "PROVISIONING":
		return awmv1.InstanceStatus_INSTANCE_STATUS_PROVISIONING
	case "RUNNING":
		return awmv1.InstanceStatus_INSTANCE_STATUS_ACTIVE
	case "DEGRADED":
		return awmv1.InstanceStatus_INSTANCE_STATUS_DEGRADED
	case "STOPPED", "CANCELED":
		return awmv1.InstanceStatus_INSTANCE_STATUS_STOPPED
	case "ARCHIVED":
		return awmv1.InstanceStatus_INSTANCE_STATUS_ARCHIVED
	default:
		return awmv1.InstanceStatus_INSTANCE_STATUS_UNSPECIFIED
	}
}

func protoInstanceStatusToString(s awmv1.InstanceStatus) string {
	switch s {
	case awmv1.InstanceStatus_INSTANCE_STATUS_PROVISIONING:
		return "PROVISIONING"
	case awmv1.InstanceStatus_INSTANCE_STATUS_ACTIVE:
		return "RUNNING"
	case awmv1.InstanceStatus_INSTANCE_STATUS_DEGRADED:
		return "DEGRADED"
	case awmv1.InstanceStatus_INSTANCE_STATUS_STOPPED:
		return "STOPPED"
	case awmv1.InstanceStatus_INSTANCE_STATUS_ARCHIVED:
		return "ARCHIVED"
	default:
		return ""
	}
}
