// Package postgres implements persistent admission and Worker Repository logic.
package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zx/federated-iot-platform/backend/internal/domain"
	"github.com/zx/federated-iot-platform/backend/internal/parameters"
)

const referenceProfileVersionID = "reference-v1"

type Repository struct {
	pool                    *pgxpool.Pool
	now                     func() time.Time
	runtime                 RuntimeIdentity
	parameterConstraints    *parameters.Document
	parameterConstraintsErr error
}

type RuntimeIdentity struct {
	AlgorithmVersion  string
	WorkerVersion     string
	WorkerImageDigest string
	NumericRuntime    string
}

func (identity RuntimeIdentity) Validate() error {
	if identity.AlgorithmVersion == "" || identity.WorkerVersion == "" || identity.NumericRuntime == "" {
		return errors.New("algorithm, worker, and numeric runtime versions are required")
	}
	if matched, _ := regexp.MatchString(`^sha256:[0-9a-f]{64}$`, identity.WorkerImageDigest); !matched {
		return errors.New("worker image digest must be immutable sha256:<64 lowercase hex>")
	}
	return nil
}

func New(pool *pgxpool.Pool, runtime ...RuntimeIdentity) *Repository {
	identity := RuntimeIdentity{}
	if len(runtime) == 1 {
		identity = runtime[0]
	}
	return &Repository{pool: pool, now: time.Now, runtime: identity}
}

func (r *Repository) Ping(ctx context.Context) error {
	return r.pool.Ping(ctx)
}

type StableError struct {
	Code        string
	Field       string
	Message     string
	Recoverable bool
}

func (e StableError) Error() string { return e.Message }

type DatasetRegistration struct {
	DatasetID            string
	DisplayName          string
	OriginalFilename     string
	StorageKey           string
	SHA256               string
	SizeBytes            int64
	Timezone             string
	UTCOffset            string
	StructuralStatistics json.RawMessage
	Warnings             json.RawMessage
}

type DatasetRecord struct {
	DatasetID            string
	DisplayName          string
	OriginalFilename     string
	SHA256               string
	SizeBytes            int64
	Timezone             string
	UTCOffset            string
	Status               domain.DatasetStatus
	StructuralStatistics json.RawMessage
	Warnings             json.RawMessage
	PreflightSummary     json.RawMessage
	PreflightSHA256      *string
	Preflight            DatasetPreflightRecord
	ValidationStartedAt  *time.Time
	ValidationFinishedAt *time.Time
	CreatedAt            time.Time
}

// DatasetPreflightRecord contains only the preflight facts safe for the
// product API. Lease secrets and storage paths remain inside the database.
type DatasetPreflightRecord struct {
	JobID         *string
	Status        *string
	QueuePosition *int
	Stage         *string
	AttemptID     *string
	LeaseState    *string
	LatestEventID *int64
	Error         json.RawMessage
}

// RegisterDataset persists a structurally valid CSV and its preflight envelope
// in one transaction. The caller must remove an uncommitted source file when a
// capacity or storage error is returned.
func (r *Repository) RegisterDataset(ctx context.Context, input DatasetRegistration) (DatasetRecord, string, int, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return DatasetRecord{}, "", 0, err
	}
	defer tx.Rollback(ctx)

	var capacity int
	if err := tx.QueryRow(ctx, "SELECT preflight_queue_capacity FROM scheduler_control WHERE singleton = TRUE FOR UPDATE").Scan(&capacity); err != nil {
		return DatasetRecord{}, "", 0, err
	}
	var queuedPreflights int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM worker_jobs WHERE job_type='DATASET_PREFLIGHT' AND status='QUEUED'").Scan(&queuedPreflights); err != nil {
		return DatasetRecord{}, "", 0, err
	}
	if queuedPreflights >= capacity {
		return DatasetRecord{}, "", 0, StableError{Code: "PREFLIGHT_QUEUE_FULL", Message: "The dataset preflight queue is at capacity.", Recoverable: true}
	}
	var queuedJobs int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM worker_jobs WHERE status='QUEUED'").Scan(&queuedJobs); err != nil {
		return DatasetRecord{}, "", 0, err
	}

	datasetID := input.DatasetID
	if datasetID == "" {
		datasetID = newOpaqueID("ds")
	}
	jobID := newOpaqueID("job")
	sequence, err := nextSequence(ctx, tx)
	if err != nil {
		return DatasetRecord{}, "", 0, err
	}
	envelope := map[string]any{
		"contract_version": domain.WorkerContractVersion,
		"job_id":           jobID,
		"job_type":         domain.JobTypePreflight,
		"run_id":           nil,
		"dataset": map[string]any{
			"dataset_id": datasetID, "relative_path": input.StorageKey, "sha256": input.SHA256, "timezone": input.Timezone,
			"columns": domain.RequiredColumns(),
		},
		"preprocessing": map[string]any{
			"contract_version":                    domain.PreflightContractVersion,
			"field_standard_configuration_sha256": fieldStandardSHA256(),
		},
		"output": map[string]any{
			"relative_tmp_directory":  fmt.Sprintf("datasets/%s/preflight/tmp", datasetID),
			"required_summary_schema": "dataset-preflight.summary.v1",
		},
		"limits": map[string]any{"memory_bytes": int64(10 * 1024 * 1024 * 1024), "cancel_check_target_ms": 5000},
	}
	envelopeJSON, err := domain.CanonicalJSON(envelope)
	if err != nil {
		return DatasetRecord{}, "", 0, err
	}

	_, err = tx.Exec(ctx, `INSERT INTO datasets
        (dataset_id, display_name, original_filename, storage_key, sha256, size_bytes, columns_json, timezone, utc_offset, status, structural_statistics, warnings_json, preflight_contract_version, validation_started_at)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'VALIDATING',$10,$11,$12,now())`,
		datasetID, input.DisplayName, input.OriginalFilename, input.StorageKey, input.SHA256, input.SizeBytes, mustJSON(domain.RequiredColumns()), input.Timezone, input.UTCOffset, input.StructuralStatistics, input.Warnings, domain.PreflightContractVersion)
	if err != nil {
		return DatasetRecord{}, "", 0, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO worker_jobs
        (job_id, job_type, dataset_id, run_id, envelope_json, envelope_sha256, enqueue_seq, status)
        VALUES ($1,'DATASET_PREFLIGHT',$2,NULL,$3,$4,$5,'QUEUED')`, jobID, datasetID, envelopeJSON, domain.SHA256Hex(envelopeJSON), sequence)
	if err != nil {
		return DatasetRecord{}, "", 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DatasetRecord{}, "", 0, err
	}
	createdAt := r.now()
	return DatasetRecord{
		DatasetID: datasetID, DisplayName: input.DisplayName, OriginalFilename: input.OriginalFilename,
		SHA256: input.SHA256, SizeBytes: input.SizeBytes, Timezone: input.Timezone, UTCOffset: input.UTCOffset,
		Status: domain.DatasetValidating, StructuralStatistics: input.StructuralStatistics, Warnings: input.Warnings,
		Preflight: DatasetPreflightRecord{
			JobID: datasetStringPointer(jobID), Status: datasetStringPointer("QUEUED"), QueuePosition: datasetIntPointer(queuedJobs + 1),
			LeaseState: datasetStringPointer("NOT_CLAIMED"),
		},
		ValidationStartedAt: datasetTimePointer(createdAt), CreatedAt: createdAt,
	}, jobID, queuedJobs + 1, nil
}

func (r *Repository) GetDataset(ctx context.Context, datasetID string) (DatasetRecord, error) {
	var record DatasetRecord
	var status string
	err := r.pool.QueryRow(ctx, `SELECT d.dataset_id, d.display_name, d.original_filename, d.sha256, d.size_bytes, d.timezone, d.utc_offset, d.status,
        d.structural_statistics, d.warnings_json, d.preflight_summary_json, d.preflight_summary_sha256,
        d.validation_started_at, d.validation_finished_at, d.created_at,
        projection.job_id, projection.job_status, projection.queue_position, projection.current_stage,
        projection.attempt_id, projection.lease_state, projection.latest_event_id, projection.error_json
        FROM datasets AS d
        LEFT JOIN LATERAL dataset_preflight_projection(d.dataset_id) AS projection ON TRUE
        WHERE d.dataset_id=$1`, datasetID).Scan(
		&record.DatasetID, &record.DisplayName, &record.OriginalFilename, &record.SHA256, &record.SizeBytes, &record.Timezone, &record.UTCOffset, &status,
		&record.StructuralStatistics, &record.Warnings, &record.PreflightSummary, &record.PreflightSHA256,
		&record.ValidationStartedAt, &record.ValidationFinishedAt, &record.CreatedAt,
		&record.Preflight.JobID, &record.Preflight.Status, &record.Preflight.QueuePosition, &record.Preflight.Stage,
		&record.Preflight.AttemptID, &record.Preflight.LeaseState, &record.Preflight.LatestEventID, &record.Preflight.Error,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DatasetRecord{}, StableError{Code: "DATASET_NOT_FOUND", Message: "Dataset was not found."}
	}
	if err != nil {
		return DatasetRecord{}, err
	}
	record.Status = domain.DatasetStatus(status)
	return record, nil
}

func datasetStringPointer(value string) *string { return &value }

func datasetIntPointer(value int) *int { return &value }

func datasetTimePointer(value time.Time) *time.Time { return &value }

type ProfileInput struct {
	DisplayName   string
	BaseVersionID string
	Shared        map[string]any
	Agents        []parameters.AgentOverride
}

// ImportProfileInput contains the profile fields whose immutable integrity is
// revalidated before an exported CUSTOM profile can be imported.
type ImportProfileInput struct {
	ProfileInput
	ContractVersion  string
	Mode             domain.RunMode
	FixedItems       map[string]any
	NormalizedSHA256 string
	Immutable        bool
}

type ProfileRecord struct {
	VersionID        string          `json:"version_id"`
	Mode             domain.RunMode  `json:"mode"`
	DisplayName      string          `json:"display_name"`
	BaseVersionID    *string         `json:"base_version_id,omitempty"`
	Shared           json.RawMessage `json:"shared_parameters"`
	Agents           json.RawMessage `json:"agents"`
	FixedItems       json.RawMessage `json:"fixed_items"`
	NormalizedSHA256 string          `json:"normalized_sha256"`
	Immutable        bool            `json:"immutable"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

type MappingRecord struct {
	VersionID        string          `json:"version_id"`
	DisplayName      string          `json:"display_name"`
	MappingType      string          `json:"mapping_type"`
	Parameters       json.RawMessage `json:"parameters"`
	ResultUnit       string          `json:"result_unit"`
	NormalizedSHA256 string          `json:"normalized_sha256"`
}

// EnsureReferenceProfiles seeds only immutable reference resources. It is safe
// to run at every startup and never mutates an existing approved profile.
func (r *Repository) EnsureReferenceProfiles(ctx context.Context) error {
	shared := referenceSharedParameters()
	agents := referenceAgents()
	fixed := referenceFixedItems()
	normalized, profileSHA, err := referenceProfileSeed()
	if err != nil {
		return err
	}
	mapping := map[string]any{"mapping_type": "identity", "parameters": map[string]any{}, "result_unit": "A"}
	mappingJSON, err := domain.CanonicalJSON(mapping)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO parameter_profiles
        (version_id, mode, display_name, base_version_id, contract_version, shared_parameters, agents_json, fixed_items, normalized_json, normalized_sha256, immutable)
        VALUES ('reference-v1','REFERENCE','Reference-compatible',NULL,$1,$2,$3,$4,$5,$6,TRUE)
	        ON CONFLICT (version_id) DO NOTHING`, domain.ParameterContractVersion, mustJSON(shared), mustJSON(agents), mustJSON(fixed), normalized, profileSHA)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO load_mapping_profiles
        (version_id, display_name, mapping_type, parameters_json, result_unit, normalized_json, normalized_sha256, immutable)
        VALUES ('identity-v1','Load proxy (average current)','identity','{}'::jsonb,'A',$1,$2,TRUE)
        ON CONFLICT (version_id) DO NOTHING`, mappingJSON, domain.SHA256Hex(mappingJSON))
	if err != nil {
		return err
	}
	return r.VerifyReferenceProfile(ctx)
}

// ConfigureParameterConstraints records the result of loading the explicit
// local constraints file. A load failure is intentionally retained so the
// running service can report a stable readiness failure instead of falling
// back to an implicit allowlist.
func (r *Repository) ConfigureParameterConstraints(document *parameters.Document, loadErr error) {
	r.parameterConstraints = document
	r.parameterConstraintsErr = loadErr
}

// VerifyParameterConstraints ensures every editable REFERENCE leaf has one
// complete declarative constraint. It never discloses local file paths.
func (r *Repository) VerifyParameterConstraints() error {
	if r.parameterConstraintsErr != nil || r.parameterConstraints == nil {
		return parameterConstraintsError()
	}
	if err := r.parameterConstraints.ValidateReference(referenceSharedParameters()); err != nil {
		return parameterConstraintsError()
	}
	return nil
}

