/* Testing-only API fixtures for contract adapter and UI tests. Never imported by the live entry point. */
import { DATASET_COLUMNS } from "../api/contract.js";

const time = "2026-08-16T08:00:00.000+08:00";
const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";

export const fixtureDatasetValidating = {
  dataset_id: "ds_fixture_validating", display_name: "strict-seven-columns.csv", original_filename: "strict-seven-columns.csv", status: "VALIDATING", sha256: hash, size_bytes: 84123,
  columns: DATASET_COLUMNS, timezone: "Asia/Shanghai", utc_offset: "+08:00", validation_started_at: null, validation_finished_at: null, created_at: time,
  preflight: { job_id: "job_fixture_preflight", status: "QUEUED", queue_position: 1, stage: null, attempt_id: null, lease_state: "NOT_CLAIMED", latest_event_id: 27, contract_version: "preprocessing.v1", error: null },
  algorithm_preprocessing: null, warnings: []
};

export const fixtureDatasetValid = {
  ...fixtureDatasetValidating, dataset_id: "ds_fixture_valid", original_filename: "aligned_4dzdl_zl_sd.csv", status: "VALID", preflight: { ...fixtureDatasetValidating.preflight, status: "COMPLETED", queue_position: null, stage: "PREPROCESSING", attempt_id: "attempt_fixture_preflight_1", lease_state: "RELEASED", latest_event_id: 51 },
  validation_started_at: time, validation_finished_at: "2026-08-16T08:01:02.000+08:00",
  algorithm_preprocessing: {
    schema_version: "dataset-preflight.summary.v1", preprocessing_contract_version: "preprocessing.v1", input_sha256: hash,
    counts: { raw_rows: 129438, invalid_numeric_rows: 0, stop_rows: 79444, suspicious_rows: 376, running_rows: 49618, spike_rows: 4502 },
    time: { start: "2026-08-16T08:00:00.000+08:00", end: "2026-08-16T08:41:20.000+08:00", parse_failed_count: 0, non_monotonic_count: 2, sampling_period_ms: { median: 1000, min: 1000, max: 1000 } },
    filter_path: { source_order: "preserved", profile: "reference-compatible" }, parameters: { median_window: 21, mad_factor: 5 }, summary_sha256: hash
  },
  warnings: [{ code: "TIME_NOT_MONOTONIC", count: 2, message: "The source order was preserved." }]
};

export const fixtureReferenceProfile: any = {
  mode: "REFERENCE", version_id: "reference-v1", display_name: "Reference-compatible", immutable: true, contract_version: "parameter-profile.v1",
  shared_parameters: {
    feature_state: { nLag: 8, speed_threshold: .01, current_threshold: 1 },
    cleaning: { median_window: 21, mad_factor: 5, smoothing_window: 5 },
    split: { training_ratio: .7, calibration_ratio: .15, minimum_training: 80, minimum_calibration: 30, minimum_testing: 30, agent_count: 3 },
    local_gp: { kNN: 100, adaptive_ratio: .1, ell: 5, sigma_f: 1, sigma_n: .1, minimum_regularization: .01 },
    trend: { threshold: 1, maximum_mixing: .75, gain: 1, maximum_step_change: 2.5 },
    interval: { confidence: .95, calibration_window: 300, minimum_scores: 20, standard_deviation_floor: .2, calibration_scale_min: .5, calibration_scale_max: 10, half_width_min: 1, half_width_max: 8, coverage_window: 200, update_mode: "all_finite", variance_floor: 1e-8 },
    anchors: { base_centers: 100, transition_centers: 30, boundary_centers: 20, transition_quantile: .75, public_anchors: 300, iterations: 60, random_seed: 2026 },
    support: { scale_multiple: 2.5, minimum_weight: 1e-5, minimum_query_support: .03, full_weight_reference: .35 },
    global_surrogate: { ell: 5, minimum_regularization: 1e-4, noise_ratio: .25, cholesky_attempts: 10, leave_one_out: true },
    fusion: { maximum_global_weight: .98, initial_improvement: .001, error_window: 50, minimum_samples: 20, win_margin: .05, variance_weight: .25, winsor_quantile: .9, global_clear_threshold: .85, neutral_upper_limit: .7, persistence: 5, rise_smoothing: .85, fall_smoothing: .55, disagreement_kappa: 2.5, maximum_variance_ratio: 2 },
    alarms: { imbalance_threshold: .15, notice_count: 1, warning_count: 3, alarm_count: 5, absolute_current_threshold: null, absolute_tension_threshold: null }
  },
  agents: [{ agent: 1, segment: "EARLY", parameters: {} }, { agent: 2, segment: "MIDDLE", parameters: {} }, { agent: 3, segment: "LATE", parameters: {} }],
  fixed_items: { agent_count: 3, feature_dimension_formula: "4*nLag+32", leave_one_out_global_model: true, predict_then_update: true },
  agent_override_whitelist: [], load_mapping: { version_id: "identity-v1", mapping_type: "identity", display_name: "Load proxy (average current)", result_unit: "A" }, normalized_sha256: hash
};

