package domain

import (
	"fmt"
	"sort"
	"strings"

	"github.com/riordanpawley/azedarach/internal/naming"
)

// RootedOrchestrationAdmission identifies the requested rooted scope and the
// closest scope in its containment chain whose blockers prevent admission.
type RootedOrchestrationAdmission struct {
	RequestedRootID naming.IssueID
	BlockingRootID  naming.IssueID
	Blockers        []string
}

// Blocked reports whether rooted orchestration admission is denied.
func (a RootedOrchestrationAdmission) Blocked() bool {
	return !a.BlockingRootID.IsZero() && len(a.Blockers) > 0
}

// AssessRootedOrchestrationAdmission applies blocker semantics to the
// requested root and each ancestor in its parent-child containment chain.
func AssessRootedOrchestrationAdmission(requestedRootID naming.IssueID, byID map[naming.IssueID]Task) (RootedOrchestrationAdmission, error) {
	admission := RootedOrchestrationAdmission{RequestedRootID: requestedRootID}
	if requestedRootID.IsZero() {
		return admission, fmt.Errorf("root issue id is required")
	}
	seen := make(map[naming.IssueID]struct{}, 4)
	for currentID := requestedRootID; !currentID.IsZero(); {
		if _, duplicate := seen[currentID]; duplicate {
			return admission, fmt.Errorf("root containment cycle at %s", currentID)
		}
		seen[currentID] = struct{}{}
		current, ok := byID[currentID]
		if !ok {
			return admission, fmt.Errorf("root containment issue unavailable: %s", currentID)
		}
		if blockers := UnresolvedBlockers(current, byID); len(blockers) > 0 {
			admission.BlockingRootID = currentID
			admission.Blockers = blockers
			return admission, nil
		}
		parentID := strings.TrimSpace(TaskParentIssueID(current))
		if parentID == "" {
			return admission, nil
		}
		parsedParentID, err := naming.ParseIssueID(parentID)
		if err != nil {
			return admission, fmt.Errorf("invalid root containment parent %q for %s: %w", parentID, currentID, err)
		}
		currentID = parsedParentID
	}
	return admission, nil
}

// UnresolvedBlockers returns the task's applicable nonterminal blocking
// dependencies. Missing dependency projections remain fail-visible because
// absence is not evidence that a blocker settled.
func UnresolvedBlockers(task Task, byID map[naming.IssueID]Task) []string {
	out := make([]string, 0, 4)
	for _, dep := range task.Dependencies {
		if dep.Type != DependencyBlocks {
			continue
		}
		depTask, ok := byID[dep.ID]
		if !ok {
			out = append(out, dep.ID.String()+"(missing)")
			continue
		}
		if !depTask.IssueClosed() {
			out = append(out, dep.ID.String())
		}
	}
	sort.Strings(out)
	return out
}
