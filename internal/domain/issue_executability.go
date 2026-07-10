package domain

import (
	"sort"
	"strings"
)

type IssueExecutabilityDisposition string

const (
	IssueExecutable         IssueExecutabilityDisposition = "executable"
	IssueNeedsEnrichment    IssueExecutabilityDisposition = "needs-enrichment"
	IssueNeedsDecomposition IssueExecutabilityDisposition = "needs-decomposition"
	IssueNeedsInteraction   IssueExecutabilityDisposition = "needs-interaction"
	IssuePremature          IssueExecutabilityDisposition = "premature"
)

// IssueContractProposal contains evidence-backed additions an orchestrator may
// safely apply. Existing contract text is never replaced by this policy.
type IssueContractProposal struct {
	Description             string               `json:"description,omitempty"`
	Acceptance              string               `json:"acceptance,omitempty"`
	Children                []IssueChildProposal `json:"children,omitempty"`
	Evidence                []string             `json:"evidence,omitempty"`
	MaterialUnknowns        []string             `json:"material_unknowns,omitempty"`
	RequiresProductJudgment bool                 `json:"requires_product_judgment,omitempty"`
}

type IssueChildProposal struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Acceptance  string `json:"acceptance"`
}

type IssueContractChange struct {
	Field    string   `json:"field"`
	Value    string   `json:"value"`
	Evidence []string `json:"evidence"`
}

type IssueExecutabilityAssessment struct {
	Disposition IssueExecutabilityDisposition `json:"disposition"`
	Executable  bool                          `json:"executable"`
	Signals     []string                      `json:"signals"`
	Reasons     []string                      `json:"reasons,omitempty"`
	Changes     []IssueContractChange         `json:"changes,omitempty"`
	Children    []IssueChildProposal          `json:"children,omitempty"`
}

// AssessIssueExecutability deterministically evaluates the issue contract and
// returns only additive, evidence-backed changes. Product judgment and material
// unknowns always require human interaction; neither can be inferred from prose.
func AssessIssueExecutability(task Task, blockers []string, proposal IssueContractProposal) IssueExecutabilityAssessment {
	a := IssueExecutabilityAssessment{Disposition: IssueExecutable, Executable: true}
	descriptionPresent := strings.TrimSpace(task.Description) != ""
	acceptancePresent := strings.TrimSpace(task.Acceptance) != ""

	if descriptionPresent {
		a.Signals = append(a.Signals, "scope-present")
	} else {
		a.Signals = append(a.Signals, "scope-missing")
		a.Reasons = append(a.Reasons, "missing-scope")
	}
	if acceptancePresent {
		a.Signals = append(a.Signals, "acceptance-present")
	} else {
		a.Signals = append(a.Signals, "acceptance-missing")
		a.Reasons = append(a.Reasons, "missing-acceptance")
	}
	if len(blockers) == 0 {
		a.Signals = append(a.Signals, "dependencies-ready")
	} else {
		a.Signals = append(a.Signals, "dependencies-blocked")
		a.Reasons = append(a.Reasons, prefixedSorted("blocked-by:", blockers)...)
	}

	unknowns := normalizedSorted(proposal.MaterialUnknowns)
	switch {
	case proposal.RequiresProductJudgment:
		a.Signals = append(a.Signals, "product-judgment-required")
		a.Reasons = append(a.Reasons, "product-judgment-required")
	case len(unknowns) > 0:
		a.Signals = append(a.Signals, "material-unknowns-present")
		a.Reasons = append(a.Reasons, prefixedSorted("material-unknown:", unknowns)...)
	default:
		a.Signals = append(a.Signals, "no-material-unknowns")
	}

	evidence := normalizedSorted(proposal.Evidence)
	if !proposal.RequiresProductJudgment && len(unknowns) == 0 && len(evidence) > 0 {
		if !descriptionPresent && strings.TrimSpace(proposal.Description) != "" {
			a.Changes = append(a.Changes, IssueContractChange{Field: "description", Value: strings.TrimSpace(proposal.Description), Evidence: evidence})
		}
		if !acceptancePresent && strings.TrimSpace(proposal.Acceptance) != "" {
			a.Changes = append(a.Changes, IssueContractChange{Field: "acceptance", Value: strings.TrimSpace(proposal.Acceptance), Evidence: evidence})
		}
		if childrenValid(proposal.Children) {
			a.Children = make([]IssueChildProposal, 0, len(proposal.Children))
			for _, child := range proposal.Children {
				a.Children = append(a.Children, IssueChildProposal{
					Title:       strings.TrimSpace(child.Title),
					Description: strings.TrimSpace(child.Description),
					Acceptance:  strings.TrimSpace(child.Acceptance),
				})
			}
			sort.Slice(a.Children, func(i, j int) bool {
				if a.Children[i].Title != a.Children[j].Title {
					return a.Children[i].Title < a.Children[j].Title
				}
				return a.Children[i].Description < a.Children[j].Description
			})
		} else if len(proposal.Children) > 0 {
			a.Reasons = append(a.Reasons, "decomposition-contract-incomplete")
		}
	}

	switch {
	case proposal.RequiresProductJudgment || len(unknowns) > 0:
		a.Disposition, a.Executable = IssueNeedsInteraction, false
	case len(a.Children) > 0:
		a.Disposition, a.Executable = IssueNeedsDecomposition, false
	case len(a.Changes) > 0:
		a.Disposition, a.Executable = IssueNeedsEnrichment, false
	case !descriptionPresent || !acceptancePresent:
		a.Disposition, a.Executable = IssuePremature, false
	case len(blockers) > 0:
		a.Disposition, a.Executable = IssuePremature, false
	}
	return a
}

func childrenValid(children []IssueChildProposal) bool {
	if len(children) == 0 {
		return false
	}
	for _, child := range children {
		if strings.TrimSpace(child.Title) == "" || strings.TrimSpace(child.Description) == "" || strings.TrimSpace(child.Acceptance) == "" {
			return false
		}
	}
	return true
}

func normalizedSorted(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	if len(out) < 2 {
		return out
	}
	unique := out[:1]
	for _, value := range out[1:] {
		if value != unique[len(unique)-1] {
			unique = append(unique, value)
		}
	}
	return unique
}

func prefixedSorted(prefix string, values []string) []string {
	values = normalizedSorted(values)
	for i := range values {
		values[i] = prefix + values[i]
	}
	return values
}
