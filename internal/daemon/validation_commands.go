package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	operationstore "github.com/riordanpawley/azedarach/internal/daemon/operations/store"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"golang.org/x/sys/unix"
)

const defaultValidationLeaseTTL = 30 * time.Second

var (
	errPublicationEvidenceAssessmentChanged          = fmt.Errorf("publication evidence changed during authoritative assessment")
	errHistoricalPublicationOperationIdentityMissing = fmt.Errorf("historical publication operation identity is missing")
)

func (d *Daemon) handleValidationCommand(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var store *operationstore.SQLiteStore
	var storeErr error
	switch req.Command {
	case protocol.CommandValidationArtifactRead:
		// Artifact reads are project-authorized by their project-scoped storage
		// namespace and do not require the validation projection store.
	case protocol.CommandPublicationEvidenceRecord,
		protocol.CommandPublicationEvidenceStatus, protocol.CommandPublicationEvidenceEvaluate:
		store, storeErr = d.publicationEvidenceProjectionStore()
	default:
		store, storeErr = d.validationProjectionStore()
	}
	if storeErr != nil {
		return d.errorResponse(req, protocol.ErrorCodeUnavailable, storeErr.Error()), nil
	}
	projectID := d.projectID(req.Meta)
	now := time.Now().UTC()
	switch req.Command {
	case protocol.CommandValidationAcquire:
		var body protocol.ValidationAcquireRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("decode validation acquire: %v", err)), nil
		}
		ttl := validationTTL(body.TTLSeconds)
		authority, err := d.validationIssueAuthority(ctx, projectID, body)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeConflict, err.Error()), nil
		}
		acquire := domain.ValidationAcquire{RequestID: strings.TrimSpace(body.RequestID), LeaseToken: strings.TrimSpace(body.LeaseToken), ProjectID: projectID, IssueID: strings.TrimSpace(body.IssueID), IssuePriority: authority.issuePriority, IssuePriorityResolved: true, Class: body.Class, Scope: body.Scope, Purpose: body.Purpose, IsolationMode: strings.TrimSpace(body.IsolationMode), EnvironmentFingerprint: strings.TrimSpace(body.EnvironmentFingerprint), Override: body.Override, OverrideActor: strings.TrimSpace(body.OverrideActor), OverrideReason: strings.TrimSpace(body.OverrideReason), Profile: strings.TrimSpace(body.Profile), Command: strings.TrimSpace(body.Command), SourceRevision: strings.TrimSpace(body.SourceRevision), ReviewerID: authority.reviewerID, ReviewerKind: authority.reviewerKind, ReviewEpochEventID: authority.reviewEpochEventID, PublicationOperationID: authority.publicationOperationID, AcceptedReviewEventID: authority.acceptedReviewEventID, AcceptedPublicationOperationID: authority.acceptedPublicationOperationID, TTL: ttl}
		if err := acquire.Validate(); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
		}
		result, err := store.AcquireValidation(ctx, acquire, now)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeConflict, err.Error()), nil
		}
		return d.validationRequestSuccessResponse(req, result)
	case protocol.CommandValidationHeartbeat:
		var body protocol.ValidationHeartbeatRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("decode validation heartbeat: %v", err)), nil
		}
		result, err := store.HeartbeatValidation(ctx, strings.TrimSpace(body.RequestID), strings.TrimSpace(body.LeaseToken), now, validationTTL(body.TTLSeconds))
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeConflict, err.Error()), nil
		}
		return d.validationRequestSuccessResponse(req, result)
	case protocol.CommandValidationNested:
		var body protocol.ValidationAuthorizeNestedRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("decode nested validation authorization: %v", err)), nil
		}
		result, err := store.AuthorizeNestedValidation(ctx, domain.ValidationNestedAuthorization{RequestID: strings.TrimSpace(body.RequestID), LeaseToken: strings.TrimSpace(body.LeaseToken), Class: body.Class, Scope: body.Scope, Purpose: body.Purpose}, now, defaultValidationLeaseTTL)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeConflict, err.Error()), nil
		}
		return d.validationRequestSuccessResponse(req, result)
	case protocol.CommandValidationFinish:
		var body protocol.ValidationFinishRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("decode validation finish: %v", err)), nil
		}
		result, err := store.FinishValidation(ctx, strings.TrimSpace(body.RequestID), strings.TrimSpace(body.LeaseToken), body.State, body.Outcome, body.Evidence, now, defaultValidationLeaseTTL)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeConflict, err.Error()), nil
		}
		return d.validationRequestSuccessResponse(req, result)
	case protocol.CommandValidationStatus:
		snapshot, err := store.ValidationSnapshot(ctx, projectID, now, defaultValidationLeaseTTL)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		return d.validationSuccessResponse(req, protocol.ValidationStatusResponse{Snapshot: snapshot})
	case protocol.CommandValidationArtifactRead:
		var body protocol.ValidationArtifactReadRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("decode validation artifact read: %v", err)), nil
		}
		content, digest, totalSize, nextOffset, err := d.readValidationArtifact(projectID, body.Reference, body.Offset, body.Limit)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
		}
		return d.validationSuccessResponse(req, protocol.ValidationArtifactReadResponse{Reference: strings.TrimSpace(body.Reference), Digest: "sha256:" + digest, Content: content, Offset: body.Offset, NextOffset: nextOffset, TotalSize: totalSize, Complete: nextOffset == totalSize})
	case protocol.CommandPublicationEvidenceRecord:
		var body protocol.PublicationEvidenceRecordRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("decode publication evidence: %v", err)), nil
		}
		evidence, err := d.verifiedPublicationEvidence(ctx, projectID, body)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
		}
		evidence, err = store.RecordPublicationEvidence(ctx, evidence)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeConflict, err.Error()), nil
		}
		if _, err = d.publicationEvidenceSnapshot(ctx, projectID, body.IssueID); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		return d.validationSuccessResponse(req, protocol.PublicationEvidenceRecordResponse{Evidence: evidence})
	case protocol.CommandPublicationEvidenceStatus:
		var body protocol.PublicationEvidenceStatusRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("decode publication evidence status: %v", err)), nil
		}
		issueID := strings.TrimSpace(body.IssueID)
		snapshot, assessments, err := d.evaluateCurrentPublicationEvidence(ctx, projectID, issueID, now)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		return d.validationSuccessResponse(req, protocol.PublicationEvidenceStatusResponse{Snapshot: snapshot, Assessments: assessments})
	case protocol.CommandPublicationEvidenceEvaluate:
		var body protocol.PublicationEvidenceEvaluateRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("decode publication evidence evaluation: %v", err)), nil
		}
		issueID := strings.TrimSpace(body.IssueID)
		if issueID == "" {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "publication evidence evaluation requires issue identity"), nil
		}
		snapshot, assessments, err := d.evaluateCurrentPublicationEvidence(ctx, projectID, issueID, now)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		return d.validationSuccessResponse(req, protocol.PublicationEvidenceEvaluateResponse{Snapshot: snapshot, Assessments: assessments})
	default:
		return d.errorResponse(req, protocol.ErrorCodeUnsupportedCommand, "unsupported validation command"), nil
	}
}

