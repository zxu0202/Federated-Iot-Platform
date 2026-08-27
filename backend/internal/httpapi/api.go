// Package httpapi exposes the approved S1 Web/API control-plane routes.
package httpapi

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/zx/federated-iot-platform/backend/internal/config"
	"github.com/zx/federated-iot-platform/backend/internal/dataset"
	"github.com/zx/federated-iot-platform/backend/internal/domain"
	"github.com/zx/federated-iot-platform/backend/internal/parameters"
	"github.com/zx/federated-iot-platform/backend/internal/storage/postgres"
)

type API struct {
	config config.Config
	repo   *postgres.Repository
	logger *slog.Logger
}

func New(cfg config.Config, repo *postgres.Repository, logger *slog.Logger) *API {
	return &API{config: cfg, repo: repo, logger: logger}
}

func (api *API) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, route := range api.routes() {
		mux.HandleFunc(route.pattern, route.handler)
	}
	if api.config.StaticRoot != "" {
		mux.Handle("/", spaHandler(api.config.StaticRoot))
	}
	return api.requestID(mux)
}

type apiRoute struct {
	pattern string
	handler http.HandlerFunc
}

// routes is the single route inventory for the frozen API 0.4 operations.
// Keeping this registry separate from implementation tests prevents a newly
// documented endpoint from silently falling through to the SPA handler.
func (api *API) routes() []apiRoute {
	return []apiRoute{
		{"GET /api/v1/health/live", api.live},
		{"GET /api/v1/health/ready", api.ready},
		{"POST /api/v1/datasets", api.createDataset},
		{"GET /api/v1/datasets/{dataset_id}", api.getDataset},
		{"GET /api/v1/configuration/reference-profile", api.getReferenceProfile},
		{"GET /api/v1/parameter-profiles", api.listParameterProfiles},
		{"POST /api/v1/parameter-profiles", api.createParameterProfile},
		{"GET /api/v1/parameter-profiles/{version_id}", api.getParameterProfile},
		{"PATCH /api/v1/parameter-profiles/{version_id}", api.updateParameterProfile},
		{"GET /api/v1/parameter-profiles/{version_id}/export", api.exportParameterProfile},
		{"POST /api/v1/parameter-profiles/import", api.importParameterProfile},
		{"GET /api/v1/load-mapping-profiles", api.listMappingProfiles},
		{"POST /api/v1/load-mapping-profiles", api.createMappingProfile},
		{"GET /api/v1/load-mapping-profiles/{version_id}", api.getMappingProfile},
		{"GET /api/v1/simulations", api.listSimulations},
		{"POST /api/v1/simulations", api.createSimulation},
		{"GET /api/v1/simulations/{run_id}", api.getSimulation},
		{"POST /api/v1/simulations/{run_id}/cancel", api.cancelSimulation},
		{"POST /api/v1/simulations/{run_id}/rerun", api.rerunSimulation},
		{"GET /api/v1/simulations/{run_id}/events", api.simulationEvents},
		{"GET /api/v1/simulations/{run_id}/summary", api.getSimulationSummary},
		{"GET /api/v1/simulations/{run_id}/results", api.getSimulationResults},
		{"GET /api/v1/simulations/{run_id}/alarms", api.getSimulationAlarms},
		{"GET /api/v1/simulations/{run_id}/replay", api.getSimulationReplay},
		{"GET /api/v1/simulations/{run_id}/replay-export", api.exportSimulationReplay},
		{"GET /api/v1/simulations/{run_id}/artifacts", api.listSimulationArtifacts},
		{"GET /api/v1/simulations/{run_id}/artifacts/{artifact_name}", api.downloadSimulationArtifact},
	}
}

func spaHandler(staticRoot string) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writer.Header().Set("Allow", "GET, HEAD")
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		requested := strings.TrimPrefix(filepath.Clean("/"+request.URL.Path), "/")
		candidate := filepath.Join(staticRoot, requested)
		relative, err := filepath.Rel(staticRoot, candidate)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			http.NotFound(writer, request)
			return
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			http.ServeFile(writer, request, candidate)
			return
		}
		index := filepath.Join(staticRoot, "index.html")
		if info, err := os.Stat(index); err != nil || info.IsDir() {
			http.NotFound(writer, request)
			return
		}
		http.ServeFile(writer, request, index)
	})
}

func (api *API) live(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{"status": "alive", "service": "web-api", "version": api.config.ServiceVersion, "time": time.Now().UTC().Format(time.RFC3339Nano)})
}

func (api *API) ready(writer http.ResponseWriter, request *http.Request) {
	checks := map[string]string{"database_profile": "postgres", "schema": "unknown", "dataset_store": "unknown", "artifact_store": "unknown", "reference_profile": "unknown", "parameter_constraints": "unknown", "field_standard": "warning", "network_binding": "ok", "worker": "not_observed"}
	status := http.StatusOK
	if err := api.repo.Ping(request.Context()); err != nil {
		checks["database"] = "failed"
		status = http.StatusServiceUnavailable
	} else {
		checks["database"] = "ok"
		checks["schema"] = "ok"
		if err := api.repo.VerifyReferenceProfile(request.Context()); err != nil {
			checks["reference_profile"] = "failed"
			status = http.StatusServiceUnavailable
		} else {
			checks["reference_profile"] = "ok"
		}
		if err := api.repo.VerifyParameterConstraints(); err != nil {
			checks["parameter_constraints"] = "failed"
			status = http.StatusServiceUnavailable
		} else {
			checks["parameter_constraints"] = "ok"
		}
		observation, err := api.repo.WorkerObservation(request.Context(), workerObservationMaximumAge(api.config.HeartbeatInterval, api.config.LeaseDuration))
		if err != nil {
			checks["worker"] = "failed"
			status = http.StatusServiceUnavailable
		} else {
			checks["worker"] = string(observation.Status)
		}
	}
	if err := ensureDirectory(api.config.DatasetRoot); err != nil {
		checks["dataset_store"] = "failed"
		status = http.StatusServiceUnavailable
	} else {
		checks["dataset_store"] = "ok"
	}
	if err := ensureDirectory(api.config.ArtifactRoot); err != nil {
		checks["artifact_store"] = "failed"
		status = http.StatusServiceUnavailable
	} else {
		checks["artifact_store"] = "ok"
	}
	state := "ready"
	if status != http.StatusOK {
		state = "not_ready"
	}
	writeJSON(writer, status, map[string]any{"status": state, "checks": checks, "worker_contract_version": api.config.WorkerContract})
}

// workerObservationMaximumAge permits three expected heartbeat intervals but
// never permits an observation older than the configured lease boundary.
func workerObservationMaximumAge(interval, lease time.Duration) time.Duration {
	maximumAge := 3 * interval
	if maximumAge <= 0 || maximumAge > lease {
		return lease
	}
	return maximumAge
}

func (api *API) createDataset(writer http.ResponseWriter, request *http.Request) {
	displayName, timezone, originalFilename, filePart, err := readDatasetMultipart(request, api.config.UploadLimitBytes)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	defer filePart.Close()

	datasetID := postgres.NewOpaqueID("ds")
	imported, err := dataset.ImportCSV(filePart, api.config.DatasetRoot, datasetID, timezone, api.config.UploadLimitBytes)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	displayName = effectiveDatasetDisplayName(displayName, originalFilename)
	structural := map[string]any{"raw_rows": imported.RawRows, "invalid_numeric_rows": imported.InvalidNumericRows}
	warnings := make([]map[string]any, 0, 1)
	if imported.NonMonotonicCount > 0 {
		warnings = append(warnings, map[string]any{"code": "TIME_NOT_MONOTONIC", "count": imported.NonMonotonicCount})
	}
	record, jobID, position, err := api.repo.RegisterDataset(request.Context(), postgres.DatasetRegistration{
		DatasetID: datasetID, DisplayName: displayName, OriginalFilename: originalFilename, StorageKey: imported.StorageKey, SHA256: imported.SHA256, SizeBytes: imported.SizeBytes,
		Timezone: imported.Timezone, UTCOffset: imported.UTCOffset, StructuralStatistics: marshalRaw(structural), Warnings: marshalRaw(warnings),
	})
	if err != nil {
		removeDatasetSource(api.config.DatasetRoot, datasetID)
		api.writeError(writer, request, err)
		return
	}
	_ = jobID
	_ = position
	writeJSON(writer, http.StatusAccepted, envelope(request, datasetResponse(record)))
}

func (api *API) getDataset(writer http.ResponseWriter, request *http.Request) {
	record, err := api.repo.GetDataset(request.Context(), request.PathValue("dataset_id"))
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, envelope(request, datasetResponse(record)))
}

