package daemonclient

import (
	"context"
	"fmt"
)

const (
	CommandSpecRequirementList   = "spec.req.list"
	CommandSpecRequirementGet    = "spec.req.get"
	CommandSpecRequirementCreate = "spec.req.create"
	CommandSpecRequirementUpdate = "spec.req.update"
	CommandSpecRequirementDelete = "spec.req.delete"
	CommandSpecLinkList          = "spec.link.list"
	CommandSpecLinkAdd           = "spec.link.add"
	CommandSpecLinkRemove        = "spec.link.remove"
	CommandSpecRead              = "spec.read"
	CommandSpecLint              = "spec.lint"
	CommandSpecParity            = "spec.parity"
	CommandSpecExport            = "spec.export"
	CommandSpecSync              = "spec.sync"
)

type SpecRequirementKind string

const (
	SpecRequirementKindFunctional SpecRequirementKind = "functional"
	SpecRequirementKindAcceptance SpecRequirementKind = "acceptance"
	SpecRequirementKindOther      SpecRequirementKind = "other"
)

type SpecLinkType string

const (
	SpecLinkTypeImplements SpecLinkType = "implements"
	SpecLinkTypeVerifies   SpecLinkType = "verifies"
	SpecLinkTypeRelates    SpecLinkType = "relates"
)

type SpecLinkFulfillmentStatus string

const (
	SpecLinkFulfillmentStatusPlanned  SpecLinkFulfillmentStatus = "planned"
	SpecLinkFulfillmentStatusPartial  SpecLinkFulfillmentStatus = "partial"
	SpecLinkFulfillmentStatusComplete SpecLinkFulfillmentStatus = "complete"
	SpecLinkFulfillmentStatusVerified SpecLinkFulfillmentStatus = "verified"
)

type SpecRequirementLookupSelector string

const (
	SpecRequirementLookupSelectorAuto         SpecRequirementLookupSelector = "auto"
	SpecRequirementLookupSelectorID           SpecRequirementLookupSelector = "id"
	SpecRequirementLookupSelectorLocalID      SpecRequirementLookupSelector = "local_id"
	SpecRequirementLookupSelectorExternalCode SpecRequirementLookupSelector = "external_code"
)

// SpecRequirement mirrors the daemon-side requirement record.
type SpecRequirement struct {
	ID           string              `json:"id"`
	LocalID      string              `json:"local_id"`
	ExternalCode *string             `json:"external_code"`
	Title        string              `json:"title"`
	Body         string              `json:"description"`
	Kind         SpecRequirementKind `json:"kind"`
	Status       string              `json:"status"`
	Priority     int                 `json:"priority"`
	CreatedAt    string              `json:"created_at"`
	UpdatedAt    string              `json:"updated_at"`
}

// SpecIssueLink mirrors the daemon-side requirement/issue link record.
type SpecIssueLink struct {
	ID                      string                    `json:"id,omitempty"`
	IssueID                 string                    `json:"issue_id"`
	RequirementID           string                    `json:"req_id"`
	RequirementLocalID      string                    `json:"requirement_local_id"`
	RequirementExternalCode *string                   `json:"requirement_external_code"`
	LinkType                SpecLinkType              `json:"role"`
	Implementations         []string                  `json:"implementations"`
	FulfillmentStatus       SpecLinkFulfillmentStatus `json:"fulfillment_status"`
	FulfillmentPercent      *int                      `json:"fulfillment_percent"`
	EvidenceNote            *string                   `json:"evidence_note"`
	CreatedAt               string                    `json:"created_at"`
	UpdatedAt               string                    `json:"updated_at"`
}

// SpecIssueRef mirrors the daemon-side issue reference record.
type SpecIssueRef struct {
	ID                 string                    `json:"id"`
	Title              *string                   `json:"title"`
	Status             *string                   `json:"status"`
	IssueType          *string                   `json:"issue_type"`
	LinkType           SpecLinkType              `json:"link_type"`
	Implementations    []string                  `json:"implementations"`
	FulfillmentStatus  SpecLinkFulfillmentStatus `json:"fulfillment_status"`
	FulfillmentPercent *int                      `json:"fulfillment_percent"`
	EvidenceNote       *string                   `json:"evidence_note"`
}

