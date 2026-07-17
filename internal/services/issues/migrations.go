package issues

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/sqlitemigration"
	"github.com/riordanpawley/azedarach/internal/sqliteutil"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const rootedBootstrapAcknowledgementMigrationID = "0049_rooted_bootstrap_acknowledgements"

type migration struct {
	id          string
	path        string
	shouldApply func(context.Context, *sql.DB) (bool, error)
	apply       func(context.Context, *sql.DB, string) error
}

const decisionIdempotencyMigrationID = "0051_decision_idempotency"

var orderedMigrations = []migration{
	{id: "0001_bootstrap_tables", path: "migrations/0001_bootstrap_tables.sql"},
	{id: "0002_dependency_foreign_keys", path: "migrations/0002_dependency_foreign_keys.sql", shouldApply: shouldApplyDependencyFKMigration},
	{id: "0003_issue_indexes", path: "migrations/0003_issue_indexes.sql"},
	{id: "0004_spec_tables", path: "migrations/0004_spec_tables.sql"},
	{id: "0005_spec_audit_log", path: "migrations/0005_spec_audit_log.sql"},
	{id: "0006_external_issue_sync", path: "migrations/0006_external_issue_sync.sql"},
	{id: "0006_issue_external_refs", path: "migrations/0006_issue_external_refs.sql"},
	{id: "0007_external_issue_sync_payload", path: "migrations/0007_external_issue_sync_payload.sql"},
	{id: "0008_decision_tables", path: "migrations/0008_decision_tables.sql"},
	{id: "0009_decision_audit_log", path: "migrations/0009_decision_audit_log.sql"},
	{id: "0010_decisions_refresh", path: "migrations/0010_decisions_refresh.sql"},
	{id: "0011_decisions_consequences", path: "migrations/0011_decisions_consequences.sql"},
	{id: "0012_blocked_status_to_open", path: "migrations/0012_blocked_status_to_open.sql"},
	{id: "0013_closed_runtime_invariants", path: "migrations/0013_closed_runtime_invariants.sql"},
	{id: "0014_linear_sync_external_refs_backfill", path: "migrations/0014_linear_sync_external_refs_backfill.sql"},
	{id: "0015_issue_attachments", path: "migrations/0015_issue_attachments.sql"},
	{id: "0016_issue_search_fts", path: "migrations/0016_issue_search_fts.sql"},
	{id: "0017_spec_requirement_search_fts", path: "migrations/0017_spec_requirement_search_fts.sql"},
	{id: "0018_issue_graph_closure", path: "migrations/0018_issue_graph_closure.sql"},
	{id: "0019_issue_observation_events", path: "migrations/0019_issue_observation_events.sql"},
	{id: "0019_agent_learnings", path: "migrations/0019_agent_learnings.sql"},
	{id: "0020_agent_learning_lifecycle", path: "migrations/0020_agent_learning_lifecycle.sql"},
	{id: "0021_agent_learning_metadata", path: "migrations/0021_agent_learning_metadata.sql"},
	{id: "0021_agent_learning_relations", path: "migrations/0021_agent_learning_relations.sql"},
	{id: "0021_agent_learning_target_state", path: "migrations/0021_agent_learning_target_state.sql"},
	{id: "0025_agent_learning_privacy", path: "migrations/0025_agent_learning_privacy.sql", apply: applyAgentLearningPrivacyMigration},
	{id: "0026_issue_ownership", path: "migrations/0026_issue_ownership.sql", apply: applyIssueOwnershipMigration},
	{id: "0026_decision_search_fts", path: "migrations/0026_decision_search_fts.sql", apply: applyDecisionSearchFTSMigration},
	{id: "0027_issue_id_allocations", path: "migrations/0027_issue_id_allocations.sql"},
	{id: "0028_runtime_projection_order_indexes", path: "migrations/0028_runtime_projection_order_indexes.sql"},
	{id: "0029_issue_state_model_v2", path: "migrations/0029_issue_state_model_v2.sql"},
	{id: "0030_issue_closed_runtime_v2_triggers", path: "migrations/0030_issue_closed_runtime_v2_triggers.sql", apply: applyIssueClosedRuntimeV2TriggersMigration},
	{id: "0031_board_views", path: "migrations/0031_board_views.sql"},
	{id: "0032_coordination_leases", path: "migrations/0032_coordination_leases.sql"},
	{id: "0033_orchestrator_scope_leases", path: "migrations/0033_orchestrator_scope_leases.sql"},
	{id: "0034_orchestration_start_attempts", path: "migrations/0034_orchestration_start_attempts.sql"},
	{id: "0034_orchestrator_lifecycle_clock", path: "migrations/0034_orchestrator_lifecycle_clock.sql", apply: applyOrchestratorLifecycleClockMigration},
	{id: "0035_interaction_requests", path: "migrations/0035_interaction_requests.sql"},
	{id: "0036_advisor_sessions", path: "migrations/0036_advisor_sessions.sql"},
	{id: "0037_projection_source_revision", path: "migrations/0037_projection_source_revision.sql"},
	{id: "0037_learning_activation_feedback", path: "migrations/0037_learning_activation_feedback.sql"},
	{id: "0038_learning_consolidation", path: "migrations/0038_learning_consolidation.sql"},
	{id: contextualLearningMigrationID, path: "migrations/0039_contextual_learning_activation.sql", apply: applyContextualLearningActivationMigration},
	{id: "0040_typed_learning_observations", path: "migrations/0040_typed_learning_observations.sql"},
	{id: "0041_learning_activation_confirmation", path: "migrations/0041_learning_activation_confirmation.sql"},
	{id: "0042_learning_consolidation_scan_cursor", path: "migrations/0042_learning_consolidation_scan_cursor.sql"},
	{id: "0043_learning_activation_telemetry", path: "migrations/0043_learning_activation_telemetry.sql"},
	{id: "0044_learning_activation_abandonment", path: "migrations/0044_learning_activation_abandonment.sql"},
	{id: "0045_issue_state_runtime_constraints", path: "migrations/0045_issue_state_runtime_constraints.sql", apply: applyIssueStateRuntimeConstraintsMigration},
	{id: "0046_repair_issue_state_runtime_constraints", path: "migrations/0046_repair_issue_state_runtime_constraints.manifest.sql", apply: applyIssueStateRuntimeConstraintsRepairMigration},
	{id: projectionDeltaAuthorityMigrationID, path: "migrations/0047_projection_delta_authority.sql", apply: applyProjectionDeltaAuthorityMigration},
	{id: humanAuthorityProjectionMigrationID, path: "migrations/0047_human_authority_projection_revision.sql"},
	{id: "0048_decision_propagation_outbox", path: "migrations/0048_decision_propagation_outbox.sql"},
	{id: rootedBootstrapAcknowledgementMigrationID, path: "migrations/0049_rooted_bootstrap_acknowledgements.sql"},
	{id: "0049_managed_agent_incarnations", path: "migrations/0049_managed_agent_incarnations.sql"},
	{id: issueObservationEventSearchMigrationID, path: "migrations/0050_issue_observation_event_search.sql"},
	{id: decisionIdempotencyMigrationID, path: "migrations/0051_decision_idempotency.sql"},
	{id: mailboxObservationProjectionCutoverMigrationID, path: "migrations/0052_mailbox_observation_projection_cutover.sql"},
	{id: agentInputDeliveryMigrationID, path: "migrations/0053_agent_input_delivery.sql"},
	{id: agentInputDeliveryFencingMigrationID, path: "migrations/0054_agent_input_delivery_fencing.sql"},
}

var migrationArtifacts = []sqlitemigration.Artifact{
	{ID: "0001_bootstrap_tables", Path: "migrations/0001_bootstrap_tables.sql", Checksum: "0bf3ae46504d70064277484bf9e2145a26be64b985989500b1c84e83020007d0"},
	{ID: "0002_dependency_foreign_keys", Path: "migrations/0002_dependency_foreign_keys.sql", Checksum: "f63f418c52c26730976c6c8115ec7c3b17856051cdb4926487e9b1294deb8faa"},
	{ID: "0003_issue_indexes", Path: "migrations/0003_issue_indexes.sql", Checksum: "0471d2d0c7e99d5634bf91c5b2647351a3216f15190a6a89d75c63d45ac33f3d"},
	{ID: "0004_spec_tables", Path: "migrations/0004_spec_tables.sql", Checksum: "65f997c26f9baff2d27c335d8a4721cbf0b87c7d447f04ca383652ffddc3524c"},
	{ID: "0005_spec_audit_log", Path: "migrations/0005_spec_audit_log.sql", Checksum: "d58cbb0ea6e2d2aaa6a6f3a323fccabc788419cad394283fe93c9581cdd782a9"},
	{ID: "0006_external_issue_sync", Path: "migrations/0006_external_issue_sync.sql", Checksum: "b69f1e807dd2002d6a3698a2a0f1635244cfabdc9af90e9bae75db15baec0cab"},
	{ID: "0006_issue_external_refs", Path: "migrations/0006_issue_external_refs.sql", Checksum: "3c0156e286d82b94462749495abae9e64dcc89b4c200e717ad78e6fbe83747dd"},
	{ID: "0007_external_issue_sync_payload", Path: "migrations/0007_external_issue_sync_payload.sql", Checksum: "e59b21ce4bc6ecead279770f70d09f6f06f63dc9ef9862a5cfdd4453537ca6f1"},
	{ID: "0008_decision_tables", Path: "migrations/0008_decision_tables.sql", Checksum: "d2a1678febb10e1036d8135f004738b053ea1c39b7663ab903c52e7c9a69ac94"},
	{ID: "0009_decision_audit_log", Path: "migrations/0009_decision_audit_log.sql", Checksum: "9c4c26e8b49eef6404fa2b08ad7f8d38c2cea4be1beccb5f7032e4c41783791c"},
	{ID: "0010_decisions_refresh", Path: "migrations/0010_decisions_refresh.sql", Checksum: "09df612b84cb9b660ef50c2f42e0df93dd17b9c4eb6f2755ebdb72163d27fde7"},
	{ID: "0011_decisions_consequences", Path: "migrations/0011_decisions_consequences.sql", Checksum: "68e30438aed5aa883772107459e43114f7bd743cd525cb30b597cdbd0c777474"},
	{ID: "0012_blocked_status_to_open", Path: "migrations/0012_blocked_status_to_open.sql", Checksum: "5a770520b605af1c4c0aa57c471b8dbb81c888b403cd1a93e3e00ed920f50ad1"},
	{ID: "0013_closed_runtime_invariants", Path: "migrations/0013_closed_runtime_invariants.sql", Checksum: "4ddfa055a9561c6a95601898774058559ae77122fb6fa6f194d57861ef726143"},
	{ID: "0014_linear_sync_external_refs_backfill", Path: "migrations/0014_linear_sync_external_refs_backfill.sql", Checksum: "8a71492754fe53e7b5bb784941c730522ab3dfa1a786785c896d5a7e454b3a24"},
	{ID: "0015_issue_attachments", Path: "migrations/0015_issue_attachments.sql", Checksum: "dd5f93a99ed0c84401796ec46044f48f8471fa23f22abb79279d4be3030b6400"},
	{ID: "0016_issue_search_fts", Path: "migrations/0016_issue_search_fts.sql", Checksum: "ccaae29ac521b2c487ada8966373803c9ae918fba7799bb6f0f663d9d65a0b64"},
	{ID: "0017_spec_requirement_search_fts", Path: "migrations/0017_spec_requirement_search_fts.sql", Checksum: "7cd22a727a9f2da7934d1f454f392104766da106b6d4ef86446c8e51d360b330"},
	{ID: "0018_issue_graph_closure", Path: "migrations/0018_issue_graph_closure.sql", Checksum: "ee360245b281908d8f7e4a14db31533788d9b3804ffc07c41656c7d009420f77"},
	{ID: "0019_agent_learnings", Path: "migrations/0019_agent_learnings.sql", Checksum: "90e4c418659b81bc38591759713d9eff9cacd47910b87d26433c8d326003e463"},
	{ID: "0019_issue_observation_events", Path: "migrations/0019_issue_observation_events.sql", Checksum: "7862370ddf094f860b6ae97f92c96bb3a1c937ce8c6402075619228624f55daa"},
	{ID: "0020_agent_learning_lifecycle", Path: "migrations/0020_agent_learning_lifecycle.sql", Checksum: "27fd546057a312954c3f44960c55ed56934fc4280c5cf672442d57f78e7d1587"},
	{ID: "0021_agent_learning_metadata", Path: "migrations/0021_agent_learning_metadata.sql", Checksum: "efdb0a78edf46f056c52b203f7a20cb12ac3365d18a314fba3a058f50e2cd2d8"},
	{ID: "0021_agent_learning_relations", Path: "migrations/0021_agent_learning_relations.sql", Checksum: "1be17d65f2bed296c4d70719adbb44375267b9a201390b7a744b067ef73c68c4"},
	{ID: "0021_agent_learning_target_state", Path: "migrations/0021_agent_learning_target_state.sql", Checksum: "0bc218f33abed5ccfd705ee02f2b2b54927361e2bf9013acdea369fd693e90f6"},
	{ID: "0025_agent_learning_privacy", Path: "migrations/0025_agent_learning_privacy.sql", Checksum: "a4454d74dc05fa6bb9117230a626e892de15fe9fd80db526e58b7fd10dab433e"},
	{ID: "0026_decision_search_fts", Path: "migrations/0026_decision_search_fts.sql", Checksum: "88f9c2e2810ff084699a88999fc3771ed917d0a946c211457a89407efb59aeda"},
	{ID: "0026_issue_ownership", Path: "migrations/0026_issue_ownership.sql", Checksum: "7d4d80fd698eafa9d7d85b84718138fa408668b3b6d1f9226837bd1989478664"},
	{ID: "0027_issue_id_allocations", Path: "migrations/0027_issue_id_allocations.sql", Checksum: "d7e05669a900b0c3c40fb188a3edc76fce0582be5ba8230979ac999d7438cd48"},
	{ID: "0028_runtime_projection_order_indexes", Path: "migrations/0028_runtime_projection_order_indexes.sql", Checksum: "ef8b0cffb8a3ea879d6e5cf44f36b69a6f3ba76154effedf2f36cd9acb0a5a8f"},
	{ID: "0029_issue_state_model_v2", Path: "migrations/0029_issue_state_model_v2.sql", Checksum: "ca0030ade7b737ae91e6ac81f37cfedceb581beb5384d3302406166d3756ce0f"},
	{ID: "0030_issue_closed_runtime_v2_triggers", Path: "migrations/0030_issue_closed_runtime_v2_triggers.sql", Checksum: "3019b99cb750044a48230af4dcd7ffba03e87345e5ee5c1cbc7f5650028a3ce4"},
	{ID: "0031_board_views", Path: "migrations/0031_board_views.sql", Checksum: "cd1c7d9222499d4a35f156eb62a810eb021e0c5aa91634b98c2759560757d931"},
	{ID: "0032_coordination_leases", Path: "migrations/0032_coordination_leases.sql", Checksum: "0cd1f81ea34f483635269e8235b822eb845a10b950c41702c8c3068b839837f3"},
	{ID: "0033_orchestrator_scope_leases", Path: "migrations/0033_orchestrator_scope_leases.sql", Checksum: "e00edd244f0ae6dd8ab005ae66a0abd173fd7becae2ce68d809df2c7a85fd7fb"},
	{ID: "0034_orchestration_start_attempts", Path: "migrations/0034_orchestration_start_attempts.sql", Checksum: "571141c87792a69e6da0b053e4a2604881b4f0741436148a6ac1284b1bbcc8fb"},
	{ID: "0034_orchestrator_lifecycle_clock", Path: "migrations/0034_orchestrator_lifecycle_clock.sql", Checksum: "4f1be08c0c843afe7cd59cdd57d32a8b709afec037db10babf7b652fd6b3d50f"},
	{ID: "0035_interaction_requests", Path: "migrations/0035_interaction_requests.sql", Checksum: "e04f344237c144670ae1f013536f2c5200908caa313d97ceaf2e47f5b1a9c529"},
	{ID: "0036_advisor_sessions", Path: "migrations/0036_advisor_sessions.sql", Checksum: "a492b56a0e64fcf7a305f6a894e855f7f7f9c67c5e9fc1c6d0c9fdbabe83359d"},
	{ID: "0037_learning_activation_feedback", Path: "migrations/0037_learning_activation_feedback.sql", Checksum: "16524bb983b9d4a6d5aac9a9ef3eee719405e55f4451a0c1c13044c22e7fcc69"},
	{ID: "0037_projection_source_revision", Path: "migrations/0037_projection_source_revision.sql", Checksum: "62b6aabb965fb823d23951e50ab8dc70a45f52b454c82053153ca8f3467c9f51"},
	{ID: "0038_learning_consolidation", Path: "migrations/0038_learning_consolidation.sql", Checksum: "268ff879069de8eddb66fcfc7ed4daa7e3d749d7dd8eb09fec8a54afef3a6d7f"},
	{ID: "0039_contextual_learning_activation", Path: "migrations/0039_contextual_learning_activation.sql", Checksum: "fd5871a322c1e7961c94e3feba1c68667458f71fa3b01b49e93fd96c60489c1e"},
	{ID: "0040_typed_learning_observations", Path: "migrations/0040_typed_learning_observations.sql", Checksum: "18a03aee58698cee1fd46081ea99fed3f1a33592d27474c8031c7233d4c8f0fa"},
	{ID: "0041_learning_activation_confirmation", Path: "migrations/0041_learning_activation_confirmation.sql", Checksum: "966ecb2589dac579a0efd894665b00ec0dfff823bae3931fde8d36c53e595677"},
	{ID: "0042_learning_consolidation_scan_cursor", Path: "migrations/0042_learning_consolidation_scan_cursor.sql", Checksum: "5d83042f5f7e7e53e38b9d92228a4d2f0fc66435b29a1632e55914620297c731"},
	{ID: "0043_learning_activation_telemetry", Path: "migrations/0043_learning_activation_telemetry.sql", Checksum: "fc665da364e9cafe07864cfeca8884929cf8c65982c4f61bcd12a4a20b526642"},
	{ID: "0044_learning_activation_abandonment", Path: "migrations/0044_learning_activation_abandonment.sql", Checksum: "56276dedf6d63e8db3e0a58e49cd29d7d862bc83ec7fdf1eb9615127004f607c"},
	{ID: "0045_issue_state_runtime_constraints", Path: "migrations/0045_issue_state_runtime_constraints.sql", Checksum: "67a11506f5d49059280d6406cbf1e66155549e4e573978f78f3e43b5ea944f23"},
	{ID: "0046_repair_issue_state_runtime_constraints", Path: "migrations/0046_repair_issue_state_runtime_constraints.manifest.sql", Checksum: "6420b559de666287450e274b283b2e481c1472e3b02914f3023019975216e20d"},
	{ID: projectionDeltaAuthorityMigrationID, Path: "migrations/0047_projection_delta_authority.sql", Checksum: projectionDeltaAuthorityChecksum},
	{ID: humanAuthorityProjectionMigrationID, Path: "migrations/0047_human_authority_projection_revision.sql", Checksum: "ac3a48512b2e6e9c018d58a68db24a2465e9d172139d22f8378f69677073a0ab"},
	{ID: "0048_decision_propagation_outbox", Path: "migrations/0048_decision_propagation_outbox.sql", Checksum: "a12c44ba35156d71fbcd88a9d78e4cdb234e75e7e4aef5f896c8b1182ada858d"},
	{ID: rootedBootstrapAcknowledgementMigrationID, Path: "migrations/0049_rooted_bootstrap_acknowledgements.sql", Checksum: "b54bdf5ec3f6af17c91e1625582ac58e66e47948cea68ee73db88d4e8df6f161"},
	{ID: "0049_managed_agent_incarnations", Path: "migrations/0049_managed_agent_incarnations.sql", Checksum: "8364ceb9fad589df3f73c1fe0f0462c22b127510f1745e62fcc11e24757fe08d"},
	{ID: "0050_issue_observation_event_search", Path: "migrations/0050_issue_observation_event_search.sql", Checksum: "e5a8efc20ddf313822576c4d6d42cd94e1837dfac810834957689d30b952005d"},
	{ID: decisionIdempotencyMigrationID, Path: "migrations/0051_decision_idempotency.sql", Checksum: "86d5400fe33bbc19e7e848bc232335809f76d85e4d45a6e45f6bc7ff77547f47"},
	{ID: mailboxObservationProjectionCutoverMigrationID, Path: "migrations/0052_mailbox_observation_projection_cutover.sql", Checksum: "fd86080f491210c169005c7f28bc778aca3eea2d70ce15a6c001bb960397e260"},
	{ID: agentInputDeliveryMigrationID, Path: "migrations/0053_agent_input_delivery.sql", Checksum: agentInputDeliveryMigrationChecksum},
	{ID: agentInputDeliveryFencingMigrationID, Path: "migrations/0054_agent_input_delivery_fencing.sql", Checksum: agentInputDeliveryFencingMigrationChecksum},
}

