package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

type ProjectionVerificationErrorKind string

const (
	ProjectionVerificationGap          ProjectionVerificationErrorKind = "gap"
	ProjectionVerificationOverlap      ProjectionVerificationErrorKind = "overlap"
	ProjectionVerificationHashMismatch ProjectionVerificationErrorKind = "hash_mismatch"
	ProjectionVerificationIncompatible ProjectionVerificationErrorKind = "incompatible_projector"
)

type ProjectionVerificationError struct {
	Kind    ProjectionVerificationErrorKind
	Message string
}

func (e *ProjectionVerificationError) Error() string { return string(e.Kind) + ": " + e.Message }

const (
	ProjectionDeltaSchemaVersion = 1
	// ProjectionDeltaProtocolVersion is the first envelope version that exposes
	// the projection delivery commands. Keeping this claim explicit prevents a
	// same-version client and daemon from disagreeing about command support.
	ProjectionDeltaProtocolVersion Version = 48
	ProjectionDeliveryContract             = "transitional-projection-delivery-v1"
	CommandProjectionDeltaList             = "projection.delta.list"
	CommandProjectionDeltaWatch            = "projection.delta.watch"
	CommandProjectionSnapshot              = "projection.snapshot"
)

// ProjectionSourceRange describes provenance, never an Azedarach-owned total
// order. Transitional legacy authorities may not provide a terminal hash.
type ProjectionSourceRange struct {
	Authority    string `json:"authority" msgpack:"authority"`
	SourceFrom   string `json:"source_from,omitempty" msgpack:"source_from,omitempty"`
	SourceTo     string `json:"source_to,omitempty" msgpack:"source_to,omitempty"`
	TerminalHash string `json:"terminal_hash,omitempty" msgpack:"terminal_hash,omitempty"`
	Transitional bool   `json:"transitional" msgpack:"transitional"`
}

type ProjectionProjector struct {
	ID            string `json:"id" msgpack:"id"`
	SchemaVersion int    `json:"schema_version" msgpack:"schema_version"`
	Build         string `json:"build" msgpack:"build"`
	Checksum      string `json:"checksum" msgpack:"checksum"`
}

// MaterializedSnapshotMetadata identifies the derived inputs used to build a
// task or orchestration snapshot. DeliveryCursor is a transitional projector
// position, never a daemon authority revision. RuntimeChecksum identifies the
// independently sourced session/worktree observation projection joined into
// the result, so consumers do not infer a cross-authority total order.
type MaterializedSnapshotMetadata struct {
	DeliveryCursor             uint64                  `json:"delivery_cursor" msgpack:"delivery_cursor"`
	DeliveryHead               uint64                  `json:"delivery_head" msgpack:"delivery_head"`
	DeliveryCursorTransitional bool                    `json:"delivery_cursor_transitional" msgpack:"delivery_cursor_transitional"`
	Projector                  ProjectionProjector     `json:"projector" msgpack:"projector"`
	SourceVector               []ProjectionSourceRange `json:"source_vector" msgpack:"source_vector"`
	IssueChecksum              string                  `json:"issue_checksum" msgpack:"issue_checksum"`
	RuntimeChecksum            string                  `json:"runtime_checksum" msgpack:"runtime_checksum"`
	SemanticChecksum           string                  `json:"semantic_checksum" msgpack:"semantic_checksum"`
	Health                     string                  `json:"health" msgpack:"health"`
}

type ProjectionEmptyAdvance struct {
	DeliveryCursor uint64                `json:"delivery_cursor" msgpack:"delivery_cursor"`
	Source         ProjectionSourceRange `json:"source" msgpack:"source"`
}

type ProjectionDeltaReadRequest struct {
	ProjectID   naming.ProjectID `json:"project_id" msgpack:"project_id"`
	AfterCursor uint64           `json:"after_cursor" msgpack:"after_cursor"`
	Limit       int              `json:"limit,omitempty" msgpack:"limit,omitempty"`
}

type ProjectionDeltaOperation string
type ProjectionKind string

// SupportsProjectionDeltaCommands reports whether an envelope version can
// name the projection delivery commands introduced at version 48.
func SupportsProjectionDeltaCommands(version Version) bool {
	return version >= ProjectionDeltaProtocolVersion && version <= CurrentVersion
}

const (
	ProjectionDeltaUpsert ProjectionDeltaOperation = "upsert"
	ProjectionDeltaDelete ProjectionDeltaOperation = "delete"
)

func (o ProjectionDeltaOperation) Valid() bool {
	return o == ProjectionDeltaUpsert || o == ProjectionDeltaDelete
}