// datasetResponse is a product-safe projection. In particular, it deliberately
// excludes storage keys, lease credentials, and database implementation errors.
func datasetResponse(record postgres.DatasetRecord) map[string]any {
	preflight := record.Preflight
	response := map[string]any{
		"dataset_id":              record.DatasetID,
		"display_name":            record.DisplayName,
		"original_filename":       record.OriginalFilename,
		"status":                  record.Status,
		"sha256":                  record.SHA256,
		"size_bytes":              record.SizeBytes,
		"columns":                 domain.RequiredColumns(),
		"timezone":                record.Timezone,
		"utc_offset":              record.UTCOffset,
		"statistics":              rawOrObject(record.StructuralStatistics),
		"preflight":               datasetPreflightResponse(preflight),
		"algorithm_preprocessing": nil,
		"validation_started_at":   timestampOrNil(record.ValidationStartedAt),
		"validation_finished_at":  timestampOrNil(record.ValidationFinishedAt),
		"created_at":              record.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if record.Status == domain.DatasetValid {
		response["algorithm_preprocessing"] = rawOrObject(record.PreflightSummary)
	}
	return response
}

func datasetPreflightResponse(record postgres.DatasetPreflightRecord) map[string]any {
	return map[string]any{
		"job_id":           stringOrNil(record.JobID),
		"status":           stringOrNil(record.Status),
		"queue_position":   integerOrNil(record.QueuePosition),
		"stage":            stringOrNil(record.Stage),
		"attempt_id":       stringOrNil(record.AttemptID),
		"lease_state":      stringOrNil(record.LeaseState),
		"latest_event_id":  integer64OrNil(record.LatestEventID),
		"contract_version": domain.PreflightContractVersion,
		"error":            datasetPreflightError(record.Error, record.JobID),
	}
}

func integerOrNil(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func integer64OrNil(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

// datasetPreflightError accepts only the frozen diagnostic fields from the
// Worker error payload. It never relays an arbitrary database or filesystem
// value to callers.
func datasetPreflightError(raw json.RawMessage, jobID *string) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	code, codeOK := value["code"].(string)
	if !codeOK || code == "" || len(code) > 128 {
		return nil
	}
	message, messageOK := value["message"].(string)
	if !messageOK || message == "" || len(message) > 1024 {
		message = "Dataset preflight failed."
	}
	stage, stageOK := value["stage"].(string)
	if !stageOK || len(stage) > 128 {
		stage = ""
	}
	diagnosticID, diagnosticOK := value["diagnostic_id"].(string)
	if !diagnosticOK || diagnosticID == "" || len(diagnosticID) > 256 {
		if jobID == nil {
			return nil
		}
		diagnosticID = "preflight:" + *jobID + ":" + code
	}
	recoverable, recoverableOK := value["recoverable"].(bool)
	if !recoverableOK {
		recoverable = false
	}
	result := map[string]any{
		"code": code, "message": message, "stage": nil,
		"diagnostic_id": diagnosticID, "recoverable": recoverable,
	}
	if stage != "" {
		result["stage"] = stage
	}
	if agent, ok := safeDiagnosticAgent(value["agent"]); ok {
		result["agent"] = agent
	}
	return result
}

func safeDiagnosticAgent(value any) (int, bool) {
	number, ok := value.(float64)
	if !ok || number != float64(int(number)) {
		return 0, false
	}
	agent := int(number)
	return agent, agent >= 1 && agent <= 3
}

func (api *API) getReferenceProfile(writer http.ResponseWriter, request *http.Request) {
	profile, mapping, err := api.repo.ReferenceProfile(request.Context())
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	constraints, editablePaths, err := api.repo.ParameterConstraints()
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, envelope(request, map[string]any{
		"mode": profile.Mode, "version_id": profile.VersionID, "display_name": profile.DisplayName, "immutable": profile.Immutable, "contract_version": domain.ParameterContractVersion,
		"shared_parameters": rawOrObject(profile.Shared), "agents": rawOrObject(profile.Agents), "fixed_items": rawOrObject(profile.FixedItems), "constraints": constraints, "editable_paths": editablePaths,
		"load_mapping": map[string]any{"version_id": mapping.VersionID, "mapping_type": mapping.MappingType, "display_name": mapping.DisplayName, "result_unit": mapping.ResultUnit, "normalized_sha256": mapping.NormalizedSHA256}, "normalized_sha256": profile.NormalizedSHA256,
		"created_at": profile.CreatedAt.UTC().Format(time.RFC3339Nano), "updated_at": profile.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}))
}

func (api *API) createParameterProfile(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		DisplayName   string                `json:"display_name"`
		BaseVersionID string                `json:"base_version_id"`
		Shared        map[string]any        `json:"shared_parameters"`
		Agents        []profileAgentRequest `json:"agents"`
	}
	if err := decodeJSON(request, &body); err != nil {
		api.writeError(writer, request, err)
		return
	}
	profile, exists, err := api.repo.CreateCustomProfile(request.Context(), postgres.ProfileInput{DisplayName: body.DisplayName, BaseVersionID: body.BaseVersionID, Shared: body.Shared, Agents: profileAgentInputs(body.Agents)})
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	response, err := api.parameterProfileResponse(profile)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	status := http.StatusCreated
	if exists {
		status = http.StatusOK
	}
	writeJSON(writer, status, envelope(request, response))
}

func (api *API) listParameterProfiles(writer http.ResponseWriter, request *http.Request) {
	profiles, err := api.repo.ListParameterProfiles(request.Context(), queryLimit(request))
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	constraints, editablePaths, err := api.repo.ParameterConstraints()
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	items := make([]any, 0, len(profiles))
	for _, profile := range profiles {
		items = append(items, profileResponse(profile, constraints, editablePaths))
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": items, "meta": map[string]any{"request_id": requestID(request), "next_cursor": nil, "has_more": false, "total": len(items)}})
}

func (api *API) getParameterProfile(writer http.ResponseWriter, request *http.Request) {
	profile, err := api.repo.GetParameterProfile(request.Context(), request.PathValue("version_id"))
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	response, err := api.parameterProfileResponse(profile)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, envelope(request, response))
}

func (api *API) updateParameterProfile(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		DisplayName string `json:"display_name"`
	}
	if err := decodeJSON(request, &body); err != nil {
		api.writeError(writer, request, err)
		return
	}
	profile, err := api.repo.UpdateCustomProfileDisplayName(request.Context(), request.PathValue("version_id"), body.DisplayName)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	response, err := api.parameterProfileResponse(profile)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, envelope(request, response))
}

func (api *API) exportParameterProfile(writer http.ResponseWriter, request *http.Request) {
	profile, err := api.repo.GetParameterProfile(request.Context(), request.PathValue("version_id"))
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Content-Disposition", "attachment; filename=parameter-profile-"+profile.VersionID+".json")
	response, err := api.parameterProfileResponse(profile)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	_ = json.NewEncoder(writer).Encode(response)
}

func (api *API) importParameterProfile(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		VersionID        string               `json:"version_id"`
		Mode             domain.RunMode       `json:"mode"`
		DisplayName      string               `json:"display_name"`
		BaseVersionID    string               `json:"base_version_id"`
		ContractVersion  string               `json:"contract_version"`
		Shared           map[string]any       `json:"shared_parameters"`
		Agents           []profileImportAgent `json:"agents"`
		FixedItems       map[string]any       `json:"fixed_items"`
		NormalizedSHA256 string               `json:"normalized_sha256"`
		Immutable        bool                 `json:"immutable"`
		CreatedAt        string               `json:"created_at"`
		UpdatedAt        string               `json:"updated_at"`
		Constraints      json.RawMessage      `json:"constraints"`
		EditablePaths    []string             `json:"editable_paths"`
	}
	if err := decodeJSON(request, &body); err != nil {
		api.writeError(writer, request, err)
		return
	}
	if body.Mode != "" && body.Mode != domain.RunModeReference && body.Mode != domain.RunModeCustom {
		api.writeError(writer, request, postgres.StableError{Code: "REQUEST_INVALID", Field: "mode", Message: "mode must be REFERENCE or CUSTOM.", Recoverable: true})
		return
	}
	profile, exists, err := api.repo.ImportCustomProfile(request.Context(), postgres.ImportProfileInput{
		ProfileInput:    postgres.ProfileInput{DisplayName: body.DisplayName, BaseVersionID: body.BaseVersionID, Shared: body.Shared, Agents: importedProfileAgentInputs(body.Agents)},
		ContractVersion: body.ContractVersion, Mode: body.Mode, FixedItems: body.FixedItems, NormalizedSHA256: body.NormalizedSHA256, Immutable: body.Immutable,
	})
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	status := http.StatusCreated
	if exists {
		status = http.StatusOK
	}
	response, err := api.parameterProfileResponse(profile)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	writeJSON(writer, status, envelope(request, response))
}

func (api *API) listMappingProfiles(writer http.ResponseWriter, request *http.Request) {
	mappings, err := api.repo.ListMappingProfiles(request.Context(), queryLimit(request))
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	items := make([]any, 0, len(mappings))
	for _, mapping := range mappings {
		items = append(items, mappingResponse(mapping))
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": items, "meta": map[string]any{"request_id": requestID(request), "next_cursor": nil, "has_more": false, "total": len(items)}})
}

func (api *API) createMappingProfile(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		DisplayName string         `json:"display_name"`
		MappingType string         `json:"mapping_type"`
		Parameters  map[string]any `json:"parameters"`
		ResultUnit  string         `json:"result_unit"`
	}
	if err := decodeJSON(request, &body); err != nil {
		api.writeError(writer, request, err)
		return
	}
	mapping, exists, err := api.repo.CreateIdentityMapping(request.Context(), postgres.MappingInput{DisplayName: body.DisplayName, MappingType: body.MappingType, Parameters: body.Parameters, ResultUnit: body.ResultUnit})
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	status := http.StatusCreated
	if exists {
		status = http.StatusOK
	}
	writeJSON(writer, status, envelope(request, mappingResponse(mapping)))
}

func (api *API) getMappingProfile(writer http.ResponseWriter, request *http.Request) {
	mapping, err := api.repo.GetMappingProfile(request.Context(), request.PathValue("version_id"))
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, envelope(request, mappingResponse(mapping)))
}

func (api *API) createSimulation(writer http.ResponseWriter, request *http.Request) {
	var body domain.CreateSimulationRequest
	if err := decodeJSON(request, &body); err != nil {
		api.writeError(writer, request, err)
		return
	}
	key := request.Header.Get("Idempotency-Key")
	record, replayed, err := api.repo.CreateSimulation(request.Context(), key, body)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	status := http.StatusAccepted
	if replayed {
		status = http.StatusOK
	}
	data := simulationResponse(record)
	data["links"] = map[string]any{"self": "/api/v1/simulations/" + record.RunID, "events": "/api/v1/simulations/" + record.RunID + "/events"}
	writeJSON(writer, status, envelope(request, data))
}

func (api *API) listSimulations(writer http.ResponseWriter, request *http.Request) {
	query, binding, err := parseSimulationListQuery(request)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	page, err := api.repo.ListSimulations(request.Context(), query)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	items := make([]any, 0, len(page.Items))
	for _, record := range page.Items {
		items = append(items, simulationListItem(record))
	}
	var nextCursor any
	if page.HasMore && len(page.Items) > 0 {
		nextCursor, err = encodeSimulationListCursor(query.View, binding, page.Items[len(page.Items)-1])
		if err != nil {
			api.writeError(writer, request, err)
			return
		}
	}
	writeJSON(writer, http.StatusOK, simulationListResponse(requestID(request), items, nextCursor, page))
}

func simulationListResponse(requestID string, items []any, nextCursor any, page postgres.SimulationPage) map[string]any {
	return map[string]any{
		"data": items,
		"meta": map[string]any{
			"request_id": requestID, "api_version": "v1", "total": page.Total,
			"next_cursor": nextCursor, "has_more": page.HasMore,
		},
	}
}

func (api *API) getSimulation(writer http.ResponseWriter, request *http.Request) {
	record, err := api.repo.GetSimulation(request.Context(), request.PathValue("run_id"))
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	if record.Status == domain.SimulationCompleted {
		if _, err := api.repo.RequireCompletedArtifacts(request.Context(), record.RunID); err != nil {
			api.writeError(writer, request, err)
			return
		}
	}
	writeJSON(writer, http.StatusOK, envelope(request, simulationResponse(record)))
}

func (api *API) rerunSimulation(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		ParameterProfileVersionID string `json:"parameter_profile_version_id"`
	}
	if request.ContentLength != 0 {
		if err := decodeJSON(request, &body); err != nil {
			api.writeError(writer, request, err)
			return
		}
	}
	replayRequest, err := api.repo.RerunRequest(request.Context(), request.PathValue("run_id"), body.ParameterProfileVersionID)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	key := request.Header.Get("Idempotency-Key")
	record, replayed, err := api.repo.CreateSimulation(request.Context(), key, replayRequest)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	status := http.StatusAccepted
	if replayed {
		status = http.StatusOK
	}
	data := simulationResponse(record)
	data["links"] = map[string]any{"self": "/api/v1/simulations/" + record.RunID, "events": "/api/v1/simulations/" + record.RunID + "/events"}
	writeJSON(writer, status, envelope(request, data))
}

func (api *API) getSimulationSummary(writer http.ResponseWriter, request *http.Request) {
	runID := request.PathValue("run_id")
	agent, err := parseSummaryAgent(request.URL.Query().Get("agent"))
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	record, err := api.repo.RequireCompletedArtifacts(request.Context(), runID)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	summary, err := api.readSimulationSummary(request.Context(), record, agent)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, envelope(request, summary))
}

