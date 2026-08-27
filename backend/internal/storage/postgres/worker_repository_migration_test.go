package postgres

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestWorkerRepositoryMigrationHardensEveryWorkerFunction(t *testing.T) {
	contents, err := os.ReadFile("migrations/000002_worker_repository_observability.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	legacyFunctions := []string{
		"worker_claim_next_job(TEXT, TEXT, INTEGER)",
		"worker_heartbeat(TEXT, TEXT, TEXT, INTEGER)",
		"worker_report_stage(TEXT, TEXT, TEXT, TEXT, SMALLINT, JSONB)",
		"worker_complete_preflight(TEXT, TEXT, TEXT, CHAR(64), JSONB, CHAR(64))",
		"worker_confirm_cancel(TEXT, TEXT, TEXT)",
		"worker_fail_job(TEXT, TEXT, TEXT, JSONB, BOOLEAN)",
		"worker_commit_simulation(TEXT, TEXT, TEXT, CHAR(64), JSONB, JSONB, JSONB)",
	}
	newFunctions := []string{
		"worker_recover_expired_leases()",
		"worker_register_instance(TEXT, TEXT, TEXT)",
		"worker_heartbeat_instance(TEXT, TEXT)",
		"worker_claim_next_job_for_worker(TEXT, TEXT, TEXT, INTEGER)",
		"worker_heartbeat_for_worker(TEXT, TEXT, TEXT, TEXT, INTEGER)",
		"worker_cancellation_context(TEXT, TEXT, TEXT)",
		"worker_report_event(TEXT, TEXT, TEXT, JSONB)",
	}
	for _, function := range legacyFunctions {
		if !strings.Contains(sql, "ALTER FUNCTION "+function+" SECURITY DEFINER;") {
			t.Fatalf("legacy Worker function is not SECURITY DEFINER: %s", function)
		}
		if !strings.Contains(sql, "ALTER FUNCTION "+function+" SET search_path = pg_catalog, public;") {
			t.Fatalf("legacy Worker function has no fixed search path: %s", function)
		}
		if !strings.Contains(sql, "ALTER FUNCTION "+function+" OWNER TO platform_worker_repository_owner;") {
			t.Fatalf("legacy Worker function has no dedicated owner: %s", function)
		}
		if !strings.Contains(sql, "REVOKE ALL ON FUNCTION "+function+" FROM PUBLIC;") {
			t.Fatalf("legacy Worker function retains PUBLIC execute: %s", function)
		}
	}
	for _, function := range newFunctions {
		if !strings.Contains(sql, "ALTER FUNCTION "+function+" OWNER TO platform_worker_repository_owner;") {
			t.Fatalf("new Worker function has no dedicated owner: %s", function)
		}
		if !strings.Contains(sql, "REVOKE ALL ON FUNCTION "+function+" FROM PUBLIC;") {
			t.Fatalf("new Worker function retains PUBLIC execute: %s", function)
		}
	}
	for _, required := range []string{
		"platform_worker_repository_owner must be a NOLOGIN role",
		"web_api must not inherit the Worker Repository owner role",
		"ALTER TABLE worker_instances OWNER TO platform_worker_repository_owner;",
		"ALTER TABLE worker_job_events OWNER TO platform_worker_repository_owner;",
		"ALTER FUNCTION emit_queue_position_events() OWNER TO platform_worker_repository_owner;",
		"REVOKE ALL ON TABLE worker_instances, worker_job_events FROM PUBLIC;",
		"REVOKE CREATE ON SCHEMA public FROM web_api;",
		"GRANT SELECT ON TABLE schema_migrations, scheduler_control, datasets, parameter_profiles,",
		"GRANT USAGE ON SEQUENCE enqueue_sequence, simulation_events_event_id_seq TO web_api;",
		"GRANT EXECUTE ON FUNCTION emit_queue_position_events(), worker_recover_expired_leases() TO web_api;",
		"GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE datasets, simulations, worker_jobs, simulation_events, artifacts, alarm_index, scheduler_control TO platform_worker_repository_owner;",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("missing Worker Repository ownership or ACL guard: %s", required)
		}
	}
	if regexp.MustCompile(`(?s)GRANT\s+[^;]*ON TABLE\s+[^;]*TO algorithm_worker;`).MatchString(sql) {
		t.Fatal("algorithm_worker must not receive Worker table privileges")
	}
	lastACL := strings.LastIndex(sql, "TO algorithm_worker;")
	firstOwnershipTransfer := strings.Index(sql, "ALTER TABLE worker_instances OWNER TO platform_worker_repository_owner;")
	if lastACL < 0 || firstOwnershipTransfer < 0 || lastACL > firstOwnershipTransfer {
		t.Fatal("Worker ACLs must be finalized before NOLOGIN ownership transfer")
	}
}

