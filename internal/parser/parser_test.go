package parser_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enriquepascalin/awm-orchestrator/internal/model"
	"github.com/enriquepascalin/awm-orchestrator/internal/parser"
)

func TestParse_ValidWorkflow(t *testing.T) {
	yamlContent := `
id: test-workflow
name: Test Workflow
version: "1.0"
start: hello
states:
  - name: hello
    type: operation
    actions:
      - name: greet
        functionRef:
          refName: log
        arguments:
          message: "Hello"
    end: true
`
	def, err := parser.Parse([]byte(yamlContent))
	require.NoError(t, err)
	assert.Equal(t, "test-workflow", def.ID)
	assert.Equal(t, "Test Workflow", def.Name)
	assert.Equal(t, "hello", def.Start)
	assert.Len(t, def.States, 1)

	state, ok := def.States[0].(*model.OperationState)
	require.True(t, ok)
	assert.Equal(t, "hello", state.Name)
	assert.True(t, state.End())
	assert.Len(t, state.Actions, 1)
	assert.Equal(t, "greet", state.Actions[0].Name)
}

func TestParse_InvalidYAML(t *testing.T) {
	_, err := parser.Parse([]byte(`invalid: yaml: :`))
	assert.Error(t, err)
}

func TestParse_UnknownStateType(t *testing.T) {
	yamlContent := `
id: test
name: test
version: "1"
start: bad
states:
  - name: bad
    type: unknown
`
	_, err := parser.Parse([]byte(yamlContent))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown state type")
}