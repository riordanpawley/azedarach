package git

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/services/network"
	"github.com/stretchr/testify/require"
)

type pollRunner struct {
	outputs map[string]struct {
		output string
		err    error
	}
}

func (r *pollRunner) Run(ctx context.Context, args ...string) (string, error) {
	key := joinArgs(args)
	if res, ok := r.outputs[key]; ok {
		return res.output, res.err
	}
	return "", nil
}

func joinArgs(args []string) string {
	if len(args) == 0 {
		return ""
	}

	key := args[0]
	for _, arg := range args[1:] {
		key += " " + arg
	}
	return key
}

func newPollService(runner CommandRunner, checker *network.StatusChecker) *GitSyncService {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{Git: config.GitConfig{WorkflowMode: "origin", BaseBranch: "main"}}
	service := NewGitSyncService(NewClient(runner, logger), checker, cfg, "/tmp/worktree", logger)
	service.commitsBehind = 9
	return service
}

func TestFetchAndCheck_PreservesLastKnownCountOnRevListError(t *testing.T) {
	runner := &pollRunner{outputs: map[string]struct {
		output string
		err    error
	}{
		"fetch origin":                       {},
		"rev-list --count main..origin/main": {err: errors.New("rev-list failed")},
	}}
	service := newPollService(runner, network.NewStatusChecker())

	msg, ok := service.FetchAndCheck()().(GitSyncMsg)
	require.True(t, ok)
	require.Error(t, msg.Err)
	require.Equal(t, 9, msg.CommitsBehind)
	require.Equal(t, 9, service.commitsBehind)
}

func TestFetchAndCheck_UpdatesCountOnSuccess(t *testing.T) {
	runner := &pollRunner{outputs: map[string]struct {
		output string
		err    error
	}{
		"fetch origin":                       {},
		"rev-list --count main..origin/main": {output: "12"},
	}}
	service := newPollService(runner, network.NewStatusChecker())

	msg, ok := service.FetchAndCheck()().(GitSyncMsg)
	require.True(t, ok)
	require.NoError(t, msg.Err)
	require.Equal(t, 12, msg.CommitsBehind)
	require.Equal(t, 12, service.commitsBehind)
}
