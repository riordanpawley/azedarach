package protocol

import "github.com/riordanpawley/azedarach/internal/naming"

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
	CommandSpecSync              = "spec.sync"
	CommandSpecSyncMD            = "spec.sync_md"
)

type SpecRequirementStatus string

const (
	SpecRequirementStatusOpen       SpecRequirementStatus = "open"
	SpecRequirementStatusAccepted   SpecRequirementStatus = "accepted"
	SpecRequirementStatusSuperseded SpecRequirementStatus = "superseded"
)

type SpecLinkRole string

const (
	SpecLinkRoleImplements SpecLinkRole = "implements"
	SpecLinkRoleVerifies   SpecLinkRole = "verifies"
	SpecLinkRoleRelates    SpecLinkRole = "relates"
)

type SpecRequirement struct {
	ID          naming.RequirementID  `json:"id" msgpack:"id"`
	Title       string                `json:"title" msgpack:"title"`
	Description string                `json:"description,omitempty" msgpack:"description,omitempty"`
	IssueID     naming.IssueID        `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	Status      SpecRequirementStatus `json:"status" msgpack:"status"`
}

type SpecLink struct {
	ID      naming.SpecLinkID    `json:"id,omitempty" msgpack:"id,omitempty"`
	IssueID naming.IssueID       `json:"issue_id" msgpack:"issue_id"`
	ReqID   naming.RequirementID `json:"req_id" msgpack:"req_id"`
	Role    SpecLinkRole         `json:"role" msgpack:"role"`
	Note    string               `json:"note,omitempty" msgpack:"note,omitempty"`
}

type SpecRequirementListRequestBody struct {
	IssueID naming.IssueID         `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	Status  SpecRequirementStatus  `json:"status,omitempty" msgpack:"status,omitempty"`
	IDs     []naming.RequirementID `json:"ids,omitempty" msgpack:"ids,omitempty"`
}

type SpecRequirementListResponseBody struct {
	Requirements []SpecRequirement `json:"requirements" msgpack:"requirements"`
}

type SpecRequirementGetRequestBody struct {
	ID naming.RequirementID `json:"id" msgpack:"id"`
}

type SpecRequirementGetResponseBody struct {
	Requirement SpecRequirement `json:"requirement" msgpack:"requirement"`
}

type SpecRequirementCreateRequestBody struct {
	ID          naming.RequirementID `json:"id" msgpack:"id"`
	Title       string               `json:"title" msgpack:"title"`
	Description string               `json:"description,omitempty" msgpack:"description,omitempty"`
	IssueID     naming.IssueID       `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
}

type SpecRequirementCreateResponseBody struct {
	Requirement SpecRequirement `json:"requirement" msgpack:"requirement"`
}

type SpecRequirementUpdateRequestBody struct {
	ID          naming.RequirementID   `json:"id" msgpack:"id"`
	Title       *string                `json:"title,omitempty" msgpack:"title,omitempty"`
	Description *string                `json:"description,omitempty" msgpack:"description,omitempty"`
	Status      *SpecRequirementStatus `json:"status,omitempty" msgpack:"status,omitempty"`
}

type SpecRequirementUpdateResponseBody struct {
	Requirement SpecRequirement `json:"requirement" msgpack:"requirement"`
}

type SpecRequirementDeleteRequestBody struct {
	ID      naming.RequirementID `json:"id" msgpack:"id"`
	Confirm bool                 `json:"confirm" msgpack:"confirm"`
}

type SpecRequirementDeleteResponseBody struct {
	ID      naming.RequirementID `json:"id" msgpack:"id"`
	Deleted bool                 `json:"deleted" msgpack:"deleted"`
}

type SpecLinkListRequestBody struct {
	IssueID naming.IssueID       `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	ReqID   naming.RequirementID `json:"req_id,omitempty" msgpack:"req_id,omitempty"`
	IDs     []naming.SpecLinkID  `json:"ids,omitempty" msgpack:"ids,omitempty"`
}

type SpecLinkListResponseBody struct {
	Links []SpecLink `json:"links" msgpack:"links"`
}

