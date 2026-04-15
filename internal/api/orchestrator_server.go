package api

import (
	"context"
	"io"
	"log"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	awmv1 "github.com/enriquepascalin/awm-orchestrator/internal/proto/awm/v1"
	"github.com/enriquepascalin/awm-orchestrator/internal/runtime"
	"github.com/enriquepascalin/awm-orchestrator/internal/supervisor"
)

type OrchestratorServer struct {
	awmv1.UnimplementedOrchestratorServer
	engine     *runtime.Engine
	supervisor *supervisor.Supervisor
}

func NewOrchestratorServer(engine *runtime.Engine, sup *supervisor.Supervisor) *OrchestratorServer {
	return &OrchestratorServer{engine: engine, supervisor: sup}
}

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
			// Update agent liveness (could be stored in Redis)
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

func (s *OrchestratorServer) ExtendTaskLease(ctx context.Context, req *awmv1.ExtendTaskLeaseRequest) (*awmv1.ExtendTaskLeaseResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "ExtendTaskLease not implemented")
}

func (s *OrchestratorServer) Heartbeat(ctx context.Context, req *awmv1.HeartbeatRequest) (*awmv1.HeartbeatResponse, error) {
	return &awmv1.HeartbeatResponse{Ok: true}, nil
}

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
		Status:               awmv1.WorkflowStatus(awmv1.WorkflowStatus_value[instance.Status]),
		DimensionalState:     stateStruct,
		CreatedAt:            timestamppb.New(instance.CreatedAt),
		UpdatedAt:            timestamppb.New(instance.UpdatedAt),
	}, nil
}

func (s *OrchestratorServer) ListWorkflows(ctx context.Context, req *awmv1.ListWorkflowsRequest) (*awmv1.ListWorkflowsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "ListWorkflows not implemented")
}

func (s *OrchestratorServer) StreamWorkflowEvents(req *awmv1.StreamWorkflowEventsRequest, stream awmv1.Orchestrator_StreamWorkflowEventsServer) error {
	return status.Errorf(codes.Unimplemented, "StreamWorkflowEvents not implemented")
}