// SpecRequirementRef mirrors the daemon-side requirement reference record.
type SpecRequirementRef struct {
	ID                 string                    `json:"id"`
	LocalID            string                    `json:"local_id"`
	ExternalCode       *string                   `json:"external_code"`
	Title              string                    `json:"title"`
	Kind               SpecRequirementKind       `json:"kind"`
	LinkType           SpecLinkType              `json:"link_type"`
	Implementations    []string                  `json:"implementations"`
	FulfillmentStatus  SpecLinkFulfillmentStatus `json:"fulfillment_status"`
	FulfillmentPercent *int                      `json:"fulfillment_percent"`
	EvidenceNote       *string                   `json:"evidence_note"`
}

// SpecRequirementWithStats adds coverage counts to a requirement record.
type SpecRequirementWithStats struct {
	SpecRequirement
	LinkedIssueCount      int `json:"linked_issue_count"`
	ImplementedIssueCount int `json:"implemented_issue_count"`
}

// SpecRequirementListRequest encodes the phase-1 req list command.
type SpecRequirementListRequest struct {
	IssueID        string              `json:"issue_id,omitempty"`
	Status         string              `json:"status,omitempty"`
	RequirementIDs []string            `json:"ids,omitempty"`
	Query          string              `json:"query,omitempty"`
	Kind           SpecRequirementKind `json:"kind,omitempty"`
	Priority       *int                `json:"priority,omitempty"`
}

// SpecRequirementListResult captures the req list response body.
type SpecRequirementListResult struct {
	Requirements []SpecRequirement `json:"requirements"`
}

// SpecRequirementGetRequest encodes the phase-1 req get command.
type SpecRequirementGetRequest struct {
	RequirementID string                        `json:"id,omitempty"`
	Selector      SpecRequirementLookupSelector `json:"selector,omitempty"`
}

// SpecRequirementGetResult captures the req get response body.
type SpecRequirementGetResult struct {
	Requirement *SpecRequirement `json:"requirement"`
}

// SpecRequirementCreateRequest encodes the phase-1 req create command.
type SpecRequirementCreateRequest struct {
	RequirementID string              `json:"id,omitempty"`
	LocalID       string              `json:"local_id,omitempty"`
	ExternalCode  string              `json:"external_code,omitempty"`
	Title         string              `json:"title"`
	Description   string              `json:"description,omitempty"`
	Kind          SpecRequirementKind `json:"kind,omitempty"`
	Status        string              `json:"status,omitempty"`
	Priority      *int                `json:"priority,omitempty"`
	IssueID       string              `json:"issue_id,omitempty"`
}

// SpecRequirementCreateResult captures the req create response body.
type SpecRequirementCreateResult struct {
	Requirement SpecRequirement `json:"requirement"`
}

// SpecRequirementUpdateRequest encodes the phase-1 req update command.
type SpecRequirementUpdateRequest struct {
	RequirementID string                        `json:"id,omitempty"`
	Selector      SpecRequirementLookupSelector `json:"selector,omitempty"`
	Title         string                        `json:"title,omitempty"`
	Description   string                        `json:"description,omitempty"`
	Kind          SpecRequirementKind           `json:"kind,omitempty"`
	Status        string                        `json:"status,omitempty"`
	Priority      *int                          `json:"priority,omitempty"`
}

// SpecRequirementUpdateResult captures the req update response body.
type SpecRequirementUpdateResult struct {
	Updated     bool             `json:"updated"`
	Requirement *SpecRequirement `json:"requirement"`
}

// SpecRequirementDeleteRequest encodes the phase-1 req delete command.
type SpecRequirementDeleteRequest struct {
	RequirementID string                        `json:"id,omitempty"`
	Selector      SpecRequirementLookupSelector `json:"selector,omitempty"`
	Confirm       bool                          `json:"confirm,omitempty"`
}

