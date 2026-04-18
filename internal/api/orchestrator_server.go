package api

import (
	"context"
	"errors"
	"io"
	"log"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	awmv1 "github.com/enriquepascalin/awm-orchestrator/internal/proto/awm/v1"
	"github.com/enriquepascalin/awm-orchestrator/internal/runtime"
	storepkg "github.com/enriquepascalin/awm-orchestrator/internal/store"
	"github.com/enriquepascalin/awm-orchestrator/internal/supervisor"
)

type OrchestratorServer struct {
	awmv1.UnimplementedOrchestratorServer
	engine     *runtime.Engine
	supervisor *supervisor.Supervisor
	store      storepkg.Store
}

func NewOrchestratorServer(engine *runtime.Engine, sup *supervisor.Supervisor, st storepkg.Store) *OrchestratorServer {
	return &OrchestratorServer{engine: engine, supervisor: sup, store: st}
}

// ── Bidirectional agent stream ────────────────────────────────────────────────

func (s *OrchestratorServer) Connect(stream awmv1.Orchestrator_ConnectServer) error {
	var agentID string
	var tenant string
	var capabilities []string

	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		switch m := msg.Message.(type) {
		case *awmv1.AgentToServer_Register:
			agentID = m.Register.AgentId
			tenant = m.Register.Tenant
			capabilities = m.Register.Capabilities
			log.Printf("Agent registered: %s (tenant=%s, capabilities=%v)", agentID, tenant, capabilities)
			stream.Send(&awmv1.ServerToAgent{
				Message: &awmv1.ServerToAgent_Ack{Ack: &awmv1.Ack{Success: true}},
			})
			go s.pushTasks(stream, agentID, tenant, capabilities)

		case *awmv1.AgentToServer_TaskResult:
			res := m.TaskResult
			taskID, err := uuid.Parse(res.TaskId)
			if err != nil {
				log.Printf("Invalid task ID: %s", res.TaskId)
				continue
			}
			switch out := res.Outcome.(type) {
			case *awmv1.TaskResult_Success:
				err = s.engine.CompleteTask(context.Background(), taskID, out.Success.AsMap())
			case *awmv1.TaskResult_Error:
				err = s.engine.FailTask(context.Background(), taskID, out.Error)
			}
			if err != nil {
				log.Printf("Failed to process task result: %v", err)
			}

		case *awmv1.AgentToServer_Heartbeat:
			// agent liveness tick — no durable state needed here
		}
	}
}

func (s *OrchestratorServer) pushTasks(stream awmv1.Orchestrator_ConnectServer, agentID, tenant string, capabilities []string) {
	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		task, err := s.engine.ClaimPendingTask(ctx, capabilities, agentID)
		if err != nil || task == nil {
			time.Sleep(2 * time.Second)
			continue
		}

		inputStruct, _ := structpb.NewStruct(task.Input)
		assignment := &awmv1.TaskAssignment{
			TaskId:             task.ID.String(),
			WorkflowInstanceId: task.WorkflowInstanceID.String(),
			ActivityName:       task.ActivityName,
			Input:              inputStruct,
		}
		if task.Deadline != nil {
			assignment.Deadline = timestamppb.New(*task.Deadline)
		}
		if err := stream.Send(&awmv1.ServerToAgent{
			Message: &awmv1.ServerToAgent_TaskAssignment{TaskAssignment: assignment},
		}); err != nil {
			log.Printf("Failed to send task to agent %s: %v", agentID, err)
			return
		}
	}
}

// ── Legacy unary task ops ─────────────────────────────────────────────────────

func (s *OrchestratorServer) PollTask(ctx context.Context, req *awmv1.PollTaskRequest) (*awmv1.PollTaskResponse, error) {
	task, err := s.engine.ClaimPendingTask(ctx, nil, req.AgentId)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return &awmv1.PollTaskResponse{}, nil
	}
	inputStruct, _ := structpb.NewStruct(task.Input)
	return &awmv1.PollTaskResponse{
		Task: &awmv1.TaskAssignment{
			TaskId:             task.ID.String(),
			WorkflowInstanceId: task.WorkflowInstanceID.String(),
			ActivityName:       task.ActivityName,
			Input:              inputStruct,
		},
	}, nil
}

