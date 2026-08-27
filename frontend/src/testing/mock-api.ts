/* Explicit development adapter. Production builds select LivePlatformApi only. */
import { ApiError, DATASET_COLUMNS, PlatformReadiness } from "../api/contract.js";
import { fixtureAlarms, fixtureCompletedSimulation, fixtureDatasetValid, fixtureDatasetValidating, fixtureReferenceProfile, fixtureSummary } from "./contract-fixtures.js";

const wait = milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds));
const copy = value => structuredClone(value);
const mergeTree = (base, overrides) => Object.entries(overrides ?? {}).reduce((all, [key, value]) => ({ ...all, [key]: value && typeof value === "object" && !Array.isArray(value) ? mergeTree(all[key] ?? {}, value) : value }), copy(base ?? {}));

export class MockPlatformApi {
  fixtureDatasetId: string;
  initialRunId: string;
  datasets: Map<string, any>;
  profiles: any[];
  profileFingerprints: Map<string, string>;
  runs: Map<string, any>;

  constructor() { this.fixtureDatasetId = fixtureDatasetValid.dataset_id; this.initialRunId = fixtureCompletedSimulation.run_id; this.datasets = new Map([[fixtureDatasetValid.dataset_id, copy(fixtureDatasetValid)], [fixtureDatasetValidating.dataset_id, copy(fixtureDatasetValidating)]]); this.profiles = []; this.profileFingerprints = new Map(); this.runs = new Map([[fixtureCompletedSimulation.run_id, copy(fixtureCompletedSimulation)]]); }
  async getDataset(datasetId) { await wait(80); const item = this.datasets.get(datasetId); if (!item) throw new ApiError("DATASET_NOT_VALID", "req_mock_dataset", { recoverable: true }); return copy(item); }
  async getReferenceProfile() { await wait(45); return copy(fixtureReferenceProfile); }
  async getParameterProfiles() { return { items: copy(this.profiles), next_cursor: null, has_more: false }; }
  async getReadiness(): Promise<PlatformReadiness> { return { status: "ready", checks: { worker: "not_observed", database: "ok" }, worker_contract_version: "worker.task.v1" }; }
  async listSimulations(query: any = {}) {
    const terminal = new Set(["COMPLETED", "FAILED", "FAILED_RECOVERABLE", "CANCELLED"]);
    const search = String(query.search ?? "").trim().toLowerCase();
    const status = String(query.status ?? "").toUpperCase();
    const mode = String(query.run_mode ?? "").toUpperCase();
    let items = [...this.runs.values()].filter(run => {
      const runStatus = String(run.status ?? "").toUpperCase();
      const dataset = run.dataset?.display_name ?? run.dataset_display_name ?? "";
      const createdAt = String(run.created_at ?? "");
      return (!query.view || query.view !== "history" || terminal.has(runStatus)) && (!query.view || query.view !== "queue" || ["RUNNING", "CANCELLING", "QUEUED"].includes(runStatus)) && (!query.run_id || run.run_id === query.run_id) && (!status || runStatus === status) && (!mode || String(run.run_mode ?? "").toUpperCase() === mode) && (!query.dataset_id || run.dataset?.dataset_id === query.dataset_id) && (!query.parameter_profile_version_id || run.parameter_version === query.parameter_profile_version_id) && (!query.date_from || createdAt >= String(query.date_from)) && (!query.date_to || createdAt <= String(query.date_to)) && (!search || `${run.run_id} ${dataset}`.toLowerCase().includes(search));
    });
    items = items.sort((left, right) => `${right.created_at ?? ""} ${right.run_id}`.localeCompare(`${left.created_at ?? ""} ${left.run_id}`));
    const offset = /^mock:(\d+)$/.test(String(query.cursor ?? "")) ? Number(String(query.cursor).slice(5)) : 0;
    const limit = Math.max(1, Math.min(500, Number(query.limit ?? 100) || 100));
    const page = items.slice(offset, offset + limit).map(copy);
    const nextOffset = offset + page.length;
    const hasMore = nextOffset < items.length;
    const nextCursor = hasMore ? `mock:${nextOffset}` : null;
    return { items: page, next_cursor: nextCursor, has_more: hasMore, total: items.length, meta: { next_cursor: nextCursor, has_more: hasMore, total: items.length } };
  }
  async getSimulation(runId) { const run = this.runs.get(runId); if (!run) throw new ApiError("RESULT_NOT_READY", "req_mock_run", { recoverable: true }); return copy(run); }
  async getSummary(runId, agent) {
    if (runId !== fixtureCompletedSimulation.run_id) throw new ApiError("RESULT_NOT_READY", "req_mock_summary", { recoverable: true });
    if (![1, 2, 3].includes(agent)) throw new ApiError("RESULT_NOT_READY", "req_mock_summary_agent", { recoverable: true });
    return { ...copy(fixtureSummary), selection: { agent, segment: ["EARLY", "MIDDLE", "LATE"][agent - 1] } };
  }
  async getResults(runId, query: any = {}) {
    if (runId !== fixtureCompletedSimulation.run_id) throw new ApiError("RESULT_NOT_READY", "req_mock_results", { recoverable: true });
    const agent = Number(query.agent ?? 1);
    if (![1, 2, 3].includes(agent)) throw new ApiError("RESULT_NOT_READY", "req_mock_results_agent", { recoverable: true });
    return { run_id: runId, agent, result_schema_version: "results.v1", items: copy(fixtureSummary.chart.points).map(point => ({ ...point, Agent: agent })), next_cursor: null };
  }
  async getReplay(runId, query: any = {}) {
    if (runId !== fixtureCompletedSimulation.run_id) throw new ApiError("RESULT_NOT_READY", "req_mock_replay", { recoverable: true });
    const agent = Number(query.agent ?? 1);
    if (![1, 2, 3].includes(agent)) throw new ApiError("RESULT_NOT_READY", "req_mock_replay_agent", { recoverable: true });
    const points = copy(fixtureSummary.chart.points).map(point => ({ ...point, Agent: agent }));
    return { run_id: runId, agent, run_mode: "REFERENCE", parameter_profile_version_id: fixtureReferenceProfile.version_id, parameter_profile_sha256: fixtureReferenceProfile.normalized_sha256, window_start: 1, window_end: points.length, total_points: points.length, next_cursor: null, points };
  }
  async getAlarms(runId, query: any = {}) {
    if (runId !== fixtureCompletedSimulation.run_id) throw new ApiError("RESULT_NOT_READY", "req_mock_alarms", { recoverable: true });
    return { run_id: runId, agent: Number(query.agent ?? 1), items: copy(fixtureAlarms).map(item => ({ ...item, Agent: Number(query.agent ?? 1), result_locator: { ...item.result_locator, agent: Number(query.agent ?? 1) } })), next_cursor: null };
  }
  async getArtifacts(runId) {
    if (runId !== fixtureCompletedSimulation.run_id) return { artifact_state: "INCOMPLETE", manifest_sha256: null, items: [] };
    const names = ["artifact_manifest.json", "run_manifest.json", "results_agent_1.csv", "results_agent_2.csv", "results_agent_3.csv", "alarms.csv", "diagnostics.json", "summary_agent_1.json", "summary_agent_2.json", "summary_agent_3.json", "traceability.json", "preflight_summary.json"];
    return { artifact_state: "COMMITTED", manifest_sha256: fixtureReferenceProfile.normalized_sha256, items: names.map(name => ({ name, media_type: name.endsWith(".csv") ? "text/csv" : "application/json", size_bytes: 1024, sha256: fixtureReferenceProfile.normalized_sha256, required: true })) };
  }
  replayExportUrl(runId, agent) { return `/api/v1/simulations/${encodeURIComponent(runId)}/replay-export?agent=${encodeURIComponent(agent)}&format=zip`; }
  artifactDownloadUrl(runId, artifactName) { return `/api/v1/simulations/${encodeURIComponent(runId)}/artifacts/${encodeURIComponent(artifactName)}`; }
  async cancelSimulation(runId) {
    const run = this.runs.get(runId);
    if (!run || run.cancellable !== true) throw new ApiError("RUN_NOT_CANCELLABLE", "req_mock_cancel", { recoverable: false, status: 409 });
    const status = String(run.status ?? "").toUpperCase() === "RUNNING" ? "CANCELLING" : "CANCELLED";
    const next = { ...run, status, cancellable: false, queue_position: status === "CANCELLED" ? null : run.queue_position };
    this.runs.set(runId, next);
    return copy(next);
  }
  async createSimulation() { const run = { ...copy(fixtureCompletedSimulation), run_id: `run_mock_${Date.now()}`, status: "QUEUED", current_stage: null, queue_position: 1, finished_at: null, elapsed_ms: 0, artifact_state: "NOT_STARTED", cancellable: true, latest_event_id: 1 }; this.runs.set(run.run_id, run); return copy(run); }
  async createParameterProfile(payload) {
    const base = this.profiles.find(profile => profile.version_id === payload.base_version_id) ?? (payload.base_version_id === fixtureReferenceProfile.version_id ? fixtureReferenceProfile : null);
    if (!base) throw new ApiError("RESULT_NOT_READY", "req_mock_profile_base", { recoverable: true });
    const sharedParameters = mergeTree(base.shared_parameters, payload.shared_parameters);
    const agents = [1, 2, 3].map(agent => {
      const baseAgent = base.agents.find(item => item.agent === agent) ?? { agent, parameters: {} };
      const override = payload.agents?.find(item => item.agent === agent)?.parameters ?? {};
      return { ...copy(baseAgent), parameters: mergeTree(baseAgent.parameters, override) };
    });
    const fingerprint = JSON.stringify({ base_version_id: base.version_id, shared_parameters: sharedParameters, agents: agents.map(agent => ({ agent: agent.agent, parameters: agent.parameters })) });
    const knownId = this.profileFingerprints.get(fingerprint);
    if (knownId) return copy(this.profiles.find(profile => profile.version_id === knownId));
    const version = { ...copy(base), mode: "CUSTOM", immutable: true, version_id: `custom-version-${this.profiles.length + 1}`, base_version_id: base.version_id, display_name: payload.display_name, shared_parameters: sharedParameters, agents, normalized_sha256: `${this.profiles.length + 1}`.padStart(64, "0") };
    this.profiles.unshift(version); this.profileFingerprints.set(fingerprint, version.version_id); return copy(version);
  }
  async renameParameterProfile(versionId, payload) {
    const index = this.profiles.findIndex(profile => profile.version_id === versionId);
    const displayName = String(payload?.display_name ?? "").trim();
    if (index < 0 || !displayName) throw new ApiError("PROFILE_RENAME_INVALID", "req_mock_profile_rename", { recoverable: true });
    const renamed = { ...this.profiles[index], display_name: displayName };
    this.profiles[index] = renamed;
    return copy(renamed);
  }
  async uploadDataset(file, displayName, onProgress) {
    if (file.size > 500 * 1024 * 1024) throw new ApiError("UPLOAD_TOO_LARGE", "req_mock_upload", { recoverable: true });
    onProgress?.(30); await wait(100); const header = (await file.slice(0, 2048).text()).replace(/^\uFEFF/, "").split(/\r?\n/, 1)[0].trim();
    if (header !== DATASET_COLUMNS.join(",")) throw new ApiError("CSV_HEADER_MISMATCH", "req_mock_upload", { recoverable: true, field: "file" });
    onProgress?.(100); const dataset = { ...copy(fixtureDatasetValidating), dataset_id: `ds_mock_${Date.now()}`, display_name: displayName || file.name, size_bytes: file.size };
    this.datasets.set(dataset.dataset_id, dataset); return dataset;
  }
  subscribeSimulationEvents(runId, lastEventId, handlers) {
    let closed = false; const id = Number(lastEventId ?? 12) + 1;
    const timer = setTimeout(() => { if (!closed) handlers.onEvent?.({ id, type: "heartbeat", data: { run_id: runId, occurred_at: new Date().toISOString(), latest_event_id: id } }); }, 150);
    return { close: () => { closed = true; clearTimeout(timer); } };
  }
}