// ParameterConstraints returns the declarative API metadata only after the
// complete document has passed the same readiness validation.
func (r *Repository) ParameterConstraints() (map[string]any, []string, error) {
	if err := r.VerifyParameterConstraints(); err != nil {
		return nil, nil, err
	}
	return r.parameterConstraints.APIResponse(), r.parameterConstraints.EditablePaths(), nil
}

func (r *Repository) ReferenceProfile(ctx context.Context) (ProfileRecord, MappingRecord, error) {
	var profile ProfileRecord
	var mode string
	var normalized json.RawMessage
	err := r.pool.QueryRow(ctx, `SELECT version_id, mode, display_name, base_version_id, shared_parameters, agents_json, fixed_items, normalized_json, normalized_sha256, immutable, created_at, updated_at
		FROM parameter_profiles WHERE version_id=$1`, referenceProfileVersionID).Scan(&profile.VersionID, &mode, &profile.DisplayName, &profile.BaseVersionID, &profile.Shared, &profile.Agents, &profile.FixedItems, &normalized, &profile.NormalizedSHA256, &profile.Immutable, &profile.CreatedAt, &profile.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProfileRecord{}, MappingRecord{}, referenceProfileIntegrityError()
	}
	if err != nil {
		return ProfileRecord{}, MappingRecord{}, err
	}
	if err := validateReferenceProfile(normalized, profile.NormalizedSHA256); err != nil {
		return ProfileRecord{}, MappingRecord{}, err
	}
	profile.Mode = domain.RunMode(mode)
	var mapping MappingRecord
	err = r.pool.QueryRow(ctx, `SELECT version_id, display_name, mapping_type, parameters_json, result_unit, normalized_sha256
        FROM load_mapping_profiles WHERE version_id='identity-v1'`).Scan(&mapping.VersionID, &mapping.DisplayName, &mapping.MappingType, &mapping.Parameters, &mapping.ResultUnit, &mapping.NormalizedSHA256)
	return profile, mapping, err
}

// CreateCustomProfile validates and materializes a complete CUSTOM profile.
// display_name is intentionally excluded from the canonical payload so it
// cannot turn equivalent immutable parameter values into duplicate versions.
func (r *Repository) CreateCustomProfile(ctx context.Context, input ProfileInput) (ProfileRecord, bool, error) {
	if err := validateProfileDisplayName(input.DisplayName); err != nil {
		return ProfileRecord{}, false, err
	}
	if err := normalizeCustomProfileBase(&input); err != nil {
		return ProfileRecord{}, false, err
	}
	shared, agents, fixed, normalized, normalizedSHA, err := r.materializeCustomProfile(ctx, input)
	if err != nil {
		return ProfileRecord{}, false, err
	}
	return r.persistCustomProfile(ctx, input.DisplayName, input.BaseVersionID, shared, agents, fixed, normalized, normalizedSHA)
}

func (r *Repository) materializeCustomProfile(ctx context.Context, input ProfileInput) (map[string]any, []map[string]any, map[string]any, json.RawMessage, string, error) {
	if err := r.VerifyParameterConstraints(); err != nil {
		return nil, nil, nil, nil, "", err
	}
	referenceShared, referenceAgents, fixed, err := r.referenceProfileComponents(ctx)
	if err != nil {
		return nil, nil, nil, nil, "", err
	}
	materialized, err := r.parameterConstraints.Materialize(referenceShared, referenceAgents, input.Shared, input.Agents)
	if err != nil {
		return nil, nil, nil, nil, "", parameterValidationError(err)
	}
	profile := map[string]any{
		"contract_version":  domain.ParameterContractVersion,
		"mode":              domain.RunModeCustom,
		"base_version_id":   input.BaseVersionID,
		"shared_parameters": materialized.Shared,
		"agents":            materialized.Agents,
		"fixed_items":       fixed,
	}
	normalized, err := domain.CanonicalJSON(profile)
	if err != nil {
		return nil, nil, nil, nil, "", err
	}
	return materialized.Shared, materialized.Agents, fixed, normalized, domain.SHA256Hex(normalized), nil
}

func (r *Repository) persistCustomProfile(ctx context.Context, displayName, baseVersionID string, shared map[string]any, agents []map[string]any, fixed map[string]any, normalized json.RawMessage, normalizedSHA string) (ProfileRecord, bool, error) {
	if existing, found, err := r.exactCustomProfileByNormalized(ctx, normalized, normalizedSHA, baseVersionID); err != nil || found {
		return existing, found, err
	}
	versionID := newOpaqueID("pp")
	_, err := r.pool.Exec(ctx, `INSERT INTO parameter_profiles
        (version_id, mode, display_name, base_version_id, contract_version, shared_parameters, agents_json, fixed_items, normalized_json, normalized_sha256, immutable)
	        VALUES ($1,'CUSTOM',$2,$3,$4,$5,$6,$7,$8,$9,TRUE)`, versionID, displayName, baseVersionID, domain.ParameterContractVersion, mustJSON(shared), mustJSON(agents), mustJSON(fixed), normalized, normalizedSHA)
	if err != nil {
		if isNormalizedProfileUniqueViolation(err) {
			existing, found, lookupErr := r.exactCustomProfileByNormalized(ctx, normalized, normalizedSHA, baseVersionID)
			if lookupErr != nil || found {
				return existing, found, lookupErr
			}
			return ProfileRecord{}, false, customProfileConflictError()
		}
		return ProfileRecord{}, false, err
	}
	now := r.now()
	return ProfileRecord{VersionID: versionID, Mode: domain.RunModeCustom, DisplayName: displayName, BaseVersionID: &baseVersionID, Shared: mustJSON(shared), Agents: mustJSON(agents), FixedItems: mustJSON(fixed), NormalizedSHA256: normalizedSHA, Immutable: true, CreatedAt: now, UpdatedAt: now}, false, nil
}

func (r *Repository) GetParameterProfile(ctx context.Context, versionID string) (ProfileRecord, error) {
	var profile ProfileRecord
	var mode string
	err := r.pool.QueryRow(ctx, `SELECT version_id,mode,display_name,base_version_id,shared_parameters,agents_json,fixed_items,normalized_sha256,immutable,created_at,updated_at
		FROM parameter_profiles WHERE version_id=$1`, versionID).Scan(&profile.VersionID, &mode, &profile.DisplayName, &profile.BaseVersionID, &profile.Shared, &profile.Agents, &profile.FixedItems, &profile.NormalizedSHA256, &profile.Immutable, &profile.CreatedAt, &profile.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProfileRecord{}, StableError{Code: "PROFILE_NOT_FOUND", Message: "Parameter profile was not found."}
	}
	profile.Mode = domain.RunMode(mode)
	return profile, err
}

// UpdateCustomProfileDisplayName changes only a human-readable CUSTOM alias.
// The update trigger rejects every canonical field and REFERENCE profile, while
// updated_at provides an auditable timestamp without changing the hash.
func (r *Repository) UpdateCustomProfileDisplayName(ctx context.Context, versionID, displayName string) (ProfileRecord, error) {
	if err := validateProfileDisplayName(displayName); err != nil {
		return ProfileRecord{}, err
	}
	var record ProfileRecord
	var mode string
	err := r.pool.QueryRow(ctx, `UPDATE parameter_profiles
        SET display_name=$2
        WHERE version_id=$1 AND mode='CUSTOM' AND immutable=TRUE
        RETURNING version_id,mode,display_name,base_version_id,shared_parameters,agents_json,fixed_items,normalized_sha256,immutable,created_at,updated_at`, versionID, displayName).
		Scan(&record.VersionID, &mode, &record.DisplayName, &record.BaseVersionID, &record.Shared, &record.Agents, &record.FixedItems, &record.NormalizedSHA256, &record.Immutable, &record.CreatedAt, &record.UpdatedAt)
	if err == nil {
		record.Mode = domain.RunMode(mode)
		return record, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ProfileRecord{}, err
	}
	var exists bool
	if err := r.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM parameter_profiles WHERE version_id=$1)", versionID).Scan(&exists); err != nil {
		return ProfileRecord{}, err
	}
	if !exists {
		return ProfileRecord{}, StableError{Code: "PROFILE_NOT_FOUND", Message: "Parameter profile was not found."}
	}
	return ProfileRecord{}, StableError{Code: "REFERENCE_CONFIG_IMMUTABLE", Field: "display_name", Message: "The immutable reference profile alias cannot be changed.", Recoverable: false}
}

func validateProfileDisplayName(displayName string) error {
	if !utf8.ValidString(displayName) || strings.TrimSpace(displayName) != displayName || displayName == "" || utf8.RuneCountInString(displayName) > 128 {
		return StableError{Code: "REQUEST_INVALID", Field: "display_name", Message: "display_name must be non-blank valid UTF-8 with at most 128 characters.", Recoverable: true}
	}
	for _, character := range displayName {
		if unicode.IsControl(character) {
			return StableError{Code: "REQUEST_INVALID", Field: "display_name", Message: "display_name must not contain control characters.", Recoverable: true}
		}
	}
	return nil
}

