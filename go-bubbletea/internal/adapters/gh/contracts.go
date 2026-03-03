package gh

import "context"

type PullRequest struct {
	Number int
	Title  string
	URL    string
	State  string
}

type CreatePullRequestRequest struct {
	Title      string
	Body       string
	HeadBranch string
	BaseBranch string
	Draft      bool
}

type Client interface {
	CreatePullRequest(ctx context.Context, req CreatePullRequestRequest) (PullRequest, error)
	GetPullRequestByBranch(ctx context.Context, branch string) (PullRequest, error)
	MergePullRequest(ctx context.Context, number int, strategy string) error
}
