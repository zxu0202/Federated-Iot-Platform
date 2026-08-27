package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestResultReadMigrationGrantsOnlyWebAPIReadAccess(t *testing.T) {
	contents, err := os.ReadFile("migrations/000005_result_read_acl.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	if !strings.Contains(sql, "GRANT SELECT ON TABLE artifacts, alarm_index TO web_api;") {
		t.Fatal("Web/API result readers need explicit artifacts and alarm_index SELECT")
	}
	if strings.Contains(sql, "TO algorithm_worker;") || strings.Contains(sql, "GRANT UPDATE") || strings.Contains(sql, "GRANT INSERT") {
		t.Fatal("result reader migration must not widen Worker or Web/API write access")
	}
}

func TestSimulationListUsesSimulationRowsAndStableOrder(t *testing.T) {
	contents, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatal(err)
	}
	repository := string(contents)
	start := strings.Index(repository, "func (r *Repository) ListSimulations(")
	end := strings.Index(repository, "func (r *Repository) RequireCompletedArtifacts(")
	if start < 0 || end < start {
		t.Fatal("simulation list implementation boundaries are missing")
	}
	implementation := repository[start:end]
	for _, required := range []string{
		"s.created_at DESC, s.run_id DESC",
		"s.status IN ('RUNNING','CANCELLING','GENERATING_ARTIFACTS','QUEUED')",
		"s.enqueue_seq ASC, s.run_id ASC",
		"query.Limit+1",
	} {
		if !strings.Contains(implementation, required) {
			t.Fatalf("simulation list is missing %q", required)
		}
	}
	if !strings.Contains(repository, "FROM simulations AS s JOIN simulation_snapshots AS ss") {
		t.Fatal("simulation list must read simulation snapshots")
	}
	if strings.Contains(implementation, "worker_jobs") {
		t.Fatal("simulation list must not mix DATASET_PREFLIGHT worker jobs into task views")
	}
}

func TestCompletedResultReadGateRequiresRegisteredRequiredArtifacts(t *testing.T) {
	contents, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatal(err)
	}
	repository := string(contents)
	start := strings.Index(repository, "func (r *Repository) RequireCompletedArtifacts(")
	end := strings.Index(repository[start:], "// RequireCommittedArtifacts")
	if start < 0 || end < 0 {
		t.Fatal("completed result-read gate boundaries are missing")
	}
	gate := repository[start : start+end]
	for _, required := range []string{
		"record.Status != domain.SimulationCompleted || record.ArtifactState != \"COMMITTED\"",
		"WHERE run_id=$1 AND required=TRUE AND name = ANY($2)",
		"registered != len(requiredResultArtifactNames())",
		"Code: \"RESULT_NOT_READY\"",
		"\"metrics.csv\"", "\"results_agent_1.csv\"", "\"results_agent_2.csv\"", "\"results_agent_3.csv\"",
		"\"alarms.csv\"", "\"artifact_manifest.json\"",
	} {
		if !strings.Contains(gate, required) && !strings.Contains(repository, required) {
			t.Fatalf("completed result-read gate is missing %q", required)
		}
	}
}

func TestArtifactInventoryGateDelegatesToStrictCompletedResultGate(t *testing.T) {
	contents, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatal(err)
	}
	repository := string(contents)
	start := strings.Index(repository, "func (r *Repository) RequireCommittedArtifacts(")
	end := strings.Index(repository[start:], "type ArtifactRecord struct")
	if start < 0 || end < 0 {
		t.Fatal("artifact inventory gate boundaries are missing")
	}
	gate := repository[start : start+end]
	if !strings.Contains(gate, "r.RequireCompletedArtifacts(ctx, runID)") {
		t.Fatal("artifact inventory gate does not share the completed result-read gate")
	}
	if !strings.Contains(gate, "Code: \"ARTIFACT_NOT_AVAILABLE\"") {
		t.Fatal("artifact inventory gate no longer returns its stable unavailable code")
	}
}
