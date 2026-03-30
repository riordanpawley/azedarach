package linearsync

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/riordanpawley/azedarach/internal/config"
)

// BootstrapProjectCandidate is a normalized project entry considered for sync bootstrap.
type BootstrapProjectCandidate struct {
	Name string
	Path string
}

// BootstrapProjectExclusion records why an input project was excluded.
type BootstrapProjectExclusion struct {
	Candidate BootstrapProjectCandidate
	Reason    string
}

// BootstrapProjectPolicy contains the normalized, eligible project candidates.
type BootstrapProjectPolicy struct {
	Candidates []BootstrapProjectCandidate
	Exclusions []BootstrapProjectExclusion
}

// BootstrapProjectSelection contains the deterministically ordered bootstrap set.
type BootstrapProjectSelection struct {
	Projects   []BootstrapProjectCandidate
	Exclusions []BootstrapProjectExclusion
}

// BootstrapProjectSnapshot combines the normalized policy and the final selected set.
type BootstrapProjectSnapshot struct {
	Policy    BootstrapProjectPolicy
	Selection BootstrapProjectSelection
}

// NormalizeBootstrapProjectPolicyInput canonicalizes registry inputs for bootstrap selection.
func NormalizeBootstrapProjectPolicyInput(projects []config.Project) BootstrapProjectPolicy {
	policy := BootstrapProjectPolicy{
		Candidates: make([]BootstrapProjectCandidate, 0, len(projects)),
		Exclusions: make([]BootstrapProjectExclusion, 0),
	}

	for _, project := range projects {
		candidate := BootstrapProjectCandidate{
			Name: strings.TrimSpace(project.Name),
			Path: strings.TrimSpace(project.Path),
		}

		switch {
		case candidate.Name == "" && candidate.Path == "":
			policy.Exclusions = append(policy.Exclusions, BootstrapProjectExclusion{
				Candidate: candidate,
				Reason:    "missing project name and path",
			})
		case candidate.Name == "":
			policy.Exclusions = append(policy.Exclusions, BootstrapProjectExclusion{
				Candidate: candidate,
				Reason:    "missing project name",
			})
		case candidate.Path == "":
			policy.Exclusions = append(policy.Exclusions, BootstrapProjectExclusion{
				Candidate: candidate,
				Reason:    "missing project path",
			})
		default:
			candidate.Path = filepath.Clean(candidate.Path)
			policy.Candidates = append(policy.Candidates, candidate)
		}
	}

	sortBootstrapProjectCandidates(policy.Candidates)
	return policy
}

// SelectBootstrapProjectSet deterministically orders and deduplicates normalized candidates.
func SelectBootstrapProjectSet(policy BootstrapProjectPolicy) BootstrapProjectSelection {
	candidates := append([]BootstrapProjectCandidate(nil), policy.Candidates...)
	sortBootstrapProjectCandidates(candidates)

	selected := make([]BootstrapProjectCandidate, 0, len(candidates))
	exclusions := make([]BootstrapProjectExclusion, 0)
	seenNames := make(map[string]struct{}, len(candidates))
	seenPaths := make(map[string]struct{}, len(candidates))

	for _, candidate := range candidates {
		if _, ok := seenPaths[candidate.Path]; ok {
			exclusions = append(exclusions, BootstrapProjectExclusion{
				Candidate: candidate,
				Reason:    "duplicate project path",
			})
			continue
		}
		if _, ok := seenNames[candidate.Name]; ok {
			exclusions = append(exclusions, BootstrapProjectExclusion{
				Candidate: candidate,
				Reason:    "duplicate project name",
			})
			continue
		}

		seenPaths[candidate.Path] = struct{}{}
		seenNames[candidate.Name] = struct{}{}
		selected = append(selected, candidate)
	}

	return BootstrapProjectSelection{
		Projects:   selected,
		Exclusions: exclusions,
	}
}

// NewBootstrapProjectSnapshot combines the normalized policy and selected set for reporting.
func NewBootstrapProjectSnapshot(policy BootstrapProjectPolicy, selection BootstrapProjectSelection) BootstrapProjectSnapshot {
	return BootstrapProjectSnapshot{
		Policy:    policy,
		Selection: selection,
	}
}

// String renders a compact, deterministic snapshot report for diagnostics.
func (s BootstrapProjectSnapshot) String() string {
	var b strings.Builder

	b.WriteString("bootstrap project set\n")
	b.WriteString(fmt.Sprintf("  candidates: %d\n", len(s.Policy.Candidates)))
	for _, candidate := range s.Policy.Candidates {
		b.WriteString(fmt.Sprintf("  candidate: name=%s path=%s\n", candidate.Name, candidate.Path))
	}

	if len(s.Policy.Exclusions) > 0 {
		b.WriteString("  normalization exclusions:\n")
		for _, exclusion := range s.Policy.Exclusions {
			b.WriteString(fmt.Sprintf("    - name=%s path=%s reason=%s\n", exclusion.Candidate.Name, exclusion.Candidate.Path, exclusion.Reason))
		}
	}

	b.WriteString(fmt.Sprintf("  selected: %d\n", len(s.Selection.Projects)))
	for _, candidate := range s.Selection.Projects {
		b.WriteString(fmt.Sprintf("  selected: name=%s path=%s\n", candidate.Name, candidate.Path))
	}

	if len(s.Selection.Exclusions) > 0 {
		b.WriteString("  selection exclusions:\n")
		for _, exclusion := range s.Selection.Exclusions {
			b.WriteString(fmt.Sprintf("    - name=%s path=%s reason=%s\n", exclusion.Candidate.Name, exclusion.Candidate.Path, exclusion.Reason))
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

func sortBootstrapProjectCandidates(candidates []BootstrapProjectCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Path != candidates[j].Path {
			return candidates[i].Path < candidates[j].Path
		}
		if candidates[i].Name != candidates[j].Name {
			return candidates[i].Name < candidates[j].Name
		}
		return false
	})
}