func (d *Daemon) validationRequestSuccessResponse(req protocol.RequestEnvelope, request domain.ValidationRequest) (protocol.ResponseEnvelope, error) {
	issueID := strings.TrimSpace(request.IssueID)
	scopeID := ""
	if issueID == "" {
		scopeID = "repository:" + strings.TrimSpace(request.ProjectID)
	}
	artifacts := make([]domain.WorkflowArtifactReference, 0, len(request.Evidence.ReportPaths)+1)
	paths := append([]string(nil), request.Evidence.ReportPaths...)
	if request.Evidence.ReportPath != "" {
		paths = append(paths, request.Evidence.ReportPath)
	}
	outputPresent := request.Evidence.FailureSummary != "" || len(paths) > 0
	for _, path := range uniqueNonEmpty(paths) {
		artifact, retainErr := d.retainValidationArtifact(request.ProjectID, path)
		if retainErr != nil {
			continue
		}
		artifacts = append(artifacts, artifact)
	}
	contextPacket, err := domain.BuildWorkflowContextPacket(domain.WorkflowContextInput{
		Role: domain.WorkflowRoleValidator, IssueID: issueID, ScopeID: scopeID, SourceRevision: request.SourceRevision,
		Summary:            "validation " + strings.TrimSpace(request.Profile),
		Requirements:       []string{"profile: " + request.Profile, "command: " + request.Command},
		AffectedInvariants: []string{"class: " + string(request.Class), "scope: " + string(request.Scope), "purpose: " + string(request.Purpose)},
	})
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "build bounded validation context: "+err.Error()), nil
	}
	summary, err := domain.BuildWorkflowResultSummary(domain.WorkflowResultInput{
		Role: domain.WorkflowRoleValidator, IssueID: issueID, ScopeID: scopeID, SourceRevision: request.SourceRevision,
		Status: string(request.State), Outcome: request.Outcome, FailureSummary: request.Evidence.FailureSummary,
		ArtifactLinks: artifacts, OutputPresent: outputPresent,
	})
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "build bounded validation result: "+err.Error()), nil
	}
	responseRequest := request
	responseRequest.Command = ""
	responseRequest.OverrideReason = ""
	responseRequest.Outcome = ""
	responseRequest.Evidence = domain.ValidationEvidence{}
	return d.validationSuccessResponse(req, protocol.ValidationRequestResponse{Request: responseRequest, Context: contextPacket, Summary: summary})
}

func (d *Daemon) validationArtifactRoot(projectID string) string {
	projectDigest := sha256.Sum256([]byte(strings.TrimSpace(projectID)))
	projectKey := fmt.Sprintf("%x", projectDigest[:])
	return filepath.Join(d.validationArtifactBaseRoot(), "validation", projectKey, "sha256")
}

func (d *Daemon) validationArtifactBaseRoot() string {
	if configured := strings.TrimSpace(d.cfg.WorkflowArtifactDir); configured != "" {
		return configured
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".azedarach", "artifacts")
	}
	return filepath.Join(os.TempDir(), "azedarach", "artifacts")
}

// retainValidationArtifact creates an immutable, content-addressed copy before
// a result claims that command output was retained. The opaque reference is
// resolvable independently of the caller-owned source path.
func (d *Daemon) retainValidationArtifact(projectID, source string) (domain.WorkflowArtifactReference, error) {
	in, err := os.Open(strings.TrimSpace(source))
	if err != nil {
		return domain.WorkflowArtifactReference{}, err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return domain.WorkflowArtifactReference{}, fmt.Errorf("validation artifact source is not a regular file")
	}
	root := d.validationArtifactRoot(projectID)
	if err = os.MkdirAll(root, 0o700); err != nil {
		return domain.WorkflowArtifactReference{}, err
	}
	tmp, err := os.CreateTemp(root, ".staged-*")
	if err != nil {
		return domain.WorkflowArtifactReference{}, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	hash := sha256.New()
	if _, err = io.Copy(io.MultiWriter(tmp, hash), in); err != nil {
		tmp.Close()
		return domain.WorkflowArtifactReference{}, err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return domain.WorkflowArtifactReference{}, err
	}
	if err = tmp.Close(); err != nil {
		return domain.WorkflowArtifactReference{}, err
	}
	digest := fmt.Sprintf("%x", hash.Sum(nil))
	if err = os.Chmod(tmpPath, 0o400); err != nil {
		return domain.WorkflowArtifactReference{}, err
	}
	target := filepath.Join(root, digest)
	if err = os.Rename(tmpPath, target); err != nil {
		return domain.WorkflowArtifactReference{}, err
	}
	if dir, openErr := os.Open(root); openErr == nil {
		err = dir.Sync()
		_ = dir.Close()
		if err != nil {
			return domain.WorkflowArtifactReference{}, err
		}
	}
	return domain.WorkflowArtifactReference{Label: "validation report", Reference: "artifact:sha256/" + digest, Digest: "sha256:" + digest}, nil
}

func (d *Daemon) openValidationArtifact(projectID, reference string) (*os.File, string, int64, error) {
	const prefix = "artifact:sha256/"
	digest := strings.TrimPrefix(strings.TrimSpace(reference), prefix)
	if len(digest) != sha256.Size*2 || prefix+digest != strings.TrimSpace(reference) {
		return nil, "", 0, fmt.Errorf("invalid validation artifact reference")
	}
	for _, char := range digest {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return nil, "", 0, fmt.Errorf("invalid validation artifact digest")
		}
	}
	projectDigest := sha256.Sum256([]byte(strings.TrimSpace(projectID)))
	root, err := openValidationArtifactRoot(d.validationArtifactBaseRoot(), []string{"validation", fmt.Sprintf("%x", projectDigest[:]), "sha256"})
	if err != nil {
		return nil, "", 0, fmt.Errorf("validation artifact unavailable")
	}
	defer root.Close()
	fd, err := unix.Openat(int(root.Fd()), digest, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, "", 0, fmt.Errorf("validation artifact unavailable")
	}
	file := os.NewFile(uintptr(fd), digest)
	if file == nil {
		_ = unix.Close(fd)
		return nil, "", 0, fmt.Errorf("validation artifact unavailable")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, "", 0, fmt.Errorf("validation artifact unavailable")
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	if copyErr != nil || fmt.Sprintf("%x", hash.Sum(nil)) != digest {
		_ = file.Close()
		return nil, "", 0, fmt.Errorf("validation artifact digest mismatch")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, "", 0, fmt.Errorf("validation artifact unavailable")
	}
	return file, digest, info.Size(), nil
}

func openValidationArtifactRoot(basePath string, descendants []string) (*os.File, error) {
	if err := validateValidationArtifactPath(basePath); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(basePath)
	if err != nil || strings.TrimSpace(basePath) == "" || abs == string(filepath.Separator) {
		return nil, fmt.Errorf("invalid validation artifact root")
	}
	abs, err = canonicalValidationArtifactPath(abs)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), string(filepath.Separator))
	if current == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open validation artifact root")
	}
	relative, err := filepath.Rel(string(filepath.Separator), abs)
	if err != nil {
		_ = current.Close()
		return nil, err
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." || filepath.Base(component) != component {
			_ = current.Close()
			return nil, fmt.Errorf("invalid validation artifact root component")
		}
		nextFD, openErr := unix.Openat(int(current.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = current.Close()
		if openErr != nil {
			return nil, openErr
		}
		current = os.NewFile(uintptr(nextFD), component)
		if current == nil {
			_ = unix.Close(nextFD)
			return nil, fmt.Errorf("open validation artifact root component")
		}
	}
	for _, component := range descendants {
		if component == "" || component == "." || component == ".." || filepath.Base(component) != component {
			_ = current.Close()
			return nil, fmt.Errorf("invalid validation artifact root component")
		}
		nextFD, openErr := unix.Openat(int(current.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = current.Close()
		if openErr != nil {
			return nil, openErr
		}
		current = os.NewFile(uintptr(nextFD), component)
		if current == nil {
			_ = unix.Close(nextFD)
			return nil, fmt.Errorf("open validation artifact root component")
		}
	}
	return current, nil
}

func validateValidationArtifactPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("invalid validation artifact root")
	}
	for _, component := range strings.Split(filepath.ToSlash(path), "/") {
		if component == "." || component == ".." {
			return fmt.Errorf("invalid validation artifact root component")
		}
	}
	return nil
}

