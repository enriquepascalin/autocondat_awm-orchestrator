package model

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// iso8601Re matches ISO 8601 durations: P[nD][T[nH][nM][n[.n]S]]
var iso8601Re = regexp.MustCompile(`^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+(?:\.\d+)?)S)?)?$`)

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

// ParseDuration parses an ISO 8601 duration string (e.g. "PT10S", "PT48H", "P1DT2H30M")
// or a Go duration string (e.g. "10s", "1h30m") into a time.Duration.
func ParseDuration(iso string) (time.Duration, error) {
	// Try Go format first so existing tests and configs keep working.
	if d, err := time.ParseDuration(iso); err == nil {
		return d, nil
	}
	m := iso8601Re.FindStringSubmatch(iso)
	if m == nil {
		return 0, fmt.Errorf("invalid duration %q: expected ISO 8601 (e.g. PT10S) or Go format (e.g. 10s)", iso)
	}
	if m[1] == "" && m[2] == "" && m[3] == "" && m[4] == "" {
		return 0, fmt.Errorf("invalid duration %q: no time components found", iso)
	}
	var d time.Duration
	if m[1] != "" {
		n, _ := strconv.Atoi(m[1])
		d += time.Duration(n) * 24 * time.Hour
	}
	if m[2] != "" {
		n, _ := strconv.Atoi(m[2])
		d += time.Duration(n) * time.Hour
	}
	if m[3] != "" {
		n, _ := strconv.Atoi(m[3])
		d += time.Duration(n) * time.Minute
	}
	if m[4] != "" {
		f, _ := strconv.ParseFloat(m[4], 64)
		d += time.Duration(f * float64(time.Second))
	}
	return d, nil
}