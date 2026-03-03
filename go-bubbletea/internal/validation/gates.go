package validation

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// GateName identifies a quality gate.
type GateName string

const (
	GateUnit        GateName = "unit"
	GateIntegration GateName = "integration"
	GateE2E         GateName = "e2e"
	GateSnapshot    GateName = "snapshot"
	GateBuild       GateName = "build"
)

var defaultGateOrder = []GateName{GateUnit, GateIntegration, GateE2E, GateSnapshot, GateBuild}

// GateDefinition describes how to run one gate.
type GateDefinition struct {
	Name     GateName
	Command  string
	Args     []string
	Required bool
}

// GateResult is one gate execution outcome.
type GateResult struct {
	Name      GateName
	StartedAt time.Time
	EndedAt   time.Time
	Passed    bool
	Required  bool
	Output    string
	Err       error
}

// ChecklistItem represents a completion step that must be satisfied.
type ChecklistItem struct {
	Name   GateName
	Done   bool
	Detail string
}

// Report is the full gate run output plus completion checklist.
type Report struct {
	Results         []GateResult
	Checklist       []ChecklistItem
	ReadyToComplete bool
}

// CommandRunner runs external commands.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// ExecRunner is the default command runner for gate execution.
type ExecRunner struct{}

// Run executes command and returns combined output.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	output := strings.TrimSpace(stdout.String())
	errText := strings.TrimSpace(stderr.String())
	if errText != "" {
		if output == "" {
			output = errText
		} else {
			output = output + "\n" + errText
		}
	}

	if err != nil {
		return output, fmt.Errorf("%s %s failed: %w", name, strings.Join(args, " "), err)
	}

	return output, nil
}

// GateRunner executes the configured validation gates.
type GateRunner struct {
	runner CommandRunner
	gates  map[GateName]GateDefinition
}

// NewGateRunner creates a gate runner with optional custom gate definitions.
func NewGateRunner(runner CommandRunner, defs ...GateDefinition) *GateRunner {
	if runner == nil {
		runner = ExecRunner{}
	}

	if len(defs) == 0 {
		defs = defaultGateDefinitions()
	}

	gates := make(map[GateName]GateDefinition, len(defs))
	for _, def := range defs {
		gates[def.Name] = def
	}

	return &GateRunner{runner: runner, gates: gates}
}

// Run executes gates in deterministic order and generates completion checklist.
func (r *GateRunner) Run(ctx context.Context, selected ...GateName) Report {
	selectedSet := make(map[GateName]bool)
	if len(selected) == 0 {
		for _, name := range defaultGateOrder {
			selectedSet[name] = true
		}
	} else {
		for _, name := range selected {
			selectedSet[name] = true
		}
	}

	results := make([]GateResult, 0, len(selectedSet))
	for _, gateName := range defaultGateOrder {
		if !selectedSet[gateName] {
			continue
		}

		def, ok := r.gates[gateName]
		if !ok {
			results = append(results, GateResult{
				Name:     gateName,
				Passed:   false,
				Required: true,
				Err:      fmt.Errorf("missing gate definition for %s", gateName),
			})
			continue
		}

		started := time.Now()
		output, err := r.runner.Run(ctx, def.Command, def.Args...)
		ended := time.Now()

		results = append(results, GateResult{
			Name:      def.Name,
			StartedAt: started,
			EndedAt:   ended,
			Passed:    err == nil,
			Required:  def.Required,
			Output:    output,
			Err:       err,
		})
	}

	report := Report{Results: results}
	report.Checklist = BuildCompletionChecklist(report)
	report.ReadyToComplete = true
	for _, item := range report.Checklist {
		if !item.Done {
			report.ReadyToComplete = false
			break
		}
	}

	return report
}

// BuildCompletionChecklist translates gate results into completion steps.
func BuildCompletionChecklist(report Report) []ChecklistItem {
	resultByName := make(map[GateName]GateResult, len(report.Results))
	for _, result := range report.Results {
		resultByName[result.Name] = result
	}

	items := make([]ChecklistItem, 0, len(defaultGateOrder))
	for _, gateName := range defaultGateOrder {
		result, ok := resultByName[gateName]
		if !ok {
			items = append(items, ChecklistItem{Name: gateName, Done: false, Detail: "not run"})
			continue
		}

		detail := "passed"
		if !result.Passed {
			detail = "failed"
			if result.Err != nil {
				detail = result.Err.Error()
			}
		}

		items = append(items, ChecklistItem{Name: gateName, Done: result.Passed, Detail: detail})
	}

	return items
}

func defaultGateDefinitions() []GateDefinition {
	return []GateDefinition{
		{Name: GateUnit, Command: "go", Args: []string{"test", "./...", "-run", "Unit"}, Required: true},
		{Name: GateIntegration, Command: "go", Args: []string{"test", "./...", "-run", "Integration"}, Required: true},
		{Name: GateE2E, Command: "go", Args: []string{"test", "./...", "-run", "E2E"}, Required: true},
		{Name: GateSnapshot, Command: "go", Args: []string{"test", "./...", "-run", "Snapshot"}, Required: true},
		{Name: GateBuild, Command: "go", Args: []string{"build", "./..."}, Required: true},
	}
}