type SpecLinkAddRequestBody struct {
	IssueID naming.IssueID       `json:"issue_id" msgpack:"issue_id"`
	ReqID   naming.RequirementID `json:"req_id" msgpack:"req_id"`
	Role    SpecLinkRole         `json:"role,omitempty" msgpack:"role,omitempty"`
	Note    string               `json:"note,omitempty" msgpack:"note,omitempty"`
}

type SpecLinkAddResponseBody struct {
	Link SpecLink `json:"link" msgpack:"link"`
}

type SpecLinkRemoveRequestBody struct {
	IssueID naming.IssueID       `json:"issue_id" msgpack:"issue_id"`
	ReqID   naming.RequirementID `json:"req_id" msgpack:"req_id"`
}

type SpecLinkRemoveResponseBody struct {
	IssueID naming.IssueID       `json:"issue_id" msgpack:"issue_id"`
	ReqID   naming.RequirementID `json:"req_id" msgpack:"req_id"`
	Removed bool                 `json:"removed" msgpack:"removed"`
}

type SpecReadRequestBody struct {
	IssueID naming.IssueID       `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	ReqID   naming.RequirementID `json:"req_id,omitempty" msgpack:"req_id,omitempty"`
}

type SpecReadResponseBody struct {
	Requirements []SpecRequirement `json:"requirements" msgpack:"requirements"`
	Links        []SpecLink        `json:"links" msgpack:"links"`
}

type SpecDiagnostic struct {
	Code     string               `json:"code" msgpack:"code"`
	Message  string               `json:"message" msgpack:"message"`
	Severity string               `json:"severity,omitempty" msgpack:"severity,omitempty"`
	IssueID  naming.IssueID       `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	ReqID    naming.RequirementID `json:"req_id,omitempty" msgpack:"req_id,omitempty"`
	LinkID   naming.SpecLinkID    `json:"link_id,omitempty" msgpack:"link_id,omitempty"`
}

type SpecLintRequestBody struct {
	Strict bool `json:"strict,omitempty" msgpack:"strict,omitempty"`
}

type SpecLintResponseBody struct {
	OK          bool             `json:"ok" msgpack:"ok"`
	Diagnostics []SpecDiagnostic `json:"diagnostics,omitempty" msgpack:"diagnostics,omitempty"`
}

type SpecParityFinding struct {
	IssueID  naming.IssueID       `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	ReqID    naming.RequirementID `json:"req_id,omitempty" msgpack:"req_id,omitempty"`
	Severity string               `json:"severity,omitempty" msgpack:"severity,omitempty"`
	Message  string               `json:"message" msgpack:"message"`
}

type SpecParityRequestBody struct {
	FailOnOut bool `json:"fail_on_out,omitempty" msgpack:"fail_on_out,omitempty"`
}

type SpecParityResponseBody struct {
	OK       bool                `json:"ok" msgpack:"ok"`
	Findings []SpecParityFinding `json:"findings,omitempty" msgpack:"findings,omitempty"`
}

type SpecSyncMDRequestBody struct {
	Target string `json:"target,omitempty" msgpack:"target,omitempty"`
	Check  bool   `json:"check,omitempty" msgpack:"check,omitempty"`
}

type SpecSyncMDResponseBody struct {
	Target  string   `json:"target" msgpack:"target"`
	Check   bool     `json:"check" msgpack:"check"`
	Changed bool     `json:"changed" msgpack:"changed"`
	Files   []string `json:"files,omitempty" msgpack:"files,omitempty"`
}

func (s SpecRequirementStatus) Valid() bool {
	switch s {
	case SpecRequirementStatusOpen,
		SpecRequirementStatusAccepted,
		SpecRequirementStatusSuperseded:
		return true
	default:
		return false
	}
}

func (r SpecLinkRole) Valid() bool {
	switch r {
	case SpecLinkRoleImplements,
		SpecLinkRoleVerifies,
		SpecLinkRoleRelates:
		return true
	default:
		return false
	}
}
