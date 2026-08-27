package postgres

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestPreflightAttemptIdentityMigrationRetainsOnlySafeAttemptFacts(t *testing.T) {
	contents, err := os.ReadFile("migrations/000008_dataset_preflight_attempt_identity.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(contents)
	for _, required := range []string{
		"ALTER TABLE worker_jobs ADD COLUMN IF NOT EXISTS last_attempt_id TEXT;",
		"SET last_attempt_id = attempt_id",
		"event.event_type = 'worker.stage'",
		"event.payload_json ->> 'attempt_id' ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'",
		"COALESCE(j.attempt_id, j.last_attempt_id, s.attempt_id)",
		"SET status = 'RUNNING', attempt_id = p_attempt_id, last_attempt_id = p_attempt_id,",
		"CREATE OR REPLACE FUNCTION dataset_preflight_projection(p_dataset_id TEXT)",
		"CREATE OR REPLACE FUNCTION worker_claim_next_job_for_worker(",
		"SECURITY DEFINER",
		"SET search_path = pg_catalog, public",
		"REVOKE ALL ON FUNCTION dataset_preflight_projection(TEXT) FROM PUBLIC;",
		"GRANT EXECUTE ON FUNCTION dataset_preflight_projection(TEXT) TO web_api;",
		"REVOKE ALL ON FUNCTION worker_claim_next_job_for_worker(TEXT, TEXT, TEXT, INTEGER) FROM PUBLIC;",
		"GRANT EXECUTE ON FUNCTION worker_claim_next_job_for_worker(TEXT, TEXT, TEXT, INTEGER) TO algorithm_worker;",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("attempt identity migration is missing %q", required)
		}
	}
	projectionStart := strings.Index(up, "CREATE OR REPLACE FUNCTION dataset_preflight_projection")
	claimStart := strings.Index(up, "CREATE OR REPLACE FUNCTION worker_claim_next_job_for_worker")
	if projectionStart < 0 || claimStart < projectionStart {
		t.Fatal("attempt identity projection boundaries are missing")
	}
	projection := up[projectionStart:claimStart]
	if strings.Contains(projection, "lease_token") || strings.Contains(projection, "lease_expires_at TIMESTAMPTZ,") {
		t.Fatal("preflight projection must not add lease secrets to its public result shape")
	}
	if regexp.MustCompile(`(?s)GRANT\s+[^;]*ON TABLE\s+[^;]*TO algorithm_worker;`).MatchString(up) {
		t.Fatal("attempt identity migration must not grant Worker table access")
	}
	grant := strings.Index(up, "GRANT CREATE ON SCHEMA public TO platform_worker_repository_owner;")
	setRole := strings.Index(up, "SET LOCAL ROLE platform_worker_repository_owner;")
	backfill := strings.Index(up, "UPDATE worker_jobs AS job")
	replaceProjection := projectionStart
	replaceClaim := claimStart
	reset := strings.Index(up, "RESET ROLE;")
	revoke := strings.LastIndex(up, "REVOKE CREATE ON SCHEMA public FROM platform_worker_repository_owner;")
	if grant < 0 || setRole < grant || backfill < setRole || replaceProjection < backfill || replaceClaim < replaceProjection || reset < replaceClaim || revoke < reset {
		t.Fatal("attempt identity backfill and replacement are not confined to the approved NOLOGIN function owner")
	}
	for _, forbidden := range []string{
		"GRANT SELECT ON TABLE worker_job_events TO platform_migrator;",
		"GRANT SELECT ON TABLE worker_job_events TO web_api;",
		"GRANT SELECT ON TABLE worker_job_events TO algorithm_worker;",
	} {
		if strings.Contains(up, forbidden) {
			t.Fatalf("attempt identity migration widens worker event access: %s", forbidden)
		}
	}

	down, err := os.ReadFile("migrations/000008_dataset_preflight_attempt_identity.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(down), "ALTER TABLE worker_jobs DROP COLUMN IF EXISTS last_attempt_id;") || !strings.Contains(string(down), "REVOKE CREATE ON SCHEMA public FROM platform_worker_repository_owner;") {
		t.Fatal("attempt identity downgrade does not restore the controlled schema boundary")
	}
}

func TestPreflightAttemptIdentityFixtureCoversTerminalAndUnclaimedCases(t *testing.T) {
	contents, err := os.ReadFile("../../../fixtures/postgres/dataset-preflight-attempt-identity.v1.sql")
	if err != nil {
		t.Fatal(err)
	}
	fixture := string(contents)
	for _, required := range []string{
		"'COMPLETED', 'attempt_terminal_fixture'",
		"'QUEUED', NULL",
		"terminal_projection.attempt_id <> 'attempt_terminal_fixture'",
		"terminal_json ? 'lease_token' OR terminal_json ? 'lease_expires_at'",
		"queued_projection.attempt_id IS NOT NULL",
		"queued_projection.lease_state <> 'NOT_CLAIMED'",
		"ROLLBACK;",
	} {
		if !strings.Contains(fixture, required) {
			t.Fatalf("attempt identity fixture is missing %q", required)
		}
	}
}
