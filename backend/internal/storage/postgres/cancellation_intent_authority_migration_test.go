package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestCancellationIntentAuthorityMigrationPreservesControlledWorkerBoundary(t *testing.T) {
	contents, err := os.ReadFile("migrations/000009_cancellation_intent_authority.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(contents)
	for _, required := range []string{
		"CREATE OR REPLACE FUNCTION worker_recover_expired_leases()",
		"CREATE OR REPLACE FUNCTION worker_fail_job(",
		"SECURITY DEFINER SET search_path = pg_catalog, public",
		"w.status = 'CANCELLING' OR s.cancel_requested_at IS NOT NULL",
		"CASE WHEN candidate.cancellation_requested THEN 'CANCELLED' ELSE 'FAILED_RECOVERABLE' END",
		"WHEN candidate.cancellation_requested THEN NULL",
		"PERFORM singleton FROM scheduler_control WHERE singleton = TRUE FOR UPDATE;",
		"cancellation_requested := cancellation_requested OR selected_job.status = 'CANCELLING';",
		"SET status = 'CANCELLED', error_json = NULL",
		"REVOKE ALL ON FUNCTION worker_recover_expired_leases() FROM PUBLIC;",
		"REVOKE ALL ON FUNCTION worker_fail_job(TEXT, TEXT, TEXT, JSONB, BOOLEAN) FROM PUBLIC;",
		"GRANT EXECUTE ON FUNCTION worker_recover_expired_leases() TO web_api;",
		"GRANT EXECUTE ON FUNCTION worker_fail_job(TEXT, TEXT, TEXT, JSONB, BOOLEAN) TO algorithm_worker;",
		"SET LOCAL ROLE platform_worker_repository_owner;",
		"REVOKE CREATE ON SCHEMA public FROM platform_worker_repository_owner;",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("cancellation authority migration is missing %q", required)
		}
	}
	if strings.Contains(up, "GRANT UPDATE ON TABLE") || strings.Contains(up, "TO algorithm_worker;\nGRANT") {
		t.Fatal("cancellation authority migration must not broaden table privileges")
	}
}

func TestCancellationIntentAuthorityDownMigrationRestoresPriorFunctions(t *testing.T) {
	contents, err := os.ReadFile("migrations/000009_cancellation_intent_authority.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(contents)
	for _, required := range []string{
		"CREATE OR REPLACE FUNCTION worker_recover_expired_leases()",
		"CREATE OR REPLACE FUNCTION worker_fail_job(",
		"SECURITY DEFINER SET search_path = pg_catalog, public",
		"REVOKE ALL ON FUNCTION worker_recover_expired_leases() FROM PUBLIC;",
		"REVOKE ALL ON FUNCTION worker_fail_job(TEXT, TEXT, TEXT, JSONB, BOOLEAN) FROM PUBLIC;",
		"REVOKE CREATE ON SCHEMA public FROM platform_worker_repository_owner;",
	} {
		if !strings.Contains(down, required) {
			t.Fatalf("cancellation authority downgrade is missing %q", required)
		}
	}
}
