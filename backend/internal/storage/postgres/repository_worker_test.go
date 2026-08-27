package postgres

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/zx/federated-iot-platform/backend/internal/domain"
	"github.com/zx/federated-iot-platform/backend/internal/parameters"
)

func TestReferenceProfileSeedContainsRequiredActiveGroups(t *testing.T) {
	normalized, sha, err := referenceProfileSeed()
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized) == 0 || domain.SHA256Hex(normalized) != sha {
		t.Fatalf("reference profile seed is empty or has an inconsistent hash: %q", sha)
	}
	if err := validateReferenceProfile(normalized, sha); err != nil {
		t.Fatalf("reference profile seed is not usable: %v", err)
	}
	profile, err := rawJSONObject(normalized, "reference parameter profile")
	if err != nil {
		t.Fatal(err)
	}
	shared := profile["shared_parameters"].(map[string]any)
	expectedGroups := map[string]map[string]any{
		"feature_state": {"nLag": 8, "speed_threshold": 0.01, "current_threshold": 1.0},
		"cleaning":      {"median_window": 21, "mad_factor": 5, "smoothing_window": 5},
		"split":         {"training_ratio": 0.70, "calibration_ratio": 0.15, "minimum_training": 80, "minimum_calibration": 30, "minimum_testing": 30, "agent_count": 3},
		"trend":         {"threshold": 1.0, "maximum_mixing": 0.75, "gain": 1.0, "maximum_step_change": 2.5},
	}
	for group, expected := range expectedGroups {
		actualJSON, err := domain.CanonicalJSON(shared[group])
		if err != nil {
			t.Fatal(err)
		}
		expectedJSON, err := domain.CanonicalJSON(expected)
		if err != nil {
			t.Fatal(err)
		}
		if string(actualJSON) != string(expectedJSON) {
			t.Fatalf("reference group %s = %s, want %s", group, actualJSON, expectedJSON)
		}
	}
	whitelist, ok := profile["fixed_items"].(map[string]any)["agent_override_whitelist"].([]any)
	if !ok || len(whitelist) != 0 {
		t.Fatalf("CUSTOM agent override whitelist changed: %#v", profile["fixed_items"])
	}
}

func TestRerunSourceStatusRequiresTerminalSnapshotSource(t *testing.T) {
	for _, status := range []domain.SimulationStatus{
		domain.SimulationCreated,
		domain.SimulationValidating,
		domain.SimulationQueued,
		domain.SimulationRunning,
		domain.SimulationCancelling,
		domain.SimulationGeneratingArtifacts,
	} {
		err := rerunSourceStatusError(status)
		var stable StableError
		if !errors.As(err, &stable) || stable.Code != "RUN_NOT_RERUNNABLE" || stable.Field != "run_id" {
			t.Fatalf("non-terminal status %q error = %#v, want stable rerun gate", status, err)
		}
	}
	for _, status := range []domain.SimulationStatus{
		domain.SimulationCompleted,
		domain.SimulationCancelled,
		domain.SimulationFailed,
		domain.SimulationFailedRecoverable,
	} {
		if err := rerunSourceStatusError(status); err != nil {
			t.Fatalf("terminal status %q rejected: %v", status, err)
		}
	}
}

func TestCancellationUsesSchedulerSerializedReadCommittedTransaction(t *testing.T) {
	if options := cancellationTxOptions(); options.IsoLevel != pgx.ReadCommitted {
		t.Fatalf("cancellation isolation = %v, want read committed with scheduler serialization", options.IsoLevel)
	}
}

