package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zx/federated-iot-platform/backend/internal/config"
	"github.com/zx/federated-iot-platform/backend/internal/domain"
	"github.com/zx/federated-iot-platform/backend/internal/storage/postgres"
)

func TestRouteInventoryMatchesFrozenOpenAPI(t *testing.T) {
	contents, err := os.ReadFile("../../../contracts/api/openapi.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatal(err)
	}
	want := make(map[string]bool)
	for path, operations := range document.Paths {
		for method := range operations {
			if method == "get" || method == "post" || method == "patch" || method == "put" || method == "delete" {
				want[strings.ToUpper(method)+" /api/v1"+path] = true
			}
		}
	}
	api := &API{}
	got := make(map[string]bool)
	for _, route := range api.routes() {
		if got[route.pattern] {
			t.Fatalf("duplicate handler route %q", route.pattern)
		}
		got[route.pattern] = true
	}
	if len(want) != 27 {
		t.Fatalf("OpenAPI operation count = %d, want 27", len(want))
	}
	if len(got) != len(want) {
		t.Fatalf("handler route count = %d, OpenAPI count = %d", len(got), len(want))
	}
	for route := range want {
		if !got[route] {
			t.Fatalf("OpenAPI route is not registered by Handler: %s", route)
		}
	}
}

func mustParseTime(t *testing.T, raw string) time.Time {
	t.Helper()
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestSimulationListCursorIsBoundToFilters(t *testing.T) {
	mode := domain.RunModeReference
	query := postgres.SimulationListQuery{View: "history", DatasetID: "ds_1", RunMode: &mode, Limit: 100}
	binding, err := simulationListBinding(query)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := encodeSimulationListCursor("history", binding, postgres.SimulationRecord{RunID: "run_1", CreatedAt: mustParseTime(t, "2026-08-16T01:02:03Z")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeSimulationListCursor(cursor, "queue", binding); err == nil {
		t.Fatal("cursor was accepted for a different view")
	}
	if _, err := decodeSimulationListCursor(cursor, "history", "different"); err == nil {
		t.Fatal("cursor was accepted for different filters")
	}
}

func TestSimulationListSearchBoundsAndCursorBinding(t *testing.T) {
	for _, search := range []string{"", "x", strings.Repeat("x", 256)} {
		request := httptest.NewRequest("GET", "/api/v1/simulations?search="+search, nil)
		query, _, err := parseSimulationListQuery(request)
		if err != nil {
			t.Fatalf("search length %d was rejected: %v", len(search), err)
		}
		if query.Search != search {
			t.Fatalf("parsed search = %q, want %q", query.Search, search)
		}
	}
	request := httptest.NewRequest("GET", "/api/v1/simulations?search="+strings.Repeat("x", 257), nil)
	if _, _, err := parseSimulationListQuery(request); err == nil {
		t.Fatal("search length 257 was accepted")
	}

	mode := domain.RunModeReference
	query := postgres.SimulationListQuery{View: "history", Search: "motor", RunMode: &mode, Limit: 100}
	binding, err := simulationListBinding(query)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := encodeSimulationListCursor(query.View, binding, postgres.SimulationRecord{RunID: "run_100", CreatedAt: mustParseTime(t, "2026-08-16T01:02:03Z")})
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest("GET", "/api/v1/simulations?search=other&cursor="+cursor, nil)
	if _, _, err := parseSimulationListQuery(request); err == nil {
		t.Fatal("cursor was accepted after the search filter changed")
	}
}

func TestSimulationListResponseAlwaysCarriesAuthoritativePaginationMeta(t *testing.T) {
	page := postgres.SimulationPage{Total: 101, HasMore: true}
	response := simulationListResponse("req_1", []any{}, "cursor_100", page)
	meta, ok := response["meta"].(map[string]any)
	if !ok {
		t.Fatalf("response meta = %#v", response["meta"])
	}
	if meta["request_id"] != "req_1" || meta["total"] != int64(101) || meta["next_cursor"] != "cursor_100" || meta["has_more"] != true {
		t.Fatalf("unexpected pagination meta: %#v", meta)
	}
	empty := simulationListResponse("req_2", []any{}, nil, postgres.SimulationPage{Total: 0})
	emptyMeta := empty["meta"].(map[string]any)
	if emptyMeta["request_id"] != "req_2" || emptyMeta["total"] != int64(0) || emptyMeta["next_cursor"] != nil || emptyMeta["has_more"] != false {
		t.Fatalf("empty pagination meta is incomplete: %#v", emptyMeta)
	}
}

func TestCommittedArtifactPathAndHashAreVerified(t *testing.T) {
	root := t.TempDir()
	runID, name := "run_artifact", "results_agent_1.csv"
	path := filepath.Join(root, "runs", runID, "committed", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	contents := []byte("OriginalRunningIndex,Time,Agent\n0,2026-08-16T00:00:00Z,1\n")
	if err := os.WriteFile(path, contents, 0o640); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	api := &API{config: config.Config{ArtifactRoot: root}}
	artifact := postgres.ArtifactRecord{
		RunID: runID, Name: name, RelativePath: "runs/" + runID + "/committed/" + name,
		SizeBytes: int64(len(contents)), SHA256: hex.EncodeToString(digest[:]),
	}
	file, err := api.openCommittedArtifact(runID, artifact)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	artifact.RelativePath = "runs/other/committed/" + name
	if _, err := api.openCommittedArtifact(runID, artifact); err == nil {
		t.Fatal("artifact path outside its committed root was accepted")
	}
}

func TestCommittedArtifactRejectsUnsafeFilesystemAndManifestState(t *testing.T) {
	root := t.TempDir()
	runID, name := "run_artifact_reject", "results_agent_1.csv"
	path := filepath.Join(root, "runs", runID, "committed", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	contents := []byte("OriginalRunningIndex,Time,Agent\n1,2026-08-16 08:00:00,1\n")
	if err := os.WriteFile(path, contents, 0o640); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	valid := postgres.ArtifactRecord{RunID: runID, Name: name, RelativePath: "runs/" + runID + "/committed/" + name, SizeBytes: int64(len(contents)), SHA256: hex.EncodeToString(digest[:])}
	api := &API{config: config.Config{ArtifactRoot: root}}

	for _, test := range []struct {
		name     string
		artifact postgres.ArtifactRecord
	}{
		{"registered size", func() postgres.ArtifactRecord { item := valid; item.SizeBytes++; return item }()},
		{"registered hash", func() postgres.ArtifactRecord { item := valid; item.SHA256 = strings.Repeat("0", 64); return item }()},
		{"cross run path", func() postgres.ArtifactRecord {
			item := valid
			item.RelativePath = "runs/other/committed/" + name
			return item
		}()},
		{"traversal name", func() postgres.ArtifactRecord { item := valid; item.Name = "../" + name; return item }()},
	} {
		t.Run(test.name, func(t *testing.T) {
			if file, err := api.openCommittedArtifact(runID, test.artifact); err == nil {
				file.Close()
				t.Fatal("unsafe committed artifact state was accepted")
			}
		})
	}

	directoryName := "not_a_regular_file"
	if err := os.Mkdir(filepath.Join(root, "runs", runID, "committed", directoryName), 0o750); err != nil {
		t.Fatal(err)
	}
	directory := valid
	directory.Name = directoryName
	directory.RelativePath = "runs/" + runID + "/committed/" + directoryName
	directory.SizeBytes = 0
	if file, err := api.openCommittedArtifact(runID, directory); err == nil {
		file.Close()
		t.Fatal("non-regular committed artifact was accepted")
	}

	target := filepath.Join(root, "outside.csv")
	if err := os.WriteFile(target, contents, 0o640); err != nil {
		t.Fatal(err)
	}
	linkName := "linked.csv"
	if err := os.Symlink(target, filepath.Join(root, "runs", runID, "committed", linkName)); err != nil {
		t.Skipf("symlink test requires local symlink permission: %v", err)
	}
	linked := valid
	linked.Name = linkName
	linked.RelativePath = "runs/" + runID + "/committed/" + linkName
	if file, err := api.openCommittedArtifact(runID, linked); err == nil {
		file.Close()
		t.Fatal("symlinked committed artifact was accepted")
	}
}

func TestResultReaderAcceptsWorkerTimestampAndPreservesTypedFields(t *testing.T) {
	header := []string{"OriginalRunningIndex", "Time", "Agent", "FusedPrediction", "FusedInsideInterval", "OverallAlarmLevel"}
	row := []string{"17", "2026-08-16 08:30:00", "2", "12.5", "true", "Warning"}
	point, err := resultPointFromCSV(header, row, 0, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if point.index != 17 || point.time.Location().String() != "Asia/Shanghai" {
		t.Fatalf("legacy Worker timestamp was not normalized for filtering: %#v", point)
	}
	if point.value["FusedPrediction"] != 12.5 || point.value["FusedInsideInterval"] != true {
		t.Fatalf("CSV result scalar types were not preserved: %#v", point.value)
	}
}

func TestReplayArchiveIsRepeatableAndContainsSourceHashManifest(t *testing.T) {
	manifest := []byte(`{"source_sha256":"abc"}`)
	exportedAt := mustParseTime(t, "2026-08-16T01:02:03Z")
	var first, second bytes.Buffer
	if err := writeReplayArchive(&first, strings.NewReader("a,b\n1,2\n"), 1, manifest, exportedAt); err != nil {
		t.Fatal(err)
	}
	if err := writeReplayArchive(&second, strings.NewReader("a,b\n1,2\n"), 1, manifest, exportedAt); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("preflight ZIP hash would not match the streamed replay archive")
	}
}