func validateMigrationRegistry() error {
	if err := sqlitemigration.Validate(migrationFiles, migrationArtifacts); err != nil {
		return err
	}
	registered := make([]sqlitemigration.Artifact, 0, len(orderedMigrations))
	for _, migration := range orderedMigrations {
		registered = append(registered, sqlitemigration.Artifact{ID: migration.id, Path: migration.path})
	}
	return sqlitemigration.ValidateRegistrations(migrationArtifacts, registered)
}

func applyIssueStateRuntimeConstraintsRepairMigration(ctx context.Context, db *sql.DB, id string) error {
	for _, column := range []string{"disposition", "engagement", "visibility"} {
		exists, err := columnExistsDB(ctx, db, "issues", column)
		if err != nil {
			return fmt.Errorf("inspect canonical issue state column %s: %w", column, err)
		}
		if !exists {
			return applyIssueStateRuntimeConstraintsMigration(ctx, db, id)
		}
	}
	if err := recordAppliedMigration(ctx, db, id); err != nil {
		return fmt.Errorf("record canonical issue state repair migration: %w", err)
	}
	return nil
}

func columnExistsDB(ctx context.Context, db sqlIssueQueryer, table, column string) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func applyIssueStateRuntimeConstraintsMigration(ctx context.Context, db *sql.DB, id string) error {
	var client Client
	if err := client.ensureRuntimeProjectionSchema(db); err != nil {
		return fmt.Errorf("repair runtime projection schema before migration %s: %w", id, err)
	}
	sqlText, err := loadMigrationSQL("migrations/0045_issue_state_runtime_constraints.sql")
	if err != nil {
		return fmt.Errorf("load migration %s: %w", id, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", id, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS interaction_requests (id TEXT PRIMARY KEY,issue_id TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,decision_key TEXT NOT NULL,state TEXT NOT NULL,revision INTEGER NOT NULL CHECK(revision>0),request_json TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("ensure interaction request authority before migration %s: %w", id, err)
	}
	for _, name := range []string{"issue_state_product_guard_insert", "issue_state_product_guard_update", "issue_archive_aggregate_guard", "issue_lease_archived_guard", "issue_lease_state_guard_update", "issue_worktree_archived_guard", "issue_worktree_archived_guard_update", "issue_session_archived_guard", "issue_session_archived_guard_update", "daemon_session_state_product_guard_insert", "daemon_session_state_product_guard_update", "daemon_session_observation_product_guard_insert", "daemon_session_observation_product_guard_update", "daemon_worktree_state_product_guard_insert", "daemon_worktree_state_product_guard_update", "issue_closed_runtime_guard_insert", "issue_closed_runtime_guard_update", "daemon_session_closed_issue_guard_insert", "daemon_session_closed_issue_guard_update", "issue_dependency_closed_runtime_guard_insert", "issue_dependency_closed_runtime_guard_update", "issue_descendant_closed_ancestor_guard_update"} {
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS `+name); err != nil {
			return err
		}
	}
	addedCanonicalStateColumn := false
	for _, column := range []struct{ name, ddl string }{{"disposition", "TEXT"}, {"engagement", "TEXT"}, {"visibility", "TEXT"}} {
		exists, err := txColumnExists(ctx, tx, "issues", column.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE issues ADD COLUMN `+column.name+` `+column.ddl); err != nil {
			return fmt.Errorf("add canonical issue state column %s: %w", column.name, err)
		}
		addedCanonicalStateColumn = true
	}
	if _, err := tx.ExecContext(ctx, `UPDATE issues SET
		disposition = CASE
			WHEN lifecycle_state='backlog' THEN 'backlog'
			WHEN lifecycle_state IN ('open','active') THEN 'ready'
			WHEN lifecycle_state='closed' AND closed_outcome='completed' THEN 'completed'
			WHEN lifecycle_state='closed' AND closed_outcome='cancelled' THEN 'cancelled'
		END,
		engagement = CASE
			WHEN lifecycle_state='active' AND review_state='requested' THEN 'review_requested'
			WHEN lifecycle_state='active' THEN 'working'
			ELSE 'idle'
		END,
		visibility = CASE WHEN archived_at IS NULL THEN 'live' ELSE 'archived' END
		WHERE disposition IS NULL OR engagement IS NULL OR visibility IS NULL`); err != nil {
		return fmt.Errorf("backfill canonical issue state: %w", err)
	}
	if addedCanonicalStateColumn {
		// The pre-canonical archive adapter represented archive intent by copying
		// deleted_at to archived_at without stopping active lifecycle state. Archive
		// is orthogonal to disposition in the canonical product, but an archived
		// issue cannot remain engaged. Normalize the canonical tuple, then regenerate
		// all legacy mirrors from it before installing the product guards.
		if _, err := tx.ExecContext(ctx, `UPDATE issues SET engagement='idle' WHERE visibility='archived';
			UPDATE issues SET
				lifecycle_state = CASE WHEN disposition='backlog' THEN 'backlog' WHEN disposition='ready' AND engagement='idle' THEN 'open' WHEN disposition='ready' THEN 'active' ELSE 'closed' END,
				review_state = CASE WHEN engagement='review_requested' THEN 'requested' ELSE 'none' END,
				closed_outcome = CASE WHEN disposition='completed' THEN 'completed' WHEN disposition='cancelled' THEN 'cancelled' ELSE 'none' END,
				status = CASE WHEN disposition='completed' THEN 'closed' WHEN disposition='cancelled' THEN 'cancelled' WHEN engagement='review_requested' THEN 'in_review' WHEN engagement='working' THEN 'in_progress' ELSE 'open' END`); err != nil {
			return fmt.Errorf("normalize legacy issue state mirrors: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM daemon_session_observations WHERE NOT EXISTS(SELECT 1 FROM issues i WHERE i.id=daemon_session_observations.issue_id);
			DELETE FROM daemon_session_projections WHERE NOT EXISTS(SELECT 1 FROM issues i WHERE i.id=daemon_session_projections.issue_id);
			DELETE FROM daemon_worktree_projections WHERE NOT EXISTS(SELECT 1 FROM issues i WHERE i.id=daemon_worktree_projections.issue_id)`); err != nil {
			return fmt.Errorf("prune impossible legacy issue authority: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE daemon_session_projections SET scope_id=issue_id
		WHERE role='worker' AND scope_kind='issue' AND COALESCE(trim(scope_id),'')=''`); err != nil {
		return fmt.Errorf("backfill legacy worker session scope identity: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE daemon_session_observations SET scope_id=issue_id WHERE role='worker' AND scope_kind='issue' AND COALESCE(trim(scope_id),'')=''; UPDATE daemon_session_observations SET state='running' WHERE state='attached'; UPDATE daemon_session_observations SET observed_state='running' WHERE observed_state='attached'`); err != nil {
		return fmt.Errorf("normalize legacy session observations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE daemon_session_projections SET state='running' WHERE state='attached'; UPDATE daemon_session_projections SET observed_state='running' WHERE observed_state='attached'`); err != nil {
		return fmt.Errorf("normalize legacy attached session state: %w", err)
	}
	if err := migrateIssueSessionLogicalIdentity(ctx, tx); err != nil {
		return fmt.Errorf("migrate logical session identity: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS issue_coordination_leases (
		issue_id TEXT NOT NULL, purpose TEXT NOT NULL CHECK (purpose IN ('execution','orchestration','review')),
		owner_id TEXT NOT NULL, owner_kind TEXT NOT NULL, claimed_at TEXT NOT NULL, expires_at TEXT,
		PRIMARY KEY(issue_id,purpose), FOREIGN KEY(issue_id) REFERENCES issues(id) ON DELETE CASCADE)`); err != nil {
		return fmt.Errorf("ensure canonical claim authority: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_issues_owner_active`); err != nil {
		return err
	}
	hasOwnerColumns, err := txColumnExists(ctx, tx, "issues", "owner_id")
	if err != nil {
		return err
	}
	if hasOwnerColumns {
		var invalidOwnerRows int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE
			(COALESCE(trim(owner_id),'')='' AND (COALESCE(trim(owner_kind),'')!='' OR COALESCE(trim(owner_claimed_at),'')!='' OR COALESCE(trim(owner_expires_at),'')!='')) OR
			(COALESCE(trim(owner_id),'')!='' AND (COALESCE(trim(owner_kind),'')='' OR COALESCE(trim(owner_claimed_at),'')=''))`).Scan(&invalidOwnerRows); err != nil {
			return fmt.Errorf("validate legacy issue claim tuples: %w", err)
		}
		if invalidOwnerRows != 0 {
			return fmt.Errorf("legacy issue claim authority contains %d partial tuples", invalidOwnerRows)
		}
		var conflictingOwnerRows int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues i JOIN issue_coordination_leases l ON l.issue_id=i.id AND l.purpose='execution'
			WHERE COALESCE(trim(i.owner_id),'')!='' AND (l.owner_id!=i.owner_id OR l.owner_kind!=i.owner_kind OR l.claimed_at!=i.owner_claimed_at OR COALESCE(l.expires_at,'')!=COALESCE(i.owner_expires_at,''))`).Scan(&conflictingOwnerRows); err != nil {
			return fmt.Errorf("compare legacy and canonical issue claims: %w", err)
		}
		if conflictingOwnerRows != 0 {
			return fmt.Errorf("legacy and canonical issue claim authority conflict on %d rows", conflictingOwnerRows)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO issue_coordination_leases(issue_id,purpose,owner_id,owner_kind,claimed_at,expires_at)
			SELECT i.id,'execution',i.owner_id,i.owner_kind,i.owner_claimed_at,i.owner_expires_at FROM issues i
			WHERE COALESCE(trim(i.owner_id),'')!='' AND NOT EXISTS(SELECT 1 FROM issue_coordination_leases l WHERE l.issue_id=i.id AND l.purpose='execution')`); err != nil {
			return fmt.Errorf("migrate unambiguous legacy issue claims: %w", err)
		}
	}
	for _, name := range []string{"owner_expires_at", "owner_claimed_at", "owner_kind", "owner_id"} {
		exists, err := txColumnExists(ctx, tx, "issues", name)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE issues DROP COLUMN `+name); err != nil {
			return fmt.Errorf("drop legacy issue claim mirror %s: %w", name, err)
		}
	}
	if addedCanonicalStateColumn {
		if _, err := tx.ExecContext(ctx, `DELETE FROM issue_coordination_leases WHERE NOT EXISTS(
			SELECT 1 FROM issues i WHERE i.id=issue_coordination_leases.issue_id
			AND i.visibility='live' AND i.disposition NOT IN ('completed','cancelled')
			AND ((issue_coordination_leases.purpose='execution' AND i.disposition='ready')
				OR (issue_coordination_leases.purpose='review' AND i.disposition='ready' AND i.engagement='review_requested')
				OR (issue_coordination_leases.purpose='orchestration' AND i.disposition IN ('backlog','ready')))
		)`); err != nil {
			return fmt.Errorf("prune impossible legacy issue claims: %w", err)
		}
	}
	for _, name := range []string{"issue_closed_runtime_guard_insert", "issue_closed_runtime_guard_update", "daemon_worktree_closed_issue_guard_insert", "daemon_worktree_closed_issue_guard_update", "daemon_session_closed_issue_guard_insert", "daemon_session_closed_issue_guard_update", "issue_dependency_closed_runtime_guard_insert", "issue_dependency_closed_runtime_guard_update", "issue_descendant_closed_ancestor_guard_update"} {
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS `+name); err != nil {
			return fmt.Errorf("drop legacy lifecycle authority trigger %s: %w", name, err)
		}
	}
	if _, err := tx.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("apply migration %s: %w", id, err)
	}
	canonicalRuntimeGuards := issueClosedRuntimeV2TriggersSQL
	canonicalRuntimeGuards = strings.ReplaceAll(canonicalRuntimeGuards, "BEFORE UPDATE OF lifecycle_state", "BEFORE UPDATE OF disposition")
	canonicalRuntimeGuards = strings.ReplaceAll(canonicalRuntimeGuards, "NEW.lifecycle_state = 'closed'", "NEW.disposition IN ('completed','cancelled')")
	canonicalRuntimeGuards = strings.ReplaceAll(canonicalRuntimeGuards, "NEW.lifecycle_state <> 'closed'", "NEW.disposition NOT IN ('completed','cancelled')")
	canonicalRuntimeGuards = strings.ReplaceAll(canonicalRuntimeGuards, "child.lifecycle_state <> 'closed'", "child.disposition NOT IN ('completed','cancelled')")
	canonicalRuntimeGuards = strings.ReplaceAll(canonicalRuntimeGuards, "i.lifecycle_state = 'closed'", "i.disposition IN ('completed','cancelled')")
	canonicalRuntimeGuards = strings.ReplaceAll(canonicalRuntimeGuards, "NEW.archived_at IS NULL", "NEW.visibility = 'live'")
	canonicalRuntimeGuards = strings.ReplaceAll(canonicalRuntimeGuards, "child.archived_at IS NULL", "child.visibility = 'live'")
	canonicalRuntimeGuards = strings.ReplaceAll(canonicalRuntimeGuards, "i.archived_at IS NULL", "i.visibility = 'live'")
	if _, err := tx.ExecContext(ctx, canonicalRuntimeGuards); err != nil {
		return fmt.Errorf("install canonical runtime aggregate guards: %w", err)
	}
	var revisionTables int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='projection_source_revision'`).Scan(&revisionTables); err != nil {
		return err
	}
	if revisionTables > 0 {
		for _, table := range []struct{ prefix, name string }{{"sessions", "daemon_session_projections"}, {"session_observations", "daemon_session_observations"}} {
			for _, action := range []string{"insert", "update", "delete"} {
				trigger := "projection_source_revision_" + table.prefix + "_" + action
				if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS `+trigger+`; CREATE TRIGGER `+trigger+` AFTER `+strings.ToUpper(action)+` ON `+table.name+` BEGIN UPDATE projection_source_revision SET revision=revision+1 WHERE singleton=1; END`); err != nil {
					return fmt.Errorf("restore projection revision trigger %s: %w", trigger, err)
				}
			}
		}
	}
	if err := recordAppliedMigration(ctx, tx, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	tx = nil
	return nil
}

func migrateIssueSessionLogicalIdentity(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE logical_runtime_winners AS
		SELECT DISTINCT project_id,role,scope_kind,scope_id,
		FIRST_VALUE(session_id) OVER (PARTITION BY project_id,role,scope_kind,scope_id ORDER BY
		CASE WHEN length(session_id)>3 AND substr(session_id,3,1)='-' AND substr(session_id,4)=CASE WHEN role='orchestrator' AND scope_id='project' THEN 'orchestrator-project' ELSE scope_id END THEN 0 ELSE 1 END,
		updated_at DESC,session_id ASC) winner_id
		FROM (SELECT project_id,session_id,role,scope_kind,scope_id,updated_at FROM daemon_session_projections WHERE instr(session_id,'.pane-')=0
		UNION ALL SELECT project_id,session_id,role,scope_kind,scope_id,updated_at FROM daemon_session_observations WHERE instr(session_id,'.pane-')=0)`); err != nil {
		return err
	}
	for _, table := range []string{"daemon_session_projections", "daemon_session_observations"} {
		merged := "merged_" + table
		if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE `+merged+` AS SELECT DISTINCT t.project_id,w.winner_id session_id,
		FIRST_VALUE(t.issue_id) OVER newest issue_id,t.role,t.scope_kind,t.scope_id,FIRST_VALUE(t.state) OVER newest state,
		FIRST_VALUE(t.observed_state) OVER newest observed_state,FIRST_VALUE(t.activity) OVER newest activity,
		FIRST_VALUE(t.activity_source) OVER newest activity_source,FIRST_VALUE(t.tmux_attached_count) OVER newest tmux_attached_count,
		MIN(NULLIF(t.started_at,'')) OVER logical_scope started_at,MAX(t.updated_at) OVER logical_scope updated_at
		FROM `+table+` t JOIN logical_runtime_winners w USING(project_id,role,scope_kind,scope_id) WHERE instr(t.session_id,'.pane-')=0
		WINDOW logical_scope AS (PARTITION BY t.project_id,t.role,t.scope_kind,t.scope_id),newest AS (PARTITION BY t.project_id,t.role,t.scope_kind,t.scope_id ORDER BY t.updated_at DESC,t.session_id ASC);
		DELETE FROM `+table+` WHERE instr(session_id,'.pane-')=0;
		INSERT INTO `+table+`(project_id,session_id,issue_id,role,scope_kind,scope_id,state,observed_state,activity,activity_source,tmux_attached_count,started_at,updated_at)
		SELECT project_id,session_id,issue_id,role,scope_kind,scope_id,state,observed_state,activity,activity_source,tmux_attached_count,started_at,updated_at FROM `+merged+`; DROP TABLE `+merged+`;`); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE logical_runtime_winners`); err != nil {
		return err
	}
	for _, table := range []string{"daemon_session_projections", "daemon_session_observations"} {
		newTable := table + "_logical_identity_v3"
		if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS `+newTable+`; CREATE TABLE `+newTable+`(
		project_id TEXT NOT NULL,session_id TEXT NOT NULL,issue_id TEXT NOT NULL,role TEXT NOT NULL DEFAULT 'worker',scope_kind TEXT NOT NULL DEFAULT 'issue',scope_id TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL,observed_state TEXT,activity TEXT,activity_source TEXT,tmux_attached_count INTEGER NOT NULL DEFAULT 0,started_at TEXT,updated_at TEXT NOT NULL,
		logical_id TEXT GENERATED ALWAYS AS (role||':'||scope_kind||':'||scope_id||CASE WHEN instr(session_id,'.pane-')>0 THEN ':pane:'||session_id ELSE '' END) STORED,
		UNIQUE(project_id,logical_id));
		INSERT INTO `+newTable+`(project_id,session_id,issue_id,role,scope_kind,scope_id,state,observed_state,activity,activity_source,tmux_attached_count,started_at,updated_at)
		SELECT project_id,session_id,issue_id,role,scope_kind,scope_id,state,observed_state,activity,activity_source,tmux_attached_count,started_at,updated_at FROM `+table+`;
		DROP TABLE `+table+`; ALTER TABLE `+newTable+` RENAME TO `+table+`;`); err != nil {
			return err
		}
	}
	return nil
}

const (
	migrationArtifactAuthority                     sqlitemigration.Authority = "project.issues"
	issueStateModelV2MigrationID                                             = "0029_issue_state_model_v2"
	issueStateModelVersionMetaKey                                            = "issue:state_model_version"
	issueStateModelV2CutoverMarkerKey                                        = "issue:state_model_v2_cutover"
	issueStateModelV2Version                                                 = "2"
	boardViewsMigrationID                                                    = "0031_board_views"
	projectionDeltaAuthorityMigrationID                                      = "0047_projection_delta_authority"
	projectionDeltaAuthorityChecksum                                         = "9f7bed54f9694c608c7ce081c4007539eb46ce67adc9127d5649a1dbb49b6c5a"
	humanAuthorityProjectionMigrationID                                      = "0047_human_authority_projection_revision"
	mailboxObservationProjectionCutoverMigrationID                           = "0052_mailbox_observation_projection_cutover"
	mailboxObservationProjectionCutoverMetaKey                               = "issue:mailbox_observation_projection_cutover"
	decisionPropagationOutboxMigrationID                                     = "0048_decision_propagation_outbox"
	issueObservationEventSearchMigrationID                                   = "0050_issue_observation_event_search"
	agentInputDeliveryMigrationID                                            = "0053_agent_input_delivery"
	agentInputDeliveryMigrationChecksum                                      = "0ab79e683f8af50d532fc65792e984fe261ce4c3e0a3b63096aac81f5d6c2fdd"
	agentInputDeliveryFencingMigrationID                                     = "0054_agent_input_delivery_fencing"
	agentInputDeliveryFencingMigrationChecksum                               = "939375d24a268a26e77e5d60e7f46b0d84e86ba8dfaf13815c427b886eb9f3c8"
	contextualLearningMigrationID                                            = "0039_contextual_learning_activation"
	legacyContextualLearningMigration                                        = "0038_contextual_learning_activation"
)

type issueStateModelV2CutoverMarker struct {
	State       string `json:"state"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
	BackupPath  string `json:"backup_path,omitempty"`
	Error       string `json:"error,omitempty"`
}

func (c *Client) runMigrations(ctx context.Context, db *sql.DB) error {
	if err := validateMigrationRegistry(); err != nil {
		return fmt.Errorf("validate migration registry: %w", err)
	}
	if err := ensureMigrationTable(ctx, db); err != nil {
		return err
	}
	if err := c.repairKnownProjectionDeltaBlankChecksum(ctx, db); err != nil {
		return err
	}
	if err := sqlitemigration.EnsureLedgerChecksumsAtomic(ctx, db, migrationArtifactAuthority, migrationArtifacts); err != nil {
		return err
	}
	if err := refusePartialIssueStateModelV2Cutover(ctx, db); err != nil {
		return err
	}
	if err := repairIssueBaseSchema(db); err != nil {
		return fmt.Errorf("repair issue base schema: %w", err)
	}
	if err := repairIssueDependencySchema(db); err != nil {
		return fmt.Errorf("repair issue dependency schema: %w", err)
	}
	if err := repairMetaSchema(db); err != nil {
		return fmt.Errorf("repair meta schema: %w", err)
	}
	externalRefsMigrationApplied, err := isMigrationApplied(ctx, db, "0006_issue_external_refs")
	if err != nil {
		return fmt.Errorf("check migration 0006_issue_external_refs before repair: %w", err)
	}
	if externalRefsMigrationApplied {
		if err := repairIssueExternalRefsSchema(db); err != nil {
			return fmt.Errorf("repair issue external refs schema: %w", err)
		}
	}
	if err := c.ensureSpecSchema(db); err != nil {
		return fmt.Errorf("repair spec schema: %w", err)
	}

	migrations := orderedMigrations
	if c.migrationCeiling != "" {
		ceiling := -1
		for i, migration := range orderedMigrations {
			if migration.id == c.migrationCeiling {
				ceiling = i
				break
			}
		}
		if ceiling < 0 {
			return fmt.Errorf("migration ceiling %s is not registered", c.migrationCeiling)
		}
		migrations = orderedMigrations[:ceiling+1]
	}
	for _, m := range migrations {
		applied, err := isMigrationApplied(ctx, db, m.id)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", m.id, err)
		}
		if applied {
			continue
		}

		shouldApply := true
		if m.shouldApply != nil {
			shouldApply, err = m.shouldApply(ctx, db)
			if err != nil {
				return fmt.Errorf("evaluate migration %s precondition: %w", m.id, err)
			}
		}

		if shouldApply {
			if m.id == issueStateModelV2MigrationID {
				if err := c.applyIssueStateModelV2Migration(ctx, db, m.id); err != nil {
					return err
				}
				continue
			}
			if m.id == boardViewsMigrationID {
				sqlText, err := loadMigrationSQL(m.path)
				if err != nil {
					return fmt.Errorf("load migration %s: %w", m.id, err)
				}
				if err := c.applyBoardViewsMigration(ctx, db, m.id, sqlText); err != nil {
					return err
				}
				continue
			}
			if m.id == humanAuthorityProjectionMigrationID {
				if err := c.applyHumanAuthorityProjectionMigration(ctx, db, m.id); err != nil {
					return err
				}
				continue
			}
			if m.id == decisionPropagationOutboxMigrationID {
				if err := c.applyDecisionPropagationOutboxMigration(ctx, db, m.id); err != nil {
					return err
				}
				continue
			}
			if m.id == agentInputDeliveryMigrationID {
				if err := c.applyAgentInputDeliveryMigration(ctx, db, m.id); err != nil {
					return err
				}
				continue
			}
			if m.id == agentInputDeliveryFencingMigrationID {
				if err := c.applyAgentInputDeliveryFencingMigration(ctx, db, m.id); err != nil {
					return err
				}
				continue
			}
			if m.id == decisionIdempotencyMigrationID {
				if err := c.applyDecisionIdempotencyMigration(ctx, db, m.id); err != nil {
					return err
				}
				continue
			}
			if m.id == issueObservationEventSearchMigrationID {
				if err := c.applyIssueObservationEventSearchMigration(ctx, db, m.id); err != nil {
					return err
				}
				continue
			}
			if m.apply != nil {
				if err := m.apply(ctx, db, m.id); err != nil {
					return err
				}
				continue
			}
			sqlText, err := loadMigrationSQL(m.path)
			if err != nil {
				return fmt.Errorf("load migration %s: %w", m.id, err)
			}
			if err := c.applyMigration(ctx, db, m.id, sqlText); err != nil {
				return err
			}
			continue
		}

		if err := recordAppliedMigration(ctx, db, m.id); err != nil {
			return fmt.Errorf("record skipped migration %s: %w", m.id, err)
		}
	}
	if err := validateHumanAuthorityProjectionRevisionTriggers(ctx, db); err != nil {
		return err
	}
	if err := validateDecisionPropagationOutboxSchema(ctx, db); err != nil {
		return err
	}
	if err := validateIssueObservationEventSearchSchema(ctx, db); err != nil {
		return err
	}
	mailboxCutoverApplied, err := isMigrationApplied(ctx, db, mailboxObservationProjectionCutoverMigrationID)
	if err != nil {
		return fmt.Errorf("check mailbox observation projection cutover migration: %w", err)
	}
	if mailboxCutoverApplied {
		if err := validateMailboxObservationProjectionCutover(ctx, db); err != nil {
			return err
		}
	}
	if err := validateProjectionDeltaAuthoritySchema(ctx, db); err != nil {
		return fmt.Errorf("validate projection delta authority schema: %w", err)
	}
	managedIdentityApplied, err := isMigrationApplied(ctx, db, "0049_managed_agent_incarnations")
	if err != nil {
		return fmt.Errorf("check managed agent identity migration: %w", err)
	}
	if managedIdentityApplied {
		if err := validateManagedAgentIdentitySchema(ctx, db); err != nil {
			return err
		}
	}
	agentInputApplied, err := isMigrationApplied(ctx, db, agentInputDeliveryMigrationID)
	if err != nil {
		return fmt.Errorf("check agent input delivery migration: %w", err)
	}
	if agentInputApplied {
		fencingApplied, err := isMigrationApplied(ctx, db, agentInputDeliveryFencingMigrationID)
		if err != nil {
			return fmt.Errorf("check agent input delivery fencing migration: %w", err)
		}
		if fencingApplied {
			err = validateAgentInputDeliverySchema(ctx, db)
		} else {
			err = validateAgentInputDeliveryBaseSchema(ctx, db)
		}
		if err != nil {
			return err
		}
	}
	decisionIdempotencyApplied, err := isMigrationApplied(ctx, db, decisionIdempotencyMigrationID)
	if err != nil {
		return fmt.Errorf("check decision idempotency migration: %w", err)
	}
	if decisionIdempotencyApplied {
		if err := validateDecisionIdempotencySchema(ctx, db); err != nil {
			return err
		}
	}

	canonicalApplied, err := isMigrationApplied(ctx, db, "0045_issue_state_runtime_constraints")
	if err != nil {
		return fmt.Errorf("check canonical issue state migration: %w", err)
	}
	if !canonicalApplied {
		if err := c.reconcileIssueStateModelV2Drift(ctx, db); err != nil {
			return err
		}
	}

	if err := repairAgentLearningBaseSchema(ctx, db); err != nil {
		return fmt.Errorf("repair agent learning base schema: %w", err)
	}
	if err := repairIssueIDAllocationSchema(ctx, db); err != nil {
		return fmt.Errorf("repair issue id allocation schema: %w", err)
	}
	if err := c.retrySQLiteBusy(ctx, func() error {
		return sqliteutil.WithWriteLock(c.dbPath, func() error {
			return c.seedAllBuiltInBoardViews(ctx, db)
		})
	}); err != nil {
		return fmt.Errorf("seed built-in board views: %w", err)
	}
	return sqlitemigration.EnsureLedgerChecksumsAtomic(ctx, db, migrationArtifactAuthority, migrationArtifacts)
}

func validateMailboxObservationProjectionCutover(ctx context.Context, db *sql.DB) error {
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, mailboxObservationProjectionCutoverMetaKey).Scan(&raw); err != nil {
		return fmt.Errorf("applied migration %s is missing its cutover marker: %w", mailboxObservationProjectionCutoverMigrationID, err)
	}
	var marker struct {
		State   string `json:"state"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal([]byte(raw), &marker); err != nil {
		return fmt.Errorf("applied migration %s has invalid cutover marker: %w", mailboxObservationProjectionCutoverMigrationID, err)
	}
	if marker.Version != 1 || marker.State != "pending" && marker.State != "complete" {
		return fmt.Errorf("applied migration %s has unsupported cutover marker state=%q version=%d", mailboxObservationProjectionCutoverMigrationID, marker.State, marker.Version)
	}
	return nil
}

func (c *Client) applyDecisionIdempotencyMigration(ctx context.Context, db *sql.DB, id string) error {
	sqlText, err := loadMigrationSQL("migrations/0051_decision_idempotency.sql")
	if err != nil {
		return fmt.Errorf("load migration %s: %w", id, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", id, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("apply migration %s: %w", id, err)
	}
	if c.decisionIdempotencyFailureHook != nil {
		if err := c.decisionIdempotencyFailureHook("after_schema"); err != nil {
			return fmt.Errorf("migration %s rolled back: %w", id, err)
		}
	}
	if err := validateDecisionIdempotencySchema(ctx, tx); err != nil {
		return fmt.Errorf("validate migration %s: %w", id, err)
	}
	if err := recordAppliedMigration(ctx, tx, id); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", id, err)
	}
	return nil
}

func validateDecisionIdempotencySchema(ctx context.Context, q sqlIssueQueryer) error {
	var columnCount int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('decisions') WHERE name='idempotency_key' AND type='TEXT'`).Scan(&columnCount); err != nil {
		return fmt.Errorf("inspect decision idempotency column: %w", err)
	}
	if columnCount != 1 {
		return errors.New("decision idempotency schema drifted: missing idempotency_key column")
	}
	var tableSQL string
	if err := q.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='decisions'`).Scan(&tableSQL); err != nil {
		return fmt.Errorf("inspect decisions table: %w", err)
	}
	normalizedTable := strings.ToLower(strings.Join(strings.Fields(tableSQL), " "))
	if !strings.Contains(normalizedTable, "check (idempotency_key is null or trim(idempotency_key) <> '')") {
		return errors.New("decision idempotency schema drifted: missing non-empty key constraint")
	}
	var indexSQL string
	if err := q.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_decisions_idempotency_key'`).Scan(&indexSQL); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("decision idempotency schema drifted: missing unique index")
		}
		return fmt.Errorf("inspect decision idempotency index: %w", err)
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(indexSQL), " "))
	for _, fragment := range []string{"create unique index", "on decisions(idempotency_key)"} {
		if !strings.Contains(normalized, fragment) {
			return fmt.Errorf("decision idempotency schema drifted: index missing %q", fragment)
		}
	}
	return nil
}

