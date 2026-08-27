package postgres

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestDatasetPreflightProjectionMigrationPreservesFilenameAndLeastPrivilege(t *testing.T) {
	contents, err := os.ReadFile("migrations/000007_dataset_api_preflight_projection.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, required := range []string{
		"ALTER TABLE datasets ADD COLUMN original_filename TEXT;",
		"UPDATE datasets",
		"ALTER COLUMN original_filename SET NOT NULL",
		"ADD CONSTRAINT datasets_original_filename_safe CHECK",
		"CREATE OR REPLACE FUNCTION dataset_preflight_projection(p_dataset_id TEXT)",
		"RETURNS TABLE(",
		"LANGUAGE sql",
		"SECURITY DEFINER",
		"SET search_path = pg_catalog, public",
		"FROM worker_jobs AS w",
		"FROM worker_job_events AS e",
		"queued_job.status = 'QUEUED'",
		"WHEN j.status = 'QUEUED' THEN 'NOT_CLAIMED'",
		"WHEN j.status IN ('RUNNING', 'CANCELLING') AND j.lease_expires_at > now() THEN 'ACTIVE'",
		"WHEN j.status IN ('RUNNING', 'CANCELLING') THEN 'EXPIRED'",
		"REVOKE ALL ON FUNCTION dataset_preflight_projection(TEXT) FROM PUBLIC;",
		"GRANT EXECUTE ON FUNCTION dataset_preflight_projection(TEXT) TO web_api;",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("dataset preflight migration is missing %q", required)
		}
	}
	if regexp.MustCompile(`(?s)GRANT\s+[^;]*ON TABLE\s+[^;]*TO algorithm_worker;`).MatchString(sql) || strings.Contains(sql, "TO algorithm_worker") {
		t.Fatal("dataset projection must not grant Worker table or function access")
	}
	grant := strings.Index(sql, "GRANT CREATE ON SCHEMA public TO platform_worker_repository_owner;")
	setRole := strings.Index(sql, "SET LOCAL ROLE platform_worker_repository_owner;")
	create := strings.Index(sql, "CREATE OR REPLACE FUNCTION dataset_preflight_projection")
	reset := strings.Index(sql, "RESET ROLE;")
	revoke := strings.LastIndex(sql, "REVOKE CREATE ON SCHEMA public FROM platform_worker_repository_owner;")
	if grant < 0 || setRole < grant || create < setRole || reset < create || revoke < reset {
		t.Fatal("dataset projection must confine schema CREATE and function ownership to the approved NOLOGIN owner")
	}

	down, err := os.ReadFile("migrations/000007_dataset_api_preflight_projection.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(down), "DROP FUNCTION IF EXISTS dataset_preflight_projection(TEXT);") || !strings.Contains(string(down), "REVOKE CREATE ON SCHEMA public FROM platform_worker_repository_owner;") {
		t.Fatal("dataset projection down migration does not retain the controlled owner boundary")
	}
}

func TestDatasetReadUsesControlledPreflightProjection(t *testing.T) {
	contents, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatal(err)
	}
	repository := string(contents)
	start := strings.Index(repository, "func (r *Repository) GetDataset(")
	end := strings.Index(repository, "type ProfileInput")
	if start < 0 || end < start {
		t.Fatal("GetDataset boundaries are missing")
	}
	query := repository[start:end]
	for _, required := range []string{
		"d.original_filename", "d.validation_started_at", "d.validation_finished_at",
		"LEFT JOIN LATERAL dataset_preflight_projection(d.dataset_id) AS projection ON TRUE",
		"projection.job_id", "projection.job_status", "projection.queue_position", "projection.current_stage",
		"projection.attempt_id", "projection.lease_state", "projection.latest_event_id", "projection.error_json",
	} {
		if !strings.Contains(query, required) {
			t.Fatalf("GetDataset is missing projection fact %q", required)
		}
	}
	if strings.Contains(query, "FROM worker_job_events") || strings.Contains(query, "lease_token") || strings.Contains(query, "lease_expires_at") || strings.Contains(query, "storage_key") {
		t.Fatal("GetDataset bypasses the safe projection or exposes a control-plane secret")
	}
}
