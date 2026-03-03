package startup

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// ToolChecker resolves executable names in PATH.
type ToolChecker interface {
	LookPath(name string) (string, error)
}

// ToolRequirement declares startup tool dependencies.
type ToolRequirement struct {
	Name     string
	Required bool
}

// ToolCheckResult captures startup availability for one tool.
type ToolCheckResult struct {
	Name      string
	Required  bool
	Available bool
	Path      string
	Error     string
}

// StartupHealthReport describes startup readiness and degraded-mode state.
type StartupHealthReport struct {
	Healthy         bool
	Degraded        bool
	Checks          []ToolCheckResult
	MissingRequired []string
}

// DegradedModeReport summarizes feature limitations caused by missing tools.
type DegradedModeReport struct {
	Enabled       bool
	MissingTools  []string
	DisabledAreas []string
	Message       string
}

var requiredStartupTools = []ToolRequirement{
	{Name: "bd", Required: true},
	{Name: "git", Required: true},
	{Name: "tmux", Required: true},
	{Name: "gh", Required: true},
}

type execToolChecker struct{}

func (execToolChecker) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

// CheckStartupHealth runs required-tool checks using the default checker.
func CheckStartupHealth() StartupHealthReport {
	return CheckStartupHealthWithChecker(execToolChecker{})
}

// CheckStartupHealthWithChecker runs required-tool checks with an injected checker.
func CheckStartupHealthWithChecker(checker ToolChecker) StartupHealthReport {
	results := make([]ToolCheckResult, 0, len(requiredStartupTools))
	missingRequired := make([]string, 0)

	for _, tool := range requiredStartupTools {
		path, err := checker.LookPath(tool.Name)
		result := ToolCheckResult{
			Name:      tool.Name,
			Required:  tool.Required,
			Available: err == nil,
			Path:      path,
		}

		if err != nil {
			result.Error = err.Error()
			if tool.Required {
				missingRequired = append(missingRequired, tool.Name)
			}
		}

		results = append(results, result)
	}

	healthy := len(missingRequired) == 0
	return StartupHealthReport{
		Healthy:         healthy,
		Degraded:        !healthy,
		Checks:          results,
		MissingRequired: missingRequired,
	}
}

// BuildDegradedModeReport maps missing tools to startup limitations.
func BuildDegradedModeReport(report StartupHealthReport) DegradedModeReport {
	if !report.Degraded {
		return DegradedModeReport{Enabled: false}
	}

	missing := append([]string(nil), report.MissingRequired...)
	sort.Strings(missing)

	disabledAreas := make([]string, 0, len(missing))
	for _, tool := range missing {
		switch tool {
		case "bd":
			disabledAreas = append(disabledAreas, "beads issue operations")
		case "git":
			disabledAreas = append(disabledAreas, "git and worktree operations")
		case "tmux":
			disabledAreas = append(disabledAreas, "session orchestration")
		case "gh":
			disabledAreas = append(disabledAreas, "GitHub pull request workflow")
		default:
			disabledAreas = append(disabledAreas, fmt.Sprintf("features depending on %s", tool))
		}
	}

	return DegradedModeReport{
		Enabled:       true,
		MissingTools:  missing,
		DisabledAreas: disabledAreas,
		Message: fmt.Sprintf(
			"running in degraded mode: missing tools %s",
			strings.Join(missing, ", "),
		),
	}
}