// SpecRequirementDeleteResult captures the req delete response body.
type SpecRequirementDeleteResult struct {
	Deleted       bool   `json:"deleted"`
	RequirementID string `json:"id,omitempty"`
}

// SpecLinkListRequest encodes the phase-1 link list command.
type SpecLinkListRequest struct {
	IssueID             string                        `json:"issue_id,omitempty"`
	RequirementID       string                        `json:"req_id,omitempty"`
	RequirementSelector SpecRequirementLookupSelector `json:"requirement_selector,omitempty"`
	LinkIDs             []string                      `json:"ids,omitempty"`
	Implementation      string                        `json:"implementation,omitempty"`
}

// SpecLinkListResult captures the link list response body.
type SpecLinkListResult struct {
	Links []SpecIssueLink `json:"links"`
}

// SpecLinkAddRequest encodes the phase-1 link add command.
type SpecLinkAddRequest struct {
	IssueID             string                        `json:"issue_id"`
	RequirementID       string                        `json:"req_id"`
	RequirementSelector SpecRequirementLookupSelector `json:"requirement_selector,omitempty"`
	Role                string                        `json:"role,omitempty"`
	Note                string                        `json:"note,omitempty"`
	Implementations     []string                      `json:"implementations,omitempty"`
	Status              SpecLinkFulfillmentStatus     `json:"status,omitempty"`
	FulfillmentPercent  *int                          `json:"fulfillment_percent,omitempty"`
}

// SpecLinkAddResult captures the link add response body.
type SpecLinkAddResult struct {
	Added bool           `json:"added"`
	Link  *SpecIssueLink `json:"link"`
}

// SpecLinkRemoveRequest encodes the phase-1 link remove command.
type SpecLinkRemoveRequest struct {
	IssueID             string                        `json:"issue_id"`
	RequirementID       string                        `json:"req_id"`
	RequirementSelector SpecRequirementLookupSelector `json:"requirement_selector,omitempty"`
	Role                string                        `json:"role,omitempty"`
	Implementations     []string                      `json:"implementations,omitempty"`
}

// SpecLinkRemoveResult captures the link remove response body.
type SpecLinkRemoveResult struct {
	Removed bool `json:"removed"`
}

// SpecReadRequest encodes the phase-1 read command.
type SpecReadRequest struct {
	IssueID             string                        `json:"issue_id,omitempty"`
	RequirementID       string                        `json:"req_id,omitempty"`
	RequirementSelector SpecRequirementLookupSelector `json:"requirement_selector,omitempty"`
}

// SpecLintRequest encodes the phase-1 lint command.
type SpecLintRequest struct {
	Strict bool `json:"strict,omitempty"`
}

// SpecCoverageGap mirrors a coverage integrity gap.
type SpecCoverageGap struct {
	Kind          string  `json:"kind"`
	RequirementID *string `json:"requirement_id"`
	IssueID       *string `json:"issue_id"`
	Message       string  `json:"message"`
}

// SpecCoverageReport mirrors the daemon-side coverage report.
type SpecCoverageReport struct {
	Requirements                       []SpecRequirementWithStats `json:"requirements"`
	UnlinkedRequirementIDs             []string                   `json:"unlinked_requirement_ids"`
	FullyImplementedRequirementIDs     []string                   `json:"fully_implemented_requirement_ids"`
	PartiallyImplementedRequirementIDs []string                   `json:"partially_implemented_requirement_ids"`
	IntegrityGaps                      []SpecCoverageGap          `json:"integrity_gaps"`
}

// SpecReadResult captures the read response body.
type SpecReadResult struct {
	Requirements []SpecRequirement  `json:"requirements"`
	Links        []SpecIssueLink    `json:"links"`
	Coverage     SpecCoverageReport `json:"coverage"`
}

// SpecLintGapCounts mirrors the lint gap counter summary.
type SpecLintGapCounts struct {
	UnlinkedRequirement int `json:"unlinked_requirement"`
	MissingIssue        int `json:"missing_issue"`
	MissingRequirement  int `json:"missing_requirement"`
}