func (api *API) getSimulationResults(writer http.ResponseWriter, request *http.Request) {
	runID := request.PathValue("run_id")
	query, err := parseResultQuery(request, runID)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	record, err := api.repo.RequireCompletedArtifacts(request.Context(), runID)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	page, err := api.readResultPage(request.Context(), record, query)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, envelope(request, page.response()))
}

func (api *API) getSimulationAlarms(writer http.ResponseWriter, request *http.Request) {
	runID := request.PathValue("run_id")
	query, binding, err := parseAlarmQuery(request, runID)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	if _, err := api.repo.RequireCompletedArtifacts(request.Context(), runID); err != nil {
		api.writeError(writer, request, err)
		return
	}
	page, err := api.repo.ListAlarms(request.Context(), query)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	response, err := alarmListResponse(request, runID, query.Agent, binding, page)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func alarmListResponse(request *http.Request, runID string, agent int, binding string, page postgres.AlarmPage) (map[string]any, error) {
	items := make([]any, 0, len(page.Items))
	for _, alarm := range page.Items {
		item := map[string]any{
			"run_id": alarm.RunID, "agent": alarm.Agent, "original_running_index": alarm.OriginalRunningIndex,
			"time": nil, "overall_alarm_level": alarm.OverallAlarmLevel, "alarm_type": alarm.AlarmType,
			"reasons": rawOrObject(alarm.Reasons), "load_status": alarm.LoadStatus,
			// A locator is an API coordinate, not Worker-provided storage metadata.
			// Construct it from the indexed identity so a legacy row can never expose
			// an artifact name, filesystem path, CSV row number, or byte offset.
			"result_locator": map[string]any{"agent": alarm.Agent, "original_running_index": alarm.OriginalRunningIndex},
		}
		if alarm.Time != nil {
			item["time"] = alarm.Time.UTC().Format(time.RFC3339Nano)
		}
		items = append(items, item)
	}
	var nextCursor any
	if page.HasMore && len(page.Items) > 0 {
		var err error
		nextCursor, err = encodeIndexedCursor("alarms.v1", binding, page.Items[len(page.Items)-1].AlarmID)
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"data": map[string]any{
			"run_id": runID, "agent": agent, "items": items, "total": page.Total,
			"next_cursor": nextCursor, "has_more": page.HasMore,
		},
		"meta": map[string]any{"request_id": requestID(request), "api_version": "v1", "total": page.Total},
	}, nil
}

// getSimulationReplay is strictly read-only. It shares the committed point
// artifact reader with results; it neither claims a job nor updates a lease.
func (api *API) getSimulationReplay(writer http.ResponseWriter, request *http.Request) {
	runID := request.PathValue("run_id")
	query, err := parseResultQuery(request, runID)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	record, err := api.repo.RequireCompletedArtifacts(request.Context(), runID)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	page, err := api.readResultPage(request.Context(), record, query)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	response := page.response()
	response["run"] = resultRunIdentity(record)
	response["window_start"] = page.windowStart
	response["window_end"] = page.windowEnd
	response["total_points"] = page.total
	response["points"] = response["items"]
	if parameterSnapshot, ok := simulationParameterSnapshot(record.Snapshot); ok {
		response["parameter_snapshot"] = parameterSnapshot
	}
	if datasetSnapshot, ok := simulationDatasetSnapshot(record.Snapshot); ok {
		response["dataset_snapshot"] = datasetSnapshot
	}
	delete(response, "items")
	writeJSON(writer, http.StatusOK, envelope(request, response))
}

func (api *API) exportSimulationReplay(writer http.ResponseWriter, request *http.Request) {
	runID := request.PathValue("run_id")
	if request.URL.Query().Get("format") != "zip" {
		api.writeError(writer, request, postgres.StableError{Code: "REQUEST_INVALID", Field: "format", Message: "format must be zip.", Recoverable: true})
		return
	}
	agent, err := parseRequiredAgent(request.URL.Query().Get("agent"))
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	record, err := api.repo.RequireCompletedArtifacts(request.Context(), runID)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	artifact, err := api.repo.GetArtifact(request.Context(), runID, fmt.Sprintf("results_agent_%d.csv", agent))
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	file, err := api.openCommittedArtifact(runID, artifact)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	defer file.Close()
	exportedAt := time.Now().UTC()
	manifest := map[string]any{
		"run": resultRunIdentity(record), "agent": agent, "source_name": artifact.Name,
		"source_size_bytes": artifact.SizeBytes, "source_sha256": artifact.SHA256,
		"exported_at": exportedAt.Format(time.RFC3339Nano), "result_schema_version": "point-result.v1",
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	preflightHash := sha256.New()
	countingWriter := &countingWriter{writer: preflightHash}
	if err := writeReplayArchive(countingWriter, file, agent, manifestJSON, exportedAt); err != nil {
		api.writeError(writer, request, artifactIntegrityError("The committed replay artifact could not be exported."))
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		api.writeError(writer, request, artifactIntegrityError("The committed replay artifact could not be rewound."))
		return
	}
	writer.Header().Set("Content-Type", "application/zip")
	writer.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": fmt.Sprintf("replay_%s_agent_%d.zip", runID, agent)}))
	writer.Header().Set("Content-Length", strconv.FormatInt(countingWriter.count, 10))
	writer.Header().Set("Digest", "sha-256="+base64.StdEncoding.EncodeToString(preflightHash.Sum(nil)))
	if err := writeReplayArchive(writer, file, agent, manifestJSON, exportedAt); err != nil && api.logger != nil {
		api.logger.Error("replay export failed", "request_id", requestID(request), "run_id", runID, "error", err.Error())
	}
}

func (api *API) listSimulationArtifacts(writer http.ResponseWriter, request *http.Request) {
	inventory, err := api.repo.ListArtifacts(request.Context(), request.PathValue("run_id"))
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	items := make([]any, 0, len(inventory.Items))
	for _, artifact := range inventory.Items {
		items = append(items, artifactResponse(artifact))
	}
	writeJSON(writer, http.StatusOK, envelope(request, map[string]any{
		"artifact_state": inventory.ArtifactState, "manifest_sha256": inventory.ManifestSHA256, "items": items,
	}))
}

func (api *API) downloadSimulationArtifact(writer http.ResponseWriter, request *http.Request) {
	runID, name := request.PathValue("run_id"), request.PathValue("artifact_name")
	if !safeArtifactName(name) {
		api.writeError(writer, request, postgres.StableError{Code: "ARTIFACT_NOT_FOUND", Field: "artifact_name", Message: "Artifact was not found in the committed manifest."})
		return
	}
	artifact, err := api.repo.GetArtifact(request.Context(), runID, name)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	file, err := api.openCommittedArtifact(runID, artifact)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	defer file.Close()
	writer.Header().Set("Content-Type", safeMediaType(artifact.MediaType))
	writer.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": artifact.Name}))
	writer.Header().Set("Content-Length", strconv.FormatInt(artifact.SizeBytes, 10))
	writer.Header().Set("X-Content-SHA256", artifact.SHA256)
	_, _ = io.CopyBuffer(writer, file, make([]byte, 32*1024))
}

func (api *API) cancelSimulation(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	if request.ContentLength != 0 {
		if err := decodeJSON(request, &body); err != nil {
			api.writeError(writer, request, err)
			return
		}
	}
	record, err := api.repo.CancelSimulation(request.Context(), request.PathValue("run_id"), body.Reason)
	if err != nil {
		api.writeError(writer, request, err)
		return
	}
	writeJSON(writer, cancellationHTTPStatus(record.Status), envelope(request, simulationResponse(record)))
}

func cancellationHTTPStatus(status domain.SimulationStatus) int {
	if status == domain.SimulationCancelling {
		return http.StatusAccepted
	}
	return http.StatusOK
}

func (api *API) simulationEvents(writer http.ResponseWriter, request *http.Request) {
	runID := request.PathValue("run_id")
	lastID, err := parseLastEventID(request.Header.Get("Last-Event-ID"))
	if err != nil {
		api.writeError(writer, request, postgres.StableError{Code: "REQUEST_INVALID", Field: "Last-Event-ID", Message: "Last-Event-ID must be a non-negative integer.", Recoverable: true})
		return
	}
	if _, err := api.repo.GetSimulation(request.Context(), runID); err != nil {
		api.writeError(writer, request, err)
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := writer.(http.Flusher)
	if !ok {
		api.writeError(writer, request, errors.New("streaming is not supported by this server"))
		return
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		reset, events, latest, err := api.repo.EventsAfter(request.Context(), runID, lastID, 100)
		if err != nil {
			return
		}
		if reset {
			writeSSE(writer, latest, "stream.reset", map[string]any{"run_id": runID, "reason": "event_retention_expired", "latest_event_id": latest, "occurred_at": time.Now().UTC().Format(time.RFC3339Nano)})
			flusher.Flush()
			lastID = latest
		}
		for _, event := range events {
			writeSSE(writer, event.EventID, event.EventType, rawOrObject(event.Payload))
			lastID = event.EventID
		}
		if len(events) > 0 {
			flusher.Flush()
		}
		select {
		case <-request.Context().Done():
			return
		case <-heartbeat.C:
			writeSSE(writer, latest, "heartbeat", map[string]any{"run_id": runID, "latest_event_id": latest, "server_time": time.Now().UTC().Format(time.RFC3339Nano)})
			flusher.Flush()
		case <-ticker.C:
		}
	}
}

var trustedRequestID = regexp.MustCompile(`^req_[A-Za-z0-9_-]+$`)

func (api *API) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		values := request.Header.Values("X-Request-ID")
		requestID := ""
		if len(values) == 1 && len(values[0]) <= 128 && trustedRequestID.MatchString(values[0]) {
			requestID = values[0]
		}
		if requestID == "" {
			requestID = postgres.NewOpaqueID("req")
		}
		writer.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), requestIDKey{}, requestID)))
	})
}

type requestIDKey struct{}

func requestID(request *http.Request) string {
	if value, ok := request.Context().Value(requestIDKey{}).(string); ok {
		return value
	}
	return "unknown"
}

func envelope(request *http.Request, data any) map[string]any {
	return map[string]any{"data": data, "meta": map[string]any{"request_id": requestID(request), "api_version": "v1"}}
}

func (api *API) writeError(writer http.ResponseWriter, request *http.Request, err error) {
	var stable postgres.StableError
	if errors.As(err, &stable) {
		status := statusForCode(stable.Code)
		writeJSON(writer, status, map[string]any{"error": map[string]any{"code": stable.Code, "message": stable.Message, "field": stable.Field, "recoverable": stable.Recoverable}, "request_id": requestID(request)})
		return
	}
	var input dataset.Error
	if errors.As(err, &input) {
		writeJSON(writer, statusForCode(input.Code), map[string]any{"error": map[string]any{"code": input.Code, "message": input.Message, "field": input.Field, "recoverable": true}, "request_id": requestID(request)})
		return
	}
	api.logger.Error("request failed", "request_id", requestID(request), "error", err.Error())
	writeJSON(writer, http.StatusInternalServerError, map[string]any{"error": map[string]any{"code": "INTERNAL_ERROR", "message": "The request could not be completed.", "recoverable": false}, "request_id": requestID(request)})
}

