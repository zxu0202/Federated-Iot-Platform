package httpapi

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWorkerCompatibleCSVReadersRetainImmutableHeaders(t *testing.T) {
	root := t.TempDir()
	metricsPath := writeWorkerCSV(t, root, "metrics.csv", "Agent,FusedRMSE,FusedMAE,FusedR2,FusedCoverage,MeanGlobalSupport\nAgent 1,1.0,0.5,0.9,0.95,10\nAgent 2,2.0,1.0,0.8,0.90,20\nAgent 3,3.0,1.5,0.7,0.85,30\n")
	partitionsPath := writeWorkerCSV(t, root, "agent_partition_summary.csv", "Agent,RunningRows,SupervisedSamples,TrainingSamples,CalibrationSamples,TestingSamples,StartTime,EndTime\nAgent 1,10,9,6,2,1,2026-08-16T08:00:00+08:00,2026-08-16T08:10:00+08:00\nAgent 2,11,10,7,2,1,2026-08-16T08:10:01+08:00,2026-08-16T08:20:00+08:00\nAgent 3,12,11,8,2,1,2026-08-16T08:20:01+08:00,2026-08-16T08:30:00+08:00\n")
	resultsPath := writeWorkerCSV(t, root, "results_agent_1.csv", "OriginalRunningIndex,Time,Agent,FusedPrediction,FusedLowerBound,FusedUpperBound,OverallAlarmLevel\n14059,2026-08-16 08:30:00,1,12.5,11.5,13.5,None\n14060,2026-08-16 08:30:01,1,13.5,12.5,14.5,Warning\n14061,2026-08-16 08:30:02,1,14.5,13.5,15.5,Alarm\n")
	alarmsPath := writeWorkerCSV(t, root, "alarms.csv", "OriginalRunningIndex,OverallAlarmLevel\n14059,None\n14060,Warning\n14061,Alarm\n")

	metricsFile := openWorkerCSV(t, metricsPath)
	metrics, err := readCSVMetadataRows(metricsFile, 3)
	metricsFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got := metrics[0]["Agent"]; got != "Agent 1" {
		t.Fatalf("metrics header was mutated after ReuseRecord reads: Agent=%#v", got)
	}
	selected, err := selectSummaryMetrics(metrics, []int{1, 2, 3}, 2)
	if err != nil || selected["Agent"] != "aggregate" {
		t.Fatalf("Worker metrics labels were not readable: metrics=%#v err=%v", selected, err)
	}

	partitionsFile := openWorkerCSV(t, partitionsPath)
	partitions, err := readCSVMetadataRows(partitionsFile, 3)
	partitionsFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	if split := normalizeSplitSummary(partitions); split["agent_1"].(map[string]any)["running_rows"] != int64(10) {
		t.Fatalf("Worker partition rows were not mapped through their immutable header: %#v", split)
	}

	assertHeaderRemainsStable(t, resultsPath, []string{"OriginalRunningIndex", "Time", "Agent", "FusedPrediction", "FusedLowerBound", "FusedUpperBound", "OverallAlarmLevel"})
	resultsFile := openWorkerCSV(t, resultsPath)
	resultCount, err := countResultCSVRows(resultsFile)
	resultsFile.Close()
	if err != nil || resultCount != 3 {
		t.Fatalf("result row counting failed with reused records: count=%d err=%v", resultCount, err)
	}
	resultsFile = openWorkerCSV(t, resultsPath)
	page, err := readResultCSVPage(resultsFile, "run_worker_csv", resultQuery{Agent: 1, Limit: 2, Sort: "index_asc", Binding: "worker-csv"}, "fixture-sha")
	resultsFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	if page.total != 3 || !page.hasMore || page.nextCursor == "" || page.windowStart != int64(14059) || page.windowEnd != int64(14060) {
		t.Fatalf("results page did not preserve non-zero Worker indices and cursor window: %#v", page)
	}
	if point := page.items[0]; point["OriginalRunningIndex"] != int64(14059) || point["Agent"] != int64(1) {
		t.Fatalf("result scalar typing/header mapping changed: %#v", point)
	}
	after := int64(14060)
	resultsFile = openWorkerCSV(t, resultsPath)
	lastPage, err := readResultCSVPage(resultsFile, "run_worker_csv", resultQuery{Agent: 1, Limit: 2, Sort: "index_asc", Binding: "worker-csv", After: &after}, "fixture-sha")
	resultsFile.Close()
	if err != nil || len(lastPage.items) != 1 || lastPage.items[0]["OriginalRunningIndex"] != int64(14061) || lastPage.hasMore {
		t.Fatalf("results cursor pagination lost or repeated a Worker result: page=%#v err=%v", lastPage, err)
	}

	resultsFile = openWorkerCSV(t, resultsPath)
	chartPoints, count, bandwidthSum, bandwidthCount, err := readSampledResultCSV(resultsFile, 3, 1, 2)
	resultsFile.Close()
	if err != nil || count != 3 || len(chartPoints) != 3 || bandwidthSum != 6 || bandwidthCount != 3 {
		t.Fatalf("summary-chart result sampling failed with reused records: points=%#v count=%d sum=%v bandwidth_count=%d err=%v", chartPoints, count, bandwidthSum, bandwidthCount, err)
	}

	alarmsFile := openWorkerCSV(t, alarmsPath)
	counts, err := countAlarmLevelsCSV(alarmsFile)
	alarmsFile.Close()
	if err != nil || counts["None"] != int64(1) || counts["Warning"] != int64(1) || counts["Alarm"] != int64(1) {
		t.Fatalf("alarm counting failed with reused records: counts=%#v err=%v", counts, err)
	}
}

func writeWorkerCSV(t *testing.T, root, name, contents string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(contents), 0o640); err != nil {
		t.Fatal(err)
	}
	return path
}

func openWorkerCSV(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func assertHeaderRemainsStable(t *testing.T, path string, expected []string) {
	t.Helper()
	file := openWorkerCSV(t, path)
	defer file.Close()
	reader := csv.NewReader(file)
	reader.ReuseRecord = true
	header, err := readImmutableCSVHeader(reader)
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := reader.Read(); err != nil {
			break
		}
	}
	if !reflect.DeepEqual(header, expected) {
		t.Fatalf("retained header changed after streaming rows: got=%q want=%q", header, expected)
	}
}