func TestWorkerRepositoryOwnerSchemaCreateIsTransient(t *testing.T) {
	contents, err := os.ReadFile("migrations/000002_worker_repository_observability.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	const grant = "GRANT CREATE ON SCHEMA public TO platform_worker_repository_owner;"
	const revoke = "REVOKE CREATE ON SCHEMA public FROM platform_worker_repository_owner;"

	grantAt := strings.Index(sql, grant)
	firstOwnershipTransfer := strings.Index(sql, "ALTER TABLE worker_instances OWNER TO platform_worker_repository_owner;")
	lastOwnershipTransfer := strings.LastIndex(sql, "ALTER FUNCTION emit_queue_position_events() OWNER TO platform_worker_repository_owner;")
	revokeAt := strings.Index(sql, revoke)
	if grantAt < 0 || firstOwnershipTransfer < 0 || lastOwnershipTransfer < firstOwnershipTransfer || revokeAt < 0 {
		t.Fatal("Worker Repository owner schema CREATE transfer guard is incomplete")
	}
	if grantAt > firstOwnershipTransfer || revokeAt < lastOwnershipTransfer {
		t.Fatal("Worker Repository owner must receive schema CREATE only around ownership transfers")
	}
	if strings.Count(sql, grant) != 1 || strings.Count(sql, revoke) != 1 {
		t.Fatal("Worker Repository owner schema CREATE must be granted and revoked exactly once")
	}
}

func TestInitialTriggerFunctionsRevokePublicExecute(t *testing.T) {
	contents, err := os.ReadFile("migrations/000002_worker_repository_observability.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, function := range []string{
		"set_updated_at()",
		"reject_immutable_update()",
		"protect_terminal_simulation()",
		"retain_simulation_events()",
	} {
		if !strings.Contains(sql, "REVOKE ALL ON FUNCTION "+function+" FROM PUBLIC;") {
			t.Fatalf("initial trigger function retains PUBLIC execute: %s", function)
		}
	}
}

func TestCustomAliasMigrationPermitsOnlyCustomDisplayNameUpdates(t *testing.T) {
	contents, err := os.ReadFile("migrations/000003_custom_profile_alias.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, required := range []string{
		"CREATE OR REPLACE FUNCTION reject_immutable_parameter_profile_update()",
		"IF OLD.mode = 'REFERENCE' OR",
		"NEW.shared_parameters IS DISTINCT FROM OLD.shared_parameters",
		"NEW.agents_json IS DISTINCT FROM OLD.agents_json",
		"NEW.normalized_sha256 IS DISTINCT FROM OLD.normalized_sha256",
		"REVOKE ALL ON FUNCTION reject_immutable_parameter_profile_update() FROM PUBLIC;",
		"DROP TRIGGER IF EXISTS parameter_profiles_immutable ON parameter_profiles;",
		"FOR EACH ROW EXECUTE FUNCTION reject_immutable_parameter_profile_update();",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("custom alias migration is missing immutable-content guard: %s", required)
		}
	}
	if strings.Contains(sql, "NEW.display_name IS DISTINCT FROM OLD.display_name") {
		t.Fatal("CUSTOM display_name must remain the only mutable parameter profile field")
	}
}

func TestWebAPIRowLockPrivilegesCoverRepositorySurfaces(t *testing.T) {
	contents, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatal(err)
	}
	repository := string(contents)
	for _, required := range []string{
		"SELECT preflight_queue_capacity FROM scheduler_control WHERE singleton = TRUE FOR UPDATE",
		"SELECT request_sha256, run_id FROM idempotency_keys WHERE idempotency_key=$1 FOR UPDATE",
		`return scanSimulation(ctx, tx, " FOR UPDATE", runID)`,
		`FROM simulations s JOIN simulation_snapshots ss ON ss.run_id=s.run_id WHERE s.run_id=$1`,
	} {
		if !strings.Contains(repository, required) {
			t.Fatalf("missing audited Web/API row-lock repository surface: %s", required)
		}
	}

	contents, err = os.ReadFile("migrations/000002_worker_repository_observability.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	const grant = "GRANT UPDATE ON TABLE scheduler_control, simulations, simulation_snapshots, worker_jobs, idempotency_keys TO web_api;"
	if !strings.Contains(string(contents), grant) {
		t.Fatalf("Web/API row-lock surfaces require the explicit grant: %s", grant)
	}
}