func statusForCode(code string) int {
	switch code {
	case "UPLOAD_TOO_LARGE":
		return http.StatusRequestEntityTooLarge
	case "CSV_HEADER_MISMATCH", "CSV_DUPLICATE_COLUMN", "CSV_EMPTY", "CSV_PARSE_FAILED", "TIME_PARSE_FAILED", "PARAMETER_NOT_ALLOWED", "PARAMETER_OUT_OF_RANGE", "PARAMETER_COMBINATION_INVALID", "AGENT_OVERRIDE_NOT_ALLOWED", "REQUEST_INVALID":
		return http.StatusUnprocessableEntity
	case "DATASET_NOT_FOUND", "RUN_NOT_FOUND", "PROFILE_NOT_FOUND", "ARTIFACT_NOT_FOUND":
		return http.StatusNotFound
	case "PARAMETER_CONSTRAINTS_INVALID":
		return http.StatusServiceUnavailable
	case "QUEUE_FULL", "PREFLIGHT_QUEUE_FULL", "IDEMPOTENCY_CONFLICT", "DATASET_NOT_VALID", "REFERENCE_CONFIG_IMMUTABLE", "RUN_NOT_CANCELLABLE", "RUN_NOT_RERUNNABLE", "RESULT_NOT_READY", "ARTIFACT_NOT_AVAILABLE", "ARTIFACT_INTEGRITY_ERROR":
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func readDatasetMultipart(request *http.Request, maxBytes int64) (string, string, string, io.ReadCloser, error) {
	reader, err := request.MultipartReader()
	if err != nil {
		return "", "", "", nil, postgres.StableError{Code: "REQUEST_INVALID", Field: "Content-Type", Message: "Dataset upload must use multipart/form-data.", Recoverable: true}
	}
	var displayName, timezone string
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return "", "", "", nil, dataset.Error{Code: "CSV_PARSE_FAILED", Field: "file", Message: "Multipart upload could not be read."}
		}
		switch part.FormName() {
		case "display_name":
			contents, err := io.ReadAll(io.LimitReader(part, 1025))
			part.Close()
			if err != nil || len(contents) > 1024 {
				return "", "", "", nil, postgres.StableError{Code: "REQUEST_INVALID", Field: "display_name", Message: "display_name is invalid.", Recoverable: true}
			}
			displayName = strings.TrimSpace(string(contents))
		case "timezone":
			contents, err := io.ReadAll(io.LimitReader(part, 129))
			part.Close()
			if err != nil || len(contents) > 128 {
				return "", "", "", nil, postgres.StableError{Code: "REQUEST_INVALID", Field: "timezone", Message: "timezone is invalid.", Recoverable: true}
			}
			timezone = strings.TrimSpace(string(contents))
		case "file":
			originalFilename, filenameErr := safeOriginalFilename(part.FileName())
			if filenameErr != nil {
				part.Close()
				return "", "", "", nil, filenameErr
			}
			// UI sends scalar fields before the file. A bounded staging mechanism is
			// intentionally deferred to avoid buffering large client files in memory.
			return displayName, timezone, originalFilename, &limitedPart{ReadCloser: part, remaining: maxBytes + 1}, nil
		default:
			part.Close()
		}
	}
	return "", "", "", nil, postgres.StableError{Code: "REQUEST_INVALID", Field: "file", Message: "file is required.", Recoverable: true}
}

// safeOriginalFilename retains only a client-safe basename. It is metadata for
// people, never a storage address or a path used by the Worker.
func safeOriginalFilename(value string) (string, error) {
	basename := value
	if index := strings.LastIndexAny(basename, `/\\`); index >= 0 {
		basename = basename[index+1:]
	}
	if !utf8.ValidString(basename) || utf8.RuneCountInString(basename) < 1 || utf8.RuneCountInString(basename) > 255 || basename == "." || basename == ".." {
		return "", postgres.StableError{Code: "REQUEST_INVALID", Field: "file", Message: "file name is invalid.", Recoverable: true}
	}
	for _, character := range basename {
		if unicode.IsControl(character) || character == '/' || character == '\\' {
			return "", postgres.StableError{Code: "REQUEST_INVALID", Field: "file", Message: "file name is invalid.", Recoverable: true}
		}
	}
	return basename, nil
}

func effectiveDatasetDisplayName(displayName, originalFilename string) string {
	if displayName == "" {
		return originalFilename
	}
	return displayName
}

type limitedPart struct {
	io.ReadCloser
	remaining int64
}

func (part *limitedPart) Read(target []byte) (int, error) {
	if part.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(target)) > part.remaining {
		target = target[:int(part.remaining)]
	}
	read, err := part.ReadCloser.Read(target)
	part.remaining -= int64(read)
	return read, err
}

func ensureDirectory(root string) error {
	if err := os.MkdirAll(root, 0o750); err != nil {
		return err
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("storage root is not a directory")
	}
	return nil
}

func removeDatasetSource(root, datasetID string) {
	dataset.RemoveImportedDataset(root, datasetID)
}

func decodeJSON(request *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return postgres.StableError{Code: "REQUEST_INVALID", Message: "Request JSON is invalid.", Recoverable: true}
	}
	if decoder.More() {
		return postgres.StableError{Code: "REQUEST_INVALID", Message: "Request JSON must contain one object.", Recoverable: true}
	}
	return nil
}

func writeJSON(writer http.ResponseWriter, status int, body any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(body)
}

func marshalRaw(value any) json.RawMessage { encoded, _ := json.Marshal(value); return encoded }

func rawOrObject(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return nil
	}
	return decoded
}

func parseLastEventID(raw string) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	return strconv.ParseInt(raw, 10, 64)
}

type simulationListCursorWire struct {
	Version         int    `json:"v"`
	Binding         string `json:"binding"`
	View            string `json:"view"`
	CreatedAt       string `json:"created_at"`
	RunID           string `json:"run_id"`
	QueueBucket     int    `json:"queue_bucket"`
	EnqueueSequence int64  `json:"enqueue_sequence"`
}

func parseSimulationListQuery(request *http.Request) (postgres.SimulationListQuery, string, error) {
	values := request.URL.Query()
	query := postgres.SimulationListQuery{View: values.Get("view"), RunID: strings.TrimSpace(values.Get("run_id")), DatasetID: strings.TrimSpace(values.Get("dataset_id")), ParameterProfileVersionID: strings.TrimSpace(values.Get("parameter_profile_version_id")), Search: strings.TrimSpace(values.Get("search"))}
	if query.View == "" {
		query.View = "history"
	}
	if query.View != "history" && query.View != "queue" {
		return postgres.SimulationListQuery{}, "", invalidQuery("view", "view must be history or queue.")
	}
	if len(query.Search) > 256 || strings.IndexByte(query.Search, 0) >= 0 {
		return postgres.SimulationListQuery{}, "", invalidQuery("search", "search is invalid.")
	}
	limit, err := strictLimit(values.Get("limit"), 100, 500)
	if err != nil {
		return postgres.SimulationListQuery{}, "", err
	}
	query.Limit = limit
	if raw := values.Get("status"); raw != "" {
		status := domain.SimulationStatus(raw)
		if !knownSimulationStatus(status) {
			return postgres.SimulationListQuery{}, "", invalidQuery("status", "status is invalid.")
		}
		query.Status = &status
	}
	if raw := values.Get("run_mode"); raw != "" {
		mode := domain.RunMode(raw)
		if mode != domain.RunModeReference && mode != domain.RunModeCustom {
			return postgres.SimulationListQuery{}, "", invalidQuery("run_mode", "run_mode must be REFERENCE or CUSTOM.")
		}
		query.RunMode = &mode
	}
	if query.CreatedFrom, err = parseOptionalTime(values.Get("date_from"), "date_from"); err != nil {
		return postgres.SimulationListQuery{}, "", err
	}
	if query.CreatedTo, err = parseOptionalTime(values.Get("date_to"), "date_to"); err != nil {
		return postgres.SimulationListQuery{}, "", err
	}
	if query.CreatedFrom != nil && query.CreatedTo != nil && query.CreatedFrom.After(*query.CreatedTo) {
		return postgres.SimulationListQuery{}, "", invalidQuery("date_to", "date_to must not precede date_from.")
	}
	binding, err := simulationListBinding(query)
	if err != nil {
		return postgres.SimulationListQuery{}, "", err
	}
	if raw := values.Get("cursor"); raw != "" {
		cursor, err := decodeSimulationListCursor(raw, query.View, binding)
		if err != nil {
			return postgres.SimulationListQuery{}, "", err
		}
		query.Cursor = &cursor
	}
	return query, binding, nil
}

func simulationListBinding(query postgres.SimulationListQuery) (string, error) {
	var status, mode, from, to string
	if query.Status != nil {
		status = string(*query.Status)
	}
	if query.RunMode != nil {
		mode = string(*query.RunMode)
	}
	if query.CreatedFrom != nil {
		from = query.CreatedFrom.UTC().Format(time.RFC3339Nano)
	}
	if query.CreatedTo != nil {
		to = query.CreatedTo.UTC().Format(time.RFC3339Nano)
	}
	payload, err := domain.CanonicalJSON(map[string]any{
		"view": query.View, "run_id": query.RunID, "status": status, "dataset_id": query.DatasetID,
		"parameter_profile_version_id": query.ParameterProfileVersionID, "run_mode": mode, "search": query.Search,
		"date_from": from, "date_to": to,
	})
	if err != nil {
		return "", err
	}
	return domain.SHA256Hex(payload), nil
}

