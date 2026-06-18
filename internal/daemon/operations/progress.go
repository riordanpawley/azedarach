package operations

import "context"

type progressReporterKey struct{}

// Progress captures durable, user-visible progress for a long-running daemon operation.
type Progress struct {
	Phase   string
	Message string
	Current int64
	Total   int64
	Unit    string
	Percent int
}

type ProgressReporter func(context.Context, Progress) error

func WithProgressReporter(ctx context.Context, reporter ProgressReporter) context.Context {
	if ctx == nil || reporter == nil {
		return ctx
	}
	return context.WithValue(ctx, progressReporterKey{}, reporter)
}

func ReportProgress(ctx context.Context, progress Progress) error {
	if ctx == nil {
		return nil
	}
	reporter, ok := ctx.Value(progressReporterKey{}).(ProgressReporter)
	if !ok || reporter == nil {
		return nil
	}
	return reporter(ctx, progress)
}