type ProjectionSnapshotRequest struct {
	ProjectID naming.ProjectID `json:"project_id" msgpack:"project_id"`
	Cursor    uint64           `json:"cursor" msgpack:"cursor"`
}

type ProjectionDelta struct {
	ProjectID        naming.ProjectID         `json:"project_id" msgpack:"project_id"`
	Cursor           uint64                   `json:"cursor" msgpack:"cursor"`
	Kind             ProjectionKind           `json:"kind" msgpack:"kind"`
	Key              string                   `json:"key" msgpack:"key"`
	QualifiedKey     string                   `json:"qualified_key" msgpack:"qualified_key"`
	Operation        ProjectionDeltaOperation `json:"operation" msgpack:"operation"`
	IdempotencyKey   string                   `json:"idempotency_key" msgpack:"idempotency_key"`
	Payload          json.RawMessage          `json:"payload,omitempty" msgpack:"payload,omitempty"`
	CommittedAt      time.Time                `json:"committed_at" msgpack:"committed_at"`
	Source           ProjectionSourceRange    `json:"source" msgpack:"source"`
	SemanticChecksum string                   `json:"semantic_checksum" msgpack:"semantic_checksum"`
}

type ProjectionValue struct {
	Kind         ProjectionKind  `json:"kind" msgpack:"kind"`
	Key          string          `json:"key" msgpack:"key"`
	QualifiedKey string          `json:"qualified_key" msgpack:"qualified_key"`
	Payload      json.RawMessage `json:"payload" msgpack:"payload"`
}

type ProjectionSnapshot struct {
	SchemaVersion              int                     `json:"schema_version" msgpack:"schema_version"`
	ProjectID                  naming.ProjectID        `json:"project_id" msgpack:"project_id"`
	Cursor                     uint64                  `json:"cursor" msgpack:"cursor"`
	HeadCursor                 uint64                  `json:"head_cursor" msgpack:"head_cursor"`
	Values                     []ProjectionValue       `json:"values" msgpack:"values"`
	DeliveryContract           string                  `json:"delivery_contract" msgpack:"delivery_contract"`
	DeliveryCursorTransitional bool                    `json:"delivery_cursor_transitional" msgpack:"delivery_cursor_transitional"`
	Projector                  ProjectionProjector     `json:"projector" msgpack:"projector"`
	SourceVector               []ProjectionSourceRange `json:"source_vector" msgpack:"source_vector"`
	SemanticChecksum           string                  `json:"semantic_checksum" msgpack:"semantic_checksum"`
	Health                     string                  `json:"health" msgpack:"health"`
	AvailableDeliveryFrom      uint64                  `json:"available_delivery_from" msgpack:"available_delivery_from"`
	AvailableDeliveryTo        uint64                  `json:"available_delivery_to" msgpack:"available_delivery_to"`
	LastGoodDeliveryCursor     uint64                  `json:"last_good_delivery_cursor" msgpack:"last_good_delivery_cursor"`
}

type ProjectionDeltaBatch struct {
	SchemaVersion              int                      `json:"schema_version" msgpack:"schema_version"`
	ProjectID                  naming.ProjectID         `json:"project_id" msgpack:"project_id"`
	AfterCursor                uint64                   `json:"after_cursor" msgpack:"after_cursor"`
	HeadCursor                 uint64                   `json:"head_cursor" msgpack:"head_cursor"`
	DeliveryToCursor           uint64                   `json:"delivery_to_cursor" msgpack:"delivery_to_cursor"`
	Deltas                     []ProjectionDelta        `json:"deltas" msgpack:"deltas"`
	EmptyAdvances              []ProjectionEmptyAdvance `json:"empty_advances" msgpack:"empty_advances"`
	DeliveryContract           string                   `json:"delivery_contract" msgpack:"delivery_contract"`
	DeliveryCursorTransitional bool                     `json:"delivery_cursor_transitional" msgpack:"delivery_cursor_transitional"`
	Projector                  ProjectionProjector      `json:"projector" msgpack:"projector"`
	SourceVector               []ProjectionSourceRange  `json:"source_vector" msgpack:"source_vector"`
	SemanticChecksum           string                   `json:"semantic_checksum" msgpack:"semantic_checksum"`
	Health                     string                   `json:"health" msgpack:"health"`
	AvailableDeliveryFrom      uint64                   `json:"available_delivery_from" msgpack:"available_delivery_from"`
	AvailableDeliveryTo        uint64                   `json:"available_delivery_to" msgpack:"available_delivery_to"`
	LastGoodDeliveryCursor     uint64                   `json:"last_good_delivery_cursor" msgpack:"last_good_delivery_cursor"`
}