func encodeSimulationListCursor(view, binding string, record postgres.SimulationRecord) (string, error) {
	bucket := 1
	if record.Status == domain.SimulationRunning || record.Status == domain.SimulationCancelling || record.Status == domain.SimulationGeneratingArtifacts {
		bucket = 0
	}
	payload, err := json.Marshal(simulationListCursorWire{
		Version: 1, Binding: binding, View: view, CreatedAt: record.CreatedAt.UTC().Format(time.RFC3339Nano), RunID: record.RunID,
		QueueBucket: bucket, EnqueueSequence: record.EnqueueSequence,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeSimulationListCursor(raw, view, binding string) (postgres.SimulationListCursor, error) {
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return postgres.SimulationListCursor{}, invalidQuery("cursor", "cursor is invalid.")
	}
	var cursor simulationListCursorWire
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.Version != 1 || cursor.View != view || cursor.Binding != binding || cursor.RunID == "" || cursor.QueueBucket < 0 || cursor.QueueBucket > 1 || cursor.EnqueueSequence < 0 {
		return postgres.SimulationListCursor{}, invalidQuery("cursor", "cursor is invalid for this query.")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, cursor.CreatedAt)
	if err != nil {
		return postgres.SimulationListCursor{}, invalidQuery("cursor", "cursor is invalid.")
	}
	return postgres.SimulationListCursor{CreatedAt: createdAt, RunID: cursor.RunID, QueueBucket: cursor.QueueBucket, EnqueueSequence: cursor.EnqueueSequence}, nil
}

type indexedCursorWire struct {
	Version int    `json:"v"`
	Scope   string `json:"scope"`
	Binding string `json:"binding"`
	Value   int64  `json:"value"`
}

func encodeIndexedCursor(scope, binding string, value int64) (string, error) {
	payload, err := json.Marshal(indexedCursorWire{Version: 1, Scope: scope, Binding: binding, Value: value})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeIndexedCursor(raw, scope, binding string) (int64, error) {
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0, invalidQuery("cursor", "cursor is invalid.")
	}
	var cursor indexedCursorWire
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.Version != 1 || cursor.Scope != scope || cursor.Binding != binding || cursor.Value < 0 {
		return 0, invalidQuery("cursor", "cursor is invalid for this query.")
	}
	return cursor.Value, nil
}

func knownSimulationStatus(status domain.SimulationStatus) bool {
	switch status {
	case domain.SimulationCreated, domain.SimulationValidating, domain.SimulationQueued, domain.SimulationRunning, domain.SimulationCancelling, domain.SimulationGeneratingArtifacts, domain.SimulationCompleted, domain.SimulationCancelled, domain.SimulationFailed, domain.SimulationFailedRecoverable:
		return true
	default:
		return false
	}
}

func invalidQuery(field, message string) postgres.StableError {
	return postgres.StableError{Code: "REQUEST_INVALID", Field: field, Message: message, Recoverable: true}
}

func strictLimit(raw string, fallback, maximum int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > maximum {
		return 0, invalidQuery("limit", fmt.Sprintf("limit must be between 1 and %d.", maximum))
	}
	return value, nil
}

func parseOptionalTime(raw, field string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil, invalidQuery(field, field+" must be an ISO-8601 timestamp with timezone.")
	}
	return &value, nil
}

func parseNonNegativeInt64(raw, field string) (*int64, error) {
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return nil, invalidQuery(field, field+" must be a non-negative integer.")
	}
	return &value, nil
}

func parseRequiredAgent(raw string) (int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > 3 {
		return 0, invalidQuery("agent", "agent must be 1, 2, or 3.")
	}
	return value, nil
}

func parseSummaryAgent(raw string) (any, error) {
	if raw == "" || raw == "aggregate" {
		return "aggregate", nil
	}
	return parseRequiredAgent(raw)
}

type resultQuery struct {
	RunID     string
	Agent     int
	Limit     int
	IndexFrom *int64
	IndexTo   *int64
	From      *time.Time
	To        *time.Time
	After     *int64
	Sort      string
	Binding   string
}

func parseResultQuery(request *http.Request, runID string) (resultQuery, error) {
	values := request.URL.Query()
	agent, err := parseRequiredAgent(values.Get("agent"))
	if err != nil {
		return resultQuery{}, err
	}
	limit, err := strictLimit(values.Get("limit"), 200, 2000)
	if err != nil {
		return resultQuery{}, err
	}
	query := resultQuery{RunID: runID, Agent: agent, Limit: limit, Sort: values.Get("sort")}
	if query.Sort == "" {
		query.Sort = "index_asc"
	}
	if query.Sort != "index_asc" && query.Sort != "index_desc" {
		return resultQuery{}, invalidQuery("sort", "sort must be index_asc or index_desc.")
	}
	if query.IndexFrom, err = parseNonNegativeInt64(values.Get("index_from"), "index_from"); err != nil {
		return resultQuery{}, err
	}
	if query.IndexTo, err = parseNonNegativeInt64(values.Get("index_to"), "index_to"); err != nil {
		return resultQuery{}, err
	}
	if query.IndexFrom != nil && query.IndexTo != nil && *query.IndexFrom > *query.IndexTo {
		return resultQuery{}, invalidQuery("index_to", "index_to must not precede index_from.")
	}
	if query.From, err = parseOptionalTime(values.Get("from"), "from"); err != nil {
		return resultQuery{}, err
	}
	if query.To, err = parseOptionalTime(values.Get("to"), "to"); err != nil {
		return resultQuery{}, err
	}
	if query.From != nil && query.To != nil && query.From.After(*query.To) {
		return resultQuery{}, invalidQuery("to", "to must not precede from.")
	}
	binding, err := resultQueryBinding(query)
	if err != nil {
		return resultQuery{}, err
	}
	query.Binding = binding
	if raw := values.Get("cursor"); raw != "" {
		after, err := decodeIndexedCursor(raw, "results.v1", binding)
		if err != nil {
			return resultQuery{}, err
		}
		query.After = &after
	}
	return query, nil
}

func resultQueryBinding(query resultQuery) (string, error) {
	var from, to string
	if query.From != nil {
		from = query.From.UTC().Format(time.RFC3339Nano)
	}
	if query.To != nil {
		to = query.To.UTC().Format(time.RFC3339Nano)
	}
	var indexFrom, indexTo any
	if query.IndexFrom != nil {
		indexFrom = *query.IndexFrom
	}
	if query.IndexTo != nil {
		indexTo = *query.IndexTo
	}
	payload, err := domain.CanonicalJSON(map[string]any{
		"run_id": query.RunID, "agent": query.Agent, "index_from": indexFrom, "index_to": indexTo,
		"from": from, "to": to, "sort": query.Sort,
	})
	if err != nil {
		return "", err
	}
	return domain.SHA256Hex(payload), nil
}

func parseAlarmQuery(request *http.Request, runID string) (postgres.AlarmQuery, string, error) {
	values := request.URL.Query()
	agent, err := parseRequiredAgent(values.Get("agent"))
	if err != nil {
		return postgres.AlarmQuery{}, "", err
	}
	limit, err := strictLimit(values.Get("limit"), 100, 500)
	if err != nil {
		return postgres.AlarmQuery{}, "", err
	}
	query := postgres.AlarmQuery{RunID: runID, Agent: agent, Limit: limit, Levels: values["level"], Types: values["type"]}
	for _, level := range query.Levels {
		if level != "None" && level != "Notice" && level != "Warning" && level != "Alarm" {
			return postgres.AlarmQuery{}, "", invalidQuery("level", "level is invalid.")
		}
	}
	for _, kind := range query.Types {
		if strings.TrimSpace(kind) == "" || len(kind) > 128 {
			return postgres.AlarmQuery{}, "", invalidQuery("type", "type is invalid.")
		}
	}
	if query.From, err = parseOptionalTime(values.Get("from"), "from"); err != nil {
		return postgres.AlarmQuery{}, "", err
	}
	if query.To, err = parseOptionalTime(values.Get("to"), "to"); err != nil {
		return postgres.AlarmQuery{}, "", err
	}
	if query.From != nil && query.To != nil && query.From.After(*query.To) {
		return postgres.AlarmQuery{}, "", invalidQuery("to", "to must not precede from.")
	}
	if query.IndexFrom, err = parseNonNegativeInt64(values.Get("index_from"), "index_from"); err != nil {
		return postgres.AlarmQuery{}, "", err
	}
	if query.IndexTo, err = parseNonNegativeInt64(values.Get("index_to"), "index_to"); err != nil {
		return postgres.AlarmQuery{}, "", err
	}
	if query.IndexFrom != nil && query.IndexTo != nil && *query.IndexFrom > *query.IndexTo {
		return postgres.AlarmQuery{}, "", invalidQuery("index_to", "index_to must not precede index_from.")
	}
	binding, err := alarmQueryBinding(query)
	if err != nil {
		return postgres.AlarmQuery{}, "", err
	}
	if raw := values.Get("cursor"); raw != "" {
		after, err := decodeIndexedCursor(raw, "alarms.v1", binding)
		if err != nil {
			return postgres.AlarmQuery{}, "", err
		}
		query.AfterID = after
	}
	return query, binding, nil
}

func alarmQueryBinding(query postgres.AlarmQuery) (string, error) {
	var from, to string
	if query.From != nil {
		from = query.From.UTC().Format(time.RFC3339Nano)
	}
	if query.To != nil {
		to = query.To.UTC().Format(time.RFC3339Nano)
	}
	var indexFrom, indexTo any
	if query.IndexFrom != nil {
		indexFrom = *query.IndexFrom
	}
	if query.IndexTo != nil {
		indexTo = *query.IndexTo
	}
	payload, err := domain.CanonicalJSON(map[string]any{
		"run_id": query.RunID, "agent": query.Agent, "levels": query.Levels, "types": query.Types,
		"from": from, "to": to, "index_from": indexFrom, "index_to": indexTo,
	})
	if err != nil {
		return "", err
	}
	return domain.SHA256Hex(payload), nil
}

type resultPage struct {
	runID       string
	agent       int
	artifactSHA string
	items       []map[string]any
	hasMore     bool
	nextCursor  any
	total       int64
	windowStart any
	windowEnd   any
}

func (page resultPage) response() map[string]any {
	return map[string]any{
		"run_id": page.runID, "agent": page.agent, "result_schema_version": "point-result.v1",
		"artifact_sha256": page.artifactSHA, "items": page.items, "next_cursor": page.nextCursor,
		"has_more": page.hasMore, "total": page.total,
	}
}

// readResultPage streams the single registered Agent artifact. It bounds JSON
// response memory to the requested page even when the Worker result CSV is
// large, and verifies each source against its database manifest before use.
func (api *API) readResultPage(ctx context.Context, record postgres.SimulationRecord, query resultQuery) (resultPage, error) {
	artifact, err := api.repo.GetArtifact(ctx, record.RunID, fmt.Sprintf("results_agent_%d.csv", query.Agent))
	if err != nil {
		return resultPage{}, err
	}
	file, err := api.openCommittedArtifact(record.RunID, artifact)
	if err != nil {
		return resultPage{}, err
	}
	defer file.Close()
	return readResultCSVPage(file, record.RunID, query, artifact.SHA256)
}

// readResultCSVPage retains the bounded streaming behavior of the HTTP reader
// while making its CSV parsing directly testable with Worker-compatible files.
func readResultCSVPage(source io.Reader, runID string, query resultQuery, artifactSHA string) (resultPage, error) {
	reader := csv.NewReader(source)
	reader.ReuseRecord = true
	header, err := readImmutableCSVHeader(reader)
	if err != nil {
		return resultPage{}, artifactIntegrityError("The committed results artifact has no CSV header.")
	}
	columns := make(map[string]int, len(header))
	for index, name := range header {
		if name == "" || strings.Contains(name, "\x00") {
			return resultPage{}, artifactIntegrityError("The committed results artifact has an invalid CSV header.")
		}
		if _, exists := columns[name]; exists {
			return resultPage{}, artifactIntegrityError("The committed results artifact has duplicate CSV columns.")
		}
		columns[name] = index
	}
	indexColumn, indexOK := columns["OriginalRunningIndex"]
	timeColumn, timeOK := columns["Time"]
	if !indexOK || !timeOK {
		return resultPage{}, artifactIntegrityError("The committed results artifact is missing result identity columns.")
	}
	page := resultPage{runID: runID, agent: query.Agent, artifactSHA: artifactSHA, items: make([]map[string]any, 0, query.Limit)}
	var descendingRing []resultPoint
	for {
		row, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil || len(row) != len(header) {
			return resultPage{}, artifactIntegrityError("The committed results artifact contains an invalid CSV row.")
		}
		point, err := resultPointFromCSV(header, row, indexColumn, timeColumn, query.Agent)
		if err != nil {
			return resultPage{}, err
		}
		if !matchesResultFilter(point, query) {
			continue
		}
		page.total++
		if query.Sort == "index_asc" {
			if query.After != nil && point.index <= *query.After {
				continue
			}
			if len(page.items) < query.Limit {
				page.items = append(page.items, point.value)
			} else {
				page.hasMore = true
			}
			continue
		}
		if query.After != nil && point.index >= *query.After {
			continue
		}
		if len(descendingRing) == query.Limit {
			descendingRing = descendingRing[1:]
			page.hasMore = true
		}
		descendingRing = append(descendingRing, point)
	}
	if query.Sort == "index_desc" {
		for index := len(descendingRing) - 1; index >= 0; index-- {
			page.items = append(page.items, descendingRing[index].value)
		}
	}
	if len(page.items) > 0 {
		first := page.items[0]["OriginalRunningIndex"]
		last := page.items[len(page.items)-1]["OriginalRunningIndex"]
		page.windowStart, page.windowEnd = first, last
		lastIndex, ok := jsonInteger(last)
		if !ok {
			return resultPage{}, artifactIntegrityError("The committed results artifact has an invalid result index.")
		}
		if page.hasMore {
			page.nextCursor, err = encodeIndexedCursor("results.v1", query.Binding, lastIndex)
			if err != nil {
				return resultPage{}, err
			}
		}
	}
	return page, nil
}

