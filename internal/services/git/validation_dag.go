package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

// CandidateValidationStage is a typed, consumer-owned unit in the exact
// candidate validation graph. Resource identifiers have no product meaning.
type CandidateValidationStage struct {
	ID, Command                         string
	DependsOn, Resources, ArtifactPaths []string
	Required                            bool
}

type CandidateValidationStageResult = domain.ValidationStageEvidence

type CandidateValidationDAGResult struct {
	Stages []CandidateValidationStageResult
}

type candidateValidationDAGKey struct{}

func WithCandidateValidationDAG(ctx context.Context, stages []CandidateValidationStage) context.Context {
	return context.WithValue(ctx, candidateValidationDAGKey{}, append([]CandidateValidationStage(nil), stages...))
}

func runCandidateValidationDAG(ctx context.Context, root string, env []string, stages []CandidateValidationStage) (CandidateValidationDAGResult, error) {
	if len(stages) == 0 {
		return CandidateValidationDAGResult{}, fmt.Errorf("validation stage capability is absent")
	}
	byID := make(map[string]CandidateValidationStage, len(stages))
	for _, stage := range stages {
		stage.ID, stage.Command = strings.TrimSpace(stage.ID), strings.TrimSpace(stage.Command)
		if stage.ID == "" || stage.Command == "" {
			return CandidateValidationDAGResult{}, fmt.Errorf("validation stage requires id and command")
		}
		if _, exists := byID[stage.ID]; exists {
			return CandidateValidationDAGResult{}, fmt.Errorf("duplicate validation stage %q", stage.ID)
		}
		byID[stage.ID] = stage
	}
	for _, stage := range byID {
		for _, dep := range stage.DependsOn {
			if _, ok := byID[strings.TrimSpace(dep)]; !ok {
				return CandidateValidationDAGResult{}, fmt.Errorf("validation stage %q depends on absent capability %q", stage.ID, dep)
			}
		}
	}
	if err := validateCandidateValidationDAG(byID); err != nil {
		return CandidateValidationDAGResult{}, err
	}
	stageRoot, err := os.MkdirTemp("", "az-validation-stages-")
	if err != nil {
		return CandidateValidationDAGResult{}, fmt.Errorf("create validation stage root: %w", err)
	}
	defer os.RemoveAll(stageRoot)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type completion struct {
		result CandidateValidationStageResult
		err    error
	}
	done := make(chan completion, len(stages))
	completed, running := map[string]bool{}, map[string]bool{}
	used := map[string]bool{}
	results := make([]CandidateValidationStageResult, 0, len(stages))
	var firstErr error
	for len(completed) < len(stages) {
		ids := make([]string, 0, len(stages))
		for id := range byID {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		started := false
		for _, id := range ids {
			stage := byID[id]
			if completed[id] || running[id] || firstErr != nil {
				continue
			}
			ready := true
			for _, dep := range stage.DependsOn {
				if !completed[strings.TrimSpace(dep)] {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
			conflict := false
			for _, resource := range stage.Resources {
				if used[strings.TrimSpace(resource)] {
					conflict = true
					break
				}
			}
			if conflict {
				continue
			}
			running[id], started = true, true
			for _, resource := range stage.Resources {
				used[strings.TrimSpace(resource)] = true
			}
			go func(stage CandidateValidationStage) {
				startedAt := time.Now().UTC()
				base, mkErr := os.MkdirTemp(stageRoot, "stage-")
				tempBase := os.TempDir()
				if runtime.GOOS == "darwin" {
					tempBase = "/tmp"
				}
				shortTemp, tempErr := os.MkdirTemp(tempBase, "azv-")
				if mkErr == nil {
					mkErr = tempErr
				}
				result := CandidateValidationStageResult{ID: stage.ID, Status: "failed", Resources: append([]string(nil), stage.Resources...), OutputRoot: filepath.Join(base, "output"), TempRoot: shortTemp, ArtifactPaths: append([]string(nil), stage.ArtifactPaths...), StartedAt: &startedAt}
				if mkErr == nil {
					mkErr = os.MkdirAll(result.OutputRoot, 0o700)
				}
				for _, dir := range []string{"cache", "config", "runtime"} {
					if mkErr == nil {
						mkErr = os.MkdirAll(filepath.Join(result.TempRoot, dir), 0o700)
					}
				}
				if mkErr == nil {
					stageEnv := hermeticCandidateValidationStageEnv(env, result.TempRoot)
					result.Stdout, result.Stderr, mkErr = runProcessGroupCommand(runCtx, root, gitEnvWithOverrides(stageEnv, []string{"AZEDARACH_VALIDATION_STAGE_ID=" + stage.ID, "AZEDARACH_VALIDATION_OUTPUT_ROOT=" + result.OutputRoot}), "/bin/sh", "-lc", stage.Command)
					result.Stdout = boundedCandidateValidationDetail(result.Stdout)
					result.Stderr = boundedCandidateValidationDetail(result.Stderr)
				}
				if mkErr == nil {
					result.Status = "passed"
				} else if runCtx.Err() != nil {
					result.Status = "cancelled"
				}
				finishedAt := time.Now().UTC()
				result.FinishedAt = &finishedAt
				result.WallSeconds = finishedAt.Sub(startedAt).Seconds()
				if shortTemp != "" {
					_ = os.RemoveAll(shortTemp)
				}
				done <- completion{result: result, err: mkErr}
			}(stage)
		}
		if len(running) == 0 {
			if firstErr != nil {
				break
			}
			if !started {
				return CandidateValidationDAGResult{Stages: results}, fmt.Errorf("validation stage graph contains a cycle")
			}
		}
		completion := <-done
		stage := byID[completion.result.ID]
		delete(running, stage.ID)
		completed[stage.ID] = true
		for _, resource := range stage.Resources {
			delete(used, strings.TrimSpace(resource))
		}
		results = append(results, completion.result)
		if completion.err != nil && (stage.Required || ctx.Err() != nil) && firstErr == nil {
			firstErr = fmt.Errorf("validation stage %s: %w", stage.ID, completion.err)
			cancel()
		}
	}
	for len(running) > 0 {
		completion := <-done
		delete(running, completion.result.ID)
		completed[completion.result.ID] = true
		results = append(results, completion.result)
	}
	if firstErr != nil {
		for id := range byID {
			if !completed[id] {
				results = append(results, CandidateValidationStageResult{ID: id, Status: "blocked"})
			}
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ID < results[j].ID })
	if firstErr != nil {
		return CandidateValidationDAGResult{Stages: results}, firstErr
	}
	return CandidateValidationDAGResult{Stages: results}, nil
}

func hermeticCandidateValidationStageEnv(base []string, tempRoot string) []string {
	filtered := make([]string, 0, len(base)+4)
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if ok && strings.HasPrefix(key, "AZEDARACH_") &&
			key != "AZEDARACH_REAL_GO_BIN" &&
			!strings.HasPrefix(key, "AZEDARACH_GO_CACHE_") &&
			!strings.HasPrefix(key, "AZEDARACH_TIMING_") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return gitEnvWithOverrides(filtered, []string{
		"HOME=" + tempRoot,
		"TMPDIR=" + tempRoot,
		"XDG_CACHE_HOME=" + filepath.Join(tempRoot, "cache"),
		"XDG_CONFIG_HOME=" + filepath.Join(tempRoot, "config"),
		"XDG_RUNTIME_DIR=" + filepath.Join(tempRoot, "runtime"),
	})
}

func validateCandidateValidationDAG(stages map[string]CandidateValidationStage) error {
	const (
		unvisited = iota
		visiting
		visited
	)
	state := make(map[string]int, len(stages))
	var visit func(string) error
	visit = func(id string) error {
		switch state[id] {
		case visiting:
			return fmt.Errorf("validation stage graph contains a cycle at %q", id)
		case visited:
			return nil
		}
		state[id] = visiting
		for _, dependency := range stages[id].DependsOn {
			if err := visit(strings.TrimSpace(dependency)); err != nil {
				return err
			}
		}
		state[id] = visited
		return nil
	}
	ids := make([]string, 0, len(stages))
	for id := range stages {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}
