package api_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	awmv1 "github.com/enriquepascalin/awm-orchestrator/internal/proto/awm/v1"
	"github.com/enriquepascalin/awm-orchestrator/internal/api"
	"github.com/enriquepascalin/awm-orchestrator/internal/model"
)

type mockRegistry struct {
	getErr error
}

func (m *mockRegistry) Get(ctx context.Context, id string) (*model.WorkflowDefinition, error) {
	return nil, m.getErr
}

func (m *mockRegistry) Store(ctx context.Context, def *model.WorkflowDefinition) error {
	return nil
}

func TestPublicServer_CreateWorkflowDefinition_InvalidYAML(t *testing.T) {
	server := api.NewPublicServer(nil, nil, nil, nil, nil)
	req := &awmv1.CreateWorkflowDefinitionRequest{
		Tenant:      "acme",
		Name:        "Test",
		YamlContent: "invalid: yaml: :",
	}
	_, err := server.CreateWorkflowDefinition(context.Background(), req)
	assert.Error(t, err)
	st, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestPublicServer_StartWorkflow_DefinitionNotFound(t *testing.T) {
	registry := &mockRegistry{getErr: assert.AnError}
	server := api.NewPublicServer(nil, registry, nil, nil, nil)
	req := &awmv1.StartWorkflowRequest{
		WorkflowDefinitionId: "missing",
		Tenant:               "acme",
	}
	_, err := server.StartWorkflow(context.Background(), req)
	assert.Error(t, err)
	st, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}