func (s *OrchestratorServer) CompleteTask(ctx context.Context, req *awmv1.CompleteTaskRequest) (*awmv1.CompleteTaskResponse, error) {
	taskID, err := uuid.Parse(req.TaskId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid task_id")
	}
	if err := s.engine.CompleteTask(ctx, taskID, req.Result.AsMap()); err != nil {
		return nil, err
	}
	return &awmv1.CompleteTaskResponse{Accepted: true}, nil
}

func (s *OrchestratorServer) FailTask(ctx context.Context, req *awmv1.FailTaskRequest) (*awmv1.FailTaskResponse, error) {
	taskID, err := uuid.Parse(req.TaskId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid task_id")
	}
	if err := s.engine.FailTask(ctx, taskID, req.Error); err != nil {
		return nil, err
	}
	return &awmv1.FailTaskResponse{Accepted: true}, nil
}

func (s *OrchestratorServer) Heartbeat(ctx context.Context, req *awmv1.HeartbeatRequest) (*awmv1.HeartbeatResponse, error) {
	return &awmv1.HeartbeatResponse{Ok: true}, nil
}

// ── Workflow state + listing ──────────────────────────────────────────────────

func (s *OrchestratorServer) GetWorkflowState(ctx context.Context, req *awmv1.GetWorkflowStateRequest) (*awmv1.GetWorkflowStateResponse, error) {
	instanceID, err := uuid.Parse(req.WorkflowInstanceId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid workflow_instance_id")
	}
	instance, err := s.engine.GetWorkflowState(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	stateStruct, _ := structpb.NewStruct(instance.DimensionalState)
	return &awmv1.GetWorkflowStateResponse{
		WorkflowInstanceId:   instance.ID.String(),
		WorkflowDefinitionId: instance.WorkflowDefinitionID,
		Status:               workflowStatusFromString(instance.Status),
		CurrentState:         instance.CurrentPhase,
		ContextData:          stateStruct,
		CreatedAt:            timestamppb.New(instance.CreatedAt),
		UpdatedAt:            timestamppb.New(instance.UpdatedAt),
	}, nil
}

func (s *OrchestratorServer) ListWorkflows(ctx context.Context, req *awmv1.ListWorkflowsRequest) (*awmv1.ListWorkflowsResponse, error) {
	limit := int(req.PageSize)
	if limit <= 0 {
		limit = 50
	}
	filter := storepkg.ListInstancesFilter{
		WorkflowDefinitionID: req.WorkflowDefinitionId,
		Status:               workflowStatusToString(req.StatusFilter),
		Limit:                limit,
	}
	instances, err := s.store.ListWorkflowInstances(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list workflows failed: %v", err)
	}

	summaries := make([]*awmv1.WorkflowSummary, len(instances))
	for i, inst := range instances {
		summaries[i] = &awmv1.WorkflowSummary{
			WorkflowInstanceId:   inst.ID.String(),
			WorkflowDefinitionId: inst.WorkflowDefinitionID,
			Status:               workflowStatusFromString(inst.Status),
			CurrentState:         inst.CurrentPhase,
			CreatedAt:            timestamppb.New(inst.CreatedAt),
		}
	}
	return &awmv1.ListWorkflowsResponse{Workflows: summaries}, nil
}

func (s *OrchestratorServer) StreamWorkflowEvents(req *awmv1.StreamWorkflowEventsRequest, stream grpc.ServerStreamingServer[awmv1.WorkflowEvent]) error {
	instanceID, err := uuid.Parse(req.WorkflowInstanceId)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid workflow_instance_id")
	}

	events, err := s.store.GetHistoryEvents(stream.Context(), instanceID, req.AfterSequence)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to load events: %v", err)
	}

	for _, e := range events {
		payload, _ := structpb.NewStruct(e.Payload)
		if err := stream.Send(&awmv1.WorkflowEvent{
			SequenceNum: e.SequenceNum,
			Timestamp:   timestamppb.New(e.RecordedAt),
			EventType:   e.EventType,
			Payload:     payload,
		}); err != nil {
			return err
		}
	}
	return nil
}