// canonicalValidationArtifactPath accounts for Darwin's standard system
// aliases (/var -> /private/var and /tmp -> /private/tmp) without permitting
// arbitrary configured symlinks. The remaining path is still opened one
// descriptor at a time with O_NOFOLLOW.
func canonicalValidationArtifactPath(path string) (string, error) {
	if runtime.GOOS != "darwin" {
		return path, nil
	}
	for _, prefix := range []string{"/var", "/tmp"} {
		relative, err := filepath.Rel(prefix, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		canonical, err := filepath.EvalSymlinks(prefix)
		if err != nil {
			return "", fmt.Errorf("canonicalize validation artifact root: %w", err)
		}
		return filepath.Join(canonical, relative), nil
	}
	return path, nil
}

func (d *Daemon) readValidationArtifact(projectID, reference string, offset int64, limit int) ([]byte, string, int64, int64, error) {
	if offset < 0 {
		return nil, "", 0, 0, fmt.Errorf("validation artifact offset must be non-negative")
	}
	if limit <= 0 {
		limit = protocol.ValidationArtifactReadMaxBytes
	}
	if limit > protocol.ValidationArtifactReadMaxBytes {
		return nil, "", 0, 0, fmt.Errorf("validation artifact read limit exceeds %d bytes", protocol.ValidationArtifactReadMaxBytes)
	}
	file, digest, totalSize, err := d.openValidationArtifact(projectID, reference)
	if err != nil {
		return nil, "", 0, 0, err
	}
	defer file.Close()
	if offset > totalSize {
		return nil, "", 0, 0, fmt.Errorf("validation artifact offset exceeds content size")
	}
	if _, err = file.Seek(offset, io.SeekStart); err != nil {
		return nil, "", 0, 0, fmt.Errorf("validation artifact unavailable")
	}
	content, err := io.ReadAll(io.LimitReader(file, int64(limit)))
	if err != nil {
		return nil, "", 0, 0, fmt.Errorf("validation artifact unavailable")
	}
	nextOffset := offset + int64(len(content))
	return content, digest, totalSize, nextOffset, nil
}

func (d *Daemon) validatePublicationEvidenceIssue(ctx context.Context, projectID, issueID string) error {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return fmt.Errorf("publication evidence requires issue identity")
	}
	client := d.issueClientForProject(projectID)
	if client == nil {
		return fmt.Errorf("publication evidence requires issue store")
	}
	if _, err := client.GetWithRuntime(ctx, projectID, issueID); err != nil {
		return fmt.Errorf("resolve publication evidence issue %s: %w", issueID, err)
	}
	return nil
}

func (d *Daemon) verifiedPublicationEvidence(ctx context.Context, projectID string, request protocol.PublicationEvidenceRecordRequest) (domain.PublicationEvidence, error) {
	request.EvidenceID = strings.TrimSpace(request.EvidenceID)
	request.IssueID = strings.TrimSpace(request.IssueID)
	request.ValidationRequestID = strings.TrimSpace(request.ValidationRequestID)
	if request.EvidenceID == "" || request.IssueID == "" || request.ValidationRequestID == "" {
		return domain.PublicationEvidence{}, fmt.Errorf("publication evidence record requires evidence, issue, and validation request identity")
	}
	if request.Layer != domain.PublicationEvidencePatchReview && request.Layer != domain.PublicationEvidenceActivePath {
		return domain.PublicationEvidence{}, fmt.Errorf("public evidence recording supports only verified patch_review or active_path execution")
	}
	if err := d.validatePublicationEvidenceIssue(ctx, projectID, request.IssueID); err != nil {
		return domain.PublicationEvidence{}, err
	}
	store, err := d.publicationEvidenceProjectionStore()
	if err != nil {
		return domain.PublicationEvidence{}, err
	}
	validation, err := store.ValidationRequest(ctx, projectID, request.ValidationRequestID)
	if err != nil {
		return domain.PublicationEvidence{}, err
	}
	if validation.IssueID != request.IssueID || validation.Scope != domain.ValidationScopeTicket || validation.Purpose != domain.ValidationPurposeReviewEvidence || validation.State != domain.ValidationRequestCompleted || !validation.Evidence.Present || validation.Override == domain.ValidationOverrideEmergency {
		return domain.PublicationEvidence{}, fmt.Errorf("publication evidence requires completed non-overridden ticket review execution for the same issue")
	}
	if validation.FinishedAt == nil {
		return domain.PublicationEvidence{}, fmt.Errorf("completed validation request has no durable finish time")
	}
	policy, capability, err := d.publicationEvidenceProjectPolicy(projectID)
	if err != nil {
		return domain.PublicationEvidence{}, err
	}
	if request.Layer == domain.PublicationEvidenceActivePath && !publicationNameContains(capability.ActivePathProfiles, validation.Profile) {
		return domain.PublicationEvidence{}, fmt.Errorf("validation profile %s is not a project-configured active-path capability", validation.Profile)
	}
	worktree, err := d.publicationEvidenceWorktree(ctx, projectID, request.IssueID)
	if err != nil {
		return domain.PublicationEvidence{}, err
	}
	head, err := d.git.HeadRevision(ctx, worktree)
	if err != nil {
		return domain.PublicationEvidence{}, err
	}
	if head != validation.SourceRevision {
		return domain.PublicationEvidence{}, fmt.Errorf("verified execution source %s does not match live issue worktree HEAD %s", validation.SourceRevision, head)
	}
	status, err := d.git.Status(ctx, worktree)
	if err != nil {
		return domain.PublicationEvidence{}, err
	}
	if status.HasChanges || status.HasConflicts {
		return domain.PublicationEvidence{}, fmt.Errorf("verified publication evidence requires a clean conflict-free issue worktree")
	}
	base, err := d.git.ResolveCommit(ctx, worktree, d.baseBranchForProject(projectID))
	if err != nil {
		return domain.PublicationEvidence{}, fmt.Errorf("resolve configured evidence base: %w", err)
	}
	patchDigest, err := d.git.PatchDigest(ctx, worktree, base, head)
	if err != nil {
		return domain.PublicationEvidence{}, err
	}
	changedPaths, err := d.git.ChangedFilesBetweenRefTrees(ctx, worktree, base, head)
	if err != nil {
		return domain.PublicationEvidence{}, err
	}
	coverage, err := publicationCoverageForPaths(changedPaths, capability)
	if err != nil {
		return domain.PublicationEvidence{}, err
	}
	producer := strings.TrimSpace(validation.ReviewerID)
	if producer == "" {
		producer = "validation:" + validation.RequestID
	}
	evidence := domain.PublicationEvidence{
		EvidenceID: request.EvidenceID, ProjectID: projectID, IssueID: request.IssueID, Layer: request.Layer,
		PatchDigest: patchDigest, SourceRevision: head, BaseRevision: base, Producer: producer,
		PolicyVersion: policy.Version, EnvironmentFingerprint: validation.EnvironmentFingerprint,
		ReusedFromEvidenceID: strings.TrimSpace(request.ReusedFromEvidenceID), Coverage: coverage, CreatedAt: validation.FinishedAt.UTC(),
	}
	return evidence, evidence.Validate()
}

