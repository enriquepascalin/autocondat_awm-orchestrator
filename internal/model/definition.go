package model

import "time"

// WorkflowDefinition represents a parsed workflow YAML.
type WorkflowDefinition struct {
	ID       string                 `yaml:"id"`
	Name     string                 `yaml:"name"`
	Version  string                 `yaml:"version"`
	Start    string                 `yaml:"start"`
	States   []State                `yaml:"states"`
	Metadata map[string]interface{} `yaml:"metadata,omitempty"`
	YAML     string                 `json:"-" yaml:"-"`
	Tenant   string                 `json:"tenant,omitempty"` // Added for DB storage
}

// State is the interface for all state types.
type State interface {
	GetName() string
	GetType() string
	GetTransition() string
	End() bool
}

// BaseState contains common fields.
type BaseState struct {
	Name       string `yaml:"name"`
	Type       string `yaml:"type"`
	Transition string `yaml:"transition,omitempty"`
	EndFlag    bool   `yaml:"end,omitempty"`
}

func (b BaseState) GetName() string       { return b.Name }
func (b BaseState) GetType() string       { return b.Type }
func (b BaseState) GetTransition() string { return b.Transition }
func (b BaseState) End() bool             { return b.EndFlag }

// OperationState executes a task.
type OperationState struct {
	BaseState `yaml:",inline"`
	Actions   []Action `yaml:"actions"`
}

// DelayState pauses execution.
type DelayState struct {
	BaseState `yaml:",inline"`
	Duration  string `yaml:"duration"`
}

// Action represents a task to be executed.
type Action struct {
	Name        string                 `yaml:"name"`
	FunctionRef FunctionRef            `yaml:"functionRef"`
	Arguments   map[string]interface{} `yaml:"arguments,omitempty"`
}

// FunctionRef references a function definition.
type FunctionRef struct {
	RefName   string                 `yaml:"refName"`
	Arguments map[string]interface{} `yaml:"arguments,omitempty"`
}

// ParseDuration converts ISO 8601 string to time.Duration.
func ParseDuration(iso string) (time.Duration, error) {
	return time.ParseDuration(iso)
}