func applyProjectionDeltaAuthorityMigration(ctx context.Context, db *sql.DB, id string) error {
	return applyProjectionDeltaAuthorityMigrationWithValidator(ctx, db, id, validateProjectionDeltaAuthoritySchema)
}

func applyProjectionDeltaAuthorityMigrationWithValidator(ctx context.Context, db *sql.DB, id string, validate func(context.Context, projectionDeltaSchemaReader) error) error {
	sqlText, err := loadMigrationSQL("migrations/0047_projection_delta_authority.sql")
	if err != nil {
		return fmt.Errorf("load migration %s: %w", id, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", id, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("apply migration %s: %w", id, err)
	}
	if err := validate(ctx, tx); err != nil {
		return fmt.Errorf("validate migration %s before ledger stamp: %w", id, err)
	}
	if err := recordAppliedMigration(ctx, tx, id); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", id, err)
	}
	return nil
}

type projectionDeltaSchemaReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (c *Client) repairKnownProjectionDeltaBlankChecksum(ctx context.Context, db *sql.DB) error {
	var hasChecksumColumn bool
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(schema_migrations)`)
	if err != nil {
		return fmt.Errorf("inspect projection delta compatibility ledger: %w", err)
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("scan projection delta compatibility ledger: %w", err)
		}
		hasChecksumColumn = hasChecksumColumn || name == "artifact_checksum"
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close projection delta compatibility ledger: %w", err)
	}
	if !hasChecksumColumn {
		return nil
	}

	var checksum sql.NullString
	err = db.QueryRowContext(ctx, `SELECT artifact_checksum FROM schema_migrations WHERE id=?`, projectionDeltaAuthorityMigrationID).Scan(&checksum)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && checksum.Valid && strings.TrimSpace(checksum.String) != "") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect projection delta compatibility marker: %w", err)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire projection delta compatibility connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("begin projection delta checksum compatibility conversion: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	var appliedAt string
	err = conn.QueryRowContext(ctx, `SELECT applied_at, artifact_checksum FROM schema_migrations WHERE id=?`, projectionDeltaAuthorityMigrationID).Scan(&appliedAt, &checksum)
	if err != nil {
		return fmt.Errorf("lock projection delta compatibility marker: %w", err)
	}
	if checksum.Valid && strings.TrimSpace(checksum.String) != "" {
		if checksum.String != projectionDeltaAuthorityChecksum {
			return fmt.Errorf("migration %s historical artifact mutated: ledger has %s, binary has %s", projectionDeltaAuthorityMigrationID, checksum.String, projectionDeltaAuthorityChecksum)
		}
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return fmt.Errorf("commit already-repaired projection delta compatibility conversion: %w", err)
		}
		committed = true
		return nil
	}
	if strings.TrimSpace(appliedAt) == "" {
		return fmt.Errorf("migration %s blank checksum row has empty applied_at", projectionDeltaAuthorityMigrationID)
	}
	if err := validateProjectionDeltaAuthoritySchema(ctx, conn); err != nil {
		return fmt.Errorf("refuse projection delta checksum compatibility conversion: %w", err)
	}
	result, err := conn.ExecContext(ctx, `UPDATE schema_migrations SET artifact_checksum=? WHERE id=? AND (artifact_checksum IS NULL OR trim(artifact_checksum)='')`, projectionDeltaAuthorityChecksum, projectionDeltaAuthorityMigrationID)
	if err != nil {
		return fmt.Errorf("stamp projection delta compatibility checksum: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return fmt.Errorf("stamp projection delta compatibility checksum changed %d rows (count error %v), want 1", updated, err)
	}
	if c.projectionDeltaChecksumRepairHook != nil {
		if err := c.projectionDeltaChecksumRepairHook("after_checksum"); err != nil {
			return fmt.Errorf("projection delta checksum compatibility conversion interrupted: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("commit projection delta checksum compatibility conversion: %w", err)
	}
	committed = true
	return nil
}

func validateProjectionDeltaAuthoritySchema(ctx context.Context, db projectionDeltaSchemaReader) error {
	expectedDDL := map[string]string{
		"projection_streams": `CREATE TABLE projection_streams (
    project_id TEXT PRIMARY KEY,
    head_cursor INTEGER NOT NULL DEFAULT 0 CHECK (head_cursor >= 0),
    updated_at TEXT NOT NULL
)`,
		"projection_deltas": `CREATE TABLE projection_deltas (
    project_id TEXT NOT NULL,
    cursor INTEGER NOT NULL CHECK (cursor > 0),
    kind TEXT NOT NULL CHECK (trim(kind) != ''),
    key TEXT NOT NULL CHECK (trim(key) != ''),
    operation TEXT NOT NULL CHECK (operation IN ('upsert', 'delete')),
    idempotency_key TEXT NOT NULL CHECK (trim(idempotency_key) != ''),
    payload_json TEXT NOT NULL,
    committed_at TEXT NOT NULL,
    PRIMARY KEY (project_id, cursor),
    UNIQUE (project_id, idempotency_key),
    FOREIGN KEY (project_id) REFERENCES projection_streams(project_id) ON DELETE CASCADE
)`,
		"projection_consumer_cursors": `CREATE TABLE projection_consumer_cursors (
    project_id TEXT NOT NULL,
    consumer TEXT NOT NULL CHECK (trim(consumer) != ''),
    cursor INTEGER NOT NULL DEFAULT 0 CHECK (cursor >= 0),
    updated_at TEXT NOT NULL,
    PRIMARY KEY (project_id, consumer),
    FOREIGN KEY (project_id) REFERENCES projection_streams(project_id) ON DELETE CASCADE
)`,
		"idx_projection_deltas_key_history": `CREATE INDEX idx_projection_deltas_key_history
    ON projection_deltas(project_id, kind, key, cursor DESC)`,
	}
	required := []struct{ kind, name string }{
		{"table", "projection_streams"},
		{"table", "projection_deltas"},
		{"table", "projection_consumer_cursors"},
		{"index", "idx_projection_deltas_key_history"},
	}
	for _, object := range required {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type=? AND name=?)`, object.kind, object.name).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("applied migration 0047_projection_delta_authority is missing %s %s", object.kind, object.name)
		}
		var ddl string
		if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type=? AND name=?`, object.kind, object.name).Scan(&ddl); err != nil {
			return err
		}
		if normalizeProjectionDDL(ddl) != normalizeProjectionDDL(expectedDDL[object.name]) {
			return fmt.Errorf("applied migration %s %s %s definition drift", projectionDeltaAuthorityMigrationID, object.kind, object.name)
		}
	}
	type columnContract struct {
		name, typ, defaultValue string
		notNull, primaryKey     int
	}
	columns := map[string][]columnContract{
		"projection_streams": {
			{name: "project_id", typ: "TEXT", primaryKey: 1},
			{name: "head_cursor", typ: "INTEGER", defaultValue: "0", notNull: 1},
			{name: "updated_at", typ: "TEXT", notNull: 1},
		},
		"projection_deltas": {
			{name: "project_id", typ: "TEXT", notNull: 1, primaryKey: 1},
			{name: "cursor", typ: "INTEGER", notNull: 1, primaryKey: 2},
			{name: "kind", typ: "TEXT", notNull: 1},
			{name: "key", typ: "TEXT", notNull: 1},
			{name: "operation", typ: "TEXT", notNull: 1},
			{name: "idempotency_key", typ: "TEXT", notNull: 1},
			{name: "payload_json", typ: "TEXT", notNull: 1},
			{name: "committed_at", typ: "TEXT", notNull: 1},
		},
		"projection_consumer_cursors": {
			{name: "project_id", typ: "TEXT", notNull: 1, primaryKey: 1},
			{name: "consumer", typ: "TEXT", notNull: 1, primaryKey: 2},
			{name: "cursor", typ: "INTEGER", defaultValue: "0", notNull: 1},
			{name: "updated_at", typ: "TEXT", notNull: 1},
		},
	}
	for table, requiredColumns := range columns {
		rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
		if err != nil {
			return err
		}
		var found []columnContract
		for rows.Next() {
			var cid int
			var current columnContract
			var defaultValue sql.NullString
			if err := rows.Scan(&cid, &current.name, &current.typ, &current.notNull, &defaultValue, &current.primaryKey); err != nil {
				rows.Close()
				return err
			}
			if defaultValue.Valid {
				current.defaultValue = defaultValue.String
			}
			found = append(found, current)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if !reflect.DeepEqual(found, requiredColumns) {
			return fmt.Errorf("applied migration %s column contract drift on %s: got %+v want %+v", projectionDeltaAuthorityMigrationID, table, found, requiredColumns)
		}
	}
	for _, table := range []string{"projection_deltas", "projection_consumer_cursors"} {
		rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_list(`+table+`)`)
		if err != nil {
			return err
		}
		var foreignKeys, matchingForeignKeys int
		for rows.Next() {
			foreignKeys++
			var id, seq int
			var target, from, to, onUpdate, onDelete, match string
			if err := rows.Scan(&id, &seq, &target, &from, &to, &onUpdate, &onDelete, &match); err != nil {
				rows.Close()
				return err
			}
			if target == "projection_streams" && from == "project_id" && to == "project_id" && onDelete == "CASCADE" {
				matchingForeignKeys++
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if foreignKeys != 1 || matchingForeignKeys != 1 {
			return fmt.Errorf("applied migration 0047_projection_delta_authority foreign key contract drift on %s", table)
		}
	}
	var indexUnique, indexPartial int
	var indexOrigin string
	if err := db.QueryRowContext(ctx, `SELECT [unique], origin, partial FROM pragma_index_list('projection_deltas') WHERE name='idx_projection_deltas_key_history'`).Scan(&indexUnique, &indexOrigin, &indexPartial); err != nil {
		return fmt.Errorf("inspect projection delta key-history index contract: %w", err)
	}
	if indexUnique != 0 || indexOrigin != "c" || indexPartial != 0 {
		return fmt.Errorf("projection delta key-history index flags drift: unique=%d origin=%s partial=%d", indexUnique, indexOrigin, indexPartial)
	}
	type indexColumn struct {
		name string
		desc int
	}
	indexRows, err := db.QueryContext(ctx, `SELECT name, [desc] FROM pragma_index_xinfo('idx_projection_deltas_key_history') WHERE key=1 ORDER BY seqno`)
	if err != nil {
		return fmt.Errorf("inspect projection delta key-history index columns: %w", err)
	}
	var indexColumns []indexColumn
	for indexRows.Next() {
		var current indexColumn
		if err := indexRows.Scan(&current.name, &current.desc); err != nil {
			indexRows.Close()
			return err
		}
		indexColumns = append(indexColumns, current)
	}
	if err := indexRows.Close(); err != nil {
		return err
	}
	wantIndexColumns := []indexColumn{{"project_id", 0}, {"kind", 0}, {"key", 0}, {"cursor", 1}}
	if !reflect.DeepEqual(indexColumns, wantIndexColumns) {
		return fmt.Errorf("projection delta key-history index columns drift: got %+v want %+v", indexColumns, wantIndexColumns)
	}
	var uniqueShapeCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM pragma_index_list('projection_deltas') l
		WHERE l.[unique]=1 AND l.origin='u' AND (
			SELECT group_concat(name, ',') FROM pragma_index_info(l.name) ORDER BY seqno
		)='project_id,idempotency_key'
	`).Scan(&uniqueShapeCount); err != nil {
		return fmt.Errorf("inspect projection delta idempotency unique contract: %w", err)
	}
	if uniqueShapeCount != 1 {
		return fmt.Errorf("projection delta idempotency unique contract count=%d, want 1", uniqueShapeCount)
	}
	checks := map[string][]string{
		"projection_streams":          {"check (head_cursor >= 0)"},
		"projection_deltas":           {"check (cursor > 0)", "check (trim(kind) != '')", "check (trim(key) != '')", "check (operation in ('upsert', 'delete'))", "check (trim(idempotency_key) != '')", "unique (project_id, idempotency_key)"},
		"projection_consumer_cursors": {"check (trim(consumer) != '')", "check (cursor >= 0)"},
	}
	for table, requiredChecks := range checks {
		var ddl string
		if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
			return err
		}
		normalized := strings.ToLower(strings.Join(strings.Fields(ddl), " "))
		for _, required := range requiredChecks {
			if !strings.Contains(normalized, required) {
				return fmt.Errorf("applied migration %s table %s missing constraint %q", projectionDeltaAuthorityMigrationID, table, required)
			}
		}
	}
	return nil
}

func normalizeProjectionDDL(ddl string) string {
	return strings.ToLower(strings.Join(strings.Fields(ddl), " "))
}

func validateManagedAgentIdentitySchema(ctx context.Context, db *sql.DB) error {
	var tableSQL string
	if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='daemon_managed_agent_incarnations'`).Scan(&tableSQL); err != nil {
		return fmt.Errorf("inspect managed agent identity table: %w", err)
	}
	normalizedTable := strings.NewReplacer(" ", "", "\n", "", "\t", "", "\r", "").Replace(strings.ToLower(tableSQL))
	for _, fragment := range []string{"pane_pidintegernotnullcheck(pane_pid>0)", "primarykey(project_id,session_id,logical_pane_id)"} {
		if !strings.Contains(normalizedTable, fragment) {
			return fmt.Errorf("managed agent identity schema drifted: missing constraint %s", fragment)
		}
	}
	for _, column := range []string{"project_id", "session_id", "logical_pane_id", "tmux_pane_id", "pane_pid", "agent_incarnation", "observed_at", "updated_at"} {
		exists, err := columnExistsDB(ctx, db, "daemon_managed_agent_incarnations", column)
		if err != nil {
			return fmt.Errorf("inspect managed agent identity column %s: %w", column, err)
		}
		if !exists {
			return fmt.Errorf("managed agent identity schema drifted: missing column %s", column)
		}
	}
	var indexSQL string
	err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_daemon_managed_agent_physical_incarnation'`).Scan(&indexSQL)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("managed agent identity schema drifted: missing index idx_daemon_managed_agent_physical_incarnation")
	}
	if err != nil {
		return fmt.Errorf("inspect managed agent identity index: %w", err)
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(indexSQL), " "))
	for _, fragment := range []string{"unique index", "(project_id, tmux_pane_id, pane_pid, agent_incarnation)"} {
		if !strings.Contains(normalized, fragment) {
			return fmt.Errorf("managed agent identity schema drifted: index idx_daemon_managed_agent_physical_incarnation has unexpected definition")
		}
	}
	return nil
}

