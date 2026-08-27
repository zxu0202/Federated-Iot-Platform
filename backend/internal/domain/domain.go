// Package domain contains stable S1 control-plane types and validation.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	WorkerContractVersion       = "worker.task.v1"
	PreflightContractVersion    = "preprocessing.v1"
	ParameterContractVersion    = "parameter-profile.v1"
	SimulationWaitCapacity      = 10
	PreflightQueueCapacity      = 4
	DefaultEventRetentionPerRun = 2000
)

type DatasetStatus string

const (
	DatasetUploading  DatasetStatus = "UPLOADING"
	DatasetValidating DatasetStatus = "VALIDATING"
	DatasetValid      DatasetStatus = "VALID"
	DatasetInvalid    DatasetStatus = "INVALID"
)

type SimulationStatus string

const (
	SimulationCreated             SimulationStatus = "CREATED"
	SimulationValidating          SimulationStatus = "VALIDATING"
	SimulationQueued              SimulationStatus = "QUEUED"
	SimulationRunning             SimulationStatus = "RUNNING"
	SimulationCancelling          SimulationStatus = "CANCELLING"
	SimulationGeneratingArtifacts SimulationStatus = "GENERATING_ARTIFACTS"
	SimulationCompleted           SimulationStatus = "COMPLETED"
	SimulationCancelled           SimulationStatus = "CANCELLED"
	SimulationFailed              SimulationStatus = "FAILED"
	SimulationFailedRecoverable   SimulationStatus = "FAILED_RECOVERABLE"
)

type Stage string

const (
	StagePreprocessing     Stage = "PREPROCESSING"
	StageLocalTraining     Stage = "LOCAL_TRAINING"
	StageAnchorAggregating Stage = "ANCHOR_AGGREGATING"
	StageGlobalDistilling  Stage = "GLOBAL_DISTILLING"
	StageCalibrating       Stage = "CALIBRATING"
	StageTesting           Stage = "TESTING"
)

type RunMode string

const (
	RunModeReference RunMode = "REFERENCE"
	RunModeCustom    RunMode = "CUSTOM"
)

type JobType string

const (
	JobTypePreflight  JobType = "DATASET_PREFLIGHT"
	JobTypeSimulation JobType = "SIMULATION"
)

var requiredColumns = []string{"Time_base", "dzdl_1", "dzdl_2", "dzdl_3", "dzdl_4", "zl", "sd"}

func RequiredColumns() []string {
	return append([]string(nil), requiredColumns...)
}

func IsTerminal(status SimulationStatus) bool {
	return status == SimulationCompleted || status == SimulationCancelled || status == SimulationFailed || status == SimulationFailedRecoverable
}

func IsExecutionStatus(status SimulationStatus) bool {
	return status == SimulationRunning || status == SimulationCancelling || status == SimulationGeneratingArtifacts
}

// ValidateAgentSet enforces the frozen S1 generic collection shape without
// creating per-agent tables or endpoints.
func ValidateAgentSet(agents []AgentOverride) error {
	if len(agents) != 3 {
		return errors.New("agents must contain exactly Agent 1, Agent 2, and Agent 3")
	}
	seen := make(map[int]bool, 3)
	for _, agent := range agents {
		if agent.Agent < 1 || agent.Agent > 3 || seen[agent.Agent] {
			return errors.New("agents must contain each of 1, 2, and 3 exactly once")
		}
		seen[agent.Agent] = true
		if len(agent.Parameters) != 0 {
			return FieldError{Code: "AGENT_OVERRIDE_NOT_ALLOWED", Field: fmt.Sprintf("agent_overrides[%d].parameters", agent.Agent), Message: "Per-run Agent overrides are not accepted; use a saved CUSTOM parameter profile."}
		}
	}
	return nil
}

// CanonicalJSON creates the deterministic normalized JSON used by profile and
// admission hashes. Go's encoder orders map keys, while ordered arrays are
// normalized recursively before encoding.
func CanonicalJSON(value any) ([]byte, error) {
	normalized, err := normalize(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

func SHA256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func normalize(value any) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		ordered := make(map[string]any, len(typed))
		for _, key := range keys {
			entry, err := normalize(typed[key])
			if err != nil {
				return nil, err
			}
			ordered[key] = entry
		}
		return ordered, nil
	case []any:
		items := make([]any, len(typed))
		for index := range typed {
			entry, err := normalize(typed[index])
			if err != nil {
				return nil, err
			}
			items[index] = entry
		}
		return items, nil
	case json.RawMessage:
		var decoded any
		if err := json.Unmarshal(typed, &decoded); err != nil {
			return nil, err
		}
		return normalize(decoded)
	default:
		return value, nil
	}
}

type FieldError struct {
	Code    string
	Field   string
	Message string
}

func (e FieldError) Error() string { return e.Message }

type AgentOverride struct {
	Agent      int            `json:"agent"`
	Parameters map[string]any `json:"parameters"`
}

type CreateSimulationRequest struct {
	DatasetID                 string          `json:"dataset_id"`
	RunMode                   RunMode         `json:"run_mode"`
	ParameterProfileVersionID string          `json:"parameter_profile_version_id"`
	LoadMappingVersionID      string          `json:"load_mapping_version_id"`
	AgentOverrides            []AgentOverride `json:"agent_overrides"`
	Seed                      int64           `json:"seed"`
	DisplayName               string          `json:"display_name"`
}

func (request CreateSimulationRequest) Validate() error {
	if strings.TrimSpace(request.DatasetID) == "" || strings.TrimSpace(request.ParameterProfileVersionID) == "" || strings.TrimSpace(request.LoadMappingVersionID) == "" {
		return FieldError{Code: "REQUEST_INVALID", Message: "Dataset, parameter profile, and load mapping are required."}
	}
	if request.RunMode != RunModeReference && request.RunMode != RunModeCustom {
		return FieldError{Code: "REQUEST_INVALID", Field: "run_mode", Message: "run_mode must be REFERENCE or CUSTOM."}
	}
	if request.Seed == 0 {
		return FieldError{Code: "PARAMETER_OUT_OF_RANGE", Field: "seed", Message: "seed must be explicit and non-zero."}
	}
	return ValidateAgentSet(request.AgentOverrides)
}

type SimulationSnapshot struct {
	ContractVersion string           `json:"contract_version"`
	Dataset         map[string]any   `json:"dataset"`
	RunMode         RunMode          `json:"run_mode"`
	Parameter       map[string]any   `json:"parameter_snapshot"`
	Mapping         map[string]any   `json:"mapping_snapshot"`
	Agents          []map[string]any `json:"agents"`
	Runtime         map[string]any   `json:"runtime"`
	FieldStandard   map[string]any   `json:"field_standard_snapshot"`
	CreatedAt       time.Time        `json:"created_at"`
}