type resultPoint struct {
	index int64
	time  time.Time
	value map[string]any
}

func resultPointFromCSV(header, row []string, indexColumn, timeColumn, expectedAgent int) (resultPoint, error) {
	index, err := strconv.ParseInt(row[indexColumn], 10, 64)
	if err != nil || index < 0 {
		return resultPoint{}, artifactIntegrityError("The committed results artifact has an invalid result index.")
	}
	pointTime, err := parseResultTimestamp(row[timeColumn])
	if err != nil {
		return resultPoint{}, artifactIntegrityError("The committed results artifact has an invalid result timestamp.")
	}
	value := make(map[string]any, len(header))
	for index, name := range header {
		value[name] = resultScalar(row[index])
	}
	if rawAgent, present := value["Agent"]; present {
		agent, ok := jsonInteger(rawAgent)
		if !ok || agent != int64(expectedAgent) {
			return resultPoint{}, artifactIntegrityError("The committed results artifact has an invalid Agent value.")
		}
	}
	return resultPoint{index: index, time: pointTime, value: value}, nil
}

// Result Time preserves the source field, whose legacy format is accepted by
// Worker preprocessing. Filtering uses the same known layouts and assigns the
// fixed S1 timezone only when the source has no explicit offset.
func parseResultTimestamp(raw string) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed, nil
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.Time{}, err
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999", "2006-01-02 15:04:05", "02-Jan-2006 15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, raw, location); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported result timestamp")
}

func matchesResultFilter(point resultPoint, query resultQuery) bool {
	if query.IndexFrom != nil && point.index < *query.IndexFrom {
		return false
	}
	if query.IndexTo != nil && point.index > *query.IndexTo {
		return false
	}
	if query.From != nil && point.time.Before(*query.From) {
		return false
	}
	if query.To != nil && point.time.After(*query.To) {
		return false
	}
	return true
}

func resultScalar(raw string) any {
	if raw == "" {
		return nil
	}
	if raw == "true" {
		return true
	}
	if raw == "false" {
		return false
	}
	if integer, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return integer
	}
	if decimal, err := strconv.ParseFloat(raw, 64); err == nil && !strings.EqualFold(raw, "nan") && !strings.Contains(strings.ToLower(raw), "inf") {
		return decimal
	}
	return raw
}

func jsonInteger(value any) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case float64:
		return int64(typed), typed == float64(int64(typed))
	default:
		return 0, false
	}
}

func artifactIntegrityError(message string) postgres.StableError {
	return postgres.StableError{Code: "ARTIFACT_INTEGRITY_ERROR", Message: message, Recoverable: false}
}

func artifactResponse(artifact postgres.ArtifactRecord) map[string]any {
	return map[string]any{
		"name": artifact.Name, "media_type": safeMediaType(artifact.MediaType), "size_bytes": artifact.SizeBytes,
		"sha256": artifact.SHA256, "required": artifact.Required, "created_at": artifact.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

type countingWriter struct {
	writer io.Writer
	count  int64
}

func (writer *countingWriter) Write(value []byte) (int, error) {
	written, err := writer.writer.Write(value)
	writer.count += int64(written)
	return written, err
}

// writeReplayArchive has no database or Worker side effect. It is used twice:
// first to calculate deterministic archive metadata, then to stream exactly
// those bytes to the client without retaining the ZIP in memory or on disk.
func writeReplayArchive(target io.Writer, source io.Reader, agent int, manifestJSON []byte, exportedAt time.Time) error {
	archive := zip.NewWriter(target)
	csvHeader := &zip.FileHeader{Name: fmt.Sprintf("replay_agent_%d.csv", agent), Method: zip.Deflate, Modified: exportedAt}
	csvEntry, err := archive.CreateHeader(csvHeader)
	if err == nil {
		_, err = io.CopyBuffer(csvEntry, source, make([]byte, 32*1024))
	}
	if err == nil {
		manifestHeader := &zip.FileHeader{Name: "replay_manifest.json", Method: zip.Deflate, Modified: exportedAt}
		manifestEntry, createErr := archive.CreateHeader(manifestHeader)
		if createErr != nil {
			err = createErr
		} else {
			_, err = manifestEntry.Write(append(manifestJSON, '\n'))
		}
	}
	if closeErr := archive.Close(); err == nil {
		err = closeErr
	}
	return err
}

func safeArtifactName(name string) bool {
	return name != "" && len(name) <= 255 && !strings.ContainsAny(name, "/\\") && !strings.Contains(name, "..") && !strings.Contains(name, "\x00")
}

func safeMediaType(value string) string {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return "application/octet-stream"
	}
	switch mediaType {
	case "application/json", "text/csv", "image/png", "image/svg+xml", "application/pdf":
		return mediaType
	default:
		return "application/octet-stream"
	}
}

// openCommittedArtifact accepts only the controlled storage identity emitted by
// the Worker: runs/<run_id>/committed/<logical name>. It checks both manifest
// metadata and filesystem bytes before a caller can expose content.
func (api *API) openCommittedArtifact(runID string, artifact postgres.ArtifactRecord) (*os.File, error) {
	if !safeArtifactName(artifact.Name) {
		return nil, artifactIntegrityError("The registered artifact name is invalid.")
	}
	expectedRelative := filepath.ToSlash(filepath.Join("runs", runID, "committed", artifact.Name))
	if filepath.ToSlash(artifact.RelativePath) != expectedRelative {
		return nil, artifactIntegrityError("The registered artifact path is outside the committed directory.")
	}
	root, err := filepath.Abs(api.config.ArtifactRoot)
	if err != nil {
		return nil, artifactIntegrityError("The artifact storage root is invalid.")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, artifactIntegrityError("The artifact storage root is unavailable.")
	}
	committedRoot := filepath.Join(resolvedRoot, "runs", runID, "committed")
	candidate := filepath.Join(committedRoot, artifact.Name)
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return nil, artifactIntegrityError("The committed artifact is missing or is not a regular file.")
	}
	relative, err := filepath.Rel(committedRoot, resolvedCandidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, artifactIntegrityError("The registered artifact path escaped the committed directory.")
	}
	info, err := os.Lstat(candidate)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, artifactIntegrityError("The committed artifact is missing or is not a regular file.")
	}
	if info.Size() != artifact.SizeBytes {
		return nil, artifactIntegrityError("The committed artifact size does not match its manifest.")
	}
	file, err := os.Open(candidate)
	if err != nil {
		return nil, artifactIntegrityError("The committed artifact could not be opened.")
	}
	digest := sha256.New()
	if _, err := io.CopyBuffer(digest, file, make([]byte, 32*1024)); err != nil {
		file.Close()
		return nil, artifactIntegrityError("The committed artifact could not be verified.")
	}
	if fmt.Sprintf("%x", digest.Sum(nil)) != artifact.SHA256 {
		file.Close()
		return nil, artifactIntegrityError("The committed artifact SHA-256 does not match its manifest.")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		return nil, artifactIntegrityError("The committed artifact could not be rewound.")
	}
	return file, nil
}

func (api *API) readCommittedJSONArtifact(ctx context.Context, runID, name string) (map[string]any, error) {
	artifact, err := api.repo.GetArtifact(ctx, runID, name)
	if err != nil {
		return nil, err
	}
	file, err := api.openCommittedArtifact(runID, artifact)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 8<<20))
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil || payload == nil {
		return nil, artifactIntegrityError("The committed JSON artifact is invalid.")
	}
	if decoder.More() {
		return nil, artifactIntegrityError("The committed JSON artifact contains trailing values.")
	}
	return payload, nil
}

func (api *API) readCommittedCSVRows(ctx context.Context, runID, name string, maximum int) ([]map[string]any, error) {
	artifact, err := api.repo.GetArtifact(ctx, runID, name)
	if err != nil {
		return nil, err
	}
	file, err := api.openCommittedArtifact(runID, artifact)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readCSVMetadataRows(file, maximum)
}

func readCSVMetadataRows(source io.Reader, maximum int) ([]map[string]any, error) {
	reader := csv.NewReader(source)
	reader.ReuseRecord = true
	header, err := readImmutableCSVHeader(reader)
	if err != nil {
		return nil, artifactIntegrityError("The committed CSV artifact has no header.")
	}
	if err := validateCSVHeader(header); err != nil {
		return nil, err
	}
	rows := make([]map[string]any, 0, min(maximum, 16))
	for {
		row, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil || len(row) != len(header) {
			return nil, artifactIntegrityError("The committed CSV artifact contains an invalid row.")
		}
		if len(rows) == maximum {
			return nil, artifactIntegrityError("The committed CSV artifact exceeded its bounded metadata row count.")
		}
		item := make(map[string]any, len(header))
		for index, column := range header {
			item[column] = resultScalar(row[index])
		}
		rows = append(rows, item)
	}
	return rows, nil
}

func validateCSVHeader(header []string) error {
	if len(header) == 0 {
		return artifactIntegrityError("The committed CSV artifact has no header.")
	}
	seen := make(map[string]bool, len(header))
	for _, column := range header {
		if column == "" || strings.Contains(column, "\x00") || seen[column] {
			return artifactIntegrityError("The committed CSV artifact has an invalid header.")
		}
		seen[column] = true
	}
	return nil
}

// readImmutableCSVHeader copies the first record before csv.Reader can reuse
// its backing array for the next streaming row. Every retained CSV header must
// use this boundary when ReuseRecord is enabled.
func readImmutableCSVHeader(reader *csv.Reader) ([]string, error) {
	header, err := reader.Read()
	if err != nil {
		return nil, err
	}
	return append([]string(nil), header...), nil
}