// ── Group D: Task queries ─────────────────────────────────────────────────────

func (s *OrchestratorServer) ListTasks(ctx context.Context, req *awmv1.ListTasksRequest) (*awmv1.ListTasksResponse, error) {
	instanceID, err := uuid.Parse(req.WorkflowInstanceId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid workflow_instance_id")
	}
	limit := int(req.PageSize)
	if limit <= 0 {
		limit = 50
	}
	tasks, err := s.store.ListTasksForInstance(ctx, instanceID, req.StatusFilter, limit, 0)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list tasks failed: %v", err)
	}

	summaries := make([]*awmv1.TaskSummary, len(tasks))
	for i, t := range tasks {
		summaries[i] = taskToSummary(&t)
	}
	return &awmv1.ListTasksResponse{Tasks: summaries}, nil
}

func (s *OrchestratorServer) GetTask(ctx context.Context, req *awmv1.GetTaskRequest) (*awmv1.GetTaskResponse, error) {
	taskID, err := uuid.Parse(req.TaskId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid task_id")
	}
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, storepkg.ErrTaskNotFound) {
			return nil, status.Errorf(codes.NotFound, "task not found")
		}
		return nil, status.Errorf(codes.Internal, "get task failed: %v", err)
	}

	inputStruct, _ := structpb.NewStruct(task.Input)
	outputStruct, _ := structpb.NewStruct(task.Output)
	resp := &awmv1.GetTaskResponse{
		TaskId:             task.ID.String(),
		WorkflowInstanceId: task.WorkflowInstanceID.String(),
		StateName:          task.StateName,
		ActivityName:       task.ActivityName,
		Capabilities:       []string(task.Capabilities),
		Roles:              []string(task.Roles),
		Input:              inputStruct,
		Output:             outputStruct,
		Status:             task.Status,
		CreatedAt:          timestamppb.New(task.CreatedAt),
		UpdatedAt:          timestamppb.New(task.UpdatedAt),
	}
	if task.AssignedAgentID != nil {
		resp.AssignedAgentId = *task.AssignedAgentID
	}
	if task.Deadline != nil {
		resp.Deadline = timestamppb.New(*task.Deadline)
	}
	return resp, nil
}

func (s *OrchestratorServer) ListMyTasks(ctx context.Context, req *awmv1.ListMyTasksRequest) (*awmv1.ListMyTasksResponse, error) {
	if req.AgentId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "agent_id is required")
	}
	tasks, err := s.store.ListTasksForAgent(ctx, req.AgentId, req.Tenant, req.StatusFilter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list tasks failed: %v", err)
	}
	summaries := make([]*awmv1.TaskSummary, len(tasks))
	for i, t := range tasks {
		summaries[i] = taskToSummary(&t)
	}
	return &awmv1.ListMyTasksResponse{Tasks: summaries}, nil
}

// ── Group E: Task resolution ──────────────────────────────────────────────────

func (s *OrchestratorServer) ClaimTask(ctx context.Context, req *awmv1.ClaimTaskRequest) (*awmv1.ClaimTaskResponse, error) {
	if req.TaskId == "" || req.AgentId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "task_id and agent_id are required")
	}
	taskID, err := uuid.Parse(req.TaskId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid task_id")
	}

	deadline := time.Now().Add(30 * time.Minute)
	if err := s.store.ClaimTask(ctx, taskID, req.AgentId, deadline); err != nil {
		if errors.Is(err, storepkg.ErrTaskNotFound) {
			return &awmv1.ClaimTaskResponse{Claimed: false, Reason: "task not found"}, nil
		}
		if errors.Is(err, storepkg.ErrTaskNotClaimable) {
			return &awmv1.ClaimTaskResponse{Claimed: false, Reason: "task is not claimable"}, nil
		}
		return nil, status.Errorf(codes.Internal, "claim failed: %v", err)
	}

	_ = s.store.AppendTaskAudit(ctx, &storepkg.TaskAuditEntry{
		ID:        uuid.New(),
		TaskID:    taskID,
		EventType: "CLAIMED",
		AgentID:   req.AgentId,
		Message:   "Task claimed",
	})

	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return &awmv1.ClaimTaskResponse{Claimed: true}, nil
	}
	inputStruct, _ := structpb.NewStruct(task.Input)
	assignment := &awmv1.TaskAssignment{
		TaskId:             task.ID.String(),
		WorkflowInstanceId: task.WorkflowInstanceID.String(),
		ActivityName:       task.ActivityName,
		Input:              inputStruct,
		Deadline:           timestamppb.New(deadline),
	}
	return &awmv1.ClaimTaskResponse{Claimed: true, Task: assignment}, nil
}