type publicationEvidenceCapability struct {
	ActivePathProfiles []string
	ExactBaseSurfaces  map[string][]string
	Dependencies       map[string][]string
}

func (d *Daemon) publicationEvidenceConfigured(projectID string) (bool, error) {
	repoDir := d.resolveRepoDirForProject(projectID)
	if strings.TrimSpace(repoDir) == "" {
		return false, nil
	}
	cfg, err := appconfig.LoadConfig(repoDir)
	if err != nil {
		return false, fmt.Errorf("load project publication evidence capability: %w", err)
	}
	return strings.TrimSpace(cfg.PublicationEvidence.PolicyVersion) != "", nil
}

func (d *Daemon) publicationEvidenceProjectPolicy(projectID string) (domain.PublicationEvidencePolicy, publicationEvidenceCapability, error) {
	repoDir := d.resolveRepoDirForProject(projectID)
	if strings.TrimSpace(repoDir) == "" {
		return domain.PublicationEvidencePolicy{}, publicationEvidenceCapability{}, fmt.Errorf("publication evidence project root unavailable")
	}
	cfg, err := appconfig.LoadConfig(repoDir)
	if err != nil {
		return domain.PublicationEvidencePolicy{}, publicationEvidenceCapability{}, fmt.Errorf("load project publication evidence policy: %w", err)
	}
	owned := cfg.PublicationEvidence
	if strings.TrimSpace(owned.PolicyVersion) == "" {
		return domain.PublicationEvidencePolicy{}, publicationEvidenceCapability{}, fmt.Errorf("project publicationEvidence.policyVersion capability is absent")
	}
	capability := publicationEvidenceCapability{
		ActivePathProfiles: canonicalPublicationConfigNames(owned.ActivePathProfiles),
		ExactBaseSurfaces:  owned.ExactBaseSurfaces,
		Dependencies:       owned.Dependencies,
	}
	for _, mapping := range []map[string][]string{capability.ExactBaseSurfaces, capability.Dependencies} {
		for name, prefixes := range mapping {
			if strings.TrimSpace(name) == "" {
				return domain.PublicationEvidencePolicy{}, publicationEvidenceCapability{}, fmt.Errorf("publication evidence capability names must be non-empty")
			}
			if _, err := domain.CanonicalPublicationPaths(prefixes); err != nil {
				return domain.PublicationEvidencePolicy{}, publicationEvidenceCapability{}, fmt.Errorf("publication evidence capability %s: %w", name, err)
			}
		}
	}
	surfaces := make([]string, 0, len(capability.ExactBaseSurfaces))
	for surface := range capability.ExactBaseSurfaces {
		surfaces = append(surfaces, surface)
	}
	sort.Strings(surfaces)
	policy := domain.PublicationEvidencePolicy{
		Version: strings.TrimSpace(owned.PolicyVersion), ExactBaseSurfaces: surfaces,
		InvalidatePathOverlap: true, InvalidateDependencyOverlap: true, RequireEnvironmentMatch: true,
		FailClosedUnknownImpact: true, RequireCapability: true,
	}
	return policy, capability, policy.Validate()
}

func (d *Daemon) publicationEvidenceWorktree(ctx context.Context, projectID, issueID string) (string, error) {
	projection, found, err := d.worktreeRuntimeStateStore(projectID).GetWorktreeStateByIssueID(ctx, projectID, issueID)
	if err != nil {
		return "", fmt.Errorf("refresh issue worktree projection: %w", err)
	}
	if !found || strings.TrimSpace(projection.Path) == "" {
		return "", fmt.Errorf("publication evidence requires a durable issue worktree projection")
	}
	return strings.TrimSpace(projection.Path), nil
}

func publicationCoverageForPaths(paths []string, capability publicationEvidenceCapability) (domain.PublicationEvidenceCoverage, error) {
	canonical, err := domain.CanonicalPublicationPaths(paths)
	if err != nil {
		return domain.PublicationEvidenceCoverage{}, err
	}
	coverage := domain.PublicationEvidenceCoverage{Paths: canonical}
	coverage.Dependencies = publicationGroupsForPaths(canonical, capability.Dependencies)
	coverage.Surfaces = publicationGroupsForPaths(canonical, capability.ExactBaseSurfaces)
	return domain.CanonicalizePublicationCoverage(coverage)
}

func publicationGroupsForPaths(paths []string, mapping map[string][]string) []string {
	var groups []string
	for name, prefixes := range mapping {
		for _, candidate := range paths {
			for _, prefix := range prefixes {
				if publicationPathWithin(candidate, prefix) {
					groups = append(groups, strings.TrimSpace(name))
					goto nextGroup
				}
			}
		}
	nextGroup:
	}
	return canonicalPublicationConfigNames(groups)
}

func publicationPathWithin(candidate, prefix string) bool {
	return candidate == prefix || strings.HasPrefix(candidate, prefix+"/")
}

