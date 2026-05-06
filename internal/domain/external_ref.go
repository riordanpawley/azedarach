package domain

import "time"

// ExternalIssueRef maps an Azedarach-owned issue ID to a sync backend's
// provider-owned issue identity. Runtime/session/worktree paths must continue
// to use the Az issue ID, not these provider values.
type ExternalIssueRef struct {
	IssueID       string            `json:"issue_id"`
	Provider      string            `json:"provider"`
	ProviderScope string            `json:"provider_scope,omitempty"`
	RemoteKey     string            `json:"remote_key"`
	DisplayKey    string            `json:"display_key,omitempty"`
	URL           string            `json:"url,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}
