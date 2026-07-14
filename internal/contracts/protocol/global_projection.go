package protocol

import (
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

const (
	CommandGlobalSnapshot                = "global.snapshot"
	CommandGlobalProjectionRebuild       = "global.projection.rebuild"
	GlobalProjectionSchemaVersion        = 2
	GlobalProjectionFreshnessFresh       = "fresh"
	GlobalProjectionFreshnessStale       = "stale"
	GlobalProjectionFreshnessUnavailable = "unavailable"
)

// GlobalViewConsumer identifies a durable user-level view selection. Keep this
// closed set shared by every consumer so misspellings cannot silently create a
// second default in the user database.
type GlobalViewConsumer string

const (
	GlobalViewConsumerBoard        GlobalViewConsumer = "global_board"
	GlobalViewConsumerTmuxSelector GlobalViewConsumer = "tmux_selector"
	GlobalViewConsumerSearch       GlobalViewConsumer = "search"
	GlobalViewConsumerReview       GlobalViewConsumer = "review"
)

func (c GlobalViewConsumer) Valid() bool {
	switch c {
	case GlobalViewConsumerBoard, GlobalViewConsumerTmuxSelector, GlobalViewConsumerSearch, GlobalViewConsumerReview:
		return true
	default:
		return false
	}
}

type GlobalSnapshotRequestBody struct {
	Query          string             `json:"query,omitempty"`
	ViewID         string             `json:"view_id,omitempty"`
	Consumer       GlobalViewConsumer `json:"consumer,omitempty"`
	Scope          GlobalViewScope    `json:"scope,omitempty"`
	HydrateTaskIDs []ScopedIssueID    `json:"hydrate_task_ids,omitempty"`
}

type GlobalViewScopeKind string

const (
	GlobalViewScopeAllProjects      GlobalViewScopeKind = "all_projects"
	GlobalViewScopeSelectedProjects GlobalViewScopeKind = "selected_projects"
	GlobalViewScopeCurrentProject   GlobalViewScopeKind = "current_project"
)

type GlobalViewScope struct {
	Kind             GlobalViewScopeKind `json:"kind,omitempty"`
	ProjectIDs       []naming.ProjectID  `json:"project_ids,omitempty"`
	CurrentProjectID naming.ProjectID    `json:"current_project_id,omitempty"`
}

// GlobalViewRecord is the persisted user-level definition. Scope is part of
// the definition rather than caller state so the same configured view behaves
// consistently in the TUI, selector, search, and review consumers.
type GlobalViewRecord struct {
	View  domain.BoardView `json:"view" msgpack:"view"`
	Scope GlobalViewScope  `json:"scope" msgpack:"scope"`
}

func (s GlobalViewScope) Validate() error {
	switch s.Kind {
	case "", GlobalViewScopeAllProjects:
		if len(s.ProjectIDs) > 0 || strings.TrimSpace(s.CurrentProjectID.String()) != "" {
			return fmt.Errorf("all_projects scope cannot include project ids")
		}
		return nil
	case GlobalViewScopeSelectedProjects:
		if len(s.ProjectIDs) == 0 {
			return fmt.Errorf("selected_projects scope requires project_ids")
		}
		if strings.TrimSpace(s.CurrentProjectID.String()) != "" {
			return fmt.Errorf("selected_projects scope cannot include current_project_id")
		}
		return nil
	case GlobalViewScopeCurrentProject:
		if strings.TrimSpace(s.CurrentProjectID.String()) == "" {
			return fmt.Errorf("current_project scope requires current_project_id")
		}
		if len(s.ProjectIDs) > 0 {
			return fmt.Errorf("current_project scope cannot include project_ids")
		}
		return nil
	default:
		return fmt.Errorf("invalid global view scope %q", s.Kind)
	}
}

type ScopedIssueID struct {
	ProjectID naming.ProjectID `json:"project_id"`
	IssueID   naming.IssueID   `json:"issue_id"`
}

type GlobalViewProjectedItem struct {
	Identity           ScopedIssueID                 `json:"identity"`
	Task               domain.Task                   `json:"task"`
	GroupID            domain.BoardColumnID          `json:"group_id"`
	Depth              int                           `json:"depth,omitempty"`
	OrchestrationState domain.OrchestrationViewState `json:"orchestration_state,omitempty"`
}

type GlobalViewProjectedGroup struct {
	GroupID domain.BoardColumnID `json:"group_id"`
	TaskIDs []ScopedIssueID      `json:"task_ids"`
}

type GlobalViewProjection struct {
	View         domain.BoardView           `json:"view"`
	Groups       []GlobalViewProjectedGroup `json:"groups"`
	Items        []GlobalViewProjectedItem  `json:"items"`
	KnownTaskIDs []ScopedIssueID            `json:"known_task_ids"`
}

type GlobalProjectSnapshot struct {
	ProjectID         string        `json:"project_id"`
	Name              string        `json:"name"`
	Path              string        `json:"path"`
	DBPath            string        `json:"db_path"`
	SchemaVersion     int           `json:"schema_version"`
	SchemaFingerprint string        `json:"schema_fingerprint"`
	ProjectionVersion int           `json:"projection_version"`
	Checkpoint        uint64        `json:"checkpoint"`
	Freshness         string        `json:"freshness"`
	LastRefreshedAt   *time.Time    `json:"last_refreshed_at,omitempty"`
	LastAttemptAt     *time.Time    `json:"last_attempt_at,omitempty"`
	LastError         string        `json:"last_error,omitempty"`
	Registered        bool          `json:"registered"`
	Tasks             []domain.Task `json:"tasks"`
}

type GlobalSnapshotResponseBody struct {
	SchemaVersion int                     `json:"schema_version"`
	GeneratedAt   time.Time               `json:"generated_at"`
	Partial       bool                    `json:"partial"`
	Projects      []GlobalProjectSnapshot `json:"projects"`
	Projection    GlobalViewProjection    `json:"projection"`
}