func (s *OrchestratorServer) ReleaseTask(ctx context.Context, req *awmv1.ReleaseTaskRequest) (*awmv1.ReleaseTaskResponse, error) {
	taskID, err := uuid.Parse(req.TaskId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid task_id")
	}
	if err := s.store.ReleaseTask(ctx, taskID, req.AgentId); err != nil {
		return nil, status.Errorf(codes.Internal, "release failed: %v", err)
	}
	_ = s.store.AppendTaskAudit(ctx, &storepkg.TaskAuditEntry{
		ID:        uuid.New(),
		TaskID:    taskID,
		EventType: "RELEASED",
		AgentID:   req.AgentId,
		Message:   req.Reason,
	})
	return &awmv1.ReleaseTaskResponse{Released: true}, nil
}

func (s *OrchestratorServer) SubmitTaskResult(ctx context.Context, req *awmv1.SubmitTaskResultRequest) (*awmv1.SubmitTaskResultResponse, error) {
	taskID, err := uuid.Parse(req.TaskId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid task_id")
	}

	result, evidenceMap := evidenceToMaps(req.Evidence)
	effect, err := s.engine.SubmitTaskResult(ctx, taskID, result, evidenceMap)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "submit result failed: %v", err)
	}

	_ = s.store.AppendTaskAudit(ctx, &storepkg.TaskAuditEntry{
		ID:        uuid.New(),
		TaskID:    taskID,
		EventType: "COMPLETED",
		AgentID:   req.AgentId,
		Message:   "Task completed with evidence",
	})

	return &awmv1.SubmitTaskResultResponse{
		Accepted: true,
		Effect:   advancementEffectToProto(effect),
	}, nil
}

func (s *OrchestratorServer) SubmitTaskFailure(ctx context.Context, req *awmv1.SubmitTaskFailureRequest) (*awmv1.SubmitTaskFailureResponse, error) {
	taskID, err := uuid.Parse(req.TaskId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid task_id")
	}
	if err := s.engine.FailTask(ctx, taskID, req.Error); err != nil {
		return nil, status.Errorf(codes.Internal, "submit failure failed: %v", err)
	}

	msg := ""
	if req.Error != nil {
		msg = req.Error.Message
	}
	_ = s.store.AppendTaskAudit(ctx, &storepkg.TaskAuditEntry{
		ID:        uuid.New(),
		TaskID:    taskID,
		EventType: "FAILED",
		AgentID:   req.AgentId,
		Message:   msg,
	})

	return &awmv1.SubmitTaskFailureResponse{
		Accepted: true,
		Effect:   awmv1.WorkflowAdvancementEffect_WORKFLOW_ADVANCEMENT_EFFECT_FAILED,
	}, nil
}

func (s *OrchestratorServer) UpdateTaskProgress(ctx context.Context, req *awmv1.UpdateTaskProgressRequest) (*awmv1.UpdateTaskProgressResponse, error) {
	taskID, err := uuid.Parse(req.TaskId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid task_id")
	}

	data := make(map[string]interface{})
	if req.Data != nil {
		data = req.Data.AsMap()
	}
	data["percent"] = req.Percent

	_ = s.store.AppendTaskAudit(ctx, &storepkg.TaskAuditEntry{
		ID:        uuid.New(),
		TaskID:    taskID,
		EventType: "PROGRESS",
		AgentID:   req.AgentId,
		Message:   req.Message,
		Data:      data,
	})
	return &awmv1.UpdateTaskProgressResponse{Accepted: true}, nil
}