func canonicalPublicationConfigNames(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; !exists {
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func publicationNameContains(values []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (d *Daemon) recordTaskCloseMergeResultEvidence(ctx context.Context, projectID, issueID string, integration taskCloseIntegrationResult) error {
	canonical := false
	for _, attempt := range integration.ValidationAttempts {
		if attempt.Canonical && attempt.Status == domain.IntegrationCandidateValidationPassed && strings.TrimSpace(attempt.CandidateHead) == strings.TrimSpace(integration.TargetOID) {
			canonical = true
			break
		}
	}
	if !canonical {
		return fmt.Errorf("exact synthetic merge %s has no passed canonical validation", integration.TargetOID)
	}
	policy, capability, err := d.publicationEvidenceProjectPolicy(projectID)
	if err != nil {
		return err
	}
	targetWorktree := strings.TrimSpace(d.resolveRepoDirForProjectExact(projectID))
	if targetWorktree == "" {
		return fmt.Errorf("exact synthetic merge target worktree unavailable without exact project routing")
	}
	projectCfg, err := appconfig.LoadConfig(targetWorktree)
	if err != nil {
		return fmt.Errorf("load exact synthetic merge publication capability: %w", err)
	}
	gateCommand := publicationValidationIdentity(projectCfg)
	if gateCommand == "" {
		return fmt.Errorf("exact synthetic merge requires configured repository push gate command")
	}
	policyVersion := publicationPolicyVersion(projectCfg, gateCommand)
	environmentFingerprint := publicationEnvironmentFingerprint(projectCfg)
	operation, validation, err := d.taskClosePublicationProvenance(ctx, projectID, issueID, integration, policyVersion, gateCommand, environmentFingerprint)
	if err != nil {
		return err
	}
	currentTarget, err := d.git.ResolveCommit(ctx, targetWorktree, integration.TargetBranch)
	if err != nil {
		return fmt.Errorf("resolve exact synthetic merge target: %w", err)
	}
	if currentTarget != strings.TrimSpace(integration.TargetOID) {
		return fmt.Errorf("exact synthetic merge target moved: current=%s recorded=%s", currentTarget, integration.TargetOID)
	}
	status, err := d.git.Status(ctx, targetWorktree)
	if err != nil {
		return err
	}
	if status.HasChanges || status.HasConflicts {
		return fmt.Errorf("exact synthetic merge target must be clean and conflict-free")
	}
	sourceBase, err := d.git.MergeBaseBetween(ctx, targetWorktree, integration.BaseOID, integration.SourceOID)
	if err != nil {
		return fmt.Errorf("resolve exact synthetic merge source base: %w", err)
	}
	patchDigest, err := d.git.PatchDigest(ctx, targetWorktree, sourceBase, integration.SourceOID)
	if err != nil {
		return err
	}
	paths, err := d.git.ChangedFilesBetweenRefTrees(ctx, targetWorktree, sourceBase, integration.SourceOID)
	if err != nil {
		return err
	}
	coverage, err := publicationCoverageForPaths(paths, capability)
	if err != nil {
		return err
	}
	evidence := domain.PublicationEvidence{
		EvidenceID: taskCloseMergeResultEvidenceID(projectID, issueID, operation.OperationID, validation.RequestID, integration, policy.Version, validation.EnvironmentFingerprint), ProjectID: projectID, IssueID: issueID, Layer: domain.PublicationEvidenceMergeResult,
		PatchDigest: patchDigest, SourceRevision: strings.TrimSpace(integration.SourceOID), BaseRevision: strings.TrimSpace(integration.BaseOID),
		ResultRevision: strings.TrimSpace(integration.TargetOID), Producer: "daemon:task.close", PolicyVersion: policy.Version,
		EnvironmentFingerprint: validation.EnvironmentFingerprint, Coverage: coverage, CreatedAt: validation.FinishedAt.UTC(),
	}
	store, err := d.publicationEvidenceProjectionStore()
	if err != nil {
		return err
	}
	if _, err = store.RecordPublicationEvidence(ctx, evidence); err != nil {
		return fmt.Errorf("record exact synthetic merge evidence: %w", err)
	}
	_, err = d.publicationEvidenceSnapshot(ctx, projectID, issueID)
	return err
}

func (d *Daemon) verifyRecoveredTaskClosePublication(ctx context.Context, projectID, issueID string, integration taskCloseIntegrationResult) error {
	canonical := false
	for _, attempt := range integration.ValidationAttempts {
		if attempt.Canonical && attempt.Status == domain.IntegrationCandidateValidationPassed && strings.TrimSpace(attempt.CandidateHead) == strings.TrimSpace(integration.TargetOID) {
			canonical = true
			break
		}
	}
	if !canonical {
		return fmt.Errorf("exact synthetic merge %s has no passed canonical validation", integration.TargetOID)
	}
	policy, _, err := d.publicationEvidenceProjectPolicy(projectID)
	if err != nil {
		return err
	}
	targetWorktree := strings.TrimSpace(d.resolveRepoDirForProjectExact(projectID))
	if targetWorktree == "" {
		return fmt.Errorf("exact synthetic merge target worktree unavailable without exact project routing")
	}
	projectCfg, err := appconfig.LoadConfig(targetWorktree)
	if err != nil {
		return fmt.Errorf("load exact synthetic merge publication capability: %w", err)
	}
	gateCommand := publicationValidationIdentity(projectCfg)
	if gateCommand == "" {
		return fmt.Errorf("exact synthetic merge requires configured repository push gate command")
	}
	policyVersion := publicationPolicyVersion(projectCfg, gateCommand)
	environmentFingerprint := publicationEnvironmentFingerprint(projectCfg)
	operation, validation, err := d.taskClosePublicationProvenance(ctx, projectID, issueID, integration, policyVersion, gateCommand, environmentFingerprint)
	if err != nil {
		return err
	}
	wantEvidenceID := taskCloseMergeResultEvidenceID(projectID, issueID, operation.OperationID, validation.RequestID, integration, policy.Version, validation.EnvironmentFingerprint)
	snapshot, err := d.publicationEvidenceSnapshot(ctx, projectID, issueID)
	if err != nil {
		return err
	}
	for _, evidence := range snapshot.Evidence {
		if evidence.EvidenceID == wantEvidenceID && evidence.Layer == domain.PublicationEvidenceMergeResult &&
			strings.TrimSpace(evidence.SourceRevision) == strings.TrimSpace(integration.SourceOID) &&
			strings.TrimSpace(evidence.BaseRevision) == strings.TrimSpace(integration.BaseOID) &&
			strings.TrimSpace(evidence.ResultRevision) == strings.TrimSpace(integration.TargetOID) &&
			strings.TrimSpace(evidence.PolicyVersion) == strings.TrimSpace(policy.Version) &&
			strings.TrimSpace(evidence.EnvironmentFingerprint) == strings.TrimSpace(validation.EnvironmentFingerprint) {
			return nil
		}
	}
	return fmt.Errorf("exact synthetic merge %s is missing authoritative merge-result evidence %s", integration.TargetOID, wantEvidenceID)
}

func taskCloseMergeResultEvidenceID(projectID, issueID, operationID, validationRequestID string, integration taskCloseIntegrationResult, policyVersion, environmentFingerprint string) string {
	identity := sha256.Sum256([]byte(strings.Join([]string{
		projectID, issueID, operationID, validationRequestID, integration.BaseOID, integration.SourceOID,
		integration.TargetOID, policyVersion, environmentFingerprint,
	}, "\x00")))
	return "merge-" + fmt.Sprintf("%x", identity[:16])
}

func (d *Daemon) taskClosePublicationProvenance(ctx context.Context, projectID, issueID string, integration taskCloseIntegrationResult, policyVersion, gateCommand, environmentFingerprint string) (domain.PublicationOperation, domain.ValidationRequest, error) {
	publicationStore, err := d.publicationStoreForProject(projectID)
	if err != nil {
		return domain.PublicationOperation{}, domain.ValidationRequest{}, fmt.Errorf("load exact publication operation store: %w", err)
	}
	operations, err := publicationStore.PublicationOperations(ctx, projectID, issueID, false)
	if err != nil {
		return domain.PublicationOperation{}, domain.ValidationRequest{}, fmt.Errorf("load exact publication operations: %w", err)
	}
	wantTarget := strings.TrimSpace(integration.TargetOID)
	wantSource := strings.TrimSpace(integration.SourceOID)
	wantBase := strings.TrimSpace(integration.BaseOID)
	wantBranch := strings.TrimSpace(integration.TargetBranch)
	wantCommand := strings.Join(strings.Fields(gateCommand), " ")
	binding, bound := taskClosePublicationBindingFromContext(ctx)
	recoveredOperationID := strings.TrimSpace(integration.PublicationOperationID)
	if bound && recoveredOperationID != "" && recoveredOperationID != binding.operationID {
		return domain.PublicationOperation{}, domain.ValidationRequest{}, fmt.Errorf("recovered publication operation %s does not match active publication binding %s", recoveredOperationID, binding.operationID)
	}
	if !bound && recoveredOperationID == "" {
		accepted, acceptedErr := d.typedMergeAcceptedPublicationBinding(ctx, projectID, issueID, wantBranch, wantSource, wantTarget)
		if acceptedErr != nil {
			return domain.PublicationOperation{}, domain.ValidationRequest{}, fmt.Errorf("%w: exact synthetic merge %s recovery receipt is missing publication operation identity: %v", errHistoricalPublicationOperationIdentityMissing, wantTarget, acceptedErr)
		}
		recoveredOperationID = strings.TrimSpace(accepted.OperationID)
	}
	for _, operation := range operations {
		activeExactOperation := bound && operation.OperationID == binding.operationID && operation.ClaimToken == binding.claimToken && operation.State == domain.PublicationOperationPassed
		completedExactOperation := !bound && operation.OperationID == recoveredOperationID && operation.State == domain.PublicationOperationMerged && operation.FinishedAt != nil
		if (!activeExactOperation && !completedExactOperation) || !strings.EqualFold(strings.TrimSpace(operation.TargetID), "base") ||
			strings.TrimSpace(operation.CandidateRevision) != wantTarget || strings.TrimSpace(operation.SourceRevision) != wantSource || strings.TrimSpace(operation.BaseRevision) != wantBase ||
			strings.TrimSpace(operation.TargetBranch) != wantBranch || strings.TrimSpace(operation.PolicyVersion) != strings.TrimSpace(policyVersion) ||
			strings.TrimSpace(operation.EnvironmentFingerprint) != strings.TrimSpace(environmentFingerprint) || strings.Join(strings.Fields(operation.ValidationCommand), " ") != wantCommand ||
			strings.TrimSpace(operation.ValidationRequestID) == "" {
			continue
		}
		if d.operationRuntime == nil || d.operationRuntime.store == nil {
			return domain.PublicationOperation{}, domain.ValidationRequest{}, fmt.Errorf("exact publication validation store unavailable")
		}
		validation, validationErr := d.operationRuntime.store.ValidationRequest(ctx, projectID, operation.ValidationRequestID)
		if validationErr != nil {
			continue
		}
		if validation.State != domain.ValidationRequestCompleted || validation.FinishedAt == nil || !validation.Evidence.Present || validation.Override != domain.ValidationOverrideNone ||
			validation.Class != domain.ValidationClassAggregate || validation.Scope != domain.ValidationScopeRepository || validation.Purpose != domain.ValidationPurposePushGate ||
			(validation.Execution != domain.ValidationExecutionExecuted && validation.Execution != domain.ValidationExecutionReused) ||
			strings.TrimSpace(validation.SourceRevision) != wantTarget || strings.TrimSpace(validation.Evidence.SourceRevision) != wantTarget ||
			strings.TrimSpace(validation.Profile) != "publication:"+strings.TrimSpace(policyVersion) || strings.Join(strings.Fields(validation.Command), " ") != wantCommand ||
			strings.TrimSpace(validation.IsolationMode) != "synthetic-worktree" || strings.TrimSpace(validation.EnvironmentFingerprint) != strings.TrimSpace(environmentFingerprint) {
			continue
		}
		if validation.Execution == domain.ValidationExecutionReused {
			authoritative := strings.TrimSpace(validation.AuthoritativeRequestID)
			if authoritative == "" || strings.TrimSpace(operation.ReusedEvidenceID) != authoritative {
				continue
			}
		} else if reused := strings.TrimSpace(operation.ReusedEvidenceID); reused != "" && reused != validation.RequestID {
			continue
		}
		return operation, validation, nil
	}
	return domain.PublicationOperation{}, domain.ValidationRequest{}, fmt.Errorf("exact synthetic merge %s requires its completed non-emergency publication operation and repository push-gate request for policy %s, command %q, and environment %s", wantTarget, strings.TrimSpace(policyVersion), wantCommand, strings.TrimSpace(environmentFingerprint))
}

func (d *Daemon) evaluateCurrentPublicationEvidence(ctx context.Context, projectID, issueID string, now time.Time) (domain.PublicationEvidenceSnapshot, []domain.PublicationEvidenceAssessment, error) {
	if issueID == "" {
		snapshot, err := d.publicationEvidenceSnapshot(ctx, projectID, "")
		return snapshot, nil, err
	}
	for attempt := 0; attempt < 3; attempt++ {
		snapshot, assessments, identity, err := d.evaluateCurrentPublicationEvidenceOnce(ctx, projectID, issueID, now)
		if err != nil {
			if err == errPublicationEvidenceAssessmentChanged {
				continue
			}
			return domain.PublicationEvidenceSnapshot{}, nil, err
		}
		latest, latestErr := d.publicationEvidenceSnapshot(ctx, projectID, issueID)
		if latestErr != nil {
			return domain.PublicationEvidenceSnapshot{}, nil, latestErr
		}
		if latest.Revision != snapshot.Revision {
			continue
		}
		worktree, err := d.publicationEvidenceWorktree(ctx, projectID, issueID)
		if err != nil {
			mergeOnly := len(snapshot.Evidence) > 0
			for _, evidence := range snapshot.Evidence {
				mergeOnly = mergeOnly && evidence.Layer == domain.PublicationEvidenceMergeResult
			}
			if !mergeOnly {
				return domain.PublicationEvidenceSnapshot{}, nil, err
			}
			worktree = strings.TrimSpace(d.resolveRepoDirForProject(projectID))
			if worktree == "" {
				return domain.PublicationEvidenceSnapshot{}, nil, err
			}
		}
		head, headErr := d.git.HeadRevision(ctx, worktree)
		base, baseErr := d.git.ResolveCommit(ctx, worktree, d.baseBranchForProject(projectID))
		if headErr == nil && baseErr == nil && identity == head+"\x00"+base {
			return snapshot, assessments, nil
		}
	}
	return domain.PublicationEvidenceSnapshot{}, nil, fmt.Errorf("publication evidence candidate changed repeatedly during authoritative assessment")
}

func (d *Daemon) evaluateCurrentPublicationEvidenceOnce(ctx context.Context, projectID, issueID string, now time.Time) (domain.PublicationEvidenceSnapshot, []domain.PublicationEvidenceAssessment, string, error) {
	snapshot, err := d.publicationEvidenceSnapshot(ctx, projectID, issueID)
	if err != nil || len(snapshot.Evidence) == 0 {
		return snapshot, nil, "", err
	}
	policy, capability, err := d.publicationEvidenceProjectPolicy(projectID)
	if err != nil {
		return domain.PublicationEvidenceSnapshot{}, nil, "", err
	}
	worktree, err := d.publicationEvidenceWorktree(ctx, projectID, issueID)
	if err != nil {
		mergeOnly := true
		for _, evidence := range snapshot.Evidence {
			if evidence.Layer != domain.PublicationEvidenceMergeResult {
				mergeOnly = false
				break
			}
		}
		if !mergeOnly {
			return domain.PublicationEvidenceSnapshot{}, nil, "", err
		}
		worktree = strings.TrimSpace(d.resolveRepoDirForProject(projectID))
		if worktree == "" {
			return domain.PublicationEvidenceSnapshot{}, nil, "", err
		}
	}
	head, err := d.git.HeadRevision(ctx, worktree)
	if err != nil {
		return domain.PublicationEvidenceSnapshot{}, nil, "", err
	}
	base, err := d.git.ResolveCommit(ctx, worktree, d.baseBranchForProject(projectID))
	if err != nil {
		return domain.PublicationEvidenceSnapshot{}, nil, "", err
	}
	status, err := d.git.Status(ctx, worktree)
	if err != nil {
		return domain.PublicationEvidenceSnapshot{}, nil, "", err
	}
	store, err := d.publicationEvidenceProjectionStore()
	if err != nil {
		return domain.PublicationEvidenceSnapshot{}, nil, "", err
	}
	latestValidation, err := store.LatestReviewValidation(ctx, projectID, issueID, now, defaultValidationLeaseTTL)
	if err != nil {
		return domain.PublicationEvidenceSnapshot{}, nil, "", err
	}
	currentEnvironment := "unavailable"
	validationCapability := false
	if latestValidation != nil && latestValidation.State == domain.ValidationRequestCompleted && latestValidation.Evidence.Present && latestValidation.SourceRevision == head && latestValidation.Override != domain.ValidationOverrideEmergency {
		currentEnvironment = latestValidation.EnvironmentFingerprint
		validationCapability = true
	}
	decisionChanged, err := d.publicationMaterialDecisionChanged(ctx, projectID, issueID, snapshot.Evidence)
	if err != nil {
		return domain.PublicationEvidenceSnapshot{}, nil, "", err
	}
	effectiveInvalidations := domain.EffectivePublicationEvidenceInvalidations(snapshot)
	assessments := make([]domain.PublicationEvidenceAssessment, 0, len(snapshot.Evidence))
	candidates := make(map[string]domain.PublicationEvidenceCandidate, len(snapshot.Evidence))
	var appendInvalidations []domain.PublicationEvidenceInvalidation
	for _, evidence := range snapshot.Evidence {
		patchDigest, patchErr := d.git.PatchDigest(ctx, worktree, evidence.BaseRevision, head)
		changedPaths, impactErr := d.git.ChangedFilesBetweenRefTrees(ctx, worktree, evidence.BaseRevision, base)
		coverage, coverageErr := publicationCoverageForPaths(changedPaths, capability)
		candidate := domain.PublicationEvidenceCandidate{
			PatchDigest: patchDigest, SourceRevision: head, BaseRevision: base, PolicyVersion: policy.Version,
			EnvironmentFingerprint: currentEnvironment, Dirty: status.HasChanges, Conflict: status.HasConflicts,
			MaterialDecisionChanged: decisionChanged[evidence.EvidenceID], ToolchainChanged: validationCapability && currentEnvironment != evidence.EnvironmentFingerprint,
			ImpactKnown: impactErr == nil && coverageErr == nil, CapabilityAvailable: validationCapability && patchErr == nil && impactErr == nil && coverageErr == nil,
			ChangedPaths: changedPaths, ChangedDependencies: coverage.Dependencies, ChangedSurfaces: coverage.Surfaces,
		}
		if evidence.Layer == domain.PublicationEvidenceMergeResult {
			attempt, found, validationErr := d.git.CanonicalIntegrationValidation(ctx, worktree, base)
			canonicalMerge := validationErr == nil && found && attempt.Canonical && attempt.Status == domain.IntegrationCandidateValidationPassed && attempt.CandidateHead == base
			mergeValidation := latestValidation != nil && latestValidation.State == domain.ValidationRequestCompleted && latestValidation.Evidence.Present && latestValidation.SourceRevision == evidence.SourceRevision && latestValidation.Override != domain.ValidationOverrideEmergency
			candidate.PatchDigest = evidence.PatchDigest
			candidate.SourceRevision = evidence.SourceRevision
			candidate.ResultRevision = base
			candidate.EnvironmentFingerprint = "unavailable"
			if mergeValidation {
				candidate.EnvironmentFingerprint = latestValidation.EnvironmentFingerprint
			}
			candidate.CapabilityAvailable = canonicalMerge && mergeValidation
			candidate.ImpactKnown = validationErr == nil
			candidate.ToolchainChanged = mergeValidation && candidate.EnvironmentFingerprint != evidence.EnvironmentFingerprint
			if base == evidence.ResultRevision {
				candidate.BaseRevision = evidence.BaseRevision
				candidate.ChangedPaths = nil
				candidate.ChangedDependencies = nil
				candidate.ChangedSurfaces = nil
			}
		}
		assessment := domain.EvaluatePublicationEvidence(evidence, candidate, policy)
		assessment = domain.ApplyPublicationEvidenceInvalidations(assessment, effectiveInvalidations)
		assessments = append(assessments, assessment)
		candidates[evidence.EvidenceID] = candidate
		for index, reason := range assessment.Reasons {
			detail := "authoritative daemon assessment invalidated current evidence"
			if index < len(assessment.Details) {
				detail = assessment.Details[index]
			}
			appendInvalidations = append(appendInvalidations, publicationAssessmentInvalidation(evidence.EvidenceID, reason, detail, candidate, now))
		}
	}
	if err := store.RecordPublicationEvidenceInvalidations(ctx, appendInvalidations); err != nil {
		return domain.PublicationEvidenceSnapshot{}, nil, "", err
	}
	if len(appendInvalidations) > 0 {
		snapshot, err = d.publicationEvidenceSnapshot(ctx, projectID, issueID)
		if err != nil {
			return domain.PublicationEvidenceSnapshot{}, nil, "", err
		}
		if len(snapshot.Evidence) != len(candidates) {
			return domain.PublicationEvidenceSnapshot{}, nil, "", errPublicationEvidenceAssessmentChanged
		}
		effectiveInvalidations = domain.EffectivePublicationEvidenceInvalidations(snapshot)
		refreshedAssessments := make([]domain.PublicationEvidenceAssessment, 0, len(snapshot.Evidence))
		for _, evidence := range snapshot.Evidence {
			candidate, exists := candidates[evidence.EvidenceID]
			if !exists {
				return domain.PublicationEvidenceSnapshot{}, nil, "", errPublicationEvidenceAssessmentChanged
			}
			refreshedAssessments = append(refreshedAssessments, domain.ApplyPublicationEvidenceInvalidations(domain.EvaluatePublicationEvidence(evidence, candidate, policy), effectiveInvalidations))
		}
		assessments = refreshedAssessments
	}
	return snapshot, assessments, head + "\x00" + base, nil
}

func (d *Daemon) publicationMaterialDecisionChanged(ctx context.Context, projectID, issueID string, evidence []domain.PublicationEvidence) (map[string]bool, error) {
	client := d.issueClientForProject(projectID)
	if client == nil {
		return nil, fmt.Errorf("publication evidence material-decision projection unavailable")
	}
	events, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventDecisionChanged}, Limit: 5000})
	if err != nil {
		return nil, err
	}
	changed := make(map[string]bool, len(evidence))
	for _, item := range evidence {
		for _, event := range events {
			if event.ObservedAt.After(item.CreatedAt) && event.Payload["material"] != false {
				changed[item.EvidenceID] = true
				break
			}
		}
	}
	return changed, nil
}

