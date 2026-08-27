package postgres

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zx/federated-iot-platform/backend/internal/domain"
)

func TestSimulationListWhereContainsEveryFilterButNotCursor(t *testing.T) {
	status := domain.SimulationCompleted
	mode := domain.RunModeCustom
	from := time.Date(2026, 8, 16, 1, 2, 3, 0, time.UTC)
	to := from.Add(time.Hour)
	where, args := simulationListWhere(SimulationListQuery{
		View: "queue", RunID: "run_1", Status: &status, DatasetID: "ds_1",
		ParameterProfileVersionID: "pp_1", RunMode: &mode, Search: "motor",
		CreatedFrom: &from, CreatedTo: &to,
		Cursor: &SimulationListCursor{RunID: "run_cursor"},
	})
	for _, clause := range []string{
		"s.status IN ('RUNNING','CANCELLING','GENERATING_ARTIFACTS','QUEUED')",
		"s.run_id=$1", "s.status=$2", "s.dataset_id=$3", "s.parameter_profile_version_id=$4",
		"s.run_mode=$5", "lower(s.run_id) LIKE $6", "s.created_at >= $9", "s.created_at <= $10",
	} {
		if !strings.Contains(where, clause) {
			t.Fatalf("shared filter plan is missing %q: %s", clause, where)
		}
	}
	if strings.Contains(where, "run_cursor") || strings.Contains(where, "enqueue_seq") {
		t.Fatalf("cursor boundary leaked into authoritative total filter: %s", where)
	}
	if len(args) != 10 {
		t.Fatalf("filter argument count = %d, want 10", len(args))
	}
}

func TestSimulationPageRetainsTotalAcrossFirstEmptyAndLastPages(t *testing.T) {
	records := make([]SimulationRecord, 101)
	for index := range records {
		records[index].RunID = "run_" + strconv.Itoa(index+1)
	}
	first := simulationPageFromRecords(records, 100, 101)
	if first.Total != 101 || len(first.Items) != 100 || !first.HasMore {
		t.Fatalf("first page = %#v, want 100 items, total 101, and has_more", first)
	}
	seen := make(map[string]bool, len(first.Items))
	for _, record := range first.Items {
		seen[record.RunID] = true
	}
	last := simulationPageFromRecords(records[100:], 100, 101)
	if last.Total != 101 || len(last.Items) != 1 || last.HasMore {
		t.Fatalf("last page = %#v, want 1 item, total 101, and no next page", last)
	}
	if seen[last.Items[0].RunID] {
		t.Fatalf("cursor pages overlap on %q", last.Items[0].RunID)
	}
	seen[last.Items[0].RunID] = true
	if len(seen) != 101 {
		t.Fatalf("cursor pages are missing records: saw %d, want 101", len(seen))
	}
	empty := simulationPageFromRecords(nil, 100, 0)
	if empty.Total != 0 || len(empty.Items) != 0 || empty.HasMore {
		t.Fatalf("empty page = %#v, want total 0 with no next page", empty)
	}
	beyondLast := simulationPageFromRecords(nil, 100, 101)
	if beyondLast.Total != 101 || len(beyondLast.Items) != 0 || beyondLast.HasMore {
		t.Fatalf("cursor-empty page = %#v, want total 101 with no next page", beyondLast)
	}
}