func TestCleanDeploymentWebAPIGrantSurfaceIsExplicit(t *testing.T) {
	contents, err := os.ReadFile("migrations/000002_worker_repository_observability.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, required := range []string{
		"GRANT SELECT ON TABLE schema_migrations, scheduler_control, datasets, parameter_profiles,",
		"load_mapping_profiles, simulations, simulation_snapshots, worker_jobs,",
		"simulation_events, idempotency_keys, worker_instances TO web_api;",
		"GRANT INSERT ON TABLE datasets, parameter_profiles, load_mapping_profiles,",
		"simulations, simulation_snapshots, worker_jobs, simulation_events,",
		"idempotency_keys TO web_api;",
		"GRANT UPDATE ON TABLE scheduler_control, simulations, simulation_snapshots, worker_jobs, idempotency_keys TO web_api;",
		"GRANT DELETE ON TABLE simulation_events TO web_api;",
		"RETURNING job_id, job_type, dataset_id, run_id",
		"INSERT INTO worker_job_events(job_id, run_id, event_type, payload_json)",
		"error_code = 'PREFLIGHT_FAILED', error_message = 'LEASE_LOST'",
		"SELECT count(*) INTO recovered_count FROM expired;",
		"PERFORM worker_recover_expired_leases();",
		"WHERE status IN ('RUNNING', 'CANCELLING')",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("missing clean-deployment Web/API privilege: %s", required)
		}
	}
}

