package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

// ImportMD reads docs/decisions/*.md, parses each file, computes a per-field
// diff against the SQLite store, and either reports the plan (Check=true) or
// applies it. Per-field conflicts (md and SQLite both non-empty and differ)
// cause that file to be skipped unless Force=true is set. Links are NOT
// reconciled in this pass — use az decision link add/remove for that.
func (s issueDecisionService) ImportMD(ctx context.Context, req protocol.DecisionImportMDRequestBody) (protocol.DecisionImportMDResponseBody, error) {
	c, err := s.client(ctx)
	if err != nil {
		return protocol.DecisionImportMDResponseBody{}, err
	}
	if s.daemon == nil {
		return protocol.DecisionImportMDResponseBody{}, errors.New("decision import_md unavailable: daemon nil")
	}
	repoDir, err := decisionMDRepoDir(s.daemon.resolveRepoDirForProject(daemonProjectIDFromContext(ctx)), req.RepoDir)
	if err != nil {
		return protocol.DecisionImportMDResponseBody{}, errors.New("decision import_md unavailable: repo dir not resolved")
	}

	sourceDir := filepath.Join(repoDir, decisionMDSubdir)
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return protocol.DecisionImportMDResponseBody{Check: req.Check, Force: req.Force}, nil
		}
		return protocol.DecisionImportMDResponseBody{}, fmt.Errorf("read %s: %w", sourceDir, err)
	}

	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		paths = append(paths, filepath.Join(sourceDir, entry.Name()))
	}
	sort.Strings(paths)

	resp := protocol.DecisionImportMDResponseBody{Check: req.Check, Force: req.Force}
	for _, path := range paths {
		fileResult := s.importOneDecisionFile(ctx, c, repoDir, path, req)
		resp.Files = append(resp.Files, fileResult)
		if fileResult.Imported {
			resp.Imported++
		}
		if len(fileResult.Conflicts) > 0 {
			resp.Conflicts++
		}
	}
	return resp, nil
}

func (s issueDecisionService) importOneDecisionFile(ctx context.Context, c *issues.Client, repoDir, path string, req protocol.DecisionImportMDRequestBody) protocol.DecisionImportMDFileResult {
	rel, _ := filepath.Rel(repoDir, path)
	if rel == "" {
		rel = path
	}
	result := protocol.DecisionImportMDFileResult{Path: rel}

	content, err := os.ReadFile(path)
	if err != nil {
		result.ParseError = err.Error()
		return result
	}
	parsed, err := parseDecisionMarkdown(content)
	if err != nil {
		result.ParseError = err.Error()
		return result
	}
	result.DecisionID = parsed.LocalID

	existing, err := c.GetDecision(ctx, parsed.LocalID)
	isNew := errors.Is(err, domain.ErrNotFound)
	if err != nil && !isNew {
		result.ApplyError = err.Error()
		return result
	}

	changes, conflicts := planDecisionImport(parsed, existing, isNew)
	result.Changes = changes
	result.Conflicts = conflicts
	result.NewRecord = isNew

	if len(conflicts) > 0 && !req.Force {
		result.Skipped = true
		return result
	}
	if req.Check {
		return result
	}
	// With --force, conflicts are applied alongside the clean changes.
	if req.Force {
		for _, c := range conflicts {
			changes = append(changes, protocol.DecisionImportMDFieldChange{
				Field:    c.Field,
				OldValue: c.SQLiteValue,
				NewValue: c.MarkdownValue,
			})
		}
	}
	if len(changes) == 0 {
		// Nothing to do; not even a no-op write.
		return result
	}

	if isNew {
		if _, err := c.ImportDecision(ctx, issues.ImportDecisionParams{
			LocalID:      parsed.LocalID,
			NumericID:    parsed.NumericID,
			Title:        parsed.Title,
			Rationale:    valueOrEmpty(parsed.Rationale),
			Context:      valueOrEmpty(parsed.Context),
			Consequences: valueOrEmpty(parsed.Consequences),
		}); err != nil {
			result.ApplyError = err.Error()
			return result
		}
	} else {
		params := updateParamsFromChanges(changes, parsed)
		if _, err := c.UpdateDecision(ctx, parsed.LocalID, params); err != nil {
			result.ApplyError = err.Error()
			return result
		}
	}
	result.Imported = true
	return result
}

// planDecisionImport walks each field, returning the list of changes that
// would apply and any conflicts that block them. The Title field is special:
// it always has a value on the markdown side (we parse it from the header),
// so on an existing decision we compare directly and flag conflicts when
// they disagree.
func planDecisionImport(parsed parsedDecisionMD, existing issues.Decision, isNew bool) ([]protocol.DecisionImportMDFieldChange, []protocol.DecisionImportMDFieldConflict) {
	var changes []protocol.DecisionImportMDFieldChange
	var conflicts []protocol.DecisionImportMDFieldConflict

	classify := func(field, sqliteVal string, mdVal *string, mdRequired bool) {
		var have string
		var present bool
		if mdRequired {
			have = strings.TrimSpace(parsed.Title) // title is set from header
			present = have != ""
		} else if mdVal != nil {
			have = strings.TrimSpace(*mdVal)
			present = true
		}
		_ = have // silence linter when mdRequired branch unused; immediately reassigned below
		if !present {
			return // md doesn't carry this field → leave SQLite alone
		}
		if !mdRequired {
			have = strings.TrimSpace(*mdVal)
		}
		oldVal := strings.TrimSpace(sqliteVal)
		if have == oldVal {
			return // no-op
		}
		if !isNew && oldVal != "" && have != "" && oldVal != have {
			conflicts = append(conflicts, protocol.DecisionImportMDFieldConflict{
				Field:         field,
				SQLiteValue:   oldVal,
				MarkdownValue: have,
			})
			return
		}
		changes = append(changes, protocol.DecisionImportMDFieldChange{
			Field:    field,
			OldValue: oldVal,
			NewValue: have,
		})
	}

	classify("title", existing.Title, &parsed.Title, true)
	classify("rationale", existing.Rationale, parsed.Rationale, false)
	classify("context", existing.Context, parsed.Context, false)
	classify("consequences", existing.Consequences, parsed.Consequences, false)
	return changes, conflicts
}

// updateParamsFromChanges turns the planner's per-field change list into the
// shape UpdateDecision expects. Conflicts are not included unless the caller
// already passed --force (the planner reclassifies them as changes in that
// case).
func updateParamsFromChanges(changes []protocol.DecisionImportMDFieldChange, parsed parsedDecisionMD) issues.UpdateDecisionParams {
	params := issues.UpdateDecisionParams{}
	for _, c := range changes {
		v := c.NewValue
		switch c.Field {
		case "title":
			params.Title = &v
		case "rationale":
			params.Rationale = &v
		case "context":
			params.Context = &v
		case "consequences":
			params.Consequences = &v
		}
	}
	_ = parsed
	return params
}

func valueOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
