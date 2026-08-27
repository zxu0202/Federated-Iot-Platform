package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zx/federated-iot-platform/backend/internal/storage/postgres"
)

func TestCustomProfileConflictWritesStableAPIError(t *testing.T) {
	api := &API{}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/parameter-profiles", nil)
	response := httptest.NewRecorder()
	api.writeError(response, request, postgres.StableError{
		Code:        "REFERENCE_CONFIG_IMMUTABLE",
		Field:       "parameter_profile_version_id",
		Message:     "A conflicting immutable parameter profile already exists.",
		Recoverable: false,
	})
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusConflict)
	}
	if strings.Contains(response.Body.String(), "duplicate key") {
		t.Fatalf("API exposed a database conflict: %s", response.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "REFERENCE_CONFIG_IMMUTABLE" {
		t.Fatalf("error code = %q, want stable immutable conflict", body.Error.Code)
	}
}

func TestSimulationParameterSnapshotUsesOnlyFrozenSnapshotMaterial(t *testing.T) {
	snapshot := []byte(`{
        "parameter_profile_version_id":"pp_frozen",
		"parameter_profile_display_name":"Frozen custom alias",
        "parameter_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"dataset":{"dataset_id":"ds_frozen","display_name":"Frozen input","sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","timezone":"Asia/Shanghai"},
        "parameter_snapshot":{"contract_version":"parameter-profile.v1","mode":"CUSTOM","base_version_id":"reference-v1","shared_parameters":{"feature_state":{"nLag":9}},"agents":[{"agent":1,"segment":"EARLY","parameters":{"feature_state":{"nLag":10}}},{"agent":2,"segment":"MIDDLE","parameters":{}},{"agent":3,"segment":"LATE","parameters":{}}],"fixed_items":{"agent_count":3}},
        "parameter_effective":{"shared_parameters":{"feature_state":{"nLag":9}},"agents":[{"agent":1,"segment":"EARLY","parameters":{"feature_state":{"nLag":10}}},{"agent":2,"segment":"MIDDLE","parameters":{"feature_state":{"nLag":9}}},{"agent":3,"segment":"LATE","parameters":{"feature_state":{"nLag":9}}}]}
    }`)
	parameter, ok := simulationParameterSnapshot(snapshot)
	if !ok {
		t.Fatal("frozen simulation parameter snapshot was not projected")
	}
	if parameter["version_id"] != "pp_frozen" || parameter["normalized_sha256"] == "" {
		t.Fatalf("profile identity was not projected from frozen material: %#v", parameter)
	}
	if parameter["shared_parameters"].(map[string]any)["feature_state"].(map[string]any)["nLag"] != float64(9) {
		t.Fatalf("shared parameters were not read from the snapshot: %#v", parameter)
	}
	effective := parameter["effective_parameters"].(map[string]any)["agents"].([]any)
	if effective[0].(map[string]any)["parameters"].(map[string]any)["feature_state"].(map[string]any)["nLag"] != float64(10) {
		t.Fatalf("effective sparse override was not preserved: %#v", parameter)
	}
	record := postgres.SimulationRecord{RunID: "run_frozen", Snapshot: snapshot}
	response := simulationResponse(record)
	if response["parameter_snapshot"].(map[string]any)["display_name"] != "Frozen custom alias" {
		t.Fatalf("simulation detail did not retain the frozen profile alias: %#v", response)
	}
	if response["dataset_snapshot"].(map[string]any)["dataset_id"] != "ds_frozen" {
		t.Fatalf("simulation detail did not retain the frozen dataset snapshot: %#v", response)
	}
}
