package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zx/federated-iot-platform/backend/internal/domain"
	"github.com/zx/federated-iot-platform/backend/internal/storage/postgres"
)

func TestDatasetResponseIsCompleteAndDoesNotExposeControlPlaneSecrets(t *testing.T) {
	started := time.Date(2026, time.August, 22, 1, 2, 3, 0, time.UTC)
	finished := started.Add(time.Minute)
	jobID, jobStatus, leaseState := "job_preflight", "FAILED", "RELEASED"
	attemptID, stage := "attempt_1", "PREPROCESSING"
	queuePosition := 3
	eventID := int64(41)
	summary := json.RawMessage(`{"schema_version":"dataset-preflight.summary.v1","preprocessing_contract_version":"preprocessing.v1","input_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","counts":{"raw_rows":129438,"invalid_numeric_rows":0,"stop_rows":79444,"suspicious_rows":376,"running_rows":49618,"spike_rows":4502},"time":{"start":"2026-08-22 09:00:00","end":"2026-08-22 10:00:00","parse_failed_count":0,"non_monotonic_count":0,"sampling_period_ms":{"median":1000,"min":1000,"max":1000}},"filter_path":{},"parameters":{},"summary_sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`)
	record := postgres.DatasetRecord{
		DatasetID: "ds_1", DisplayName: "Motor data", OriginalFilename: "motor.csv",
		Status: domain.DatasetValid, SHA256: strings.Repeat("c", 64), SizeBytes: 12,
		Timezone: "Asia/Shanghai", UTCOffset: "+08:00", StructuralStatistics: json.RawMessage(`{"raw_rows":129438,"invalid_numeric_rows":0}`),
		PreflightSummary: summary, ValidationStartedAt: &started, ValidationFinishedAt: &finished, CreatedAt: started,
		Preflight: postgres.DatasetPreflightRecord{
			JobID: &jobID, Status: &jobStatus, QueuePosition: &queuePosition, Stage: &stage, AttemptID: &attemptID,
			LeaseState: &leaseState, LatestEventID: &eventID,
			Error: json.RawMessage(`{"code":"INSUFFICIENT_SAMPLES","message":"The filtered data has too few running rows.","stage":"PREPROCESSING","diagnostic_id":"diag_preflight_1","recoverable":false,"agent":2,"internal_path":"/var/lib/iot/datasets"}`),
		},
	}
	response := datasetResponse(record)
	for _, key := range []string{"dataset_id", "display_name", "original_filename", "status", "sha256", "size_bytes", "columns", "timezone", "utc_offset", "statistics", "preflight", "algorithm_preprocessing", "validation_started_at", "validation_finished_at", "created_at"} {
		if _, ok := response[key]; !ok {
			t.Fatalf("dataset response is missing %q: %#v", key, response)
		}
	}
	for _, forbidden := range []string{"storage_key", "lease_token", "lease_expires_at", "error_code", "error_message"} {
		if _, ok := response[forbidden]; ok {
			t.Fatalf("dataset response exposed %q", forbidden)
		}
	}
	if response["original_filename"] != "motor.csv" {
		t.Fatalf("original filename = %#v", response["original_filename"])
	}
	if !reflect.DeepEqual(response["algorithm_preprocessing"], rawOrObject(summary)) {
		t.Fatalf("valid preprocessing summary was changed: %#v", response["algorithm_preprocessing"])
	}
	preflight := response["preflight"].(map[string]any)
	if preflight["job_id"] != jobID || preflight["status"] != jobStatus || preflight["latest_event_id"] != eventID || preflight["contract_version"] != domain.PreflightContractVersion {
		t.Fatalf("preflight projection = %#v", preflight)
	}
	errorValue := preflight["error"].(map[string]any)
	if errorValue["code"] != "INSUFFICIENT_SAMPLES" || errorValue["message"] != "The filtered data has too few running rows." || errorValue["stage"] != "PREPROCESSING" || errorValue["diagnostic_id"] != "diag_preflight_1" || errorValue["recoverable"] != false || errorValue["agent"] != 2 {
		t.Fatalf("preflight error lost a frozen diagnostic: %#v", errorValue)
	}
	if _, leaked := errorValue["internal_path"]; leaked {
		t.Fatal("preflight error leaked an internal field")
	}
}