func (s *OrchestratorServer) ReassignTask(ctx context.Context, req *awmv1.ReassignTaskRequest) (*awmv1.ReassignTaskResponse, error) {
	taskID, err := uuid.Parse(req.TaskId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid task_id")
	}
	if err := s.store.ReassignTask(ctx, taskID, req.FromAgentId, req.ToAgentId); err != nil {
		return nil, status.Errorf(codes.Internal, "reassign failed: %v", err)
	}
	_ = s.store.AppendTaskAudit(ctx, &storepkg.TaskAuditEntry{
		ID:        uuid.New(),
		TaskID:    taskID,
		EventType: "REASSIGNED",
		AgentID:   req.FromAgentId,
		Message:   req.Reason,
		Data:      map[string]interface{}{"to_agent_id": req.ToAgentId},
	})
	return &awmv1.ReassignTaskResponse{Accepted: true}, nil
}

func (s *OrchestratorServer) GetTaskHistory(ctx context.Context, req *awmv1.GetTaskHistoryRequest) (*awmv1.GetTaskHistoryResponse, error) {
	taskID, err := uuid.Parse(req.TaskId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid task_id")
	}
	entries, err := s.store.GetTaskAuditLog(ctx, taskID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get history failed: %v", err)
	}

	protoEntries := make([]*awmv1.TaskAuditEntry, len(entries))
	for i, e := range entries {
		dataStruct, _ := structpb.NewStruct(e.Data)
		protoEntries[i] = &awmv1.TaskAuditEntry{
			Timestamp: timestamppb.New(e.OccurredAt),
			EventType: e.EventType,
			AgentId:   e.AgentID,
			Message:   e.Message,
			Data:      dataStruct,
		}
	}
	return &awmv1.GetTaskHistoryResponse{
		TaskId:  req.TaskId,
		Entries: protoEntries,
	}, nil
}