function editableLeaves(value: any, prefix: string[] = []): string[] {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return [prefix.join(".")];
  return Object.entries(value).flatMap(([key, nested]) => editableLeaves(nested, [...prefix, key]));
}

function fixtureConstraint(path: string, value: any, editable: boolean) {
  const base = { editable, nullable: value === null, minimum: null, maximum: null, allowed_values: null as any };
  if (path === "feature_state.nLag") return { ...base, type: "integer", minimum: 1, maximum: 128 };
  if (path === "split.training_ratio" || path === "split.calibration_ratio") return { ...base, type: "number", minimum: 0, maximum: 1 };
  if (path === "interval.update_mode") return { ...base, type: "string", allowed_values: ["all_finite", "recent"] };
  if (path === "alarms.absolute_current_threshold" || path === "alarms.absolute_tension_threshold") return { ...base, type: "number" };
  if (typeof value === "boolean") return { ...base, type: "boolean" };
  if (typeof value === "number") return { ...base, type: Number.isInteger(value) ? "integer" : "number" };
  return { ...base, type: "string" };
}

const fixtureFixedPaths = new Set(["split.agent_count", "global_surrogate.leave_one_out"]);
const fixtureAllParameterPaths = editableLeaves(fixtureReferenceProfile.shared_parameters);
fixtureReferenceProfile.editable_paths = fixtureAllParameterPaths.filter(path => !fixtureFixedPaths.has(path));
fixtureReferenceProfile.constraints = { paths: Object.fromEntries(fixtureAllParameterPaths.map(path => {
  const relative = path.split(".");
  const value = relative.reduce((current, key) => current[key], fixtureReferenceProfile.shared_parameters);
  return [path, fixtureConstraint(path, value, !fixtureFixedPaths.has(path))];
})) };

function chartPoints() {
  return Array.from({ length: 260 }, (_, index) => {
    const truth = 30 + Math.sin(index / 12) * 3 + Math.sin(index / 4) * .65;
    const local = truth + Math.sin(index / 7) * .8;
    const global = truth + Math.cos(index / 10) * .65;
    const fused = (local + global) / 2;
    const half = 1.4 + Math.sin(index / 9) * .22;
    return { OriginalRunningIndex: index + 1, Time: `2026-08-16T08:${String(Math.floor(index / 60)).padStart(2, "0")}:${String(index % 60).padStart(2, "0")}.000+08:00`, TrueAverageCurrentSmoothed: truth, LocalPrediction: local, GlobalPrediction: global, FusedPrediction: fused, FusedLowerBound: fused - half, FusedUpperBound: fused + half, FusionAlpha: .62 + Math.sin(index / 17) * .1, GlobalSupport: .8 + Math.cos(index / 13) * .1, RecentLocalRMSE: .72 + Math.sin(index / 11) * .08, RecentGlobalRMSE: .81 + Math.cos(index / 14) * .07, FusedHalfWidth: half, LoadStatus: "Normal load", OverallAlarmLevel: "None" };
  });
}

