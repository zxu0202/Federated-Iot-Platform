import { ApiError, DATASET_COLUMNS, formatApiError, validateAgentCollection } from "./api/contract.js";
import { createI18n } from "./i18n.js";
import { DatasetPoller } from "./state/dataset-poller.js";
import { SimulationStore } from "./state/simulation-store.js";

const number = value => typeof value === "number" ? new Intl.NumberFormat(undefined, { maximumFractionDigits: 3 }).format(value) : "—";
const statusClass = status => String(status ?? "").replace(/[^A-Z_]/g, "");
const STATUS_LABEL_KEYS: Record<string, string> = {
  INVALID: "status.invalid", VALID: "status.valid", VALIDATING: "status.validating", QUEUED: "status.queued", RUNNING: "status.running",
  CANCELLING: "status.cancelling", CANCELLED: "status.cancelled", FAILED: "status.failed", FAILED_RECOVERABLE: "status.failedRecoverable",
  COMPLETED: "status.completed", READY: "status.ready", CHECKING: "status.checking", UNAVAILABLE: "status.unavailable", NOT_OBSERVED: "status.notObserved", WARNING: "status.warning"
};
const HTML_ESCAPES: Record<string, string> = { "&": "&amp;", "<": "&lt;", ">": "&gt;", "\"": "&quot;", "'": "&#39;" };
const PARAMETER_GROUP_LABEL_KEYS: Record<string, string> = {
  feature_state: "param.group.featureState", cleaning: "param.group.cleaning", split: "param.group.split", local_gp: "param.group.localGp",
  trend: "param.group.trend", interval: "param.group.interval", anchors: "param.group.anchors", support: "param.group.support",
  global_surrogate: "param.group.globalSurrogate", fusion: "param.group.fusion", alarms: "param.group.alarms"
};
const S1_FIXED_PARAMETER_PATHS = new Set(["split.agent_count", "global_surrogate.leave_one_out"]);