// VerifyProjectionDeltaBatch checks transitional delivery continuity and
// deterministic semantic output without treating its cursor as source history.
func VerifyProjectionDeltaBatch(batch ProjectionDeltaBatch, expectedAfter uint64, expectedProjector ProjectionProjector) error {
	switch {
	case batch.AfterCursor < expectedAfter:
		return &ProjectionVerificationError{Kind: ProjectionVerificationOverlap, Message: fmt.Sprintf("after=%d expected=%d", batch.AfterCursor, expectedAfter)}
	case batch.AfterCursor > expectedAfter:
		return &ProjectionVerificationError{Kind: ProjectionVerificationGap, Message: fmt.Sprintf("after=%d expected=%d", batch.AfterCursor, expectedAfter)}
	case batch.DeliveryContract != ProjectionDeliveryContract || !batch.DeliveryCursorTransitional:
		return &ProjectionVerificationError{Kind: ProjectionVerificationIncompatible, Message: "delivery contract is not the transitional projection bridge"}
	case batch.Projector != expectedProjector:
		return &ProjectionVerificationError{Kind: ProjectionVerificationIncompatible, Message: fmt.Sprintf("projector=%+v expected=%+v", batch.Projector, expectedProjector)}
	}
	positions := make([]uint64, 0, len(batch.Deltas)+len(batch.EmptyAdvances))
	checksums := make([]string, 0, len(batch.Deltas))
	for _, delta := range batch.Deltas {
		positions = append(positions, delta.Cursor)
		want := projectionDeltaChecksum(delta)
		if delta.SemanticChecksum != want {
			return &ProjectionVerificationError{Kind: ProjectionVerificationHashMismatch, Message: fmt.Sprintf("delta cursor %d", delta.Cursor)}
		}
		checksums = append(checksums, want)
	}
	for _, advance := range batch.EmptyAdvances {
		positions = append(positions, advance.DeliveryCursor)
	}
	if expectedSources := mergeProjectionSources(batch.Deltas, batch.EmptyAdvances); !reflect.DeepEqual(expectedSources, batch.SourceVector) {
		return &ProjectionVerificationError{Kind: ProjectionVerificationHashMismatch, Message: "source vector or terminal source hash"}
	}
	sort.Slice(positions, func(i, j int) bool { return positions[i] < positions[j] })
	expected := batch.AfterCursor + 1
	for _, position := range positions {
		if position != expected {
			kind := ProjectionVerificationGap
			if position < expected {
				kind = ProjectionVerificationOverlap
			}
			return &ProjectionVerificationError{Kind: kind, Message: fmt.Sprintf("delivery cursor=%d expected=%d", position, expected)}
		}
		expected++
	}
	if batch.DeliveryToCursor != expected-1 || batch.DeliveryToCursor > batch.HeadCursor {
		return &ProjectionVerificationError{Kind: ProjectionVerificationGap, Message: fmt.Sprintf("delivery_to=%d covered_to=%d head=%d", batch.DeliveryToCursor, expected-1, batch.HeadCursor)}
	}
	if got := projectionDeltaBatchChecksum(checksums, batch.EmptyAdvances, batch.SourceVector); got != batch.SemanticChecksum {
		return &ProjectionVerificationError{Kind: ProjectionVerificationHashMismatch, Message: "batch semantic checksum"}
	}
	return nil
}

func VerifyProjectionSnapshot(snapshot ProjectionSnapshot, expectedProjector ProjectionProjector) error {
	if snapshot.DeliveryContract != ProjectionDeliveryContract || !snapshot.DeliveryCursorTransitional || snapshot.Projector != expectedProjector {
		return &ProjectionVerificationError{Kind: ProjectionVerificationIncompatible, Message: "snapshot delivery contract or projector"}
	}
	canonicalSources := canonicalProjectionSourceVector(snapshot.SourceVector)
	if !reflect.DeepEqual(canonicalSources, snapshot.SourceVector) {
		return &ProjectionVerificationError{Kind: ProjectionVerificationHashMismatch, Message: "snapshot source vector"}
	}
	if got := projectionSnapshotChecksum(snapshot.Values, canonicalSources); got != snapshot.SemanticChecksum {
		return &ProjectionVerificationError{Kind: ProjectionVerificationHashMismatch, Message: "snapshot semantic checksum or source vector"}
	}
	for _, value := range snapshot.Values {
		if value.QualifiedKey != projectionQualifiedKey(snapshot.ProjectID, value.Kind, value.Key) {
			return &ProjectionVerificationError{Kind: ProjectionVerificationHashMismatch, Message: "snapshot project-qualified key"}
		}
	}
	return nil
}