func validateAgentInputDeliveryBaseSchema(ctx context.Context, db sqlIssueQueryer) error {
	contract, err := agentInputDeliveryBaseSchemaContract()
	if err != nil {
		return fmt.Errorf("derive agent input delivery base schema from pinned artifact: %w", err)
	}
	return validateAgentInputDeliverySchemaContract(ctx, db, contract)
}

func validateAgentInputDeliverySchema(ctx context.Context, db sqlIssueQueryer) error {
	contract, err := agentInputDeliveryFencedSchemaContract()
	if err != nil {
		return fmt.Errorf("derive agent input delivery fenced schema from pinned artifacts: %w", err)
	}
	return validateAgentInputDeliverySchemaContract(ctx, db, contract)
}

type exactSQLiteColumn struct {
	name       string
	columnType string
	notNull    int
	defaultSQL string
	primaryKey int
}

type exactSQLiteSchemaObject struct {
	objectType string
	name       string
	tableName  string
	sql        string
}

type exactSQLiteIndexColumn struct {
	sequence   int
	columnID   int
	name       string
	descending int
	collation  string
	key        int
}

type exactSQLiteIndex struct {
	unique  int
	origin  string
	partial int
	columns []exactSQLiteIndexColumn
}

type agentInputDeliverySchemaContract struct {
	objects []exactSQLiteSchemaObject
	columns map[string][]exactSQLiteColumn
	indexes map[string]exactSQLiteIndex
}

var (
	agentInputDeliveryBaseSchemaContract = sync.OnceValues(func() (*agentInputDeliverySchemaContract, error) {
		return deriveAgentInputDeliverySchemaContract(false)
	})
	agentInputDeliveryFencedSchemaContract = sync.OnceValues(func() (*agentInputDeliverySchemaContract, error) {
		return deriveAgentInputDeliverySchemaContract(true)
	})
)

func deriveAgentInputDeliverySchemaContract(fenced bool) (*agentInputDeliverySchemaContract, error) {
	if err := validateMigrationRegistry(); err != nil {
		return nil, fmt.Errorf("authenticate pinned migration artifacts: %w", err)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, fmt.Errorf("open reference database: %w", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	ctx := context.Background()
	baseSQL, err := loadRegisteredMigrationSQL(agentInputDeliveryMigrationID)
	if err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, baseSQL); err != nil {
		return nil, fmt.Errorf("execute pinned migration 0053 reference artifact: %w", err)
	}
	if fenced {
		fencingSQL, err := loadRegisteredMigrationSQL(agentInputDeliveryFencingMigrationID)
		if err != nil {
			return nil, err
		}
		if _, err := db.ExecContext(ctx, fencingSQL); err != nil {
			return nil, fmt.Errorf("execute pinned migration 0054 reference artifact: %w", err)
		}
	}
	return inspectAgentInputDeliverySchemaContract(ctx, db)
}

func loadRegisteredMigrationSQL(id string) (string, error) {
	for _, migration := range orderedMigrations {
		if migration.id == id {
			return loadMigrationSQL(migration.path)
		}
	}
	return "", fmt.Errorf("migration %s is not registered", id)
}

func inspectAgentInputDeliverySchemaContract(ctx context.Context, db sqlIssueQueryer) (*agentInputDeliverySchemaContract, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT type, name, tbl_name, COALESCE(sql, '')
		FROM sqlite_master
		WHERE type IN ('table','index','trigger')
		  AND (name GLOB 'agent_input_delivery_*' OR tbl_name GLOB 'agent_input_delivery_*')
		ORDER BY type, name
	`)
	if err != nil {
		return nil, fmt.Errorf("inspect agent input delivery schema objects: %w", err)
	}
	contract := &agentInputDeliverySchemaContract{
		columns: make(map[string][]exactSQLiteColumn),
		indexes: make(map[string]exactSQLiteIndex),
	}
	for rows.Next() {
		var object exactSQLiteSchemaObject
		if err := rows.Scan(&object.objectType, &object.name, &object.tableName, &object.sql); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan agent input delivery schema object: %w", err)
		}
		contract.objects = append(contract.objects, object)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate agent input delivery schema objects: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close agent input delivery schema object inspection: %w", err)
	}
	for _, object := range contract.objects {
		switch object.objectType {
		case "table":
			columns, err := inspectExactSQLiteColumns(ctx, db, object.name)
			if err != nil {
				return nil, err
			}
			contract.columns[object.name] = columns
		case "index":
			index, err := inspectExactSQLiteIndex(ctx, db, object.tableName, object.name)
			if err != nil {
				return nil, err
			}
			contract.indexes[object.name] = index
		}
	}
	return contract, nil
}

func validateAgentInputDeliverySchemaContract(ctx context.Context, db sqlIssueQueryer, expected *agentInputDeliverySchemaContract) error {
	actual, err := inspectAgentInputDeliverySchemaContract(ctx, db)
	if err != nil {
		return fmt.Errorf("agent input delivery schema drifted: %w", err)
	}
	if len(actual.objects) != len(expected.objects) {
		return fmt.Errorf("agent input delivery schema drifted: schema object inventory has %d objects, want %d", len(actual.objects), len(expected.objects))
	}
	for i := range expected.objects {
		got, want := actual.objects[i], expected.objects[i]
		if got.objectType != want.objectType || got.name != want.name || got.tableName != want.tableName {
			return fmt.Errorf("agent input delivery schema drifted: schema object inventory entry %d is %s %s on %s, want %s %s on %s", i, got.objectType, got.name, got.tableName, want.objectType, want.name, want.tableName)
		}
		if normalizeExactSQLiteDDL(got.sql) != normalizeExactSQLiteDDL(want.sql) {
			return fmt.Errorf("agent input delivery schema drifted: %s %s has non-canonical definition", got.objectType, got.name)
		}
	}
	if !reflect.DeepEqual(actual.columns, expected.columns) {
		return errors.New("agent input delivery schema drifted: table column metadata is non-canonical")
	}
	if !reflect.DeepEqual(actual.indexes, expected.indexes) {
		return errors.New("agent input delivery schema drifted: index metadata is non-canonical")
	}
	return nil
}

func validateAgentInputDeliveryNoInboundDependencies(ctx context.Context, db sqlIssueQueryer) error {
	const target = "agent_input_delivery_intents"
	rows, err := db.QueryContext(ctx, `
		SELECT schema_name, type, name, ddl
		FROM (
			SELECT 'main' AS schema_name, type, name, COALESCE(sql, '') AS ddl
			FROM sqlite_master
			WHERE type IN ('view','trigger')
			UNION ALL
			SELECT 'temp' AS schema_name, type, name, COALESCE(sql, '') AS ddl
			FROM sqlite_temp_master
			WHERE type IN ('view','trigger')
		)
		ORDER BY type, name
	`)
	if err != nil {
		return fmt.Errorf("inspect inbound view and trigger dependencies: %w", err)
	}
	for rows.Next() {
		var schemaName, objectType, name, ddl string
		if err := rows.Scan(&schemaName, &objectType, &name, &ddl); err != nil {
			rows.Close()
			return fmt.Errorf("scan inbound schema dependency: %w", err)
		}
		if exactSQLiteDDLReferencesIdentifier(ddl, target) {
			rows.Close()
			return fmt.Errorf("inbound %s %s.%s references %s", objectType, schemaName, name, target)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate inbound schema dependencies: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close inbound schema dependency inspection: %w", err)
	}

	tables, err := db.QueryContext(ctx, `
		SELECT name
		FROM sqlite_master
		WHERE type='table' AND name NOT LIKE 'sqlite_%' AND lower(name) <> lower(?)
		ORDER BY name
	`, target)
	if err != nil {
		return fmt.Errorf("inspect tables for inbound foreign keys: %w", err)
	}
	var tableNames []string
	for tables.Next() {
		var table string
		if err := tables.Scan(&table); err != nil {
			tables.Close()
			return fmt.Errorf("scan table for inbound foreign keys: %w", err)
		}
		tableNames = append(tableNames, table)
	}
	if err := tables.Err(); err != nil {
		tables.Close()
		return fmt.Errorf("iterate tables for inbound foreign keys: %w", err)
	}
	if err := tables.Close(); err != nil {
		return fmt.Errorf("close table inspection for inbound foreign keys: %w", err)
	}
	for _, table := range tableNames {
		foreignKeys, err := db.QueryContext(ctx, `
			SELECT id
			FROM pragma_foreign_key_list(?)
			WHERE lower([table])=lower(?)
			LIMIT 1
		`, table, target)
		if err != nil {
			return fmt.Errorf("inspect table %s inbound foreign keys: %w", table, err)
		}
		hasDependency := foreignKeys.Next()
		if err := foreignKeys.Err(); err != nil {
			foreignKeys.Close()
			return fmt.Errorf("iterate table %s inbound foreign keys: %w", table, err)
		}
		if err := foreignKeys.Close(); err != nil {
			return fmt.Errorf("close table %s inbound foreign key inspection: %w", table, err)
		}
		if hasDependency {
			return fmt.Errorf("inbound foreign key from table %s references %s", table, target)
		}
	}
	return nil
}

func exactSQLiteDDLReferencesIdentifier(ddl, target string) bool {
	for i := 0; i < len(ddl); {
		switch {
		case i+1 < len(ddl) && ddl[i] == '-' && ddl[i+1] == '-':
			i += 2
			for i < len(ddl) && ddl[i] != '\n' {
				i++
			}
		case i+1 < len(ddl) && ddl[i] == '/' && ddl[i+1] == '*':
			i += 2
			for i+1 < len(ddl) && !(ddl[i] == '*' && ddl[i+1] == '/') {
				i++
			}
			if i+1 < len(ddl) {
				i += 2
			}
		case ddl[i] == '\'':
			i++
			var quotedToken strings.Builder
			for i < len(ddl) {
				if ddl[i] == '\'' {
					if i+1 < len(ddl) && ddl[i+1] == '\'' {
						quotedToken.WriteByte('\'')
						i += 2
						continue
					}
					i++
					break
				}
				quotedToken.WriteByte(ddl[i])
				i++
			}
			// SQLite accepts single-quoted identifiers in table-name positions.
			// Conservatively treat an exact target token as a dependency; rejecting
			// an unusual string literal is safer than rebuilding through an
			// unobserved compatibility-quoted reference.
			if strings.EqualFold(quotedToken.String(), target) {
				return true
			}
		case ddl[i] == '"' || ddl[i] == '`' || ddl[i] == '[':
			quote := ddl[i]
			closeQuote := quote
			if quote == '[' {
				closeQuote = ']'
			}
			i++
			var identifier strings.Builder
			for i < len(ddl) {
				if ddl[i] == closeQuote {
					if quote != '[' && i+1 < len(ddl) && ddl[i+1] == closeQuote {
						identifier.WriteByte(closeQuote)
						i += 2
						continue
					}
					i++
					break
				}
				identifier.WriteByte(ddl[i])
				i++
			}
			if strings.EqualFold(identifier.String(), target) {
				return true
			}
		default:
			if !isSQLiteIdentifierByte(ddl[i]) {
				i++
				continue
			}
			start := i
			for i < len(ddl) && isSQLiteIdentifierByte(ddl[i]) {
				i++
			}
			if strings.EqualFold(ddl[start:i], target) {
				return true
			}
		}
	}
	return false
}

func isSQLiteIdentifierByte(character byte) bool {
	return character == '_' || character == '$' || character >= '0' && character <= '9' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= 0x80
}

func inspectExactSQLiteColumns(ctx context.Context, db sqlIssueQueryer, table string) ([]exactSQLiteColumn, error) {
	rows, err := db.QueryContext(ctx, `SELECT cid, name, type, [notnull], dflt_value, pk, hidden FROM pragma_table_xinfo(?) ORDER BY cid`, table)
	if err != nil {
		return nil, fmt.Errorf("inspect table %s columns: %w", table, err)
	}
	defer rows.Close()
	var columns []exactSQLiteColumn
	for rows.Next() {
		var column exactSQLiteColumn
		var cid, hidden int
		var defaultSQL sql.NullString
		if err := rows.Scan(&cid, &column.name, &column.columnType, &column.notNull, &defaultSQL, &column.primaryKey, &hidden); err != nil {
			return nil, fmt.Errorf("inspect table %s columns: %w", table, err)
		}
		if cid != len(columns) || hidden != 0 {
			return nil, fmt.Errorf("table %s column %s has cid=%d hidden=%d", table, column.name, cid, hidden)
		}
		if defaultSQL.Valid {
			column.defaultSQL = defaultSQL.String
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspect table %s columns: %w", table, err)
	}
	return columns, nil
}

func inspectExactSQLiteIndex(ctx context.Context, db sqlIssueQueryer, table, indexName string) (exactSQLiteIndex, error) {
	var index exactSQLiteIndex
	if err := db.QueryRowContext(ctx, `
		SELECT [unique], origin, partial
		FROM pragma_index_list(?)
		WHERE name=?
	`, table, indexName).Scan(&index.unique, &index.origin, &index.partial); err != nil {
		return exactSQLiteIndex{}, fmt.Errorf("inspect index %s metadata: %w", indexName, err)
	}
	rows, err := db.QueryContext(ctx, `
		SELECT seqno, cid, name, [desc], coll, key
		FROM pragma_index_xinfo(?)
		ORDER BY seqno
	`, indexName)
	if err != nil {
		return exactSQLiteIndex{}, fmt.Errorf("inspect index %s columns: %w", indexName, err)
	}
	defer rows.Close()
	for rows.Next() {
		var column exactSQLiteIndexColumn
		var name, collation sql.NullString
		if err := rows.Scan(&column.sequence, &column.columnID, &name, &column.descending, &collation, &column.key); err != nil {
			return exactSQLiteIndex{}, fmt.Errorf("scan index %s columns: %w", indexName, err)
		}
		if name.Valid {
			column.name = name.String
		}
		if collation.Valid {
			column.collation = collation.String
		}
		index.columns = append(index.columns, column)
	}
	if err := rows.Err(); err != nil {
		return exactSQLiteIndex{}, fmt.Errorf("iterate index %s columns: %w", indexName, err)
	}
	return index, nil
}

func normalizeExactSQLiteDDL(ddl string) string {
	var normalized strings.Builder
	normalized.Grow(len(ddl))
	var quote byte
	for i := 0; i < len(ddl); i++ {
		character := ddl[i]
		if quote != 0 {
			normalized.WriteByte(character)
			if quote == '[' {
				if character == ']' {
					quote = 0
				}
				continue
			}
			if character == quote {
				if i+1 < len(ddl) && ddl[i+1] == quote {
					i++
					normalized.WriteByte(ddl[i])
					continue
				}
				quote = 0
			}
			continue
		}
		switch character {
		case '\'', '"', '`', '[':
			quote = character
			normalized.WriteByte(character)
		case ' ', '\n', '\t', '\r', '\f', '\v':
			continue
		default:
			if character >= 'A' && character <= 'Z' {
				character += 'a' - 'A'
			}
			normalized.WriteByte(character)
		}
	}
	return normalized.String()
}

func repairIssueIDAllocationSchema(ctx context.Context, db *sql.DB) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS issue_id_allocations (
			id TEXT PRIMARY KEY,
			allocated_at TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT ''
		)
	`); err != nil {
		return fmt.Errorf("ensure issue_id_allocations table: %w", err)
	}
	if err := ensureTableColumns(db, "issue_id_allocations", []sqliteColumnSpec{
		{name: "allocated_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "source", ddl: "TEXT NOT NULL DEFAULT ''"},
	}); err != nil {
		return fmt.Errorf("ensure issue_id_allocations columns: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	sources := []struct {
		table  string
		column string
		source string
	}{
		{table: "issues", column: "id", source: "issues"},
		{table: "issue_dependencies", column: "issue_id", source: "issue_dependencies.issue_id"},
		{table: "issue_dependencies", column: "depends_on_id", source: "issue_dependencies.depends_on_id"},
		{table: "issue_external_refs", column: "issue_id", source: "issue_external_refs"},
		{table: "azedarach_external_issue_refs", column: "issue_id", source: "azedarach_external_issue_refs"},
		{table: "spec_requirements", column: "issue_id", source: "spec_requirements"},
		{table: "spec_links", column: "issue_id", source: "spec_links"},
		{table: "issue_attachments", column: "issue_id", source: "issue_attachments"},
		{table: "issue_observation_events", column: "issue_id", source: "issue_observation_events"},
		{table: "daemon_session_projections", column: "issue_id", source: "daemon_session_projections"},
		{table: "daemon_session_observations", column: "issue_id", source: "daemon_session_observations"},
		{table: "daemon_worktree_projections", column: "issue_id", source: "daemon_worktree_projections"},
		{table: "agent_learnings", column: "issue_id", source: "agent_learnings"},
		{table: "agent_learning_relations", column: "scope_issue_id", source: "agent_learning_relations"},
	}
	for _, source := range sources {
		if err := seedIssueIDAllocationsFromColumn(ctx, db, source.table, source.column, source.source, now); err != nil {
			return err
		}
	}
	return nil
}

func seedIssueIDAllocationsFromColumn(ctx context.Context, db *sql.DB, tableName, columnName, source, allocatedAt string) error {
	exists, err := tableExists(db, tableName)
	if err != nil {
		return fmt.Errorf("inspect %s for issue id allocation seed: %w", tableName, err)
	}
	if !exists {
		return nil
	}
	columns, err := tableColumns(db, tableName)
	if err != nil {
		return fmt.Errorf("inspect %s columns for issue id allocation seed: %w", tableName, err)
	}
	if _, ok := columns[columnName]; !ok {
		return nil
	}
	stmt := fmt.Sprintf(`
		INSERT INTO issue_id_allocations (id, allocated_at, source)
		SELECT DISTINCT TRIM(%[1]s), ?, ?
		FROM %[2]s
		WHERE TRIM(COALESCE(%[1]s, '')) <> ''
		ON CONFLICT(id) DO NOTHING
	`, columnName, tableName)
	if _, err := db.ExecContext(ctx, stmt, allocatedAt, source); err != nil {
		return fmt.Errorf("seed issue id allocations from %s.%s: %w", tableName, columnName, err)
	}
	return nil
}

func repairIssueBaseSchema(db *sql.DB) error {
	exists, err := tableExists(db, "issues")
	if err != nil {
		return fmt.Errorf("inspect issues table: %w", err)
	}
	if !exists {
		return nil
	}

	return ensureTableColumns(db, "issues", []sqliteColumnSpec{
		{name: "description", ddl: "TEXT"},
		{name: "status", ddl: "TEXT NOT NULL DEFAULT 'open'"},
		{name: "priority", ddl: "INTEGER NOT NULL DEFAULT 2"},
		{name: "issue_type", ddl: "TEXT NOT NULL DEFAULT 'task'"},
		{name: "created_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "updated_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "closed_at", ddl: "TEXT"},
		{name: "assignee", ddl: "TEXT"},
		{name: "labels_json", ddl: "TEXT"},
		{name: "implementations_json", ddl: "TEXT"},
		{name: "design", ddl: "TEXT"},
		{name: "notes", ddl: "TEXT"},
		{name: "acceptance", ddl: "TEXT"},
		{name: "estimate", ddl: "INTEGER"},
		{name: "deleted_at", ddl: "TEXT"},
	})
}

func repairIssueDependencySchema(db *sql.DB) error {
	issuesExists, err := tableExists(db, "issues")
	if err != nil {
		return fmt.Errorf("inspect issues table: %w", err)
	}
	if !issuesExists {
		return nil
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS issue_dependencies (
			issue_id TEXT NOT NULL,
			depends_on_id TEXT NOT NULL,
			dependency_type TEXT NOT NULL,
			tombstoned_at TEXT,
			PRIMARY KEY (issue_id, depends_on_id, dependency_type)
		)
	`); err != nil {
		return fmt.Errorf("ensure issue_dependencies table: %w", err)
	}
	if err := ensureTableColumns(db, "issue_dependencies", []sqliteColumnSpec{
		{name: "issue_id", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "depends_on_id", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "dependency_type", ddl: "TEXT NOT NULL DEFAULT 'blocks'"},
		{name: "tombstoned_at", ddl: "TEXT"},
	}); err != nil {
		return err
	}

	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_dependencies_issue_active_type ON issue_dependencies(issue_id, tombstoned_at, dependency_type)`,
		`CREATE INDEX IF NOT EXISTS idx_dependencies_depends_on_active_type ON issue_dependencies(depends_on_id, tombstoned_at, dependency_type)`,
		`CREATE INDEX IF NOT EXISTS idx_dependencies_depends_on ON issue_dependencies(depends_on_id, dependency_type, tombstoned_at)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("ensure issue dependency index: %w", err)
		}
	}

	return nil
}

func repairMetaSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("ensure meta table: %w", err)
	}
	return nil
}

func repairIssueExternalRefsSchema(db *sql.DB) error {
	issuesExists, err := tableExists(db, "issues")
	if err != nil {
		return fmt.Errorf("inspect issues table: %w", err)
	}
	if !issuesExists {
		return nil
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS issue_external_refs (
			issue_id TEXT NOT NULL,
			provider TEXT NOT NULL,
			provider_scope TEXT NOT NULL DEFAULT '',
			remote_key TEXT NOT NULL,
			display_key TEXT,
			url TEXT,
			metadata_json TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT,
			PRIMARY KEY (issue_id, provider, provider_scope, remote_key),
			FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
		)
	`); err != nil {
		return fmt.Errorf("ensure issue_external_refs table: %w", err)
	}
	if err := ensureTableColumns(db, "issue_external_refs", []sqliteColumnSpec{
		{name: "issue_id", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "provider", ddl: "TEXT NOT NULL DEFAULT 'linear'"},
		{name: "provider_scope", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "remote_key", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "display_key", ddl: "TEXT"},
		{name: "url", ddl: "TEXT"},
		{name: "metadata_json", ddl: "TEXT"},
		{name: "created_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "updated_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "deleted_at", ddl: "TEXT"},
	}); err != nil {
		return err
	}

	for _, stmt := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_external_refs_active_remote
			ON issue_external_refs(provider, provider_scope, remote_key)
			WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_issue_external_refs_issue_active
			ON issue_external_refs(issue_id, provider, provider_scope, updated_at DESC)
			WHERE deleted_at IS NULL`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("ensure issue external refs index: %w", err)
		}
	}

	return nil
}

func ensureMigrationTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			id TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL,
			artifact_checksum TEXT
		)
	`)
	if err != nil {
		return fmt.Errorf("ensure schema_migrations table: %w", err)
	}
	return nil
}

func isMigrationApplied(ctx context.Context, db *sql.DB, id string) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM schema_migrations WHERE id = ?
		)
	`, id).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

func recordAppliedMigration(ctx context.Context, db sqlitemigration.LedgerWriter, id string) error {
	return recordAppliedMigrationAt(ctx, db, id, time.Now().UTC().Format(time.RFC3339Nano))
}

func recordAppliedMigrationAt(ctx context.Context, db sqlitemigration.LedgerWriter, id, appliedAt string) error {
	return sqlitemigration.RecordApplied(ctx, db, migrationArtifacts, id, appliedAt)
}

func loadMigrationSQL(path string) (string, error) {
	content, err := fs.ReadFile(migrationFiles, path)
	if err != nil {
		return "", err
	}
	sqlText := strings.TrimSpace(string(content))
	if sqlText == "" {
		return "", fmt.Errorf("empty migration sql")
	}
	return sqlText, nil
}

func (c *Client) applyMigration(ctx context.Context, db *sql.DB, id, sqlText string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", id, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	beforeCount, countErr := dependencyCount(tx)
	if countErr != nil {
		return fmt.Errorf("count dependencies before migration %s: %w", id, countErr)
	}

	if _, err := tx.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("apply migration %s: %w", id, err)
	}

	if err := recordAppliedMigration(ctx, tx, id); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}

	afterCount, countErr := dependencyCount(tx)
	if countErr != nil {
		return fmt.Errorf("count dependencies after migration %s: %w", id, countErr)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", id, err)
	}
	tx = nil

	if id == "0002_dependency_foreign_keys" {
		if dropped := beforeCount - afterCount; dropped > 0 {
			c.logger.Warn("dropped orphaned dependency edges during sqlite fk migration", "dropped", dropped)
		}
	}

	return nil
}

func applyOrchestratorLifecycleClockMigration(ctx context.Context, db *sql.DB, id string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()
	columns := []struct{ name, definition string }{
		{"complete_since", "TEXT"},
		{"last_wake_at", "TEXT"},
		{"last_wake_reason", "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range columns {
		exists, err := txColumnExists(ctx, tx, "daemon_orchestrator_scope_leases", column.name)
		if err != nil {
			return fmt.Errorf("inspect migration %s column %s: %w", id, column.name, err)
		}
		if exists {
			continue
		}
		if _, err := tx.ExecContext(ctx, "ALTER TABLE daemon_orchestrator_scope_leases ADD COLUMN "+column.name+" "+column.definition); err != nil {
			return fmt.Errorf("apply migration %s column %s: %w", id, column.name, err)
		}
	}
	if err := recordAppliedMigration(ctx, tx, id); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", id, err)
	}
	return nil
}

func applyContextualLearningActivationMigration(ctx context.Context, db *sql.DB, id string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()

	var legacyApplied bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE id = ?)
	`, legacyContextualLearningMigration).Scan(&legacyApplied); err != nil {
		return fmt.Errorf("inspect legacy migration %s: %w", legacyContextualLearningMigration, err)
	}

	if legacyApplied {
		if err := completeHistoricalContextualLearningSchema(ctx, tx); err != nil {
			return fmt.Errorf("complete legacy migration %s before aliasing to %s: %w", legacyContextualLearningMigration, id, err)
		}
	} else {
		sqlText, err := loadMigrationSQL("migrations/0039_contextual_learning_activation.sql")
		if err != nil {
			return fmt.Errorf("load migration %s: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, sqlText); err != nil {
			return fmt.Errorf("apply migration %s: %w", id, err)
		}
	}

	if err := recordAppliedMigration(ctx, tx, id); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", id, err)
	}
	return nil
}

func completeHistoricalContextualLearningSchema(ctx context.Context, tx *sql.Tx) error {
	for _, column := range []struct {
		name string
		ddl  string
	}{
		{name: "purpose", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "session_id", ddl: "TEXT NOT NULL DEFAULT ''"},
	} {
		exists, err := txColumnExists(ctx, tx, "learning_activations", column.name)
		if err != nil {
			return fmt.Errorf("inspect learning_activations.%s: %w", column.name, err)
		}
		if !exists {
			if _, err := tx.ExecContext(ctx, "ALTER TABLE learning_activations ADD COLUMN "+column.name+" "+column.ddl); err != nil {
				return fmt.Errorf("add learning_activations.%s: %w", column.name, err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_learning_activations_session
			ON learning_activations(project_id, session_id, delivered_at DESC);
		CREATE TABLE IF NOT EXISTS learning_activation_deliveries (
			activation_id TEXT NOT NULL REFERENCES learning_activations(activation_id) ON DELETE CASCADE,
			project_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			learning_id TEXT NOT NULL,
			PRIMARY KEY (project_id, session_id, learning_id)
		);
		CREATE INDEX IF NOT EXISTS idx_learning_activation_deliveries_activation
			ON learning_activation_deliveries(activation_id);
	`); err != nil {
		return fmt.Errorf("complete contextual learning tables and indexes: %w", err)
	}
	return nil
}

func (c *Client) applyBoardViewsMigration(ctx context.Context, db *sql.DB, id, sqlText string) error {
	if err := refusePartialBoardViewsSchema(db); err != nil {
		return fmt.Errorf("migration %s: %w", id, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", id, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	beforeCount, countErr := dependencyCount(tx)
	if countErr != nil {
		return fmt.Errorf("count dependencies before migration %s: %w", id, countErr)
	}
	if _, err := tx.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("apply migration %s: %w", id, err)
	}
	if err := c.runBoardViewsMigrationFailureHook("after_schema"); err != nil {
		return fmt.Errorf("migration %s rolled back: %w", id, err)
	}
	if err := recordAppliedMigration(ctx, tx, id); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}

	afterCount, countErr := dependencyCount(tx)
	if countErr != nil {
		return fmt.Errorf("count dependencies after migration %s: %w", id, countErr)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", id, err)
	}
	tx = nil

	if id == boardViewsMigrationID && beforeCount != afterCount {
		c.logger.Warn("unexpected dependency count change during board view migration", "before", beforeCount, "after", afterCount)
	}
	return nil
}

func (c *Client) runBoardViewsMigrationFailureHook(stage string) error {
	if c.boardViewsMigrationFailureHook == nil {
		return nil
	}
	if err := c.boardViewsMigrationFailureHook(stage); err != nil {
		return fmt.Errorf("injected board views migration failure at %s: %w", stage, err)
	}
	return nil
}

var humanAuthorityProjectionRevisionTriggers = []string{
	"projection_source_revision_issue_observations_insert",
	"projection_source_revision_issue_observations_update",
	"projection_source_revision_issue_observations_delete",
	"projection_source_revision_interactions_insert",
	"projection_source_revision_interactions_update",
	"projection_source_revision_interactions_delete",
}

func (c *Client) applyHumanAuthorityProjectionMigration(ctx context.Context, db *sql.DB, id string) error {
	sqlText, err := loadMigrationSQL("migrations/0047_human_authority_projection_revision.sql")
	if err != nil {
		return fmt.Errorf("load migration %s: %w", id, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("apply migration %s: %w", id, err)
	}
	if err := c.runHumanAuthorityMigrationFailureHook("after_schema"); err != nil {
		return fmt.Errorf("migration %s rolled back: %w", id, err)
	}
	if err := validateHumanAuthorityProjectionRevisionTriggers(ctx, tx); err != nil {
		return fmt.Errorf("validate migration %s: %w", id, err)
	}
	if err := recordAppliedMigration(ctx, tx, id); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", id, err)
	}
	return nil
}

func (c *Client) runHumanAuthorityMigrationFailureHook(stage string) error {
	if c.humanAuthorityMigrationFailureHook == nil {
		return nil
	}
	if err := c.humanAuthorityMigrationFailureHook(stage); err != nil {
		return fmt.Errorf("injected human authority migration failure at %s: %w", stage, err)
	}
	return nil
}

func (c *Client) applyDecisionPropagationOutboxMigration(ctx context.Context, db *sql.DB, id string) error {
	sqlText, err := loadMigrationSQL("migrations/0048_decision_propagation_outbox.sql")
	if err != nil {
		return fmt.Errorf("load migration %s: %w", id, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", id, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("apply migration %s: %w", id, err)
	}
	if c.decisionOutboxMigrationFailureHook != nil {
		if err := c.decisionOutboxMigrationFailureHook("after_schema"); err != nil {
			return fmt.Errorf("migration %s rolled back: %w", id, err)
		}
	}
	if err := validateDecisionPropagationOutboxSchema(ctx, tx); err != nil {
		return fmt.Errorf("validate migration %s: %w", id, err)
	}
	if err := recordAppliedMigration(ctx, tx, id); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", id, err)
	}
	return nil
}

func (c *Client) applyAgentInputDeliveryMigration(ctx context.Context, db *sql.DB, id string) error {
	sqlText, err := loadMigrationSQL("migrations/0053_agent_input_delivery.sql")
	if err != nil {
		return fmt.Errorf("load migration %s: %w", id, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", id, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("apply migration %s: %w", id, err)
	}
	if c.agentInputMigrationFailureHook != nil {
		if err := c.agentInputMigrationFailureHook("after_schema"); err != nil {
			return fmt.Errorf("migration %s rolled back: %w", id, err)
		}
	}
	if err := validateAgentInputDeliveryBaseSchema(ctx, tx); err != nil {
		return fmt.Errorf("validate migration %s: %w", id, err)
	}
	if err := recordAppliedMigration(ctx, tx, id); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", id, err)
	}
	return nil
}

func (c *Client) applyAgentInputDeliveryFencingMigration(ctx context.Context, db *sql.DB, id string) error {
	sqlText, err := loadMigrationSQL("migrations/0054_agent_input_delivery_fencing.sql")
	if err != nil {
		return fmt.Errorf("load migration %s: %w", id, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", id, err)
	}
	defer tx.Rollback()
	// Migration 0054 rebuilds the 0053 table. Validate the exact immutable
	// predecessor, including both indexes, before executing any destructive DDL
	// so schema drift remains intact and diagnosable on failure.
	if err := validateAgentInputDeliveryBaseSchema(ctx, tx); err != nil {
		return fmt.Errorf("validate migration %s predecessor: %w", id, err)
	}
	if err := validateAgentInputDeliveryNoInboundDependencies(ctx, tx); err != nil {
		return fmt.Errorf("validate migration %s predecessor dependencies: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("apply migration %s: %w", id, err)
	}
	if c.agentInputMigrationFailureHook != nil {
		if err := c.agentInputMigrationFailureHook("after_fencing_schema"); err != nil {
			return fmt.Errorf("migration %s rolled back: %w", id, err)
		}
	}
	if err := validateAgentInputDeliverySchema(ctx, tx); err != nil {
		return fmt.Errorf("validate migration %s: %w", id, err)
	}
	if err := recordAppliedMigration(ctx, tx, id); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", id, err)
	}
	return nil
}

func validateDecisionPropagationOutboxSchema(ctx context.Context, q sqlIssueQueryer) error {
	var tableSQL string
	if err := q.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='decision_propagation_outbox'`).Scan(&tableSQL); err != nil {
		return fmt.Errorf("inspect decision propagation outbox table: %w", err)
	}
	normalizedSQL := strings.ToLower(strings.Join(strings.Fields(tableSQL), " "))
	for _, fragment := range []string{
		"id integer primary key autoincrement",
		"decision_id text not null",
		"revision integer not null check (revision > 0)",
		"issue_id text not null",
		"event_kind text not null check (event_kind in ('changed', 'withdrawn'))",
		"source_command text not null default ''",
		"payload_json text not null check (json_valid(payload_json))",
		"created_at text not null",
		"unique (decision_id, revision, issue_id, event_kind)",
		"foreign key (revision) references decision_audit_log(id)",
	} {
		if !strings.Contains(normalizedSQL, fragment) {
			return fmt.Errorf("decision propagation outbox missing constraint %s", fragment)
		}
	}
	required := map[string]bool{
		"id": false, "decision_id": false, "revision": false, "issue_id": false,
		"event_kind": false, "source_command": false, "payload_json": false,
		"created_at": false, "materialized_event_id": false, "retired_at": false,
	}
	rows, err := q.QueryContext(ctx, `PRAGMA table_info('decision_propagation_outbox')`)
	if err != nil {
		return fmt.Errorf("inspect decision propagation outbox columns: %w", err)
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		if _, ok := required[name]; ok {
			required[name] = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for name, found := range required {
		if !found {
			return fmt.Errorf("decision propagation outbox missing column %s", name)
		}
	}
	expectedIndexes := map[string]string{
		"idx_decision_propagation_outbox_active":         "create index idx_decision_propagation_outbox_active on decision_propagation_outbox(id) where retired_at is null",
		"idx_decision_propagation_outbox_issue_revision": "create index idx_decision_propagation_outbox_issue_revision on decision_propagation_outbox(issue_id, decision_id, revision)",
	}
	for index, expectedSQL := range expectedIndexes {
		var indexSQL string
		if err := q.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&indexSQL); err != nil {
			return fmt.Errorf("inspect decision propagation outbox index %s: %w", index, err)
		}
		normalizedIndexSQL := strings.ToLower(strings.Join(strings.Fields(indexSQL), " "))
		if normalizedIndexSQL != expectedSQL {
			return fmt.Errorf("decision propagation outbox index %s drifted: got %q", index, normalizedIndexSQL)
		}
	}
	return nil
}

func (c *Client) applyIssueObservationEventSearchMigration(ctx context.Context, db *sql.DB, id string) error {
	sqlText, err := loadMigrationSQL("migrations/0050_issue_observation_event_search.sql")
	if err != nil {
		return fmt.Errorf("load migration %s: %w", id, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", id, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("apply migration %s: %w", id, err)
	}
	if c.eventSearchMigrationFailureHook != nil {
		if err := c.eventSearchMigrationFailureHook("after_schema"); err != nil {
			return fmt.Errorf("migration %s rolled back: %w", id, err)
		}
	}
	if err := validateIssueObservationEventSearchSchema(ctx, tx); err != nil {
		return fmt.Errorf("validate migration %s: %w", id, err)
	}
	if err := validateIssueObservationEventSearchCoverage(ctx, tx); err != nil {
		return fmt.Errorf("validate migration %s backfill: %w", id, err)
	}
	if err := recordAppliedMigration(ctx, tx, id); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", id, err)
	}
	return nil
}

func validateIssueObservationEventSearchSchema(ctx context.Context, q sqlIssueQueryer) error {
	var tableSQL string
	if err := q.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='issue_observation_event_search_fts'`).Scan(&tableSQL); err != nil {
		return fmt.Errorf("inspect issue observation event search table: %w", err)
	}
	normalizedTableSQL := strings.ToLower(strings.Join(strings.Fields(tableSQL), " "))
	for _, fragment := range []string{"using fts5", "issue_id unindexed", "content", "content = ''", "detail = none", "tokenize = 'unicode61'"} {
		if !strings.Contains(normalizedTableSQL, fragment) {
			return fmt.Errorf("issue observation event search table drifted: missing %s", fragment)
		}
	}
	for _, trigger := range []string{"issue_observation_events_ai_search_fts", "issue_observation_events_ad_search_fts", "issue_observation_events_au_search_fts"} {
		var triggerSQL string
		if err := q.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, trigger).Scan(&triggerSQL); err != nil {
			return fmt.Errorf("inspect issue observation event search trigger %s: %w", trigger, err)
		}
		normalized := strings.ToLower(strings.Join(strings.Fields(triggerSQL), " "))
		for _, fragment := range []string{"issue_observation_event_search_fts", "$.summary", "$.body", "$.message", "$.line", "$.evidence"} {
			if !strings.Contains(normalized, fragment) {
				return fmt.Errorf("issue observation event search trigger %s drifted: missing %s", trigger, fragment)
			}
		}
	}
	expectedIndexes := map[string]string{
		"idx_issue_observation_events_issue_source_id":              "create index idx_issue_observation_events_issue_source_id on issue_observation_events(issue_id, source, id)",
		"idx_issue_observation_events_issue_source_command_id":      "create index idx_issue_observation_events_issue_source_command_id on issue_observation_events(issue_id, source_command, id)",
		"idx_issue_observation_events_issue_operation_id_id":        "create index idx_issue_observation_events_issue_operation_id_id on issue_observation_events(issue_id, operation_id, id)",
		"idx_issue_observation_events_issue_session_id_id":          "create index idx_issue_observation_events_issue_session_id_id on issue_observation_events(issue_id, session_id, id)",
		"idx_issue_observation_events_issue_worktree_path_id":       "create index idx_issue_observation_events_issue_worktree_path_id on issue_observation_events(issue_id, worktree_path, id)",
		"idx_issue_observation_events_issue_payload_outcome_id":     "create index idx_issue_observation_events_issue_payload_outcome_id on issue_observation_events(issue_id, json_extract(payload_json, '$.outcome'), id)",
		"idx_issue_observation_events_issue_payload_disposition_id": "create index idx_issue_observation_events_issue_payload_disposition_id on issue_observation_events(issue_id, json_extract(payload_json, '$.disposition'), id)",
		"idx_issue_observation_events_issue_payload_decision_id_id": "create index idx_issue_observation_events_issue_payload_decision_id_id on issue_observation_events(issue_id, json_extract(payload_json, '$.decision_id'), id)",
		"idx_issue_observation_events_issue_payload_revision_id":    "create index idx_issue_observation_events_issue_payload_revision_id on issue_observation_events(issue_id, json_extract(payload_json, '$.revision'), id)",
		"idx_issue_observation_events_issue_payload_actor_id_id":    "create index idx_issue_observation_events_issue_payload_actor_id_id on issue_observation_events(issue_id, json_extract(payload_json, '$.actor_id'), id)",
	}
	for index, expectedSQL := range expectedIndexes {
		var indexSQL string
		if err := q.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&indexSQL); err != nil {
			return fmt.Errorf("inspect issue observation event search index %s: %w", index, err)
		}
		if normalized := strings.ToLower(strings.Join(strings.Fields(indexSQL), " ")); normalized != expectedSQL {
			return fmt.Errorf("issue observation event search index %s drifted: got %q", index, normalized)
		}
	}
	return nil
}

