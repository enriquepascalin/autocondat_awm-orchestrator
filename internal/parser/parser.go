package parser

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/enriquepascalin/awm-orchestrator/internal/model"
)

func ParseFile(path string) (*model.WorkflowDefinition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	return Parse(data)
}

func Parse(data []byte) (*model.WorkflowDefinition, error) {
	var raw struct {
		ID       string                 `yaml:"id"`
		Name     string                 `yaml:"name"`
		Version  string                 `yaml:"version"`
		Start    string                 `yaml:"start"`
		States   []yaml.Node            `yaml:"states"`
		Metadata map[string]interface{} `yaml:"metadata,omitempty"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	def := &model.WorkflowDefinition{
		ID:       raw.ID,
		Name:     raw.Name,
		Version:  raw.Version,
		Start:    raw.Start,
		Metadata: raw.Metadata,
		YAML:     string(data),
	}
	for _, node := range raw.States {
		state, err := parseState(node)
		if err != nil {
			return nil, fmt.Errorf("parse state: %w", err)
		}
		def.States = append(def.States, state)
	}
	return def, nil
}

func parseState(node yaml.Node) (model.State, error) {
	var base struct {
		Name string `yaml:"name"`
		Type string `yaml:"type"`
	}
	if err := node.Decode(&base); err != nil {
		return nil, err
	}
	switch base.Type {
	case "operation":
		var s model.OperationState
		if err := node.Decode(&s); err != nil {
			return nil, err
		}
		return &s, nil
	case "delay":
		var s model.DelayState
		if err := node.Decode(&s); err != nil {
			return nil, err
		}
		return &s, nil
	default:
		return nil, fmt.Errorf("unknown state type: %s", base.Type)
	}
}