func (r *Repository) ListParameterProfiles(ctx context.Context, limit int) ([]ProfileRecord, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `SELECT version_id,mode,display_name,base_version_id,shared_parameters,agents_json,fixed_items,normalized_sha256,immutable,created_at,updated_at
        FROM parameter_profiles ORDER BY created_at DESC,version_id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	profiles := make([]ProfileRecord, 0, limit)
	for rows.Next() {
		var profile ProfileRecord
		var mode string
		if err := rows.Scan(&profile.VersionID, &mode, &profile.DisplayName, &profile.BaseVersionID, &profile.Shared, &profile.Agents, &profile.FixedItems, &profile.NormalizedSHA256, &profile.Immutable, &profile.CreatedAt, &profile.UpdatedAt); err != nil {
			return nil, err
		}
		profile.Mode = domain.RunMode(mode)
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

// ImportCustomProfile applies exactly the same constraints and canonical
// materialization as POST. Exported identity fields are verified; the source
// version ID, timestamp, and display name never alter canonical equivalence.
func (r *Repository) ImportCustomProfile(ctx context.Context, input ImportProfileInput) (ProfileRecord, bool, error) {
	if input.ContractVersion != domain.ParameterContractVersion {
		return ProfileRecord{}, false, StableError{Code: "REQUEST_INVALID", Field: "contract_version", Message: "The parameter profile contract version is not supported.", Recoverable: true}
	}
	if input.Mode != domain.RunModeCustom {
		return ProfileRecord{}, false, StableError{Code: "PARAMETER_NOT_ALLOWED", Field: "mode", Message: "Only CUSTOM profiles can be imported.", Recoverable: true}
	}
	if !input.Immutable {
		return ProfileRecord{}, false, StableError{Code: "REQUEST_INVALID", Field: "immutable", Message: "Imported parameter profiles must be immutable.", Recoverable: true}
	}
	if err := normalizeCustomProfileBase(&input.ProfileInput); err != nil {
		return ProfileRecord{}, false, err
	}
	shared, agents, fixed, normalized, normalizedSHA, err := r.materializeCustomProfile(ctx, input.ProfileInput)
	if err != nil {
		return ProfileRecord{}, false, err
	}
	providedFixed, err := domain.CanonicalJSON(input.FixedItems)
	expectedFixed, expectedErr := domain.CanonicalJSON(fixed)
	if err != nil || expectedErr != nil || !bytes.Equal(providedFixed, expectedFixed) {
		return ProfileRecord{}, false, StableError{Code: "PARAMETER_NOT_ALLOWED", Field: "fixed_items", Message: "Fixed S1 topology items cannot be changed.", Recoverable: true}
	}
	if input.NormalizedSHA256 != normalizedSHA {
		return ProfileRecord{}, false, StableError{Code: "REQUEST_INVALID", Field: "normalized_sha256", Message: "The imported parameter profile hash does not match its canonical values.", Recoverable: true}
	}
	return r.persistCustomProfile(ctx, input.DisplayName, input.BaseVersionID, shared, agents, fixed, normalized, normalizedSHA)
}

func normalizeCustomProfileBase(input *ProfileInput) error {
	if input.BaseVersionID == "" {
		input.BaseVersionID = referenceProfileVersionID
	}
	if input.BaseVersionID != referenceProfileVersionID {
		return StableError{Code: "PARAMETER_NOT_ALLOWED", Field: "base_version_id", Message: "CUSTOM profiles must use the immutable reference base.", Recoverable: true}
	}
	return nil
}

func (r *Repository) exactCustomProfileByNormalized(ctx context.Context, expectedNormalized json.RawMessage, expectedSHA, expectedBaseVersionID string) (ProfileRecord, bool, error) {
	var record ProfileRecord
	var mode string
	var normalized json.RawMessage
	err := r.pool.QueryRow(ctx, `SELECT version_id,mode,display_name,base_version_id,shared_parameters,agents_json,fixed_items,normalized_json,normalized_sha256,immutable,created_at,updated_at
		FROM parameter_profiles WHERE normalized_sha256=$1`, expectedSHA).Scan(&record.VersionID, &mode, &record.DisplayName, &record.BaseVersionID, &record.Shared, &record.Agents, &record.FixedItems, &normalized, &record.NormalizedSHA256, &record.Immutable, &record.CreatedAt, &record.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProfileRecord{}, false, nil
	}
	if err != nil {
		return ProfileRecord{}, false, err
	}
	record.Mode = domain.RunMode(mode)
	if err := validateExactCustomProfile(record, normalized, expectedNormalized, expectedSHA, expectedBaseVersionID); err != nil {
		return ProfileRecord{}, false, err
	}
	return record, true, nil
}

func validateExactCustomProfile(record ProfileRecord, normalized, expectedNormalized json.RawMessage, expectedSHA, expectedBaseVersionID string) error {
	canonical, err := domain.CanonicalJSON(normalized)
	if err != nil || domain.SHA256Hex(canonical) != expectedSHA || !bytes.Equal(canonical, expectedNormalized) {
		return customProfileConflictError()
	}
	if record.Mode != domain.RunModeCustom || !record.Immutable || record.BaseVersionID == nil || *record.BaseVersionID != expectedBaseVersionID || record.NormalizedSHA256 != expectedSHA {
		return customProfileConflictError()
	}
	return nil
}

func isNormalizedProfileUniqueViolation(err error) bool {
	var databaseError *pgconn.PgError
	return errors.As(err, &databaseError) && databaseError.Code == "23505" && databaseError.ConstraintName == "parameter_profiles_normalized_sha256_key"
}

func customProfileConflictError() StableError {
	return StableError{
		Code:        "REFERENCE_CONFIG_IMMUTABLE",
		Field:       "parameter_profile_version_id",
		Message:     "A conflicting immutable parameter profile already exists.",
		Recoverable: false,
	}
}

type MappingInput struct {
	DisplayName string
	MappingType string
	Parameters  map[string]any
	ResultUnit  string
}

func (r *Repository) CreateIdentityMapping(ctx context.Context, input MappingInput) (MappingRecord, bool, error) {
	if strings.TrimSpace(input.DisplayName) == "" {
		return MappingRecord{}, false, StableError{Code: "REQUEST_INVALID", Field: "display_name", Message: "display_name is required.", Recoverable: true}
	}
	if input.MappingType != "identity" {
		return MappingRecord{}, false, StableError{Code: "MAPPING_TYPE_NOT_APPROVED", Field: "mapping_type", Message: "Only identity mapping is approved in S1.", Recoverable: true}
	}
	if len(input.Parameters) != 0 {
		return MappingRecord{}, false, StableError{Code: "MAPPING_TYPE_NOT_APPROVED", Field: "parameters", Message: "Identity mapping does not accept parameters.", Recoverable: true}
	}
	if input.ResultUnit != "A" {
		return MappingRecord{}, false, StableError{Code: "MAPPING_TYPE_NOT_APPROVED", Field: "result_unit", Message: "Identity mapping uses the approved unit A.", Recoverable: true}
	}
	normalized, err := domain.CanonicalJSON(map[string]any{"mapping_type": "identity", "parameters": map[string]any{}, "result_unit": "A"})
	if err != nil {
		return MappingRecord{}, false, err
	}
	var existingID string
	err = r.pool.QueryRow(ctx, "SELECT version_id FROM load_mapping_profiles WHERE normalized_sha256=$1", domain.SHA256Hex(normalized)).Scan(&existingID)
	if err == nil {
		record, lookupErr := r.GetMappingProfile(ctx, existingID)
		return record, true, lookupErr
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return MappingRecord{}, false, err
	}
	versionID := newOpaqueID("map")
	_, err = r.pool.Exec(ctx, `INSERT INTO load_mapping_profiles(version_id,display_name,mapping_type,parameters_json,result_unit,normalized_json,normalized_sha256,immutable)
        VALUES($1,$2,'identity','{}'::jsonb,'A',$3,$4,TRUE)`, versionID, input.DisplayName, normalized, domain.SHA256Hex(normalized))
	if err != nil {
		return MappingRecord{}, false, err
	}
	return MappingRecord{VersionID: versionID, DisplayName: input.DisplayName, MappingType: "identity", Parameters: json.RawMessage("{}"), ResultUnit: "A", NormalizedSHA256: domain.SHA256Hex(normalized)}, false, nil
}

func (r *Repository) GetMappingProfile(ctx context.Context, versionID string) (MappingRecord, error) {
	var mapping MappingRecord
	err := r.pool.QueryRow(ctx, `SELECT version_id,display_name,mapping_type,parameters_json,result_unit,normalized_sha256
        FROM load_mapping_profiles WHERE version_id=$1`, versionID).Scan(&mapping.VersionID, &mapping.DisplayName, &mapping.MappingType, &mapping.Parameters, &mapping.ResultUnit, &mapping.NormalizedSHA256)
	if errors.Is(err, pgx.ErrNoRows) {
		return MappingRecord{}, StableError{Code: "MAPPING_NOT_FOUND", Message: "Load mapping profile was not found."}
	}
	return mapping, err
}

func (r *Repository) ListMappingProfiles(ctx context.Context, limit int) ([]MappingRecord, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `SELECT version_id,display_name,mapping_type,parameters_json,result_unit,normalized_sha256
        FROM load_mapping_profiles ORDER BY created_at DESC,version_id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	mappings := make([]MappingRecord, 0, limit)
	for rows.Next() {
		var mapping MappingRecord
		if err := rows.Scan(&mapping.VersionID, &mapping.DisplayName, &mapping.MappingType, &mapping.Parameters, &mapping.ResultUnit, &mapping.NormalizedSHA256); err != nil {
			return nil, err
		}
		mappings = append(mappings, mapping)
	}
	return mappings, rows.Err()
}

type SimulationRecord struct {
	RunID           string                  `json:"run_id"`
	DisplayName     *string                 `json:"display_name"`
	Status          domain.SimulationStatus `json:"status"`
	CurrentStage    *domain.Stage           `json:"current_stage"`
	QueuePosition   *int                    `json:"queue_position"`
	RunMode         domain.RunMode          `json:"run_mode"`
	SnapshotSHA256  string                  `json:"snapshot_sha256"`
	Snapshot        json.RawMessage         `json:"-"`
	CreatedAt       time.Time               `json:"created_at"`
	StartedAt       *time.Time              `json:"started_at"`
	FinishedAt      *time.Time              `json:"finished_at"`
	Error           json.RawMessage         `json:"-"`
	ArtifactState   string                  `json:"artifact_state"`
	LastHeartbeatAt *time.Time              `json:"last_heartbeat_at"`
	LatestEventID   int64                   `json:"latest_event_id"`
	StageDurations  json.RawMessage         `json:"stage_durations_ms"`
	EnqueueSequence int64                   `json:"-"`
}

// SimulationListQuery contains only API-validated list filters. It never
// queries worker_jobs, so DATASET_PREFLIGHT jobs cannot appear in a simulation
// queue or history response.
type SimulationListQuery struct {
	View                      string
	RunID                     string
	Status                    *domain.SimulationStatus
	DatasetID                 string
	ParameterProfileVersionID string
	RunMode                   *domain.RunMode
	Search                    string
	CreatedFrom               *time.Time
	CreatedTo                 *time.Time
	Cursor                    *SimulationListCursor
	Limit                     int
}

// SimulationListCursor is an already query-bound stable ordering position.
// The HTTP layer owns the opaque wire representation.
type SimulationListCursor struct {
	CreatedAt       time.Time
	RunID           string
	QueueBucket     int
	EnqueueSequence int64
}

type SimulationPage struct {
	Items   []SimulationRecord
	HasMore bool
	// Total is the exact number of persisted simulation rows matching the
	// requested view and filters. It deliberately excludes cursor and limit.
	Total int64
}