func (api *API) readSimulationSummary(ctx context.Context, record postgres.SimulationRecord, selection any) (map[string]any, error) {
	metricsRows, err := api.readCommittedCSVRows(ctx, record.RunID, "metrics.csv", 3)
	if err != nil {
		return nil, err
	}
	if len(metricsRows) != 3 {
		return nil, artifactIntegrityError("The committed metrics artifact must contain exactly three Agent rows.")
	}
	partitionRows, err := api.readCommittedCSVRows(ctx, record.RunID, "agent_partition_summary.csv", 3)
	if err != nil {
		return nil, err
	}
	if len(partitionRows) != 3 {
		return nil, artifactIntegrityError("The committed partition artifact must contain exactly three Agent rows.")
	}
	preprocessing, err := api.readCommittedJSONArtifact(ctx, record.RunID, "preprocessing_summary.json")
	if err != nil {
		return nil, err
	}
	anchorSummary, err := api.readCommittedJSONArtifact(ctx, record.RunID, "anchor_summary.json")
	if err != nil {
		return nil, err
	}
	diagnostics, err := api.readCommittedJSONArtifact(ctx, record.RunID, "diagnostics.json")
	if err != nil {
		return nil, err
	}
	manifest, err := api.repo.GetArtifact(ctx, record.RunID, "artifact_manifest.json")
	if err != nil {
		return nil, err
	}
	agents, segment, err := summarySelection(selection)
	if err != nil {
		return nil, err
	}
	chart, chartStats, err := api.readSummaryChart(ctx, record, agents)
	if err != nil {
		return nil, err
	}
	metrics, err := selectSummaryMetrics(metricsRows, agents, chartStats.meanBandwidth)
	if err != nil {
		return nil, err
	}
	alarmSummary, err := api.countCommittedAlarmLevels(ctx, record.RunID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"run":                resultRunIdentity(record),
		"selection":          map[string]any{"agent": selection, "segment": segment},
		"metrics":            metrics,
		"stage_durations_ms": rawOrObject(record.StageDurations),
		"preprocessing":      normalizePreprocessingSummary(preprocessing, agents[0]),
		"split_summary":      normalizeSplitSummary(partitionRows),
		"anchor_summary":     normalizeAnchorSummary(anchorSummary),
		"alarm_summary":      alarmSummary,
		"diagnostic_summary": normalizeDiagnosticSummary(diagnostics, metrics),
		"chart":              chart,
		"artifact_integrity": map[string]any{"status": "VERIFIED", "manifest_sha256": manifest.SHA256},
	}, nil
}

func summarySelection(selection any) ([]int, string, error) {
	if aggregate, ok := selection.(string); ok && aggregate == "aggregate" {
		return []int{1, 2, 3}, "ALL", nil
	}
	agent, ok := selection.(int)
	if !ok || agent < 1 || agent > 3 {
		return nil, "", invalidQuery("agent", "agent must be 1, 2, 3, or aggregate.")
	}
	return []int{agent}, []string{"EARLY", "MIDDLE", "LATE"}[agent-1], nil
}

func normalizePreprocessingSummary(raw map[string]any, agent int) map[string]any {
	selected := raw
	if byAgent, ok := raw["by_agent"].(map[string]any); ok {
		if candidate, ok := byAgent[strconv.Itoa(agent)].(map[string]any); ok {
			selected = candidate
		}
	}
	counts, _ := selected["counts"].(map[string]any)
	filterPath, _ := selected["filter_path"].(map[string]any)
	return map[string]any{
		"contract_version": selected["preprocessing_contract_version"], "raw_rows": counts["raw_rows"],
		"invalid_numeric_rows": counts["invalid_numeric_rows"], "stopped_rows": counts["stop_rows"],
		"suspicious_rows": counts["suspicious_rows"], "running_rows": counts["running_rows"],
		"spike_flags": counts["spike_rows"], "filter_path": filterPath, "summary_sha256": selected["summary_sha256"],
	}
}

func normalizeSplitSummary(rows []map[string]any) map[string]any {
	summary := make(map[string]any, len(rows))
	for _, row := range rows {
		agent, _ := row["Agent"].(string)
		if agent == "" {
			continue
		}
		summary[strings.ToLower(strings.ReplaceAll(agent, " ", "_"))] = map[string]any{
			"running_rows": row["RunningRows"], "supervised_samples": row["SupervisedSamples"],
			"training_samples": row["TrainingSamples"], "calibration_samples": row["CalibrationSamples"],
			"testing_samples": row["TestingSamples"], "start_time": row["StartTime"], "end_time": row["EndTime"],
		}
	}
	return summary
}

func normalizeAnchorSummary(raw map[string]any) map[string]any {
	result := make(map[string]any, len(raw)+1)
	for key, value := range raw {
		result[key] = value
	}
	if value, exists := raw["public_anchor_count"]; exists {
		result["public_anchors"] = value
	}
	return result
}

func normalizeDiagnosticSummary(raw map[string]any, metrics map[string]any) map[string]any {
	result := make(map[string]any, len(raw)+1)
	for key, value := range raw {
		result[key] = value
	}
	if value, exists := metrics["MeanGlobalSupport"]; exists {
		result["mean_global_support"] = value
	}
	return result
}

func selectSummaryMetrics(rows []map[string]any, agents []int, meanBandwidth float64) (map[string]any, error) {
	byAgent := make(map[int]map[string]any, len(rows))
	for _, row := range rows {
		label, _ := row["Agent"].(string)
		parts := strings.Fields(label)
		if len(parts) != 2 || parts[0] != "Agent" {
			return nil, artifactIntegrityError("The committed metrics artifact has an invalid Agent label.")
		}
		agent, err := strconv.Atoi(parts[1])
		if err != nil || agent < 1 || agent > 3 || byAgent[agent] != nil {
			return nil, artifactIntegrityError("The committed metrics artifact has invalid Agent coverage.")
		}
		byAgent[agent] = row
	}
	for _, agent := range agents {
		if byAgent[agent] == nil {
			return nil, artifactIntegrityError("The committed metrics artifact is missing an Agent row.")
		}
	}
	result := make(map[string]any)
	if len(agents) == 1 {
		for key, value := range byAgent[agents[0]] {
			result[key] = value
		}
	} else {
		sums := make(map[string]float64)
		counts := make(map[string]int)
		for _, agent := range agents {
			for key, value := range byAgent[agent] {
				if number, ok := jsonNumber(value); ok {
					sums[key] += number
					counts[key]++
				}
			}
		}
		for key, sum := range sums {
			result[key] = sum / float64(counts[key])
		}
		result["Agent"] = "aggregate"
	}
	result["RMSE"] = result["FusedRMSE"]
	result["MAE"] = result["FusedMAE"]
	result["R2"] = result["FusedR2"]
	result["Coverage"] = result["FusedCoverage"]
	result["MeanBandwidth"] = meanBandwidth
	return result, nil
}

func jsonNumber(value any) (float64, bool) {
	switch number := value.(type) {
	case int64:
		return float64(number), true
	case float64:
		return number, true
	default:
		return 0, false
	}
}

type chartStatistics struct{ meanBandwidth float64 }

func (api *API) readSummaryChart(ctx context.Context, record postgres.SimulationRecord, agents []int) (map[string]any, chartStatistics, error) {
	points := make([]resultPoint, 0, 2000*len(agents))
	var total int64
	var bandwidthSum float64
	var bandwidthCount int64
	for _, agent := range agents {
		sampled, pointCount, sum, count, err := api.readSampledResultPoints(ctx, record.RunID, agent, 2000)
		if err != nil {
			return nil, chartStatistics{}, err
		}
		points = append(points, sampled...)
		total += pointCount
		bandwidthSum += sum
		bandwidthCount += count
	}
	sort.Slice(points, func(left, right int) bool {
		if points[left].index == points[right].index {
			return fmt.Sprint(points[left].value["Agent"]) < fmt.Sprint(points[right].value["Agent"])
		}
		return points[left].index < points[right].index
	})
	items := make([]any, 0, len(points))
	for _, point := range points {
		items = append(items, point.value)
	}
	method := "none"
	if int64(len(items)) < total {
		method = "uniform_with_alarm_and_boundary_retention"
	}
	statistics := chartStatistics{}
	if bandwidthCount > 0 {
		statistics.meanBandwidth = bandwidthSum / float64(bandwidthCount)
	}
	return map[string]any{"original_point_count": total, "display_point_count": len(items), "sampling_method": method, "points": items}, statistics, nil
}

func (api *API) readSampledResultPoints(ctx context.Context, runID string, agent, maximum int) ([]resultPoint, int64, float64, int64, error) {
	name := fmt.Sprintf("results_agent_%d.csv", agent)
	artifact, err := api.repo.GetArtifact(ctx, runID, name)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	count, err := api.countResultRows(runID, artifact)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	file, err := api.openCommittedArtifact(runID, artifact)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	defer file.Close()
	return readSampledResultCSV(file, count, agent, maximum)
}

func readSampledResultCSV(source io.Reader, count int64, agent, maximum int) ([]resultPoint, int64, float64, int64, error) {
	stride := int64(1)
	if count > int64(maximum) {
		stride = (count + int64(maximum) - 1) / int64(maximum)
	}
	reader := csv.NewReader(source)
	reader.ReuseRecord = true
	header, err := readImmutableCSVHeader(reader)
	if err != nil {
		return nil, 0, 0, 0, artifactIntegrityError("The committed results artifact has no CSV header.")
	}
	if err := validateCSVHeader(header); err != nil {
		return nil, 0, 0, 0, err
	}
	columns := csvColumnIndex(header)
	indexColumn, indexOK := columns["OriginalRunningIndex"]
	timeColumn, timeOK := columns["Time"]
	if !indexOK || !timeOK {
		return nil, 0, 0, 0, artifactIntegrityError("The committed results artifact is missing result identity columns.")
	}
	points := make([]resultPoint, 0, maximum)
	seen := make(map[int64]bool)
	var rowNumber, bandwidthCount int64
	var bandwidthSum float64
	var last resultPoint
	for {
		row, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil || len(row) != len(header) {
			return nil, 0, 0, 0, artifactIntegrityError("The committed results artifact contains an invalid CSV row.")
		}
		point, err := resultPointFromCSV(header, row, indexColumn, timeColumn, agent)
		if err != nil {
			return nil, 0, 0, 0, err
		}
		if lower, lowerOK := jsonNumber(point.value["FusedLowerBound"]); lowerOK {
			if upper, upperOK := jsonNumber(point.value["FusedUpperBound"]); upperOK {
				bandwidthSum += upper - lower
				bandwidthCount++
			}
		}
		alarm := strings.EqualFold(fmt.Sprint(point.value["OverallAlarmLevel"]), "alarm") || strings.EqualFold(fmt.Sprint(point.value["OverallAlarmLevel"]), "warning") || strings.EqualFold(fmt.Sprint(point.value["OverallAlarmLevel"]), "notice")
		if rowNumber == 0 || rowNumber%stride == 0 || alarm {
			if !seen[point.index] {
				points = append(points, point)
				seen[point.index] = true
			}
		}
		last = point
		rowNumber++
	}
	if rowNumber > 0 && !seen[last.index] {
		points = append(points, last)
	}
	return points, count, bandwidthSum, bandwidthCount, nil
}