func validateIssueObservationEventSearchCoverage(ctx context.Context, q sqlIssueQueryer) error {
	var eventCount, searchCount, missingCount int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_observation_events`).Scan(&eventCount); err != nil {
		return fmt.Errorf("count issue observation events for search validation: %w", err)
	}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_observation_event_search_fts`).Scan(&searchCount); err != nil {
		return fmt.Errorf("count issue observation event search rows: %w", err)
	}
	if searchCount != eventCount {
		return fmt.Errorf("issue observation event search projection drifted: events=%d search_rows=%d", eventCount, searchCount)
	}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_observation_events AS events LEFT JOIN issue_observation_event_search_fts ON issue_observation_event_search_fts.rowid=events.id WHERE issue_observation_event_search_fts.rowid IS NULL`).Scan(&missingCount); err != nil {
		return fmt.Errorf("inspect issue observation event search coverage: %w", err)
	}
	if missingCount != 0 {
		return fmt.Errorf("issue observation event search projection drifted: missing_rows=%d", missingCount)
	}
	return nil
}

func validateHumanAuthorityProjectionRevisionTriggers(ctx context.Context, q sqlIssueQueryer) error {
	for _, name := range humanAuthorityProjectionRevisionTriggers {
		var exists bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='trigger' AND name=?)`, name).Scan(&exists); err != nil {
			return fmt.Errorf("inspect human authority projection revision trigger %s: %w", name, err)
		}
		if !exists {
			return fmt.Errorf("applied migration %s is missing required trigger %s", humanAuthorityProjectionMigrationID, name)
		}
	}
	return nil
}

func refusePartialBoardViewsSchema(db *sql.DB) error {
	exists, err := tableExists(db, "board_views")
	if err != nil {
		return fmt.Errorf("inspect board_views table: %w", err)
	}
	if !exists {
		return nil
	}
	columns, err := tableColumns(db, "board_views")
	if err != nil {
		return fmt.Errorf("inspect board_views columns: %w", err)
	}
	missing := missingBoardViewsColumns(columns)
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("refusing startup with partial board_views schema: missing columns %s; restore the database from backup before retrying", strings.Join(missing, ", "))
}

func missingBoardViewsColumns(columns map[string]struct{}) []string {
	required := []string{
		"project_id",
		"id",
		"name",
		"definition_json",
		"built_in",
		"created_at",
		"updated_at",
		"deleted_at",
	}
	missing := make([]string, 0)
	for _, column := range required {
		if _, ok := columns[column]; !ok {
			missing = append(missing, column)
		}
	}
	sort.Strings(missing)
	return missing
}

func refusePartialIssueStateModelV2Cutover(ctx context.Context, db *sql.DB) error {
	marker, ok, err := readIssueStateModelV2CutoverMarker(ctx, db)
	if err != nil {
		return fmt.Errorf("inspect issue state-model v2 cutover marker: %w", err)
	}
	if !ok {
		return nil
	}
	switch marker.State {
	case "complete", "":
		return nil
	default:
		return issueStateModelV2CutoverError("refusing startup after partial issue state-model v2 cutover", marker.BackupPath, marker.Error)
	}
}

func (c *Client) applyIssueStateModelV2Migration(ctx context.Context, db *sql.DB, id string) error {
	issuesExists, err := tableExists(db, "issues")
	if err != nil {
		return fmt.Errorf("inspect migration %s issues table: %w", id, err)
	}
	if !issuesExists {
		return recordAppliedMigration(ctx, db, id)
	}

	columns, err := tableColumns(db, "issues")
	if err != nil {
		return fmt.Errorf("inspect migration %s issues columns: %w", id, err)
	}
	targetColumns := []string{"lifecycle_state", "closed_outcome", "review_state", "archived_at"}
	existingTargetColumns := 0
	for _, column := range targetColumns {
		if _, ok := columns[column]; ok {
			existingTargetColumns++
		}
	}
	if existingTargetColumns > 0 && existingTargetColumns != len(targetColumns) {
		return issueStateModelV2CutoverError("refusing startup after partial issue state-model v2 schema cutover", "", "")
	}
	if existingTargetColumns == len(targetColumns) {
		if err := validateIssueStateModelV2Rows(ctx, db); err != nil {
			return fmt.Errorf("validate existing issue state-model v2 rows: %w", err)
		}
		if err := setIssueStateModelV2CompleteMarker(ctx, db, ""); err != nil {
			return fmt.Errorf("mark existing issue state-model v2 migration complete: %w", err)
		}
		return recordAppliedMigration(ctx, db, id)
	}

	backupPath, err := c.backupIssueDBForStateModelV2(ctx, db)
	if err != nil {
		return fmt.Errorf("backup issue DB before migration %s: %w", id, err)
	}
	startedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeIssueStateModelV2CutoverMarker(ctx, db, issueStateModelV2CutoverMarker{
		State:      "in_progress",
		StartedAt:  startedAt,
		BackupPath: backupPath,
	}); err != nil {
		return fmt.Errorf("mark migration %s in progress: %w", id, err)
	}

	if err := c.runIssueStateModelV2CutoverTransaction(ctx, db, id, backupPath, startedAt); err != nil {
		marker := issueStateModelV2CutoverMarker{
			State:      "failed",
			StartedAt:  startedAt,
			BackupPath: backupPath,
			Error:      err.Error(),
		}
		if markerErr := writeIssueStateModelV2CutoverMarker(context.Background(), db, marker); markerErr != nil {
			return issueStateModelV2CutoverError(
				fmt.Sprintf("migration %s failed and failed to record rollback details: %v", id, markerErr),
				backupPath,
				err.Error(),
			)
		}
		return issueStateModelV2CutoverError(fmt.Sprintf("migration %s rolled back", id), backupPath, err.Error())
	}

	return nil
}

func applyIssueClosedRuntimeV2TriggersMigration(ctx context.Context, db *sql.DB, id string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", id, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, issueClosedRuntimeProjectionTablesSQL); err != nil {
		return fmt.Errorf("ensure migration %s runtime projection tables: %w", id, err)
	}

	for _, triggerName := range []string{
		"issue_closed_runtime_guard_insert",
		"issue_closed_runtime_guard_update",
		"daemon_worktree_closed_issue_guard_insert",
		"daemon_worktree_closed_issue_guard_update",
		"daemon_session_closed_issue_guard_insert",
		"daemon_session_closed_issue_guard_update",
		"issue_dependency_closed_runtime_guard_insert",
		"issue_dependency_closed_runtime_guard_update",
		"issue_descendant_closed_ancestor_guard_update",
	} {
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS `+triggerName); err != nil {
			return fmt.Errorf("drop trigger %s: %w", triggerName, err)
		}
	}

	if _, err := tx.ExecContext(ctx, issueClosedRuntimeV2TriggersSQL); err != nil {
		return fmt.Errorf("apply migration %s triggers: %w", id, err)
	}
	if err := recordAppliedMigration(ctx, tx, id); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", id, err)
	}
	tx = nil
	return nil
}

const issueClosedRuntimeProjectionTablesSQL = `
CREATE TABLE IF NOT EXISTS daemon_session_projections (
	project_id TEXT NOT NULL,
	session_id TEXT NOT NULL,
	issue_id TEXT NOT NULL,
	state TEXT NOT NULL,
	started_at TEXT,
	updated_at TEXT NOT NULL,
	tmux_attached_count INTEGER NOT NULL DEFAULT 0,
	observed_state TEXT,
	activity TEXT,
	activity_source TEXT,
	PRIMARY KEY (project_id, session_id)
);

CREATE INDEX IF NOT EXISTS idx_daemon_session_projections_project_issue
	ON daemon_session_projections (project_id, issue_id);

CREATE INDEX IF NOT EXISTS idx_daemon_session_projections_project_issue_updated
	ON daemon_session_projections (project_id, issue_id, updated_at DESC, session_id DESC);

CREATE TABLE IF NOT EXISTS daemon_worktree_projections (
	project_id TEXT NOT NULL,
	issue_id TEXT NOT NULL,
	path TEXT NOT NULL,
	branch TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	git_status_json TEXT,
	git_status_updated_at TEXT,
	PRIMARY KEY (project_id, issue_id)
);

CREATE INDEX IF NOT EXISTS idx_daemon_worktree_projections_project_path
	ON daemon_worktree_projections (project_id, path);
`

const issueClosedRuntimeV2TriggersSQL = `
CREATE TRIGGER issue_closed_runtime_guard_insert
BEFORE INSERT ON issues
WHEN NEW.lifecycle_state = 'closed'
BEGIN
	SELECT RAISE(ABORT, 'closed issue cannot have active runtime attachments')
	WHERE
		EXISTS (
			SELECT 1
			FROM daemon_worktree_projections w
			WHERE
				w.issue_id = NEW.id
				AND TRIM(COALESCE(w.path, '')) <> ''
		)
		OR EXISTS (
			SELECT 1
			FROM daemon_session_projections s
			WHERE
				s.issue_id = NEW.id
				AND LOWER(TRIM(COALESCE(s.state, ''))) <> 'stopped'
		)
		OR EXISTS (
			WITH RECURSIVE descendants(issue_id) AS (
				SELECT d.issue_id
				FROM issue_dependencies d
				WHERE
					d.depends_on_id = NEW.id
					AND d.tombstoned_at IS NULL
					AND d.dependency_type IN ('parent-child', 'parent_child')
				UNION
				SELECT d.issue_id
				FROM descendants
				JOIN issue_dependencies d ON d.depends_on_id = descendants.issue_id
				WHERE
					d.tombstoned_at IS NULL
					AND d.dependency_type IN ('parent-child', 'parent_child')
			)
			SELECT 1
			FROM descendants
			JOIN issues child ON child.id = descendants.issue_id
			WHERE
				child.archived_at IS NULL
				AND (
					child.lifecycle_state <> 'closed'
					OR
					EXISTS (
						SELECT 1
						FROM daemon_worktree_projections w
						WHERE
							w.issue_id = child.id
							AND TRIM(COALESCE(w.path, '')) <> ''
					)
					OR EXISTS (
						SELECT 1
						FROM daemon_session_projections s
						WHERE
							s.issue_id = child.id
							AND LOWER(TRIM(COALESCE(s.state, ''))) <> 'stopped'
					)
				)
		);
END;

CREATE TRIGGER issue_closed_runtime_guard_update
BEFORE UPDATE OF lifecycle_state ON issues
WHEN NEW.lifecycle_state = 'closed'
BEGIN
	SELECT RAISE(ABORT, 'closed issue cannot have active runtime attachments')
	WHERE
		EXISTS (
			SELECT 1
			FROM daemon_worktree_projections w
			WHERE
				w.issue_id = NEW.id
				AND TRIM(COALESCE(w.path, '')) <> ''
		)
		OR EXISTS (
			SELECT 1
			FROM daemon_session_projections s
			WHERE
				s.issue_id = NEW.id
				AND LOWER(TRIM(COALESCE(s.state, ''))) <> 'stopped'
		)
		OR EXISTS (
			WITH RECURSIVE descendants(issue_id) AS (
				SELECT d.issue_id
				FROM issue_dependencies d
				WHERE
					d.depends_on_id = NEW.id
					AND d.tombstoned_at IS NULL
					AND d.dependency_type IN ('parent-child', 'parent_child')
				UNION
				SELECT d.issue_id
				FROM descendants
				JOIN issue_dependencies d ON d.depends_on_id = descendants.issue_id
				WHERE
					d.tombstoned_at IS NULL
					AND d.dependency_type IN ('parent-child', 'parent_child')
			)
			SELECT 1
			FROM descendants
			JOIN issues child ON child.id = descendants.issue_id
			WHERE
				child.archived_at IS NULL
				AND (
					child.lifecycle_state <> 'closed'
					OR
					EXISTS (
						SELECT 1
						FROM daemon_worktree_projections w
						WHERE
							w.issue_id = child.id
							AND TRIM(COALESCE(w.path, '')) <> ''
					)
					OR EXISTS (
						SELECT 1
						FROM daemon_session_projections s
						WHERE
							s.issue_id = child.id
							AND LOWER(TRIM(COALESCE(s.state, ''))) <> 'stopped'
					)
				)
		);
END;

CREATE TRIGGER daemon_worktree_closed_issue_guard_insert
BEFORE INSERT ON daemon_worktree_projections
WHEN TRIM(COALESCE(NEW.path, '')) <> ''
BEGIN
	SELECT RAISE(ABORT, 'cannot attach worktree to closed issue or closed ancestor')
	WHERE EXISTS (
		WITH RECURSIVE ancestors(issue_id) AS (
			SELECT NEW.issue_id
			UNION
			SELECT d.depends_on_id
			FROM ancestors
			JOIN issue_dependencies d ON d.issue_id = ancestors.issue_id
			WHERE
				d.tombstoned_at IS NULL
				AND d.dependency_type IN ('parent-child', 'parent_child')
		)
		SELECT 1
		FROM ancestors
		JOIN issues i ON i.id = ancestors.issue_id
		WHERE
			i.lifecycle_state = 'closed'
			AND i.archived_at IS NULL
	);
END;

CREATE TRIGGER daemon_worktree_closed_issue_guard_update
BEFORE UPDATE OF issue_id, path ON daemon_worktree_projections
WHEN TRIM(COALESCE(NEW.path, '')) <> ''
BEGIN
	SELECT RAISE(ABORT, 'cannot attach worktree to closed issue or closed ancestor')
	WHERE EXISTS (
		WITH RECURSIVE ancestors(issue_id) AS (
			SELECT NEW.issue_id
			UNION
			SELECT d.depends_on_id
			FROM ancestors
			JOIN issue_dependencies d ON d.issue_id = ancestors.issue_id
			WHERE
				d.tombstoned_at IS NULL
				AND d.dependency_type IN ('parent-child', 'parent_child')
		)
		SELECT 1
		FROM ancestors
		JOIN issues i ON i.id = ancestors.issue_id
		WHERE
			i.lifecycle_state = 'closed'
			AND i.archived_at IS NULL
	);
END;

CREATE TRIGGER daemon_session_closed_issue_guard_insert
BEFORE INSERT ON daemon_session_projections
WHEN LOWER(TRIM(COALESCE(NEW.state, ''))) <> 'stopped'
BEGIN
	SELECT RAISE(ABORT, 'cannot attach active session to closed issue or closed ancestor')
	WHERE EXISTS (
		WITH RECURSIVE ancestors(issue_id) AS (
			SELECT NEW.issue_id
			UNION
			SELECT d.depends_on_id
			FROM ancestors
			JOIN issue_dependencies d ON d.issue_id = ancestors.issue_id
			WHERE
				d.tombstoned_at IS NULL
				AND d.dependency_type IN ('parent-child', 'parent_child')
		)
		SELECT 1
		FROM ancestors
		JOIN issues i ON i.id = ancestors.issue_id
		WHERE
			i.lifecycle_state = 'closed'
			AND i.archived_at IS NULL
	);
END;

CREATE TRIGGER daemon_session_closed_issue_guard_update
BEFORE UPDATE OF issue_id, state ON daemon_session_projections
WHEN LOWER(TRIM(COALESCE(NEW.state, ''))) <> 'stopped'
BEGIN
	SELECT RAISE(ABORT, 'cannot attach active session to closed issue or closed ancestor')
	WHERE EXISTS (
		WITH RECURSIVE ancestors(issue_id) AS (
			SELECT NEW.issue_id
			UNION
			SELECT d.depends_on_id
			FROM ancestors
			JOIN issue_dependencies d ON d.issue_id = ancestors.issue_id
			WHERE
				d.tombstoned_at IS NULL
				AND d.dependency_type IN ('parent-child', 'parent_child')
		)
		SELECT 1
		FROM ancestors
		JOIN issues i ON i.id = ancestors.issue_id
		WHERE
			i.lifecycle_state = 'closed'
			AND i.archived_at IS NULL
	);
END;

CREATE TRIGGER issue_dependency_closed_runtime_guard_insert
BEFORE INSERT ON issue_dependencies
WHEN
	NEW.tombstoned_at IS NULL
	AND NEW.dependency_type IN ('parent-child', 'parent_child')
BEGIN
	SELECT RAISE(ABORT, 'cannot place unresolved descendant under closed issue')
	WHERE
		EXISTS (
			WITH RECURSIVE ancestors(issue_id) AS (
				SELECT NEW.depends_on_id
				UNION
				SELECT d.depends_on_id
				FROM ancestors
				JOIN issue_dependencies d ON d.issue_id = ancestors.issue_id
				WHERE
					d.tombstoned_at IS NULL
					AND d.dependency_type IN ('parent-child', 'parent_child')
			)
			SELECT 1
			FROM ancestors
			JOIN issues i ON i.id = ancestors.issue_id
			WHERE
				i.lifecycle_state = 'closed'
				AND i.archived_at IS NULL
		)
		AND EXISTS (
			WITH RECURSIVE descendants(issue_id) AS (
				SELECT NEW.issue_id
				UNION
				SELECT d.issue_id
				FROM descendants
				JOIN issue_dependencies d ON d.depends_on_id = descendants.issue_id
				WHERE
					d.tombstoned_at IS NULL
					AND d.dependency_type IN ('parent-child', 'parent_child')
			)
			SELECT 1
			FROM descendants
			LEFT JOIN issues child ON child.id = descendants.issue_id
			WHERE
				(
					child.id IS NOT NULL
					AND child.archived_at IS NULL
					AND child.lifecycle_state <> 'closed'
				)
				OR
				EXISTS (
					SELECT 1
					FROM daemon_worktree_projections w
					WHERE
						w.issue_id = descendants.issue_id
						AND TRIM(COALESCE(w.path, '')) <> ''
				)
				OR EXISTS (
					SELECT 1
					FROM daemon_session_projections s
					WHERE
						s.issue_id = descendants.issue_id
						AND LOWER(TRIM(COALESCE(s.state, ''))) <> 'stopped'
				)
		);
END;

CREATE TRIGGER issue_dependency_closed_runtime_guard_update
BEFORE UPDATE OF issue_id, depends_on_id, dependency_type, tombstoned_at ON issue_dependencies
WHEN
	NEW.tombstoned_at IS NULL
	AND NEW.dependency_type IN ('parent-child', 'parent_child')
BEGIN
	SELECT RAISE(ABORT, 'cannot place unresolved descendant under closed issue')
	WHERE
		EXISTS (
			WITH RECURSIVE ancestors(issue_id) AS (
				SELECT NEW.depends_on_id
				UNION
				SELECT d.depends_on_id
				FROM ancestors
				JOIN issue_dependencies d ON d.issue_id = ancestors.issue_id
				WHERE
					d.tombstoned_at IS NULL
					AND d.dependency_type IN ('parent-child', 'parent_child')
			)
			SELECT 1
			FROM ancestors
			JOIN issues i ON i.id = ancestors.issue_id
			WHERE
				i.lifecycle_state = 'closed'
				AND i.archived_at IS NULL
		)
		AND EXISTS (
			WITH RECURSIVE descendants(issue_id) AS (
				SELECT NEW.issue_id
				UNION
				SELECT d.issue_id
				FROM descendants
				JOIN issue_dependencies d ON d.depends_on_id = descendants.issue_id
				WHERE
					d.tombstoned_at IS NULL
					AND d.dependency_type IN ('parent-child', 'parent_child')
			)
			SELECT 1
			FROM descendants
			LEFT JOIN issues child ON child.id = descendants.issue_id
			WHERE
				(
					child.id IS NOT NULL
					AND child.archived_at IS NULL
					AND child.lifecycle_state <> 'closed'
				)
				OR
				EXISTS (
					SELECT 1
					FROM daemon_worktree_projections w
					WHERE
						w.issue_id = descendants.issue_id
						AND TRIM(COALESCE(w.path, '')) <> ''
				)
				OR EXISTS (
					SELECT 1
					FROM daemon_session_projections s
					WHERE
						s.issue_id = descendants.issue_id
						AND LOWER(TRIM(COALESCE(s.state, ''))) <> 'stopped'
				)
		);
END;

CREATE TRIGGER issue_descendant_closed_ancestor_guard_update
BEFORE UPDATE OF lifecycle_state, archived_at ON issues
WHEN NEW.lifecycle_state <> 'closed' AND NEW.archived_at IS NULL
BEGIN
	SELECT RAISE(ABORT, 'cannot move descendant out of closed under closed ancestor')
	WHERE EXISTS (
		WITH RECURSIVE ancestors(issue_id) AS (
			SELECT d.depends_on_id
			FROM issue_dependencies d
			WHERE
				d.issue_id = NEW.id
				AND d.tombstoned_at IS NULL
				AND d.dependency_type IN ('parent-child', 'parent_child')
			UNION
			SELECT d.depends_on_id
			FROM ancestors
			JOIN issue_dependencies d ON d.issue_id = ancestors.issue_id
			WHERE
				d.tombstoned_at IS NULL
				AND d.dependency_type IN ('parent-child', 'parent_child')
		)
		SELECT 1
		FROM ancestors
		JOIN issues i ON i.id = ancestors.issue_id
		WHERE
			i.lifecycle_state = 'closed'
			AND i.archived_at IS NULL
	);
END;
`

func (c *Client) runIssueStateModelV2CutoverTransaction(ctx context.Context, db *sql.DB, id, backupPath, startedAt string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", id, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	if err := c.issueStateModelV2FailurePoint("after_begin"); err != nil {
		return err
	}

	for _, column := range []struct {
		name string
		ddl  string
	}{
		{name: "lifecycle_state", ddl: "TEXT"},
		{name: "closed_outcome", ddl: "TEXT"},
		{name: "review_state", ddl: "TEXT"},
		{name: "archived_at", ddl: "TEXT"},
	} {
		exists, err := txColumnExists(ctx, tx, "issues", column.name)
		if err != nil {
			return fmt.Errorf("inspect migration %s column %s: %w", id, column.name, err)
		}
		if exists {
			return issueStateModelV2CutoverError("refusing startup after partial issue state-model v2 schema cutover", backupPath, "")
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE issues ADD COLUMN %s %s`, column.name, column.ddl)); err != nil {
			return fmt.Errorf("apply migration %s: add %s: %w", id, column.name, err)
		}
	}

	if err := c.issueStateModelV2FailurePoint("after_columns"); err != nil {
		return err
	}

	if err := normalizeResidualBlockedStatusesForStateModelV2(ctx, tx); err != nil {
		return fmt.Errorf("apply migration %s: normalize residual blocked statuses: %w", id, err)
	}
	if err := mapIssueStateModelV2Rows(ctx, tx); err != nil {
		return fmt.Errorf("apply migration %s: map v1 statuses: %w", id, err)
	}

	if err := c.issueStateModelV2FailurePoint("after_mapping"); err != nil {
		return err
	}
	if err := validateIssueStateModelV2Rows(ctx, tx); err != nil {
		return fmt.Errorf("validate migration %s: %w", id, err)
	}

	completedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeIssueStateModelV2CutoverMarkerTx(ctx, tx, issueStateModelV2CutoverMarker{
		State:       "complete",
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		BackupPath:  backupPath,
	}); err != nil {
		return fmt.Errorf("mark migration %s complete: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO meta (key, value)
		VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, issueStateModelVersionMetaKey, issueStateModelV2Version); err != nil {
		return fmt.Errorf("record migration %s state model version: %w", id, err)
	}
	if err := recordAppliedMigrationAt(ctx, tx, id, completedAt); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}

	if err := c.issueStateModelV2FailurePoint("before_commit"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", id, err)
	}
	tx = nil
	return nil
}