func publicationAssessmentInvalidation(evidenceID string, reason domain.PublicationInvalidationReason, detail string, candidate domain.PublicationEvidenceCandidate, now time.Time) domain.PublicationEvidenceInvalidation {
	identity := strings.Join([]string{evidenceID, string(reason), candidate.SourceRevision, candidate.BaseRevision, candidate.ResultRevision, candidate.PolicyVersion, candidate.EnvironmentFingerprint}, "\x00")
	digest := sha256.Sum256([]byte(identity))
	return domain.PublicationEvidenceInvalidation{
		InvalidationID: fmt.Sprintf("assessment-%x", digest[:16]), EvidenceID: evidenceID, Reason: reason,
		Details: strings.TrimSpace(detail), CreatedAt: now.UTC(),
	}
}

type validationIssueAuthority struct {
	issuePriority                  domain.Priority
	reviewerID                     string
	reviewerKind                   string
	reviewEpochEventID             int64
	publicationOperationID         string
	acceptedReviewEventID          int64
	acceptedPublicationOperationID string
}

func (d *Daemon) validationIssueAuthority(ctx context.Context, projectID string, body protocol.ValidationAcquireRequest) (validationIssueAuthority, error) {
	if body.Scope == domain.ValidationScopeRepository {
		if strings.TrimSpace(body.IssueID) != "" {
			return validationIssueAuthority{}, fmt.Errorf("repository-scoped validation must not identify a ticket")
		}
		return validationIssueAuthority{issuePriority: domain.P2}, nil
	}
	if body.Scope != domain.ValidationScopeTicket {
		return validationIssueAuthority{}, fmt.Errorf("validation requires explicit repository or ticket scope")
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return validationIssueAuthority{}, fmt.Errorf("ticket-scoped validation requires issue store")
	}
	issueID := strings.TrimSpace(body.IssueID)
	if issueID == "" {
		return validationIssueAuthority{}, fmt.Errorf("ticket-scoped validation requires ticket identity")
	}
	task, err := issueClient.GetWithRuntime(ctx, projectID, issueID)
	if err != nil {
		return validationIssueAuthority{}, fmt.Errorf("resolve ticket-scoped validation %s: %w", issueID, err)
	}
	authority := validationIssueAuthority{issuePriority: task.Priority}
	if task.Priority < domain.P0 || task.Priority > domain.P4 {
		return validationIssueAuthority{}, fmt.Errorf("ticket-scoped validation %s has invalid priority %d", issueID, task.Priority)
	}
	if body.Purpose != domain.ValidationPurposeReviewEvidence {
		return authority, nil
	}
	if body.Class != domain.ValidationClassAggregate {
		return validationIssueAuthority{}, fmt.Errorf("review evidence requires aggregate class and ticket scope")
	}
	if task.Status != domain.StatusInReview {
		return authority, nil
	}
	reviewer, err := domain.CanonicalReviewerIdentity(body.ReviewerID, body.ReviewerKind)
	if err != nil {
		return validationIssueAuthority{}, fmt.Errorf("review-assigned aggregate validation requires typed orchestrator identity: %w", err)
	}
	reviewLease := coordinationLease(task, domain.CoordinationLeaseReview)
	if reviewLease == nil || reviewLease.IsExpired(time.Now().UTC()) {
		return validationIssueAuthority{}, fmt.Errorf("review-assigned aggregate validation requires an active durable review lease")
	}
	if !reviewer.Matches(reviewLease.OwnerID, reviewLease.OwnerKind) {
		return validationIssueAuthority{}, fmt.Errorf("review-assigned aggregate validation reviewer %s/%s does not own typed review lease", reviewer.OwnerID, reviewer.OwnerKind)
	}
	reviewAuthority := issues.ReviewPublicationAuthority{
		Reviewer: reviewer, ReviewEpochEventID: body.ReviewEpochEventID,
		AcceptedReviewEventID: body.AcceptedReviewEventID, PublicationOperationID: strings.TrimSpace(body.PublicationOperationID),
		AcceptedPublicationOperationID: strings.TrimSpace(body.AcceptedPublicationOperationID),
	}
	if err := issueClient.BeginReviewPublicationClose(ctx, issueID, reviewAuthority, nil); err != nil {
		return validationIssueAuthority{}, fmt.Errorf("review-assigned aggregate validation publication authority: %w", err)
	}
	authority.reviewerID, authority.reviewerKind = reviewer.OwnerID, reviewer.OwnerKind
	authority.reviewEpochEventID = body.ReviewEpochEventID
	authority.publicationOperationID = strings.TrimSpace(body.PublicationOperationID)
	authority.acceptedReviewEventID = body.AcceptedReviewEventID
	authority.acceptedPublicationOperationID = strings.TrimSpace(body.AcceptedPublicationOperationID)
	return authority, nil
}