// CreateSimulation implements idempotent, atomic 1+10 admission. Preflight
// jobs share Worker FIFO but are excluded from the simulation capacity count.
func (r *Repository) CreateSimulation(ctx context.Context, key string, request domain.CreateSimulationRequest) (SimulationRecord, bool, error) {
	if err := r.runtime.Validate(); err != nil {
		return SimulationRecord{}, false, StableError{Code: "WORKER_RUNTIME_NOT_CONFIGURED", Message: "Simulation runtime identity is not configured.", Recoverable: true}
	}
	if len(key) < 16 || len(key) > 128 {
		return SimulationRecord{}, false, StableError{Code: "REQUEST_INVALID", Field: "Idempotency-Key", Message: "Idempotency-Key must contain 16 to 128 characters.", Recoverable: true}
	}
	if err := request.Validate(); err != nil {
		return SimulationRecord{}, false, toStableError(err)
	}
	requestJSON, err := canonicalSimulationRequest(request)
	if err != nil {
		return SimulationRecord{}, false, err
	}
	requestSHA := domain.SHA256Hex(requestJSON)

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return SimulationRecord{}, false, err
	}
	defer tx.Rollback(ctx)
	if err := lockScheduler(ctx, tx); err != nil {
		return SimulationRecord{}, false, err
	}

	var storedHash, previousRunID string
	err = tx.QueryRow(ctx, "SELECT request_sha256, run_id FROM idempotency_keys WHERE idempotency_key=$1 FOR UPDATE", key).Scan(&storedHash, &previousRunID)
	if err == nil {
		if storedHash != requestSHA {
			return SimulationRecord{}, false, StableError{Code: "IDEMPOTENCY_CONFLICT", Message: "The Idempotency-Key was previously used with a different request.", Recoverable: true}
		}
		record, err := simulationByID(ctx, tx, previousRunID)
		return record, true, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return SimulationRecord{}, false, err
	}

	var datasetStorageKey string
	var datasetHash string
	var datasetDisplayName string
	var datasetTimezone string
	var datasetStatus string
	// Dataset validity is terminal once VALID, while source bytes are immutable.
	// A plain MVCC read avoids requiring UPDATE solely for a parent-row lock; the
	// following FK insert still enforces referential integrity in this transaction.
	if err := tx.QueryRow(ctx, "SELECT storage_key, sha256, display_name, timezone, status FROM datasets WHERE dataset_id=$1", request.DatasetID).Scan(&datasetStorageKey, &datasetHash, &datasetDisplayName, &datasetTimezone, &datasetStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SimulationRecord{}, false, StableError{Code: "DATASET_NOT_VALID", Field: "dataset_id", Message: "Dataset does not exist or is not VALID.", Recoverable: true}
		}
		return SimulationRecord{}, false, err
	}
	if domain.DatasetStatus(datasetStatus) != domain.DatasetValid {
		return SimulationRecord{}, false, StableError{Code: "DATASET_NOT_VALID", Field: "dataset_id", Message: "Dataset must complete Worker preflight before simulation.", Recoverable: true}
	}

	var profileMode string
	var profileDisplayName string
	var profileJSON json.RawMessage
	var profileSHA string
	// Parameter content is immutable; only the display alias can change. The
	// selected alias and canonical material are copied into the same snapshot.
	if err := tx.QueryRow(ctx, "SELECT mode, display_name, normalized_json, normalized_sha256 FROM parameter_profiles WHERE version_id=$1", request.ParameterProfileVersionID).Scan(&profileMode, &profileDisplayName, &profileJSON, &profileSHA); err != nil {
		return SimulationRecord{}, false, StableError{Code: "PROFILE_NOT_FOUND", Field: "parameter_profile_version_id", Message: "Parameter profile was not found.", Recoverable: true}
	}
	if string(request.RunMode) != profileMode {
		return SimulationRecord{}, false, StableError{Code: "REFERENCE_CONFIG_IMMUTABLE", Field: "run_mode", Message: "Run mode must match the immutable parameter profile.", Recoverable: true}
	}
	if request.ParameterProfileVersionID == referenceProfileVersionID {
		if err := validateReferenceProfile(profileJSON, profileSHA); err != nil {
			return SimulationRecord{}, false, err
		}
	}
	var mappingType string
	var mappingJSON json.RawMessage
	var mappingSHA string
	// S1 mapping profiles are immutable identity mappings, so no parent-row lock
	// is needed before the simulation FK is inserted.
	if err := tx.QueryRow(ctx, "SELECT mapping_type, normalized_json, normalized_sha256 FROM load_mapping_profiles WHERE version_id=$1", request.LoadMappingVersionID).Scan(&mappingType, &mappingJSON, &mappingSHA); err != nil {
		return SimulationRecord{}, false, StableError{Code: "MAPPING_NOT_FOUND", Field: "load_mapping_version_id", Message: "Load mapping profile was not found.", Recoverable: true}
	}
	if mappingType != "identity" {
		return SimulationRecord{}, false, StableError{Code: "MAPPING_TYPE_NOT_APPROVED", Field: "load_mapping_version_id", Message: "Only the identity mapping is approved in S1.", Recoverable: true}
	}

	var active, waiting int
	if err := tx.QueryRow(ctx, "SELECT count(*) FILTER (WHERE status IN ('RUNNING','CANCELLING','GENERATING_ARTIFACTS')), count(*) FILTER (WHERE status='QUEUED') FROM simulations").Scan(&active, &waiting); err != nil {
		return SimulationRecord{}, false, err
	}
	if (active > 0 && waiting >= domain.SimulationWaitCapacity) || (active == 0 && waiting >= domain.SimulationWaitCapacity+1) {
		return SimulationRecord{}, false, StableError{Code: "QUEUE_FULL", Message: "The simulation queue has one execution slot and ten waiting slots.", Recoverable: true}
	}

	runID := newOpaqueID("run")
	jobID := newOpaqueID("job")
	sequence, err := nextSequence(ctx, tx)
	if err != nil {
		return SimulationRecord{}, false, err
	}
	snapshot, err := buildSnapshot(runID, request, datasetHash, datasetDisplayName, datasetTimezone, profileDisplayName, profileJSON, profileSHA, mappingJSON, mappingSHA, r.runtime, r.now().UTC())
	if err != nil {
		return SimulationRecord{}, false, err
	}
	snapshotJSON, err := domain.CanonicalJSON(snapshot)
	if err != nil {
		return SimulationRecord{}, false, err
	}
	snapshotSHA := domain.SHA256Hex(snapshotJSON)
	if _, err := canonicalWorkerDatasetPath(request.DatasetID, datasetStorageKey); err != nil {
		return SimulationRecord{}, false, StableError{Code: "DATASET_NOT_VALID", Field: "dataset_id", Message: "Dataset source storage path is invalid.", Recoverable: true}
	}
	envelope, err := buildSimulationEnvelope(jobID, runID, request, snapshot, datasetStorageKey)
	if err != nil {
		return SimulationRecord{}, false, err
	}
	envelopeJSON, err := domain.CanonicalJSON(envelope)
	if err != nil {
		return SimulationRecord{}, false, err
	}

	_, err = tx.Exec(ctx, `INSERT INTO simulations
        (run_id, display_name, dataset_id, parameter_profile_version_id, load_mapping_version_id, run_mode, status, current_stage, enqueue_seq)
        VALUES ($1,$2,$3,$4,$5,$6,'QUEUED',NULL,$7)`, runID, nullIfBlank(request.DisplayName), request.DatasetID, request.ParameterProfileVersionID, request.LoadMappingVersionID, request.RunMode, sequence)
	if err != nil {
		return SimulationRecord{}, false, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO simulation_snapshots(run_id, snapshot_json, snapshot_sha256) VALUES($1,$2,$3)", runID, snapshotJSON, snapshotSHA)
	if err != nil {
		return SimulationRecord{}, false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO worker_jobs(job_id, job_type, dataset_id, run_id, envelope_json, envelope_sha256, enqueue_seq, status)
        VALUES($1,'SIMULATION',$2,$3,$4,$5,$6,'QUEUED')`, jobID, request.DatasetID, runID, envelopeJSON, domain.SHA256Hex(envelopeJSON), sequence)
	if err != nil {
		return SimulationRecord{}, false, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO simulation_events(run_id,event_type,payload_json) VALUES($1,'simulation.state',jsonb_build_object('status','QUEUED'))", runID)
	if err != nil {
		return SimulationRecord{}, false, err
	}
	if _, err := tx.Exec(ctx, "SELECT emit_queue_position_events()"); err != nil {
		return SimulationRecord{}, false, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO idempotency_keys(idempotency_key,request_sha256,run_id) VALUES($1,$2,$3)", key, requestSHA, runID)
	if err != nil {
		return SimulationRecord{}, false, err
	}
	// Read the just-persisted row before commit so a successful 202 always has
	// the database-authoritative display name, timestamps, and full snapshot.
	record, err := simulationByID(ctx, tx, runID)
	if err != nil {
		return SimulationRecord{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SimulationRecord{}, false, err
	}
	return record, false, nil
}

func (r *Repository) CancelSimulation(ctx context.Context, runID, reason string) (SimulationRecord, error) {
	// scheduler_control is the cancellation/claim serialization point. Read
	// committed avoids returning a raw serialization error after a claim has
	// released that lock while preserving the lock's total transition order.
	tx, err := r.pool.BeginTx(ctx, cancellationTxOptions())
	if err != nil {
		return SimulationRecord{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockScheduler(ctx, tx); err != nil {
		return SimulationRecord{}, err
	}
	record, err := simulationByIDForUpdate(ctx, tx, runID)
	if err != nil {
		return SimulationRecord{}, err
	}
	switch record.Status {
	case domain.SimulationQueued:
		_, err = tx.Exec(ctx, "UPDATE simulations SET status='CANCELLED', finished_at=now(), artifact_state='INCOMPLETE' WHERE run_id=$1", runID)
		if err == nil {
			_, err = tx.Exec(ctx, "UPDATE worker_jobs SET status='CANCELLED' WHERE run_id=$1 AND status='QUEUED'", runID)
		}
		if err == nil {
			_, err = tx.Exec(ctx, "INSERT INTO simulation_events(run_id,event_type,payload_json) VALUES($1,'simulation.state',jsonb_build_object('previous_status','QUEUED','status','CANCELLED','reason',$2::text))", runID, reason)
		}
		if err == nil {
			_, err = tx.Exec(ctx, "SELECT emit_queue_position_events()")
		}
		record.Status, record.QueuePosition = domain.SimulationCancelled, nil
	case domain.SimulationRunning:
		_, err = tx.Exec(ctx, "UPDATE simulations SET status='CANCELLING', cancel_requested_at=now(), cancel_reason=$2 WHERE run_id=$1", runID, reason)
		if err == nil {
			_, err = tx.Exec(ctx, "UPDATE worker_jobs SET status='CANCELLING' WHERE run_id=$1 AND status='RUNNING'", runID)
		}
		if err == nil {
			_, err = tx.Exec(ctx, "INSERT INTO simulation_events(run_id,event_type,payload_json) VALUES($1,'simulation.state',jsonb_build_object('previous_status','RUNNING','status','CANCELLING','reason',$2::text))", runID, reason)
		}
		record.Status = domain.SimulationCancelling
	case domain.SimulationCancelling, domain.SimulationCancelled:
		// Idempotent success; state stays authoritative.
	default:
		return SimulationRecord{}, StableError{Code: "RUN_NOT_CANCELLABLE", Message: "The simulation is not cancellable in its current state.", Recoverable: false}
	}
	if err != nil {
		return SimulationRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SimulationRecord{}, err
	}
	return record, nil
}

func cancellationTxOptions() pgx.TxOptions {
	return pgx.TxOptions{IsoLevel: pgx.ReadCommitted}
}

func (r *Repository) GetSimulation(ctx context.Context, runID string) (SimulationRecord, error) {
	return scanSimulationRow(r.pool.QueryRow(ctx, simulationSelect+` WHERE s.run_id=$1`, runID))
}

// ListSimulations returns only persisted simulation rows. History ordering is
// fixed by the public contract. Queue ordering places the (at most one) active
// simulation before FIFO queued work, and remains stable across pages.
func (r *Repository) ListSimulations(ctx context.Context, query SimulationListQuery) (SimulationPage, error) {
	if query.View != "history" && query.View != "queue" {
		return SimulationPage{}, StableError{Code: "REQUEST_INVALID", Field: "view", Message: "view must be history or queue.", Recoverable: true}
	}
	if query.Limit < 1 || query.Limit > 500 {
		return SimulationPage{}, StableError{Code: "REQUEST_INVALID", Field: "limit", Message: "limit must be between 1 and 500.", Recoverable: true}
	}

	where, filterArgs := simulationListWhere(query)
	// A repeatable-read, read-only transaction makes the filtered COUNT(*) and
	// its page a single authoritative database view even if new simulations are
	// admitted while this request is executing.
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return SimulationPage{}, err
	}
	defer tx.Rollback(ctx)

	var total int64
	countArgs := append([]any(nil), filterArgs...)
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM simulations AS s JOIN simulation_snapshots AS ss ON ss.run_id=s.run_id"+where, countArgs...).Scan(&total); err != nil {
		return SimulationPage{}, err
	}

	pageWhere := where
	args := append([]any(nil), filterArgs...)
	bindCursor := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	orderBy := "s.created_at DESC, s.run_id DESC"
	if query.Cursor != nil {
		cursorClause := ""
		if query.View == "queue" {
			cursorClause = "(CASE WHEN s.status IN ('RUNNING','CANCELLING','GENERATING_ARTIFACTS') THEN 0 ELSE 1 END, s.enqueue_seq, s.run_id) > (" + bindCursor(query.Cursor.QueueBucket) + "," + bindCursor(query.Cursor.EnqueueSequence) + "," + bindCursor(query.Cursor.RunID) + ")"
		} else {
			cursorClause = "(s.created_at, s.run_id) < (" + bindCursor(query.Cursor.CreatedAt.UTC()) + "," + bindCursor(query.Cursor.RunID) + ")"
		}
		pageWhere = appendSimulationListClause(pageWhere, cursorClause)
	}
	if query.View == "queue" {
		orderBy = "CASE WHEN s.status IN ('RUNNING','CANCELLING','GENERATING_ARTIFACTS') THEN 0 ELSE 1 END ASC, s.enqueue_seq ASC, s.run_id ASC"
	}
	args = append(args, query.Limit+1)
	rows, err := tx.Query(ctx, simulationSelect+pageWhere+" ORDER BY "+orderBy+fmt.Sprintf(" LIMIT $%d", len(args)), args...)
	if err != nil {
		return SimulationPage{}, err
	}
	defer rows.Close()
	records := make([]SimulationRecord, 0, query.Limit+1)
	for rows.Next() {
		record, err := scanSimulationRow(rows)
		if err != nil {
			return SimulationPage{}, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return SimulationPage{}, err
	}
	return simulationPageFromRecords(records, query.Limit, total), nil
}

// simulationListWhere returns exactly the immutable filter plan shared by the
// authoritative total and the cursor-paginated query. Cursor is intentionally
// excluded because it defines a page boundary, not the filtered result set.
func simulationListWhere(query SimulationListQuery) (string, []any) {
	clauses := make([]string, 0, 10)
	args := make([]any, 0, 10)
	bind := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if query.View == "queue" {
		clauses = append(clauses, "s.status IN ('RUNNING','CANCELLING','GENERATING_ARTIFACTS','QUEUED')")
	}
	if query.RunID != "" {
		clauses = append(clauses, "s.run_id="+bind(query.RunID))
	}
	if query.Status != nil {
		clauses = append(clauses, "s.status="+bind(string(*query.Status)))
	}
	if query.DatasetID != "" {
		clauses = append(clauses, "s.dataset_id="+bind(query.DatasetID))
	}
	if query.ParameterProfileVersionID != "" {
		clauses = append(clauses, "s.parameter_profile_version_id="+bind(query.ParameterProfileVersionID))
	}
	if query.RunMode != nil {
		clauses = append(clauses, "s.run_mode="+bind(string(*query.RunMode)))
	}
	if query.Search != "" {
		needle := "%" + strings.ToLower(query.Search) + "%"
		clauses = append(clauses, "(lower(s.run_id) LIKE "+bind(needle)+" OR lower(COALESCE(s.display_name,'')) LIKE "+bind(needle)+" OR lower(COALESCE(ss.snapshot_json->'dataset'->>'display_name','')) LIKE "+bind(needle)+")")
	}
	if query.CreatedFrom != nil {
		clauses = append(clauses, "s.created_at >= "+bind(query.CreatedFrom.UTC()))
	}
	if query.CreatedTo != nil {
		clauses = append(clauses, "s.created_at <= "+bind(query.CreatedTo.UTC()))
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func appendSimulationListClause(where, clause string) string {
	if where == "" {
		return " WHERE " + clause
	}
	return where + " AND " + clause
}

func simulationPageFromRecords(records []SimulationRecord, limit int, total int64) SimulationPage {
	page := SimulationPage{Items: make([]SimulationRecord, 0, limit), Total: total}
	for _, record := range records {
		if len(page.Items) == limit {
			page.HasMore = true
			break
		}
		page.Items = append(page.Items, record)
	}
	return page
}

// RequireCompletedArtifacts is the common result-read gate. A terminal failure
// is intentionally not a result: its immutable detail and any separately
// committed diagnostic artifacts remain available through their own endpoints.
func (r *Repository) RequireCompletedArtifacts(ctx context.Context, runID string) (SimulationRecord, error) {
	record, err := r.GetSimulation(ctx, runID)
	if err != nil {
		return SimulationRecord{}, err
	}
	if err := r.requireCompletedArtifacts(ctx, record); err != nil {
		return SimulationRecord{}, err
	}
	return record, nil
}

// requireCompletedArtifacts is the single result-read gate shared by detail
// and result endpoints. File opening remains in the artifact readers, where
// regular-file, containment, size, and hash checks can be applied.
func (r *Repository) requireCompletedArtifacts(ctx context.Context, record SimulationRecord) error {
	if record.Status != domain.SimulationCompleted || record.ArtifactState != "COMMITTED" {
		return StableError{Code: "RESULT_NOT_READY", Message: "Simulation results are not available until artifacts are committed.", Recoverable: true}
	}
	var registered int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM artifacts
        WHERE run_id=$1 AND required=TRUE AND name = ANY($2)`, record.RunID, requiredResultArtifactNames()).Scan(&registered); err != nil {
		return err
	}
	if registered != len(requiredResultArtifactNames()) {
		return StableError{Code: "RESULT_NOT_READY", Message: "Simulation results are not available until required artifacts are registered.", Recoverable: true}
	}
	return nil
}