// SpecDiagnostic mirrors daemon-side lint diagnostics.
type SpecDiagnostic struct {
	Code     string  `json:"code"`
	Message  string  `json:"message"`
	Severity string  `json:"severity,omitempty"`
	IssueID  *string `json:"issue_id,omitempty"`
	ReqID    *string `json:"req_id,omitempty"`
	LinkID   *string `json:"link_id,omitempty"`
}

// SpecLintResult mirrors the daemon-side lint result.
type SpecLintResult struct {
	OK          bool             `json:"ok"`
	Diagnostics []SpecDiagnostic `json:"diagnostics,omitempty"`
}

// SpecParityRequest encodes the phase-1 parity command.
type SpecParityRequest struct {
	Implementation string `json:"implementation,omitempty"`
	FailOnOut      bool   `json:"fail_on_out,omitempty"`
}

// SpecParityRequirement mirrors the daemon-side parity row.
type SpecParityRequirement struct {
	ID                 string   `json:"id"`
	LocalID            string   `json:"local_id"`
	ExternalCode       *string  `json:"external_code"`
	Title              string   `json:"title"`
	ImplementsIssueIDs []string `json:"implements_issue_ids"`
	PartialIssueIDs    []string `json:"partial_issue_ids"`
	TestsIssueIDs      []string `json:"tests_issue_ids"`
	OtherIssueIDs      []string `json:"other_issue_ids"`
}

// SpecParityReport mirrors the daemon-side parity report.
type SpecParityReport struct {
	Implementation                     string                  `json:"implementation"`
	TotalRequirements                  int                     `json:"total_requirements"`
	ImplementedRequirementIDs          []string                `json:"implemented_requirement_ids"`
	PartiallyImplementedRequirementIDs []string                `json:"partially_implemented_requirement_ids"`
	TestedRequirementIDs               []string                `json:"tested_requirement_ids"`
	UncoveredRequirementIDs            []string                `json:"uncovered_requirement_ids"`
	RelatedOnlyRequirementIDs          []string                `json:"related_only_requirement_ids"`
	Requirements                       []SpecParityRequirement `json:"requirements"`
}

// SpecParityFinding mirrors daemon-side parity findings.
type SpecParityFinding struct {
	IssueID  string `json:"issue_id,omitempty"`
	ReqID    string `json:"req_id,omitempty"`
	Severity string `json:"severity,omitempty"`
	Message  string `json:"message"`
}

// SpecParityResult captures the parity response body.
type SpecParityResult struct {
	OK       bool                `json:"ok"`
	Findings []SpecParityFinding `json:"findings,omitempty"`
}

type SpecSyncRequest struct {
	Target string `json:"target"`
	Check  bool   `json:"check,omitempty"`
}

type SpecMarkdownSyncDocumentResult struct {
	Key     string `json:"key"`
	Path    string `json:"path"`
	Status  string `json:"status"`
	Changed bool   `json:"changed"`
}

type SpecMarkdownSyncResult struct {
	OutDir           string                           `json:"out_dir"`
	Check            bool                             `json:"check"`
	Ok               bool                             `json:"ok"`
	TotalDocuments   int                              `json:"total_documents"`
	ChangedDocuments int                              `json:"changed_documents"`
	Documents        []SpecMarkdownSyncDocumentResult `json:"documents"`
}

func (c *Client) ListSpecRequirements(ctx context.Context, req SpecRequirementListRequest) (SpecRequirementListResult, error) {
	var out SpecRequirementListResult
	if err := c.commandJSON(ctx, CommandSpecRequirementList, req, &out); err != nil {
		return SpecRequirementListResult{}, err
	}
	return out, nil
}

func (c *Client) GetSpecRequirement(ctx context.Context, req SpecRequirementGetRequest) (SpecRequirementGetResult, error) {
	var out SpecRequirementGetResult
	if err := c.commandJSON(ctx, CommandSpecRequirementGet, req, &out); err != nil {
		return SpecRequirementGetResult{}, err
	}
	return out, nil
}