func (c *Client) backupIssueDBForStateModelV2(ctx context.Context, db *sql.DB) (string, error) {
	return c.backupIssueDB(ctx, db, "state-model-v1")
}

func (c *Client) backupIssueDB(ctx context.Context, db *sql.DB, label string) (string, error) {
	if strings.TrimSpace(c.dbPath) == "" || c.dbPath == ":memory:" {
		return "", fmt.Errorf("cannot create SQLite backup for empty or in-memory issue DB path")
	}
	if err := c.issueStateModelV2FailurePoint("before_backup"); err != nil {
		return "", err
	}
	dbPath, err := filepath.Abs(c.dbPath)
	if err != nil {
		return "", fmt.Errorf("resolve DB path: %w", err)
	}
	backupPath := fmt.Sprintf("%s.%s.%s.bak", dbPath, label, time.Now().UTC().Format("20060102T150405.000000000Z"))
	for i := 1; ; i++ {
		if _, err := os.Stat(backupPath); err != nil {
			if os.IsNotExist(err) {
				break
			}
			return "", fmt.Errorf("inspect backup path: %w", err)
		}
		backupPath = fmt.Sprintf("%s.%s.%s.%d.bak", dbPath, label, time.Now().UTC().Format("20060102T150405.000000000Z"), i)
	}
	if _, err := db.ExecContext(ctx, `VACUUM INTO `+quoteSQLiteStringLiteral(backupPath)); err != nil {
		return "", fmt.Errorf("vacuum into %s: %w", backupPath, err)
	}
	if err := c.issueStateModelV2FailurePoint("after_backup"); err != nil {
		return "", err
	}
	return backupPath, nil
}

type issueStateModelV2Reconciliation struct {
	id             string
	legacyStatus   string
	lifecycleState string
	closedOutcome  string
	reviewState    string
	archivedAt     sql.NullString
}