export const fixtureAlarms = [
  { run_id: "run_fixture_completed", Agent: 1, OriginalRunningIndex: 83, Time: "2026-08-16T08:01:23.000+08:00", OverallAlarmLevel: "NOTICE", alarm_type: "LOAD_IMBALANCE", reasons: ["LOAD_IMBALANCE"], result_locator: { agent: 1, original_running_index: 83 } },
  { run_id: "run_fixture_completed", Agent: 1, OriginalRunningIndex: 168, Time: "2026-08-16T08:02:48.000+08:00", OverallAlarmLevel: "WARNING", alarm_type: "INTERVAL_WIDENING", reasons: ["INTERVAL_WIDENING"], result_locator: { agent: 1, original_running_index: 168 } }
];

export const fixtureCompletedSimulation = {
  run_id: "run_fixture_completed", display_name: "Reference fixture run", status: "COMPLETED", current_stage: null, queue_position: null, run_mode: "REFERENCE", created_at: time, started_at: time, finished_at: "2026-08-16T08:04:00.000+08:00", elapsed_ms: 240000,
  dataset: { dataset_id: fixtureDatasetValid.dataset_id, display_name: fixtureDatasetValid.display_name }, parameter_version: fixtureReferenceProfile.version_id, mapping_version: "identity-v1",
  snapshot: {
    sha256: hash, dataset_sha256: hash, parameter_profile_version_id: fixtureReferenceProfile.version_id, load_mapping_version_id: "identity-v1", agents: fixtureReferenceProfile.agents,
    parameter_profile: {
      version_id: fixtureReferenceProfile.version_id, display_name: fixtureReferenceProfile.display_name, sha256: fixtureReferenceProfile.normalized_sha256,
      shared_parameters: fixtureReferenceProfile.shared_parameters, agents: fixtureReferenceProfile.agents, fixed_items: fixtureReferenceProfile.fixed_items
    }
  },
  artifact_state: "COMMITTED", latest_event_id: 12, cancellable: false, cancel_reason: "RUN_NOT_CANCELLABLE"
};

export const fixtureLiveRunDetail = (() => {
  const { snapshot, ...run } = fixtureCompletedSimulation;
  return {
    ...run,
    status: "RUNNING",
    current_stage: "LOCAL_TRAINING",
    parameter_version: { version_id: fixtureReferenceProfile.version_id, display_name: "Reference-compatible", normalized_sha256: fixtureReferenceProfile.normalized_sha256 },
    mapping_version: { version_id: "identity-v1", normalized_sha256: hash },
    snapshot_sha256: hash,
    parameter_snapshot: snapshot.parameter_profile
  };
})();

export const fixtureSummary = {
  run: { run_id: fixtureCompletedSimulation.run_id, run_mode: "REFERENCE", parameter_profile_version_id: fixtureReferenceProfile.version_id, load_mapping_version_id: "identity-v1", snapshot_sha256: hash },
  selection: { agent: 1, segment: "EARLY" },
  metrics: { RMSE: .842, MAE: .618, R2: .936, Coverage: .954, MeanBandwidth: 3.18, MeanOnlineGlobalWeight: .64, NegativeTransferRate: .018 },
  stage_durations_ms: { validation: 2200, preprocessing: 8020, local_training: 64000, aggregation: 11000, calibration: 9000, testing: 142000, artifact: 3800, total: 240000 },
  preprocessing: fixtureDatasetValid.algorithm_preprocessing, split_summary: { agent_1: "11571 / 2479 / 2481" }, anchor_summary: { public_anchors: 300 }, alarm_summary: { None: 2481 }, diagnostic_summary: { mean_global_support: .82 },
  chart: { original_point_count: 2481, display_point_count: 260, sampling_method: "lttb-for-display", points: chartPoints() }, artifact_integrity: { status: "VERIFIED", manifest_sha256: hash }
};