func requiredResultArtifactNames() []string {
	return []string{
		"run_manifest.json", "preprocessing_summary.json", "agent_partition_summary.csv", "feature_schema.json",
		"anchor_summary.json", "metrics.csv", "results_agent_1.csv", "results_agent_2.csv", "results_agent_3.csv",
		"alarms.csv", "diagnostics.json", "artifact_manifest.json",
	}
}

// RequireCommittedArtifacts is the inventory/download gate. It delegates to
// the strict result-read gate so detail, result, and artifact routes accept
// the same terminal, committed, fully registered run identity.
func (r *Repository) RequireCommittedArtifacts(ctx context.Context, runID string) (SimulationRecord, error) {
	record, err := r.RequireCompletedArtifacts(ctx, runID)
	if err != nil {
		var stable StableError
		if errors.As(err, &stable) && stable.Code == "RESULT_NOT_READY" {
			return SimulationRecord{}, StableError{Code: "ARTIFACT_NOT_AVAILABLE", Message: "Artifacts are not available until the committed directory is registered.", Recoverable: true}
		}
		return SimulationRecord{}, err
	}
	return record, nil
}

type ArtifactRecord struct {
	RunID        string
	Name         string
	RelativePath string
	MediaType    string
	SizeBytes    int64
	SHA256       string
	Required     bool
	CreatedAt    time.Time
}

type ArtifactInventory struct {
	ArtifactState  string
	ManifestSHA256 string
	Items          []ArtifactRecord
}

func (r *Repository) ListArtifacts(ctx context.Context, runID string) (ArtifactInventory, error) {
	record, err := r.RequireCommittedArtifacts(ctx, runID)
	if err != nil {
		return ArtifactInventory{}, err
	}
	rows, err := r.pool.Query(ctx, `SELECT run_id,name,relative_path,media_type,size_bytes,sha256,required,created_at
        FROM artifacts WHERE run_id=$1 ORDER BY name ASC`, runID)
	if err != nil {
		return ArtifactInventory{}, err
	}
	defer rows.Close()
	inventory := ArtifactInventory{ArtifactState: record.ArtifactState, Items: make([]ArtifactRecord, 0, 16)}
	for rows.Next() {
		var artifact ArtifactRecord
		if err := rows.Scan(&artifact.RunID, &artifact.Name, &artifact.RelativePath, &artifact.MediaType, &artifact.SizeBytes, &artifact.SHA256, &artifact.Required, &artifact.CreatedAt); err != nil {
			return ArtifactInventory{}, err
		}
		if artifact.Name == "artifact_manifest.json" {
			inventory.ManifestSHA256 = artifact.SHA256
		}
		inventory.Items = append(inventory.Items, artifact)
	}
	if err := rows.Err(); err != nil {
		return ArtifactInventory{}, err
	}
	if inventory.ManifestSHA256 == "" {
		return ArtifactInventory{}, StableError{Code: "ARTIFACT_NOT_AVAILABLE", Message: "The committed artifact manifest is not registered.", Recoverable: true}
	}
	return inventory, nil
}

func (r *Repository) GetArtifact(ctx context.Context, runID, name string) (ArtifactRecord, error) {
	if _, err := r.RequireCommittedArtifacts(ctx, runID); err != nil {
		return ArtifactRecord{}, err
	}
	var artifact ArtifactRecord
	err := r.pool.QueryRow(ctx, `SELECT run_id,name,relative_path,media_type,size_bytes,sha256,required,created_at
        FROM artifacts WHERE run_id=$1 AND name=$2`, runID, name).Scan(
		&artifact.RunID, &artifact.Name, &artifact.RelativePath, &artifact.MediaType,
		&artifact.SizeBytes, &artifact.SHA256, &artifact.Required, &artifact.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ArtifactRecord{}, StableError{Code: "ARTIFACT_NOT_FOUND", Field: "artifact_name", Message: "Artifact was not found in the committed manifest."}
	}
	if err != nil {
		return ArtifactRecord{}, err
	}
	return artifact, nil
}

type AlarmQuery struct {
	RunID     string
	Agent     int
	Levels    []string
	Types     []string
	From      *time.Time
	To        *time.Time
	IndexFrom *int64
	IndexTo   *int64
	AfterID   int64
	Limit     int
}

type AlarmRecord struct {
	AlarmID              int64
	RunID                string
	Agent                int
	OriginalRunningIndex int64
	Time                 *time.Time
	OverallAlarmLevel    string
	AlarmType            string
	Reasons              json.RawMessage
	LoadStatus           string
	ResultLocator        json.RawMessage
}

type AlarmPage struct {
	Items   []AlarmRecord
	HasMore bool
	// Total is the exact count after all alarm filters and before cursor/limit.
	Total int64
}

// ListAlarms reads the bounded, committed alarm index rather than opening a
// worker file. result_locator remains the Worker-supplied safe API locator,
// never an absolute filesystem path.
func (r *Repository) ListAlarms(ctx context.Context, query AlarmQuery) (AlarmPage, error) {
	if query.Agent < 1 || query.Agent > 3 {
		return AlarmPage{}, StableError{Code: "REQUEST_INVALID", Field: "agent", Message: "agent must be 1, 2, or 3.", Recoverable: true}
	}
	if query.Limit < 1 || query.Limit > 500 {
		return AlarmPage{}, StableError{Code: "REQUEST_INVALID", Field: "limit", Message: "limit must be between 1 and 500.", Recoverable: true}
	}
	where, filterArgs := alarmListWhere(query)
	// Count and page share one read-only snapshot, so the public total is never
	// inferred from the current page and cannot race a concurrent index commit.
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return AlarmPage{}, err
	}
	defer tx.Rollback(ctx)
	var total int64
	countArgs := append([]any(nil), filterArgs...)
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM alarm_index"+where, countArgs...).Scan(&total); err != nil {
		return AlarmPage{}, err
	}

	pageWhere := where
	args := append([]any(nil), filterArgs...)
	bind := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if query.AfterID > 0 {
		pageWhere = appendAlarmListClause(pageWhere, "alarm_id > "+bind(query.AfterID))
	}
	args = append(args, query.Limit+1)
	rows, err := tx.Query(ctx, `SELECT alarm_id,run_id,agent,original_running_index,time_value,overall_alarm_level,
        alarm_type,reasons_json,load_status,result_locator_json FROM alarm_index`+pageWhere+` ORDER BY alarm_id ASC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return AlarmPage{}, err
	}
	defer rows.Close()
	records := make([]AlarmRecord, 0, query.Limit+1)
	for rows.Next() {
		var alarm AlarmRecord
		if err := rows.Scan(&alarm.AlarmID, &alarm.RunID, &alarm.Agent, &alarm.OriginalRunningIndex, &alarm.Time, &alarm.OverallAlarmLevel, &alarm.AlarmType, &alarm.Reasons, &alarm.LoadStatus, &alarm.ResultLocator); err != nil {
			return AlarmPage{}, err
		}
		records = append(records, alarm)
	}
	if err := rows.Err(); err != nil {
		return AlarmPage{}, err
	}
	return alarmPageFromRecords(records, query.Limit, total), nil
}

// alarmListWhere contains all result-set filters shared by the authoritative
// count and the cursor-paginated read. Cursor is only a page boundary.
func alarmListWhere(query AlarmQuery) (string, []any) {
	clauses := []string{"run_id=$1", "agent=$2"}
	args := []any{query.RunID, query.Agent}
	bind := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if len(query.Levels) > 0 {
		clauses = append(clauses, "overall_alarm_level = ANY("+bind(query.Levels)+")")
	}
	if len(query.Types) > 0 {
		clauses = append(clauses, "alarm_type = ANY("+bind(query.Types)+")")
	}
	if query.From != nil {
		clauses = append(clauses, "time_value >= "+bind(query.From.UTC()))
	}
	if query.To != nil {
		clauses = append(clauses, "time_value <= "+bind(query.To.UTC()))
	}
	if query.IndexFrom != nil {
		clauses = append(clauses, "original_running_index >= "+bind(*query.IndexFrom))
	}
	if query.IndexTo != nil {
		clauses = append(clauses, "original_running_index <= "+bind(*query.IndexTo))
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func appendAlarmListClause(where, clause string) string {
	return where + " AND " + clause
}

func alarmPageFromRecords(records []AlarmRecord, limit int, total int64) AlarmPage {
	page := AlarmPage{Items: make([]AlarmRecord, 0, limit), Total: total}
	for _, record := range records {
		if len(page.Items) == limit {
			page.HasMore = true
			break
		}
		page.Items = append(page.Items, record)
	}
	return page
}

// RerunRequest reconstructs a new admission request from the original run's
// immutable identity. It defaults to the original saved parameter version; a
// caller may explicitly choose another already-saved version for the new run.
func (r *Repository) RerunRequest(ctx context.Context, runID, parameterProfileVersionID string) (domain.CreateSimulationRequest, error) {
	var datasetID, originalProfileVersionID, mappingVersionID, mode, sourceStatus string
	var displayName *string
	var snapshot json.RawMessage
	err := r.pool.QueryRow(ctx, `SELECT s.dataset_id,s.parameter_profile_version_id,s.load_mapping_version_id,s.run_mode,s.status,s.display_name,ss.snapshot_json
        FROM simulations s JOIN simulation_snapshots ss ON ss.run_id=s.run_id WHERE s.run_id=$1`, runID).
		Scan(&datasetID, &originalProfileVersionID, &mappingVersionID, &mode, &sourceStatus, &displayName, &snapshot)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CreateSimulationRequest{}, StableError{Code: "RUN_NOT_FOUND", Message: "Simulation was not found."}
	}
	if err != nil {
		return domain.CreateSimulationRequest{}, err
	}
	if err := rerunSourceStatusError(domain.SimulationStatus(sourceStatus)); err != nil {
		return domain.CreateSimulationRequest{}, err
	}
	datasetID, originalProfileVersionID, mappingVersionID, mode, err = snapshotRerunIdentity(snapshot, datasetID, originalProfileVersionID, mappingVersionID, mode)
	if err != nil {
		return domain.CreateSimulationRequest{}, err
	}
	seed, err := snapshotMasterSeed(snapshot)
	if err != nil {
		return domain.CreateSimulationRequest{}, err
	}
	selectedProfile := originalProfileVersionID
	selectedMode := domain.RunMode(mode)
	if strings.TrimSpace(parameterProfileVersionID) != "" && parameterProfileVersionID != originalProfileVersionID {
		var selectedModeRaw string
		err := r.pool.QueryRow(ctx, "SELECT mode FROM parameter_profiles WHERE version_id=$1 AND immutable=TRUE", parameterProfileVersionID).Scan(&selectedModeRaw)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CreateSimulationRequest{}, StableError{Code: "PROFILE_NOT_FOUND", Field: "parameter_profile_version_id", Message: "Parameter profile was not found.", Recoverable: true}
		}
		if err != nil {
			return domain.CreateSimulationRequest{}, err
		}
		selectedProfile = parameterProfileVersionID
		selectedMode = domain.RunMode(selectedModeRaw)
	}
	request := domain.CreateSimulationRequest{
		DatasetID: datasetID, RunMode: selectedMode, ParameterProfileVersionID: selectedProfile, LoadMappingVersionID: mappingVersionID,
		AgentOverrides: []domain.AgentOverride{{Agent: 1, Parameters: map[string]any{}}, {Agent: 2, Parameters: map[string]any{}}, {Agent: 3, Parameters: map[string]any{}}},
		Seed:           seed,
	}
	if displayName != nil {
		request.DisplayName = *displayName
	}
	return request, nil
}

// rerunSourceStatusError prevents rerun admission from allocating any new
// snapshot or Worker job until the source run has a frozen terminal outcome.
func rerunSourceStatusError(status domain.SimulationStatus) error {
	if domain.IsTerminal(status) {
		return nil
	}
	return StableError{Code: "RUN_NOT_RERUNNABLE", Field: "run_id", Message: "Only a terminal simulation can be rerun.", Recoverable: false}
}

// snapshotRerunIdentity takes the frozen values when present. The database
// columns are retained only as a compatibility fallback for snapshots written
// before explicit mapping-version material was introduced.
func snapshotRerunIdentity(snapshot json.RawMessage, fallbackDataset, fallbackProfile, fallbackMapping, fallbackMode string) (string, string, string, string, error) {
	stored, err := rawJSONObject(snapshot, "simulation snapshot")
	if err != nil {
		return "", "", "", "", StableError{Code: "REFERENCE_CONFIG_IMMUTABLE", Field: "run_id", Message: "The immutable simulation snapshot is invalid.", Recoverable: false}
	}
	dataset, datasetOK := stored["dataset"].(map[string]any)
	datasetID, datasetIDOK := dataset["dataset_id"].(string)
	profileID, profileOK := stored["parameter_profile_version_id"].(string)
	mode, modeOK := stored["run_mode"].(string)
	if !datasetOK || !datasetIDOK || !profileOK || !modeOK {
		return "", "", "", "", StableError{Code: "REFERENCE_CONFIG_IMMUTABLE", Field: "run_id", Message: "The immutable simulation snapshot is invalid.", Recoverable: false}
	}
	mappingID, mappingOK := stored["load_mapping_version_id"].(string)
	if !mappingOK || mappingID == "" {
		mappingID = fallbackMapping
	}
	if datasetID != fallbackDataset || profileID != fallbackProfile || mode != fallbackMode || mappingID == "" {
		return "", "", "", "", StableError{Code: "REFERENCE_CONFIG_IMMUTABLE", Field: "run_id", Message: "The immutable simulation snapshot does not match its task identity.", Recoverable: false}
	}
	return datasetID, profileID, mappingID, mode, nil
}

func snapshotMasterSeed(snapshot json.RawMessage) (int64, error) {
	stored, err := rawJSONObject(snapshot, "simulation snapshot")
	if err != nil {
		return 0, StableError{Code: "REFERENCE_CONFIG_IMMUTABLE", Field: "run_id", Message: "The immutable simulation snapshot is invalid.", Recoverable: false}
	}
	runtime, runtimeOK := stored["runtime"].(map[string]any)
	seed, seedOK := runtime["master_seed"]
	if !runtimeOK || !seedOK {
		return 0, StableError{Code: "REFERENCE_CONFIG_IMMUTABLE", Field: "run_id", Message: "The immutable simulation snapshot is invalid.", Recoverable: false}
	}
	switch value := seed.(type) {
	case float64:
		if value == 0 || value != float64(int64(value)) {
			break
		}
		return int64(value), nil
	case int64:
		if value != 0 {
			return value, nil
		}
	}
	return 0, StableError{Code: "REFERENCE_CONFIG_IMMUTABLE", Field: "run_id", Message: "The immutable simulation snapshot is invalid.", Recoverable: false}
}

type SimulationEvent struct {
	EventID    int64
	EventType  string
	Payload    json.RawMessage
	OccurredAt time.Time
}

// EventsAfter reads persisted SSE events and detects an expired cursor. It does
// not depend on a process-local pub/sub queue for correctness.
func (r *Repository) EventsAfter(ctx context.Context, runID string, lastEventID int64, limit int) (bool, []SimulationEvent, int64, error) {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	var minimum, latest int64
	if err := r.pool.QueryRow(ctx, `SELECT COALESCE(min(event_id),0), COALESCE(max(event_id),0) FROM simulation_events WHERE run_id=$1`, runID).Scan(&minimum, &latest); err != nil {
		return false, nil, 0, err
	}
	if lastEventID > 0 && minimum > 0 && lastEventID < minimum-1 {
		return true, nil, latest, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT event_id,event_type,payload_json,occurred_at FROM simulation_events
        WHERE run_id=$1 AND event_id>$2 ORDER BY event_id ASC LIMIT $3`, runID, lastEventID, limit)
	if err != nil {
		return false, nil, latest, err
	}
	defer rows.Close()
	events := make([]SimulationEvent, 0, limit)
	for rows.Next() {
		var event SimulationEvent
		if err := rows.Scan(&event.EventID, &event.EventType, &event.Payload, &event.OccurredAt); err != nil {
			return false, nil, latest, err
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err == nil {
			payload["run_id"] = runID
			payload["occurred_at"] = event.OccurredAt.UTC().Format(time.RFC3339Nano)
			event.Payload = mustJSON(payload)
		}
		events = append(events, event)
	}
	return false, events, latest, rows.Err()
}

