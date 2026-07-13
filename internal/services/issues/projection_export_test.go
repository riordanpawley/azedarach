package issues

import (
	"context"
	"testing"
	"time"
)

func TestExportProjectionUsesStableSchemaFingerprintAndMonotonicSourceRevision(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	first, err := c.ExportProjection(ctx, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if first.SchemaFingerprint == "" || first.SchemaVersion == 0 {
		t.Fatalf("contract=%d %q", first.SchemaVersion, first.SchemaFingerprint)
	}
	const legacyCheckpointCeiling = uint64(1 << 62)
	if first.Checkpoint < legacyCheckpointCeiling {
		t.Fatalf("initial revision %d does not preserve ordering with legacy checkpoints", first.Checkpoint)
	}
	checkpointOnly, err := c.ProjectionSourceCheckpoint(ctx)
	if err != nil || checkpointOnly != first.Checkpoint {
		t.Fatalf("checkpoint API=%d err=%v want export checkpoint=%d", checkpointOnly, err, first.Checkpoint)
	}
	db, err := c.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	older := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, `INSERT INTO issues(id,title,description,status,disposition,engagement,visibility,lifecycle_state,closed_outcome,review_state,priority,issue_type,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, `issue-1`, `before`, ``, `open`, `ready`, `idle`, `live`, `open`, `none`, `none`, 2, `task`, older, older)
	if err != nil {
		t.Fatal(err)
	}
	updated := time.Now().UTC().Add(time.Second).Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, `INSERT INTO daemon_session_projections(project_id,session_id,issue_id,scope_id,state,observed_state,activity,updated_at) VALUES(?,?,?,?,?,?,?,?)`, `project-a`, `session-1`, `issue-1`, `issue-1`, `running`, `running`, `busy`, updated)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.ExportProjection(ctx, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if second.SchemaFingerprint != first.SchemaFingerprint {
		t.Fatalf("fingerprint changed without schema change: %q -> %q", first.SchemaFingerprint, second.SchemaFingerprint)
	}
	if second.Checkpoint <= first.Checkpoint {
		t.Fatalf("runtime checkpoint did not advance: %d -> %d", first.Checkpoint, second.Checkpoint)
	}
	// Use a deliberately older timestamp: checkpoint ordering must follow durable
	// writes, not wall-clock values or incomparable issue/runtime counters.
	_, err = db.ExecContext(ctx, `UPDATE issues SET title='after-runtime',updated_at=? WHERE id='issue-1'`, older)
	if err != nil {
		t.Fatal(err)
	}
	third, err := c.ExportProjection(ctx, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if third.Checkpoint <= second.Checkpoint {
		t.Fatalf("issue checkpoint did not advance after runtime checkpoint: %d -> %d", second.Checkpoint, third.Checkpoint)
	}
	if len(third.Tasks) != 1 || third.Tasks[0].ID != "issue-1" {
		t.Fatalf("issue mutation missing from export: %+v", third.Tasks)
	}

	previous := third.Checkpoint
	assertAdvanced := func(label string) ProjectionExport {
		t.Helper()
		exported, exportErr := c.ExportProjection(ctx, "project-a")
		if exportErr != nil {
			t.Fatalf("%s export: %v", label, exportErr)
		}
		if exported.Checkpoint <= previous {
			t.Fatalf("%s checkpoint did not advance: %d -> %d", label, previous, exported.Checkpoint)
		}
		checkpoint, checkpointErr := c.ProjectionSourceCheckpoint(ctx)
		if checkpointErr != nil || checkpoint != exported.Checkpoint {
			t.Fatalf("%s checkpoint API=%d err=%v want=%d", label, checkpoint, checkpointErr, exported.Checkpoint)
		}
		previous = exported.Checkpoint
		return exported
	}

	observedAt := time.Now().UTC().Add(2 * time.Second).Format(time.RFC3339Nano)
	if _, err = db.ExecContext(ctx, `INSERT INTO daemon_session_observations(project_id,session_id,issue_id,scope_id,state,observed_state,activity,updated_at) VALUES(?,?,?,?,?,?,?,?)`, "project-a", "observed-1", "issue-1", "issue-1", "running", "running", "waiting", observedAt); err != nil {
		t.Fatal(err)
	}
	exported := assertAdvanced("session observation insert")
	if len(exported.Tasks) != 1 || exported.Tasks[0].Session == nil || exported.Tasks[0].Session.Activity != "waiting" {
		t.Fatalf("session observation content missing: %+v", exported.Tasks)
	}
	if _, err = db.ExecContext(ctx, `DELETE FROM daemon_session_observations WHERE project_id=? AND session_id=?`, "project-a", "observed-1"); err != nil {
		t.Fatal(err)
	}
	assertAdvanced("session observation delete")

	if _, err = db.ExecContext(ctx, `INSERT INTO issue_external_refs(issue_id,provider,provider_scope,remote_key,display_key,url,metadata_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, "issue-1", "github", "", "42", "#42", "https://example.test/42", `{}`, observedAt, observedAt); err != nil {
		t.Fatal(err)
	}
	exported = assertAdvanced("external ref insert")
	if len(exported.Tasks) != 1 || exported.Tasks[0].PullRequest == nil || exported.Tasks[0].PullRequest.URL != "https://example.test/42" {
		t.Fatalf("external ref content missing: %+v", exported.Tasks)
	}
	if _, err = db.ExecContext(ctx, `DELETE FROM issue_external_refs WHERE issue_id=?`, "issue-1"); err != nil {
		t.Fatal(err)
	}
	exported = assertAdvanced("external ref delete")
	if exported.Tasks[0].PullRequest != nil {
		t.Fatalf("deleted external ref remains: %+v", exported.Tasks[0].PullRequest)
	}

	if _, err = db.ExecContext(ctx, `INSERT INTO issue_coordination_leases(issue_id,purpose,owner_id,owner_kind,claimed_at) VALUES(?,?,?,?,?)`, "issue-1", "execution", "reviewer", "agent", observedAt); err != nil {
		t.Fatal(err)
	}
	exported = assertAdvanced("coordination lease insert")
	if len(exported.Tasks) != 1 || len(exported.Tasks[0].CoordinationLeases) != 1 || exported.Tasks[0].CoordinationLeases[0].OwnerID != "reviewer" {
		t.Fatalf("coordination lease content missing: %+v", exported.Tasks)
	}
	if _, err = db.ExecContext(ctx, `DELETE FROM issue_coordination_leases WHERE issue_id=?`, "issue-1"); err != nil {
		t.Fatal(err)
	}
	exported = assertAdvanced("coordination lease delete")
	if len(exported.Tasks[0].CoordinationLeases) != 0 {
		t.Fatalf("deleted coordination lease remains: %+v", exported.Tasks[0].CoordinationLeases)
	}
}
