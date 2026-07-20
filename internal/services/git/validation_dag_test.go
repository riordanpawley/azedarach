package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCandidateValidationDAGRunsPortableCommandsWithIsolatedRoots(t *testing.T) {
	root := t.TempDir()
	result, err := runCandidateValidationDAG(context.Background(), root, os.Environ(), []CandidateValidationStage{
		{ID: "compile", Command: `test -n "$TMPDIR" && test -n "$AZEDARACH_VALIDATION_OUTPUT_ROOT" && printf compiled > "$AZEDARACH_VALIDATION_OUTPUT_ROOT/result"`, Required: true},
		{ID: "inspect", Command: `printf inspected > "$AZEDARACH_VALIDATION_OUTPUT_ROOT/result"`, DependsOn: []string{"compile"}, Required: true},
	})
	require.NoError(t, err)
	require.Len(t, result.Stages, 2)
	assert.NotEqual(t, result.Stages[0].OutputRoot, result.Stages[1].OutputRoot)
	for _, stage := range result.Stages {
		body, readErr := os.ReadFile(filepath.Join(stage.OutputRoot, "result"))
		require.NoError(t, readErr)
		assert.NotEmpty(t, body)
		assert.Equal(t, "passed", stage.Status)
	}
}

func TestCandidateValidationDAGFailsClosedAndRetainsEveryStartedStageResult(t *testing.T) {
	result, err := runCandidateValidationDAG(context.Background(), t.TempDir(), os.Environ(), []CandidateValidationStage{
		{ID: "failure", Command: `printf diagnostic >&2; exit 23`, Required: true},
		{ID: "downstream", Command: `exit 0`, DependsOn: []string{"failure"}, Required: true},
	})
	require.ErrorContains(t, err, "validation stage failure")
	require.Len(t, result.Stages, 2)
	assert.Equal(t, "downstream", result.Stages[0].ID)
	assert.Equal(t, "blocked", result.Stages[0].Status)
	assert.Equal(t, "failure", result.Stages[1].ID)
	assert.Contains(t, result.Stages[1].Stderr, "diagnostic")
}

func TestCandidateValidationDAGRejectsAbsentCapabilityAndCycle(t *testing.T) {
	_, err := runCandidateValidationDAG(context.Background(), t.TempDir(), os.Environ(), []CandidateValidationStage{{ID: "consumer", Command: "true", DependsOn: []string{"missing"}}})
	require.ErrorContains(t, err, "absent capability")
	_, err = runCandidateValidationDAG(context.Background(), t.TempDir(), os.Environ(), []CandidateValidationStage{{ID: "one", Command: "true", DependsOn: []string{"two"}}, {ID: "two", Command: "true", DependsOn: []string{"one"}}})
	require.ErrorContains(t, err, "contains a cycle")
}

func TestCandidateValidationDAGFailsClosedForInjectedFailureInEveryRequiredStage(t *testing.T) {
	ids := []string{"prepare", "verify", "package"}
	for _, failedID := range ids {
		t.Run(failedID, func(t *testing.T) {
			stages := make([]CandidateValidationStage, 0, len(ids))
			for index, id := range ids {
				command := "true"
				if id == failedID {
					command = "exit 41"
				}
				stage := CandidateValidationStage{ID: id, Command: command, Required: true}
				if index > 0 {
					stage.DependsOn = []string{ids[index-1]}
				}
				stages = append(stages, stage)
			}
			result, err := runCandidateValidationDAG(context.Background(), t.TempDir(), os.Environ(), stages)
			require.ErrorContains(t, err, "validation stage "+failedID)
			require.Len(t, result.Stages, len(ids))
			for _, stage := range result.Stages {
				if stage.ID == failedID {
					assert.Equal(t, "failed", stage.Status)
				}
			}
		})
	}
}

func TestCandidateValidationDAGCancellationFailsClosedEvenForOptionalStage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := runCandidateValidationDAG(ctx, t.TempDir(), os.Environ(), []CandidateValidationStage{{ID: "diagnostic", Command: "true"}})
	require.Error(t, err)
	require.Len(t, result.Stages, 1)
	assert.Equal(t, "cancelled", result.Stages[0].Status)
}

func TestCandidateValidationDAGSerializesSharedOpaqueResource(t *testing.T) {
	root := t.TempDir()
	command := `mkdir active && trap 'rmdir active' EXIT && test ! -e overlap`
	result, err := runCandidateValidationDAG(context.Background(), root, os.Environ(), []CandidateValidationStage{
		{ID: "alpha", Command: command, Resources: []string{"consumer-machine"}, Required: true},
		{ID: "beta", Command: command, Resources: []string{"consumer-machine"}, Required: true},
	})
	require.NoError(t, err)
	require.Len(t, result.Stages, 2)
	assert.Equal(t, "alpha", result.Stages[0].ID)
	assert.Equal(t, "beta", result.Stages[1].ID)
}