func (s *OrchestratorServer) WatchTask(req *awmv1.WatchTaskRequest, stream grpc.ServerStreamingServer[awmv1.TaskStatusEvent]) error {
	taskID, err := uuid.Parse(req.TaskId)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid task_id")
	}

	ctx := stream.Context()
	var lastCount int

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		entries, err := s.store.GetTaskAuditLog(ctx, taskID)
		if err != nil {
			return status.Errorf(codes.Internal, "watch failed: %v", err)
		}
		for i := lastCount; i < len(entries); i++ {
			e := entries[i]
			if err := stream.Send(&awmv1.TaskStatusEvent{
				TaskId:     req.TaskId,
				Status:     e.EventType,
				AgentId:    e.AgentID,
				Message:    e.Message,
				OccurredAt: timestamppb.New(e.OccurredAt),
			}); err != nil {
				return err
			}
		}
		lastCount = len(entries)

		// Stop streaming on terminal states
		task, err := s.store.GetTask(ctx, taskID)
		if err == nil && (task.Status == "COMPLETED" || task.Status == "FAILED") {
			return nil
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *OrchestratorServer) ExtendTaskLease(ctx context.Context, req *awmv1.ExtendTaskLeaseRequest) (*awmv1.ExtendTaskLeaseResponse, error) {
	taskID, err := uuid.Parse(req.TaskId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid task_id")
	}
	if req.NewDeadline == nil {
		return nil, status.Errorf(codes.InvalidArgument, "new_deadline is required")
	}
	deadline := req.NewDeadline.AsTime()
	if err := s.store.ExtendTaskDeadline(ctx, taskID, deadline); err != nil {
		return nil, status.Errorf(codes.Internal, "extend lease failed: %v", err)
	}
	return &awmv1.ExtendTaskLeaseResponse{
		Granted:        true,
		ActualDeadline: timestamppb.New(deadline),
	}, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func taskToSummary(t *storepkg.Task) *awmv1.TaskSummary {
	s := &awmv1.TaskSummary{
		TaskId:             t.ID.String(),
		WorkflowInstanceId: t.WorkflowInstanceID.String(),
		StateName:          t.StateName,
		ActivityName:       t.ActivityName,
		Status:             t.Status,
		CreatedAt:          timestamppb.New(t.CreatedAt),
	}
	if t.AssignedAgentID != nil {
		s.AssignedAgentId = *t.AssignedAgentID
	}
	if t.Deadline != nil {
		s.Deadline = timestamppb.New(*t.Deadline)
	}
	return s
}

// evidenceToMaps converts proto TaskEvidence to the result and evidence maps expected by the engine.
func evidenceToMaps(ev *awmv1.TaskEvidence) (result map[string]interface{}, evidence map[string]interface{}) {
	result = make(map[string]interface{})
	evidence = make(map[string]interface{})
	if ev == nil {
		return
	}
	evidence["summary"] = ev.Summary
	evidence["agent_id"] = ev.AgentId
	if ev.Data != nil {
		result = ev.Data.AsMap()
		evidence["data"] = ev.Data.AsMap()
	}
	artifacts := make([]map[string]interface{}, len(ev.Artifacts))
	for i, a := range ev.Artifacts {
		artifacts[i] = map[string]interface{}{
			"type":  a.Type,
			"uri":   a.Uri,
			"label": a.Label,
		}
	}
	evidence["artifacts"] = artifacts
	return
}

func advancementEffectToProto(e runtime.TaskAdvancementEffect) awmv1.WorkflowAdvancementEffect {
	switch e {
	case runtime.EffectTasksPending:
		return awmv1.WorkflowAdvancementEffect_WORKFLOW_ADVANCEMENT_EFFECT_TASKS_PENDING
	case runtime.EffectAdvanced:
		return awmv1.WorkflowAdvancementEffect_WORKFLOW_ADVANCEMENT_EFFECT_ADVANCED
	case runtime.EffectCompleted:
		return awmv1.WorkflowAdvancementEffect_WORKFLOW_ADVANCEMENT_EFFECT_COMPLETED
	case runtime.EffectFailed:
		return awmv1.WorkflowAdvancementEffect_WORKFLOW_ADVANCEMENT_EFFECT_FAILED
	case runtime.EffectRetrying:
		return awmv1.WorkflowAdvancementEffect_WORKFLOW_ADVANCEMENT_EFFECT_RETRYING
	case runtime.EffectWaiting:
		return awmv1.WorkflowAdvancementEffect_WORKFLOW_ADVANCEMENT_EFFECT_WAITING
	default:
		return awmv1.WorkflowAdvancementEffect_WORKFLOW_ADVANCEMENT_EFFECT_UNSPECIFIED
	}
}

func workflowStatusFromString(s string) awmv1.WorkflowStatus {
	switch s {
	case "RUNNING":
		return awmv1.WorkflowStatus_WORKFLOW_STATUS_RUNNING
	case "COMPLETED":
		return awmv1.WorkflowStatus_WORKFLOW_STATUS_COMPLETED
	case "FAILED":
		return awmv1.WorkflowStatus_WORKFLOW_STATUS_FAILED
	case "CANCELED":
		return awmv1.WorkflowStatus_WORKFLOW_STATUS_CANCELED
	case "SUSPENDED":
		return awmv1.WorkflowStatus_WORKFLOW_STATUS_SUSPENDED
	case "WAITING":
		return awmv1.WorkflowStatus_WORKFLOW_STATUS_WAITING
	default:
		return awmv1.WorkflowStatus_WORKFLOW_STATUS_UNSPECIFIED
	}
}

func workflowStatusToString(s awmv1.WorkflowStatus) string {
	switch s {
	case awmv1.WorkflowStatus_WORKFLOW_STATUS_RUNNING:
		return "RUNNING"
	case awmv1.WorkflowStatus_WORKFLOW_STATUS_COMPLETED:
		return "COMPLETED"
	case awmv1.WorkflowStatus_WORKFLOW_STATUS_FAILED:
		return "FAILED"
	case awmv1.WorkflowStatus_WORKFLOW_STATUS_CANCELED:
		return "CANCELED"
	case awmv1.WorkflowStatus_WORKFLOW_STATUS_SUSPENDED:
		return "SUSPENDED"
	case awmv1.WorkflowStatus_WORKFLOW_STATUS_WAITING:
		return "WAITING"
	default:
		return ""
	}
}