func TestWorkerClaimSerializesRecoveryBeforeCheckingLiveLease(t *testing.T) {
	contents, err := os.ReadFile("migrations/000002_worker_repository_observability.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	claimStart := strings.Index(sql, "CREATE OR REPLACE FUNCTION worker_claim_next_job_for_worker(")
	if claimStart < 0 {
		t.Fatal("Worker claim function is missing")
	}
	claimBody := sql[claimStart:]
	lock := strings.Index(claimBody, "PERFORM singleton FROM scheduler_control WHERE singleton = TRUE FOR UPDATE;")
	recover := strings.Index(claimBody, "PERFORM worker_recover_expired_leases();")
	liveLease := strings.Index(claimBody, "AND active_job.lease_expires_at > now()")
	queuedClaim := strings.Index(claimBody, "WHERE queued_job.status = 'QUEUED'")
	if lock < 0 || recover < lock || liveLease < recover || queuedClaim < liveLease {
		t.Fatal("Worker claim must lock scheduler, recover expired leases, reject a live lease, then select queued work")
	}
}

func TestWorkerClaimQualifiesReturnedColumnReferences(t *testing.T) {
	contents, err := os.ReadFile("migrations/000002_worker_repository_observability.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	claimStart := strings.Index(sql, "CREATE OR REPLACE FUNCTION worker_claim_next_job_for_worker(")
	if claimStart < 0 {
		t.Fatal("Worker claim function boundaries are missing")
	}
	claimEnd := strings.Index(sql[claimStart:], "CREATE OR REPLACE FUNCTION worker_heartbeat_for_worker(")
	if claimEnd < 0 {
		t.Fatal("Worker claim function boundaries are missing")
	}
	claimBody := sql[claimStart : claimStart+claimEnd]
	for _, required := range []string{
		"FROM worker_jobs AS active_job",
		"WHERE active_job.status IN ('RUNNING', 'CANCELLING')",
		"AND active_job.lease_expires_at > now()",
		"FROM worker_jobs AS queued_job",
		"WHERE queued_job.status = 'QUEUED'",
		"ORDER BY queued_job.enqueue_seq",
		"UPDATE worker_jobs AS claimed_job",
		"WHERE claimed_job.job_id = selected_job.job_id;",
	} {
		if !strings.Contains(claimBody, required) {
			t.Fatalf("Worker claim has an unqualified or missing source reference: %s", required)
		}
	}
	if strings.Contains(claimBody, "AND lease_expires_at > now()") {
		t.Fatal("Worker claim must not use an unqualified RETURNS TABLE lease_expires_at reference")
	}
}

func TestWorkerRepositoryMigrationDownRestoresLegacyOwnership(t *testing.T) {
	contents, err := os.ReadFile("migrations/000002_worker_repository_observability.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	if !strings.Contains(sql, "ALTER FUNCTION worker_claim_next_job(TEXT, TEXT, INTEGER) OWNER TO web_api;") {
		t.Fatal("down migration does not restore legacy Worker function ownership")
	}
	if !strings.Contains(sql, "ALTER FUNCTION emit_queue_position_events() OWNER TO web_api;") {
		t.Fatal("down migration does not restore queue helper ownership")
	}
}

func TestAlarmLocatorHardeningMigrationRetainsControlledWorkerBoundary(t *testing.T) {
	contents, err := os.ReadFile("migrations/000006_alarm_locator_hardening.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(contents)
	for _, required := range []string{
		"UPDATE alarm_index",
		"ADD CONSTRAINT alarm_index_result_locator_exact_shape",
		"result_locator_json ?& ARRAY['agent', 'original_running_index']",
		"(result_locator_json - 'agent' - 'original_running_index') = '{}'::jsonb",
		"result_locator_json ->> 'agent' = agent::TEXT",
		"result_locator_json ->> 'original_running_index' = original_running_index::TEXT",
		"jsonb_array_elements(COALESCE(p_alarms, '[]'::jsonb))",
		"RAISE EXCEPTION 'invalid alarm result locator' USING ERRCODE = '22023'",
		"jsonb_build_object('agent', item.agent, 'original_running_index', item.original_running_index)",
		"REVOKE ALL ON FUNCTION worker_commit_simulation(TEXT, TEXT, TEXT, CHAR(64), JSONB, JSONB, JSONB) FROM PUBLIC;",
		"GRANT EXECUTE ON FUNCTION worker_commit_simulation(TEXT, TEXT, TEXT, CHAR(64), JSONB, JSONB, JSONB) TO algorithm_worker;",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("alarm locator hardening is missing %q", required)
		}
	}
	if regexp.MustCompile(`(?s)GRANT\s+[^;]*ON TABLE\s+[^;]*TO algorithm_worker;`).MatchString(up) {
		t.Fatal("alarm locator migration must not grant Worker table privileges")
	}
	grant := strings.Index(up, "GRANT CREATE ON SCHEMA public TO platform_worker_repository_owner;")
	setRole := strings.Index(up, "SET LOCAL ROLE platform_worker_repository_owner;")
	replace := strings.Index(up, "CREATE OR REPLACE FUNCTION worker_commit_simulation(")
	reset := strings.Index(up, "RESET ROLE;")
	revoke := strings.LastIndex(up, "REVOKE CREATE ON SCHEMA public FROM platform_worker_repository_owner;")
	if grant < 0 || setRole < grant || replace < setRole || reset < replace || revoke < reset {
		t.Fatal("alarm locator migration does not confine CREATE and replacement to the approved NOLOGIN owner")
	}

	contents, err = os.ReadFile("migrations/000006_alarm_locator_hardening.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(contents)
	for _, required := range []string{
		"CREATE OR REPLACE FUNCTION worker_commit_simulation(",
		"SECURITY DEFINER",
		"SET search_path = pg_catalog, public",
		"REVOKE ALL ON FUNCTION worker_commit_simulation(TEXT, TEXT, TEXT, CHAR(64), JSONB, JSONB, JSONB) FROM PUBLIC;",
		"DROP CONSTRAINT alarm_index_result_locator_exact_shape;",
	} {
		if !strings.Contains(down, required) {
			t.Fatalf("alarm locator downgrade is missing %q", required)
		}
	}
}

func TestSafeAlarmLocatorCommitFixtureCoversAtomicRetry(t *testing.T) {
	contents, err := os.ReadFile("../../../fixtures/worker/worker-commit-safe-locator.v1.sql")
	if err != nil {
		t.Fatal(err)
	}
	fixture := string(contents)
	for _, required := range []string{
		"invalid alarm locator was accepted",
		"EXCEPTION WHEN SQLSTATE '22023'",
		"current_status <> 'RUNNING' OR artifact_count <> 0 OR alarm_count <> 0 OR event_count <> 0",
		"accepted IS DISTINCT FROM TRUE OR retried IS DISTINCT FROM FALSE",
		"artifact_count <> 12 OR alarm_count <> 1 OR event_count <> 3",
		"locator <> jsonb_build_object('agent', 1, 'original_running_index', 14059)",
		"ROLLBACK;",
	} {
		if !strings.Contains(fixture, required) {
			t.Fatalf("safe alarm locator fixture is missing %q", required)
		}
	}
}

func TestWorkerRepositoryUpgradeBootstrapIsLocalAdminOnly(t *testing.T) {
	contents, err := os.ReadFile("../../../scripts/prepare_000002_owner_upgrade.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, required := range []string{
		"REASSIGN OWNED BY web_api TO platform_migrator;",
		"ALTER SCHEMA public OWNER TO platform_migrator;",
		"GRANT platform_worker_repository_owner TO platform_migrator WITH INHERIT FALSE, SET TRUE;",
		"REVOKE platform_worker_repository_owner FROM web_api;",
		"REVOKE platform_worker_repository_owner FROM algorithm_worker;",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("missing required owner-upgrade step: %s", required)
		}
	}
}

func TestExpiredPreflightRecoveryFixtureUsesFrozenTerminalCodes(t *testing.T) {
	contents, err := os.ReadFile("../../../fixtures/worker/lease-recovery-preflight.v1.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, required := range []string{
		"SELECT worker_recover_expired_leases() INTO recovered;",
		"IF recovered <> 1 THEN",
		"dataset_status <> 'INVALID'",
		"dataset_error_code <> 'PREFLIGHT_FAILED'",
		"dataset_error_message <> 'LEASE_LOST'",
		"job_status <> 'FAILED_RECOVERABLE' OR job_error_code <> 'LEASE_LOST'",
		"ROLLBACK;",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("recovery fixture is missing assertion: %s", required)
		}
	}
}
