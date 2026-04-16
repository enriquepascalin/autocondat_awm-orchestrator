package api_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/enriquepascalin/awm-orchestrator/internal/api"
	awmv1 "github.com/enriquepascalin/awm-orchestrator/internal/proto/awm/v1"
)

func TestPublicServer_CreateWorkflowDefinition_InvalidYAML(t *testing.T) {
	// NewPublicServer expects 5 arguments: engine, registry, sup, st, db
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