func (c *Client) CreateSpecRequirement(ctx context.Context, req SpecRequirementCreateRequest) (SpecRequirementCreateResult, error) {
	var out SpecRequirementCreateResult
	if err := c.commandJSON(ctx, CommandSpecRequirementCreate, req, &out); err != nil {
		return SpecRequirementCreateResult{}, err
	}
	if out.Requirement.ID == "" {
		return SpecRequirementCreateResult{}, fmt.Errorf("%s returned empty requirement id", CommandSpecRequirementCreate)
	}
	return out, nil
}

func (c *Client) UpdateSpecRequirement(ctx context.Context, req SpecRequirementUpdateRequest) (SpecRequirementUpdateResult, error) {
	var out SpecRequirementUpdateResult
	if err := c.commandJSON(ctx, CommandSpecRequirementUpdate, req, &out); err != nil {
		return SpecRequirementUpdateResult{}, err
	}
	return out, nil
}

func (c *Client) DeleteSpecRequirement(ctx context.Context, req SpecRequirementDeleteRequest) (SpecRequirementDeleteResult, error) {
	var out SpecRequirementDeleteResult
	if err := c.commandJSON(ctx, CommandSpecRequirementDelete, req, &out); err != nil {
		return SpecRequirementDeleteResult{}, err
	}
	return out, nil
}

func (c *Client) ListSpecIssueLinks(ctx context.Context, req SpecLinkListRequest) (SpecLinkListResult, error) {
	var out SpecLinkListResult
	if err := c.commandJSON(ctx, CommandSpecLinkList, req, &out); err != nil {
		return SpecLinkListResult{}, err
	}
	return out, nil
}

func (c *Client) AddSpecIssueLink(ctx context.Context, req SpecLinkAddRequest) (SpecLinkAddResult, error) {
	var out SpecLinkAddResult
	if err := c.commandJSON(ctx, CommandSpecLinkAdd, req, &out); err != nil {
		return SpecLinkAddResult{}, err
	}
	return out, nil
}

func (c *Client) RemoveSpecIssueLink(ctx context.Context, req SpecLinkRemoveRequest) (SpecLinkRemoveResult, error) {
	var out SpecLinkRemoveResult
	if err := c.commandJSON(ctx, CommandSpecLinkRemove, req, &out); err != nil {
		return SpecLinkRemoveResult{}, err
	}
	return out, nil
}

func (c *Client) ReadSpec(ctx context.Context, req SpecReadRequest) (SpecReadResult, error) {
	var out SpecReadResult
	if err := c.commandJSON(ctx, CommandSpecRead, req, &out); err != nil {
		return SpecReadResult{}, err
	}
	return out, nil
}

func (c *Client) LintSpec(ctx context.Context, req SpecLintRequest) (SpecLintResult, error) {
	var out SpecLintResult
	if err := c.commandJSON(ctx, CommandSpecLint, req, &out); err != nil {
		return SpecLintResult{}, err
	}
	return out, nil
}

func (c *Client) ParitySpec(ctx context.Context, req SpecParityRequest) (SpecParityResult, error) {
	var out SpecParityResult
	if err := c.commandJSON(ctx, CommandSpecParity, req, &out); err != nil {
		return SpecParityResult{}, err
	}
	return out, nil
}

func (c *Client) ExportSpecMarkdown(ctx context.Context, req SpecSyncRequest) (SpecMarkdownSyncResult, error) {
	var out SpecMarkdownSyncResult
	if err := c.commandJSON(ctx, CommandSpecExport, req, &out); err != nil {
		return SpecMarkdownSyncResult{}, err
	}
	return out, nil
}

// SyncSpecMarkdown is retained for compatibility with older callers.
func (c *Client) SyncSpecMarkdown(ctx context.Context, req SpecSyncRequest) (SpecMarkdownSyncResult, error) {
	return c.ExportSpecMarkdown(ctx, req)
}
