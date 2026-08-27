package postgres

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/zx/federated-iot-platform/backend/internal/domain"
)

func TestSimulationAdmissionReadsImmutableParentsWithoutUpdateLocks(t *testing.T) {
	contents, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatal(err)
	}
	repository := string(contents)
	start := strings.Index(repository, "func (r *Repository) CreateSimulation(")
	end := strings.Index(repository, "func (r *Repository) CancelSimulation(")
	if start < 0 || end < start {
		t.Fatal("simulation admission function boundaries are missing")
	}
	admission := repository[start:end]
	for _, query := range []string{
		"FROM datasets WHERE dataset_id=$1\"",
		"FROM parameter_profiles WHERE version_id=$1\"",
		"FROM load_mapping_profiles WHERE version_id=$1\"",
	} {
		if !strings.Contains(admission, query) {
			t.Fatalf("admission parent read is missing: %s", query)
		}
	}
	if strings.Contains(admission, "FOR KEY SHARE") {
		t.Fatal("admission must not require UPDATE on immutable parent rows")
	}
	for _, required := range []string{
		"SELECT request_sha256, run_id FROM idempotency_keys WHERE idempotency_key=$1 FOR UPDATE",
		"if err := lockScheduler(ctx, tx); err != nil",
		"pgx.TxOptions{IsoLevel: pgx.Serializable}",
		"INSERT INTO simulation_snapshots(run_id, snapshot_json, snapshot_sha256)",
		"record, err := simulationByID(ctx, tx, runID)",
		"return record, false, nil",
	} {
		if !strings.Contains(admission, required) {
			t.Fatalf("admission lost its required atomic guard: %s", required)
		}
	}
	readback := strings.LastIndex(admission, "record, err := simulationByID(ctx, tx, runID)")
	commit := strings.LastIndex(admission, "if err := tx.Commit(ctx); err != nil")
	if readback < 0 || commit < 0 || readback > commit {
		t.Fatal("created simulation response must read its persisted record before commit")
	}
}

func TestCustomAliasACLIsColumnScopedAndRoleFixtureIsNarrow(t *testing.T) {
	contents, err := os.ReadFile("migrations/000004_custom_profile_alias_acl.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration := string(contents)
	if !strings.Contains(migration, "GRANT UPDATE (display_name) ON TABLE parameter_profiles TO web_api;") ||
		strings.Contains(migration, "GRANT UPDATE ON TABLE parameter_profiles TO web_api;") {
		t.Fatal("CUSTOM alias migration must grant only the display_name column")
	}

	fixture, err := os.ReadFile("../../../fixtures/postgres/web-api-admission-acl.v1.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"fixture must run as web_api",
		"has_table_privilege(current_user, 'public.datasets', 'UPDATE')",
		"has_column_privilege(current_user, 'public.parameter_profiles', 'display_name', 'UPDATE')",
		"SELECT dataset_id FROM datasets WHERE FALSE;",
		"SELECT version_id FROM parameter_profiles WHERE FALSE;",
		"SELECT version_id FROM load_mapping_profiles WHERE FALSE;",
	} {
		if !strings.Contains(string(fixture), required) {
			t.Fatalf("admission ACL fixture is missing %q", required)
		}
	}
}

func TestSimulationAdmissionRequiresExactlyThreeAgentPlaceholders(t *testing.T) {
	valid := domain.CreateSimulationRequest{
		DatasetID: "ds_valid", RunMode: domain.RunModeReference, ParameterProfileVersionID: referenceProfileVersionID,
		LoadMappingVersionID: "identity-v1", Seed: 2026,
		AgentOverrides: []domain.AgentOverride{{Agent: 1, Parameters: map[string]any{}}, {Agent: 2, Parameters: map[string]any{}}, {Agent: 3, Parameters: map[string]any{}}},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("frozen three-Agent admission request was rejected: %v", err)
	}
	invalid := valid
	invalid.AgentOverrides = []domain.AgentOverride{}
	err := toStableError(invalid.Validate())
	var stable StableError
	if !errors.As(err, &stable) || stable.Code != "REQUEST_INVALID" {
		t.Fatalf("empty Agent set error = %#v, want stable REQUEST_INVALID", err)
	}
}

func TestCancelSimulationUsesTypedReasonEventsAndRetainsAtomicStateMachine(t *testing.T) {
	contents, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatal(err)
	}
	repository := string(contents)
	start := strings.Index(repository, "func (r *Repository) CancelSimulation(")
	end := strings.Index(repository, "func (r *Repository) GetSimulation(")
	if start < 0 || end < start {
		t.Fatal("cancellation function boundaries are missing")
	}
	cancellation := repository[start:end]
	for _, required := range []string{
		"tx, err := r.pool.BeginTx(ctx, cancellationTxOptions())",
		"if err := lockScheduler(ctx, tx); err != nil",
		"record, err := simulationByIDForUpdate(ctx, tx, runID)",
		"case domain.SimulationQueued:",
		"UPDATE simulations SET status='CANCELLED', finished_at=now(), artifact_state='INCOMPLETE' WHERE run_id=$1",
		"UPDATE worker_jobs SET status='CANCELLED' WHERE run_id=$1 AND status='QUEUED'",
		"jsonb_build_object('previous_status','QUEUED','status','CANCELLED','reason',$2::text)",
		"case domain.SimulationRunning:",
		"UPDATE simulations SET status='CANCELLING', cancel_requested_at=now(), cancel_reason=$2 WHERE run_id=$1",
		"UPDATE worker_jobs SET status='CANCELLING' WHERE run_id=$1 AND status='RUNNING'",
		"jsonb_build_object('previous_status','RUNNING','status','CANCELLING','reason',$2::text)",
		"case domain.SimulationCancelling, domain.SimulationCancelled:",
		"Code: \"RUN_NOT_CANCELLABLE\"",
		"if err := tx.Commit(ctx); err != nil",
	} {
		if !strings.Contains(cancellation, required) {
			t.Fatalf("cancellation transaction is missing %q", required)
		}
	}
	if strings.Contains(cancellation, "'reason',$2))") {
		t.Fatal("cancellation event reason must be explicitly typed for jsonb_build_object")
	}
	for _, forbidden := range []string{
		"INSERT INTO simulations", "INSERT INTO simulation_snapshots", "INSERT INTO worker_jobs", "lease_token =", "lease_expires_at =",
	} {
		if strings.Contains(cancellation, forbidden) {
			t.Fatalf("cancellation must not allocate a new run, snapshot, job, or lease: %q", forbidden)
		}
	}
	queuedUpdate := strings.Index(cancellation, "UPDATE simulations SET status='CANCELLED'")
	queuedJob := strings.Index(cancellation, "UPDATE worker_jobs SET status='CANCELLED'")
	queuedEvent := strings.Index(cancellation, "'previous_status','QUEUED','status','CANCELLED'")
	queuePositions := strings.Index(cancellation, "SELECT emit_queue_position_events()")
	commit := strings.LastIndex(cancellation, "if err := tx.Commit(ctx); err != nil")
	if queuedUpdate < 0 || queuedJob < queuedUpdate || queuedEvent < queuedJob || queuePositions < queuedEvent || commit < queuePositions {
		t.Fatal("queued cancellation must update the run, cancel its job, persist its event, recalculate positions, and commit atomically")
	}
}