func TestRequiredResultArtifactNamesAreCompleteAndDistinct(t *testing.T) {
	required := requiredResultArtifactNames()
	if len(required) != 12 {
		t.Fatalf("required result artifact count = %d, want 12", len(required))
	}
	seen := make(map[string]bool, len(required))
	for _, name := range required {
		if name == "" || seen[name] {
			t.Fatalf("required result artifacts contain an invalid or duplicate name: %q", name)
		}
		seen[name] = true
	}
	for _, name := range []string{"metrics.csv", "results_agent_1.csv", "results_agent_2.csv", "results_agent_3.csv", "alarms.csv", "artifact_manifest.json"} {
		if !seen[name] {
			t.Fatalf("required result artifact %q is missing", name)
		}
	}
}

func TestParameterConstraintsFileCoversReferenceAndHidesFixedLeaves(t *testing.T) {
	document, err := parameters.LoadFile(filepath.Join("..", "..", "..", "config", "parameter-constraints.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := document.ValidateReference(referenceSharedParameters()); err != nil {
		t.Fatalf("constraints do not fully cover the reference parameter shape: %v", err)
	}
	if got := len(document.Paths); got != 69 {
		t.Fatalf("constraint leaf count = %d, want all 69 reference leaves", got)
	}
	editable := document.EditablePaths()
	if got := len(editable); got != 67 {
		t.Fatalf("editable leaf count = %d, want 67", got)
	}
	for _, fixed := range []string{"split.agent_count", "global_surrogate.leave_one_out"} {
		if document.Paths[fixed].Editable {
			t.Fatalf("fixed S1 leaf %q was exposed as editable", fixed)
		}
		for _, path := range editable {
			if path == fixed {
				t.Fatalf("fixed S1 leaf %q was returned by editable_paths", fixed)
			}
		}
	}
}

func TestReferenceProfileValidationRejectsEmptyActiveGroupWithoutDefaults(t *testing.T) {
	normalized, _, err := referenceProfileSeed()
	if err != nil {
		t.Fatal(err)
	}
	profile, err := rawJSONObject(normalized, "reference parameter profile")
	if err != nil {
		t.Fatal(err)
	}
	profile["shared_parameters"].(map[string]any)["trend"] = map[string]any{}
	invalid, err := domain.CanonicalJSON(profile)
	if err != nil {
		t.Fatal(err)
	}
	err = validateReferenceProfile(invalid, domain.SHA256Hex(invalid))
	var stable StableError
	if !errors.As(err, &stable) || stable.Code != "REFERENCE_CONFIG_IMMUTABLE" {
		t.Fatalf("empty immutable reference profile error = %#v, want stable integrity failure", err)
	}
}

func TestExactCustomProfileValidationAcceptsOnlyEquivalentImmutableProfile(t *testing.T) {
	expected, err := domain.CanonicalJSON(map[string]any{
		"contract_version":  domain.ParameterContractVersion,
		"mode":              domain.RunModeCustom,
		"base_version_id":   referenceProfileVersionID,
		"shared_parameters": map[string]any{"feature_state": map[string]any{"nLag": 8}},
		"agents":            []any{},
		"fixed_items":       map[string]any{"agent_override_whitelist": []any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sha := domain.SHA256Hex(expected)
	base := referenceProfileVersionID
	record := ProfileRecord{Mode: domain.RunModeCustom, BaseVersionID: &base, NormalizedSHA256: sha, Immutable: true}
	if err := validateExactCustomProfile(record, expected, expected, sha, referenceProfileVersionID); err != nil {
		t.Fatalf("equivalent immutable CUSTOM profile was rejected: %v", err)
	}

	for _, testCase := range []struct {
		name   string
		record ProfileRecord
		stored json.RawMessage
	}{
		{name: "different mode", record: ProfileRecord{Mode: domain.RunModeReference, BaseVersionID: &base, NormalizedSHA256: sha, Immutable: true}, stored: expected},
		{name: "different base", record: ProfileRecord{Mode: domain.RunModeCustom, BaseVersionID: stringPointer("other-reference"), NormalizedSHA256: sha, Immutable: true}, stored: expected},
		{name: "different canonical payload", record: record, stored: json.RawMessage(`{"mode":"CUSTOM"}`)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateExactCustomProfile(testCase.record, testCase.stored, expected, sha, referenceProfileVersionID)
			var stable StableError
			if !errors.As(err, &stable) || stable.Code != "REFERENCE_CONFIG_IMMUTABLE" {
				t.Fatalf("non-equivalent normalized profile error = %#v, want stable conflict", err)
			}
		})
	}
}

func TestNormalizedProfileUniqueViolationIsNarrow(t *testing.T) {
	if !isNormalizedProfileUniqueViolation(&pgconn.PgError{Code: "23505", ConstraintName: "parameter_profiles_normalized_sha256_key"}) {
		t.Fatal("normalized hash unique violation was not recognized")
	}
	if isNormalizedProfileUniqueViolation(&pgconn.PgError{Code: "23505", ConstraintName: "parameter_profiles_pkey"}) {
		t.Fatal("version identifier unique violation must not be treated as an idempotent profile match")
	}
}

func TestCustomProfileAliasValidationIsNarrowAndStable(t *testing.T) {
	for _, value := range []string{"", " alias", "alias ", "alias\nname", strings.Repeat("a", 129)} {
		err := validateProfileDisplayName(value)
		var stable StableError
		if !errors.As(err, &stable) || stable.Code != "REQUEST_INVALID" || stable.Field != "display_name" {
			t.Fatalf("display name %q error = %#v, want stable validation error", value, err)
		}
	}
	if err := validateProfileDisplayName("Custom α"); err != nil {
		t.Fatalf("valid UTF-8 CUSTOM alias was rejected: %v", err)
	}
}

func TestWorkerEnvelopeCopiesStoredReferenceParameterGroupsAndHash(t *testing.T) {
	storedProfile, storedSHA, err := referenceProfileSeed()
	if err != nil {
		t.Fatal(err)
	}
	mapping := json.RawMessage(`{"mapping_type":"identity","parameters":{},"result_unit":"A"}`)
	request := domain.CreateSimulationRequest{
		DatasetID: "ds_001", RunMode: domain.RunModeReference,
		ParameterProfileVersionID: referenceProfileVersionID, LoadMappingVersionID: "identity-v1", Seed: 2026,
	}
	snapshot, err := buildSnapshot("run_001", request, strings.Repeat("a", 64), "dataset.csv", "Asia/Shanghai", "Reference-compatible", storedProfile, storedSHA, mapping, domain.SHA256Hex(mapping), RuntimeIdentity{}, referenceTime())
	if err != nil {
		t.Fatal(err)
	}
	snapshotProfile := snapshot["parameter_snapshot"].(json.RawMessage)
	if string(snapshotProfile) != string(storedProfile) || snapshot["parameter_sha256"] != storedSHA {
		t.Fatal("simulation snapshot did not preserve the stored immutable reference profile")
	}
	envelope, err := buildSimulationEnvelope("job_001", "run_001", request, snapshot, "datasets/ds_001/source.csv")
	if err != nil {
		t.Fatal(err)
	}
	workerDataset := envelope["dataset"].(map[string]any)
	if workerDataset["relative_path"] != "datasets/ds_001/source.csv" {
		t.Fatalf("Worker dataset path = %#v, want canonical source path", workerDataset["relative_path"])
	}
	if _, disclosed := snapshot["dataset"].(map[string]any)["relative_path"]; disclosed {
		t.Fatal("simulation product snapshot must not retain the Worker storage path")
	}
	parameter := envelope["parameter_snapshot"].(map[string]any)
	if parameter["version_id"] != referenceProfileVersionID || parameter["sha256"] != storedSHA {
		t.Fatalf("Worker parameter identity = %#v, want stored reference version and hash", parameter)
	}
	stored, err := rawJSONObject(storedProfile, "reference parameter profile")
	if err != nil {
		t.Fatal(err)
	}
	storedShared := stored["shared_parameters"].(map[string]any)
	workerShared := parameter["shared_parameters"].(map[string]any)
	for _, group := range []string{"feature_state", "cleaning", "split", "trend"} {
		storedJSON, err := domain.CanonicalJSON(storedShared[group])
		if err != nil {
			t.Fatal(err)
		}
		workerJSON, err := domain.CanonicalJSON(workerShared[group])
		if err != nil {
			t.Fatal(err)
		}
		if string(workerJSON) != string(storedJSON) {
			t.Fatalf("Worker %s parameters = %s, want stored %s", group, workerJSON, storedJSON)
		}
	}
}

func TestWorkerDatasetEnvelopeUsesSchemaRequiredCanonicalStoragePath(t *testing.T) {
	contents, err := os.ReadFile("../../../../contracts/worker/worker.task.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Definitions map[string]struct {
			Required   []string                   `json:"required"`
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(contents, &schema); err != nil {
		t.Fatal(err)
	}
	datasetDefinition, exists := schema.Definitions["dataset"]
	if !exists || !containsString(datasetDefinition.Required, "relative_path") {
		t.Fatal("worker.task.v1 schema no longer requires dataset.relative_path")
	}
	if _, exists := datasetDefinition.Properties["relative_path"]; !exists {
		t.Fatal("worker.task.v1 schema has no dataset.relative_path definition")
	}
	if _, exists := schema.Definitions["safeRelativePath"]; !exists {
		t.Fatal("worker.task.v1 schema has no safe relative-path definition")
	}

	productSnapshot := map[string]any{
		"dataset_id": "ds_r7", "display_name": "M1 reference full closed loop r7",
		"sha256": strings.Repeat("a", 64), "timezone": "Asia/Shanghai", "columns": domain.RequiredColumns(),
	}
	workerDataset, err := workerDatasetSnapshot(productSnapshot, "datasets/ds_r7/source.csv")
	if err != nil {
		t.Fatalf("canonical stored dataset path was rejected: %v", err)
	}
	if workerDataset["relative_path"] != "datasets/ds_r7/source.csv" {
		t.Fatalf("Worker relative path = %#v, want canonical stored source path", workerDataset["relative_path"])
	}
	if _, disclosed := productSnapshot["relative_path"]; disclosed {
		t.Fatal("product dataset snapshot must not disclose the Worker storage path")
	}
	for _, storageKey := range []string{"", "/datasets/ds_r7/source.csv", "datasets/../ds_r7/source.csv", "datasets/ds_r7/other.csv"} {
		if _, err := workerDatasetSnapshot(productSnapshot, storageKey); err == nil {
			t.Fatalf("unsafe or non-canonical storage key was accepted: %q", storageKey)
		}
	}
}

func TestSimulationSnapshotPreservesCustomSparseAndEffectiveParameters(t *testing.T) {
	shared := referenceSharedParameters()
	shared["feature_state"].(map[string]any)["nLag"] = 9
	agents := []map[string]any{
		{"agent": 1, "segment": "EARLY", "parameters": map[string]any{"feature_state": map[string]any{"nLag": 10}}},
		{"agent": 2, "segment": "MIDDLE", "parameters": map[string]any{}},
		{"agent": 3, "segment": "LATE", "parameters": map[string]any{"trend": map[string]any{"threshold": 1.5}}},
	}
	profile, err := domain.CanonicalJSON(map[string]any{
		"contract_version": domain.ParameterContractVersion, "mode": domain.RunModeCustom, "base_version_id": referenceProfileVersionID,
		"shared_parameters": shared, "agents": agents, "fixed_items": referenceFixedItems(),
	})
	if err != nil {
		t.Fatal(err)
	}
	profileSHA := domain.SHA256Hex(profile)
	mapping := json.RawMessage(`{"mapping_type":"identity","parameters":{},"result_unit":"A"}`)
	request := domain.CreateSimulationRequest{DatasetID: "ds_001", RunMode: domain.RunModeCustom, ParameterProfileVersionID: "pp_custom", LoadMappingVersionID: "identity-v1", Seed: 2026}
	snapshot, err := buildSnapshot("run_002", request, strings.Repeat("b", 64), "dataset.csv", "Asia/Shanghai", "Custom draft name", profile, profileSHA, mapping, domain.SHA256Hex(mapping), RuntimeIdentity{}, referenceTime())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot["parameter_profile_version_id"] != "pp_custom" || snapshot["parameter_profile_display_name"] != "Custom draft name" || snapshot["parameter_sha256"] != profileSHA {
		t.Fatalf("CUSTOM profile identity was not frozen: %#v", snapshot)
	}
	frozenProfile := snapshot["parameter_snapshot"].(json.RawMessage)
	if string(frozenProfile) != string(profile) {
		t.Fatal("simulation snapshot did not preserve the complete stored CUSTOM profile")
	}
	effective := snapshot["parameter_effective"].(map[string]any)
	effectiveAgents := effective["agents"].([]map[string]any)
	if got := effectiveAgents[0]["parameters"].(map[string]any)["feature_state"].(map[string]any)["nLag"]; got != float64(10) {
		t.Fatalf("Agent 1 effective nLag = %#v, want sparse override 10", got)
	}
	if got := effectiveAgents[1]["parameters"].(map[string]any)["feature_state"].(map[string]any)["nLag"]; got != float64(9) {
		t.Fatalf("Agent 2 effective nLag = %#v, want frozen shared value 9", got)
	}
	if got := effectiveAgents[2]["parameters"].(map[string]any)["trend"].(map[string]any)["threshold"]; got != 1.5 {
		t.Fatalf("Agent 3 effective trend threshold = %#v, want sparse override 1.5", got)
	}
	envelope, err := buildSimulationEnvelope("job_002", "run_002", request, snapshot, "datasets/ds_001/source.csv")
	if err != nil {
		t.Fatal(err)
	}
	workerAgents := envelope["parameter_snapshot"].(map[string]any)["agents"].([]any)
	if got := workerAgents[0].(map[string]any)["parameters"].(map[string]any)["feature_state"].(map[string]any)["nLag"]; got != float64(10) {
		t.Fatalf("Worker envelope did not preserve sparse Agent 1 override: %#v", workerAgents)
	}
	snapshotJSON, err := domain.CanonicalJSON(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	datasetID, profileID, mappingID, mode, err := snapshotRerunIdentity(snapshotJSON, "ds_001", "pp_custom", "identity-v1", "CUSTOM")
	if err != nil || datasetID != "ds_001" || profileID != "pp_custom" || mappingID != "identity-v1" || mode != "CUSTOM" {
		t.Fatalf("rerun identity did not default to the frozen template: dataset=%q profile=%q mapping=%q mode=%q err=%v", datasetID, profileID, mappingID, mode, err)
	}
	if seed, err := snapshotMasterSeed(snapshotJSON); err != nil || seed != 2026 {
		t.Fatalf("rerun did not preserve the frozen seed: seed=%d err=%v", seed, err)
	}
}

func referenceTime() time.Time { return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC) }

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func stringPointer(value string) *string { return &value }

func TestBindClaimEnvelopeUsesAttemptScopedControlledPaths(t *testing.T) {
	runID := "run_001"
	for _, testCase := range []struct {
		name     string
		job      LeasedJob
		expected string
	}{
		{
			name:     "simulation",
			job:      LeasedJob{JobType: domain.JobTypeSimulation, RunID: &runID, Envelope: json.RawMessage(`{"output":{"relative_tmp_directory":"runs/run_001/tmp","required_artifact_schema":"artifact.manifest.v1"}}`)},
			expected: "runs/run_001/tmp/attempt_001",
		},
		{
			name:     "preflight",
			job:      LeasedJob{JobType: domain.JobTypePreflight, Envelope: json.RawMessage(`{"dataset":{"dataset_id":"ds_001"},"output":{"relative_tmp_directory":"datasets/ds_001/preflight/tmp","required_summary_schema":"dataset-preflight.summary.v1"}}`)},
			expected: "datasets/ds_001/preflight/tmp/attempt_001",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			job := testCase.job
			if err := bindClaimEnvelope(&job, "attempt_001", "lease-token-012345"); err != nil {
				t.Fatal(err)
			}
			var envelope map[string]any
			if err := json.Unmarshal(job.Envelope, &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope["attempt_id"] != "attempt_001" || envelope["lease_token"] != "lease-token-012345" {
				t.Fatalf("claim identity was not injected: %#v", envelope)
			}
			output := envelope["output"].(map[string]any)
			if output["relative_tmp_directory"] != testCase.expected {
				t.Fatalf("output path = %q, want %q", output["relative_tmp_directory"], testCase.expected)
			}
		})
	}
}

func TestBuildSimulationEnvelopeUsesStrictWorkerSnapshotShape(t *testing.T) {
	request := domain.CreateSimulationRequest{
		DatasetID: "ds_001", RunMode: domain.RunModeReference,
		ParameterProfileVersionID: "reference-v1", LoadMappingVersionID: "identity-v1",
	}
	snapshot := map[string]any{
		"dataset": map[string]any{
			"dataset_id": "ds_001", "display_name": "dataset.csv", "sha256": strings.Repeat("a", 64),
			"timezone": "Asia/Shanghai", "columns": domain.RequiredColumns(),
		},
		"parameter_sha256":        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"mapping_sha256":          "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"parameter_snapshot":      json.RawMessage(`{"contract_version":"parameter-profile.v1","mode":"REFERENCE","shared_parameters":{},"agents":[{"agent":1,"segment":"EARLY","parameters":{}},{"agent":2,"segment":"MIDDLE","parameters":{}},{"agent":3,"segment":"LATE","parameters":{}}],"fixed_items":{}}`),
		"mapping_snapshot":        json.RawMessage(`{"mapping_type":"identity","parameters":{},"result_unit":"A"}`),
		"field_standard_snapshot": map[string]any{},
		"runtime":                 map[string]any{},
	}
	envelope, err := buildSimulationEnvelope("job_001", "run_001", request, snapshot, "datasets/ds_001/source.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, present := envelope["snapshot_sha256"]; present {
		t.Fatal("strict worker.task.v1 envelope must not contain snapshot_sha256")
	}
	parameter := envelope["parameter_snapshot"].(map[string]any)
	if parameter["version_id"] != "reference-v1" || parameter["sha256"] != snapshot["parameter_sha256"] {
		t.Fatalf("unexpected parameter snapshot: %#v", parameter)
	}
	mapping := envelope["mapping_snapshot"].(map[string]any)
	if mapping["version_id"] != "identity-v1" || mapping["sha256"] != snapshot["mapping_sha256"] {
		t.Fatalf("unexpected mapping snapshot: %#v", mapping)
	}
	dataset := envelope["dataset"].(map[string]any)
	if dataset["relative_path"] != "datasets/ds_001/source.csv" || dataset["timezone"] != "Asia/Shanghai" || dataset["sha256"] != strings.Repeat("a", 64) {
		t.Fatalf("Worker dataset does not satisfy the frozen semantic contract: %#v", dataset)
	}
	if columns, ok := dataset["columns"].([]string); !ok || !matchesRequiredDatasetColumns(columns) {
		t.Fatalf("Worker dataset columns do not satisfy the frozen semantic contract: %#v", dataset["columns"])
	}
	encoded, err := domain.CanonicalJSON(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "snapshot_sha256") {
		t.Fatalf("unexpected strict envelope field: %s", encoded)
	}
}