func (d *Daemon) validationProjectionStore() (*operationstore.SQLiteStore, error) {
	if sourceForInvariant(daemonInvariantValidationCapacity) != daemonInvariantSourceProjection {
		return nil, fmt.Errorf("invariant %s requires projection source", daemonInvariantValidationCapacity)
	}
	if d.operationRuntime == nil || d.operationRuntime.store == nil {
		return nil, fmt.Errorf("validation projection store unavailable")
	}
	return d.operationRuntime.store, nil
}

func (d *Daemon) publicationEvidenceProjectionStore() (*operationstore.SQLiteStore, error) {
	if sourceForInvariant(daemonInvariantPublicationEvidence) != daemonInvariantSourceProjection {
		return nil, fmt.Errorf("invariant %s requires projection source", daemonInvariantPublicationEvidence)
	}
	if d.operationRuntime == nil || d.operationRuntime.store == nil {
		return nil, fmt.Errorf("publication evidence projection store unavailable")
	}
	return d.operationRuntime.store, nil
}

// publicationEvidenceSnapshot enforces the projection invariant's
// refresh-then-cache read contract. Every read replaces the complete project
// cache from durable SQLite before selecting an issue, so another daemon's
// write cannot leave this process evaluating stale evidence.
func (d *Daemon) publicationEvidenceSnapshot(ctx context.Context, projectID, issueID string) (domain.PublicationEvidenceSnapshot, error) {
	store, err := d.publicationEvidenceProjectionStore()
	if err != nil {
		return domain.PublicationEvidenceSnapshot{}, err
	}
	refreshed, err := store.PublicationEvidenceSnapshot(ctx, projectID, "")
	if err != nil {
		return domain.PublicationEvidenceSnapshot{}, fmt.Errorf("refresh publication evidence projection: %w", err)
	}
	if d.publicationEvidenceAfterRefresh != nil {
		d.publicationEvidenceAfterRefresh(refreshed)
	}
	d.publicationEvidenceMu.Lock()
	if d.publicationEvidenceCache == nil {
		d.publicationEvidenceCache = make(map[string]domain.PublicationEvidenceSnapshot)
	}
	cached, exists := d.publicationEvidenceCache[projectID]
	if !exists || refreshed.Revision >= cached.Revision {
		d.publicationEvidenceCache[projectID] = refreshed
		cached = refreshed
	}
	selected := publicationEvidenceSnapshotForIssue(cached, strings.TrimSpace(issueID))
	d.publicationEvidenceMu.Unlock()
	return selected, nil
}