type LeasedJob struct {
	JobID          string
	JobType        domain.JobType
	RunID          *string
	Envelope       json.RawMessage
	LeaseExpiresAt time.Time
}

// WorkerObservationStatus is derived from the durable worker_instances record.
// It is intentionally not inferred from an in-process Worker heartbeat.
type WorkerObservationStatus string

const (
	WorkerNotObserved WorkerObservationStatus = "not_observed"
	WorkerObserved    WorkerObservationStatus = "ok"
	WorkerStale       WorkerObservationStatus = "stale"
)

type WorkerObservation struct {
	Status          WorkerObservationStatus
	WorkerID        string
	WorkerVersion   string
	LastHeartbeatAt *time.Time
}

// WorkerObservation returns the newest persisted compatible Worker heartbeat.
// An absent or stale Worker is an advisory readiness condition; a query error
// means the control-plane database is not able to provide the observation.
func (r *Repository) WorkerObservation(ctx context.Context, maximumAge time.Duration) (WorkerObservation, error) {
	if maximumAge <= 0 {
		return WorkerObservation{}, fmt.Errorf("worker observation maximum age must be positive")
	}
	seconds := int64((maximumAge + time.Second - 1) / time.Second)
	var observation WorkerObservation
	var observedAt time.Time
	var fresh bool
	err := r.pool.QueryRow(ctx, `SELECT worker_id,worker_version,last_heartbeat_at,
        last_heartbeat_at >= now() - make_interval(secs => $1::integer)
        FROM worker_instances
        WHERE worker_contract_version=$2
        ORDER BY last_heartbeat_at DESC,worker_id ASC
        LIMIT 1`, seconds, domain.WorkerContractVersion).Scan(&observation.WorkerID, &observation.WorkerVersion, &observedAt, &fresh)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkerObservation{Status: WorkerNotObserved}, nil
	}
	if err != nil {
		return WorkerObservation{}, err
	}
	observation.LastHeartbeatAt = &observedAt
	if fresh {
		observation.Status = WorkerObserved
	} else {
		observation.Status = WorkerStale
	}
	return observation, nil
}

// WorkerRegistration is the bounded Worker identity recorded before a Worker
// can claim a task. It is separate from a job lease so readiness can observe
// an idle Worker.
type WorkerRegistration struct {
	WorkerID        string
	ContractVersion string
	WorkerVersion   string
}

func (r *Repository) RegisterWorker(ctx context.Context, registration WorkerRegistration) (time.Time, error) {
	var observedAt time.Time
	err := r.pool.QueryRow(ctx, "SELECT worker_register_instance($1,$2,$3)", registration.WorkerID, registration.ContractVersion, registration.WorkerVersion).Scan(&observedAt)
	return observedAt, err
}

func (r *Repository) HeartbeatWorker(ctx context.Context, workerID, contractVersion string) (bool, error) {
	var accepted bool
	err := r.pool.QueryRow(ctx, "SELECT worker_heartbeat_instance($1,$2)", workerID, contractVersion).Scan(&accepted)
	return accepted, err
}