func mergeProjectionSources(deltas []ProjectionDelta, advances []ProjectionEmptyAdvance) []ProjectionSourceRange {
	type positionedSource struct {
		cursor uint64
		source ProjectionSourceRange
	}
	positions := make([]positionedSource, 0, len(deltas)+len(advances))
	for _, delta := range deltas {
		positions = append(positions, positionedSource{delta.Cursor, delta.Source})
	}
	for _, advance := range advances {
		positions = append(positions, positionedSource{advance.DeliveryCursor, advance.Source})
	}
	sort.Slice(positions, func(i, j int) bool { return positions[i].cursor < positions[j].cursor })
	sources := make([]ProjectionSourceRange, 0, len(positions))
	for _, position := range positions {
		sources = append(sources, position.source)
	}
	return canonicalProjectionSourceVector(sources)
}

// canonicalProjectionSourceVector is the single source-range derivation used
// by batch and snapshot checks. Input order declares each authority's range;
// output authority order is deterministic for checksumming.
func canonicalProjectionSourceVector(sources []ProjectionSourceRange) []ProjectionSourceRange {
	byAuthority := map[string]ProjectionSourceRange{}
	for _, source := range sources {
		current, found := byAuthority[source.Authority]
		if !found {
			byAuthority[source.Authority] = source
			continue
		}
		current.SourceTo = source.SourceTo
		if source.TerminalHash != "" {
			current.TerminalHash = source.TerminalHash
		}
		byAuthority[source.Authority] = current
	}
	authorities := make([]string, 0, len(byAuthority))
	for authority := range byAuthority {
		authorities = append(authorities, authority)
	}
	sort.Strings(authorities)
	result := make([]ProjectionSourceRange, 0, len(authorities))
	for _, authority := range authorities {
		result = append(result, byAuthority[authority])
	}
	return result
}

// FinalizeProjectionDeltaBatch binds transport-facing project-qualified keys
// and checksums after a project-store ID is remapped by the daemon.
func FinalizeProjectionDeltaBatch(batch *ProjectionDeltaBatch) {
	if batch == nil {
		return
	}
	checksums := make([]string, 0, len(batch.Deltas))
	for index := range batch.Deltas {
		delta := &batch.Deltas[index]
		delta.ProjectID = batch.ProjectID
		delta.QualifiedKey = projectionQualifiedKey(batch.ProjectID, delta.Kind, delta.Key)
		delta.SemanticChecksum = projectionDeltaChecksum(*delta)
		checksums = append(checksums, delta.SemanticChecksum)
	}
	batch.SourceVector = mergeProjectionSources(batch.Deltas, batch.EmptyAdvances)
	batch.SemanticChecksum = projectionDeltaBatchChecksum(checksums, batch.EmptyAdvances, batch.SourceVector)
}

func FinalizeProjectionSnapshot(snapshot *ProjectionSnapshot) {
	if snapshot == nil {
		return
	}
	for index := range snapshot.Values {
		value := &snapshot.Values[index]
		value.QualifiedKey = projectionQualifiedKey(snapshot.ProjectID, value.Kind, value.Key)
	}
	snapshot.SourceVector = canonicalProjectionSourceVector(snapshot.SourceVector)
	snapshot.SemanticChecksum = projectionSnapshotChecksum(snapshot.Values, snapshot.SourceVector)
}

func projectionQualifiedKey(projectID naming.ProjectID, kind ProjectionKind, key string) string {
	return NormalizeProjectID(projectID.String()) + "/" + string(kind) + "/" + key
}

func projectionDeltaChecksum(delta ProjectionDelta) string {
	return projectionChecksumJSON(struct {
		ProjectID string                   `json:"project_id"`
		Kind      ProjectionKind           `json:"kind"`
		Key       string                   `json:"key"`
		Operation ProjectionDeltaOperation `json:"operation"`
		Payload   json.RawMessage          `json:"payload"`
		Source    ProjectionSourceRange    `json:"source"`
	}{delta.ProjectID.String(), delta.Kind, delta.Key, delta.Operation, delta.Payload, delta.Source})
}

func projectionDeltaBatchChecksum(checksums []string, advances []ProjectionEmptyAdvance, sources []ProjectionSourceRange) string {
	return projectionChecksumJSON(struct {
		DeltaChecksums []string                 `json:"delta_checksums"`
		EmptyAdvances  []ProjectionEmptyAdvance `json:"empty_advances"`
		SourceVector   []ProjectionSourceRange  `json:"source_vector"`
	}{checksums, advances, sources})
}

func projectionSnapshotChecksum(values []ProjectionValue, sources []ProjectionSourceRange) string {
	return projectionChecksumJSON(struct {
		Values       []ProjectionValue       `json:"values"`
		SourceVector []ProjectionSourceRange `json:"source_vector"`
	}{values, sources})
}

func projectionChecksumJSON(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