func publicationEvidenceSnapshotForIssue(snapshot domain.PublicationEvidenceSnapshot, issueID string) domain.PublicationEvidenceSnapshot {
	if issueID == "" {
		return snapshot
	}
	filtered := domain.PublicationEvidenceSnapshot{
		Schema: snapshot.Schema, ProjectID: snapshot.ProjectID, IssueID: issueID, Revision: snapshot.Revision,
	}
	evidenceIDs := make(map[string]struct{})
	for _, evidence := range snapshot.Evidence {
		if evidence.IssueID == issueID {
			filtered.Evidence = append(filtered.Evidence, evidence)
			evidenceIDs[evidence.EvidenceID] = struct{}{}
		}
	}
	for _, invalidation := range snapshot.Invalidations {
		if _, ok := evidenceIDs[invalidation.EvidenceID]; ok {
			filtered.Invalidations = append(filtered.Invalidations, invalidation)
		}
	}
	return filtered
}

func (d *Daemon) validationSuccessResponse(req protocol.RequestEnvelope, value any) (protocol.ResponseEnvelope, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	resp.Revision = d.currentRevision(d.projectID(req.Meta))
	return resp, nil
}

func validationTTL(seconds int) time.Duration {
	if seconds <= 0 {
		return defaultValidationLeaseTTL
	}
	if seconds > 300 {
		seconds = 300
	}
	return time.Duration(seconds) * time.Second
}