const escapeHtml = value => String(value ?? "").replace(/[&<>"']/g, character => HTML_ESCAPES[character]);
const isRecord = value => typeof value === "object" && value !== null && !Array.isArray(value);

function versionReference(value: any) {
  const record = isRecord(value) ? value : null;
  return {
    versionId: record ? record.version_id ?? null : value ?? null,
    displayName: record?.display_name ?? null,
    hash: record?.normalized_sha256 ?? record?.sha256 ?? null
  };
}

export function localizedStatus(status, t): string {
  const normalized = String(status ?? "").toUpperCase().replace(/-/g, "_");
  const key = STATUS_LABEL_KEYS[normalized];
  return key ? t(key) : t("status.unknown");
}

function formatParameterValue(value: any, t: ((key: string) => string) | null = null): string {
  if (value === null || value === undefined) return "—";
  // Parameter values are frozen inputs, not compact dashboard metrics. Keep small
  // algorithm values inspectable instead of rounding them to a misleading zero.
  if (typeof value === "number") return formatFrozenParameterNumber(value);
  if (Array.isArray(value)) return value.map(item => formatParameterValue(item, t)).join(", ");
  if (isRecord(value)) return displayProfileText(JSON.stringify(value), t);
  return displayProfileText(String(value), t);
}

function formatFrozenParameterNumber(value: number): string {
  if (!Number.isFinite(value)) return "—";
  // `Number#toString` is the shortest decimal representation that round-trips to
  // this frozen IEEE-754 value. Intl compact formatting would turn 1e-8 into 0.
  return String(value);
}

function flattenParameterLeaves(value: any, prefix: string[] = []): [string, any][] {
  if (!isRecord(value)) return [[prefix.join("."), value]];
  const entries = Object.entries(value);
  if (!entries.length) return [[prefix.join("."), "{}"]];
  return entries.flatMap(([key, nested]) => flattenParameterLeaves(nested, [...prefix, key]));
}

function parameterGroups(profile: any): [string, any][] {
  const shared = profile?.shared_parameters ?? {};
  const entries = Object.entries(shared);
  if (!entries.length) return [];
  return entries.some(([, value]) => isRecord(value)) ? entries : [["local_gp", shared]];
}

function parameterGroupLabel(groupKey: string, t): string {
  return PARAMETER_GROUP_LABEL_KEYS[groupKey] ? t(PARAMETER_GROUP_LABEL_KEYS[groupKey]) : groupKey;
}

const PARAMETER_LEAF_LABEL_KEYS: Record<string, string> = {
  nLag: "param.leaf.nLag", speed_threshold: "param.leaf.speedThreshold", current_threshold: "param.leaf.currentThreshold",
  median_window: "param.leaf.medianWindow", mad_factor: "param.leaf.madFactor", smoothing_window: "param.leaf.smoothingWindow",
  training_ratio: "param.leaf.trainingRatio", calibration_ratio: "param.leaf.calibrationRatio", minimum_training: "param.leaf.minimumTraining", minimum_calibration: "param.leaf.minimumCalibration", minimum_testing: "param.leaf.minimumTesting", agent_count: "param.leaf.agentCount",
  kNN: "param.leaf.kNN", adaptive_ratio: "param.leaf.adaptiveRatio", ell: "param.leaf.lengthScale", sigma_f: "param.leaf.signalScale", sigma_n: "param.leaf.noiseScale", minimum_regularization: "param.leaf.minimumRegularization",
  threshold: "param.leaf.trendThreshold", maximum_mixing: "param.leaf.maximumMixing", gain: "param.leaf.trendGain", maximum_step_change: "param.leaf.maximumStepChange",
  confidence: "param.leaf.confidence", calibration_window: "param.leaf.calibrationWindow", minimum_scores: "param.leaf.minimumScores", standard_deviation_floor: "param.leaf.standardDeviationFloor", std_floor: "param.leaf.stdFloor", calibration_scale_min: "param.leaf.calibrationScaleMinimum", calibration_scale_max: "param.leaf.calibrationScaleMaximum", half_width_min: "param.leaf.halfWidthMinimum", half_width_max: "param.leaf.halfWidthMaximum", coverage_window: "param.leaf.coverageWindow", update_mode: "param.leaf.updateMode", variance_floor: "param.leaf.varianceFloor",
  base_centers: "param.leaf.baseCenters", transition_centers: "param.leaf.transitionCenters", boundary_centers: "param.leaf.boundaryCenters", transition_quantile: "param.leaf.transitionQuantile", public_anchors: "param.leaf.publicAnchors", iterations: "param.leaf.iterations", random_seed: "param.leaf.randomSeed",
  scale_multiple: "param.leaf.scaleMultiple", minimum_weight: "param.leaf.minimumWeight", minimum_query_support: "param.leaf.minimumQuerySupport", full_weight_reference: "param.leaf.fullWeightReference",
  noise_ratio: "param.leaf.noiseRatio", cholesky_attempts: "param.leaf.choleskyAttempts", leave_one_out: "param.leaf.leaveOneOut",
  maximum_global_weight: "param.leaf.maximumGlobalWeight", initial_improvement: "param.leaf.initialImprovement", error_window: "param.leaf.errorWindow", minimum_samples: "param.leaf.minimumSamples", win_margin: "param.leaf.winMargin", variance_weight: "param.leaf.varianceWeight", winsor_quantile: "param.leaf.winsorQuantile", global_clear_threshold: "param.leaf.globalClearThreshold", neutral_upper_limit: "param.leaf.neutralUpperLimit", persistence: "param.leaf.persistence", rise_smoothing: "param.leaf.riseSmoothing", fall_smoothing: "param.leaf.fallSmoothing", disagreement_kappa: "param.leaf.disagreementKappa", maximum_variance_ratio: "param.leaf.maximumVarianceRatio",
  imbalance_threshold: "param.leaf.imbalanceThreshold", notice_count: "param.leaf.noticeCount", warning_count: "param.leaf.warningCount", alarm_count: "param.leaf.alarmCount", absolute_current_threshold: "param.leaf.absoluteCurrentThreshold", absolute_tension_threshold: "param.leaf.absoluteTensionThreshold", tension_threshold: "param.leaf.tensionThreshold",
  feature_dimension_formula: "param.leaf.featureDimensionFormula", leave_one_out_global_model: "param.leaf.leaveOneOutGlobalModel", predict_then_update: "param.leaf.predictThenUpdate", agent_override_whitelist: "param.leaf.agentOverrideWhitelist"
};

export function parameterLeafLabel(key: string, t): string {
  const resourceKey = PARAMETER_LEAF_LABEL_KEYS[key];
  if (resourceKey) return t(resourceKey);
  return key.replace(/([a-z])([A-Z])/g, "$1 $2").replace(/_/g, " ");
}

export function displayAgentSegment(segment, t): string {
  const key = `agent.segment.${String(segment ?? "").toLowerCase()}`;
  const translated = t(key);
  return translated === key ? String(segment ?? "—") : translated;
}

export function displayProfileText(value, t: ((key: string) => string) | null = null, fallback = "—"): string {
  const raw = String(value ?? "").trim();
  if (!raw) return fallback;
  return raw;
}

export function profileDisplayName(profile, t): string {
  if (profile?.mode === "REFERENCE") return t("profile.referenceCompatible");
  return displayProfileText(profile?.display_name, t, t("state.loading"));
}

function frozenProfilePrimaryText(profile, t, fallback = "—"): string {
  if (String(profile?.mode ?? profile?.run_mode ?? "").toUpperCase() === "REFERENCE") return t("profile.referenceCompatible");
  return displayProfileText(profile?.display_name, t, fallback);
}

export function displayProfileVersion(versionId, t: ((key: string) => string) | null = null): string {
  const raw = String(versionReference(versionId).versionId ?? "").trim();
  return raw || "—";
}

export function runSnapshotDisplay(detail, t) {
  const parameter = versionReference(detail?.parameter_version ?? detail?.parameter_profile_version_id);
  const mapping = versionReference(detail?.mapping_version ?? detail?.load_mapping_version_id);
  return {
    parameter: displayProfileVersion(parameter.versionId, t),
    mapping: displayProfileText(mapping.displayName ?? mapping.versionId, t),
    snapshotHash: detail?.snapshot_sha256 ?? detail?.snapshot?.sha256 ?? null,
    parameterHash: parameter.hash,
    mappingHash: mapping.hash
  };
}

function cloneJson<T>(value: T): T { return value === undefined ? value : JSON.parse(JSON.stringify(value)); }
function pathParts(path: string): string[] { return path.split(".").filter(Boolean); }
function readPath(value: any, path: string[]): any { return path.reduce((current, part) => current !== null && current !== undefined ? current[part] : undefined, value); }
function hasPath(value: any, path: string[]): boolean { const parent = readPath(value, path.slice(0, -1)); return path.length > 0 && parent !== null && parent !== undefined && Object.prototype.hasOwnProperty.call(parent, path[path.length - 1]); }
function writePath(value: any, path: string[], next: any) { let target = value; path.slice(0, -1).forEach(part => { if (!isRecord(target[part])) target[part] = {}; target = target[part]; }); target[path[path.length - 1]] = next; }
function removePath(value: any, path: string[]) { const parent = readPath(value, path.slice(0, -1)); if (isRecord(parent)) delete parent[path[path.length - 1]]; }
function equalJson(left: any, right: any): boolean { return JSON.stringify(left) === JSON.stringify(right); }

function profileEditableDefinitions(profile: any) {
  const metadata = profile?.editable_paths;
  const profileConstraints = profile?.constraints?.paths ?? profile?.constraints ?? profile?.parameter_constraints?.paths ?? profile?.parameter_constraints ?? {};
  const source = Array.isArray(metadata) ? metadata : isRecord(metadata) ? Object.entries(metadata).map(([path, constraint]) => ({ path, constraint })) : [];
  return source.map((item: any) => {
    const path = typeof item === "string" ? item : item?.path ?? item?.editable_path;
    const relative = typeof path === "string" ? path.replace(/^shared_parameters\./, "") : "";
    const constraint = typeof item === "string" ? profileConstraints[path] ?? profileConstraints[relative] : item?.constraint ?? item?.constraints ?? profileConstraints[path] ?? profileConstraints[relative] ?? item;
    return { path, relative, parts: pathParts(relative), constraint: isRecord(constraint) ? constraint : {} };
  }).filter(item => item.relative && !S1_FIXED_PARAMETER_PATHS.has(item.relative) && item.constraint.editable !== false && hasPath(profile?.shared_parameters, item.parts));
}

function normalizeConstraint(definition: any, value: any) {
  const constraint = definition.constraint ?? {};
  const type = constraint.type ?? (typeof value === "boolean" ? "boolean" : typeof value === "number" && Number.isInteger(value) ? "integer" : typeof value === "number" ? "number" : "string");
  const min = constraint.min ?? constraint.minimum ?? null;
  const max = constraint.max ?? constraint.maximum ?? null;
  const allowedValues = Array.isArray(constraint.allowed_values) ? constraint.allowed_values : Array.isArray(constraint.enum) ? constraint.enum : null;
  return { type, min, max, allowedValues };
}

function customDraftProfile(draft: any) {
  return { ...draft.baseProfile, mode: "CUSTOM", display_name: draft.display_name, shared_parameters: draft.shared_parameters, agents: draft.agents };
}

function createCustomDraft(baseProfile: any) {
  return {
    baseProfile: cloneJson(baseProfile),
    base_version_id: baseProfile?.mode === "CUSTOM" ? baseProfile?.base_version_id ?? null : baseProfile?.version_id ?? null,
    display_name: "",
    shared_parameters: cloneJson(baseProfile?.shared_parameters ?? {}),
    agents: [1, 2, 3].map(agent => ({ agent, segment: baseProfile?.agents?.find(item => item.agent === agent)?.segment, parameters: cloneJson(baseProfile?.agents?.find(item => item.agent === agent)?.parameters ?? {}) }))
  };
}

export function draftIsDirty(draft: any): boolean {
  if (!draft?.baseProfile) return false;
  return String(draft.display_name ?? "").trim() !== "" || !equalJson(draft.shared_parameters, draft.baseProfile.shared_parameters) || !equalJson(draft.agents.map(agent => agent.parameters), [1, 2, 3].map(agent => draft.baseProfile.agents?.find(item => item.agent === agent)?.parameters ?? {}));
}

export function validateCustomDraftName(value, t) {
  const name = String(value ?? "").trim();
  const length = Array.from(name).length;
  if (!name) return { valid: false, name, error: t("config.aliasRequired") };
  if (length > 128 || /[\u0000-\u001F\u007F]/.test(name)) return { valid: false, name, error: t("config.aliasInvalid") };
  return { valid: true, name, error: null };
}

export function beginCustomDraft(state, render): boolean {
  const baseProfile = state.customProfile ?? state.profile;
  if (!baseProfile || state.customSave?.pending) return false;
  state.customDraft = createCustomDraft(baseProfile);
  state.configScope = "shared";
  state.customSave = { pending: false, versionId: null, error: null };
  state.customRename = { editing: false, pending: false, error: null };
  render();
  return true;
}

export function selectCustomProfile(state, versionId, render): boolean {
  const selected = (state.customProfiles ?? []).find(profile => profile.version_id === versionId) ?? null;
  state.customProfile = selected;
  state.customDraft = null;
  state.customSave = { pending: false, versionId: null, error: null };
  state.customRename = { editing: false, pending: false, error: null };
  state.configScope = "shared";
  if (!selected) { render(); return false; }
  return beginCustomDraft(state, render);
}

function createApplicationState(api) {
  return {
    api, view: "workspace", language: "en", dataset: null as any, datasetEpoch: 0, profile: null, customProfile: null, customProfiles: [],
    upload: { percent: null, error: null, fileName: null }, queue: { items: [], waitingCount: 0, loading: false, error: null, nextCursor: null, hasMore: false, total: null },
    history: { items: [], aggregateMetrics: {}, loading: false, error: null, query: "", status: "", mode: "", nextCursor: null, hasMore: false, total: null, listEpoch: 0, selectionEpoch: 0, selectedRunId: null, detail: null, detailLoading: false, detailError: null, deepLinkNotice: null, artifacts: null, replay: { agent: 1, loading: false, error: null, data: null, results: null, alarms: null } },
    replayPlayback: { playing: false, speed: 1, position: 0, timer: null as any },
    customDraft: null, customSave: { pending: false, versionId: null, error: null }, customRename: { editing: false, pending: false, error: null }, configScope: "shared", chart: { zoom: 1, pan: 0, focus: null, dragging: null, series: { truth: true, local: true, global: true, fused: true, interval: true } }
  };
}

export function startApplication(root, api) {
  if (!root) throw new Error("Missing #app mount point.");
  const i18n = createI18n("en");
  const state = createApplicationState(api);
  const taskStore = new SimulationStore(api);
  const datasetPoller = new DatasetPoller(api, {
    onDataset: dataset => {
      if (state.dataset?.dataset_id !== dataset.dataset_id) return;
      state.dataset = dataset;
      state.upload = { ...state.upload, percent: null, error: null };
      render({ dynamic: true, source: "dataset-poll" });
    },
    onError: (datasetId, error) => {
      if (state.dataset?.dataset_id !== datasetId) return;
      state.upload = { ...state.upload, percent: null, error };
      render({ dynamic: true, source: "dataset-poll" });
    }
  });
  const queueRefreshTimer = window.setInterval(() => { void loadQueue(state, render, taskStore); }, 10000);
  window.addEventListener("beforeunload", () => { stopReplayPlayback(state); datasetPoller.close(); taskStore.close(); clearInterval(queueRefreshTimer); }, { once: true });

  const render = (options: { preserveConfig?: boolean; dynamic?: boolean; source?: string } = {}) => {
    document.documentElement.lang = state.language;
    document.title = i18n.t("document.title");
    if ((options.dynamic || options.preserveConfig) && patchDynamicApplication(root, state, taskStore.state, taskStore, i18n, options.source ?? "state", () => bindQueueCancelInteractions(root, state, taskStore, i18n, render), () => bindHistoryActionButtons(root, state, taskStore, i18n, render))) return;
    const context = captureRenderContext(root);
    root.innerHTML = applicationTemplate(state, taskStore.state, i18n);
    bindInteractions(root, state, taskStore, datasetPoller, i18n, render);
    const chartSummary = chartSummaryForView(state, taskStore.state.summary);
    drawChart(root, state, chartSummary, i18n);
    drawDiagnosticCharts(root, chartSummary, workspaceResultsForRun(taskStore.state));
    restoreRenderContext(root, context);
  };

  taskStore.subscribe((nextTask, notification) => {
    synchronizeQueueWaitingCount(state, nextTask);
    synchronizeQueueProjection(state, nextTask);
    if (isTerminalHistoryRun(nextTask.detail)) upsertHistoryRun(state, nextTask.detail);
    render({ dynamic: notification.source !== "initial", source: notification.source });
  });
  taskStore.startReadinessRefresh();
  void loadConfiguration(state, taskStore, render);
  if (api.fixtureDatasetId) void loadDataset(state, api.fixtureDatasetId, datasetPoller, render);
  const requestUrl = new URL(window.location.href);
  const requestedView = requestUrl.searchParams.get("view");
  if (requestedView === "history" || requestedView === "replay") state.view = requestedView;
  const requestedRunId = requestUrl.searchParams.get("run_id");
  const historyDeepLink = (state.view === "history" || state.view === "replay") ? requestedRunId : null;
  if (!historyDeepLink && (requestedRunId || api.initialRunId)) void taskStore.selectRun(requestedRunId ?? api.initialRunId);
  render();
  void loadQueue(state, render, taskStore);
  if (state.view === "history" || state.view === "replay") {
    if (historyDeepLink) void openHistoryDeepLink(state, taskStore, historyDeepLink, i18n, render, state.view === "replay");
    else void loadHistory(state, render);
  }
  window.addEventListener("popstate", () => {
    const location = new URL(window.location.href);
    const nextView = location.searchParams.get("view");
    if (nextView !== "history" && nextView !== "replay") return;
    stopReplayPlayback(state);
    state.view = nextView;
    const runId = location.searchParams.get("run_id");
    render();
    if (runId) void openHistoryDeepLink(state, taskStore, runId, i18n, render, nextView === "replay");
    else void loadHistory(state, render);
  });
}

async function loadConfiguration(state, taskStore, render) {
  try {
    state.profile = await state.api.getReferenceProfile();
    if (state.api.getParameterProfiles) {
      const response = await state.api.getParameterProfiles();
      state.customProfiles = Array.isArray(response?.items) ? response.items : Array.isArray(response) ? response : [];
    }
  } catch (error) { taskStore.emit({ error }); }
  render();
}

async function loadDataset(state, datasetId, datasetPoller, render) {
  const epoch = ++state.datasetEpoch;
  datasetPoller.stop();
  try {
    const dataset = await state.api.getDataset(datasetId);
    if (epoch !== state.datasetEpoch) return;
    state.dataset = dataset;
    state.upload = { ...state.upload, percent: null, error: null };
    datasetPoller.watch(dataset);
  } catch (error) { if (epoch === state.datasetEpoch) state.upload = { ...state.upload, percent: null, error }; }
  render();
}

function applicationTemplate(state, task, i18n) {
  const t = i18n.t.bind(i18n); const detail = task.detail;
  const waitingCount = queueWaitingCount(state.queue);
  return `<a class="skip" href="#main-content">${t("aria.skip")}</a>
    <div class="shell">
      <header class="topbar">
        <div class="brand"><span class="brand-mark" aria-hidden="true">ϟ</span><span><strong>zx/federated-iot-platform:latest</strong><small>${t("brand.subtitle")}</small></span></div>
        <nav class="nav" aria-label="${t("aria.primaryNav")}">${navButton("workspace", t("nav.workspace"), state.view)}${navButton("data", t("nav.data"), state.view)}${navButton("config", t("nav.parameters"), state.view)}${navButton("queue", t("nav.queue"), state.view, waitingCount, t("nav.queueWaiting", { count: waitingCount }))}${navButton("history", t("nav.history"), state.view)}</nav>
        <div class="actions"><span data-connection class="connection ${connectionDisplay(task.connection, t).className}">${connectionDisplay(task.connection, t).label}</span><label><span class="skip">${t("aria.language")}</span><select data-language aria-label="${t("aria.language")}"><option value="en" ${state.language === "en" ? "selected" : ""}>English</option><option value="zh-CN" ${state.language === "zh-CN" ? "selected" : ""}>中文</option></select></label><span class="run-actions" data-run-actions>${actionButtons(state, detail, t)}</span></div>
      </header>
      ${systemStrip(detail, state.dataset, task, t, waitingCount)}
      <main id="main-content">${viewTemplate(state, task, i18n)}</main>
      <div class="toast-region" aria-live="polite"></div>
    </div>`;
}

function navButton(name, label, active, count: number | null = null, countLabel = "") { return `<button type="button" data-nav="${name}" class="${name === active ? "active" : ""}"${count === null ? "" : ` aria-label="${escapeHtml(countLabel)}"`}>${label}${count === null ? "" : ` <span class="nav-count" data-queue-nav-count>${count}</span>`}</button>`; }

function actionButtons(state, detail, t) {
  const canStart = canCreateSimulation(state);
  const cancel = queueRunIsCancellable(detail) ? `<button class="button danger compact" data-cancel>${t("action.cancel")}</button>` : "";
  return `${cancel}<button class="button primary compact" data-start ${canStart ? "" : "disabled"} title="${canStart ? "" : t("state.disabled")}">▶ ${t("action.start")}</button>`;
}

export function readinessDisplay(task, component, t) {
  if (task.readinessLoading) return { key: "CHECKING", label: t("health.checking") };
  if (task.readinessError || !task.readiness) return { key: "UNAVAILABLE", label: t("health.unavailable") };
  const raw = component === "web" ? task.readiness.status : task.readiness.checks?.[component];
  const normalized = String(raw ?? "").toLowerCase();
  if (["ok", "ready"].includes(normalized)) return { key: "READY", label: t("health.ready") };
  if (["not_observed", "not-observed"].includes(normalized)) return { key: "NOT_OBSERVED", label: t("health.notObserved") };
  if (["warning", "warn", "degraded"].includes(normalized)) return { key: "WARNING", label: t("health.warning") };
  return { key: "UNAVAILABLE", label: t("health.unavailable") };
}

function readinessEntry(label, component, task, t) {
  const status = readinessDisplay(task, component, t);
  return `<span><b>${label}</b> <span class="state ${status.key}">${status.label}</span></span>`;
}

export function systemStrip(detail, dataset, task, t, waitingCount = 0) {
  const status = detail?.status ?? "—"; const preflight = dataset?.preflight?.status ?? "—";
  const statusLabel = status === "—" ? status : localizedStatus(status, t);
  const preflightLabel = preflight === "—" ? preflight : localizedStatus(preflight, t);
  return `<div class="system-strip" data-system-strip>${readinessEntry(t("system.web"), "web", task, t)}${readinessEntry(t("system.worker"), "worker", task, t)}${readinessEntry(t("system.database"), "database", task, t)}<span>${t("system.currentRun")}: <b>${detail?.run_id ?? "—"}</b></span><span class="state ${statusClass(status)}" aria-label="${escapeHtml(statusLabel)}">${escapeHtml(statusLabel)}</span><span>${t("system.stage")}: <b>${detail?.current_stage ?? "—"}</b></span><span>${t("system.queue")}: <b>${waitingCount} / 10</b></span><span>${t("system.preflight")}: <b aria-label="${escapeHtml(preflightLabel)}">${escapeHtml(preflightLabel)}</b></span>${task.lastEventId ? `<span>${t("state.lastEvent", { id: task.lastEventId })}</span>` : ""}</div>`;
}

function connectionMarkup(task, t) {
  const display = connectionDisplay(task.connection, t);
  return `<span data-connection class="connection ${display.className}">${display.label}</span>`;
}

function connectionDisplay(connection, t) {
  const normalized = String(connection ?? "connecting").toLowerCase();
  if (normalized === "connected") return { className: "", label: t("connection.connected") };
  if (normalized === "connecting") return { className: "connecting", label: t("connection.connecting") };
  if (normalized === "reconnecting") return { className: "connecting", label: t("connection.reconnecting") };
  return { className: "muted", label: normalized === "offline" ? t("connection.offline") : t("connection.disconnected") };
}

function bindRunActionInteractions(scope, state, taskStore, i18n, root) {
  scope.querySelector("[data-start]")?.addEventListener("click", () => void startSimulation(state, taskStore, i18n, root));
  scope.querySelector("[data-cancel]")?.addEventListener("click", () => void cancelSimulation(taskStore, i18n, root));
}

function updateConfigurationChrome(root, state, task, taskStore, i18n) {
  const t = i18n.t.bind(i18n);
  const connection = root.querySelector("[data-connection]");
  if (connection) {
    const display = connectionDisplay(task.connection, t);
    connection.className = `connection ${display.className}`;
    connection.textContent = display.label;
  }
  const system = root.querySelector("[data-system-strip]");
  if (system) {
    const next = systemStrip(task.detail, state.dataset, task, t, queueWaitingCount(state.queue));
    system.innerHTML = next.slice(next.indexOf(">") + 1, -6);
  }
  const actions = root.querySelector("[data-run-actions]");
  if (actions) {
    actions.innerHTML = actionButtons(state, task.detail, t);
    bindRunActionInteractions(actions, state, taskStore, i18n, root);
  }
  const waitingCount = queueWaitingCount(state.queue);
  const queueCount = root.querySelector("[data-queue-nav-count]");
  if (queueCount) {
    queueCount.textContent = String(waitingCount);
    queueCount.closest?.("[data-nav=queue]")?.setAttribute("aria-label", t("nav.queueWaiting", { count: waitingCount }));
  }
}

export function captureRenderContext(root) {
  const documentRef = root?.ownerDocument;
  const detail = root?.querySelector?.("[data-config-detail-scroll]");
  const historyTable = root?.querySelector?.("[data-history-table-wrap]");
  const active = documentRef?.activeElement as HTMLInputElement | null;
  const focusSelector = ["data-language", "data-chart", "data-draft-display-name", "data-history-query", "data-history-status", "data-history-mode", "data-history-more"].find(attribute => active?.hasAttribute?.(attribute));
  return { detailScrollTop: detail?.scrollTop ?? null, historyScrollTop: historyTable?.scrollTop ?? null, focusSelector: focusSelector ? `[${focusSelector}]` : null, selectionStart: active?.selectionStart ?? null, selectionEnd: active?.selectionEnd ?? null };
}

export function restoreRenderContext(root, context) {
  const detail = root?.querySelector?.("[data-config-detail-scroll]");
  if (detail && context?.detailScrollTop !== null) detail.scrollTop = context.detailScrollTop;
  const historyTable = root?.querySelector?.("[data-history-table-wrap]");
  if (historyTable && context?.historyScrollTop !== null) historyTable.scrollTop = context.historyScrollTop;
  const target = context?.focusSelector ? root?.querySelector?.(context.focusSelector) as HTMLInputElement | null : null;
  if (!target || target.disabled) return;
  target.focus({ preventScroll: true });
  if (typeof context.selectionStart === "number" && typeof context.selectionEnd === "number") target.setSelectionRange?.(context.selectionStart, context.selectionEnd);
}

function matchingNodes(root, selector) {
  if (typeof root?.querySelectorAll === "function") return Array.from(root.querySelectorAll(selector));
  const node = root?.querySelector?.(selector);
  return node ? [node] : [];
}

function patchWorkspaceDynamicState(root, state, task, t) {
  const workspace = root.querySelector("[data-workspace-view]");
  if (!workspace) return false;
  const renderedRunId = workspace.getAttribute("data-run-id") ?? "";
  const nextRunId = task.detail?.run_id ?? "";
  const hasSummary = workspace.getAttribute("data-has-summary") === "true";
  const summary = workspaceSummaryForRun(task);
  const renderedAgent = workspace.getAttribute("data-workspace-agent") ?? "";
  const nextAgent = summary?.selection?.agent === null || summary?.selection?.agent === undefined ? "" : String(summary.selection.agent);
  if (renderedRunId !== nextRunId || Boolean(summary) !== hasSummary || renderedAgent !== nextAgent) return false;
  const taskStatus = String(task.detail?.status ?? "").toUpperCase();
  matchingNodes(root, "[data-workspace-live-status]").forEach((status: any) => {
    if (!task.detail) return;
    status.className = `state ${task.loading ? "VALIDATING" : statusClass(taskStatus)}`;
    status.textContent = task.loading ? t("state.loading") : hasSummary ? t("workspace.frozen") : localizedStatus(taskStatus, t);
  });
  matchingNodes(root, "[data-workspace-stage]").forEach((stage: any) => { if (task.detail) stage.textContent = task.detail.current_stage ?? "—"; });
  const liveSummaryStatus = root.querySelector("[data-workspace-live-summary-status]");
  if (liveSummaryStatus && task.detail) {
    liveSummaryStatus.className = `state ${statusClass(taskStatus)}`;
    liveSummaryStatus.textContent = taskStatus === "RUNNING" ? t("workspace.runningLive") : taskStatus === "COMPLETED" ? t("workspace.completedRun") : localizedStatus(taskStatus, t);
  }
  const elapsed = root.querySelector("[data-workspace-elapsed]");
  if (elapsed && task.detail) elapsed.textContent = t("workspace.elapsed", { value: elapsedDuration(task.detail.elapsed_ms) });
  matchingNodes(root, "[data-workspace-event]").forEach((event: any) => { event.textContent = task.lastEventId ? t("state.lastEvent", { id: task.lastEventId }) : t("workspace.noTaskEvents"); });
  const result = root.querySelector("[data-workspace-result-state]");
  if (result && task.detail) {
    const terminal = ["COMPLETED", "FAILED", "FAILED_RECOVERABLE", "CANCELLED"].includes(taskStatus);
    result.innerHTML = `<div class="callout ${terminal ? "error" : ""}"><strong>${terminal ? t("workspace.resultsUnavailable") : t("workspace.resultsPending")}</strong></div>`;
  }
  const resultFiles = root.querySelector("[data-workspace-result-files]");
  if (resultFiles && task.detail && summary) {
    resultFiles.innerHTML = workspaceResultFilesNotice(workspaceResultFilesPresentation(task.detail, summary), t).replace(/^<section[^>]*>|<\/section>$/g, "");
  }
  const eventRows = taskEventRows(task.events, t) || `<div class="event"><time>—</time><span>${t("event.noEvents")}</span></div>`;
  const inlineEvents = root.querySelector("[data-event-inline]");
  if (inlineEvents) inlineEvents.textContent = `${task.connection === "disconnected" ? t("state.reconnecting") : connectionDisplay(task.connection, t).label} · ${task.lastEventId ? t("state.lastEvent", { id: task.lastEventId }) : t("event.noEvents")}`;
  const drawerEvents = root.querySelector("[data-event-drawer-events]");
  if (drawerEvents) drawerEvents.innerHTML = `<div class="event"><time>${t("event.connection")}</time><span>${task.connection === "disconnected" ? t("state.reconnecting") : connectionDisplay(task.connection, t).label}</span></div>${eventRows}<div class="event"><time>REST</time><span>${t("event.rest")}</span></div>`;
  if (summary) {
    const results = workspaceResultsForRun(task);
    patchWorkspaceDiagnosticSelection(root, summary, results, workspaceAlarmsForRun(task), state, t, nextRunId);
    drawDiagnosticCharts(root, summary, results);
  }
  return true;
}

function patchQueueDynamicState(root, state, task, t, bindQueueCancels) {
  const queue = root.querySelector("[data-queue-view]");
  if (!queue) return false;
  const items = Array.isArray(state.queue?.items) ? state.queue.items : [];
  const active = queueActiveRun(items, task);
  const waiting = items.filter(item => String(item.status).toUpperCase() === "QUEUED").sort((left, right) => Number(left.queue_position ?? Number.MAX_SAFE_INTEGER) - Number(right.queue_position ?? Number.MAX_SAFE_INTEGER));
  const capacity = root.querySelector("[data-queue-capacity]");
  const activeCard = root.querySelector("[data-queue-active]");
  const table = root.querySelector("[data-queue-table]");
  if (!capacity || !activeCard || !table) return false;
  const tableScroll = table.querySelector?.(".table-wrap")?.scrollTop ?? null;
  const focused = root.ownerDocument?.activeElement;
  const focusedCancelRun = queue.contains?.(focused) ? focused?.getAttribute?.("data-queue-cancel") : null;
  const waitingCount = queueWaitingCount(state.queue);
  capacity.innerHTML = `<div><span>${t("queue.activeSlot")}</span><strong>${active ? 1 : 0} / 1</strong></div><div class="capacity-track" aria-hidden="true"><i style="width:${active ? 100 : 0}%"></i></div><div><span>${t("queue.waiting")}</span><strong>${waitingCount} / 10</strong></div><small>${t("queue.capacityHint")}</small>`;
  activeCard.outerHTML = queueActiveTask(active, task.detail, state, t, task.events);
  table.innerHTML = queueTableContent(!active && !waiting.length, waiting, state.queue?.error, state, t);
  const replacementTable = root.querySelector("[data-queue-table]");
  const replacementScroll = replacementTable?.querySelector?.(".table-wrap");
  if (replacementScroll && tableScroll !== null) replacementScroll.scrollTop = tableScroll;
  bindQueueCancels?.();
  if (focusedCancelRun && root.ownerDocument?.activeElement !== focused) {
    matchingNodes(root, "[data-queue-cancel]").find((node: any) => node.getAttribute("data-queue-cancel") === focusedCancelRun)?.focus?.({ preventScroll: true });
  }
  return true;
}

export function queueWaitingCount(queueOrItems) {
  if (!Array.isArray(queueOrItems) && Number.isFinite(Number(queueOrItems?.waitingCount))) return Math.max(0, Number(queueOrItems.waitingCount));
  const items = Array.isArray(queueOrItems) ? queueOrItems : queueOrItems?.items;
  return (Array.isArray(items) ? items : []).filter(item => String(item?.status ?? "").toUpperCase() === "QUEUED").length;
}

// SSE carries the authoritative queue count even while the compact queue list is
// between REST corrections. Never infer it from a different run or historical row.
export function synchronizeQueueWaitingCount(state, task) {
  const events = Array.isArray(task?.events) ? task.events : [];
  const value = events.length ? events[events.length - 1]?.data?.queued_count : null;
  if (!Number.isFinite(Number(value))) return queueWaitingCount(state?.queue);
  state.queue = { ...(state.queue ?? { items: [] }), waitingCount: Math.max(0, Number(value)) };
  return state.queue.waitingCount;
}

// Keep the compact queue projection aligned to the selected run only. REST remains
// authoritative for ordering; SSE supplies only the current run's incremental state.
export function synchronizeQueueProjection(state, task) {
  const detail = task?.detail;
  const runId = String(detail?.run_id ?? "");
  if (!runId) return state?.queue?.items ?? [];
  const status = String(detail?.status ?? "").toUpperCase();
  const terminal = ["COMPLETED", "CANCELLED", "FAILED", "FAILED_RECOVERABLE"].includes(status);
  const queue = state.queue ?? { items: [] };
  const items = Array.isArray(queue.items) ? queue.items : [];
  const index = items.findIndex(item => item?.run_id === runId);
  if (terminal) {
    if (index >= 0) state.queue = { ...queue, items: items.filter(item => item?.run_id !== runId) };
    return state.queue?.items ?? items;
  }
  if (!["QUEUED", "RUNNING", "CANCELLING"].includes(status)) return items;
  const projected = { ...(index >= 0 ? items[index] : {}), ...detail, cancellable: detail.cancellable === true };
  state.queue = { ...queue, items: index >= 0 ? items.map((item, itemIndex) => itemIndex === index ? projected : item) : [...items, projected] };
  return state.queue.items;
}

function patchDataDynamicState(root, state, t) {
  const page = root.querySelector(".data-page");
  const uploadStatus = root.querySelector("[data-upload-status]");
  const datasetContent = root.querySelector("[data-dataset-content]");
  const statistics = root.querySelector("[data-data-stats-content]");
  const validationContent = root.querySelector("[data-validation-content]");
  const useDataset = root.querySelector("[data-use-dataset]") as HTMLButtonElement | null;
  if (!page || !uploadStatus || !datasetContent || !statistics || !validationContent || !useDataset) return false;
  const statisticsMarkup = dataStatisticsSection(state.dataset, t);
  const selection = root.querySelector("#file-selection");
  if (selection) selection.textContent = state.upload?.fileName ?? t("data.noFileSelected");
  const fileInput = root.querySelector("[data-file]") as HTMLInputElement | null;
  if (fileInput) fileInput.disabled = state.upload?.percent !== null && state.upload?.percent !== undefined;
  uploadStatus.innerHTML = dataUploadStatus(state.upload, t);
  datasetContent.innerHTML = state.dataset ? datasetDetail(state.dataset, t) : emptyDatasetDetail(t);
  statistics.hidden = !statisticsMarkup;
  statistics.innerHTML = statisticsMarkup;
  validationContent.innerHTML = dataValidationReport(state.dataset, state.upload, t);
  useDataset.disabled = state.dataset?.status !== "VALID";
  return true;
}

function patchHistoryDynamicState(root, state, task, t, bindHistory) {
  const list = root.querySelector("[data-history-list]");
  if (!list) return false;
  const table = root.querySelector("[data-history-table-wrap]");
  const history = historyState(state);
  const scrollTop = table?.scrollTop ?? history.restoreScrollTop ?? null;
  const documentRef = root.ownerDocument;
  const active = documentRef?.activeElement;
  const restoreMoreFocus = Boolean(active?.hasAttribute?.("data-history-more"));
  list.innerHTML = historyListContent(state, task, t);
  const replacementTable = root.querySelector("[data-history-table-wrap]");
  if (replacementTable && scrollTop !== null) {
    replacementTable.scrollTop = scrollTop;
    if (history.restoreScrollTop !== null && history.restoreScrollTop !== undefined) state.history = { ...state.history, restoreScrollTop: null };
  }
  const count = root.querySelector("[data-history-count]");
  const currentHistory = historyState(state);
  if (count) count.textContent = t("history.count", { shown: filteredHistoryItems(currentHistory).length, total: historyTotal(currentHistory) });
  bindHistory?.();
  if (restoreMoreFocus) root.querySelector("[data-history-more]")?.focus?.({ preventScroll: true });
  return true;
}

export function patchDynamicApplication(root, state, task, taskStore, i18n, source, onQueueCancel: (() => void) | null = null, onHistoryInteractions: (() => void) | null = null): boolean {
  const header = root?.querySelector?.(".topbar");
  const navigation = root?.querySelector?.(".nav");
  const main = root?.querySelector?.("#main-content");
  if (!header || !navigation || !main) return false;
  const t = i18n.t.bind(i18n);
  updateConfigurationChrome(root, state, task, taskStore, i18n);
  let patched = false;
  if (state.view === "config") patched = Boolean(root.querySelector("[data-config-detail-scroll]"));
  else if (state.view === "workspace") patched = patchWorkspaceDynamicState(root, state, task, t);
  else if (state.view === "queue") patched = patchQueueDynamicState(root, state, task, t, onQueueCancel);
  else if (state.view === "history") patched = patchHistoryDynamicState(root, state, task, t, onHistoryInteractions);
  else if (state.view === "data") patched = patchDataDynamicState(root, state, t);
  else if (state.view === "replay") patched = true;
  if (!patched || root.querySelector(".topbar") !== header || root.querySelector(".nav") !== navigation || root.querySelector("#main-content") !== main) return false;
  void source;
  return true;
}

export function preserveConfigDynamicState(root, state, updateChrome): boolean {
  if (state?.view !== "config") return false;
  const detail = root?.querySelector?.("[data-config-detail-scroll]");
  if (!detail) return false;
  const scrollTop = detail.scrollTop;
  const documentRef = root.ownerDocument;
  const focused = documentRef?.activeElement;
  const restoreFocus = Boolean(focused && detail.contains?.(focused));
  updateChrome();
  if (root.querySelector?.("[data-config-detail-scroll]") !== detail) return false;
  detail.scrollTop = scrollTop;
  if (restoreFocus && documentRef?.activeElement !== focused && typeof focused.focus === "function") focused.focus({ preventScroll: true });
  detail.scrollTop = scrollTop;
  return true;
}

function viewTemplate(state, task, i18n) {
  if (state.view === "data") return dataView(state, i18n);
  if (state.view === "config") return configView(state, i18n);
  if (state.view === "queue") return queueView(state, task, i18n);
  if (state.view === "history") return historyView(state, task, i18n);
  if (state.view === "replay") return replayView(state, task, i18n);
  return workspaceView(state, task, i18n);
}

function workspaceSummaryForRun(task) {
  const summary = task?.summary;
  const runId = task?.detail?.run_id ?? task?.runId ?? null;
  if (!summary || !runId) return null;
  if (Object.prototype.hasOwnProperty.call(task ?? {}, "summaryRunId")) return task.summaryRunId === runId ? summary : null;
  const declaredRunId = summary?.run_id ?? summary?.run?.run_id ?? null;
  return declaredRunId === runId ? summary : null;
}

// A resource is renderable only when its declared run identity matches the run visible in Workspace.
function workspaceResourceForRun(task, resource) {
  const value = task?.[resource];
  const runId = task?.detail?.run_id ?? task?.runId ?? null;
  if (!value || !runId) return null;
  const runKey = `${resource}RunId`;
  if (Object.prototype.hasOwnProperty.call(task ?? {}, runKey)) return task[runKey] === runId ? value : null;
  const declaredRunId = value?.run_id ?? value?.run?.run_id ?? null;
  return declaredRunId === runId ? value : null;
}

function workspaceResultsForRun(task) { return workspaceResourceForRun(task, "results"); }
function workspaceAlarmsForRun(task) { return workspaceResourceForRun(task, "alarms"); }
function workspaceArtifactsForRun(task) { return workspaceResourceForRun(task, "artifacts"); }

export function workspaceResultFilesPresentation(detail, summary) {
  const completed = String(detail?.status ?? "").toUpperCase() === "COMPLETED";
  const committedState = String(detail?.artifact_state ?? summary?.artifact_state ?? "").toUpperCase();
  const integrity = String(summary?.artifact_integrity?.status ?? "").toUpperCase();
  const committed = !committedState || ["COMMITTED", "VERIFIED"].includes(committedState);
  const verified = completed && committed && ["VERIFIED", "COMMITTED"].includes(integrity);
  return { verified, incomplete: Boolean(summary) && !verified };
}

function workspaceResultFilesNotice(presentation, t) {
  if (!presentation) return "";
  const verified = presentation.verified;
  return `<section class="panel workspace-result-files" data-workspace-result-files><div class="callout ${verified ? "success" : "error"}"><strong>${verified ? t("history.artifactsComplete") : t("workspace.artifactsIncomplete")}</strong></div></section>`;
}

function frozenRailGroup(profile, groupKeys, label, t, marker, open = false) {
  const agent = Number(profile?.selectedAgent ?? 1);
  const selected = Array.isArray(profile?.agents) ? profile.agents.find(item => Number(item?.agent) === agent) : null;
  const effective = mergeParameterValues(profile?.shared_parameters ?? {}, selected?.parameters ?? {});
  const groups = groupKeys.map(key => [key, effective?.[key]] as [string, any]).filter(([, value]) => isRecord(value));
  if (!groups.length) return "";
  const groupLabel = `<span class="frozen-rail-group-title"><i class="rail-group-icon ${escapeHtml(marker.tone)}" aria-hidden="true">${escapeHtml(marker.code)}</i><span>${escapeHtml(label)}</span></span>`;
  return `<details class="frozen-rail-group"${open ? " open" : ""}><summary>${groupLabel}<span>⌄</span></summary><div class="readout">${groups.map(([key, value]) => parameterLeafReadout(value, key, t)).join("")}</div></details>`;
}

function frozenRail(detail, selectedAgent, agentTabs, t) {
  const profile = frozenParameterProfile(detail);
  const snapshot = runSnapshotDisplay(detail, t);
  const name = frozenProfilePrimaryText({ mode: profile?.mode ?? detail?.run_mode, display_name: profile?.display_name ?? detail?.parameter_profile_display_name ?? detail?.display_name }, t, t("workspace.notConfigured"));
  const railProfile = profile ? { ...profile, selectedAgent } : null;
  const groups = railProfile ? `${frozenRailGroup(railProfile, ["local_gp"], t("workspace.railLocalGpr"), t, { code: "G", tone: "cyan" }, true)}${frozenRailGroup(railProfile, ["feature_state", "cleaning"], t("workspace.railDataProcessing"), t, { code: "P", tone: "pink" })}${frozenRailGroup(railProfile, ["split", "anchors"], t("workspace.railSplitsAnchors"), t, { code: "S", tone: "cyan" })}${frozenRailGroup(railProfile, ["global_surrogate", "support", "fusion"], t("workspace.railOnlineFusion"), t, { code: "F", tone: "green" })}${frozenRailGroup(railProfile, ["interval", "alarms"], t("workspace.railIntervalsAlarms"), t, { code: "A", tone: "amber" })}` : `<p class="muted">${t("profileSnapshot.unavailable")}</p>`;
  return `<aside class="panel parameter-rail"><p class="eyebrow">${t("workspace.runConfiguration")}</p><h2>${escapeHtml(name)}</h2><p class="muted">${t("workspace.snapshotTitle")}</p><div class="snapshot rail-snapshot">${snapshotRow(t("workspace.dataset"), displayProfileText(detail?.dataset?.display_name, t, t("workspace.notSelected")), "text", t)}${snapshotRow(t("workspace.parameterVersion"), snapshot.parameter, "text", t)}${snapshotRow(t("workspace.mapping"), snapshot.mapping, "text", t)}${snapshotRow(t("workspace.hash"), snapshot.snapshotHash ?? "—", "hash", t)}</div><div class="agent-tabs" role="tablist" aria-label="${t("aria.agents")}">${agentTabs}</div><div class="frozen-rail-groups" data-parameter-profile-snapshot>${groups}</div><button class="button workspace-adjust" data-nav="config">${t("workspace.configureNew")}</button><p class="muted workspace-adjust-hint">${t("workspace.adjustParametersHint")}</p></aside>`;
}

function workspaceLiveSummary(detail, task, t) {
  if (!detail) return "";
  const status = String(detail.status ?? "").toUpperCase();
  const heading = status === "RUNNING" ? t("workspace.runningLive") : status === "COMPLETED" ? t("workspace.completedRun") : localizedStatus(status, t);
  return `<div class="workspace-live-summary" data-workspace-run-live><span class="state ${statusClass(status)}" data-workspace-live-summary-status>${escapeHtml(heading)}</span><span data-workspace-elapsed>${t("workspace.elapsed", { value: elapsedDuration(detail.elapsed_ms) })}</span><span>${t("system.stage")}: <strong data-workspace-stage>${escapeHtml(detail.current_stage ?? "—")}</strong></span><button class="button compact" data-event-drawer-open aria-haspopup="dialog">${t("workspace.openEvents")}</button></div>`;
}

export function workspaceView(state, task, i18n) {
  const t = i18n.t.bind(i18n); const detail = task.detail; const summary = workspaceSummaryForRun(task);
  const snapshotDisplay = runSnapshotDisplay(detail, t);
  const runId = detail?.run_id ?? "";
  const taskStatus = String(detail?.status ?? "").toUpperCase();
  const isTerminal = ["COMPLETED", "FAILED", "FAILED_RECOVERABLE", "CANCELLED"].includes(taskStatus);
  const taskNotice = !detail ? "" : summary ? "" : `<section class="panel workspace-result-pending" data-workspace-result-state><div class="callout ${isTerminal ? "error" : ""}"><strong>${isTerminal ? t("workspace.resultsUnavailable") : t("workspace.resultsPending")}</strong></div></section>`;
  const resultFilesNotice = summary ? workspaceResultFilesNotice(workspaceResultFilesPresentation(detail, summary), t) : "";
  const frozenStatus = detail ? (summary ? t("workspace.frozen") : localizedStatus(taskStatus, t)) : "—";
  const liveStatus = task.loading ? t("state.loading") : detail ? localizedStatus(taskStatus, t) : "";
  const agentTabs = [1, 2, 3].map(agent => {
    const enabled = Boolean(summary);
    const selected = enabled && task.selectedAgent === agent;
    return `<button data-agent="${agent}" class="${selected ? "active" : ""}" role="tab" aria-selected="${selected}" ${enabled ? "" : "disabled"}>${t("workspace.agent", { agent })}<small>${displayAgentSegment(["EARLY", "MIDDLE", "LATE"][agent - 1], t)}</small></button>`;
  }).join("");
  const rail = detail ? frozenRail(detail, task.selectedAgent, agentTabs, t) : `<aside class="panel parameter-rail"><p class="eyebrow">${t("workspace.snapshot")}</p><h2>${t("workspace.notConfigured")}</h2><span class="state">${frozenStatus}</span><div class="agent-tabs" role="tablist" aria-label="${t("aria.agents")}">${agentTabs}</div><p class="muted">${t("workspace.notSelected")}</p></aside>`;
  const main = !detail ? `<div><header class="page-head"><div><p class="eyebrow">${t("workspace.eyebrow")}</p><h1>${t("workspace.title")}</h1><p>${t("workspace.description")}</p></div></header><section class="panel workspace-onboarding"><p class="eyebrow">${t("workspace.notConfigured")}</p><h2>${t("workspace.notConfigured")}</h2><p>${t("workspace.onboarding")}</p><div class="inline-actions"><button class="button primary" data-nav="data">${t("workspace.goData")}</button><button class="button" data-nav="config">${t("workspace.goParameters")}</button></div></section></div>` : `<div><header class="page-head"><div><p class="eyebrow">${t("workspace.eyebrow")}</p><h1>${t("workspace.title")}</h1><p>${t("workspace.description")}</p></div><span class="state ${task.loading ? "VALIDATING" : statusClass(taskStatus)}" data-workspace-live-status>${liveStatus}</span></header>${task.error ? errorPanel(task.error, t) : ""}<section class="panel workspace-live-task"><div class="panel-heading"><div><p class="eyebrow">${t("workspace.liveTask")}</p><h2>${escapeHtml(displayProfileText(detail.display_name ?? detail.run_id, t))}</h2></div><code>${escapeHtml(detail.run_id)}</code></div>${workspaceLiveSummary(detail, task, t)}<div class="snapshot">${snapshotRow(t("event.title"), `<span data-workspace-event>${task.lastEventId ? t("state.lastEvent", { id: task.lastEventId }) : t("workspace.noTaskEvents")}</span>`, "text", t)}</div></section>${taskNotice}${resultFilesNotice}${summary ? renderSelectedAgentResults(summary, t) : ""}${summary ? chartPanel(summary, state, t) : ""}${summary ? diagnosticPanel(summary, workspaceResultsForRun(task), workspaceAlarmsForRun(task), workspaceArtifactsForRun(task), task, state, t) : ""}${eventPanel(task, t)}</div>`;
  const selectedAgent = summary?.selection?.agent === null || summary?.selection?.agent === undefined ? "" : String(summary.selection.agent);
  return `<section class="workspace" data-workspace-view data-run-id="${escapeHtml(runId)}" data-has-summary="${Boolean(summary)}" data-workspace-agent="${escapeHtml(selectedAgent)}">${rail}${main}</section>`;
}

function elapsedDuration(milliseconds) {
  const totalSeconds = Math.max(0, Math.floor(Number(milliseconds ?? 0) / 1000));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor(totalSeconds % 3600 / 60);
  const seconds = totalSeconds % 60;
  return hours ? `${hours}:${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}` : `${minutes}:${String(seconds).padStart(2, "0")}`;
}

function queueStatus(status, t) {
  const raw = String(status ?? "").toUpperCase();
  return `<span class="state ${statusClass(raw)}" aria-label="${escapeHtml(localizedStatus(raw, t))}">${escapeHtml(localizedStatus(raw, t))}</span>`;
}

function queueStageTimeline(run, detail, t) {
  const currentStage = detail?.current_stage ?? run?.current_stage ?? null;
  const stages = ["VALIDATING", "PREPROCESSING", "LOCAL_TRAINING", "ANCHOR_AGGREGATING", "GLOBAL_DISTILLING", "CALIBRATING", "TESTING"];
  if (!currentStage) return `<div class="empty queue-stage-empty">${t("queue.noStage")}</div>`;
  const currentIndex = stages.indexOf(currentStage);
  return `<div class="stage-stepper" aria-label="${t("queue.stage")}">${stages.map((stage, index) => `<div class="${stage === currentStage ? "active" : ""} ${currentIndex > index ? "complete" : ""}"><i>${currentIndex > index ? "✓" : stage === currentStage ? "•" : ""}</i><span>${escapeHtml(stage)}</span><small>${currentIndex > index ? t("status.completed") : stage === currentStage ? localizedStatus(run?.status ?? "RUNNING", t) : "—"}</small></div>`).join("")}</div>`;
}

export function queueRunIsCancellable(run: any) {
  return run?.cancellable === true && String(run?.status ?? "").toUpperCase() !== "CANCELLING";
}

function queueCancelIsPending(state, runId) {
  return Array.isArray(state.queue?.cancellingRunIds) && state.queue.cancellingRunIds.includes(runId);
}

function queueCancelAttributes(state, runId) {
  return queueCancelIsPending(state, runId) ? ' disabled aria-busy="true"' : "";
}

function eventStateText(event, t) {
  const data = event?.data ?? {};
  const parts = [] as string[];
  if (data.status) parts.push(localizedStatus(String(data.status), t));
  if (data.current_stage) parts.push(t("event.stage", { stage: String(data.current_stage) }));
  if (data.queue_position !== null && data.queue_position !== undefined && Number.isFinite(Number(data.queue_position))) parts.push(t("event.queuePosition", { position: Number(data.queue_position) }));
  if (data.queued_count !== null && data.queued_count !== undefined && Number.isFinite(Number(data.queued_count))) parts.push(t("event.waitingCount", { count: Number(data.queued_count) }));
  return parts.length ? parts.join(" · ") : t("event.received");
}

function taskEventRows(events, t) {
  const recent = (Array.isArray(events) ? events : []).slice(-12).reverse();
  return recent.map(event => `<div class="event"><time>${event?.id === null || event?.id === undefined ? "—" : `#${escapeHtml(String(event.id))}`}</time><span><code>${escapeHtml(String(event?.type ?? "message"))}</code><br>${escapeHtml(eventStateText(event, t))}</span></div>`).join("");
}

export function queueActiveRun(items, task) {
  const detail = task?.detail;
  const detailRunId = detail?.run_id;
  const status = String(detail?.status ?? "").toUpperCase();
  const active = (Array.isArray(items) ? items : []).find(item => ["RUNNING", "CANCELLING"].includes(String(item?.status ?? "").toUpperCase())) ?? null;
  const detailIsTerminal = ["COMPLETED", "CANCELLED", "FAILED", "FAILED_RECOVERABLE"].includes(status);
  if (active && detailRunId === active.run_id && detailIsTerminal) return null;
  if (active) return active;
  if (!detailRunId || (task?.runId && task.runId !== detailRunId)) return null;
  return ["RUNNING", "CANCELLING"].includes(status) ? detail : null;
}

function queueActiveTask(active, detail, state, t, taskEvents = []) {
  if (!active) return `<article class="panel active-task-card" data-queue-active><div class="empty">${t("queue.noActive")}</div></article>`;
  const runDetail = detail?.run_id === active.run_id ? detail : null;
  const canCancel = queueRunIsCancellable({ ...active, ...(runDetail ?? {}) });
  const profileVersion = displayProfileVersion(active.parameter_version ?? active.parameter_profile_version_id, t);
  const dataset = displayProfileText(active.dataset?.display_name ?? active.dataset_display_name, t);
  const eventId = runDetail?.latest_event_id ?? active.latest_event_id ?? null;
  const events = taskEvents;
  return `<article class="panel active-task-card" data-queue-active data-run-id="${escapeHtml(active.run_id)}"><div class="active-task-head"><div>${queueStatus(active.status, t)}<h2>${escapeHtml(active.run_id)}</h2><p><b>${escapeHtml(active.run_mode ?? "—")}</b> · ${escapeHtml(dataset)} · ${t("queue.agents")} · ${t("queue.elapsed")}: ${elapsedDuration(active.elapsed_ms)}</p></div>${canCancel ? `<button class="button danger" data-queue-cancel="${escapeHtml(active.run_id)}"${queueCancelAttributes(state, active.run_id)}>${t("queue.cancelActive")}</button>` : ""}</div><div class="snapshot queue-active-meta">${snapshotRow(t("queue.parameters"), profileVersion)}${snapshotRow(t("queue.dataset"), dataset)}${snapshotRow(t("queue.stage"), runDetail?.current_stage ?? active.current_stage ?? t("queue.noStage"))}</div>${queueStageTimeline(active, runDetail, t)}<div class="event-console" data-queue-events aria-live="polite">${events.length ? taskEventRows(events, t) : `<div>${eventId ? `<time>${escapeHtml(String(eventId))}</time><span>${t("queue.latestEvent", { id: eventId })}</span><strong>${escapeHtml(runDetail?.current_stage ?? active.current_stage ?? t("queue.noStage"))}</strong>` : `<span>${t("queue.eventsEmpty")}</span>`}</div>`}</div></article>`;
}

function queueRows(items, state, t) {
  return items.map(item => `<tr data-queue-row="${escapeHtml(item.run_id)}"><td>${item.queue_position ?? "—"}</td><td><code>${escapeHtml(item.run_id)}</code></td><td>${escapeHtml(item.run_mode ?? "—")}<br><small>${escapeHtml(displayProfileVersion(item.parameter_version ?? item.parameter_profile_version_id, t))}</small></td><td>${escapeHtml(displayProfileText(item.dataset?.display_name ?? item.dataset_display_name, t))}</td><td>${escapeHtml(item.created_at ?? "—")}</td><td>${queueStatus(item.status, t)}</td><td>${queueRunIsCancellable(item) ? `<button class="button compact" data-queue-cancel="${escapeHtml(item.run_id)}"${queueCancelAttributes(state, item.run_id)}>${t("queue.cancel")}</button>` : ""}</td></tr>`).join("");
}

function queueTableContent(empty, waiting, error, state, t) {
  return `<div class="panel-heading"><div><p class="eyebrow">${t("queue.waiting")}</p><h2>${t("queue.waitingRuns")}</h2></div><span class="state QUEUED">${t("queue.fifo")}</span></div>${error ? errorPanel(error, t) : ""}${empty ? `<div class="empty">${t("queue.empty")}</div>` : waiting.length ? `<div class="table-wrap"><table class="queue-table"><thead><tr><th>${t("queue.position")}</th><th>${t("queue.run")}</th><th>${t("queue.parameters")}</th><th>${t("queue.dataset")}</th><th>${t("queue.created")}</th><th>${t("queue.status")}</th><th><span class="skip">${t("queue.cancel")}</span></th></tr></thead><tbody data-queue-rows>${queueRows(waiting, state, t)}</tbody></table></div>` : `<div class="empty">${t("queue.noWaiting")}</div>`}`;
}

export function queueView(state, task, i18n) {
  const t = i18n.t.bind(i18n);
  const items = Array.isArray(state.queue?.items) ? state.queue.items : [];
  const active = queueActiveRun(items, task);
  const waiting = items.filter(item => String(item.status).toUpperCase() === "QUEUED").sort((left, right) => Number(left.queue_position ?? Number.MAX_SAFE_INTEGER) - Number(right.queue_position ?? Number.MAX_SAFE_INTEGER));
  const waitingCount = queueWaitingCount(state.queue);
  const empty = !active && !waiting.length && waitingCount === 0;
  return `<section data-queue-view><header class="page-head"><div><p class="eyebrow">${t("queue.title")}</p><h1>${t("queue.title")}</h1><p>${t("queue.description")}</p></div></header><div class="queue-capacity panel" data-queue-capacity><div><span>${t("queue.activeSlot")}</span><strong>${active ? 1 : 0} / 1</strong></div><div class="capacity-track" aria-hidden="true"><i style="width:${active ? 100 : 0}%"></i></div><div><span>${t("queue.waiting")}</span><strong>${waitingCount} / 10</strong></div><small>${t("queue.capacityHint")}</small></div>${queueActiveTask(active, task.detail, state, t, task.events)}<article class="panel queue-table-panel" data-queue-table>${queueTableContent(empty, waiting, state.queue?.error, state, t)}</article></section>`;
}

function listPageItems(response) { return Array.isArray(response?.items) ? response.items : Array.isArray(response?.data) ? response.data : Array.isArray(response) ? response : []; }

function listPageMeta(response) {
  const meta = response?.meta ?? {};
  return {
    nextCursor: response?.next_cursor ?? meta.next_cursor ?? null,
    hasMore: Boolean(response?.has_more ?? meta.has_more ?? false),
    total: response?.total ?? meta.total ?? null
  };
}

async function reconcileQueueItems(items, state, taskStore) {
  const list = Array.isArray(items) ? items : [];
  const details = await Promise.all(list.map(async item => {
    if (!item?.run_id) return null;
    if (typeof state.api.getSimulation === "function") {
      try { return await state.api.getSimulation(item.run_id); }
      catch { return null; }
    }
    const selected = taskStore?.state?.detail;
    return selected?.run_id === item.run_id ? selected : null;
  }));
  return list.map((item, index) => {
    const detail = details[index];
    if (detail?.run_id === item?.run_id) return { ...item, ...detail, cancellable: detail.cancellable === true };
    return { ...item, cancellable: item?.cancellable === true };
  });
}

export async function loadQueue(state, render, taskStore: any = null) {
  if (state.queue?.loading || typeof state.api.listSimulations !== "function") return;
  state.queue = { ...state.queue, loading: true, error: null };
  render({ dynamic: true, source: "queue-list" });
  try {
    const response = await state.api.listSimulations({ view: "queue", limit: 11 });
    const meta = listPageMeta(response);
    const items = await reconcileQueueItems(listPageItems(response).slice(0, 11), state, taskStore);
    state.queue = { ...state.queue, items, waitingCount: queueWaitingCount(items), loading: false, error: null, ...meta };
    const current = taskStore?.state?.runId;
    const next = state.queue.items.find(item => ["RUNNING", "CANCELLING", "QUEUED"].includes(String(item?.status ?? "").toUpperCase()));
    const selectedIsTerminal = isTerminalHistoryRun(taskStore?.state?.detail);
    const canSelectQueueRun = state.view !== "history" && state.view !== "replay";
    if (canSelectQueueRun && (!current || (state.view === "queue" && selectedIsTerminal)) && next?.run_id && current !== next.run_id) void taskStore.selectRun(next.run_id);
  } catch (error) {
    state.queue = { ...state.queue, loading: false, error };
  }
  render({ dynamic: true, source: "queue-list" });
}

const TERMINAL_HISTORY_STATUSES = new Set(["COMPLETED", "FAILED", "FAILED_RECOVERABLE", "CANCELLED"]);

function historyState(state) {
  return state.history ?? { items: [], aggregateMetrics: {}, loading: false, error: null, query: "", status: "", mode: "", nextCursor: null, hasMore: false, total: null, selectedRunId: null, detail: null, detailLoading: false, detailError: null, deepLinkNotice: null, artifacts: null, replay: { agent: 1, loading: false, error: null, data: null, results: null, alarms: null } };
}

function isTerminalHistoryRun(run) { return TERMINAL_HISTORY_STATUSES.has(String(run?.status ?? "").toUpperCase()); }
function terminalHistoryCount(history) { return (Array.isArray(history?.items) ? history.items : []).filter(isTerminalHistoryRun).length; }
function historyTotal(history) { return Number.isFinite(Number(history?.total)) ? Number(history.total) : terminalHistoryCount(history); }

export function filteredHistoryItems(history) {
  const query = String(history?.query ?? "").trim().toLowerCase();
  const status = String(history?.status ?? "").toUpperCase();
  const mode = String(history?.mode ?? "").toUpperCase();
  return (Array.isArray(history?.items) ? history.items : []).filter(isTerminalHistoryRun).filter(item => {
    const itemStatus = String(item?.status ?? "").toUpperCase();
    const itemMode = String(item?.run_mode ?? item?.mode ?? "").toUpperCase();
    const dataset = item?.dataset?.display_name ?? item?.dataset_display_name ?? "";
    return (!query || `${item?.run_id ?? ""} ${dataset}`.toLowerCase().includes(query)) && (!status || itemStatus === status) && (!mode || itemMode === mode);
  });
}

function admissionForRun(run) {
  const snapshot = run?.snapshot ?? {};
  const parameter = run?.parameter_snapshot ?? snapshot.parameter_snapshot ?? snapshot.parameter_profile ?? {};
  const parameterReference = versionReference(parameter);
  const runReference = versionReference(run?.parameter_version ?? run?.parameter_profile_version_id);
  return {
    name: parameterReference.displayName ?? runReference.displayName ?? snapshot.parameter_profile_display_name ?? run?.parameter_profile_display_name ?? "—",
    mode: parameter?.mode ?? snapshot.parameter_profile_mode ?? run?.parameter_profile_mode ?? run?.run_mode ?? null,
    version: parameterReference.versionId ?? snapshot.parameter_profile_version_id ?? runReference.versionId ?? "—",
    hash: parameterReference.hash ?? snapshot.parameter_profile_sha256 ?? runReference.hash ?? run?.parameter_profile_sha256 ?? "—"
  };
}

export function historyArtifactGate(run: any, artifacts: any = null) {
  const completed = String(run?.status ?? "").toUpperCase() === "COMPLETED";
  const artifactState = String(artifacts?.artifact_state ?? run?.artifact_state ?? "").toUpperCase();
  const items = Array.isArray(artifacts?.items) ? artifacts.items : null;
  const validRequiredItems = items === null || items.filter(item => item?.required).every(item => Boolean(item?.name) && typeof item?.sha256 === "string" && Number(item?.size_bytes) >= 0);
  const ready = completed && artifactState === "COMMITTED" && validRequiredItems;
  if (ready) return { ready, key: "complete", requiredCount: items?.filter(item => item?.required).length ?? null };
  if (!completed) return { ready, key: "terminal", requiredCount: items?.filter(item => item?.required).length ?? null };
  return { ready, key: "incomplete", requiredCount: items?.filter(item => item?.required).length ?? null };
}

function historyMetric(run, history) {
  const aggregate = history?.aggregateMetrics?.[run?.run_id];
  const candidate = aggregate ?? run?.summary_metric?.FusedRMSE ?? run?.summary_metric?.fused_rmse ?? run?.summary?.metrics?.FusedRMSE ?? run?.summary?.metrics?.fused_rmse ?? null;
  return Number.isFinite(Number(candidate)) ? String(candidate) : "—";
}

function historyArtifactsLabel(run, artifacts, t) {
  const gate = historyArtifactGate(run, artifacts);
  if (gate.ready) return `<span class="state COMPLETED">${t("history.artifactsComplete")}${gate.requiredCount === null ? "" : ` · ${gate.requiredCount}`}</span>`;
  return `<span class="state ${gate.key === "terminal" ? statusClass(run?.status) : "FAILED"}">${gate.key === "terminal" ? t("history.artifactsUnavailable") : t("history.artifactsIncomplete")}</span>`;
}

function historyRow(run, task, history, t) {
  const admission = admissionForRun(run);
  const selected = history.selectedRunId === run.run_id;
  const artifacts = selected ? history.artifacts : null;
  const gate = historyArtifactGate(run, artifacts);
  const actionHint = gate.key === "terminal" ? t("history.terminalOnly") : t("history.artifactsRequired");
  return `<tr data-history-row="${escapeHtml(run.run_id)}" ${selected ? "aria-selected=\"true\"" : ""}><td><button class="history-run-link" data-history-select="${escapeHtml(run.run_id)}"><code>${escapeHtml(run.run_id)}</code></button></td><td>${escapeHtml(run.finished_at ?? "—")}</td><td><span class="state ${statusClass(run.run_mode ?? "")}">${escapeHtml(run.run_mode ?? "—")}</span><br><span>${escapeHtml(frozenProfilePrimaryText(admission, t))}</span><br><small>${escapeHtml(displayProfileVersion(admission.version, t))} · ${renderHashValue(admission.hash, "hash-value hash-value-inline", t)}</small></td><td>${escapeHtml(displayProfileText(run.dataset?.display_name ?? run.dataset_display_name, t))}</td><td>${queueStatus(run.status, t)}</td><td>${historyMetric(run, history)}</td><td>${historyArtifactsLabel(run, artifacts, t)}</td><td><div class="history-actions"><button class="button compact" data-history-select="${escapeHtml(run.run_id)}">${t("history.inspect")}</button><button class="button compact" data-history-replay="${escapeHtml(run.run_id)}" ${gate.ready ? "" : `disabled title="${escapeHtml(actionHint)}"`}>${t("action.openReplay")}</button><button class="button compact" data-history-export="${escapeHtml(run.run_id)}" ${gate.ready ? "" : `disabled title="${escapeHtml(actionHint)}"`}>${t("history.export")}</button><button class="button compact" data-history-manifest="${escapeHtml(run.run_id)}" ${gate.ready ? "" : `disabled title="${escapeHtml(actionHint)}"`}>${t("history.manifest")}</button></div></td></tr>`;
}

function historyListContent(state, task, t) {
  const history = historyState(state); const items = filteredHistoryItems(history);
  if (history.loading && !history.items.length) return `<div class="empty">${t("history.loading")}</div>`;
  if (history.error && !history.items.length) return errorPanel(history.error, t);
  if (!history.items.length) return `<div class="empty">${t("history.empty")}</div>`;
  if (!items.length) return `<div class="empty">${t("history.filterEmpty")}</div>`;
  const more = history.hasMore ? `<div class="history-more"><button class="button" data-history-more ${history.loading ? "disabled" : ""}>${history.loading ? t("history.loading") : t("history.loadMore")}</button></div>` : "";
  return `<div class="table-wrap history-table-wrap" data-history-table-wrap><table class="queue-table history-table"><thead><tr><th>${t("history.run")}</th><th>${t("history.finished")}</th><th>${t("history.admission")}</th><th>${t("workspace.dataset")}</th><th>${t("queue.status")}</th><th>${t("history.metric")}</th><th>${t("history.artifacts")}</th><th><span class="skip">${t("history.actions")}</span></th></tr></thead><tbody data-history-rows>${items.map(item => historyRow(item, task, history, t)).join("")}</tbody></table></div>${more}`;
}

export function historyView(state, task, i18n) {
  const t = i18n.t.bind(i18n); const history = historyState(state); const selected = history.detail?.run_id === history.selectedRunId ? history.detail : null;
  const selectedGate = selected ? historyArtifactGate(selected, history.artifacts) : null;
  const deepLinkNotice = history.deepLinkNotice ? `<section class="panel"><div class="callout error" data-history-deep-link-notice>${t(history.deepLinkNotice)}</div></section>` : "";
  return `<section data-history-view><header class="page-head"><div><p class="eyebrow">${t("history.eyebrow")}</p><h1>${t("history.title")}</h1><p>${t("history.description")}</p></div></header><section class="panel history-filters"><label><span>${t("history.search")}</span><input data-history-query value="${escapeHtml(history.query)}" maxlength="256" placeholder="${t("history.searchPlaceholder")}"></label><label><span>${t("history.statusFilter")}</span><select data-history-status><option value="">${t("history.allStatuses")}</option>${["COMPLETED", "FAILED", "FAILED_RECOVERABLE", "CANCELLED"].map(status => `<option value="${status}" ${history.status === status ? "selected" : ""}>${localizedStatus(status, t)}</option>`).join("")}</select></label><label><span>${t("history.modeFilter")}</span><select data-history-mode><option value="">${t("history.allModes")}</option>${["REFERENCE", "CUSTOM"].map(mode => `<option value="${mode}" ${history.mode === mode ? "selected" : ""}>${escapeHtml(mode)}</option>`).join("")}</select></label><span class="muted" data-history-count>${t("history.count", { shown: filteredHistoryItems(history).length, total: historyTotal(history) })}</span></section>${deepLinkNotice}<section class="panel" data-history-list>${historyListContent(state, task, t)}</section>${history.error && history.items.length ? errorPanel(history.error, t) : ""}${history.detailLoading ? `<section class="panel"><div class="empty">${t("history.detailLoading")}</div></section>` : history.detailError ? `<section class="panel">${errorPanel(history.detailError, t)}</section>` : selected ? `<section class="panel history-detail" data-history-detail><header class="panel-heading"><div><p class="eyebrow">${t("history.selected")}</p><h2>${escapeHtml(displayProfileText(selected.display_name ?? selected.run_id, t))}</h2></div>${selectedGate?.ready ? `<button class="button primary" data-history-replay="${escapeHtml(selected.run_id)}">${t("action.openReplay")}</button>` : `<span class="muted">${selectedGate?.key === "terminal" ? t("history.terminalOnly") : t("history.artifactsRequired")}</span>`}</header><div class="snapshot">${snapshotRow(t("history.run"), selected.run_id, "text", t)}${snapshotRow(t("workspace.dataset"), displayProfileText(selected.dataset?.display_name ?? selected.dataset_snapshot?.display_name, t), "text", t)}${snapshotRow(t("profileSnapshot.name"), frozenProfilePrimaryText(admissionForRun(selected), t), "text", t)}${snapshotRow(t("profileSnapshot.version"), displayProfileVersion(admissionForRun(selected).version, t), "text", t)}${snapshotRow(t("profileSnapshot.hash"), admissionForRun(selected).hash, "hash", t)}</div>${renderFrozenParameterProfileSnapshot(selected, t, true)}<p class="muted">${t("history.rerunHint")}</p></section>` : ""}</section>`;
}

function replayPoints(replay) { return replay?.points ?? replay?.items ?? replay?.data?.points ?? []; }

export function replayChartSummary(summary, replay) {
  const points = replayPoints(replay);
  if (!summary || !points.length) return summary;
  return {
    ...summary,
    chart: {
      ...(summary.chart ?? {}),
      points,
      display_point_count: points.length,
      original_point_count: Number(replay?.total_points ?? summary.chart?.original_point_count ?? points.length)
    }
  };
}

function chartSummaryForView(state, summary) {
  return state.view === "replay" ? replayChartSummary(summary, historyState(state).replay.data) : summary;
}

function replayPlaybackState(state) {
  if (!state.replayPlayback) state.replayPlayback = { playing: false, speed: 1, position: 0, timer: null };
  return state.replayPlayback;
}

export function setReplayPlaybackPosition(state, pointCount, position) {
  const playback = replayPlaybackState(state);
  const maximum = Math.max(0, Number(pointCount ?? 0) - 1);
  playback.position = Math.max(0, Math.min(maximum, Math.round(Number(position) || 0)));
  state.chart.focus = playback.position;
  return playback.position;
}

export function replayKeyboardPosition(key, current, pointCount) {
  const maximum = Math.max(0, Number(pointCount ?? 0) - 1);
  if (key === "Home") return 0;
  if (key === "ArrowRight") return Math.min(maximum, Math.max(0, Number(current) || 0) + 1);
  if (key === "End") return maximum;
  return null;
}

export function advanceReplayPlayback(state, pointCount) {
  const playback = replayPlaybackState(state);
  const maximum = Math.max(0, Number(pointCount ?? 0) - 1);
  if (!maximum || playback.position >= maximum) {
    playback.playing = false;
    return false;
  }
  setReplayPlaybackPosition(state, pointCount, playback.position + Math.max(1, Math.round(Number(playback.speed) || 1)));
  if (playback.position >= maximum) playback.playing = false;
  return playback.playing;
}

function stopReplayPlayback(state) {
  const playback = replayPlaybackState(state);
  if (playback.timer !== null) clearInterval(playback.timer);
  playback.timer = null;
  playback.playing = false;
}

function replayTransportControls(state, points, enabled, t) {
  const playback = replayPlaybackState(state);
  const maximum = Math.max(0, points.length - 1);
  if (playback.position > maximum) setReplayPlaybackPosition(state, points.length, maximum);
  const disabled = enabled && points.length ? "" : "disabled";
  return `<div class="replay-transport" data-replay-transport><button class="button primary" data-replay-play ${disabled} aria-pressed="${playback.playing}">${playback.playing ? t("action.pause") : t("action.play")}</button><label><span>${t("replay.speed")}</span><select data-replay-speed ${disabled}><option value="1" ${playback.speed === 1 ? "selected" : ""}>1×</option><option value="2" ${playback.speed === 2 ? "selected" : ""}>2×</option><option value="4" ${playback.speed === 4 ? "selected" : ""}>4×</option></select></label><label class="replay-position"><span>${t("replay.position")}</span><input data-replay-position type="range" min="0" max="${maximum}" value="${playback.position}" ${disabled} aria-valuetext="${t("replay.positionValue", { current: playback.position + 1, total: points.length })}"></label><output data-replay-progress aria-live="polite">${t(playback.playing ? "replay.playing" : "replay.paused")} · ${t("replay.positionValue", { current: points.length ? playback.position + 1 : 0, total: points.length })}</output></div>`;
}

function updateReplayPlaybackDom(root, state, task, i18n) {
  const playback = replayPlaybackState(state);
  const points = replayPoints(historyState(state).replay.data);
  const play = root.querySelector("[data-replay-play]");
  if (play) { play.textContent = i18n.t(playback.playing ? "action.pause" : "action.play"); play.setAttribute("aria-pressed", String(playback.playing)); }
  const position = root.querySelector("[data-replay-position]");
  if (position) { position.value = String(playback.position); position.setAttribute("aria-valuetext", i18n.t("replay.positionValue", { current: points.length ? playback.position + 1 : 0, total: points.length })); }
  const progress = root.querySelector("[data-replay-progress]");
  if (progress) progress.textContent = `${i18n.t(playback.playing ? "replay.playing" : "replay.paused")} · ${i18n.t("replay.positionValue", { current: points.length ? playback.position + 1 : 0, total: points.length })}`;
  drawChart(root, state, chartSummaryForView(state, task.summary), i18n);
}

function toggleReplayPlayback(root, state, task, i18n) {
  const points = replayPoints(historyState(state).replay.data);
  if (!points.length) return;
  const playback = replayPlaybackState(state);
  if (playback.playing) { stopReplayPlayback(state); updateReplayPlaybackDom(root, state, task, i18n); return; }
  if (playback.position >= points.length - 1) setReplayPlaybackPosition(state, points.length, 0);
  playback.playing = true;
  playback.timer = setInterval(() => {
    const keepPlaying = advanceReplayPlayback(state, points.length);
    if (!keepPlaying) stopReplayPlayback(state);
    updateReplayPlaybackDom(root, state, task, i18n);
  }, 600);
  updateReplayPlaybackDom(root, state, task, i18n);
}

function replayDiagnostic(state, t) {
  const points = replayPoints(historyState(state).replay.data);
  const focused = typeof state.chart?.focus === "number" ? points[state.chart.focus] : null;
  if (!focused) return `<section class="panel"><p class="eyebrow">${t("replay.diagnostics")}</p><h2>${t("replay.pointDiagnostics")}</h2><p class="muted">${t("replay.noPoint")}</p></section>`;
  const alarm = alarmTypePresentation(focused.alarm_type ?? focused.AlarmType ?? focused.OverallAlarmType, t);
  return `<section class="panel"><p class="eyebrow">${t("replay.diagnostics")}</p><h2>${t("replay.pointDiagnostics")}</h2><div class="snapshot">${snapshotRow(t("replay.point"), focused.OriginalRunningIndex ?? "—")}${snapshotRow(t("replay.time"), focused.Time ?? "—")}${snapshotRow(t("replay.loadStatus"), focused.LoadStatus ?? "—")}${snapshotRow(t("replay.alarm"), alarm.token ? `${alarm.label} (${alarm.token})` : focused.OverallAlarmLevel ?? "—")}${snapshotRow(t("tooltip.weightSupport"), `${formatParameterValue(focused.FusionAlpha ?? "—", t)} / ${formatParameterValue(focused.GlobalSupport ?? "—", t)}`)}</div></section>`;
}

function replayArtifacts(detail, artifacts, t) {
  const items = Array.isArray(artifacts?.items) ? artifacts.items : [];
  if (!items.length) return `<section class="panel"><p class="eyebrow">${t("history.artifacts")}</p><h2>${t("history.artifactsUnavailable")}</h2><p class="muted">${t("history.artifactsRequired")}</p></section>`;
  return `<section class="panel"><p class="eyebrow">${t("history.artifacts")}</p><h2>${t("history.artifactsComplete")}</h2><div class="table-wrap"><table class="validation-table"><thead><tr><th>${t("history.artifactName")}</th><th>${t("data.size")}</th><th>SHA-256</th><th>${t("history.required")}</th><th><span class="skip">${t("action.export")}</span></th></tr></thead><tbody>${items.map(item => `<tr><td><code>${escapeHtml(item.name)}</code></td><td>${number(item.size_bytes)} ${t("data.bytes")}</td><td><code>${escapeHtml(String(item.sha256 ?? "—").slice(0, 16))}</code></td><td>${item.required ? t("history.required") : "—"}</td><td><button class="button compact" data-artifact-download="${escapeHtml(item.name)}">${t("history.download")}</button></td></tr>`).join("")}</tbody></table></div></section>`;
}

export function replayView(state, task, i18n) {
  const t = i18n.t.bind(i18n); const history = historyState(state); const detail = history.detail?.run_id === history.selectedRunId ? history.detail : null;
  if (!detail) return `<section data-replay-view><header class="page-head"><div><p class="eyebrow">${t("history.eyebrow")}</p><h1>${t("replay.title")}</h1><p>${t("replay.frozenHint")}</p></div><button class="button" data-nav="history">${t("action.backHistory")}</button></header><section class="panel"><div class="empty">${history.detailLoading ? t("history.detailLoading") : history.deepLinkNotice ? t(history.deepLinkNotice) : t("replay.noSelection")}</div></section></section>`;
  const gate = historyArtifactGate(detail, history.artifacts); const replay = history.replay; const agent = replay.agent ?? 1; const summary = task?.runId === detail.run_id ? workspaceSummaryForRun({ ...task, detail }) : null;
  const replayError = replay.error ? errorPanel(replay.error, t) : "";
  const points = replayPoints(replay.data);
  const chartSummary = replayChartSummary(summary, replay.data);
  const alarms = replay.alarms?.items ?? replay.alarms?.alarms ?? [];
  const replayReady = gate.ready && !replay.loading && !replayError && Boolean(chartSummary) && points.length > 0;
  const content = !gate.ready ? `<section class="panel"><div class="callout error">${t("history.artifactsRequired")}</div></section>` : replay.loading ? `<section class="panel"><div class="empty">${t("replay.loading")}</div></section>` : replayError || !chartSummary || !points.length ? `<section class="panel">${replayError || `<div class="empty">${t("replay.unavailable")}</div>`}</section>` : `${renderSelectedAgentResults(summary, t)}${chartPanel(chartSummary, state, t)}<div class="grid-2">${replayDiagnostic(state, t)}<section class="panel"><p class="eyebrow">${t("replay.alarms")}</p><h2>${t("replay.alarmSummary")}</h2>${alarms.length ? `<div class="event-list">${alarms.map(alarm => { const type = alarmTypePresentation(alarm.alarm_type ?? alarm.type, t); return `<div class="event"><time>${escapeHtml(alarm.Time ?? alarm.time ?? "—")}</time><span>${escapeHtml(type.label)}${type.token ? ` <code>${escapeHtml(type.token)}</code>` : ""}</span></div>`; }).join("")}</div>` : `<p class="muted">${t("replay.noAlarms")}</p>`}</section></div>`;
  return `<section data-replay-view><header class="page-head"><div><p class="eyebrow">${t("history.eyebrow")}</p><h1>${t("replay.title")}</h1><p>${t("replay.frozenHint")}</p></div><div class="inline-actions"><button class="button" data-nav="history">${t("action.backHistory")}</button><button class="button" data-replay-export ${gate.ready ? "" : "disabled"}>${t("history.export")}</button><button class="button" data-artifact-manifest ${gate.ready ? "" : "disabled"}>${t("history.manifest")}</button></div></header><section class="panel replay-detail"><h2>${escapeHtml(displayProfileText(detail.display_name ?? detail.run_id, t))}</h2><div class="snapshot">${snapshotRow(t("history.run"), detail.run_id, "text", t)}${snapshotRow(t("workspace.dataset"), displayProfileText(detail.dataset?.display_name ?? detail.dataset_snapshot?.display_name, t), "text", t)}${snapshotRow(t("profileSnapshot.name"), frozenProfilePrimaryText(admissionForRun(detail), t), "text", t)}${snapshotRow(t("profileSnapshot.version"), displayProfileVersion(admissionForRun(detail).version, t), "text", t)}${snapshotRow(t("profileSnapshot.hash"), admissionForRun(detail).hash, "hash", t)}</div>${renderFrozenParameterProfileSnapshot(detail, t, true)}</section><section class="panel replay-controls"><div class="agent-tabs" role="tablist" aria-label="${t("aria.agents")}">${[1,2,3].map(nextAgent => `<button data-replay-agent="${nextAgent}" class="${agent === nextAgent ? "active" : ""}" role="tab" aria-selected="${agent === nextAgent}" ${gate.ready ? "" : "disabled"}>${t("workspace.agent", { agent: nextAgent })}</button>`).join("")}</div>${replayTransportControls(state, points, replayReady, t)}<p class="muted">${t("replay.points", { count: points.length || "—" })}</p></section>${content}${replayArtifacts(detail, history.artifacts, t)}<p class="muted">${t("history.rerunHint")}</p></section>`;
}

function replayPayloadPoints(payload) { return payload?.points ?? payload?.items ?? payload?.data?.points ?? []; }

function historyRunFromDetail(detail: any, previous: any = null) {
  return { ...previous, ...detail, dataset: detail?.dataset ?? detail?.dataset_snapshot ?? previous?.dataset, parameter_version: detail?.parameter_version ?? detail?.parameter_snapshot?.version_id ?? previous?.parameter_version, artifact_state: detail?.artifact_state ?? previous?.artifact_state };
}

function upsertHistoryRun(state, detail) {
  if (!isTerminalHistoryRun(detail)) return;
  const history = historyState(state);
  const index = history.items.findIndex(item => item.run_id === detail.run_id);
  const next = historyRunFromDetail(detail, index >= 0 ? history.items[index] : null);
  history.items = index >= 0 ? history.items.map((item, itemIndex) => itemIndex === index ? next : item) : [next, ...history.items];
}

function aggregateFusedRmse(summary) {
  const metrics = summary?.metrics ?? summary?.aggregate_metrics ?? {};
  const value = metrics?.FusedRMSE ?? metrics?.fused_rmse ?? metrics?.fusedRmse ?? metrics?.RMSE ?? summary?.FusedRMSE ?? summary?.fused_rmse;
  return Number.isFinite(Number(value)) ? Number(value) : null;
}

async function loadHistoryAggregateMetrics(api, runs) {
  if (typeof api?.getSummary !== "function") return {};
  const entries = await Promise.all((Array.isArray(runs) ? runs : []).map(async run => {
    try {
      const value = aggregateFusedRmse(await api.getSummary(run.run_id, "aggregate"));
      return value === null ? null : [run.run_id, value];
    } catch { return null; }
  }));
  return Object.fromEntries(entries.filter((entry): entry is [string, number] => Array.isArray(entry)));
}

export async function loadHistory(state, render, append = false, requestedRunId: string | null = null) {
  const history = historyState(state);
  if (history.loading || typeof state.api?.listSimulations !== "function" || (append && (!history.hasMore || !history.nextCursor))) return false;
  const epoch = (history.listEpoch ?? 0) + 1;
  const priorItems = append ? history.items : [];
  state.history = { ...history, items: priorItems, listEpoch: epoch, loading: true, error: null, ...(append ? {} : { nextCursor: null, hasMore: false, total: null }) };
  render({ dynamic: true, source: "history-list" });
  try {
    const search = String(history.query ?? "").trim();
    const response = await state.api.listSimulations({ view: "history", ...(requestedRunId ? { limit: 1, run_id: requestedRunId } : { limit: 100 }), search: search || undefined, status: history.status || undefined, run_mode: history.mode || undefined, cursor: append ? history.nextCursor : undefined });
    if (state.history?.listEpoch !== epoch) return false;
    const pageItems = listPageItems(response).filter(isTerminalHistoryRun);
    const items = append ? [...priorItems, ...pageItems.filter(next => !priorItems.some(previous => previous.run_id === next.run_id))] : pageItems;
    const meta = listPageMeta(response);
    const aggregateMetrics = { ...(append ? history.aggregateMetrics : {}), ...await loadHistoryAggregateMetrics(state.api, pageItems) };
    state.history = { ...state.history, items, aggregateMetrics, loading: false, error: null, ...meta };
    return true;
  } catch (error) {
    if (state.history?.listEpoch === epoch) state.history = { ...state.history, loading: false, error };
    return false;
  } finally {
    if (state.history?.listEpoch === epoch) render({ dynamic: true, source: "history-list" });
  }
}

export async function openHistoryDeepLink(state, taskStore, runId, i18n, render, openReplay = false) {
  const requestedRunId = String(runId ?? "").trim();
  const history = historyState(state);
  const reset = notice => {
    state.history = { ...history, items: [], selectedRunId: null, detail: null, detailLoading: false, detailError: null, deepLinkNotice: notice, artifacts: null, replay: { agent: 1, loading: false, error: null, data: null, results: null, alarms: null } };
    render({ dynamic: true, source: "history-deep-link" });
  };
  if (!/^run_[A-Za-z0-9][A-Za-z0-9_-]{0,255}$/.test(requestedRunId)) { reset("history.deepLinkInvalid"); return false; }
  state.history = { ...history, items: [], selectedRunId: null, detail: null, detailLoading: false, detailError: null, deepLinkNotice: null, artifacts: null, replay: { agent: 1, loading: false, error: null, data: null, results: null, alarms: null } };
  const loaded = await loadHistory(state, render, false, requestedRunId);
  if (!loaded) { state.history = { ...historyState(state), deepLinkNotice: "history.deepLinkLoadFailed" }; render({ dynamic: true, source: "history-deep-link" }); return false; }
  const item = historyState(state).items.find(candidate => candidate.run_id === requestedRunId && isTerminalHistoryRun(candidate));
  if (!item) { state.history = { ...historyState(state), items: [], selectedRunId: null, detail: null, artifacts: null, deepLinkNotice: "history.deepLinkUnavailable" }; render({ dynamic: true, source: "history-deep-link" }); return false; }
  return selectHistoryRun(state, taskStore, requestedRunId, i18n, render, openReplay);
}

async function loadReplayResources(state, taskStore, render, epoch) {
  const history = historyState(state); const runId = history.selectedRunId; const agent = history.replay.agent;
  if (!runId || !history.detail || !historyArtifactGate(history.detail, history.artifacts).ready) return false;
  state.history = { ...history, replay: { ...history.replay, loading: true, error: null, data: null, results: null, alarms: null } };
  render();
  const unavailable = name => Promise.reject(new ApiError("RESULT_NOT_READY", null, { message: `${name} is unavailable.` }));
  const [replay, results, alarms] = await Promise.allSettled([
    state.api.getReplay ? state.api.getReplay(runId, { agent, limit: 500 }) : unavailable("Replay"),
    state.api.getResults ? state.api.getResults(runId, { agent, limit: 500 }) : unavailable("Results"),
    state.api.getAlarms ? state.api.getAlarms(runId, { agent, limit: 100 }) : unavailable("Alarms")
  ]);
  if (state.history?.selectionEpoch !== epoch || state.history?.selectedRunId !== runId || state.history?.replay?.agent !== agent) return false;
  const primaryError = replay.status === "rejected" ? replay.reason : results.status === "rejected" ? results.reason : null;
  state.history = { ...state.history, replay: { ...state.history.replay, loading: false, error: primaryError, data: replay.status === "fulfilled" ? replay.value : null, results: results.status === "fulfilled" ? results.value : null, alarms: alarms.status === "fulfilled" ? alarms.value : null } };
  render();
  return !primaryError;
}

export async function selectHistoryRun(state, taskStore, runId, i18n, render, openReplay = false, openWorkspace = false) {
  const history = historyState(state); const item = history.items.find(candidate => candidate.run_id === runId) ?? null;
  if (!item || !isTerminalHistoryRun(item)) return false;
  stopReplayPlayback(state);
  const epoch = (history.selectionEpoch ?? 0) + 1;
  state.history = { ...history, selectionEpoch: epoch, selectedRunId: runId, detail: null, detailLoading: true, detailError: null, artifacts: null, replay: { agent: 1, loading: false, error: null, data: null, results: null, alarms: null } };
  state.replayPlayback = { ...replayPlaybackState(state), playing: false, position: 0, timer: null };
  state.chart = { ...state.chart, zoom: 1, pan: 0, focus: null };
  render();
  try {
    await taskStore.selectRun(runId);
    if (state.history?.selectionEpoch !== epoch || taskStore.state.runId !== runId) return false;
    const detail = taskStore.state.detail;
    if (!detail || detail.run_id !== runId || !isTerminalHistoryRun(detail)) throw taskStore.state.error ?? new ApiError("RESULT_NOT_READY", null);
    const artifacts = taskStore.state.artifactsRunId === runId ? taskStore.state.artifacts : state.api.getArtifacts ? await state.api.getArtifacts(runId) : null;
    if (state.history?.selectionEpoch !== epoch) return false;
    const resultReady = taskStore.state.summaryRunId === runId && taskStore.state.resultsRunId === runId && historyArtifactGate(detail, artifacts).ready;
    if (!resultReady) throw taskStore.state.error ?? taskStore.state.resultsError ?? taskStore.state.artifactsError ?? new ApiError("RESULT_NOT_READY", null);
    state.history = { ...state.history, detail, detailLoading: false, detailError: null, artifacts };
    upsertHistoryRun(state, detail);
    const gate = historyArtifactGate(detail, artifacts);
    if (gate.ready) await loadReplayResources(state, taskStore, render, epoch);
    if (openReplay && state.history?.selectionEpoch === epoch) state.view = "replay";
    else if (openWorkspace && state.history?.selectionEpoch === epoch) state.view = "workspace";
    return true;
  } catch (error) {
    if (state.history?.selectionEpoch === epoch) state.history = { ...state.history, detailLoading: false, detailError: error };
    return false;
  } finally {
    if (state.history?.selectionEpoch === epoch) render();
  }
}

async function selectReplayAgent(state, taskStore, agent, render) {
  const history = historyState(state);
  if (![1, 2, 3].includes(agent) || !history.detail || history.replay.loading) return;
  stopReplayPlayback(state);
  const epoch = history.selectionEpoch;
  state.replayPlayback = { ...replayPlaybackState(state), playing: false, position: 0, timer: null };
  state.chart = { ...state.chart, zoom: 1, pan: 0, focus: null };
  state.history = { ...history, replay: { ...history.replay, agent } };
  await taskStore.selectAgent(agent);
  if (state.history?.selectionEpoch === epoch && taskStore.state.runId === history.selectedRunId) await loadReplayResources(state, taskStore, render, epoch);
}

function downloadUrl(url) {
  if (!url || typeof document === "undefined") return;
  const link = document.createElement("a"); link.href = url; link.download = ""; link.rel = "noopener"; link.click();
}

function downloadReplayExport(state) {
  const history = historyState(state); const detail = history.detail;
  if (!detail || !historyArtifactGate(detail, history.artifacts).ready) return;
  downloadUrl(state.api.replayExportUrl?.(detail.run_id, history.replay.agent));
}

function downloadManifest(state) {
  const history = historyState(state); const detail = history.detail;
  if (!detail || !historyArtifactGate(detail, history.artifacts).ready) return;
  const manifest = (history.artifacts?.items ?? []).find(item => item?.name === "artifact_manifest.json");
  if (manifest) downloadUrl(state.api.artifactDownloadUrl?.(detail.run_id, manifest.name));
}

export function renderHashValue(value, className = "hash-value", t: ((key: string, variables?: any) => string) | null = null): string {
  const text = String(value ?? "—");
  const escaped = escapeHtml(text);
  const label = t ? t("aria.copyHash", { value: text }) : `Copy full hash: ${text}`;
  return `<button type="button" class="${className}" data-hash-value data-copy-hash="${escaped}" title="${escaped}" aria-label="${escapeHtml(label)}">${escaped}</button>`;
}

function snapshotRow(label, value, kind: "text" | "hash" = "text", t: ((key: string, variables?: any) => string) | null = null) {
  if (kind === "hash") return `<div><span>${label}</span><strong class="hash-presentation">${renderHashValue(value, "hash-value", t)}</strong></div>`;
  return `<div><span>${label}</span><strong title="${value ?? ""}">${value ?? "—"}</strong></div>`;
}

function parameterLeafReadout(value: any, groupKey: string, t): string {
  return flattenParameterLeaves(value).map(([key, leaf]) => `<div><span class="readout-meta"><span class="readout-label">${escapeHtml(parameterLeafLabel(key, t))}</span><code class="readout-path">${escapeHtml(groupKey ? `${groupKey}.${key}` : key)}</code></span><strong>${escapeHtml(formatParameterValue(leaf, t))}</strong></div>`).join("");
}

function renderParameterGroup(groupKey: string, values: any, t, options: { open?: boolean; fields?: boolean } = {}): string {
  const label = parameterGroupLabel(groupKey, t);
  const id = `parameter-group-${groupKey}`;
  if (options.fields) {
    return `<section id="${escapeHtml(id)}" class="parameter-group"><div class="parameter-group-heading"><h3>${escapeHtml(label)}</h3><code>${escapeHtml(groupKey)}</code></div><div class="field-grid">${flattenParameterLeaves(values).map(([key, leaf]) => {
      const rendered = escapeHtml(formatParameterValue(leaf, t));
      return `<label class="field"><span class="field-label">${escapeHtml(parameterLeafLabel(key, t))}</span><code class="field-path">${escapeHtml(`${groupKey}.${key}`)}</code><span class="field-control"><input value="${rendered}" title="${rendered}" disabled></span></label>`;
    }).join("")}</div></section>`;
  }
  return `<details class="parameter-group"${options.open ? " open" : ""}><summary><span>${escapeHtml(label)} <code>${escapeHtml(groupKey)}</code></span><span>⌄</span></summary><div class="readout">${parameterLeafReadout(values, groupKey, t)}</div></details>`;
}

export function renderParameterReadout(profile, t) {
  if (!profile) return `<div class="empty">${t("state.loading")}</div>`;
  const groups = parameterGroups(profile);
  const groupedReadout = groups.length ? groups.map(([groupKey, values], index) => renderParameterGroup(groupKey, values, t, { open: index === 0 })).join("") : `<div class="empty">${t("state.empty")}</div>`;
  return `<div class="parameter-readout">${groupedReadout}<details class="parameter-group"><summary><span>${t("config.fixed")}</span><span>⌄</span></summary><div class="readout">${parameterLeafReadout(profile.fixed_items ?? {}, "fixed", t)}</div></details></div>`;
}

function mergeParameterValues(shared: any, overrides: any): any {
  const merged = cloneJson(shared ?? {});
  Object.entries(overrides ?? {}).forEach(([key, value]) => {
    if (isRecord(value)) merged[key] = mergeParameterValues(merged[key], value);
    else merged[key] = cloneJson(value);
  });
  return merged;
}

function frozenParameterProfile(detail: any) {
  const snapshot = detail?.snapshot ?? {};
  const candidate = detail?.parameter_snapshot ?? snapshot.parameter_snapshot ?? snapshot.parameter_profile ?? null;
  const directParameter = versionReference(detail?.parameter_version ?? detail?.parameter_profile_version_id);
  const versionId = candidate?.version_id ?? snapshot.parameter_profile_version_id ?? directParameter.versionId ?? null;
  if (candidate && isRecord(candidate.shared_parameters) && Array.isArray(candidate.agents)) {
    return { ...candidate, version_id: versionId, normalized_sha256: candidate.sha256 ?? candidate.normalized_sha256 ?? snapshot.parameter_profile_sha256 ?? directParameter.hash ?? detail?.snapshot_sha256 ?? snapshot.sha256 ?? null };
  }
  return null;
}

export function renderFrozenParameterProfileSnapshot(detail, t, open = false) {
  const snapshot = detail?.snapshot ?? {};
  const profile = frozenParameterProfile(detail);
  const directParameter = versionReference(detail?.parameter_version ?? detail?.parameter_profile_version_id);
  const versionId = profile?.version_id ?? snapshot.parameter_profile_version_id ?? directParameter.versionId ?? "—";
  const hash = profile?.normalized_sha256 ?? profile?.sha256 ?? snapshot.parameter_profile_sha256 ?? directParameter.hash ?? detail?.snapshot_sha256 ?? "—";
  const name = frozenProfilePrimaryText({ mode: profile?.mode ?? detail?.run_mode, display_name: profile?.display_name ?? snapshot.parameter_profile_display_name ?? detail?.parameter_profile_display_name }, t);
  const shared = profile?.shared_parameters ?? null;
  const agents = Array.isArray(profile?.agents) ? profile.agents : Array.isArray(snapshot.agents) ? snapshot.agents : [];
  const body = shared ? `<div class="parameter-snapshot-section"><h3>${t("profileSnapshot.shared")}</h3>${renderParameterReadout({ shared_parameters: shared, fixed_items: profile?.fixed_items ?? {} }, t)}</div>${agents.map(agent => {
    const overrides = agent?.parameters ?? {};
    const effective = mergeParameterValues(shared, overrides);
    return `<details class="parameter-snapshot-agent"><summary><span>${t("workspace.agent", { agent: agent?.agent ?? "—" })} · ${displayAgentSegment(agent?.segment, t)}</span><span>⌄</span></summary><div class="parameter-snapshot-agent-content"><div><h4>${t("profileSnapshot.overrides")}</h4>${Object.keys(overrides).length ? `<div class="readout">${parameterLeafReadout(overrides, `agents.${agent.agent}.parameters`, t)}</div>` : `<p class="muted">${t("profileSnapshot.noOverrides")}</p>`}</div><div><h4>${t("profileSnapshot.effective")}</h4><div class="parameter-readout">${parameterGroups({ shared_parameters: effective }).map(([groupKey, values], index) => renderParameterGroup(groupKey, values, t, { open: index === 0 })).join("")}</div></div></div></details>`;
  }).join("")}` : `<p class="muted">${t("profileSnapshot.unavailable")}</p>`;
  return `<details class="parameter-snapshot" data-parameter-profile-snapshot ${open ? "open" : ""}><summary><span>${t("profileSnapshot.title")}</span><span>${t("workspace.frozen")}</span></summary><div class="parameter-snapshot-meta">${snapshotRow(t("profileSnapshot.name"), name, "text", t)}${snapshotRow(t("profileSnapshot.version"), displayProfileVersion(versionId, t), "text", t)}${snapshotRow(t("profileSnapshot.hash"), hash, "hash", t)}</div><p class="muted">${t("profileSnapshot.frozenHint")}</p>${body}</details>`;
}

export function renderProfileParameterFields(profile, t) {
  const groups = parameterGroups(profile);
  if (!groups.length) return `<div class="empty">${t("state.empty")}</div>`;
  return `<div class="parameter-groups">${groups.map(([groupKey, values]) => renderParameterGroup(groupKey, values, t, { fields: true })).join("")}</div>`;
}

function renderDraftControl(definition: any, draft: any, scope: string, agent: number | null, t): string {
  const sharedValue = readPath(draft.shared_parameters, definition.parts);
  const agentParameters = agent ? draft.agents.find(item => item.agent === agent)?.parameters ?? {} : null;
  const hasOverride = agentParameters ? hasPath(agentParameters, definition.parts) : true;
  const value = agentParameters ? readPath(agentParameters, definition.parts) : sharedValue;
  const normalized = normalizeConstraint(definition, sharedValue);
  const data = `data-draft-input data-draft-scope="${scope}" data-draft-path="${escapeHtml(definition.relative)}" data-value-type="${escapeHtml(normalized.type)}"${agent ? ` data-agent="${agent}"` : ""}`;
  const rawKey = agent ? `agents[${agent}].parameters.${definition.relative}` : `shared_parameters.${definition.relative}`;
  const title = escapeHtml(formatParameterValue(value, t));
  const inherited = agent ? `<small class="field-hint">${t("config.inherit", { value: formatParameterValue(sharedValue, t) })}</small>` : "";
  let control = "";
  if (normalized.type === "boolean" && !agent) {
    control = `<input type="checkbox" ${data} ${value ? "checked" : ""}>`;
  } else if (normalized.type === "boolean") {
    control = `<select ${data}><option value="__inherit__" ${!hasOverride ? "selected" : ""}>${t("config.inheritOption")}</option><option value="true" ${value === true ? "selected" : ""}>true</option><option value="false" ${value === false ? "selected" : ""}>false</option></select>`;
  } else if (normalized.allowedValues) {
    const inheritedOption = agent ? `<option value="__inherit__" ${!hasOverride ? "selected" : ""}>${t("config.inheritOption")}</option>` : "";
    control = `<select ${data}>${inheritedOption}${normalized.allowedValues.map(item => `<option value="${escapeHtml(item)}" ${String(value) === String(item) ? "selected" : ""}>${escapeHtml(item)}</option>`).join("")}</select>`;
  } else {
    const numeric = normalized.type === "integer" || normalized.type === "number";
    const minimum = typeof normalized.min === "number" ? ` min="${normalized.min}"` : "";
    const maximum = typeof normalized.max === "number" ? ` max="${normalized.max}"` : "";
    const step = normalized.type === "integer" ? " step=\"1\"" : normalized.type === "number" ? " step=\"any\"" : "";
    control = `<input type="${numeric ? "number" : "text"}" ${data} value="${hasOverride ? title : ""}"${minimum}${maximum}${step}${agent && !hasOverride ? ` placeholder="${escapeHtml(t("config.inheritPlaceholder", { value: formatParameterValue(sharedValue, t) }))}"` : ""}>`;
  }
  return `<label class="field editable-field"><span class="field-label">${escapeHtml(parameterLeafLabel(definition.parts[definition.parts.length - 1], t))}</span><code class="field-path">${escapeHtml(rawKey)}</code>${inherited ? `<span class="field-helper">${inherited}</span>` : ""}<span class="field-control">${control}</span></label>`;
}

function renderLockedDraftControl(groupKey: string, key: string, value: any, t): string {
  const technicalPath = `${groupKey}.${key}`;
  const rendered = escapeHtml(formatParameterValue(value, t));
  const control = typeof value === "boolean" ? `<input type="checkbox" ${value ? "checked" : ""} disabled>` : `<input value="${rendered}" title="${rendered}" disabled>`;
  return `<label class="field locked-draft-field"><span class="field-label">${escapeHtml(parameterLeafLabel(key, t))}</span><code class="field-path">${escapeHtml(technicalPath)}</code><span class="field-helper"><small class="field-hint">${t("config.s1FixedPath")}</small></span><span class="field-control">${control}</span></label>`;
}

export function renderDraftParameterFields(draft, scope, t) {
  const definitions = profileEditableDefinitions(draft?.baseProfile);
  if (!definitions.length) return `<div class="empty">${t("config.noEditablePaths")}</div>`;
  const selectedAgent = scope.startsWith("agent-") ? Number(scope.slice("agent-".length)) : null;
  const definitionByPath = new Map(definitions.map(definition => [definition.relative, definition]));
  const groups = parameterGroups(draft?.baseProfile).map(([groupKey, values]) => {
    const fields = flattenParameterLeaves(values).map(([key, value]) => {
      const relative = `${groupKey}.${key}`;
      const definition = definitionByPath.get(relative);
      if (definition) return renderDraftControl(definition, draft, selectedAgent ? "agent" : "shared", selectedAgent, t);
      if (S1_FIXED_PARAMETER_PATHS.has(relative)) return renderLockedDraftControl(groupKey, key, value, t);
      return "";
    }).join("");
    return fields ? [groupKey, fields] as [string, string] : null;
  }).filter(Boolean) as [string, string][];
  return `<div class="parameter-groups">${groups.map(([groupKey, fields]) => `<section id="parameter-group-${escapeHtml(groupKey)}" class="parameter-group"><div class="parameter-group-heading"><h3>${escapeHtml(parameterGroupLabel(groupKey, t))}</h3><code>${escapeHtml(groupKey)}</code></div><div class="field-grid">${fields}</div></section>`).join("")}</div>`;
}

export function renderSelectedAgentResults(summary, t) {
  if (!summary) return `<section class="panel"><div class="empty">${t("workspace.empty")}</div></section>`;
  const agent = summary.selection.agent;
  const metricMap = [["rmse", "RMSE", ""], ["mae", "MAE", "A"], ["r2", "R2", ""], ["coverage", "Coverage", "%"], ["bandwidth", "MeanBandwidth", "A"], ["weight", "MeanOnlineGlobalWeight", ""], ["negative", "NegativeTransferRate", "%"]];
  return `<section class="result-context"><p class="eyebrow">${t("workspace.metrics")}</p><h2>${t("workspace.resultTitle", { agent })}</h2><span class="muted">${t("workspace.agent", { agent })} · ${displayAgentSegment(summary.selection.segment, t)}</span></section><section class="metric-grid" aria-label="${t("workspace.metrics")}">${metricMap.map(([label,key,suffix]) => { const raw = summary.metrics[key]; const value = suffix === "%" && raw !== null ? `${number(raw * 100)}%` : `${number(raw)}${suffix ? ` ${suffix}` : ""}`; return `<article class="metric"><span>${t(`metric.${label}`)}</span><strong>${value}</strong><small>${t("workspace.agent", { agent })}</small></article>`; }).join("")}</section>`;
}

function chartPanel(summary, state, t) {
  if (!summary) return "";
  const legend = [["truth", "legend.truth"], ["local", "legend.local"], ["global", "legend.global"], ["fused", "legend.fused"], ["interval", "legend.interval"]];
  return `<section class="panel"><div class="panel-heading"><div><p class="eyebrow">${t("workspace.metrics")}</p><h2>${t("workspace.chart")}</h2></div><div class="chart-actions"><button class="button ghost compact" data-zoom="1.3">＋ ${t("action.zoomIn")}</button><button class="button ghost compact" data-zoom=".77">－ ${t("action.zoomOut")}</button><button class="button ghost compact" data-reset>${t("action.reset")}</button></div></div><div class="legend" role="group" aria-label="${t("aria.series")}">${legend.map(([name, label]) => `<label><input type="checkbox" data-series="${name}" ${state.chart.series[name] ? "checked" : ""}><i class="series-dot ${name}" aria-hidden="true"></i><span>${t(label)}</span></label>`).join("")}<span class="muted">${t("workspace.interaction")}</span></div><div class="chart-wrap"><canvas data-chart role="img" tabindex="0" aria-describedby="chart-keyboard-help" aria-label="${t("aria.chart")}"></canvas><span id="chart-keyboard-help" class="skip">${t("aria.chartKeyboard")}</span><div class="tooltip" data-tooltip hidden></div></div><div class="chart-footer"><span>${t("chart.originalPoints", { count: number(summary.chart.original_point_count) })}</span><span>${t("chart.displayPoints", { count: number(summary.chart.display_point_count) })} · ${summary.chart.sampling_method}</span><span>${summary.artifact_integrity.status} · ${summary.artifact_integrity.manifest_sha256.slice(0, 12)}…</span></div></section>`;
}

function resourceItems(resource) {
  return Array.isArray(resource?.items) ? resource.items : Array.isArray(resource?.data) ? resource.data : Array.isArray(resource?.points) ? resource.points : [];
}

function diagnosticPointValue(point, keys: string[]) {
  return keys.map(key => Number(point?.[key])).find(Number.isFinite);
}

function diagnosticValues(points, keys: string[]) {
  return (Array.isArray(points) ? points : []).map(point => diagnosticPointValue(point, keys)).filter((value): value is number => Number.isFinite(value));
}

function diagnosticPointSequence(points) {
  return [...(Array.isArray(points) ? points : [])].sort((left, right) => Number(left?.OriginalRunningIndex ?? left?.original_running_index ?? 0) - Number(right?.OriginalRunningIndex ?? right?.original_running_index ?? 0));
}

export function workspaceDiagnosticSeries(summary, results, kind: "fusion" | "recent") {
  const summaryPoints = diagnosticPointSequence(summary?.chart?.points);
  const resultPoints = diagnosticPointSequence(resourceItems(results));
  const definitions = kind === "fusion"
    ? [["diagnostic.fusionAlpha", ["FusionAlpha", "fusion_alpha"]], ["diagnostic.globalSupport", ["GlobalSupport", "global_support"]]]
    : [["diagnostic.localRmse", ["RecentLocalRMSE", "LocalRMSE", "local_rmse"]], ["diagnostic.globalRmse", ["RecentGlobalRMSE", "GlobalRMSE", "global_rmse"]], ["diagnostic.intervalHalfWidth", ["FusedHalfWidth", "IntervalHalfWidth", "interval_half_width"]]];
  const sourcePoints = kind === "fusion" ? (summaryPoints.length ? summaryPoints : resultPoints) : resultPoints;
  const configuredWindow = Number(results?.window_size ?? results?.window ?? summary?.diagnostic_summary?.error_window ?? 0);
  const points = kind === "recent" && configuredWindow > 0 ? sourcePoints.slice(-configuredWindow) : sourcePoints;
  return {
    source: points.length ? (kind === "fusion" && summaryPoints.length ? "summary" : "results") : "none",
    window: kind === "recent" ? (configuredWindow || points.length) : null,
    series: definitions.map(([labelKey, keys]) => ({ labelKey, keys: keys as string[], values: diagnosticValues(points, keys as string[]) }))
  };
}

function diagnosticPointLocator(point) {
  const value = point?.OriginalRunningIndex ?? point?.original_running_index ?? point?.result_locator?.original_running_index ?? point?.result_locator?.point;
  return value === null || value === undefined || value === "" ? null : String(value);
}

// Prefer the loaded result page, then the same frozen summary point retained for chart or alarm locators.
export function workspaceDiagnosticSelection(summary, results, alarms, state, runId = "") {
  const summaryPoints = diagnosticPointSequence(summary?.chart?.points);
  const resultPoints = diagnosticPointSequence(resourceItems(results));
  const alarmRequested = state?.workspaceAlarmDialog?.runId === runId;
  const selectedAlarm = alarmRequested ? resourceItems(alarms)[Number(state.workspaceAlarmDialog.index)] : null;
  const focus = state?.chart?.focus;
  const focusRequested = Number.isInteger(focus);
  const chartPoint = alarmRequested ? null : focusRequested ? summary?.chart?.points?.[focus] : null;
  const locator = diagnosticPointLocator(selectedAlarm ?? chartPoint);
  if (locator === null) return { requested: alarmRequested || focusRequested, locator: null, point: null };
  const point = resultPoints.find(candidate => diagnosticPointLocator(candidate) === locator)
    ?? summaryPoints.find(candidate => diagnosticPointLocator(candidate) === locator)
    ?? null;
  return { requested: true, locator, point };
}

function diagnosticValueText(series, selection) {
  if (selection.requested) {
    const value = selection.point ? diagnosticPointValue(selection.point, series.keys) : null;
    return Number.isFinite(value) ? Number(value).toFixed(2) : "—";
  }
  const latest = series.values[series.values.length - 1];
  return Number.isFinite(latest) ? Number(latest).toFixed(2) : "—";
}

function diagnosticMetadata(data, selection, t, kind: "fusion" | "recent") {
  if (selection.requested) return selection.point ? t("diagnostic.selectedPoint", { point: selection.locator }) : t("diagnostic.selectedPointUnavailable");
  return kind === "fusion" ? t("diagnostic.pointSynchronized") : t("diagnostic.window", { count: data.window ?? 0 });
}

function diagnosticLegendMarkup(data, selection, t, kind: "fusion" | "recent") {
  const legendClasses = kind === "fusion" ? ["fused", "global"] : ["local", "global", "interval"];
  return data.series.map((series, index) => `<span><i class="series-dot ${legendClasses[index] ?? "truth"}" aria-hidden="true"></i>${t(series.labelKey)}: <strong data-diagnostic-value="${kind}-${index}">${diagnosticValueText(series, selection)}</strong></span>`).join("");
}

function patchWorkspaceDiagnosticSelection(root, summary, results, alarms, state, t, runId) {
  const selection = workspaceDiagnosticSelection(summary, results, alarms, state, runId);
  (["fusion", "recent"] as const).forEach(kind => {
    const data = workspaceDiagnosticSeries(summary, results, kind);
    const panel = root.querySelector(`[data-diagnostic-panel="${kind}"]`);
    const metadata = root.querySelector(`[data-diagnostic-metadata="${kind}"]`);
    const legend = root.querySelector(`[data-diagnostic-legend="${kind}"]`);
    if (panel) panel.setAttribute("data-diagnostic-selected", selection.requested ? selection.locator ?? "unavailable" : "");
    if (metadata) metadata.textContent = diagnosticMetadata(data, selection, t, kind);
    if (legend) legend.innerHTML = diagnosticLegendMarkup(data, selection, t, kind);
  });
}

function diagnosticSeriesPanel(summary, results, alarms, state, runId, t, kind: "fusion" | "recent") {
  const data = workspaceDiagnosticSeries(summary, results, kind);
  const selection = workspaceDiagnosticSelection(summary, results, alarms, state, runId);
  const available = selection.requested || data.series.some(series => series.values.length > 1);
  const eyebrow = t(kind === "fusion" ? "diagnostic.fusionEyebrow" : "diagnostic.recentEyebrow");
  const title = t(kind === "fusion" ? "diagnostic.fusionTitle" : "diagnostic.recentTitle");
  const metadata = diagnosticMetadata(data, selection, t, kind);
  return `<section class="panel diagnostic-series" data-diagnostic-panel="${kind}" data-diagnostic-source="${data.source}" data-diagnostic-selected="${escapeHtml(selection.requested ? selection.locator ?? "unavailable" : "")}"><header class="panel-heading"><div><p class="eyebrow">${eyebrow}</p><h2>${title}</h2></div><span class="muted" data-diagnostic-metadata="${kind}">${escapeHtml(metadata)}</span></header>${available ? `<div class="diagnostic-legend" data-diagnostic-legend="${kind}" role="group" aria-label="${t("aria.diagnosticSeries")}">${diagnosticLegendMarkup(data, selection, t, kind)}</div><canvas class="mini-chart" data-diagnostic-chart="${kind}" role="img" aria-label="${escapeHtml(title)}"></canvas>` : `<div class="empty">${t("diagnostic.noSeries")}</div>`}</section>`;
}

function alarmLevelLabel(value, t) {
  const normalized = String(value ?? "").toLowerCase();
  if (normalized.includes("notice")) return t("alarm.notice");
  if (normalized.includes("warning")) return t("alarm.warning");
  if (normalized.includes("alarm")) return t("alarm.alarm");
  if (normalized === "none" || normalized === "normal") return t("alarm.none");
  return t("alarm.unknown");
}

function alarmTypeLabel(value, t) {
  const normalized = String(value ?? "").trim().toUpperCase().replace(/[^A-Z0-9]+/g, "_");
  const keys: Record<string, string> = {
    LOAD_IMBALANCE: "alarm.type.loadImbalance", HEAVY_LOAD: "alarm.type.heavyLoad", LIGHT_LOAD: "alarm.type.lightLoad",
    ABOVE_FUSED_UPPER_BOUND: "alarm.type.upperBound", LIGHT_LOAD_PERSISTED: "alarm.type.lightLoad", OVERALL: "alarm.type.overall"
  };
  return keys[normalized] ? t(keys[normalized]) : t("alarm.unknownType");
}

function alarmTypePresentation(value, t) {
  const token = String(value ?? "").trim().toUpperCase().replace(/[^A-Z0-9]+/g, "_");
  return { label: alarmTypeLabel(token, t), token: token || "" };
}

function alarmScalar(value) {
  if (value === null || value === undefined || isRecord(value) || Array.isArray(value)) return "—";
  const raw = String(value).trim();
  return raw && !/(?:[A-Za-z]:[\\/]|(?:^|\s)\/|\b(?:select|insert|update|delete|lease[_ -]?token)\b)/i.test(raw) ? raw : "—";
}

function alarmTimeValue(alarm) {
  const raw = alarmScalar(alarm?.Time ?? alarm?.time ?? alarm?.timestamp);
  if (raw === "—" || /(?:Z|[+-]\d{2}:\d{2})$/i.test(raw)) return raw;
  const timestamp = Date.parse(raw);
  if (!Number.isFinite(timestamp)) return raw;
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "medium", timeZoneName: "shortOffset" }).format(new Date(timestamp));
}

function alarmLocator(alarm, t) {
  const locator = isRecord(alarm?.result_locator) ? alarm.result_locator : {};
  const agent = alarmScalar(alarm?.Agent ?? alarm?.agent ?? locator?.agent);
  const point = alarmScalar(alarm?.OriginalRunningIndex ?? alarm?.original_running_index ?? locator?.original_running_index ?? locator?.point);
  return { agent, point, label: t("diagnostic.pointLocator", { agent, point }) };
}

function alarmReasons(alarm) {
  const values = alarm?.reasons ?? alarm?.reason_codes ?? alarm?.reason ?? [];
  return (Array.isArray(values) ? values : [values]).map(alarmScalar).filter(value => value !== "—");
}

function loadStatusDistribution(summary, t) {
  const labels = ["normal", "light", "heavy"];
  const authority = summary?.load_status_counts ?? summary?.load_status_summary ?? summary?.diagnostic_summary?.load_status_counts;
  const counts = { normal: 0, light: 0, heavy: 0 };
  let source = "";
  if (isRecord(authority)) {
    Object.entries(authority).forEach(([key, value]) => {
      const normalized = key.toLowerCase();
      const count = Number(value);
      if (Number.isFinite(count) && labels.includes(normalized)) counts[normalized] += count;
    });
    if (labels.some(key => counts[key] > 0)) source = "authoritative";
  }
  if (!source) {
    (Array.isArray(summary?.chart?.points) ? summary.chart.points : []).forEach(point => {
      const status = String(point?.LoadStatus ?? point?.load_status ?? "").toLowerCase();
      const key = labels.find(label => status.includes(label));
      if (key) counts[key] += 1;
    });
    if (labels.some(key => counts[key] > 0)) source = "displayed";
  }
  const total = labels.reduce((sum, key) => sum + counts[key], 0);
  if (!total) return `<p class="muted load-status-unavailable" data-load-status-unavailable>${t("diagnostic.loadDistributionUnavailable")}</p>`;
  const scope = source === "displayed" ? t("diagnostic.loadDistributionDisplayed", { count: total }) : t("diagnostic.loadDistributionAuthoritative");
  const items = labels.map(key => ({ key, count: counts[key], percent: counts[key] / total * 100 }));
  return `<section class="state-distribution" data-load-status-distribution data-load-status-scope="${source}" aria-label="${escapeHtml(scope)}"><p class="muted">${escapeHtml(scope)}</p><div class="state-bar" aria-hidden="true">${items.map(item => `<i class="${item.key}" style="width:${item.percent.toFixed(2)}%"></i>`).join("")}</div><div class="state-distribution-legend">${items.map(item => `<span><b class="state-key ${item.key}"></b>${t(`load.${item.key}`)} ${item.count} (${number(item.percent)}%)</span>`).join("")}</div></section>`;
}

export function renderAlarmDetailDialog(alarm, index, runId, t) {
  const locator = alarmLocator(alarm, t);
  const reasons = alarmReasons(alarm);
  const type = alarmTypePresentation(alarm?.alarm_type ?? alarm?.type, t);
  const titleId = `alarm-detail-title-${index}`;
  const descriptionId = `alarm-detail-description-${index}`;
  return `<dialog id="alarm-detail-dialog" class="alarm-dialog" data-alarm-dialog data-alarm-index="${escapeHtml(String(index))}" data-alarm-run-id="${escapeHtml(runId)}" data-open="true" aria-labelledby="${titleId}" aria-describedby="${descriptionId}" tabindex="-1"><header class="dialog-head"><div><p class="eyebrow">${t("diagnostic.alarmsEyebrow")}</p><h2 id="${titleId}">${t("diagnostic.alarmDetails")}</h2></div><button type="button" class="button compact" data-alarm-dialog-close aria-label="${escapeHtml(t("action.close"))}">${t("action.close")}</button></header><div id="${descriptionId}" class="alarm-dialog-content"><dl class="alarm-dialog-details"><div><dt>${t("diagnostic.alarmTime")}</dt><dd>${escapeHtml(alarmTimeValue(alarm))}</dd></div><div><dt>${t("diagnostic.alarmLevel")}</dt><dd>${escapeHtml(alarmLevelLabel(alarm?.OverallAlarmLevel ?? alarm?.overall_alarm_level, t))}</dd></div><div><dt>${t("diagnostic.alarmType")}</dt><dd>${escapeHtml(type.label)}${type.token ? ` <code>${escapeHtml(type.token)}</code>` : ""}</dd></div><div><dt>${t("diagnostic.alarmAgent")}</dt><dd>${escapeHtml(locator.agent)}</dd></div><div><dt>${t("diagnostic.originalRunningIndex")}</dt><dd>${escapeHtml(locator.point)}</dd></div><div><dt>${t("diagnostic.pointLocatorLabel")}</dt><dd>${escapeHtml(locator.label)}</dd></div></dl><section><h3>${t("diagnostic.alarmReasons")}</h3>${reasons.length ? `<ul>${reasons.map(reason => `<li>${escapeHtml(reason)}</li>`).join("")}</ul>` : `<p class="muted">${t("diagnostic.alarmNoReasons")}</p>`}</section></div></dialog>`;
}

function alarmTimelinePanel(alarms, error, summary, t, runId, selectedIndex: number | null = null) {
  if (error) return `<section class="panel alarm-timeline"><header class="panel-heading"><div><p class="eyebrow">${t("diagnostic.alarmsEyebrow")}</p><h2>${t("diagnostic.alarmsTitle")}</h2></div></header><div class="empty">${t("diagnostic.alarmsUnavailable")}</div></section>`;
  const items = resourceItems(alarms);
  const total = Number(alarms?.meta?.total ?? alarms?.total);
  const totalLabel = Number.isFinite(total) ? `<span class="muted">${t("diagnostic.alarmsLoaded", { shown: items.length, total })}</span>` : "";
  const body = items.length ? `<div class="alarm-list">${items.map((alarm, index) => {
    const locator = alarmLocator(alarm, t);
    const level = alarmLevelLabel(alarm?.OverallAlarmLevel ?? alarm?.overall_alarm_level, t);
    const type = alarmTypePresentation(alarm?.alarm_type ?? alarm?.type, t);
    return `<button class="alarm-row" type="button" data-alarm-open="${index}" data-alarm-run-id="${escapeHtml(runId)}" aria-haspopup="dialog" aria-controls="alarm-detail-dialog" aria-label="${escapeHtml(`${level} · ${type.label} · ${locator.label}`)}"><span class="state ${statusClass(String(alarm?.OverallAlarmLevel ?? "WARNING").toUpperCase())}">${escapeHtml(level)}</span><strong>${escapeHtml(type.label)}${type.token ? ` <code>${escapeHtml(type.token)}</code>` : ""}</strong><time>${escapeHtml(alarmTimeValue(alarm))}</time><small>${escapeHtml(locator.label)}</small></button>`;
  }).join("")}</div>` : `<div class="empty">${t("diagnostic.noAlarms")}</div>`;
  const selected = Number.isInteger(selectedIndex) && selectedIndex !== null ? items[selectedIndex] : null;
  const dialog = selected ? renderAlarmDetailDialog(selected, selectedIndex, runId, t) : "";
  return `<section class="panel alarm-timeline" data-alarm-timeline><header class="panel-heading"><div><p class="eyebrow">${t("diagnostic.alarmsEyebrow")}</p><h2>${t("diagnostic.alarmsTitle")}</h2></div>${totalLabel}</header>${loadStatusDistribution(summary, t)}${body}${dialog}</section>`;
}

function traceNumber(value) {
  const numeric = Number(value);
  return Number.isFinite(numeric) ? number(numeric) : "—";
}

function traceEntryValue(entry, keys) {
  if (!isRecord(entry)) return undefined;
  return keys.map(key => entry[key]).find(value => value !== undefined && value !== null);
}

function traceSplitCards(summary, t) {
  const entries = Object.entries(summary?.split_summary ?? {});
  if (!entries.length) return `<div><span>${t("diagnostic.agentSplit", { agent: "—" })}</span><strong>—</strong></div>`;
  return entries.map(([agent, value]) => {
    const label = t("diagnostic.agentSplit", { agent: String(agent).replace(/^agent_?/i, "") || "—" });
    if (!isRecord(value)) return `<div><span>${label}</span><strong>${escapeHtml(alarmScalar(value))}</strong></div>`;
    const train = traceEntryValue(value, ["train", "training", "training_rows", "training_samples", "train_count", "training_count", "Train"]);
    const calibration = traceEntryValue(value, ["calibration", "calibration_rows", "calibration_samples", "calibration_count", "Calibration"]);
    const test = traceEntryValue(value, ["test", "testing", "test_rows", "testing_rows", "testing_samples", "test_count", "testing_count", "Test"]);
    const values = [train, calibration, test];
    const readable = values.every(item => item !== undefined) ? values.map(traceNumber).join(" / ") : "—";
    return `<div><span>${label}</span><strong>${escapeHtml(readable)}</strong>${readable === "—" ? "" : `<small>${t("diagnostic.trainCalibrationTest")}</small>`}</div>`;
  }).join("");
}

function traceAnchorsCard(summary, t) {
  const anchors = summary?.anchor_summary ?? {};
  const publicAnchors = traceEntryValue(anchors, ["public_anchors"]);
  const loo = traceEntryValue(anchors, ["leave_one_out", "loo"]);
  const supportFallbacks = traceEntryValue(anchors, ["support_fallbacks", "support_fallback_count", "fallback_count"]);
  const supplements = [] as string[];
  if (loo !== undefined) supplements.push(t("diagnostic.loo", { value: alarmScalar(loo) }));
  if (supportFallbacks !== undefined) supplements.push(t("diagnostic.supportFallbacks", { count: traceNumber(supportFallbacks) }));
  return `<div><span>${t("diagnostic.anchors")}</span><strong>${escapeHtml(traceNumber(publicAnchors))}</strong>${supplements.length ? `<small>${escapeHtml(supplements.join(" · "))}</small>` : ""}</div>`;
}

export function renderManifestDialog(files, integrity, manifestHash, runId, t) {
  const titleId = "manifest-dialog-title";
  const descriptionId = "manifest-dialog-description";
  return `<dialog id="manifest-dialog" class="alarm-dialog manifest-dialog" data-manifest-dialog data-manifest-run-id="${escapeHtml(runId)}" data-open="true" aria-labelledby="${titleId}" aria-describedby="${descriptionId}" tabindex="-1"><header class="dialog-head"><div><p class="eyebrow">${t("diagnostic.resultFiles")}</p><h2 id="${titleId}">${t("diagnostic.manifestDialogTitle")}</h2></div><button type="button" class="button compact" data-manifest-dialog-close aria-label="${escapeHtml(t("action.close"))}">${t("action.close")}</button></header><div id="${descriptionId}" class="alarm-dialog-content"><p class="muted">${t("diagnostic.manifestDialogDescription", { count: files.length, integrity: String(integrity ?? "—") })}</p>${manifestHash ? `<p>${t("diagnostic.manifest")}: ${renderHashValue(manifestHash, "hash-value hash-value-inline", t)}</p>` : ""}<div class="manifest-file-list">${files.map(file => `<div><code>${escapeHtml(String(file?.name ?? "—"))}</code><span>${number(file?.size_bytes)} ${t("data.bytes")}</span>${renderHashValue(file?.sha256 ?? file?.sha_256 ?? "—", "hash-value hash-value-inline", t)}<span>${file?.required ? t("history.required") : "—"}</span><span>${escapeHtml(String(file?.integrity ?? integrity ?? "—"))}</span></div>`).join("")}</div></div></dialog>`;
}

function traceabilityPanel(summary, artifacts, artifactsError, t, runId, manifestOpen) {
  const integrity = summary?.artifact_integrity?.status ?? "—";
  const preprocessing = summary?.preprocessing ?? {};
  const files = resourceItems(artifacts);
  const manifestHash = artifacts?.manifest_sha256 ?? summary?.artifact_integrity?.manifest_sha256 ?? null;
  const counts = isRecord(preprocessing?.counts) ? preprocessing.counts : {};
  const rawRows = preprocessing?.raw_rows ?? traceEntryValue(counts, ["raw_rows"]);
  const runningRows = preprocessing?.running_rows ?? traceEntryValue(counts, ["running_rows"]);
  const spikeRows = preprocessing?.spike_flags ?? preprocessing?.spike_rows ?? traceEntryValue(counts, ["spike_rows", "spike_flags"]);
  const hasPreprocessingCounts = rawRows !== undefined && runningRows !== undefined;
  const preprocessingContract = preprocessing?.preprocessing_contract_version ?? preprocessing?.contract_version ?? null;
  const preprocessingCard = `<div><span>${t("diagnostic.preprocessing")}</span><strong>${hasPreprocessingCounts ? `${traceNumber(rawRows)} → ${traceNumber(runningRows)}` : "—"}</strong>${spikeRows !== undefined ? `<small>${t("diagnostic.preprocessingSpikes", { count: traceNumber(spikeRows) })}</small>` : preprocessingContract ? `<small>${escapeHtml(String(preprocessingContract))}</small>` : ""}</div>`;
  const fileSummary = artifactsError ? "—" : `${files.length} · ${String(integrity)}`;
  const manifestAction = files.length ? `<button type="button" class="button compact" data-manifest-dialog-open data-manifest-run-id="${escapeHtml(runId)}" aria-haspopup="dialog" aria-controls="manifest-dialog">${t("diagnostic.viewManifest")}</button>` : `<button type="button" class="button compact" disabled title="${escapeHtml(t("diagnostic.filesUnavailable"))}">${t("diagnostic.viewManifest")}</button>`;
  const dialog = manifestOpen && files.length ? renderManifestDialog(files, integrity, manifestHash, runId, t) : "";
  const fileDetails = artifactsError ? `<p class="muted">${t("diagnostic.filesUnavailable")}</p>` : files.length ? `<p class="muted">${t("diagnostic.registeredFiles", { count: files.length })}</p>` : `<p class="muted">${t("diagnostic.filesUnavailable")}</p>`;
  return `<section class="panel trace-panel" data-workspace-traceability><header class="panel-heading"><div><p class="eyebrow">${t("diagnostic.traceEyebrow")}</p><h2>${t("diagnostic.traceTitle")}</h2></div><div class="inline-actions"><span class="state ${["VERIFIED", "COMMITTED"].includes(String(integrity).toUpperCase()) ? "COMPLETED" : "WARNING"}">${escapeHtml(String(integrity))}</span>${manifestAction}</div></header><div class="trace-grid">${preprocessingCard}${traceAnchorsCard(summary, t)}${traceSplitCards(summary, t)}<div><span>${t("diagnostic.resultFiles")}</span><strong>${escapeHtml(fileSummary)}</strong>${manifestHash ? `<small>${t("diagnostic.manifest")}: ${renderHashValue(manifestHash, "hash-value hash-value-inline", t)}</small>` : ""}</div><div><span>${t("diagnostic.summaryHash")}</span><strong>${renderHashValue(preprocessing?.summary_sha256 ?? "—", "hash-value hash-value-inline", t)}</strong></div></div>${fileDetails}${dialog}</section>`;
}

function diagnosticPanel(summary, results, alarms, artifacts, task, state, t) {
  if (!summary) return "";
  const runId = task?.detail?.run_id ?? task?.runId ?? "";
  const selectedAlarmIndex = state?.workspaceAlarmDialog?.runId === runId ? Number(state.workspaceAlarmDialog.index) : null;
  const manifestOpen = state?.workspaceManifestDialog?.runId === runId;
  return `<div class="workspace-diagnostics"><div class="grid-2 workspace-diagnostics-top">${diagnosticSeriesPanel(summary, results, alarms, state, runId, t, "fusion")}${diagnosticSeriesPanel(summary, results, alarms, state, runId, t, "recent")}</div><div class="workspace-diagnostics-lower" data-workspace-diagnostics-lower>${alarmTimelinePanel(alarms, task?.alarmsError, summary, t, runId, selectedAlarmIndex)}${traceabilityPanel(summary, artifacts, task?.artifactsError, t, runId, manifestOpen)}</div></div>`;
}

function eventDrawer(task, t) {
  const eventRows = taskEventRows(task?.events, t);
  return `<div class="event-drawer-backdrop" data-event-drawer-backdrop hidden></div><aside class="event-drawer" data-event-drawer hidden role="dialog" aria-modal="true" aria-labelledby="event-drawer-title" tabindex="-1"><header class="panel-heading"><div><p class="eyebrow">${t("event.eyebrow")}</p><h2 id="event-drawer-title">${t("event.drawerTitle")}</h2></div><button class="button compact" data-event-drawer-close aria-label="${t("event.close")}">${t("action.close")}</button></header><p class="muted">${t("event.drawerDescription")}</p><div class="event-list" data-event-drawer-events><div class="event"><time>${t("event.connection")}</time><span>${task.connection === "disconnected" ? t("state.reconnecting") : connectionDisplay(task.connection, t).label}</span></div>${eventRows || `<div class="event"><time>—</time><span>${t("event.noEvents")}</span></div>`}<div class="event"><time>REST</time><span>${t("event.rest")}</span></div></div></aside>`;
}

function eventPanel(task, t) {
  return eventDrawer(task, t);
}

export function preflightPresentation(dataset, t) {
  const datasetStatus = String(dataset?.status ?? "").toUpperCase();
  const workerStatus = String(dataset?.preflight?.status ?? "").toUpperCase();
  if (datasetStatus === "VALID") return { key: "VALID", status: "VALID", label: localizedStatus("VALID", t), title: t("data.valid"), message: t("data.preflight.valid"), complete: true };
  if (["INVALID", "FAILED"].includes(datasetStatus) || ["INVALID", "FAILED"].includes(workerStatus)) {
    const failureStatus = workerStatus === "FAILED" || datasetStatus === "FAILED" ? "FAILED" : "INVALID";
    return { key: failureStatus, status: failureStatus, label: localizedStatus(failureStatus, t), title: t("data.invalid"), message: t("data.preflight.invalid"), complete: false };
  }
  if (workerStatus === "QUEUED") return { key: "QUEUED", status: "QUEUED", label: localizedStatus("QUEUED", t), title: t("data.preflightQueued"), message: t("data.preflight.queued"), complete: false };
  if (workerStatus === "RUNNING") return { key: "RUNNING", status: "RUNNING", label: localizedStatus("RUNNING", t), title: t("data.preflightRunning"), message: t("data.preflight.running"), complete: false };
  if (workerStatus === "COMPLETED") return { key: "VALIDATING", status: "COMPLETED", label: localizedStatus("VALIDATING", t), title: t("data.preflightFinalizing"), message: t("data.preflight.finalizing"), complete: false };
  return { key: "VALIDATING", status: workerStatus || datasetStatus || "VALIDATING", label: localizedStatus("VALIDATING", t), title: t("data.awaiting"), message: t("data.preflight.validating"), complete: false };
}

export function preflightFailureDisplay(dataset, t) {
  const preflight = dataset?.preflight ?? {};
  const failure = dataset?.error ?? preflight.error ?? preflight.failure ?? dataset?.preflight_error ?? {};
  const candidates = [dataset?.error?.code, preflight.error?.code, preflight.error_code, preflight.failure?.code, dataset?.preflight_error?.code, dataset?.preflight_error_code];
  const code = candidates.find(candidate => typeof candidate === "string" && /^[A-Z][A-Z0-9_]+$/.test(candidate)) ?? null;
  const translated = code ? t(`error.${code}`) : null;
  return {
    code,
    message: translated && translated !== `error.${code}` ? translated : t("data.preflight.invalid"),
    stage: failure?.stage ?? preflight.stage ?? null,
    diagnosticId: failure?.diagnostic_id ?? preflight.diagnostic_id ?? null,
    recoverable: typeof failure?.recoverable === "boolean" ? failure.recoverable : null
  };
}

export function dataView(state, i18n) {
  const t = i18n.t.bind(i18n); const dataset = state.dataset;
  const statistics = dataStatisticsSection(dataset, t);
  return `<section class="data-page"><header class="page-head data-page-head"><div><p class="eyebrow">${t("data.eyebrow")}</p><h1>${t("data.title")}</h1><p>${t("data.description")}</p></div><span class="scope-pill">${t("data.offlineScope")}</span></header><div class="grid-2 data-layout"><section class="panel data-upload-panel">${renderFilePicker(state.upload, t)}<div class="schema-line"><b>${t("data.requiredHeader")}</b><code>${DATASET_COLUMNS.join(", ")}</code></div><div data-upload-status>${dataUploadStatus(state.upload, t)}</div></section><section class="panel data-dataset-panel" data-dataset-content>${dataset ? datasetDetail(dataset, t) : emptyDatasetDetail(t)}</section></div><section class="data-stat-section" data-data-stats-content ${statistics ? "" : "hidden"}>${statistics}</section><section class="panel data-validation-panel"><div class="panel-heading"><div><p class="eyebrow">${t("data.validationEyebrow")}</p><h2>${t("data.validationTitle")}</h2></div><button class="button compact" data-use-dataset ${dataset?.status === "VALID" ? "" : "disabled"}>${t("data.useDataset")}</button></div><div data-validation-content>${dataValidationReport(dataset, state.upload, t)}</div></section></section>`;
}

function dataUploadStatus(upload, t) {
  return `${upload?.percent !== null && upload?.percent !== undefined ? `<p class="muted" aria-live="polite">${t("data.uploading", { name: upload.fileName ?? "…" })}: ${upload.percent}%</p>` : ""}${upload?.error ? errorPanel(upload.error, t, { id: "data-upload-authoritative-error" }) : ""}`;
}

export function datasetPreflightPanel(dataset, upload, t) {
  if (!dataset) return { title: t("data.preflightNoDataset"), presentation: null, content: `<div class="${upload?.error ? "callout error" : "empty"}">${t("data.preflightNoDataset")}</div>` };
  const preprocessing = datasetPreprocessing(dataset);
  const presentation = preflightPresentation(dataset, t);
  const failure = ["INVALID", "FAILED", "FAILED_RECOVERABLE"].includes(presentation.key) ? preflightFailureDisplay(dataset, t) : null;
  const content = failure ? `<div class="callout error"><strong>${failure.message}</strong>${failure.code ? `<br><code>${escapeHtml(failure.code)}</code>` : ""}</div>` : presentation.complete && preprocessing ? statistics(dataset, preprocessing, t) : `<div class="callout"><strong>${presentation.message}</strong><br><span class="muted">${t("data.noStats")}</span></div>`;
  return { title: presentation.title, presentation, content };
}

export function datasetPreprocessing(dataset: any) {
  return dataset?.algorithm_preprocessing ?? null;
}

export function datasetPreprocessingCounts(dataset: any, preprocessing = datasetPreprocessing(dataset)) {
  void dataset;
  const counts = preprocessing?.counts ?? {};
  return {
    rawRows: counts.raw_rows ?? null,
    invalidNumericRows: counts.invalid_numeric_rows ?? null,
    stopRows: counts.stop_rows ?? null,
    suspiciousRows: counts.suspicious_rows ?? null,
    runningRows: counts.running_rows ?? null,
    spikeRows: counts.spike_rows ?? null
  };
}

export function renderFilePicker(upload, t) {
  const selection = upload?.fileName ? escapeHtml(upload.fileName) : t("data.noFileSelected");
  const pending = upload?.percent !== null && upload?.percent !== undefined;
  const describedBy = upload?.error ? "file-selection data-upload-authoritative-error" : "file-selection";
  return `<label class="dropzone" data-dropzone><input class="file-input-visually-hidden" type="file" accept=".csv,text/csv" data-file aria-label="${t("aria.file")}" aria-describedby="${describedBy}" ${pending ? "disabled" : ""}><span class="upload-icon" aria-hidden="true">⇧</span><strong>${t("data.dropTitle")}</strong><small>${t("data.dropHint")}</small><span class="button file-button" aria-hidden="true">${t("data.choose")}</span><span id="file-selection" class="selected-file">${selection}</span></label>`;
}

function datasetDetail(dataset, t) {
  const preflight = preflightPresentation(dataset, t);
  const fileName = dataset.original_filename ?? dataset.file_name ?? dataset.filename ?? "—";
  const worker = dataset.preflight ?? {};
  const failure = ["INVALID", "FAILED", "FAILED_RECOVERABLE"].includes(preflight.key) ? preflightFailureDisplay(dataset, t) : null;
  const preflightRows = [
    [t("data.preflightJob"), worker.job_id],
    [t("data.preflightStatus"), worker.status ? `${preflight.label} · ${worker.status}` : preflight.label],
    [t("data.preflightQueuePosition"), worker.queue_position === null || worker.queue_position === undefined ? null : `#${worker.queue_position}`],
    [t("data.preflightStage"), worker.stage],
    [t("data.preflightAttempt"), worker.attempt_id],
    [t("data.preflightLease"), worker.lease_state],
    [t("data.preflightEvent"), worker.latest_event_id === null || worker.latest_event_id === undefined ? null : `#${worker.latest_event_id}`],
    [t("data.validationStarted"), dataset.validation_started_at],
    [t("data.validationFinished"), dataset.validation_finished_at],
    [t("data.diagnosticId"), failure?.diagnosticId],
    [t("data.errorStage"), failure?.stage]
  ].filter(([, value]) => value !== null && value !== undefined && value !== "");
  return `<div class="panel-heading"><div><p class="eyebrow">${t("data.selectedEyebrow")}</p><h2>${escapeHtml(displayProfileText(dataset.display_name ?? dataset.dataset_id, t))}</h2></div><span class="state ${preflight.key}" aria-label="${escapeHtml(preflight.label)}">${escapeHtml(preflight.label)}</span></div><dl class="dataset-details"><div><dt>${t("data.identity")}</dt><dd><code>${escapeHtml(dataset.dataset_id)}</code></dd></div><div><dt>${t("data.displayName")}</dt><dd>${escapeHtml(displayProfileText(dataset.display_name, t))}</dd></div><div><dt>${t("data.fileName")}</dt><dd>${escapeHtml(String(fileName))}</dd></div><div><dt>${t("data.size")}</dt><dd>${number(dataset.size_bytes)} ${t("data.bytes")}</dd></div><div><dt>${t("data.hash")}</dt><dd>${renderHashValue(dataset.sha256 ?? "—", "hash-value hash-value-inline", t)}</dd></div><div><dt>${t("data.timezone")}</dt><dd>${escapeHtml(dataset.timezone ?? "—")}</dd></div></dl><dl class="dataset-details preflight-details">${preflightRows.map(([label, value]) => `<div><dt>${escapeHtml(label)}</dt><dd>${escapeHtml(String(value))}</dd></div>`).join("")}</dl>${dataset.warnings?.length ? `<div class="callout"><strong>${t("data.warning")}</strong><br>${dataset.warnings.map(warning => `${escapeHtml(warning.code)} (${number(warning.count)})`).join(", ")}</div>` : ""}`;
}

function emptyDatasetDetail(t) {
  return `<div class="panel-heading"><div><p class="eyebrow">${t("data.selectedEyebrow")}</p><h2>${t("data.noDatasetSelected")}</h2></div></div><p class="empty">${t("data.noDatasetSelected")}</p>`;
}

function preprocessingMetadataValue(preprocessing, key) {
  if (key === "time") {
    const range = preprocessing?.time ?? {};
    const start = range.start ?? null;
    const end = range.end ?? null;
    return start && end ? `${start} — ${end}` : "—";
  }
  if (key === "sampling") {
    const sampling = preprocessing?.time?.sampling_period_ms ?? null;
    if (sampling && typeof sampling === "object") {
      const formatSamplingNumber = value => typeof value === "number" && Number.isFinite(value) ? number(value) : null;
      const median = formatSamplingNumber(sampling.median);
      if (median !== null) {
        const range = [sampling.min, sampling.max].map(formatSamplingNumber).filter(value => value !== null).join(" — ");
        return `${median} ms${range ? ` (${range} ms)` : ""}`;
      }
      return "—";
    }
    return "—";
  }
  return "—";
}

function preprocessingContractVersion(preprocessing) {
  return preprocessing?.preprocessing_contract_version ?? "—";
}

function technicalValue(value) {
  if (value === null || value === undefined || value === "") return "—";
  if (typeof value === "string" || typeof value === "number" || typeof value === "boolean") return String(value);
  try { return JSON.stringify(value); }
  catch { return "—"; }
}

function statistics(dataset, preprocessing, t) {
  const counts = datasetPreprocessingCounts(dataset, preprocessing);
  const cards = [[t("data.raw"), counts.rawRows], [t("data.invalidRows"), counts.invalidNumericRows], [t("data.stopped"), counts.stopRows], [t("data.suspicious"), counts.suspiciousRows], [t("data.running"), counts.runningRows], [t("data.spikes"), counts.spikeRows]];
  const metadata = [[t("data.preprocessingSchema"), technicalValue(preprocessing.schema_version)], [t("data.preprocessingContract"), preprocessingContractVersion(preprocessing)], [t("data.preprocessing"), technicalValue(preprocessing.filter_path)], [t("data.preprocessingParameters"), technicalValue(preprocessing.parameters)], [t("data.timeRange"), preprocessingMetadataValue(preprocessing, "time")], [t("data.sampling"), preprocessingMetadataValue(preprocessing, "sampling")], [t("data.hash"), preprocessing.summary_sha256]];
  return `<div class="stats">${cards.map(([label, value]) => `<article class="stat"><span>${label}</span><strong>${number(value)}</strong><small>${t("data.worker")}</small></article>`).join("")}</div><div class="table-wrap"><table class="validation-table"><thead><tr><th>${t("data.validation")}</th><th>${t("data.status")}</th><th>${t("data.contract")}</th></tr></thead><tbody><tr><td>${t("data.preprocessing")}</td><td><span class="state VALID" aria-label="${escapeHtml(localizedStatus("VALID", t))}">${escapeHtml(localizedStatus("VALID", t))}</span></td><td>${escapeHtml(preprocessingContractVersion(preprocessing))} · ${escapeHtml(technicalValue(preprocessing.filter_path))}</td></tr>${metadata.map(([label, value]) => `<tr><td>${label}</td><td colspan="2">${label === t("data.hash") ? renderHashValue(value ?? "—", "hash-value hash-value-inline", t) : escapeHtml(value ?? "—")}</td></tr>`).join("")}</tbody></table></div>`;
}

export function dataStatisticsSection(dataset, t) {
  const presentation = preflightPresentation(dataset, t);
  const preprocessing = datasetPreprocessing(dataset);
  if (!presentation.complete || !preprocessing) return "";
  const counts = datasetPreprocessingCounts(dataset, preprocessing);
  const cards = [[t("data.raw"), counts.rawRows, t("data.sourceRecorded")], [t("data.invalidRows"), counts.invalidNumericRows, t("data.numericDetails", { count: number(counts.invalidNumericRows) })], [t("data.stopped"), counts.stopRows, t("data.worker")], [t("data.suspicious"), counts.suspiciousRows, t("data.worker")], [t("data.running"), counts.runningRows, t("data.worker")], [t("data.spikes"), counts.spikeRows, t("data.sourceRecorded")]];
  return `<div class="data-stat-grid" aria-label="${t("data.stats")}">${cards.map(([label, value, detail]) => `<article><span>${label}</span><strong>${number(value)}</strong><small>${detail}</small></article>`).join("")}</div>`;
}

function datasetHeaderMatchesContract(dataset) {
  return Array.isArray(dataset?.columns) && dataset.columns.length === DATASET_COLUMNS.length && dataset.columns.every((column, index) => column === DATASET_COLUMNS[index]);
}

function validationState(status, label, t) {
  return `<span class="state ${status}" aria-label="${escapeHtml(label)}">${escapeHtml(label)}</span>`;
}

export function dataValidationReport(dataset, upload, t) {
  if (!dataset) return upload?.error ? errorPanel(upload.error, t, { id: "data-upload-error-context", assertive: false }) : `<div class="empty">${t("data.preflightNoDataset")}</div>`;
  const presentation = preflightPresentation(dataset, t);
  const preprocessing = datasetPreprocessing(dataset);
  const failure = ["INVALID", "FAILED", "FAILED_RECOVERABLE"].includes(presentation.key) ? preflightFailureDisplay(dataset, t) : null;
  if (!presentation.complete || !preprocessing) {
    const details = failure
      ? [failure.message, failure.code, failure.stage ? `${t("data.errorStage")}: ${failure.stage}` : null, failure.diagnosticId ? `${t("data.diagnosticId")}: ${failure.diagnosticId}` : null].filter(Boolean).join(" · ")
      : [presentation.message || t("data.awaitingDetails"), dataset.preflight?.job_id ? `${t("data.preflightJob")}: ${dataset.preflight.job_id}` : null, dataset.preflight?.queue_position ? `${t("data.preflightQueuePosition")}: #${dataset.preflight.queue_position}` : null, dataset.preflight?.stage ? `${t("data.preflightStage")}: ${dataset.preflight.stage}` : null, dataset.preflight?.attempt_id ? `${t("data.preflightAttempt")}: ${dataset.preflight.attempt_id}` : null, dataset.preflight?.latest_event_id ? `${t("data.preflightEvent")}: #${dataset.preflight.latest_event_id}` : null].filter(Boolean).join(" · ");
    const action = failure ? t("data.startBlocked") : t("data.review");
    const alert = failure ? `<div class="callout error" role="alert" aria-live="assertive"><strong>${escapeHtml(failure.message)}</strong><br><code>${escapeHtml([failure.code, failure.diagnosticId].filter(Boolean).join(" · "))}</code></div>` : "";
    return `${alert}<div class="table-wrap"><table class="validation-table"><thead><tr><th>${t("data.check")}</th><th>${t("data.result")}</th><th>${t("data.details")}</th><th>${t("data.record")}</th></tr></thead><tbody><tr><td>${t("data.preflightCheck")}</td><td>${validationState(presentation.key, presentation.label, t)}</td><td>${escapeHtml(details)}</td><td>${escapeHtml(action)}</td></tr></tbody></table></div>`;
  }
  const counts = datasetPreprocessingCounts(dataset, preprocessing);
  const timeRange = preprocessingMetadataValue(preprocessing, "time");
  const sampling = preprocessingMetadataValue(preprocessing, "sampling");
  const contractVersion = preprocessingContractVersion(preprocessing);
  const schemaVersion = technicalValue(preprocessing.schema_version);
  const headerMatches = datasetHeaderMatchesContract(dataset);
  const integrityComplete = Boolean(dataset.sha256 && preprocessing.summary_sha256);
  const rows = [
    [t("data.headerCheck"), headerMatches ? "VALID" : "WARNING", headerMatches ? t("data.verified") : t("data.notReported"), t("data.headerDetails"), t("data.keep"), false],
    [t("data.numericCheck"), Number(counts.invalidNumericRows ?? 0) === 0 ? "VALID" : "WARNING", number(counts.invalidNumericRows), t("data.numericDetails", { count: number(counts.invalidNumericRows) }), t("data.review"), false],
    [t("data.timeCheck"), timeRange === "—" ? "WARNING" : "VALID", timeRange === "—" ? t("data.notReported") : t("data.verified"), t("data.timeDetails", { range: timeRange }), t("data.keep"), false],
    [t("data.sampling"), sampling === "—" ? "WARNING" : "VALID", sampling === "—" ? t("data.notReported") : t("data.verified"), sampling, t("data.keep"), false],
    [t("data.preprocessingSchema"), schemaVersion === "—" ? "WARNING" : "VALID", schemaVersion === "—" ? t("data.notReported") : t("data.verified"), schemaVersion, t("data.keep"), false],
    [t("data.preprocessingContract"), contractVersion === "—" ? "WARNING" : "VALID", contractVersion === "—" ? t("data.notReported") : t("data.verified"), `${contractVersion} · ${t("data.filterPath")}: ${technicalValue(preprocessing.filter_path)} · ${t("data.preprocessingParameters")}: ${technicalValue(preprocessing.parameters)}`, t("data.keep"), false],
    [t("data.integrityCheck"), integrityComplete ? "VALID" : "WARNING", integrityComplete ? t("data.verified") : t("data.notReported"), `${t("data.integrityDetails")}: ${dataset.sha256 ? renderHashValue(dataset.sha256, "hash-value hash-value-inline", t) : "—"} · ${preprocessing.summary_sha256 ? renderHashValue(preprocessing.summary_sha256, "hash-value hash-value-inline", t) : "—"}`, t("data.keep"), true],
    [t("data.preflightCheck"), presentation.key, presentation.label, `${t("data.preflightDetails", { contract: contractVersion })} · ${t("data.inputHash")}: ${technicalValue(preprocessing.input_sha256)}`, t("data.readyForParameters"), false]
  ];
  return `<div class="table-wrap"><table class="validation-table"><thead><tr><th>${t("data.check")}</th><th>${t("data.result")}</th><th>${t("data.details")}</th><th>${t("data.record")}</th></tr></thead><tbody>${rows.map(([check, status, label, details, action, detailsHtml]) => `<tr><td>${escapeHtml(check)}</td><td>${validationState(status, label, t)}</td><td>${detailsHtml ? details : escapeHtml(details)}</td><td>${escapeHtml(action)}</td></tr>`).join("")}</tbody></table></div>`;
}

export function configView(state, i18n) {
  const t = i18n.t.bind(i18n); const draft = state.customDraft; const profile = draft ? customDraftProfile(draft) : state.customProfile ?? state.profile; const mode = draft || state.customProfile ? "CUSTOM" : "REFERENCE"; const groups = parameterGroups(profile); const save = state.customSave ?? { pending: false, versionId: null, error: null }; const scope = draft ? state.configScope ?? "shared" : "shared";
  const rename = state.customRename ?? { editing: false, pending: false, error: null };
  const alias = draft ? validateCustomDraftName(draft.display_name, t) : null;
  const index = groups.map(([groupKey]) => `<a data-config-anchor href="#parameter-group-${escapeHtml(groupKey)}">${escapeHtml(parameterGroupLabel(groupKey, t))}<code>${escapeHtml(groupKey)}</code></a>`).join("");
  const modeButton = (modeName: "REFERENCE" | "CUSTOM", label: string, selected: boolean) => `<button class="button mode-button ${selected ? "primary" : ""}" data-config-mode="${modeName.toLowerCase()}" aria-label="${label} (${modeName})" ${save.pending ? "disabled" : ""}><span>${label}</span><small>${modeName}</small></button>`;
  const savedProfile = save.profile ?? state.customProfile;
  const saveFeedback = save.pending ? `<span class="state VALIDATING">${t("config.saving")}</span>` : save.error ? `<div class="callout error"><strong>${escapeHtml(save.error.title)}</strong>${save.error.detail ? `<br><code>${escapeHtml(save.error.detail)}</code>` : ""}</div>` : save.versionId ? `<div class="callout success"><strong>${t("config.saved", { version: save.versionId })}</strong><br><span>${t("config.savedAuthoritative", { name: escapeHtml(displayProfileText(savedProfile?.display_name, t)) })}</span><br>${renderHashValue(savedProfile?.normalized_sha256 ?? savedProfile?.sha256 ?? "—", "hash-value", t)}<br><span>${t("config.savedImmutable")}</span></div>` : draft ? `<span class="state ${draftIsDirty(draft) ? "WARNING" : "READY"}">${draftIsDirty(draft) ? t("config.dirty") : t("config.draftReady")}</span>` : "";
  const scopeTabs = draft ? `<h3>${t("config.editScope")}</h3><div class="agent-tabs config-scope-tabs" role="tablist" aria-label="${t("aria.parameterScope")}"><button data-config-scope="shared" class="${scope === "shared" ? "active" : ""}" role="tab" aria-selected="${scope === "shared"}">${t("config.shared")}</button>${[1,2,3].map(agent => `<button data-config-scope="agent-${agent}" class="${scope === `agent-${agent}` ? "active" : ""}" role="tab" aria-selected="${scope === `agent-${agent}`}">${t("workspace.agent", { agent })}<small>${displayAgentSegment(profile?.agents?.[agent - 1]?.segment, t)}</small></button>`).join("")}</div>` : `<h3>${t("config.agents")}</h3><div class="agent-tabs">${[1,2,3].map(agent => `<button disabled>${t("workspace.agent", { agent })}<small>${displayAgentSegment(profile?.agents?.[agent - 1]?.segment, t)}</small></button>`).join("")}</div>`;
  const readonlyHint = draft ? `<div class="callout"><strong class="locked">${t("config.editable")}</strong><br>${t("config.editableHint")}</div>` : mode === "CUSTOM" ? `<div class="callout"><strong class="locked">${t("config.savedImmutable")}</strong><br>${t("config.savedImmutableHint")}</div>` : `<div class="callout"><strong class="locked">${t("config.referenceLocked")}</strong><br>${t("config.referenceLockedHint")}</div>`;
  const profileLabel = mode === "CUSTOM" ? t("config.custom") : t("profile.referenceCompatible");
  const profileName = draft ? t("config.customDraft") : profileDisplayName(profile, t);
  const savedProfiles = Array.isArray(state.customProfiles) ? state.customProfiles : [];
  const referencePickerText = `${t("profile.referenceCompatible")} · ${displayProfileVersion(state.profile?.version_id, t)}`;
  const profilePicker = savedProfiles.length ? `<label class="field saved-profile-picker"><span>${t("config.savedProfiles")}</span><select data-custom-profile ${draft || save.pending ? "disabled" : ""}><option value="">${escapeHtml(referencePickerText)}</option>${savedProfiles.map(item => `<option value="${escapeHtml(item.version_id)}" ${item.version_id === state.customProfile?.version_id ? "selected" : ""}>${escapeHtml(displayProfileText(item.display_name ?? item.version_id, t))} · ${escapeHtml(displayProfileVersion(item.version_id, t))}</option>`).join("")}</select></label>` : "";
  const renameFeedback = rename.pending ? `<span class="state VALIDATING">${t("config.renaming")}</span>` : rename.error ? `<div class="callout error"><strong>${escapeHtml(rename.error.title)}</strong>${rename.error.detail ? `<br><code>${escapeHtml(rename.error.detail)}</code>` : ""}</div>` : "";
  const renameControl = !draft && mode === "CUSTOM" ? rename.editing ? `<form class="rename-profile-form" data-profile-rename-form><label class="field"><span>${t("config.renameLabel")}</span><input data-profile-rename-input value="${escapeHtml(displayProfileText(profile?.display_name, t, ""))}" maxlength="160" required ${rename.pending ? "disabled" : ""}></label><div class="inline-actions"><button class="button primary" type="submit" ${rename.pending ? "disabled" : ""}>${rename.pending ? t("config.renaming") : t("action.rename")}</button><button class="button" type="button" data-profile-rename-cancel ${rename.pending ? "disabled" : ""}>${t("action.cancel")}</button></div></form>` : `<div class="inline-actions"><button class="button" data-profile-rename>${t("action.rename")}</button></div>` : "";
  const aliasControl = draft ? `<label class="field draft-alias-field"><span class="field-label">${t("config.aliasLabel")}</span><small id="draft-alias-help" class="field-helper field-hint">${t("config.aliasHelp")}</small><span class="field-control"><input data-draft-display-name value="${escapeHtml(draft.display_name ?? "")}" maxlength="128" required aria-describedby="draft-alias-help draft-alias-error" aria-invalid="${draft.display_name && !alias?.valid ? "true" : "false"}" ${save.pending ? "disabled" : ""}></span><small id="draft-alias-error" data-draft-alias-error class="field-error" aria-live="polite">${draft.display_name && !alias?.valid ? escapeHtml(alias?.error ?? "") : ""}</small></label>` : "";
  const draftActions = draft ? `<div class="custom-create-action"><div class="inline-actions"><button class="button" data-draft-discard ${save.pending ? "disabled" : ""}>${t("action.discard")}</button><button class="button" data-draft-reset ${save.pending ? "disabled" : ""}>${t("action.resetBase")}</button><button class="button" data-draft-restore-defaults ${save.pending ? "disabled" : ""}>${t("action.restoreDefaults")}</button><button class="button primary" data-draft-save ${draftIsDirty(draft) && alias?.valid && !save.pending ? "" : "disabled"}>${save.pending ? t("config.saving") : t("action.save")}</button></div><div class="custom-create-feedback" aria-live="polite">${saveFeedback}</div></div>` : mode === "CUSTOM" ? `<div class="custom-create-action"><button class="button primary" data-draft-edit>${t("action.editNew")}</button><div class="custom-create-feedback" aria-live="polite">${saveFeedback}</div></div>` : "";
  return `<section><header class="page-head config-page-head"><div class="page-head-copy"><p class="eyebrow">${t("config.eyebrow")}</p><h1>${t("config.title")}</h1><p>${t("config.description")}</p></div><div class="config-action-area"><div class="inline-actions config-actions">${modeButton("REFERENCE", t("profile.referenceCompatible"), mode === "REFERENCE")}${modeButton("CUSTOM", t("config.custom"), mode === "CUSTOM")}<button class="button mode-button config-export" data-export-profile ${profile ? "" : "disabled"}><span>${t("action.export")}</span><small aria-hidden="true">&nbsp;</small></button></div><div class="custom-create-feedback" aria-live="polite">${saveFeedback}</div></div></header><div class="config-layout"><aside class="panel config-index" aria-label="${t("aria.parameterGroups")}">${index || `<span class="muted">${t("state.loading")}</span>`}</aside><section class="panel config-detail-scroll" data-config-detail-scroll tabindex="0"><div class="profile-row"><div class="profile-copy"><p class="eyebrow">${profileLabel} <code>${mode}</code></p><h2>${escapeHtml(profileName)}</h2><span class="muted">${escapeHtml(displayProfileVersion(profile?.version_id, t))} · ${renderHashValue(profile?.normalized_sha256 ?? profile?.sha256 ?? "—", "hash-value hash-value-inline", t)}</span></div><span class="state ${draft ? "WARNING" : "VALID"} profile-readonly">${draft ? t("config.draft") : t("config.readonly")}</span></div>${readonlyHint}${profilePicker}${renameControl}<div class="custom-create-feedback" aria-live="polite">${renameFeedback}</div>${aliasControl}${scopeTabs}${draft ? renderDraftParameterFields(draft, scope, t) : renderProfileParameterFields(profile, t)}<div class="panel"><h3>${t("config.fixed")}</h3><p class="muted">${t("config.fixedHint")}</p><div class="snapshot">${parameterLeafReadout(profile?.fixed_items ?? {}, "fixed", t)}</div></div>${draftActions}</section></div></section>`;
}

function errorPanel(error, t, options: any = {}) {
  const message = formatApiError(error, t);
  const id = options.id ? ` id="${escapeHtml(options.id)}"` : "";
  const announcement = options.assertive === false ? "" : ' role="alert" aria-live="assertive"';
  return `<div class="callout error"${id}${announcement}><strong>${escapeHtml(message.title)}</strong>${message.detail ? `<br><code>${escapeHtml(message.detail)}</code>` : ""}</div>`;
}

function canCreateSimulation(state) {
  const datasetValid = state.dataset?.status === "VALID";
  const profile = state.customProfile ?? state.profile;
  const profileValid = Boolean(profile?.version_id);
  const mappingValid = profile?.load_mapping?.mapping_type === "identity";
  const agentsValid = validateAgentCollection(profile?.agents?.map(agent => agent.agent));
  return datasetValid && profileValid && mappingValid && agentsValid;
}

function bindInteractions(root, state, taskStore, datasetPoller, i18n, render) {
  root.querySelectorAll("[data-nav]").forEach(button => button.addEventListener("click", () => { if (button.dataset.nav !== "replay") stopReplayPlayback(state); state.view = button.dataset.nav; render(); if (state.view === "queue") void loadQueue(state, render, taskStore); if (state.view === "history" || state.view === "replay") void loadHistory(state, render); }));
  root.querySelector("[data-open-replay]")?.addEventListener("click", () => { state.view = "replay"; render(); });
  root.querySelector("[data-language]")?.addEventListener("change", event => { state.language = event.target.value; i18n.setLanguage(state.language); render(); });
  root.querySelectorAll("[data-agent]").forEach(button => button.addEventListener("click", () => { void taskStore.selectAgent(Number(button.dataset.agent)); }));
  root.querySelector("[data-file]")?.addEventListener("change", event => { const file = event.target.files?.[0]; if (file) void uploadFile(state, file, datasetPoller, render); });
  const dropzone = root.querySelector("[data-dropzone]");
  ["dragenter", "dragover"].forEach(type => dropzone?.addEventListener(type, event => { event.preventDefault(); if (state.upload?.percent === null || state.upload?.percent === undefined) dropzone.classList.add("is-dragover"); }));
  ["dragleave", "drop"].forEach(type => dropzone?.addEventListener(type, event => { event.preventDefault(); dropzone.classList.remove("is-dragover"); }));
  dropzone?.addEventListener("drop", event => { const file = (event as DragEvent).dataTransfer?.files?.[0]; if (file && (state.upload?.percent === null || state.upload?.percent === undefined)) void uploadFile(state, file, datasetPoller, render); });
  root.querySelector("[data-use-dataset]")?.addEventListener("click", () => { if (state.dataset?.status === "VALID") { state.view = "config"; render(); } });
  root.querySelectorAll("[data-config-mode]").forEach(button => button.addEventListener("click", () => {
    if (button.dataset.configMode === "reference") { state.customProfile = null; state.customDraft = null; state.customSave = { pending: false, versionId: null, error: null }; state.customRename = { editing: false, pending: false, error: null }; state.configScope = "shared"; render(); }
    else if (!state.customDraft) beginCustomDraft(state, render);
  }));
  root.querySelector("[data-custom-profile]")?.addEventListener("change", event => {
    selectCustomProfile(state, event.target.value, render);
  });
  root.querySelectorAll("[data-config-scope]").forEach(button => button.addEventListener("click", () => { state.configScope = button.dataset.configScope; render(); }));
  root.querySelectorAll("[data-draft-input]").forEach(input => input.addEventListener("change", () => { updateDraftValue(state, input); render(); }));
  root.querySelector("[data-draft-display-name]")?.addEventListener("input", event => {
    if (!state.customDraft || state.customSave?.pending) return;
    state.customDraft.display_name = event.target.value;
    state.customSave = { pending: false, versionId: null, profile: null, error: null };
    const validation = validateCustomDraftName(state.customDraft.display_name, i18n.t.bind(i18n));
    const input = event.target as HTMLInputElement;
    input.setAttribute("aria-invalid", String(!validation.valid));
    const error = root.querySelector("[data-draft-alias-error]");
    if (error) error.textContent = state.customDraft.display_name && !validation.valid ? validation.error : "";
    const save = root.querySelector("[data-draft-save]") as HTMLButtonElement | null;
    if (save) save.disabled = !validation.valid || !draftIsDirty(state.customDraft);
  });
  root.querySelectorAll("[data-config-anchor]").forEach(anchor => anchor.addEventListener("click", event => {
    const targetId = anchor.getAttribute("href")?.slice(1);
    const detail = root.querySelector("[data-config-detail-scroll]");
    const target = targetId ? root.querySelector(`#${targetId}`) : null;
    if (!detail || !target) return;
    event.preventDefault();
    detail.scrollTo({ top: Math.max(0, target.offsetTop - detail.offsetTop - 12), behavior: "smooth" });
  }));
  bindEventDrawerInteractions(root);
  bindAlarmDialogInteractions(root, state, render);
  bindManifestDialogInteractions(root, state, render);
  bindHashCopyInteractions(root, i18n);
  root.querySelector("[data-draft-discard]")?.addEventListener("click", () => discardCustomDraft(state, render));
  root.querySelector("[data-draft-reset]")?.addEventListener("click", () => resetCustomDraft(state, render));
  root.querySelector("[data-draft-restore-defaults]")?.addEventListener("click", () => restoreCustomDraftDefaults(state, render));
  root.querySelector("[data-draft-edit]")?.addEventListener("click", () => beginCustomDraft(state, render));
  root.querySelector("[data-draft-save]")?.addEventListener("click", () => void saveCustomDraft(state, i18n, render));
  root.querySelector("[data-profile-rename]")?.addEventListener("click", () => { if (!state.customRename?.pending) { state.customRename = { editing: true, pending: false, error: null }; render(); } });
  root.querySelector("[data-profile-rename-cancel]")?.addEventListener("click", () => { if (!state.customRename?.pending) { state.customRename = { editing: false, pending: false, error: null }; render(); } });
  root.querySelector("[data-profile-rename-form]")?.addEventListener("submit", event => { event.preventDefault(); const input = root.querySelector("[data-profile-rename-input]") as HTMLInputElement | null; void renameCustomProfile(state, i18n, input?.value ?? "", render); });
  root.querySelector("[data-export-profile]")?.addEventListener("click", () => exportProfile(state.customProfile ?? state.profile));
  bindQueueCancelInteractions(root, state, taskStore, i18n, render);
  bindHistoryInteractions(root, state, taskStore, i18n, render);
  bindRunActionInteractions(root, state, taskStore, i18n, root);
  const chartSummary = chartSummaryForView(state, taskStore.state.summary);
  root.querySelectorAll("[data-series]").forEach(input => input.addEventListener("change", () => { state.chart.series[input.dataset.series] = input.checked; drawChart(root, state, chartSummary, i18n); }));
  root.querySelectorAll("[data-zoom]").forEach(button => button.addEventListener("click", () => { state.chart.zoom = Math.max(1, Math.min(8, state.chart.zoom * Number(button.dataset.zoom))); drawChart(root, state, chartSummary, i18n); }));
  root.querySelector("[data-reset]")?.addEventListener("click", () => { state.chart.zoom = 1; state.chart.pan = 0; drawChart(root, state, chartSummary, i18n); });
  root.querySelector("[data-replay-play]")?.addEventListener("click", () => toggleReplayPlayback(root, state, taskStore.state, i18n));
  root.querySelector("[data-replay-speed]")?.addEventListener("change", event => { replayPlaybackState(state).speed = Number(event.target.value) || 1; updateReplayPlaybackDom(root, state, taskStore.state, i18n); });
  const replayPosition = root.querySelector("[data-replay-position]") as HTMLInputElement | null;
  const updateReplayPosition = value => { setReplayPlaybackPosition(state, replayPoints(historyState(state).replay.data).length, value); updateReplayPlaybackDom(root, state, taskStore.state, i18n); };
  replayPosition?.addEventListener("input", event => updateReplayPosition((event.target as HTMLInputElement).value));
  replayPosition?.addEventListener("keydown", event => {
    const count = replayPoints(historyState(state).replay.data).length;
    const next = replayKeyboardPosition(event.key, replayPosition.value, count);
    if (next !== null) { event.preventDefault(); updateReplayPosition(next); }
  });
  bindChartPointer(root, state, chartSummary, i18n, workspaceResultsForRun(taskStore.state), workspaceAlarmsForRun(taskStore.state), taskStore.state?.detail?.run_id ?? taskStore.state?.runId ?? "");
}

function bindEventDrawerInteractions(root) {
  const drawer = root.querySelector("[data-event-drawer]");
  const backdrop = root.querySelector("[data-event-drawer-backdrop]");
  const opens = Array.from(root.querySelectorAll("[data-event-drawer-open]"));
  let opener: any = null;
  const close = () => {
    if (!drawer || !backdrop) return;
    drawer.hidden = true; backdrop.hidden = true; opener?.focus?.({ preventScroll: true });
  };
  opens.forEach((open: any) => open.addEventListener("click", () => {
    if (!drawer || !backdrop) return;
    opener = open;
    drawer.hidden = false; backdrop.hidden = false; drawer.focus?.({ preventScroll: true });
  }));
  root.querySelector("[data-event-drawer-close]")?.addEventListener("click", close);
  backdrop?.addEventListener("click", close);
  drawer?.addEventListener("keydown", event => { if (event.key === "Escape") close(); });
}

export function bindAlarmDialogInteractions(root, state, render) {
  root.querySelectorAll("[data-alarm-open]").forEach(button => button.addEventListener("click", () => {
    const index = Number(button.dataset.alarmOpen);
    const runId = String(button.dataset.alarmRunId ?? "");
    if (!runId || !Number.isInteger(index)) return;
    state.workspaceManifestDialog = null;
    state.workspaceAlarmDialog = { runId, index };
    render();
  }));
  const dialog = root.querySelector("[data-alarm-dialog]");
  if (!dialog) return;
  const restoreFocus = () => root.querySelector(`[data-alarm-open="${dialog.dataset.alarmIndex}"]`)?.focus?.({ preventScroll: true });
  let closing = false;
  const close = () => {
    if (closing) return;
    closing = true;
    state.workspaceAlarmDialog = null;
    if (dialog.open) dialog.close?.();
    restoreFocus();
    closing = false;
  };
  if (dialog.dataset.open === "true") {
    try { dialog.showModal?.(); } catch { /* The dialog may already be open in a browser restore cycle. */ }
    dialog.focus?.({ preventScroll: true });
  }
  root.querySelector("[data-alarm-dialog-close]")?.addEventListener("click", close);
  dialog.addEventListener("cancel", event => { event.preventDefault(); close(); });
  dialog.addEventListener("keydown", event => { if (event.key === "Escape") { event.preventDefault(); close(); } });
  dialog.addEventListener("close", () => {
    if (closing) return;
    state.workspaceAlarmDialog = null;
    restoreFocus();
  });
}

export function bindManifestDialogInteractions(root, state, render) {
  root.querySelectorAll("[data-manifest-dialog-open]").forEach(button => button.addEventListener("click", () => {
    const runId = String(button.dataset.manifestRunId ?? "");
    if (!runId) return;
    state.workspaceAlarmDialog = null;
    state.workspaceManifestDialog = { runId };
    render();
  }));
  const dialog = root.querySelector("[data-manifest-dialog]");
  if (!dialog) return;
  const restoreFocus = () => root.querySelector(`[data-manifest-dialog-open][data-manifest-run-id="${dialog.dataset.manifestRunId}"]`)?.focus?.({ preventScroll: true });
  let closing = false;
  const close = () => {
    if (closing) return;
    closing = true;
    state.workspaceManifestDialog = null;
    if (dialog.open) dialog.close?.();
    restoreFocus();
    closing = false;
  };
  if (dialog.dataset.open === "true") {
    try { dialog.showModal?.(); } catch { /* The dialog may already be open in a browser restore cycle. */ }
    dialog.focus?.({ preventScroll: true });
  }
  root.querySelector("[data-manifest-dialog-close]")?.addEventListener("click", close);
  dialog.addEventListener("cancel", event => { event.preventDefault(); close(); });
  dialog.addEventListener("close", () => {
    if (closing) return;
    state.workspaceManifestDialog = null;
    restoreFocus();
  });
}

function bindHashCopyInteractions(root, i18n) {
  root.querySelectorAll("[data-copy-hash]").forEach(button => button.addEventListener("click", () => {
    const value = String(button.getAttribute("data-copy-hash") ?? "");
    if (!value || value === "—" || !globalThis.navigator?.clipboard?.writeText) return;
    void globalThis.navigator.clipboard.writeText(value).then(
      () => notify(root.querySelector(".toast-region"), i18n.t("hash.copied")),
      () => notify(root.querySelector(".toast-region"), i18n.t("hash.copyFailed"))
    );
  }));
}

function bindQueueCancelInteractions(root, state, taskStore, i18n, render) {
  root.querySelectorAll("[data-queue-cancel]").forEach(button => button.addEventListener("click", () => void cancelQueuedSimulation(button.dataset.queueCancel, state, taskStore, i18n, root, render)));
}

function bindHistoryInteractions(root, state, taskStore, i18n, render) {
  root.querySelector("[data-history-query]")?.addEventListener("input", event => updateHistoryFilters(state, { query: String(event.target.value).slice(0, 256) }, render, root));
  root.querySelector("[data-history-status]")?.addEventListener("change", event => updateHistoryFilters(state, { status: event.target.value }, render, root));
  root.querySelector("[data-history-mode]")?.addEventListener("change", event => updateHistoryFilters(state, { mode: event.target.value }, render, root));
  bindHistoryActionButtons(root, state, taskStore, i18n, render);
}

export function updateHistoryFilters(state, changes, render, root: any = null) {
  const history = historyState(state);
  const unchanged = Object.entries(changes).every(([key, value]) => history[key] === value);
  if (unchanged) return;
  const table = root?.querySelector?.("[data-history-table-wrap]");
  state.history = { ...history, ...changes, items: [], nextCursor: null, hasMore: false, total: null, loading: false, error: null, restoreScrollTop: table?.scrollTop ?? null, listEpoch: (history.listEpoch ?? 0) + 1 };
  render();
  void loadHistory(state, render);
}

function bindHistoryActionButtons(root, state, taskStore, i18n, render) {
  root.querySelectorAll("[data-history-select]").forEach(button => button.addEventListener("click", () => void selectHistoryRun(state, taskStore, button.dataset.historySelect, i18n, render, false, true)));
  root.querySelectorAll("[data-history-replay]").forEach(button => button.addEventListener("click", () => void selectHistoryRun(state, taskStore, button.dataset.historyReplay, i18n, render, true)));
  root.querySelectorAll("[data-history-export]").forEach(button => button.addEventListener("click", async () => { if (state.history.selectedRunId !== button.dataset.historyExport) await selectHistoryRun(state, taskStore, button.dataset.historyExport, i18n, render); downloadReplayExport(state); }));
  root.querySelectorAll("[data-history-manifest]").forEach(button => button.addEventListener("click", async () => { if (state.history.selectedRunId !== button.dataset.historyManifest) await selectHistoryRun(state, taskStore, button.dataset.historyManifest, i18n, render); downloadManifest(state); }));
  root.querySelectorAll("[data-replay-agent]").forEach(button => button.addEventListener("click", () => void selectReplayAgent(state, taskStore, Number(button.dataset.replayAgent), render)));
  root.querySelector("[data-replay-export]")?.addEventListener("click", () => downloadReplayExport(state));
  root.querySelector("[data-artifact-manifest]")?.addEventListener("click", () => downloadManifest(state));
  root.querySelectorAll("[data-artifact-download]").forEach(button => button.addEventListener("click", () => {
    const detail = state.history?.detail;
    if (detail && historyArtifactGate(detail, state.history.artifacts).ready) downloadUrl(state.api.artifactDownloadUrl?.(detail.run_id, button.dataset.artifactDownload));
  }));
  root.querySelector("[data-history-more]")?.addEventListener("click", () => void loadHistory(state, render, true));
}

async function uploadFile(state, file, datasetPoller, render) {
  const epoch = ++state.datasetEpoch;
  datasetPoller.stop();
  state.dataset = null;
  state.upload = { percent: 0, error: null, fileName: file.name }; render();
  try {
    const dataset = await state.api.uploadDataset(file, file.name, percent => {
      if (epoch === state.datasetEpoch) { state.upload.percent = percent; render(); }
    });
    if (epoch !== state.datasetEpoch) return;
    state.dataset = dataset;
    state.upload = { ...state.upload, percent: null, error: null };
    datasetPoller.watch(dataset);
  } catch (error) { if (epoch === state.datasetEpoch) state.upload = { ...state.upload, percent: null, error }; }
  render();
}

function inputValue(input: any) {
  if (input.dataset.valueType === "boolean" && input.type === "checkbox") return Boolean(input.checked);
  if (input.value === "__inherit__") return undefined;
  if (input.dataset.valueType === "integer" || input.dataset.valueType === "number") {
    if (input.value === "") return undefined;
    const numeric = Number(input.value);
    return Number.isFinite(numeric) ? input.dataset.valueType === "integer" ? Math.trunc(numeric) : numeric : undefined;
  }
  return input.value;
}

export function updateDraftValue(state, input): boolean {
  const draft = state.customDraft;
  const relative = input?.dataset?.draftPath;
  const definition = profileEditableDefinitions(draft?.baseProfile).find(item => item.relative === relative);
  if (!draft || !definition) return false;
  const value = inputValue(input);
  if (input.dataset.draftScope === "agent") {
    const agent = draft.agents.find(item => item.agent === Number(input.dataset.agent));
    if (!agent) return false;
    if (value === undefined) removePath(agent.parameters, definition.parts); else writePath(agent.parameters, definition.parts, value);
  } else if (value !== undefined) writePath(draft.shared_parameters, definition.parts, value);
  state.customSave = { pending: false, versionId: null, error: null };
  return true;
}

export function resetCustomDraft(state, render): boolean {
  if (!state.customDraft || state.customSave?.pending) return false;
  state.customDraft = createCustomDraft(state.customDraft.baseProfile);
  state.customSave = { pending: false, versionId: null, error: null };
  render();
  return true;
}

export function restoreCustomDraftDefaults(state, render): boolean {
  const draft = state.customDraft;
  if (!draft || state.customSave?.pending || !state.profile?.shared_parameters) return false;
  draft.shared_parameters = cloneJson(state.profile.shared_parameters);
  draft.agents = [1, 2, 3].map(agent => ({ agent, segment: draft.agents.find(item => item.agent === agent)?.segment, parameters: {} }));
  state.customSave = { pending: false, versionId: null, error: null };
  state.configScope = "shared";
  render();
  return true;
}

export function discardCustomDraft(state, render): boolean {
  if (!state.customDraft || state.customSave?.pending) return false;
  state.customDraft = null;
  state.customSave = { pending: false, versionId: null, error: null };
  state.configScope = "shared";
  render();
  return true;
}

export function buildCustomProfilePayload(draft, translate) {
  const definitions = profileEditableDefinitions(draft?.baseProfile);
  const agents = [1, 2, 3].map(agentNumber => {
    const parameters = {};
    const current = draft.agents.find(item => item.agent === agentNumber)?.parameters ?? {};
    definitions.forEach(definition => {
      const nextExists = hasPath(current, definition.parts);
      const next = readPath(current, definition.parts);
      if (nextExists) writePath(parameters, definition.parts, cloneJson(next));
    });
    return { agent: agentNumber, parameters };
  });
  return { display_name: String(draft?.display_name ?? "").trim(), base_version_id: draft?.base_version_id ?? null, shared_parameters: cloneJson(draft?.shared_parameters ?? {}), agents };
}

export async function saveCustomDraft(state, i18n, render) {
  const draft = state.customDraft;
  if (!draft || state.customSave?.pending || !draftIsDirty(draft)) return false;
  const alias = validateCustomDraftName(draft.display_name, i18n.t.bind(i18n));
  if (!alias.valid || !draft.base_version_id) {
    state.customSave = { pending: false, versionId: null, profile: null, error: { title: alias.valid ? i18n.t("config.baseUnavailable") : alias.error, detail: null } };
    render();
    return false;
  }
  draft.display_name = alias.name;
  state.customSave = { pending: true, versionId: null, error: null };
  render();
  try {
    const profile = await state.api.createParameterProfile(buildCustomProfilePayload(draft, i18n.t.bind(i18n)));
    state.customProfile = profile;
    state.customProfiles = [profile, ...(state.customProfiles ?? []).filter(item => item.version_id !== profile.version_id)];
    state.customDraft = null;
    state.configScope = "shared";
    state.customSave = { pending: false, versionId: profile.version_id, profile, error: null };
    state.customRename = { editing: false, pending: false, error: null };
    notify(typeof document === "undefined" ? null : document.querySelector(".toast-region"), i18n.t("config.saved", { version: profile.version_id }));
    return true;
  } catch (error) {
    const message = formatApiError(error, i18n.t.bind(i18n));
    state.customSave = { pending: false, versionId: null, error: message };
    notify(typeof document === "undefined" ? null : document.querySelector(".toast-region"), message.title);
    return false;
  } finally {
    render();
  }
}

export async function renameCustomProfile(state, i18n, name, render) {
  const profile = state.customProfile;
  const displayName = String(name ?? "").trim();
  if (!profile || state.customDraft || state.customRename?.pending) return false;
  if (!displayName) {
    state.customRename = { editing: true, pending: false, error: { title: i18n.t("error.PROFILE_RENAME_INVALID"), detail: "PROFILE_RENAME_INVALID" } };
    render();
    return false;
  }
  state.customRename = { editing: true, pending: true, error: null };
  render();
  try {
    if (typeof state.api.renameParameterProfile !== "function") throw new ApiError("PROFILE_RENAME_UNAVAILABLE", null);
    const renamed = await state.api.renameParameterProfile(profile.version_id, { display_name: displayName });
    if (!renamed || renamed.version_id !== profile.version_id) throw new ApiError("PROFILE_RENAME_UNAVAILABLE", null);
    state.customProfile = renamed;
    state.customProfiles = (state.customProfiles ?? []).map(item => item.version_id === renamed.version_id ? renamed : item);
    state.customRename = { editing: false, pending: false, error: null };
    notify(typeof document === "undefined" ? null : document.querySelector(".toast-region"), i18n.t("config.renameSaved", { name: displayProfileText(renamed.display_name, i18n.t.bind(i18n)) }));
    return true;
  } catch (error) {
    state.customRename = { editing: true, pending: false, error: formatApiError(error, i18n.t.bind(i18n)) };
    return false;
  } finally {
    render();
  }
}

export function buildSimulationPayload(state, t) {
  const profile = state.customProfile ?? state.profile;
  return {
    dataset_id: state.dataset.dataset_id,
    run_mode: profile.mode,
    parameter_profile_version_id: profile.version_id,
    load_mapping_version_id: profile.load_mapping.version_id,
    agent_overrides: [1, 2, 3].map(agent => ({ agent, parameters: {} })),
    seed: 2026,
    display_name: t("run.defaultDisplayName")
  };
}

function localizedErrorNotice(error, i18n) {
  const message = formatApiError(error, i18n.t.bind(i18n));
  return `${message.title}${message.detail ? ` · ${message.detail}` : ""}`;
}

async function startSimulation(state, taskStore, i18n, root) {
  try { const run = await state.api.createSimulation(buildSimulationPayload(state, i18n.t.bind(i18n))); await taskStore.selectRun(run.run_id); notify(root.querySelector(".toast-region"), `${run.run_id} · ${localizedStatus(run.status, i18n.t.bind(i18n))}`); }
  catch (error) { notify(root.querySelector(".toast-region"), localizedErrorNotice(error, i18n)); }
}

async function cancelSimulation(taskStore, i18n, root) {
  try { await taskStore.api.cancelSimulation(taskStore.state.runId); await taskStore.refresh(); notify(root.querySelector(".toast-region"), i18n.t("state.cancelRequested")); }
  catch (error) { notify(root.querySelector(".toast-region"), localizedErrorNotice(error, i18n)); }
}

export async function cancelQueuedSimulation(runId, state, taskStore, i18n, root, render) {
  const queue = state.queue ?? { items: [], cancellingRunIds: [] };
  const item = Array.isArray(queue.items) ? queue.items.find(candidate => candidate?.run_id === runId) : null;
  if (!runId || queueCancelIsPending({ queue }, runId)) return false;
  const pendingRunIds = [...new Set([...(Array.isArray(queue.cancellingRunIds) ? queue.cancellingRunIds : []), runId])];
  state.queue = { ...queue, cancellingRunIds: pendingRunIds, error: null };
  render({ dynamic: true, source: "queue-cancel-pending" });
  try {
    const api = state.api ?? taskStore?.api;
    const selected = taskStore?.state?.runId === runId ? taskStore.state.detail : null;
    const before = typeof api?.getSimulation === "function" ? await api.getSimulation(runId) : selected;
    const run = { ...(item ?? {}), ...(before?.run_id === runId ? before : {}) };
    if (!queueRunIsCancellable(run)) {
      const error = new ApiError("RUN_NOT_CANCELLABLE", null, { recoverable: false, status: 409 });
      state.queue = { ...(state.queue ?? queue), error };
      notify(root.querySelector(".toast-region"), localizedErrorNotice(error, i18n));
      return false;
    }
    const cancelledRun = await api.cancelSimulation(runId);
    const after = typeof api?.getSimulation === "function" ? await api.getSimulation(runId) : cancelledRun;
    if (after && typeof after === "object") {
      const latest = state.queue ?? queue;
      const items = Array.isArray(latest.items) ? latest.items.map(item => item?.run_id === runId ? { ...item, ...after, cancellable: after.cancellable === true } : item) : [];
      state.queue = { ...latest, items };
      render({ dynamic: true, source: "queue-cancel-success" });
    }
    notify(root.querySelector(".toast-region"), i18n.t("queue.cancelled", { run: runId }));
    if (runId === taskStore.state.runId) await taskStore.refresh();
    await loadQueue(state, render, taskStore);
    return true;
  } catch (error) {
    state.queue = { ...(state.queue ?? queue), error };
    notify(root.querySelector(".toast-region"), localizedErrorNotice(error, i18n));
    return false;
  } finally {
    const latest = state.queue ?? queue;
    const remainingRunIds = (Array.isArray(latest.cancellingRunIds) ? latest.cancellingRunIds : []).filter(candidate => candidate !== runId);
    state.queue = { ...latest, cancellingRunIds: remainingRunIds };
    render({ dynamic: true, source: "queue-cancel-result" });
  }
}

function exportProfile(profile) {
  const blob = new Blob([JSON.stringify(profile, null, 2)], { type: "application/json" }); const link = document.createElement("a"); link.href = URL.createObjectURL(blob); link.download = `${profile.version_id}.json`; link.click(); URL.revokeObjectURL(link.href);
}

function notify(region, message) { if (!region) return; const toast = document.createElement("div"); toast.className = "toast"; toast.textContent = message; region.append(toast); setTimeout(() => toast.remove(), 4500); }

function drawChart(root, state, summary, i18n) {
  const canvas = root.querySelector("[data-chart]"); if (!canvas || !summary) return;
  const ctx = canvas.getContext("2d"); const rect = canvas.getBoundingClientRect(); const ratio = window.devicePixelRatio || 1; const width = Math.max(1, Math.floor(rect.width * ratio)); const height = Math.max(1, Math.floor(rect.height * ratio));
  if (canvas.width !== width || canvas.height !== height) { canvas.width = width; canvas.height = height; }
  ctx.setTransform(ratio, 0, 0, ratio, 0, 0); const cssWidth = rect.width; const cssHeight = rect.height; ctx.clearRect(0, 0, cssWidth, cssHeight);
  const all = summary.chart.points; const count = Math.max(12, Math.floor(all.length / state.chart.zoom)); const start = Math.max(0, Math.min(all.length - count, Math.round(state.chart.pan))); const points = all.slice(start, start + count); const margin = { left: 46, right: 18, top: 18, bottom: 30 }; const plotW = cssWidth - margin.left - margin.right; const plotH = cssHeight - margin.top - margin.bottom;
  const values = points.flatMap(point => [point.TrueAverageCurrentSmoothed, point.LocalPrediction, point.GlobalPrediction, point.FusedPrediction, point.FusedLowerBound, point.FusedUpperBound]); const min = Math.min(...values) - .5; const max = Math.max(...values) + .5; const x = index => margin.left + index / Math.max(1, points.length - 1) * plotW; const y = value => margin.top + (max - value) / Math.max(.01, max - min) * plotH;
  const canvasFontSize = Math.max(12, parseFloat(getComputedStyle(document.documentElement).fontSize) * .75);
  ctx.strokeStyle = "rgba(146,173,210,.16)"; ctx.fillStyle = "#74849b"; ctx.font = `${canvasFontSize}px Segoe UI`; for (let i = 0; i < 5; i += 1) { const yy = margin.top + i / 4 * plotH; ctx.beginPath(); ctx.moveTo(margin.left, yy); ctx.lineTo(cssWidth - margin.right, yy); ctx.stroke(); ctx.fillText((max - (max - min) * i / 4).toFixed(1), 3, yy + 4); }
  if (state.chart.series.interval) { ctx.beginPath(); points.forEach((point,index) => index ? ctx.lineTo(x(index), y(point.FusedUpperBound)) : ctx.moveTo(x(index), y(point.FusedUpperBound))); [...points].reverse().forEach((point,index) => ctx.lineTo(x(points.length - 1 - index), y(point.FusedLowerBound))); ctx.closePath(); ctx.fillStyle = "rgba(39,215,231,.11)"; ctx.fill(); }
  const drawLine = (key, color, width, dashed: number[] = []) => { if (!state.chart.series[key]) return; const field = { truth: "TrueAverageCurrentSmoothed", local: "LocalPrediction", global: "GlobalPrediction", fused: "FusedPrediction" }[key]; ctx.beginPath(); points.forEach((point,index) => index ? ctx.lineTo(x(index), y(point[field])) : ctx.moveTo(x(index), y(point[field]))); ctx.strokeStyle = color; ctx.lineWidth = width; ctx.setLineDash(dashed); ctx.stroke(); ctx.setLineDash([]); };
  drawLine("truth", "#eef5ff", 1.25); drawLine("local", "#f06aa6", 1.2, [5,3]); drawLine("global", "#f5b95d", 1.2, [5,3]); drawLine("fused", "#27d7e7", 2.1);
  if (state.chart.focus !== null && state.chart.focus >= start && state.chart.focus < start + points.length) { const index = state.chart.focus - start; ctx.strokeStyle = "rgba(245,185,93,.8)"; ctx.beginPath(); ctx.moveTo(x(index), margin.top); ctx.lineTo(x(index), cssHeight - margin.bottom); ctx.stroke(); }
  ctx.fillStyle = "#74849b"; ctx.fillText(`#${points[0].OriginalRunningIndex}`, margin.left, cssHeight - 8); ctx.fillText(`#${points[points.length - 1].OriginalRunningIndex}`, cssWidth - margin.right - 42, cssHeight - 8);
}

function drawDiagnosticCharts(root, summary, results = null) {
  if (!summary || typeof window === "undefined") return;
  root.querySelectorAll("[data-diagnostic-chart]").forEach((canvas: HTMLCanvasElement) => {
    const kind = canvas.getAttribute("data-diagnostic-chart");
    const diagnostic = workspaceDiagnosticSeries(summary, results, kind === "fusion" ? "fusion" : "recent");
    const series = diagnostic.series.map(item => item.values);
    if (!series.some(values => values.length > 1)) return;
    const rect = canvas.getBoundingClientRect();
    if (rect.width < 2 || rect.height < 2) return;
    const ratio = window.devicePixelRatio || 1;
    const width = Math.floor(rect.width * ratio); const height = Math.floor(rect.height * ratio);
    if (canvas.width !== width || canvas.height !== height) { canvas.width = width; canvas.height = height; }
    const context = canvas.getContext("2d"); if (!context) return;
    context.setTransform(ratio, 0, 0, ratio, 0, 0); context.clearRect(0, 0, rect.width, rect.height);
    const combined = series.flat().filter((value): value is number => Number.isFinite(value)); const lower = Math.min(...combined); const upper = Math.max(...combined); const span = Math.max(.0001, upper - lower);
    const margin = { left: 10, right: 10, top: 10, bottom: 12 }; const plotWidth = rect.width - margin.left - margin.right; const plotHeight = rect.height - margin.top - margin.bottom;
    context.strokeStyle = "rgba(146,173,210,.16)"; context.lineWidth = 1;
    [0, .5, 1].forEach(ratioY => { const y = margin.top + plotHeight * ratioY; context.beginPath(); context.moveTo(margin.left, y); context.lineTo(rect.width - margin.right, y); context.stroke(); });
    const colors = kind === "fusion" ? ["#27d7e7", "#f5b95d"] : ["#f06aa6", "#f5b95d", "#27d7e7"];
    colors.forEach((color, index) => {
      const values = series[index] ?? []; if (values.length < 2) return;
      context.beginPath(); values.forEach((value, pointIndex) => { const x = margin.left + pointIndex / Math.max(1, values.length - 1) * plotWidth; const y = margin.top + (upper - value) / span * plotHeight; if (pointIndex) context.lineTo(x, y); else context.moveTo(x, y); });
      context.strokeStyle = color; context.lineWidth = index ? 1.35 : 1.75; context.setLineDash(index ? [4, 3] : []); context.stroke(); context.setLineDash([]);
    });
  });
}

function bindChartPointer(root, state, summary, i18n, results = null, alarms = null, runId = "") {
  const canvas = root.querySelector("[data-chart]"); const tooltip = root.querySelector("[data-tooltip]"); if (!canvas || !summary || !tooltip) return;
  const redraw = () => drawChart(root, state, summary, i18n);
  const synchronizeDiagnostics = () => patchWorkspaceDiagnosticSelection(root, summary, results, alarms, state, i18n.t.bind(i18n), runId);
  canvas.addEventListener("wheel", event => { event.preventDefault(); state.chart.zoom = Math.max(1, Math.min(8, state.chart.zoom * (event.deltaY < 0 ? 1.22 : .82))); redraw(); }, { passive: false });
  canvas.addEventListener("pointerdown", event => { state.chart.dragging = { x: event.clientX, pan: state.chart.pan }; canvas.setPointerCapture(event.pointerId); });
  canvas.addEventListener("pointermove", event => { const rect = canvas.getBoundingClientRect(); const count = Math.max(12, Math.floor(summary.chart.points.length / state.chart.zoom)); if (state.chart.dragging) { state.chart.pan = Math.max(0, Math.min(summary.chart.points.length - count, state.chart.dragging.pan - (event.clientX - state.chart.dragging.x) / rect.width * count)); redraw(); return; } const position = Math.max(0, Math.min(count - 1, Math.round((event.clientX - rect.left - 46) / Math.max(1, rect.width - 64) * (count - 1)))); const start = Math.round(state.chart.pan); const point = summary.chart.points[start + position]; if (!point) return; state.chart.focus = start + position; synchronizeDiagnostics(); tooltip.hidden = false; tooltip.style.left = `${Math.min(rect.width - 245, Math.max(8, event.clientX - rect.left + 12))}px`; tooltip.style.top = `${Math.min(rect.height - 165, Math.max(8, event.clientY - rect.top - 28))}px`; tooltip.innerHTML = `<div><b>#${point.OriginalRunningIndex}</b><span>${point.Time}</span></div><div><span>${i18n.t("legend.truth")}</span><b>${point.TrueAverageCurrentSmoothed.toFixed(2)} A</b></div><div><span>${i18n.t("legend.local")} / ${i18n.t("legend.global")}</span><b>${point.LocalPrediction.toFixed(2)} / ${point.GlobalPrediction.toFixed(2)}</b></div><div><span>${i18n.t("legend.fused")}</span><b>${point.FusedPrediction.toFixed(2)} A</b></div><div><span>${i18n.t("legend.interval")}</span><b>${point.FusedLowerBound.toFixed(2)} — ${point.FusedUpperBound.toFixed(2)}</b></div><div><span>${i18n.t("tooltip.weightSupport")}</span><b>${point.FusionAlpha.toFixed(2)} / ${point.GlobalSupport.toFixed(2)}</b></div><div><span>${i18n.t("tooltip.loadAlarm")}</span><b>${point.LoadStatus} / ${point.OverallAlarmLevel}</b></div>`; redraw(); });
  canvas.addEventListener("keydown", event => { if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return; const pointCount = Array.isArray(summary?.chart?.points) ? summary.chart.points.length : 0; if (!pointCount) return; event.preventDefault(); const current = Number.isInteger(state.chart.focus) ? state.chart.focus : -1; if (event.key === "Home") state.chart.focus = 0; else if (event.key === "End") state.chart.focus = pointCount - 1; else if (event.key === "ArrowLeft") state.chart.focus = Math.max(0, current < 0 ? 0 : current - 1); else state.chart.focus = Math.min(pointCount - 1, current < 0 ? 0 : current + 1); tooltip.hidden = true; synchronizeDiagnostics(); redraw(); });
  const finish = event => { state.chart.dragging = null; if (canvas.hasPointerCapture(event.pointerId)) canvas.releasePointerCapture(event.pointerId); }; canvas.addEventListener("pointerup", finish); canvas.addEventListener("pointercancel", finish); canvas.addEventListener("pointerleave", () => { if (!state.chart.dragging) { state.chart.focus = null; tooltip.hidden = true; synchronizeDiagnostics(); redraw(); } });
}