func (api *API) countResultRows(runID string, artifact postgres.ArtifactRecord) (int64, error) {
	file, err := api.openCommittedArtifact(runID, artifact)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	return countResultCSVRows(file)
}

func countResultCSVRows(source io.Reader) (int64, error) {
	reader := csv.NewReader(source)
	reader.ReuseRecord = true
	header, err := readImmutableCSVHeader(reader)
	if err != nil {
		return 0, artifactIntegrityError("The committed results artifact has no CSV header.")
	}
	if err := validateCSVHeader(header); err != nil {
		return 0, err
	}
	var count int64
	for {
		row, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil || len(row) != len(header) {
			return 0, artifactIntegrityError("The committed results artifact contains an invalid CSV row.")
		}
		count++
	}
	return count, nil
}

func csvColumnIndex(header []string) map[string]int {
	columns := make(map[string]int, len(header))
	for index, name := range header {
		columns[name] = index
	}
	return columns
}

func (api *API) countCommittedAlarmLevels(ctx context.Context, runID string) (map[string]any, error) {
	artifact, err := api.repo.GetArtifact(ctx, runID, "alarms.csv")
	if err != nil {
		return nil, err
	}
	file, err := api.openCommittedArtifact(runID, artifact)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return countAlarmLevelsCSV(file)
}

func countAlarmLevelsCSV(source io.Reader) (map[string]any, error) {
	reader := csv.NewReader(source)
	reader.ReuseRecord = true
	header, err := readImmutableCSVHeader(reader)
	if err != nil {
		return nil, artifactIntegrityError("The committed alarms artifact has no CSV header.")
	}
	if err := validateCSVHeader(header); err != nil {
		return nil, err
	}
	levelIndex, exists := csvColumnIndex(header)["OverallAlarmLevel"]
	if !exists {
		return nil, artifactIntegrityError("The committed alarms artifact is missing OverallAlarmLevel.")
	}
	counts := map[string]any{"None": int64(0), "Notice": int64(0), "Warning": int64(0), "Alarm": int64(0)}
	for {
		row, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil || len(row) != len(header) {
			return nil, artifactIntegrityError("The committed alarms artifact contains an invalid CSV row.")
		}
		level := row[levelIndex]
		if level == "NONE" {
			level = "None"
		}
		if current, ok := counts[level].(int64); ok {
			counts[level] = current + 1
		}
	}
	return counts, nil
}

func queryLimit(request *http.Request) int {
	raw := request.URL.Query().Get("limit")
	if raw == "" {
		return 100
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > 500 {
		return 100
	}
	return value
}

type profileAgentRequest struct {
	Agent      int            `json:"agent"`
	Parameters map[string]any `json:"parameters"`
}

type profileImportAgent struct {
	Agent      int            `json:"agent"`
	Segment    string         `json:"segment"`
	Parameters map[string]any `json:"parameters"`
}

func profileAgentInputs(requests []profileAgentRequest) []parameters.AgentOverride {
	inputs := make([]parameters.AgentOverride, 0, len(requests))
	for _, request := range requests {
		inputs = append(inputs, parameters.AgentOverride{Agent: request.Agent, Parameters: request.Parameters})
	}
	return inputs
}

func importedProfileAgentInputs(requests []profileImportAgent) []parameters.AgentOverride {
	inputs := make([]parameters.AgentOverride, 0, len(requests))
	for _, request := range requests {
		inputs = append(inputs, parameters.AgentOverride{Agent: request.Agent, Segment: request.Segment, Parameters: request.Parameters})
	}
	return inputs
}

func (api *API) parameterProfileResponse(profile postgres.ProfileRecord) (map[string]any, error) {
	constraints, editablePaths, err := api.repo.ParameterConstraints()
	if err != nil {
		return nil, err
	}
	return profileResponse(profile, constraints, editablePaths), nil
}

func profileResponse(profile postgres.ProfileRecord, constraints map[string]any, editablePaths []string) map[string]any {
	return map[string]any{"version_id": profile.VersionID, "mode": profile.Mode, "display_name": profile.DisplayName, "base_version_id": profile.BaseVersionID, "immutable": profile.Immutable, "contract_version": domain.ParameterContractVersion, "shared_parameters": rawOrObject(profile.Shared), "agents": rawOrObject(profile.Agents), "fixed_items": rawOrObject(profile.FixedItems), "constraints": constraints, "editable_paths": editablePaths, "normalized_sha256": profile.NormalizedSHA256, "created_at": profile.CreatedAt.UTC().Format(time.RFC3339Nano), "updated_at": profile.UpdatedAt.UTC().Format(time.RFC3339Nano)}
}

func mappingResponse(mapping postgres.MappingRecord) map[string]any {
	return map[string]any{"version_id": mapping.VersionID, "display_name": mapping.DisplayName, "mapping_type": mapping.MappingType, "parameters": rawOrObject(mapping.Parameters), "result_unit": mapping.ResultUnit, "normalized_sha256": mapping.NormalizedSHA256}
}

func simulationResponse(record postgres.SimulationRecord) map[string]any {
	response := simulationListItem(record)
	response["snapshot_sha256"] = record.SnapshotSHA256
	response["latest_event_id"] = record.LatestEventID
	response["artifact_state"] = record.ArtifactState
	response["stage_durations_ms"] = rawOrObject(record.StageDurations)
	response["worker_heartbeat_at"] = timestampOrNil(record.LastHeartbeatAt)
	if parameterSnapshot, ok := simulationParameterSnapshot(record.Snapshot); ok {
		response["parameter_snapshot"] = parameterSnapshot
	}
	if datasetSnapshot, ok := simulationDatasetSnapshot(record.Snapshot); ok {
		response["dataset_snapshot"] = datasetSnapshot
	}
	return response
}

func simulationListItem(record postgres.SimulationRecord) map[string]any {
	response := map[string]any{
		"run_id": record.RunID, "display_name": stringOrNil(record.DisplayName), "status": record.Status,
		"current_stage": record.CurrentStage, "queue_position": record.QueuePosition, "run_mode": record.RunMode,
		"created_at": record.CreatedAt.UTC().Format(time.RFC3339Nano), "started_at": timestampOrNil(record.StartedAt),
		"finished_at": timestampOrNil(record.FinishedAt), "elapsed_ms": elapsedMilliseconds(record),
		"error_summary": errorSummary(record.Error), "artifact_state": record.ArtifactState,
		"cancellable": simulationCancellable(record.Status),
	}
	if datasetSnapshot, ok := simulationDatasetSnapshot(record.Snapshot); ok {
		response["dataset"] = map[string]any{
			"dataset_id": datasetSnapshot["dataset_id"], "display_name": datasetSnapshot["display_name"], "sha256": datasetSnapshot["sha256"],
		}
	}
	if parameterSnapshot, ok := simulationParameterSnapshot(record.Snapshot); ok {
		parameterVersion := map[string]any{
			"version_id": parameterSnapshot["version_id"], "display_name": parameterSnapshot["display_name"], "normalized_sha256": parameterSnapshot["normalized_sha256"],
		}
		response["parameter_version"] = parameterVersion
		response["parameter_profile"] = parameterVersion
	}
	if snapshotIdentity, ok := simulationSnapshotIdentity(record.Snapshot); ok {
		response["snapshot_identity"] = snapshotIdentity
		response["mapping_version"] = map[string]any{"version_id": snapshotIdentity["load_mapping_version_id"], "normalized_sha256": snapshotIdentity["mapping_sha256"]}
	}
	return response
}

func simulationCancellable(status domain.SimulationStatus) bool {
	return status == domain.SimulationQueued || status == domain.SimulationRunning
}

func simulationSnapshotIdentity(snapshot json.RawMessage) (map[string]any, bool) {
	var stored map[string]any
	if err := json.Unmarshal(snapshot, &stored); err != nil {
		return nil, false
	}
	return map[string]any{
		"contract_version": stored["contract_version"], "parameter_profile_version_id": stored["parameter_profile_version_id"],
		"parameter_profile_display_name": stored["parameter_profile_display_name"], "parameter_sha256": stored["parameter_sha256"],
		"load_mapping_version_id": stored["load_mapping_version_id"], "mapping_sha256": stored["mapping_sha256"],
		"runtime": stored["runtime"],
	}, true
}

func resultRunIdentity(record postgres.SimulationRecord) map[string]any {
	identity, _ := simulationSnapshotIdentity(record.Snapshot)
	if identity == nil {
		identity = map[string]any{}
	}
	identity["run_id"] = record.RunID
	identity["run_mode"] = record.RunMode
	identity["snapshot_sha256"] = record.SnapshotSHA256
	return identity
}

func timestampOrNil(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func stringOrNil(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func elapsedMilliseconds(record postgres.SimulationRecord) int64 {
	if record.StartedAt == nil {
		return 0
	}
	end := record.FinishedAt
	if end == nil {
		now := time.Now().UTC()
		end = &now
	}
	if end.Before(*record.StartedAt) {
		return 0
	}
	return end.Sub(*record.StartedAt).Milliseconds()
}

func errorSummary(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil
	}
	result := make(map[string]any, 6)
	for _, key := range []string{"code", "message", "stage", "agent", "diagnostic_id", "recoverable"} {
		if value, ok := stored[key]; ok {
			result[key] = value
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func simulationDatasetSnapshot(snapshot json.RawMessage) (map[string]any, bool) {
	var stored map[string]any
	if err := json.Unmarshal(snapshot, &stored); err != nil {
		return nil, false
	}
	dataset, ok := stored["dataset"].(map[string]any)
	return dataset, ok
}

// simulationParameterSnapshot is a read-only projection of the immutable
// simulation snapshot. It deliberately does not query parameter_profiles, so
// queued, running, and terminal runs cannot observe later CUSTOM versions.
func simulationParameterSnapshot(snapshot json.RawMessage) (map[string]any, bool) {
	var stored map[string]any
	if err := json.Unmarshal(snapshot, &stored); err != nil {
		return nil, false
	}
	profile, ok := stored["parameter_snapshot"].(map[string]any)
	if !ok {
		return nil, false
	}
	effective, effectiveOK := stored["parameter_effective"].(map[string]any)
	versionID, versionOK := stored["parameter_profile_version_id"].(string)
	displayName, displayNameOK := stored["parameter_profile_display_name"].(string)
	hash, hashOK := stored["parameter_sha256"].(string)
	if !effectiveOK || !versionOK || !displayNameOK || !hashOK {
		return nil, false
	}
	return map[string]any{
		"version_id":           versionID,
		"display_name":         displayName,
		"normalized_sha256":    hash,
		"contract_version":     profile["contract_version"],
		"mode":                 profile["mode"],
		"base_version_id":      profile["base_version_id"],
		"shared_parameters":    profile["shared_parameters"],
		"agents":               profile["agents"],
		"fixed_items":          profile["fixed_items"],
		"effective_parameters": effective,
	}, true
}

func writeSSE(writer http.ResponseWriter, eventID int64, event string, payload any) {
	encoded, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(writer, "id: %d\nevent: %s\nretry: 3000\ndata: %s\n\n", eventID, event, encoded)
}
