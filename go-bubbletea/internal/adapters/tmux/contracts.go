package tmux

import "context"

type Client interface {
	HasSession(ctx context.Context, name string) (bool, error)
	NewSession(ctx context.Context, name string, workdir string) error
	KillSession(ctx context.Context, name string) error
	CapturePane(ctx context.Context, name string, lines int) (string, error)
}