func TestDatasetResponseMapsLegacyLeaseLossWithoutCollapsingItsCode(t *testing.T) {
	jobID, status, leaseState := "job_expired", "FAILED_RECOVERABLE", "RELEASED"
	record := postgres.DatasetRecord{
		DatasetID: "ds_expired", DisplayName: "Expired", OriginalFilename: "expired.csv", Status: domain.DatasetInvalid,
		Preflight: postgres.DatasetPreflightRecord{JobID: &jobID, Status: &status, LeaseState: &leaseState, Error: json.RawMessage(`{"code":"LEASE_LOST","recoverable":true}`)},
	}
	errorValue := datasetResponse(record)["preflight"].(map[string]any)["error"].(map[string]any)
	if errorValue["code"] != "LEASE_LOST" || errorValue["message"] != "Dataset preflight failed." || errorValue["diagnostic_id"] != "preflight:job_expired:LEASE_LOST" || errorValue["recoverable"] != true {
		t.Fatalf("legacy preflight error is not safely projected: %#v", errorValue)
	}
	if got := datasetResponse(record)["algorithm_preprocessing"]; got != nil {
		t.Fatalf("invalid dataset returned algorithm preprocessing: %#v", got)
	}
}

func TestSafeOriginalFilenameRetainsOnlySafeBasename(t *testing.T) {
	filename, err := safeOriginalFilename(`C:\\upload\\aligned.csv`)
	if err != nil || filename != "aligned.csv" {
		t.Fatalf("safe filename = %q, %v", filename, err)
	}
	for _, raw := range []string{"", ".", "..", "bad\x00.csv", strings.Repeat("a", 256)} {
		if _, err := safeOriginalFilename(raw); err == nil {
			t.Fatalf("unsafe filename was accepted: %q", raw)
		}
	}
}

func TestDatasetMultipartRetainsOriginalFilenameAndDefaultsDisplayName(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("timezone", "Asia/Shanghai"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "aligned.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("Time_base,dzdl_1\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/api/v1/datasets", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	displayName, timezone, originalFilename, file, err := readDatasetMultipart(request, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	contents, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if displayName != "" || timezone != "Asia/Shanghai" || originalFilename != "aligned.csv" || string(contents) != "Time_base,dzdl_1\n" {
		t.Fatalf("multipart metadata was not retained safely: display=%q timezone=%q original=%q contents=%q", displayName, timezone, originalFilename, contents)
	}
	if got := effectiveDatasetDisplayName(displayName, originalFilename); got != "aligned.csv" {
		t.Fatalf("default display name = %q, want original filename", got)
	}
}

func TestSimulationCancellableIsLimitedToQueuedAndRunning(t *testing.T) {
	for status, want := range map[domain.SimulationStatus]bool{
		domain.SimulationQueued: true, domain.SimulationRunning: true, domain.SimulationCancelling: false,
		domain.SimulationCancelled: false, domain.SimulationCompleted: false, domain.SimulationFailed: false,
		domain.SimulationFailedRecoverable: false,
	} {
		if got := simulationCancellable(status); got != want {
			t.Fatalf("simulationCancellable(%s) = %t, want %t", status, got, want)
		}
		record := postgres.SimulationRecord{RunID: "run_1", Status: status}
		if got := simulationListItem(record)["cancellable"]; got != want {
			t.Fatalf("list cancellable(%s) = %#v, want %t", status, got, want)
		}
		if got := simulationResponse(record)["cancellable"]; got != want {
			t.Fatalf("detail cancellable(%s) = %#v, want %t", status, got, want)
		}
	}
}