func (c *Client) reconcileIssueStateModelV2Drift(ctx context.Context, db *sql.DB) error {
	applied, err := isMigrationApplied(ctx, db, issueStateModelV2MigrationID)
	if err != nil {
		return fmt.Errorf("check issue state-model v2 migration before reconciliation: %w", err)
	}
	if !applied {
		return nil
	}
	updates, err := issueStateModelV2Reconciliations(ctx, db)
	if err != nil {
		return fmt.Errorf("inspect issue state-model v2 compatibility mirror: %w", err)
	}
	if len(updates) == 0 {
		return validateIssueStateModelV2LegacyMirror(ctx, db)
	}

	backupPath, err := c.backupIssueDB(ctx, db, "state-model-v2-reconcile")
	if err != nil {
		return fmt.Errorf("backup issue DB before state-model v2 reconciliation: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin issue state-model v2 reconciliation: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	updates, err = issueStateModelV2Reconciliations(ctx, tx)
	if err != nil {
		return issueStateModelV2CutoverError("issue state-model v2 reconciliation rolled back", backupPath, err.Error())
	}
	for _, update := range updates {
		if _, err := tx.ExecContext(ctx, `
			UPDATE issues
			SET status = ?,
				lifecycle_state = ?,
				closed_outcome = ?,
				review_state = ?,
				archived_at = ?
			WHERE id = ?
		`, update.legacyStatus, update.lifecycleState, update.closedOutcome, update.reviewState, update.archivedAt, update.id); err != nil {
			return issueStateModelV2CutoverError("issue state-model v2 reconciliation rolled back", backupPath, fmt.Sprintf("issue %s: %v", update.id, err))
		}
	}
	if err := c.issueStateModelV2FailurePoint("after_reconciliation"); err != nil {
		return issueStateModelV2CutoverError("issue state-model v2 reconciliation rolled back", backupPath, err.Error())
	}
	if err := validateIssueStateModelV2Rows(ctx, tx); err != nil {
		return issueStateModelV2CutoverError("issue state-model v2 reconciliation rolled back", backupPath, err.Error())
	}
	if err := validateIssueStateModelV2LegacyMirror(ctx, tx); err != nil {
		return issueStateModelV2CutoverError("issue state-model v2 reconciliation rolled back", backupPath, err.Error())
	}
	if err := tx.Commit(); err != nil {
		return issueStateModelV2CutoverError("commit issue state-model v2 reconciliation", backupPath, err.Error())
	}
	tx = nil
	return nil
}

func issueStateModelV2Reconciliations(ctx context.Context, db sqlIssueQueryer) ([]issueStateModelV2Reconciliation, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, status, priority, lifecycle_state, closed_outcome, review_state, archived_at, deleted_at
		FROM issues
		ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	updates := make([]issueStateModelV2Reconciliation, 0)
	for rows.Next() {
		var (
			id           string
			legacyStatus string
			priority     int
			lifecycleRaw sql.NullString
			outcomeRaw   sql.NullString
			reviewRaw    sql.NullString
			archivedAt   sql.NullString
			deletedAt    sql.NullString
		)
		if err := rows.Scan(&id, &legacyStatus, &priority, &lifecycleRaw, &outcomeRaw, &reviewRaw, &archivedAt, &deletedAt); err != nil {
			return nil, err
		}
		lifecycleState := lifecycleRaw.String
		closedOutcome := outcomeRaw.String
		reviewState := reviewRaw.String
		legacyState, legacyErr := domain.IssueStateFromLegacy(domain.LegacyIssueStateInput{
			Status:   domain.Status(legacyStatus),
			Priority: domain.Priority(priority),
			Archived: nonEmptyNullString(deletedAt),
		})
		var v2State domain.IssueState
		if strings.TrimSpace(lifecycleState) == "" {
			if legacyErr != nil {
				return nil, fmt.Errorf("issue %s legacy state: %w", id, legacyErr)
			}
			v2State = legacyState
		} else {
			var err error
			v2State, err = domain.NewIssueState(domain.IssueStateParts{
				Workflow:     domain.IssueWorkflow(lifecycleState),
				Review:       domain.IssueReviewState(reviewState),
				CloseOutcome: domain.IssueCloseOutcome(closedOutcome),
				Archive:      issueArchiveStateFromTimestamp(archivedAt),
			})
			if err != nil {
				return nil, fmt.Errorf("issue %s v2 state: %w", id, err)
			}
		}
		authoritativeState := v2State
		// Closing is monotonic across the cutover boundary. A legacy terminal
		// write therefore wins over a non-terminal v2 value; every other
		// disagreement is repaired from the v2 authority into the mirror.
		if legacyErr == nil && legacyState.IsClosed() && !v2State.IsClosed() {
			authoritativeState = legacyState
		}
		// Legacy writers only know deleted_at, while v2 writers update both
		// archive columns atomically. Any disagreement therefore means a legacy
		// archive or unarchive write happened after the last aligned state.
		// Preserve the v2 lifecycle authority selected above, but mirror the
		// legacy writer's latest archive intent into archived_at.
		authoritativeState, err = domain.NewIssueState(domain.IssueStateParts{
			Workflow:     authoritativeState.Workflow(),
			Review:       authoritativeState.Review(),
			CloseOutcome: authoritativeState.CloseOutcome(),
			Archive:      issueArchiveStateFromTimestamp(deletedAt),
		})
		if err != nil {
			return nil, fmt.Errorf("issue %s reconciled state: %w", id, err)
		}
		expectedStatus := string(legacyStatusFromIssueState(authoritativeState))
		if legacyStatus == expectedStatus &&
			lifecycleState == string(authoritativeState.Workflow()) &&
			closedOutcome == string(authoritativeState.CloseOutcome()) &&
			reviewState == string(authoritativeState.Review()) &&
			archivedAt == deletedAt {
			continue
		}
		updates = append(updates, issueStateModelV2Reconciliation{
			id:             id,
			legacyStatus:   expectedStatus,
			lifecycleState: string(authoritativeState.Workflow()),
			closedOutcome:  string(authoritativeState.CloseOutcome()),
			reviewState:    string(authoritativeState.Review()),
			archivedAt:     deletedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return updates, nil
}

func validateIssueStateModelV2LegacyMirror(ctx context.Context, db sqlIssueQueryer) error {
	updates, err := issueStateModelV2Reconciliations(ctx, db)
	if err != nil {
		return err
	}
	if len(updates) > 0 {
		return fmt.Errorf("legacy compatibility mirror drift: %d rows", len(updates))
	}
	return nil
}

func validateIssueStateModelV2Rows(ctx context.Context, db sqlIssueQueryer) error {
	checks := []struct {
		name  string
		query string
	}{
		{
			name: "missing lifecycle_state",
			query: `SELECT COUNT(*) FROM issues
				WHERE COALESCE(lifecycle_state, '') = ''`,
		},
		{
			name: "invalid lifecycle_state",
			query: `SELECT COUNT(*) FROM issues
				WHERE lifecycle_state NOT IN ('backlog', 'open', 'active', 'closed')`,
		},
		{
			name: "invalid closed_outcome for closed lifecycle",
			query: `SELECT COUNT(*) FROM issues
				WHERE lifecycle_state = 'closed' AND COALESCE(closed_outcome, '') NOT IN ('completed', 'cancelled')`,
		},
		{
			name: "invalid closed_outcome for non-closed lifecycle",
			query: `SELECT COUNT(*) FROM issues
				WHERE lifecycle_state <> 'closed' AND COALESCE(closed_outcome, '') <> 'none'`,
		},
		{
			name: "invalid closed_outcome",
			query: `SELECT COUNT(*) FROM issues
				WHERE closed_outcome NOT IN ('none', 'completed', 'cancelled')`,
		},
		{
			name: "missing review_state",
			query: `SELECT COUNT(*) FROM issues
				WHERE COALESCE(review_state, '') = ''`,
		},
		{
			name: "invalid review_state",
			query: `SELECT COUNT(*) FROM issues
				WHERE review_state NOT IN ('none', 'requested')`,
		},
		{
			name: "review requested for non-active lifecycle",
			query: `SELECT COUNT(*) FROM issues
				WHERE review_state = 'requested' AND lifecycle_state <> 'active'`,
		},
		{
			name: "archive timestamp drift",
			query: `SELECT COUNT(*) FROM issues
				WHERE COALESCE(archived_at, '') <> COALESCE(deleted_at, '')`,
		},
	}
	for _, check := range checks {
		var count int
		if err := db.QueryRowContext(ctx, check.query).Scan(&count); err != nil {
			return fmt.Errorf("%s: %w", check.name, err)
		}
		if count > 0 {
			return fmt.Errorf("%s: %d rows", check.name, count)
		}
	}
	return nil
}

func normalizeResidualBlockedStatusesForStateModelV2(ctx context.Context, tx *sql.Tx) error {
	script, err := fs.ReadFile(migrationFiles, "migrations/0012_blocked_status_to_open.sql")
	if err != nil {
		return fmt.Errorf("read blocked status migration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, string(script)); err != nil {
		return fmt.Errorf("run blocked status migration: %w", err)
	}
	return nil
}

func mapIssueStateModelV2Rows(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, status, priority, deleted_at
		FROM issues
		ORDER BY id
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type update struct {
		id             string
		lifecycleState string
		closedOutcome  string
		reviewState    string
		archivedAt     sql.NullString
	}
	updates := []update{}
	for rows.Next() {
		var (
			id        string
			status    string
			priority  int
			deletedAt sql.NullString
		)
		if err := rows.Scan(&id, &status, &priority, &deletedAt); err != nil {
			return err
		}
		state, err := domain.IssueStateFromLegacy(domain.LegacyIssueStateInput{
			Status:   domain.Status(status),
			Priority: domain.Priority(priority),
			Archived: deletedAt.Valid && strings.TrimSpace(deletedAt.String) != "",
		})
		if err != nil {
			return fmt.Errorf("issue %s: %w", id, err)
		}
		updates = append(updates, update{
			id:             id,
			lifecycleState: string(state.Workflow()),
			closedOutcome:  string(state.CloseOutcome()),
			reviewState:    string(state.Review()),
			archivedAt:     deletedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, update := range updates {
		if _, err := tx.ExecContext(ctx, `
			UPDATE issues
			SET lifecycle_state = ?,
				closed_outcome = ?,
				review_state = ?,
				archived_at = ?
			WHERE id = ?
		`, update.lifecycleState, update.closedOutcome, update.reviewState, update.archivedAt, update.id); err != nil {
			return fmt.Errorf("issue %s: %w", update.id, err)
		}
	}
	return nil
}

func readIssueStateModelV2CutoverMarker(ctx context.Context, db *sql.DB) (issueStateModelV2CutoverMarker, bool, error) {
	metaExists, err := tableExists(db, "meta")
	if err != nil {
		return issueStateModelV2CutoverMarker{}, false, err
	}
	if !metaExists {
		return issueStateModelV2CutoverMarker{}, false, nil
	}
	var raw string
	err = db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, issueStateModelV2CutoverMarkerKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return issueStateModelV2CutoverMarker{}, false, nil
	}
	if err != nil {
		return issueStateModelV2CutoverMarker{}, false, err
	}
	var marker issueStateModelV2CutoverMarker
	if err := json.Unmarshal([]byte(raw), &marker); err != nil {
		return issueStateModelV2CutoverMarker{}, true, fmt.Errorf("decode marker: %w", err)
	}
	return marker, true, nil
}

func writeIssueStateModelV2CutoverMarker(ctx context.Context, db *sql.DB, marker issueStateModelV2CutoverMarker) error {
	payload, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO meta (key, value)
		VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, issueStateModelV2CutoverMarkerKey, string(payload))
	return err
}

func writeIssueStateModelV2CutoverMarkerTx(ctx context.Context, tx *sql.Tx, marker issueStateModelV2CutoverMarker) error {
	payload, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO meta (key, value)
		VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, issueStateModelV2CutoverMarkerKey, string(payload))
	return err
}

func setIssueStateModelV2CompleteMarker(ctx context.Context, db *sql.DB, backupPath string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeIssueStateModelV2CutoverMarker(ctx, db, issueStateModelV2CutoverMarker{
		State:       "complete",
		StartedAt:   now,
		CompletedAt: now,
		BackupPath:  backupPath,
	}); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO meta (key, value)
		VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, issueStateModelVersionMetaKey, issueStateModelV2Version)
	return err
}

func issueStateModelV2CutoverError(message, backupPath, cause string) error {
	detail := strings.TrimSpace(message)
	if strings.TrimSpace(backupPath) != "" {
		detail += fmt.Sprintf("; backup=%s", backupPath)
	}
	if strings.TrimSpace(cause) != "" {
		detail += fmt.Sprintf("; restore the backup before retrying; cause=%s", cause)
	} else if strings.TrimSpace(backupPath) != "" {
		detail += "; restore the backup before retrying"
	}
	return fmt.Errorf("%s", detail)
}

func (c *Client) issueStateModelV2FailurePoint(stage string) error {
	if c.stateModelV2MigrationFailureHook == nil {
		return nil
	}
	if err := c.stateModelV2MigrationFailureHook(stage); err != nil {
		return fmt.Errorf("injected issue state-model v2 migration failure at %s: %w", stage, err)
	}
	return nil
}

func quoteSQLiteStringLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func applyDecisionSearchFTSMigration(ctx context.Context, db *sql.DB, id string) error {
	var c Client
	if err := c.ensureDecisionSchema(db); err != nil {
		return fmt.Errorf("repair decision schema before migration %s: %w", id, err)
	}
	sqlText, err := loadMigrationSQL("migrations/0026_decision_search_fts.sql")
	if err != nil {
		return fmt.Errorf("load migration %s: %w", id, err)
	}
	return c.applyMigration(ctx, db, id, sqlText)
}

func applyAgentLearningPrivacyMigration(ctx context.Context, db *sql.DB, id string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", id, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	hasEvidencePrivate, err := txColumnExists(ctx, tx, "agent_learnings", "evidence_private")
	if err != nil {
		return fmt.Errorf("inspect migration %s: %w", id, err)
	}
	if !hasEvidencePrivate {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE agent_learnings ADD COLUMN evidence_private INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("apply migration %s: add evidence_private: %w", id, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_agent_learnings_active_privacy
			ON agent_learnings(project_id, status, evidence_private, updated_at DESC, local_id)
			WHERE deleted_at IS NULL
	`); err != nil {
		return fmt.Errorf("apply migration %s: create privacy index: %w", id, err)
	}

	if err := recordAppliedMigration(ctx, tx, id); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", id, err)
	}
	tx = nil
	return nil
}

func applyIssueOwnershipMigration(ctx context.Context, db *sql.DB, id string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", id, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	for _, column := range []struct {
		name string
		ddl  string
	}{
		{name: "owner_id", ddl: "TEXT"},
		{name: "owner_kind", ddl: "TEXT"},
		{name: "owner_claimed_at", ddl: "TEXT"},
		{name: "owner_expires_at", ddl: "TEXT"},
	} {
		exists, err := txColumnExists(ctx, tx, "issues", column.name)
		if err != nil {
			return fmt.Errorf("inspect migration %s column %s: %w", id, column.name, err)
		}
		if exists {
			continue
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE issues ADD COLUMN %s %s`, column.name, column.ddl)); err != nil {
			return fmt.Errorf("apply migration %s: add %s: %w", id, column.name, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_issues_owner_active
			ON issues (owner_id, owner_expires_at)
			WHERE deleted_at IS NULL AND owner_id IS NOT NULL
	`); err != nil {
		return fmt.Errorf("apply migration %s: create ownership index: %w", id, err)
	}

	if err := recordAppliedMigration(ctx, tx, id); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", id, err)
	}
	tx = nil
	return nil
}

func repairAgentLearningBaseSchema(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	tableExists, err := txTableExists(ctx, tx, "agent_learnings")
	if err != nil {
		return fmt.Errorf("inspect table: %w", err)
	}
	if !tableExists {
		if err := tx.Commit(); err != nil {
			return err
		}
		tx = nil
		return nil
	}

	for _, column := range []struct {
		name string
		ddl  string
	}{
		{name: "evidence_private", ddl: "INTEGER NOT NULL DEFAULT 0"},
		{name: "promotion_target", ddl: "TEXT"},
		{name: "promotion_target_id", ddl: "TEXT"},
		{name: "promotion_note", ddl: "TEXT"},
		{name: "promoted_at", ddl: "TEXT"},
		{name: "review_note", ddl: "TEXT"},
		{name: "reviewed_at", ddl: "TEXT"},
		{name: "expires_at", ddl: "TEXT"},
		{name: "stale_at", ddl: "TEXT"},
		{name: "last_recalled_at", ddl: "TEXT"},
		{name: "recall_count", ddl: "INTEGER NOT NULL DEFAULT 0"},
		{name: "superseded_at", ddl: "TEXT"},
		{name: "target_retired_at", ddl: "TEXT"},
		{name: "target_state", ddl: "TEXT"},
		{name: "target_hash", ddl: "TEXT"},
		{name: "target_metadata_json", ddl: "TEXT NOT NULL DEFAULT '{}'"},
		{name: "target_drifted_at", ddl: "TEXT"},
	} {
		exists, err := txColumnExists(ctx, tx, "agent_learnings", column.name)
		if err != nil {
			return fmt.Errorf("inspect column %s: %w", column.name, err)
		}
		if exists {
			continue
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE agent_learnings ADD COLUMN %s %s`, column.name, column.ddl)); err != nil {
			return fmt.Errorf("add column %s: %w", column.name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	tx = nil
	return nil
}

func txTableExists(ctx context.Context, tx *sql.Tx, tableName string) (bool, error) {
	var exists bool
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM sqlite_master
			WHERE type = 'table' AND name = ?
		)
	`, tableName).Scan(&exists)
	return exists, err
}

func txColumnExists(ctx context.Context, tx *sql.Tx, tableName, columnName string) (bool, error) {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info('%s')", tableName))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal any
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &primaryKey); err != nil {
			return false, err
		}
		if strings.EqualFold(name, columnName) {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, nil
}

func dependencyCount(tx *sql.Tx) (int, error) {
	exists, err := tableExists(tx, "issue_dependencies")
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM issue_dependencies`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func tableExists(queryer interface {
	QueryRow(string, ...any) *sql.Row
}, tableName string) (bool, error) {
	var exists bool
	err := queryer.QueryRow(`
		SELECT EXISTS(
			SELECT 1
			FROM sqlite_master
			WHERE type = 'table' AND name = ?
		)
	`, tableName).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

func shouldApplyDependencyFKMigration(ctx context.Context, db *sql.DB) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_list('issue_dependencies')`)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	hasIssueFK := false
	hasDependsOnFK := false
	for rows.Next() {
		var (
			id       int
			seq      int
			table    string
			from     string
			to       string
			onUpdate string
			onDelete string
			match    string
		)
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return false, err
		}
		if table != "issues" || to != "id" {
			continue
		}
		if from == "issue_id" {
			hasIssueFK = true
		}
		if from == "depends_on_id" {
			hasDependsOnFK = true
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}

	return !(hasIssueFK && hasDependsOnFK), nil
}

type sqliteColumnSpec struct {
	name string
	ddl  string
}

func (c *Client) ensureSpecSchema(db *sql.DB) error {
	if err := migrateLegacySpecRequirementsSchema(db); err != nil {
		return fmt.Errorf("normalize legacy spec schema: %w", err)
	}

	requirementsDDL := `
		CREATE TABLE IF NOT EXISTS spec_requirements (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			local_id TEXT NOT NULL,
			external_code TEXT,
			title TEXT NOT NULL,
			description TEXT,
			issue_id TEXT,
			status TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT,
			FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE SET NULL
		)
	`
	if _, err := db.Exec(requirementsDDL); err != nil {
		return fmt.Errorf("ensure spec_requirements table: %w", err)
	}
	if err := ensureTableColumns(db, "spec_requirements", []sqliteColumnSpec{
		{name: "local_id", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "external_code", ddl: "TEXT"},
		{name: "title", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "description", ddl: "TEXT"},
		{name: "issue_id", ddl: "TEXT"},
		{name: "status", ddl: "TEXT NOT NULL DEFAULT 'open'"},
		{name: "created_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "updated_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "deleted_at", ddl: "TEXT"},
	}); err != nil {
		return fmt.Errorf("ensure spec_requirements columns: %w", err)
	}

	linksDDL := `
		CREATE TABLE IF NOT EXISTS spec_links (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id TEXT NOT NULL,
			requirement_id INTEGER NOT NULL,
			role TEXT NOT NULL,
			note TEXT,
			implementations_json TEXT,
			fulfillment_status TEXT,
			fulfilled_at TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT,
			FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE,
			FOREIGN KEY (requirement_id) REFERENCES spec_requirements(id) ON DELETE CASCADE
		)
	`
	if _, err := db.Exec(linksDDL); err != nil {
		return fmt.Errorf("ensure spec_links table: %w", err)
	}
	if err := ensureTableColumns(db, "spec_links", []sqliteColumnSpec{
		{name: "issue_id", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "requirement_id", ddl: "INTEGER NOT NULL DEFAULT 0"},
		{name: "role", ddl: "TEXT NOT NULL DEFAULT 'implements'"},
		{name: "note", ddl: "TEXT"},
		{name: "implementations_json", ddl: "TEXT"},
		{name: "fulfillment_status", ddl: "TEXT"},
		{name: "fulfilled_at", ddl: "TEXT"},
		{name: "created_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "updated_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "deleted_at", ddl: "TEXT"},
	}); err != nil {
		return fmt.Errorf("ensure spec_links columns: %w", err)
	}

	indexes := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_spec_requirements_active_local_id ON spec_requirements(local_id) WHERE deleted_at IS NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_spec_requirements_active_external_code ON spec_requirements(external_code) WHERE deleted_at IS NULL AND external_code IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_spec_requirements_issue_status_updated ON spec_requirements(issue_id, status, updated_at DESC) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_spec_requirements_updated ON spec_requirements(updated_at DESC, local_id) WHERE deleted_at IS NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_spec_links_active_issue_requirement ON spec_links(issue_id, requirement_id) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_spec_links_issue_role_updated ON spec_links(issue_id, role, updated_at DESC) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_spec_links_requirement_role_updated ON spec_links(requirement_id, role, updated_at DESC) WHERE deleted_at IS NULL`,
	}
	for _, stmt := range indexes {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("ensure spec schema index: %w", err)
		}
	}

	return nil
}

type specRequirementsLegacyProfile struct {
	hasBodyMD      bool
	hasKind        bool
	hasPriority    bool
	textPrimaryKey bool
}

func migrateLegacySpecRequirementsSchema(db *sql.DB) error {
	cols, err := tableColumnDetails(db, "spec_requirements")
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		return nil
	}

	profile := specRequirementsLegacyProfile{}
	for _, col := range cols {
		switch col.name {
		case "body_md":
			profile.hasBodyMD = true
		case "kind":
			profile.hasKind = true
		case "priority":
			profile.hasPriority = true
		case "id":
			typeName := strings.ToUpper(strings.TrimSpace(col.typ))
			profile.textPrimaryKey = col.primaryKey > 0 && !strings.Contains(typeName, "INT")
		}
	}
	hasColumn := func(name string) bool {
		for _, col := range cols {
			if col.name == name {
				return true
			}
		}
		return false
	}

	if !profile.hasBodyMD && !profile.hasKind && !profile.hasPriority && !profile.textPrimaryKey {
		return nil
	}

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`
		ALTER TABLE spec_requirements RENAME TO spec_requirements_legacy
	`); err != nil {
		return err
	}

	if _, err := tx.Exec(`
		CREATE TABLE spec_requirements (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			local_id TEXT NOT NULL,
			external_code TEXT,
			title TEXT NOT NULL,
			description TEXT,
			issue_id TEXT,
			status TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT,
			FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE SET NULL
		)
	`); err != nil {
		return err
	}

	localIDExpr := `CAST(id AS TEXT)`
	localIDExprLegacy := `CAST(legacy.id AS TEXT)`
	if hasColumn("local_id") {
		localIDExpr = `COALESCE(NULLIF(TRIM(local_id), ''), CAST(id AS TEXT))`
		localIDExprLegacy = `COALESCE(NULLIF(TRIM(legacy.local_id), ''), CAST(legacy.id AS TEXT))`
	}
	externalCodeExpr := `NULL`
	if hasColumn("external_code") {
		externalCodeExpr = `NULLIF(TRIM(external_code), '')`
	}
	descriptionExpr := `''`
	switch {
	case hasColumn("description") && profile.hasBodyMD:
		descriptionExpr = `COALESCE(NULLIF(description, ''), body_md, '')`
	case hasColumn("description"):
		descriptionExpr = `COALESCE(description, '')`
	case profile.hasBodyMD:
		descriptionExpr = `COALESCE(body_md, '')`
	}
	issueIDExpr := `NULL`
	if hasColumn("issue_id") {
		issueIDExpr = `NULLIF(TRIM(issue_id), '')`
	}
	statusExpr := `'open'`
	if hasColumn("status") {
		statusExpr = `CASE WHEN status IN ('open', 'accepted', 'superseded') THEN status ELSE 'open' END`
	}
	createdAtExpr := `'1970-01-01T00:00:00Z'`
	if hasColumn("created_at") {
		createdAtExpr = `created_at`
	}
	updatedAtExpr := `'1970-01-01T00:00:00Z'`
	if hasColumn("updated_at") {
		updatedAtExpr = `updated_at`
	}
	deletedAtExpr := `NULL`
	if hasColumn("deleted_at") {
		deletedAtExpr = `deleted_at`
	}

	copyRequirementsSQL := fmt.Sprintf(`
		INSERT INTO spec_requirements (
			local_id,
			external_code,
			title,
			description,
			issue_id,
			status,
			created_at,
			updated_at,
			deleted_at
		)
		SELECT
			%s,
			%s,
			title,
			%s,
			%s,
			%s,
			%s,
			%s,
			%s
		FROM spec_requirements_legacy
	`, localIDExpr, externalCodeExpr, descriptionExpr, issueIDExpr, statusExpr, createdAtExpr, updatedAtExpr, deletedAtExpr)
	if _, err := tx.Exec(copyRequirementsSQL); err != nil {
		return err
	}

	if tableExistsInTx(tx, "spec_links") {
		if _, err := tx.Exec(`ALTER TABLE spec_links RENAME TO spec_links_legacy`); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			CREATE TABLE spec_links (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				issue_id TEXT NOT NULL,
				requirement_id INTEGER NOT NULL,
				role TEXT NOT NULL,
				note TEXT,
				implementations_json TEXT,
				fulfillment_status TEXT,
				fulfilled_at TEXT,
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL,
				deleted_at TEXT,
				FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE,
				FOREIGN KEY (requirement_id) REFERENCES spec_requirements(id) ON DELETE CASCADE
			)
		`); err != nil {
			return err
		}

		if _, err := tx.Exec(`
			CREATE TEMP TABLE spec_requirement_id_map (
				old_key TEXT PRIMARY KEY,
				new_id INTEGER NOT NULL
			)
		`); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO spec_requirement_id_map(old_key, new_id)
			SELECT CAST(legacy.id AS TEXT), current.id
			FROM spec_requirements_legacy legacy
			JOIN spec_requirements current
			  ON current.local_id = ` + localIDExprLegacy + `
		`); err != nil {
			return err
		}
		if hasColumn("local_id") {
			if _, err := tx.Exec(`
				INSERT OR IGNORE INTO spec_requirement_id_map(old_key, new_id)
				SELECT legacy.local_id, current.id
				FROM spec_requirements_legacy legacy
				JOIN spec_requirements current
				  ON current.local_id = ` + localIDExprLegacy + `
				WHERE legacy.local_id IS NOT NULL AND TRIM(legacy.local_id) != ''
			`); err != nil {
				return err
			}
		}
		if hasColumn("external_code") {
			if _, err := tx.Exec(`
			INSERT OR IGNORE INTO spec_requirement_id_map(old_key, new_id)
			SELECT legacy.external_code, current.id
			FROM spec_requirements_legacy legacy
			JOIN spec_requirements current
			  ON current.local_id = ` + localIDExprLegacy + `
			WHERE legacy.external_code IS NOT NULL AND TRIM(legacy.external_code) != ''
		`); err != nil {
				return err
			}
		}

		if _, err := tx.Exec(`
			INSERT INTO spec_links (
				issue_id,
				requirement_id,
				role,
				note,
				implementations_json,
				fulfillment_status,
				fulfilled_at,
				created_at,
				updated_at,
				deleted_at
			)
			SELECT
				l.issue_id,
				m.new_id,
				l.role,
				l.note,
				l.implementations_json,
				l.fulfillment_status,
				l.fulfilled_at,
				l.created_at,
				l.updated_at,
				l.deleted_at
			FROM spec_links_legacy l
			JOIN spec_requirement_id_map m
			  ON m.old_key = CAST(l.requirement_id AS TEXT)
		`); err != nil {
			return err
		}

		if _, err := tx.Exec(`DROP TABLE spec_links_legacy`); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(`DROP TABLE spec_requirements_legacy`); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func tableExistsInTx(tx *sql.Tx, tableName string) bool {
	var exists int
	if err := tx.QueryRow(`
		SELECT COUNT(*)
		FROM sqlite_master
		WHERE type = 'table' AND name = ?
	`, tableName).Scan(&exists); err != nil {
		return false
	}
	return exists > 0
}

func (c *Client) ensureDecisionSchema(db *sql.DB) error {
	decisionsDDL := `
		CREATE TABLE IF NOT EXISTS decisions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			local_id TEXT NOT NULL,
			title TEXT NOT NULL,
			rationale TEXT,
			context TEXT,
			consequences TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT
		)
	`
	if _, err := db.Exec(decisionsDDL); err != nil {
		return fmt.Errorf("ensure decisions table: %w", err)
	}
	if err := ensureTableColumns(db, "decisions", []sqliteColumnSpec{
		{name: "local_id", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "title", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "rationale", ddl: "TEXT"},
		{name: "context", ddl: "TEXT"},
		{name: "consequences", ddl: "TEXT"},
		{name: "created_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "updated_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "deleted_at", ddl: "TEXT"},
	}); err != nil {
		return fmt.Errorf("ensure decisions columns: %w", err)
	}

	linksDDL := `
		CREATE TABLE IF NOT EXISTS decision_links (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			decision_id INTEGER NOT NULL,
			target_kind TEXT NOT NULL,
			target_id TEXT NOT NULL,
			relation TEXT NOT NULL,
			note TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT,
			FOREIGN KEY (decision_id) REFERENCES decisions(id) ON DELETE CASCADE
		)
	`
	if _, err := db.Exec(linksDDL); err != nil {
		return fmt.Errorf("ensure decision_links table: %w", err)
	}
	if err := ensureTableColumns(db, "decision_links", []sqliteColumnSpec{
		{name: "decision_id", ddl: "INTEGER NOT NULL DEFAULT 0"},
		{name: "target_kind", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "target_id", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "relation", ddl: "TEXT NOT NULL DEFAULT 'relates'"},
		{name: "note", ddl: "TEXT"},
		{name: "created_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "updated_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "deleted_at", ddl: "TEXT"},
	}); err != nil {
		return fmt.Errorf("ensure decision_links columns: %w", err)
	}

	for _, stmt := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_decisions_active_local_id ON decisions(local_id) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_decisions_updated ON decisions(updated_at DESC, local_id) WHERE deleted_at IS NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_decision_links_active_unique ON decision_links(decision_id, target_kind, target_id) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_decision_links_target ON decision_links(target_kind, target_id, updated_at DESC) WHERE deleted_at IS NULL`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("ensure decision schema index: %w", err)
		}
	}
	return nil
}

func (c *Client) ensureDecisionAuditSchema(db *sql.DB) error {
	auditDDL := `
		CREATE TABLE IF NOT EXISTS decision_audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			entity_type TEXT NOT NULL,
			entity_id TEXT NOT NULL,
			operation TEXT NOT NULL,
			actor_source TEXT NOT NULL,
			before_json TEXT NOT NULL,
			after_json TEXT NOT NULL,
			created_at TEXT NOT NULL
		)
	`
	if _, err := db.Exec(auditDDL); err != nil {
		return fmt.Errorf("ensure decision_audit_log table: %w", err)
	}
	if err := ensureTableColumns(db, "decision_audit_log", []sqliteColumnSpec{
		{name: "entity_type", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "entity_id", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "operation", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "actor_source", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "before_json", ddl: "TEXT NOT NULL DEFAULT 'null'"},
		{name: "after_json", ddl: "TEXT NOT NULL DEFAULT 'null'"},
		{name: "created_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
	}); err != nil {
		return fmt.Errorf("ensure decision_audit_log columns: %w", err)
	}
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_decision_audit_entity_created_at ON decision_audit_log(entity_type, entity_id, created_at, id)`,
		`CREATE INDEX IF NOT EXISTS idx_decision_audit_created_at ON decision_audit_log(created_at, id)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("ensure decision audit index: %w", err)
		}
	}
	return nil
}

func (c *Client) ensureSpecAuditSchema(db *sql.DB) error {
	auditDDL := `
		CREATE TABLE IF NOT EXISTS spec_audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			entity_type TEXT NOT NULL,
			entity_id TEXT NOT NULL,
			operation TEXT NOT NULL,
			actor_source TEXT NOT NULL,
			before_json TEXT NOT NULL,
			after_json TEXT NOT NULL,
			created_at TEXT NOT NULL
		)
	`
	if _, err := db.Exec(auditDDL); err != nil {
		return fmt.Errorf("ensure spec_audit_log table: %w", err)
	}
	if err := ensureTableColumns(db, "spec_audit_log", []sqliteColumnSpec{
		{name: "entity_type", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "entity_id", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "operation", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "actor_source", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "before_json", ddl: "TEXT NOT NULL DEFAULT 'null'"},
		{name: "after_json", ddl: "TEXT NOT NULL DEFAULT 'null'"},
		{name: "created_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
	}); err != nil {
		return fmt.Errorf("ensure spec_audit_log columns: %w", err)
	}

	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_spec_audit_entity_created_at ON spec_audit_log(entity_type, entity_id, created_at, id)`,
		`CREATE INDEX IF NOT EXISTS idx_spec_audit_created_at ON spec_audit_log(created_at, id)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("ensure spec audit index: %w", err)
		}
	}

	return nil
}

func ensureTableColumns(db *sql.DB, tableName string, columns []sqliteColumnSpec) error {
	existing, err := tableColumns(db, tableName)
	if err != nil {
		return err
	}

	for _, column := range columns {
		if _, ok := existing[column.name]; ok {
			continue
		}
		stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", tableName, column.name, column.ddl)
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("add column %s.%s: %w", tableName, column.name, err)
		}
	}

	return nil
}

func tableColumns(db *sql.DB, tableName string) (map[string]struct{}, error) {
	details, err := tableColumnDetails(db, tableName)
	if err != nil {
		return nil, err
	}
	columns := make(map[string]struct{})
	for _, detail := range details {
		columns[detail.name] = struct{}{}
	}
	return columns, nil
}

type tableColumnDetail struct {
	name       string
	typ        string
	primaryKey int
}

func tableColumnDetails(db *sql.DB, tableName string) ([]tableColumnDetail, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info('%s')", tableName))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	details := make([]tableColumnDetail, 0, 16)
	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal any
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &primaryKey); err != nil {
			return nil, err
		}
		details = append(details, tableColumnDetail{
			name:       name,
			typ:        columnType,
			primaryKey: primaryKey,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return details, nil
}