// ClaimNextJobForWorker performs the Worker-scoped FIFO claim. The database
// validates a fresh registration and returns no job when the shared slot queue
// is empty.
func (r *Repository) ClaimNextJobForWorker(ctx context.Context, workerID, attemptID, leaseToken string, leaseSeconds int) (*LeasedJob, error) {
	var job LeasedJob
	var jobType string
	err := r.pool.QueryRow(ctx, "SELECT job_id,job_type,run_id,envelope_json,lease_expires_at FROM worker_claim_next_job_for_worker($1,$2,$3,$4)", workerID, attemptID, leaseToken, leaseSeconds).Scan(&job.JobID, &jobType, &job.RunID, &job.Envelope, &job.LeaseExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	job.JobType = domain.JobType(jobType)
	if err := bindClaimEnvelope(&job, attemptID, leaseToken); err != nil {
		return nil, err
	}
	return &job, nil
}

// ClaimNextJob is the narrow Worker Repository lease operation. It relies on
// the migration function rather than an in-memory queue.
func (r *Repository) ClaimNextJob(ctx context.Context, attemptID, leaseToken string, leaseSeconds int) (*LeasedJob, error) {
	var job LeasedJob
	var jobType string
	err := r.pool.QueryRow(ctx, "SELECT job_id,job_type,run_id,envelope_json,lease_expires_at FROM worker_claim_next_job($1,$2,$3)", attemptID, leaseToken, leaseSeconds).Scan(&job.JobID, &jobType, &job.RunID, &job.Envelope, &job.LeaseExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	job.JobType = domain.JobType(jobType)
	if err := bindClaimEnvelope(&job, attemptID, leaseToken); err != nil {
		return nil, err
	}
	return &job, nil
}

func bindClaimEnvelope(job *LeasedJob, attemptID, leaseToken string) error {
	var envelope map[string]any
	if err := json.Unmarshal(job.Envelope, &envelope); err != nil {
		return fmt.Errorf("decode claimed job envelope: %w", err)
	}
	output, ok := envelope["output"].(map[string]any)
	if !ok {
		return errors.New("claimed job envelope has no output object")
	}
	switch job.JobType {
	case domain.JobTypeSimulation:
		if job.RunID == nil || *job.RunID == "" {
			return errors.New("simulation claim has no run identifier")
		}
		output["relative_tmp_directory"] = fmt.Sprintf("runs/%s/tmp/%s", *job.RunID, attemptID)
	case domain.JobTypePreflight:
		dataset, ok := envelope["dataset"].(map[string]any)
		if !ok {
			return errors.New("preflight claim has no dataset object")
		}
		datasetID, ok := dataset["dataset_id"].(string)
		if !ok || datasetID == "" {
			return errors.New("preflight claim has no dataset identifier")
		}
		output["relative_tmp_directory"] = fmt.Sprintf("datasets/%s/preflight/tmp/%s", datasetID, attemptID)
	default:
		return fmt.Errorf("unsupported claimed job type %q", job.JobType)
	}
	envelope["attempt_id"] = attemptID
	envelope["lease_token"] = leaseToken
	encoded, err := domain.CanonicalJSON(envelope)
	if err != nil {
		return fmt.Errorf("encode claimed job envelope: %w", err)
	}
	job.Envelope = encoded
	return nil
}

func (r *Repository) Heartbeat(ctx context.Context, jobID, attemptID, leaseToken string, leaseSeconds int) (bool, error) {
	var accepted bool
	err := r.pool.QueryRow(ctx, "SELECT worker_heartbeat($1,$2,$3,$4)", jobID, attemptID, leaseToken, leaseSeconds).Scan(&accepted)
	return accepted, err
}

// HeartbeatWorkerLease renews both the durable Worker observation and a
// lease-scoped task heartbeat. It is the Worker-facing operation.
func (r *Repository) HeartbeatWorkerLease(ctx context.Context, workerID, jobID, attemptID, leaseToken string, leaseSeconds int) (bool, error) {
	var accepted bool
	err := r.pool.QueryRow(ctx, "SELECT worker_heartbeat_for_worker($1,$2,$3,$4,$5)", workerID, jobID, attemptID, leaseToken, leaseSeconds).Scan(&accepted)
	return accepted, err
}

type CancellationContext struct {
	CancelRequested   bool
	CancelRequestedAt *time.Time
	LeaseValid        bool
}

// CancellationContext reads persisted cancellation intent without exposing
// control-plane tables to the Worker database role.
func (r *Repository) CancellationContext(ctx context.Context, jobID, attemptID, leaseToken string) (CancellationContext, error) {
	var cancellation CancellationContext
	err := r.pool.QueryRow(ctx, "SELECT cancel_requested,cancel_requested_at,lease_valid FROM worker_cancellation_context($1,$2,$3)", jobID, attemptID, leaseToken).Scan(&cancellation.CancelRequested, &cancellation.CancelRequestedAt, &cancellation.LeaseValid)
	if errors.Is(err, pgx.ErrNoRows) {
		return CancellationContext{LeaseValid: false}, nil
	}
	return cancellation, err
}

// ReportWorkerEvent persists a lease-scoped worker.event.v1 event. Simulation
// events are projected in the same database transaction for SSE replay.
func (r *Repository) ReportWorkerEvent(ctx context.Context, jobID, attemptID, leaseToken string, event json.RawMessage) (bool, error) {
	var accepted bool
	err := r.pool.QueryRow(ctx, "SELECT worker_report_event($1,$2,$3,$4)", jobID, attemptID, leaseToken, event).Scan(&accepted)
	return accepted, err
}

func (r *Repository) ReportStage(ctx context.Context, jobID, attemptID, leaseToken string, stage domain.Stage, agent *int, diagnostics json.RawMessage) (bool, error) {
	var agentValue *int16
	if agent != nil {
		converted := int16(*agent)
		agentValue = &converted
	}
	var accepted bool
	err := r.pool.QueryRow(ctx, "SELECT worker_report_stage($1,$2,$3,$4,$5,$6)", jobID, attemptID, leaseToken, stage, agentValue, diagnostics).Scan(&accepted)
	return accepted, err
}

func (r *Repository) RecoverExpiredLeases(ctx context.Context) (int64, error) {
	var recovered int64
	err := r.pool.QueryRow(ctx, "SELECT worker_recover_expired_leases()").Scan(&recovered)
	return recovered, err
}

func (r *Repository) CompletePreflight(ctx context.Context, jobID, attemptID, leaseToken, inputSHA string, summary json.RawMessage, summarySHA string) (bool, error) {
	var accepted bool
	err := r.pool.QueryRow(ctx, "SELECT worker_complete_preflight($1,$2,$3,$4,$5,$6)", jobID, attemptID, leaseToken, inputSHA, summary, summarySHA).Scan(&accepted)
	return accepted, err
}

func (r *Repository) ConfirmCancelled(ctx context.Context, jobID, attemptID, leaseToken string) (bool, error) {
	var accepted bool
	err := r.pool.QueryRow(ctx, "SELECT worker_confirm_cancel($1,$2,$3)", jobID, attemptID, leaseToken).Scan(&accepted)
	return accepted, err
}

func (r *Repository) FailJob(ctx context.Context, jobID, attemptID, leaseToken string, workerError json.RawMessage, recoverable bool) (bool, error) {
	var accepted bool
	err := r.pool.QueryRow(ctx, "SELECT worker_fail_job($1,$2,$3,$4,$5)", jobID, attemptID, leaseToken, workerError, recoverable).Scan(&accepted)
	return accepted, err
}

type ArtifactCommit struct {
	ManifestSHA256 string
	Artifacts      json.RawMessage
	Alarms         json.RawMessage
	StageDurations json.RawMessage
}

// CommitSimulation records artifact metadata and the COMPLETED terminal state
// only after Worker has atomically renamed its fully verified committed folder.
func (r *Repository) CommitSimulation(ctx context.Context, jobID, attemptID, leaseToken string, commit ArtifactCommit) (bool, error) {
	var accepted bool
	err := r.pool.QueryRow(ctx, "SELECT worker_commit_simulation($1,$2,$3,$4,$5,$6,$7)", jobID, attemptID, leaseToken, commit.ManifestSHA256, commit.Artifacts, commit.Alarms, commit.StageDurations).Scan(&accepted)
	return accepted, err
}

func lockScheduler(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SELECT singleton FROM scheduler_control WHERE singleton = TRUE FOR UPDATE")
	return err
}

func nextSequence(ctx context.Context, tx pgx.Tx) (int64, error) {
	var value int64
	err := tx.QueryRow(ctx, "SELECT nextval('enqueue_sequence')").Scan(&value)
	return value, err
}

func simulationByID(ctx context.Context, tx pgx.Tx, runID string) (SimulationRecord, error) {
	return scanSimulation(ctx, tx, "", runID)
}

func simulationByIDForUpdate(ctx context.Context, tx pgx.Tx, runID string) (SimulationRecord, error) {
	return scanSimulation(ctx, tx, " FOR UPDATE", runID)
}

const simulationSelect = `SELECT
    s.run_id, s.display_name, s.status, s.current_stage, s.run_mode,
    ss.snapshot_sha256, ss.snapshot_json, s.created_at, s.started_at,
    s.finished_at, s.error_json, s.artifact_state, s.last_heartbeat_at,
    s.enqueue_seq,
    CASE WHEN s.status='QUEUED' THEN (
        SELECT count(*) FROM simulations AS queued_run
         WHERE queued_run.status='QUEUED' AND queued_run.enqueue_seq <= s.enqueue_seq
    ) ELSE NULL END AS queue_position,
    COALESCE((SELECT max(event_id) FROM simulation_events AS latest_event WHERE latest_event.run_id=s.run_id), 0) AS latest_event_id,
    COALESCE((
        SELECT payload_json->'stage_durations_ms' FROM simulation_events AS artifact_event
         WHERE artifact_event.run_id=s.run_id AND artifact_event.event_type='artifact.committed'
         ORDER BY artifact_event.event_id DESC LIMIT 1
    ), '{}'::jsonb) AS stage_durations_ms
FROM simulations AS s JOIN simulation_snapshots AS ss ON ss.run_id=s.run_id`

type rowScanner interface {
	Scan(...any) error
}

func scanSimulation(ctx context.Context, tx pgx.Tx, suffix, runID string) (SimulationRecord, error) {
	return scanSimulationRow(tx.QueryRow(ctx, simulationSelect+` WHERE s.run_id=$1`+suffix, runID))
}

func scanSimulationRow(row rowScanner) (SimulationRecord, error) {
	var record SimulationRecord
	var status, mode string
	var stage *string
	var queuePosition *int64
	var errorJSON []byte
	err := row.Scan(
		&record.RunID, &record.DisplayName, &status, &stage, &mode,
		&record.SnapshotSHA256, &record.Snapshot, &record.CreatedAt,
		&record.StartedAt, &record.FinishedAt, &errorJSON, &record.ArtifactState,
		&record.LastHeartbeatAt, &record.EnqueueSequence, &queuePosition,
		&record.LatestEventID, &record.StageDurations,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return SimulationRecord{}, StableError{Code: "RUN_NOT_FOUND", Message: "Simulation was not found."}
	}
	if err != nil {
		return SimulationRecord{}, err
	}
	record.Error = json.RawMessage(errorJSON)
	record.Status, record.RunMode = domain.SimulationStatus(status), domain.RunMode(mode)
	if stage != nil {
		converted := domain.Stage(*stage)
		record.CurrentStage = &converted
	}
	if queuePosition != nil {
		position := int(*queuePosition)
		record.QueuePosition = &position
	}
	return record, nil
}

func canonicalSimulationRequest(request domain.CreateSimulationRequest) ([]byte, error) {
	agents := append([]domain.AgentOverride(nil), request.AgentOverrides...)
	sort.Slice(agents, func(left, right int) bool { return agents[left].Agent < agents[right].Agent })
	return domain.CanonicalJSON(map[string]any{
		"dataset_id": request.DatasetID, "run_mode": request.RunMode, "parameter_profile_version_id": request.ParameterProfileVersionID,
		"load_mapping_version_id": request.LoadMappingVersionID, "agent_overrides": agents, "seed": request.Seed, "display_name": request.DisplayName,
	})
}

func buildSnapshot(runID string, request domain.CreateSimulationRequest, datasetHash, datasetDisplayName, timezone, profileDisplayName string, profileJSON json.RawMessage, profileSHA string, mappingJSON json.RawMessage, mappingSHA string, runtime RuntimeIdentity, now time.Time) (map[string]any, error) {
	effective, err := effectiveParameterSnapshot(profileJSON)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"contract_version": "simulation.snapshot.v1", "run_id": runID, "created_at": now.Format(time.RFC3339Nano), "run_mode": request.RunMode,
		"dataset":                      map[string]any{"dataset_id": request.DatasetID, "display_name": datasetDisplayName, "sha256": datasetHash, "timezone": timezone, "columns": domain.RequiredColumns()},
		"parameter_profile_version_id": request.ParameterProfileVersionID, "parameter_profile_display_name": profileDisplayName,
		"parameter_snapshot": json.RawMessage(profileJSON), "parameter_sha256": profileSHA, "parameter_effective": effective,
		"load_mapping_version_id": request.LoadMappingVersionID,
		"mapping_snapshot":        json.RawMessage(mappingJSON), "mapping_sha256": mappingSHA,
		"agents":                  referenceAgents(),
		"field_standard_snapshot": fieldStandardSnapshot(),
		"runtime":                 workerRuntime(runtime, request.Seed),
	}, nil
}

// effectiveParameterSnapshot derives each Agent's full effective parameters
// from the profile already frozen in the same simulation snapshot. It never
// reads a mutable parameter profile after admission.
func effectiveParameterSnapshot(profileJSON json.RawMessage) (map[string]any, error) {
	profile, err := rawJSONObject(profileJSON, "parameter profile")
	if err != nil {
		return nil, err
	}
	shared, ok := profile["shared_parameters"].(map[string]any)
	if !ok || len(shared) == 0 {
		return nil, errors.New("stored parameter profile has no shared parameters")
	}
	agents, ok := profile["agents"].([]any)
	if !ok || len(agents) != 3 {
		return nil, errors.New("stored parameter profile has an invalid Agent collection")
	}
	effectiveAgents := make([]map[string]any, 0, len(agents))
	seen := make(map[int]bool, len(agents))
	for _, rawAgent := range agents {
		agent, ok := rawAgent.(map[string]any)
		if !ok {
			return nil, errors.New("stored parameter profile has an invalid Agent entry")
		}
		identifier, identifierOK := numberAsInt(agent["agent"])
		segment, segmentOK := agent["segment"].(string)
		overrides, overridesOK := agent["parameters"].(map[string]any)
		if !identifierOK || identifier < 1 || identifier > 3 || seen[identifier] || !segmentOK || !overridesOK {
			return nil, errors.New("stored parameter profile has an invalid Agent entry")
		}
		seen[identifier] = true
		full, err := mergeParameterMaps(shared, overrides)
		if err != nil {
			return nil, err
		}
		effectiveAgents = append(effectiveAgents, map[string]any{"agent": identifier, "segment": segment, "parameters": full})
	}
	sort.Slice(effectiveAgents, func(left, right int) bool {
		return effectiveAgents[left]["agent"].(int) < effectiveAgents[right]["agent"].(int)
	})
	if len(seen) != 3 {
		return nil, errors.New("stored parameter profile has an incomplete Agent collection")
	}
	return map[string]any{"shared_parameters": cloneParameterMap(shared), "agents": effectiveAgents}, nil
}

func mergeParameterMaps(base, overrides map[string]any) (map[string]any, error) {
	merged := cloneParameterMap(base)
	for key, override := range overrides {
		baseValue, exists := merged[key]
		if !exists {
			return nil, errors.New("stored Agent override has an unknown parameter path")
		}
		if overrideObject, isObject := override.(map[string]any); isObject {
			baseObject, baseIsObject := baseValue.(map[string]any)
			if !baseIsObject {
				return nil, errors.New("stored Agent override changes a parameter shape")
			}
			mergedObject, err := mergeParameterMaps(baseObject, overrideObject)
			if err != nil {
				return nil, err
			}
			merged[key] = mergedObject
			continue
		}
		if _, baseIsObject := baseValue.(map[string]any); baseIsObject {
			return nil, errors.New("stored Agent override changes a parameter shape")
		}
		merged[key] = override
	}
	return merged, nil
}

func cloneParameterMap(source map[string]any) map[string]any {
	copy := make(map[string]any, len(source))
	for key, value := range source {
		if nested, ok := value.(map[string]any); ok {
			copy[key] = cloneParameterMap(nested)
		} else {
			copy[key] = value
		}
	}
	return copy
}

func numberAsInt(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case float64:
		if number == float64(int(number)) {
			return int(number), true
		}
	}
	return 0, false
}

func workerRuntime(identity RuntimeIdentity, masterSeed int64) map[string]any {
	return map[string]any{
		"algorithm_version": identity.AlgorithmVersion,
		"worker_version":    identity.WorkerVersion,
		"image_digest":      identity.WorkerImageDigest,
		"numeric_runtime":   identity.NumericRuntime,
		"master_seed":       masterSeed,
		"random_streams": map[string]any{
			"generator":                       "MT19937_TWISTER_COMPAT",
			"seed_mapping_version":            "reference-anchor-v1",
			"base_center_seed_by_agent":       seedMap(masterSeed),
			"transition_center_seed_by_agent": seedMap(masterSeed + 20),
			"boundary_seed_by_agent":          seedMap(masterSeed + 40),
			"public_anchor_seed":              masterSeed + 100,
		},
	}
}

func seedMap(seed int64) map[string]int64 {
	return map[string]int64{"1": seed + 1, "2": seed + 2, "3": seed + 3}
}

func fieldStandardMaterial() map[string]any {
	return map[string]any{
		"configuration_version": "platform-config.v1",
		"zl":                    map[string]any{"unit_symbol": nil, "validation_enabled": false},
		"sd":                    map[string]any{"unit_symbol": nil, "validation_enabled": false},
		"sampling":              map[string]any{"expected_period_ms": nil, "tolerance_ms": nil},
	}
}

func fieldStandardSHA256() string {
	canonical, err := domain.CanonicalJSON(fieldStandardMaterial())
	if err != nil {
		panic(err)
	}
	return domain.SHA256Hex(canonical)
}

func fieldStandardSnapshot() map[string]any {
	snapshot := fieldStandardMaterial()
	snapshot["sha256"] = fieldStandardSHA256()
	return snapshot
}

func buildSimulationEnvelope(jobID, runID string, request domain.CreateSimulationRequest, snapshot map[string]any, datasetStorageKey string) (map[string]any, error) {
	dataset, err := workerDatasetSnapshot(snapshot["dataset"], datasetStorageKey)
	if err != nil {
		return nil, err
	}
	parameterSnapshot, err := workerParameterSnapshot(request.ParameterProfileVersionID, snapshot["parameter_sha256"], snapshot["parameter_snapshot"])
	if err != nil {
		return nil, err
	}
	mappingSnapshot, err := workerMappingSnapshot(request.LoadMappingVersionID, snapshot["mapping_sha256"], snapshot["mapping_snapshot"])
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"contract_version": domain.WorkerContractVersion, "job_id": jobID, "job_type": domain.JobTypeSimulation, "run_id": runID,
		"dataset": dataset, "run_mode": request.RunMode, "parameter_snapshot": parameterSnapshot, "mapping_snapshot": mappingSnapshot,
		"field_standard_snapshot": snapshot["field_standard_snapshot"], "runtime": snapshot["runtime"],
		"output": map[string]any{"relative_tmp_directory": fmt.Sprintf("runs/%s/tmp", runID), "required_artifact_schema": "artifact.manifest.v1"},
		"limits": map[string]any{"memory_bytes": int64(10 * 1024 * 1024 * 1024), "cancel_check_target_ms": 5000},
	}, nil
}

