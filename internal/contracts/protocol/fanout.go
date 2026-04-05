package protocol

import (
	"encoding/json"

	"github.com/riordanpawley/azedarach/internal/naming"
)

const (
	CommandIssueFanout      = "issue.fanout"
	CommandIssueFanoutDrift = "issue.fanout.drift"
	CommandMailSend         = "mail.send"
	CommandMailList         = "mail.list"
	CommandMailWatch        = "mail.watch"
)

type FanoutCommandBody struct {
	Apply              bool            `json:"apply" msgpack:"apply"`
	RepoDir            string          `json:"repo_dir" msgpack:"repo_dir"`
	DefaultParentIssue string          `json:"default_parent_issue,omitempty" msgpack:"default_parent_issue,omitempty"`
	Spec               json.RawMessage `json:"spec" msgpack:"spec"`
}

type FanoutDriftCommandBody struct {
	IssueID  naming.IssueID `json:"issue_id" msgpack:"issue_id"`
	RepoDir  string         `json:"repo_dir" msgpack:"repo_dir"`
	Worktree string         `json:"worktree,omitempty" msgpack:"worktree,omitempty"`
}

type FanoutSpec struct {
	ParentIssue string       `json:"parent_issue" msgpack:"parent_issue"`
	Nodes       []FanoutNode `json:"nodes" msgpack:"nodes"`
}

type FanoutNode struct {
	Key         string       `json:"key" msgpack:"key"`
	Kind        string       `json:"kind" msgpack:"kind"`
	Title       string       `json:"title" msgpack:"title"`
	Description string       `json:"description" msgpack:"description"`
	Impl        []string     `json:"impl" msgpack:"impl"`
	FileBudget  []string     `json:"file_budget" msgpack:"file_budget"`
	DependsOn   []string     `json:"depends_on" msgpack:"depends_on"`
	Children    []FanoutNode `json:"children" msgpack:"children"`
}

type FanoutPlan struct {
	ParentIssue string             `json:"parent_issue" msgpack:"parent_issue"`
	NodeCount   int                `json:"node_count" msgpack:"node_count"`
	Create      []FanoutCreatePlan `json:"create" msgpack:"create"`
	Blocks      []FanoutBlocksPlan `json:"blocks" msgpack:"blocks"`
	Warnings    []string           `json:"warnings,omitempty" msgpack:"warnings,omitempty"`
}

type FanoutCreatePlan struct {
	Key        string   `json:"key" msgpack:"key"`
	Title      string   `json:"title" msgpack:"title"`
	Kind       string   `json:"kind" msgpack:"kind"`
	Parent     string   `json:"parent" msgpack:"parent"`
	Type       string   `json:"issue_type" msgpack:"issue_type"`
	Impl       []string `json:"impl" msgpack:"impl"`
	FileBudget []string `json:"file_budget,omitempty" msgpack:"file_budget,omitempty"`
}

type FanoutBlocksPlan struct {
	IssueKey     string `json:"issue_key" msgpack:"issue_key"`
	DependsOnKey string `json:"depends_on_key" msgpack:"depends_on_key"`
	Type         string `json:"type" msgpack:"type"`
}

type FanoutApplyResult struct {
	ParentIssue string            `json:"parent_issue" msgpack:"parent_issue"`
	Created     map[string]string `json:"created" msgpack:"created"`
	BlocksAdded int               `json:"blocks_added" msgpack:"blocks_added"`
}

type FanoutDriftResult struct {
	IssueID      naming.IssueID `json:"issue_id" msgpack:"issue_id"`
	Worktree     string         `json:"worktree" msgpack:"worktree"`
	FileBudget   []string       `json:"file_budget" msgpack:"file_budget"`
	ChangedFiles []string       `json:"changed_files" msgpack:"changed_files"`
	OutOfBudget  []string       `json:"out_of_budget" msgpack:"out_of_budget"`
	AdvisoryOnly bool           `json:"advisory_only" msgpack:"advisory_only"`
}

type MailSendCommandBody struct {
	RepoDir     string         `json:"repo_dir" msgpack:"repo_dir"`
	ParentIssue string         `json:"parent_issue" msgpack:"parent_issue"`
	IssueID     naming.IssueID `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	Type        string         `json:"type" msgpack:"type"`
	From        string         `json:"from,omitempty" msgpack:"from,omitempty"`
	To          string         `json:"to,omitempty" msgpack:"to,omitempty"`
	Body        string         `json:"body" msgpack:"body"`
}

type MailListCommandBody struct {
	RepoDir     string `json:"repo_dir" msgpack:"repo_dir"`
	ParentIssue string `json:"parent_issue" msgpack:"parent_issue"`
	SinceSeq    int64  `json:"since_seq,omitempty" msgpack:"since_seq,omitempty"`
	Limit       int    `json:"limit,omitempty" msgpack:"limit,omitempty"`
}

type MailWatchCommandBody struct {
	RepoDir     string `json:"repo_dir" msgpack:"repo_dir"`
	ParentIssue string `json:"parent_issue" msgpack:"parent_issue"`
	SinceSeq    int64  `json:"since_seq,omitempty" msgpack:"since_seq,omitempty"`
}

type MailEvent struct {
	Seq         int64                  `json:"seq" msgpack:"seq"`
	ParentIssue string                 `json:"parent_issue" msgpack:"parent_issue"`
	IssueID     naming.IssueID         `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	Type        string                 `json:"type" msgpack:"type"`
	From        string                 `json:"from,omitempty" msgpack:"from,omitempty"`
	To          string                 `json:"to,omitempty" msgpack:"to,omitempty"`
	Body        string                 `json:"body" msgpack:"body"`
	CreatedAt   string                 `json:"created_at" msgpack:"created_at"`
	Payload     map[string]interface{} `json:"payload,omitempty" msgpack:"payload,omitempty"`
}