// workerDatasetSnapshot turns the immutable dataset metadata in a simulation
// snapshot into the narrower Worker dataset contract. StorageKey is retained
// only in the immutable job envelope: list/detail/history/replay snapshots do
// not disclose storage layout.
func workerDatasetSnapshot(snapshotValue any, storageKey string) (map[string]any, error) {
	snapshot, ok := snapshotValue.(map[string]any)
	if !ok {
		return nil, errors.New("simulation snapshot has no dataset object")
	}
	if _, disclosed := snapshot["relative_path"]; disclosed {
		return nil, errors.New("simulation product snapshot must not contain a storage path")
	}
	datasetID, idOK := snapshot["dataset_id"].(string)
	sha, shaOK := snapshot["sha256"].(string)
	timezone, timezoneOK := snapshot["timezone"].(string)
	columns, columnsOK := snapshot["columns"].([]string)
	if !idOK || !shaOK || !timezoneOK || !columnsOK || !validWorkerOpaqueID(datasetID) || !validWorkerSHA256(sha) || timezone != "Asia/Shanghai" || !matchesRequiredDatasetColumns(columns) {
		return nil, errors.New("simulation snapshot has an invalid dataset object")
	}
	relativePath, err := canonicalWorkerDatasetPath(datasetID, storageKey)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"dataset_id": datasetID, "relative_path": relativePath, "sha256": sha, "timezone": timezone, "columns": columns,
	}, nil
}

func validWorkerOpaqueID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	matched, _ := regexp.MatchString(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`, value)
	return matched
}

func validWorkerSHA256(value string) bool {
	matched, _ := regexp.MatchString(`^[0-9a-f]{64}$`, value)
	return matched
}

func matchesRequiredDatasetColumns(columns []string) bool {
	expected := domain.RequiredColumns()
	if len(columns) != len(expected) {
		return false
	}
	for index := range expected {
		if columns[index] != expected[index] {
			return false
		}
	}
	return true
}

// canonicalWorkerDatasetPath is the shared admission boundary for the
// controlled storage layout enforced independently by worker.task.v1 semantic
// validation. It rejects paths that are absent, absolute, escaped, or merely
// different from the immutable dataset identifier's canonical source path.
func canonicalWorkerDatasetPath(datasetID, storageKey string) (string, error) {
	expected := "datasets/" + datasetID + "/source.csv"
	if !safeWorkerRelativePath(storageKey) || storageKey != expected {
		return "", errors.New("dataset storage key is not the controlled source path")
	}
	return expected, nil
}

func safeWorkerRelativePath(value string) bool {
	if value == "" || len(value) > 512 || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || strings.Contains(value, "//") || strings.HasSuffix(value, "/") {
		return false
	}
	if matched, _ := regexp.MatchString(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`, value); !matched {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}

func workerParameterSnapshot(versionID string, shaValue, raw any) (map[string]any, error) {
	profile, err := rawJSONObject(raw, "parameter profile")
	if err != nil {
		return nil, err
	}
	shared, sharedOK := profile["shared_parameters"].(map[string]any)
	agents, agentsOK := profile["agents"].([]any)
	fixed, fixedOK := profile["fixed_items"].(map[string]any)
	sha, shaOK := shaValue.(string)
	if !sharedOK || !agentsOK || !fixedOK || !shaOK {
		return nil, errors.New("stored parameter profile does not satisfy the worker snapshot contract")
	}
	return map[string]any{"version_id": versionID, "sha256": sha, "shared_parameters": shared, "agents": agents, "fixed_items": fixed}, nil
}

func workerMappingSnapshot(versionID string, shaValue, raw any) (map[string]any, error) {
	mapping, err := rawJSONObject(raw, "load mapping")
	if err != nil {
		return nil, err
	}
	mappingType, mappingTypeOK := mapping["mapping_type"].(string)
	parameters, parametersOK := mapping["parameters"].(map[string]any)
	resultUnit, resultUnitOK := mapping["result_unit"].(string)
	sha, shaOK := shaValue.(string)
	if !mappingTypeOK || !parametersOK || !resultUnitOK || !shaOK {
		return nil, errors.New("stored load mapping does not satisfy the worker snapshot contract")
	}
	return map[string]any{"version_id": versionID, "mapping_type": mappingType, "parameters": parameters, "result_unit": resultUnit, "sha256": sha}, nil
}

func rawJSONObject(raw any, name string) (map[string]any, error) {
	bytes, ok := raw.(json.RawMessage)
	if !ok {
		return nil, fmt.Errorf("stored %s is not JSON", name)
	}
	var decoded map[string]any
	if err := json.Unmarshal(bytes, &decoded); err != nil {
		return nil, fmt.Errorf("decode stored %s: %w", name, err)
	}
	return decoded, nil
}

// VerifyReferenceProfile checks the stored immutable reference material used
// by readiness and profile creation. It never repairs or rewrites a profile.
func (r *Repository) VerifyReferenceProfile(ctx context.Context) error {
	_, _, err := r.validatedReferenceProfile(ctx)
	return err
}

func (r *Repository) validatedReferenceProfile(ctx context.Context) (json.RawMessage, string, error) {
	var normalized json.RawMessage
	var sha string
	err := r.pool.QueryRow(ctx, `SELECT normalized_json, normalized_sha256
        FROM parameter_profiles
        WHERE version_id=$1 AND mode='REFERENCE' AND immutable=TRUE`, referenceProfileVersionID).Scan(&normalized, &sha)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", referenceProfileIntegrityError()
	}
	if err != nil {
		return nil, "", err
	}
	if err := validateReferenceProfile(normalized, sha); err != nil {
		return nil, "", err
	}
	return normalized, sha, nil
}

func (r *Repository) referenceProfileComponents(ctx context.Context) (map[string]any, []any, map[string]any, error) {
	normalized, _, err := r.validatedReferenceProfile(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	profile, err := rawJSONObject(normalized, "reference parameter profile")
	if err != nil {
		return nil, nil, nil, referenceProfileIntegrityError()
	}
	shared, sharedOK := profile["shared_parameters"].(map[string]any)
	agents, agentsOK := profile["agents"].([]any)
	fixed, fixedOK := profile["fixed_items"].(map[string]any)
	if !sharedOK || !agentsOK || !fixedOK {
		return nil, nil, nil, referenceProfileIntegrityError()
	}
	return shared, agents, fixed, nil
}

func referenceProfileSeed() (json.RawMessage, string, error) {
	profile := map[string]any{
		"contract_version":  domain.ParameterContractVersion,
		"mode":              domain.RunModeReference,
		"shared_parameters": referenceSharedParameters(),
		"agents":            referenceAgents(),
		"fixed_items":       referenceFixedItems(),
	}
	normalized, err := domain.CanonicalJSON(profile)
	if err != nil {
		return nil, "", err
	}
	return normalized, domain.SHA256Hex(normalized), nil
}

func validateReferenceProfile(normalized json.RawMessage, storedSHA string) error {
	profile, err := rawJSONObject(normalized, "reference parameter profile")
	if err != nil {
		return referenceProfileIntegrityError()
	}
	canonical, err := domain.CanonicalJSON(profile)
	if err != nil || domain.SHA256Hex(canonical) != storedSHA {
		return referenceProfileIntegrityError()
	}
	if profile["contract_version"] != domain.ParameterContractVersion || profile["mode"] != string(domain.RunModeReference) {
		return referenceProfileIntegrityError()
	}
	shared, sharedOK := profile["shared_parameters"].(map[string]any)
	agents, agentsOK := profile["agents"].([]any)
	fixed, fixedOK := profile["fixed_items"].(map[string]any)
	if !sharedOK || len(shared) == 0 || !agentsOK || len(agents) != 3 || !fixedOK {
		return referenceProfileIntegrityError()
	}
	for _, group := range []string{"feature_state", "cleaning", "split", "trend"} {
		values, ok := shared[group].(map[string]any)
		if !ok || len(values) == 0 {
			return referenceProfileIntegrityError()
		}
	}
	expectedFixed, err := domain.CanonicalJSON(referenceFixedItems())
	actualFixed, actualErr := domain.CanonicalJSON(fixed)
	if err != nil || actualErr != nil || !bytes.Equal(expectedFixed, actualFixed) {
		return referenceProfileIntegrityError()
	}
	return nil
}

func referenceProfileIntegrityError() StableError {
	return StableError{
		Code:        "REFERENCE_CONFIG_IMMUTABLE",
		Field:       "parameter_profile_version_id",
		Message:     "The immutable reference parameter profile is missing or invalid.",
		Recoverable: false,
	}
}

func parameterConstraintsError() StableError {
	return StableError{
		Code:        "PARAMETER_CONSTRAINTS_INVALID",
		Field:       "parameter_constraints",
		Message:     "The parameter constraints configuration is missing or invalid.",
		Recoverable: false,
	}
}

func parameterValidationError(err error) error {
	var validation parameters.ValidationError
	if errors.As(err, &validation) {
		return StableError{Code: validation.Code, Field: validation.Field, Message: validation.Message, Recoverable: true}
	}
	return parameterConstraintsError()
}

func referenceAgents() []map[string]any {
	return []map[string]any{
		{"agent": 1, "segment": "EARLY", "parameters": map[string]any{}},
		{"agent": 2, "segment": "MIDDLE", "parameters": map[string]any{}},
		{"agent": 3, "segment": "LATE", "parameters": map[string]any{}},
	}
}

func referenceFixedItems() map[string]any {
	return map[string]any{"agent_count": 3, "feature_dimension_formula": "4*nLag+32", "leave_one_out_global_model": true, "predict_then_update": true, "agent_override_whitelist": []any{}}
}

func referenceSharedParameters() map[string]any {
	return map[string]any{
		"feature_state":    map[string]any{"nLag": 8, "speed_threshold": 0.01, "current_threshold": 1.0},
		"cleaning":         map[string]any{"median_window": 21, "mad_factor": 5, "smoothing_window": 5},
		"split":            map[string]any{"training_ratio": 0.70, "calibration_ratio": 0.15, "minimum_training": 80, "minimum_calibration": 30, "minimum_testing": 30, "agent_count": 3},
		"local_gp":         map[string]any{"kNN": 100, "adaptive_ratio": 0.10, "ell": 5.0, "sigma_f": 1.0, "sigma_n": 0.10, "minimum_regularization": 0.01},
		"trend":            map[string]any{"threshold": 1.0, "maximum_mixing": 0.75, "gain": 1.0, "maximum_step_change": 2.5},
		"interval":         map[string]any{"confidence": 0.95, "calibration_window": 300, "minimum_scores": 20, "std_floor": 0.20, "calibration_scale_min": 0.5, "calibration_scale_max": 10.0, "half_width_min": 1.0, "half_width_max": 8.0, "coverage_window": 200, "update_mode": "all_finite", "variance_floor": 1e-8},
		"anchors":          map[string]any{"base_centers": 100, "transition_centers": 30, "boundary_centers": 20, "transition_quantile": 0.75, "public_anchors": 300, "iterations": 60, "random_seed": 2026},
		"support":          map[string]any{"scale_multiple": 2.5, "minimum_weight": 1e-5, "minimum_query_support": 0.03, "full_weight_reference": 0.35},
		"global_surrogate": map[string]any{"ell": 5.0, "minimum_regularization": 1e-4, "noise_ratio": 0.25, "cholesky_attempts": 10, "leave_one_out": true},
		"fusion":           map[string]any{"maximum_global_weight": 0.98, "initial_improvement": 0.001, "error_window": 50, "minimum_samples": 20, "win_margin": 0.05, "variance_weight": 0.25, "winsor_quantile": 0.90, "global_clear_threshold": 0.85, "neutral_upper_limit": 0.70, "persistence": 5, "rise_smoothing": 0.85, "fall_smoothing": 0.55, "disagreement_kappa": 2.5, "maximum_variance_ratio": 2.0},
		"alarms":           map[string]any{"imbalance_threshold": 0.15, "notice_count": 1, "warning_count": 3, "alarm_count": 5, "absolute_current_threshold": nil, "absolute_tension_threshold": nil},
	}
}

func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func newOpaqueID(prefix string) string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		panic(fmt.Sprintf("random identifier: %v", err))
	}
	return prefix + "_" + hex.EncodeToString(bytes)
}

// NewOpaqueID creates an API-safe opaque identifier for a Backend-owned
// resource before its atomic file and database transaction are coordinated.
func NewOpaqueID(prefix string) string {
	return newOpaqueID(prefix)
}

func nullIfBlank(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}

func toStableError(err error) error {
	var field domain.FieldError
	if errors.As(err, &field) {
		return StableError{Code: field.Code, Field: field.Field, Message: field.Message, Recoverable: true}
	}
	return StableError{Code: "REQUEST_INVALID", Message: err.Error(), Recoverable: true}
}
