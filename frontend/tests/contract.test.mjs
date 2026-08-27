import assert from "assert";
import { readFile } from "node:fs/promises";

if (!globalThis.AbortController) {
  globalThis.AbortController = class {
    constructor() { this.signal = { aborted: false }; }
    abort() { this.signal.aborted = true; }
  };
}

if (!globalThis.structuredClone) {
  globalThis.structuredClone = value => JSON.parse(JSON.stringify(value));
}

const verificationDist = process.env.FRONTEND_VERIFY_DIST ?? "dist";
const verificationModule = relativePath => new URL(`../${verificationDist}/${relativePath}`, import.meta.url);

const contract = await import(verificationModule("api/contract.js"));
const { SimulationStore } = await import(verificationModule("state/simulation-store.js"));
const { DATASET_POLL_INTERVAL_MS, DatasetPoller } = await import(verificationModule("state/dataset-poller.js"));
const { LivePlatformApi } = await import(verificationModule("api/live-api.js"));
const { fixtureAlarms, fixtureCompletedSimulation, fixtureDatasetValid, fixtureDatasetValidating, fixtureLiveRunDetail, fixtureReferenceProfile, fixtureSummary } = await import(verificationModule("testing/contract-fixtures.js"));
const { createI18n, languages } = await import(verificationModule("i18n.js"));
const { advanceReplayPlayback, beginCustomDraft, bindAlarmDialogInteractions, bindManifestDialogInteractions, buildCustomProfilePayload, buildSimulationPayload, cancelQueuedSimulation, captureRenderContext, configView, dataStatisticsSection, dataValidationReport, dataView, datasetPreflightPanel, datasetPreprocessing, datasetPreprocessingCounts, discardCustomDraft, displayProfileText, displayProfileVersion, draftIsDirty, filteredHistoryItems, historyArtifactGate, historyView, loadHistory, loadQueue, localizedStatus, openHistoryDeepLink, parameterLeafLabel, patchDynamicApplication, preflightFailureDisplay, preflightPresentation, preserveConfigDynamicState, queueActiveRun, queueRunIsCancellable, queueView, queueWaitingCount, readinessDisplay, renameCustomProfile, renderAlarmDetailDialog, renderDraftParameterFields, renderFilePicker, renderFrozenParameterProfileSnapshot, renderHashValue, renderManifestDialog, renderParameterReadout, renderProfileParameterFields, renderSelectedAgentResults, replayChartSummary, replayKeyboardPosition, replayView, resetCustomDraft, restoreRenderContext, restoreCustomDraftDefaults, runSnapshotDisplay, saveCustomDraft, selectCustomProfile, selectHistoryRun, setReplayPlaybackPosition, synchronizeQueueProjection, synchronizeQueueWaitingCount, systemStrip, updateDraftValue, updateHistoryFilters, workspaceDiagnosticSelection, workspaceDiagnosticSeries, workspaceResultFilesPresentation, workspaceView } = await import(verificationModule("ui.js"));

async function test(name, callback) {
  try { await callback(); console.log(`PASS ${name}`); }
  catch (error) { console.error(`FAIL ${name}`); throw error; }
}

await test("S1 CSV contract retains the exact frozen seven-column order", () => {
  assert.deepEqual(contract.DATASET_COLUMNS, ["Time_base", "dzdl_1", "dzdl_2", "dzdl_3", "dzdl_4", "zl", "sd"]);
});

await test("S1 Agent collection accepts exactly Agent 1, Agent 2, Agent 3 once", () => {
  assert.equal(contract.validateAgentCollection([1, 2, 3]), true);
  assert.equal(contract.validateAgentCollection([3, 1, 2]), true);
  assert.equal(contract.validateAgentCollection([1, 2]), false);
  assert.equal(contract.validateAgentCollection([1, 2, 2]), false);
  assert.equal(contract.validateAgentCollection([1, 2, 3, 4]), false);
});

await test("terminal states cannot be confused with a running task", () => {
  assert.equal(contract.isTerminalStatus("COMPLETED"), true);
  assert.equal(contract.isTerminalStatus("CANCELLED"), true);
  assert.equal(contract.isTerminalStatus("FAILED_RECOVERABLE"), true);
  assert.equal(contract.isTerminalStatus("GENERATING_ARTIFACTS"), false);
});

await test("selected Agent drives the summary query, metrics, and result title", async () => {
  const requestedAgents = [];
  const api = {
    getSimulation: async () => ({ status: "COMPLETED", latest_event_id: 1 }),
    getSummary: async (_runId, agent) => {
      requestedAgents.push(agent);
      return { selection: { agent, segment: ["EARLY", "MIDDLE", "LATE"][agent - 1] }, metrics: { RMSE: .842, MAE: .618, R2: .936, Coverage: .954, MeanBandwidth: 3.18, MeanOnlineGlobalWeight: .64, NegativeTransferRate: .018 } };
    },
    subscribeSimulationEvents: () => ({ close() {} })
  };
  const store = new SimulationStore(api);
  await store.selectRun("run_contract_agent");
  await store.selectAgent(2);
  assert.equal(requestedAgents[requestedAgents.length - 1], 2);
  assert.equal(store.state.summary.selection.agent, 2);
  const translate = (key, values = {}) => ({
    "workspace.metrics": "Current-agent test metrics",
    "workspace.resultTitle": `Frozen results · Agent ${values.agent}`,
    "workspace.agent": `Agent ${values.agent}`,
    "metric.rmse": "Fused RMSE", "metric.mae": "Fused MAE", "metric.r2": "R²", "metric.coverage": "Interval coverage", "metric.bandwidth": "Mean bandwidth", "metric.weight": "Mean global weight", "metric.negative": "Negative transfer rate"
  }[key] ?? key);
  const markup = renderSelectedAgentResults(store.state.summary, translate);
  assert.match(markup, /Frozen results · Agent 2/);
  assert.match(markup, /<small>Agent 2<\/small>/);
  assert.doesNotMatch(markup, /Agent 1/);
});

await test("readiness endpoint maps live Worker observation without fabricating Ready", async () => {
  const originalFetch = globalThis.fetch;
  const originalHeaders = globalThis.Headers;
  const requestedUrls = [];
  globalThis.Headers = class {
    constructor(values = {}) { this.values = { ...values }; }
    set(name, value) { this.values[name] = value; }
  };
  globalThis.fetch = async (url) => {
    requestedUrls.push(url);
    return { ok: true, status: 200, headers: { get: () => "req_readiness" }, json: async () => ({ status: "ready", checks: { worker: "not_observed", database: "ok" } }) };
  };
  try {
    const liveApi = new LivePlatformApi("/api/v1");
    const payload = await liveApi.getReadiness();
    assert.equal(requestedUrls[0], "/api/v1/health/ready");
    assert.equal(payload.checks.worker, "not_observed");
    const store = new SimulationStore({ getReadiness: async () => payload });
    await store.refreshReadiness();
    const translate = key => ({ "health.checking": "Checking", "health.ready": "Ready", "health.notObserved": "Not observed", "health.warning": "Warning", "health.unavailable": "Unavailable" }[key] ?? key);
    assert.deepEqual(readinessDisplay(store.state, "web", translate), { key: "READY", label: "Ready" });
    assert.deepEqual(readinessDisplay(store.state, "worker", translate), { key: "NOT_OBSERVED", label: "Not observed" });
    assert.deepEqual(readinessDisplay(store.state, "database", translate), { key: "READY", label: "Ready" });
    assert.deepEqual(readinessDisplay({ readiness: null, readinessLoading: true, readinessError: null }, "worker", translate), { key: "CHECKING", label: "Checking" });
    assert.deepEqual(readinessDisplay({ readiness: null, readinessLoading: false, readinessError: new Error("offline") }, "worker", translate), { key: "UNAVAILABLE", label: "Unavailable" });
  } finally {
    if (originalFetch === undefined) delete globalThis.fetch; else globalThis.fetch = originalFetch;
    if (originalHeaders === undefined) delete globalThis.Headers; else globalThis.Headers = originalHeaders;
  }
});

await test("the live adapter uses only run-scoped replay, result, alarm, and artifact endpoints", async () => {
  const originalFetch = globalThis.fetch;
  const originalHeaders = globalThis.Headers;
  const requestedUrls = [];
  globalThis.Headers = class {
    constructor(values = {}) { this.values = { ...values }; }
    set(name, value) { this.values[name] = value; }
  };
  globalThis.fetch = async url => {
    requestedUrls.push(url);
    return { ok: true, status: 200, headers: { get: () => "req_replay" }, json: async () => ({ data: { run_id: "run/frozen" } }) };
  };
  try {
    const api = new LivePlatformApi("/api/v1");
    await api.getReplay("run/frozen", { agent: 2, limit: 50 });
    await api.getResults("run/frozen", { agent: 2, cursor: "page two" });
    await api.getAlarms("run/frozen", { agent: 2 });
    await api.getArtifacts("run/frozen");
    assert.deepEqual(requestedUrls, [
      "/api/v1/simulations/run%2Ffrozen/replay?agent=2&limit=50",
      "/api/v1/simulations/run%2Ffrozen/results?agent=2&cursor=page+two",
      "/api/v1/simulations/run%2Ffrozen/alarms?agent=2",
      "/api/v1/simulations/run%2Ffrozen/artifacts"
    ]);
    assert.equal(api.replayExportUrl("run/frozen", 2), "/api/v1/simulations/run%2Ffrozen/replay-export?agent=2&format=zip");
    assert.equal(api.artifactDownloadUrl("run/frozen", "artifact manifest.json"), "/api/v1/simulations/run%2Ffrozen/artifacts/artifact%20manifest.json");
  } finally {
    if (originalFetch === undefined) delete globalThis.fetch; else globalThis.fetch = originalFetch;
    if (originalHeaders === undefined) delete globalThis.Headers; else globalThis.Headers = originalHeaders;
  }
});

await test("the live simulation list preserves cursor metadata instead of discarding the transport envelope", async () => {
  const originalFetch = globalThis.fetch;
  const originalHeaders = globalThis.Headers;
  let requestedUrl = null;
  globalThis.Headers = class {
    constructor(values = {}) { this.values = { ...values }; }
    set(name, value) { this.values[name] = value; }
  };
  globalThis.fetch = async url => {
    requestedUrl = url;
    return { ok: true, status: 200, headers: { get: () => "req_page" }, json: async () => ({ data: [{ run_id: "run_page_1" }], meta: { request_id: "req_page", next_cursor: "cursor_page_2", has_more: true, total: 203 } }) };
  };
  try {
    const api = new LivePlatformApi("/api/v1");
    const page = await api.listSimulations({ view: "history", search: "frozen dataset", status: "COMPLETED", run_mode: "REFERENCE", cursor: "cursor_page_1", limit: 100 });
    assert.equal(requestedUrl, "/api/v1/simulations?view=history&search=frozen+dataset&status=COMPLETED&run_mode=REFERENCE&cursor=cursor_page_1&limit=100");
    assert.deepEqual(page.items, [{ run_id: "run_page_1" }]);
    assert.equal(page.next_cursor, "cursor_page_2");
    assert.equal(page.has_more, true);
    assert.equal(page.total, 203);
    assert.equal(page.request_id, "req_page");
    assert.equal(page.meta.request_id, "req_page");
  } finally {
    if (originalFetch === undefined) delete globalThis.fetch; else globalThis.fetch = originalFetch;
    if (originalHeaders === undefined) delete globalThis.Headers; else globalThis.Headers = originalHeaders;
  }
});

await test("the live adapter sends a narrow PATCH for a Custom display-name rename", async () => {
  const originalFetch = globalThis.fetch;
  const originalHeaders = globalThis.Headers;
  let request;
  globalThis.Headers = class {
    constructor(values = {}) { this.values = { ...values }; }
    set(name, value) { this.values[name] = value; }
  };
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return { ok: true, status: 200, headers: { get: () => "req_rename" }, json: async () => ({ data: { version_id: "pp_fixture", display_name: "Renamed", normalized_sha256: "hash" } }) };
  };
  try {
    const api = new LivePlatformApi("/api/v1");
    const response = await api.renameParameterProfile("pp_fixture", { display_name: "Renamed" });
    assert.equal(request.url, "/api/v1/parameter-profiles/pp_fixture");
    assert.equal(request.options.method, "PATCH");
    assert.deepEqual(JSON.parse(request.options.body), { display_name: "Renamed" });
    assert.equal(response.version_id, "pp_fixture");
  } finally {
    if (originalFetch === undefined) delete globalThis.fetch; else globalThis.fetch = originalFetch;
    if (originalHeaders === undefined) delete globalThis.Headers; else globalThis.Headers = originalHeaders;
  }
});

await test("the dataset multipart adapter sends display_name before the file part", async () => {
  const originalFetch = globalThis.fetch;
  const originalHeaders = globalThis.Headers;
  const originalFormData = globalThis.FormData;
  let request;
  globalThis.Headers = class {
    constructor(values = {}) { this.values = { ...values }; }
    set(name, value) { this.values[name] = value; }
  };
  globalThis.FormData = class {
    constructor() { this.parts = []; }
    append(name, value) { this.parts.push([name, value]); }
  };
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return { ok: true, status: 201, headers: { get: () => "req_upload" }, json: async () => ({ data: { dataset_id: "ds_upload" } }) };
  };
  try {
    const api = new LivePlatformApi("/api/v1");
    const file = { name: "source.csv" };
    const progress = [];
    await api.uploadDataset(file, "Friendly source", value => progress.push(value));
    assert.equal(request.url, "/api/v1/datasets");
    assert.deepEqual(request.options.body.parts, [["display_name", "Friendly source"], ["file", file]]);
    assert.deepEqual(progress, [0, 100]);
  } finally {
    if (originalFetch === undefined) delete globalThis.fetch; else globalThis.fetch = originalFetch;
    if (originalHeaders === undefined) delete globalThis.Headers; else globalThis.Headers = originalHeaders;
    if (originalFormData === undefined) delete globalThis.FormData; else globalThis.FormData = originalFormData;
  }
});

await test("the live adapter omits browser-generated request IDs and retains the authoritative backend request ID", async () => {
  const originalFetch = globalThis.fetch;
  const originalHeaders = globalThis.Headers;
  let request;
  globalThis.Headers = class {
    constructor(values = {}) { this.values = { ...values }; }
    set(name, value) { this.values[name] = value; }
  };
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return { ok: false, status: 422, headers: { get: () => "req_header_authority" }, json: async () => ({ error: { code: "CSV_HEADER_MISMATCH", message: "ignored by localized UI", recoverable: true }, request_id: "req_body_authority" }) };
  };
  try {
    const api = new LivePlatformApi("/api/v1");
    await assert.rejects(() => api.getDataset("ds_missing"), error => {
      assert.equal(error.code, "CSV_HEADER_MISMATCH");
      assert.equal(error.requestId, "req_body_authority");
      return true;
    });
    assert.equal(request.url, "/api/v1/datasets/ds_missing");
    assert.equal(request.options.headers.values["X-Request-ID"], undefined);
    const english = createI18n("en");
    const chinese = createI18n("zh-CN");
    const error = new contract.ApiError("CSV_HEADER_MISMATCH", "req_body_authority", { recoverable: true });
    const englishContext = dataValidationReport(null, { error }, english.t.bind(english));
    const chineseContext = dataValidationReport(null, { error }, chinese.t.bind(chinese));
    assert.match(englishContext, /id="data-upload-error-context"/);
    assert.doesNotMatch(englishContext, /role="alert"|aria-live="assertive"/);
    assert.match(englishContext, /CSV_HEADER_MISMATCH · req_body_authority/);
    assert.match(chineseContext, /CSV_HEADER_MISMATCH · req_body_authority/);
    assert.doesNotMatch(chineseContext, /ignored by localized UI/);
    [english, chinese].forEach(i18n => {
      const uploadError = dataView({ dataset: null, upload: { percent: null, error, fileName: "bad-header.csv" } }, i18n);
      assert.equal((uploadError.match(/role="alert" aria-live="assertive"/g) ?? []).length, 1);
      assert.match(uploadError, /id="data-upload-authoritative-error"/);
      assert.match(uploadError, /id="data-upload-error-context"/);
      assert.match(uploadError, /aria-describedby="file-selection data-upload-authoritative-error"/);
      assert.ok(uploadError.includes(i18n.t("error.CSV_HEADER_MISMATCH")));
      assert.equal((uploadError.match(/CSV_HEADER_MISMATCH · req_body_authority/g) ?? []).length, 2);
    });
  } finally {
    if (originalFetch === undefined) delete globalThis.fetch; else globalThis.fetch = originalFetch;
    if (originalHeaders === undefined) delete globalThis.Headers; else globalThis.Headers = originalHeaders;
  }
});

await test("readiness refresh repeats without overlap and clears stale Ready after failure", async () => {
  const originalSetInterval = globalThis.setInterval;
  const originalClearInterval = globalThis.clearInterval;
  let scheduledCallback;
  let scheduledDelay;
  let clearedTimer;
  let calls = 0;
  globalThis.setInterval = (callback, delay) => { scheduledCallback = callback; scheduledDelay = delay; return 73; };
  globalThis.clearInterval = timer => { clearedTimer = timer; };
  try {
    const store = new SimulationStore({ getReadiness: async () => {
      calls += 1;
      if (calls === 1) return { status: "ready", checks: { worker: "ok", database: "ok" } };
      throw new Error("readiness unavailable");
    } });
    store.startReadinessRefresh();
    await Promise.resolve(); await Promise.resolve();
    assert.equal(scheduledDelay, 10000);
    assert.equal(calls, 1);
    await scheduledCallback();
    await Promise.resolve(); await Promise.resolve();
    assert.equal(calls, 2);
    assert.equal(store.state.readiness, null);
    assert.equal(store.state.readinessError.message, "readiness unavailable");
    const translate = key => ({ "health.checking": "Checking", "health.ready": "Ready", "health.notObserved": "Not observed", "health.warning": "Warning", "health.unavailable": "Unavailable" }[key] ?? key);
    assert.deepEqual(readinessDisplay(store.state, "worker", translate), { key: "UNAVAILABLE", label: "Unavailable" });
    store.close();
    assert.equal(clearedTimer, 73);
  } finally {
    globalThis.setInterval = originalSetInterval;
    globalThis.clearInterval = originalClearInterval;
  }
});

await test("store close aborts a pending readiness request", () => {
  let readinessSignal;
  const store = new SimulationStore({ getReadiness: signal => {
    readinessSignal = signal;
    return new Promise(() => {});
  } });
  store.startReadinessRefresh();
  assert.equal(readinessSignal.aborted, false);
  store.close();
  assert.equal(readinessSignal.aborted, true);
});

await test("dataset polling retries validating preflight on a fixed timer and stops at VALID", async () => {
  const originalSetTimeout = globalThis.setTimeout;
  const originalClearTimeout = globalThis.clearTimeout;
  let scheduledCallback;
  let scheduledDelay;
  let calls = 0;
  const updates = [];
  globalThis.setTimeout = (callback, delay) => { scheduledCallback = callback; scheduledDelay = delay; return 91; };
  globalThis.clearTimeout = () => {};
  try {
    const poller = new DatasetPoller({ getDataset: async () => {
      calls += 1;
      return calls === 1 ? { dataset_id: "ds_poll", status: "VALIDATING", preflight: { status: "RUNNING" } } : { dataset_id: "ds_poll", status: "VALID", preflight: { status: "VALID" } };
    } }, { onDataset: dataset => updates.push(dataset.status), onError: error => { throw error; } });
    poller.watch({ dataset_id: "ds_poll", status: "VALIDATING", preflight: { status: "QUEUED" } });
    await Promise.resolve(); await Promise.resolve();
    assert.equal(calls, 1);
    assert.equal(scheduledDelay, DATASET_POLL_INTERVAL_MS);
    scheduledCallback();
    await Promise.resolve(); await Promise.resolve();
    assert.equal(calls, 2);
    assert.deepEqual(updates, ["VALIDATING", "VALID"]);
    assert.equal(poller.timer, null);
  } finally {
    globalThis.setTimeout = originalSetTimeout;
    globalThis.clearTimeout = originalClearTimeout;
  }
});

await test("dataset polling ignores stale responses after a dataset switch", async () => {
  const pending = new Map();
  const updates = [];
  const poller = new DatasetPoller({ getDataset: (datasetId, signal) => new Promise(resolve => { pending.set(datasetId, { resolve, signal }); }) }, { onDataset: dataset => updates.push(dataset.dataset_id), onError: () => {} });
  poller.watch({ dataset_id: "ds_old", status: "VALIDATING", preflight: { status: "QUEUED" } });
  poller.watch({ dataset_id: "ds_new", status: "VALIDATING", preflight: { status: "QUEUED" } });
  assert.equal(pending.get("ds_old").signal.aborted, true);
  pending.get("ds_old").resolve({ dataset_id: "ds_old", status: "VALID", preflight: { status: "VALID" } });
  pending.get("ds_new").resolve({ dataset_id: "ds_new", status: "VALID", preflight: { status: "VALID" } });
  await Promise.resolve(); await Promise.resolve();
  assert.deepEqual(updates, ["ds_new"]);
  poller.close();
});

await test("dataset polling reports a temporary error without fabricating VALID and aborts on close", async () => {
  const originalSetTimeout = globalThis.setTimeout;
  const originalClearTimeout = globalThis.clearTimeout;
  let scheduledCallback;
  let signal;
  const errors = [];
  let calls = 0;
  globalThis.setTimeout = callback => { scheduledCallback = callback; return 92; };
  globalThis.clearTimeout = () => {};
  try {
    const poller = new DatasetPoller({ getDataset: (_datasetId, requestSignal) => {
      calls += 1;
      signal = requestSignal;
      if (calls === 1) return Promise.reject(new Error("temporary network error"));
      return new Promise(() => {});
    } }, { onDataset: () => { throw new Error("network failure must not fabricate VALID"); }, onError: (_datasetId, error) => errors.push(error.message) });
    poller.watch({ dataset_id: "ds_network", status: "VALIDATING", preflight: { status: "RUNNING" } });
    await Promise.resolve(); await Promise.resolve();
    assert.deepEqual(errors, ["temporary network error"]);
    scheduledCallback();
    await Promise.resolve();
    poller.close();
    assert.equal(signal.aborted, true);
  } finally {
    globalThis.setTimeout = originalSetTimeout;
    globalThis.clearTimeout = originalClearTimeout;
  }
});

await test("the actual nested reference profile renders every group and leaf without object coercion", () => {
  const expectedGroups = ["feature_state", "cleaning", "split", "local_gp", "trend", "interval", "anchors", "support", "global_surrogate", "fusion", "alarms"];
  assert.deepEqual(Object.keys(fixtureReferenceProfile.shared_parameters), expectedGroups);
  assert.equal(fixtureReferenceProfile.editable_paths.length, 67);
  assert.equal(fixtureReferenceProfile.constraints.paths["split.agent_count"].editable, false);
  assert.equal(fixtureReferenceProfile.constraints.paths["global_surrogate.leave_one_out"].editable, false);
  const i18n = createI18n("en");
  const readout = renderParameterReadout(fixtureReferenceProfile, i18n.t.bind(i18n));
  const fields = renderProfileParameterFields(fixtureReferenceProfile, i18n.t.bind(i18n));
  expectedGroups.forEach(group => {
    assert.match(readout, new RegExp(`<code>${group}</code>`));
    assert.match(fields, new RegExp(`id="parameter-group-${group}"`));
  });
  assert.match(readout, /calibration_scale_min/);
  assert.match(fields, /global_clear_threshold/);
  assert.match(fields, /leave_one_out/);
  assert.match(fields, /value="—"/);
  assert.doesNotMatch(readout, /\[object Object\]/);
  assert.doesNotMatch(fields, /\[object Object\]/);
  assert.ok((fields.match(/disabled/g) ?? []).length > 40);
});

await test("frozen parameter leaves preserve small authoritative values instead of rounding them to zero", () => {
  const profile = structuredClone(fixtureReferenceProfile);
  profile.shared_parameters.global_surrogate.minimum_regularization = 0.0001;
  profile.shared_parameters.interval.variance_floor = 1e-8;
  profile.shared_parameters.support.minimum_weight = 1e-5;
  const english = createI18n("en");
  const chinese = createI18n("zh-CN");
  [renderParameterReadout(profile, english.t.bind(english)), renderProfileParameterFields(profile, english.t.bind(english)), renderParameterReadout(profile, chinese.t.bind(chinese))].forEach(markup => {
    ["0.0001", "1e-8", "0.00001"].forEach(value => assert.match(markup, new RegExp(value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"))));
  });
});

await test("Workspace frozen rail preserves small parameter values from the authoritative terminal snapshot", () => {
  const detail = structuredClone(fixtureCompletedSimulation);
  detail.parameter_snapshot = structuredClone(detail.snapshot.parameter_profile);
  detail.parameter_snapshot.shared_parameters.global_surrogate.minimum_regularization = 0.0001;
  detail.parameter_snapshot.shared_parameters.interval.variance_floor = 1e-8;
  detail.parameter_snapshot.shared_parameters.support.minimum_weight = 1e-5;
  const task = { detail, runId: detail.run_id, summary: structuredClone(fixtureSummary), summaryRunId: detail.run_id, results: { run_id: detail.run_id, items: [] }, resultsRunId: detail.run_id, alarms: { run_id: detail.run_id, items: [{ ...fixtureAlarms[0], alarm_type: "OVERALL" }] }, alarmsRunId: detail.run_id, artifacts: { run_id: detail.run_id, artifact_state: "COMMITTED", items: [] }, artifactsRunId: detail.run_id, selectedAgent: 1, loading: false, error: null, lastEventId: 209, events: [] };
  const english = workspaceView({ chart: { series: {} } }, task, createI18n("en"));
  const chinese = workspaceView({ chart: { series: {} } }, task, createI18n("zh-CN"));
  [english, chinese].forEach(markup => ["0.0001", "1e-8", "0.00001"].forEach(value => assert.match(markup, new RegExp(value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")))));
  assert.match(english, /Overall alarm <code>OVERALL<\/code>/);
  assert.match(chinese, /整体告警 <code>OVERALL<\/code>/);
  assert.match(english, /View result file manifest and SHA-256/);
  assert.match(chinese, /查看结果文件清单与 SHA-256/);
});

await test("parameter configuration localizes friendly labels while retaining frozen technical keys", () => {
  const state = { profile: fixtureReferenceProfile, customProfile: null, customDraft: null, customSave: { pending: false, versionId: null, error: null } };
  const english = createI18n("en");
  const englishView = configView(state, english);
  assert.match(englishView, /Reference and Custom parameter profiles/);
  assert.match(englishView, /<span>Reference-compatible<\/span><small>REFERENCE<\/small>/);
  assert.match(englishView, /<span>Custom<\/span><small>CUSTOM<\/small>/);
  assert.match(englishView, /Reference-compatible/);
  assert.match(englishView, /Early<\/small>/);
  assert.match(englishView, /<span class="field-label">Lag order<\/span><code class="field-path">feature_state\.nLag<\/code><span class="field-control">/);
  assert.match(englishView, /<span class="field-label">Absolute current threshold<\/span><code class="field-path">alarms\.absolute_current_threshold<\/code><span class="field-control"><input value="—"/);
  assert.doesNotMatch(englishView, /\[object Object\]/);
  assert.match(englishView, /Reference-compatible/);

  const chinese = createI18n("zh-CN");
  const chineseView = configView(state, chinese);
  assert.match(chineseView, /参考与自定义参数方案/);
  assert.match(chineseView, /<span>参考兼容<\/span><small>REFERENCE<\/small>/);
  assert.match(chineseView, /<span>自定义<\/span><small>CUSTOM<\/small>/);
  assert.match(chineseView, /参考兼容/);
  assert.match(chineseView, /早期<\/small>/);
  assert.match(chineseView, /<span class="field-label">滞后阶数<\/span><code class="field-path">feature_state\.nLag<\/code><span class="field-control">/);
  assert.match(chineseView, /<span class="state VALID profile-readonly">只读<\/span>/);
  assert.doesNotMatch(chineseView, /参考兼容配置/);
  assert.doesNotMatch(chineseView, /<span>CUSTOM<\/span>/);
});

await test("Custom drafts render only declared editable paths with shared and Agent-specific typed controls", () => {
  const state = { profile: structuredClone(fixtureReferenceProfile), customProfile: null, customDraft: null, customSave: { pending: false, versionId: null, error: null }, configScope: "shared" };
  assert.equal(beginCustomDraft(state, () => {}), true);
  assert.equal(draftIsDirty(state.customDraft), false);
  const i18n = createI18n("en");
  const shared = configView(state, i18n);
  assert.match(shared, /<span>Custom<\/span><small>CUSTOM<\/small>/);
  assert.match(shared, /data-draft-input data-draft-scope="shared" data-draft-path="feature_state\.nLag" data-value-type="integer"/);
  assert.match(shared, /data-draft-path="feature_state\.nLag"[^>]*min="1" max="128" step="1"/);
  assert.match(shared, /data-draft-path="interval\.update_mode"[^>]*><option value="all_finite" selected>/);
  assert.doesNotMatch(shared, /data-draft-path="alarms\.absolute_current_threshold"[^>]*\s(?:min|max)=/);
  assert.equal((shared.match(/data-draft-input data-draft-scope="shared"/g) ?? []).length, 67);
  assert.match(shared, /split\.agent_count[^]*?S1 hard constraint \(read-only\)[^]*?<input value="3" title="3" disabled>/);
  assert.match(shared, /global_surrogate\.leave_one_out[^]*?S1 hard constraint \(read-only\)[^]*?<input type="checkbox" checked disabled>/);
  assert.equal(updateDraftValue(state, { dataset: { draftPath: "split.agent_count", draftScope: "shared", valueType: "integer" }, value: "4" }), false);
  state.customDraft.baseProfile.editable_paths.push("shared_parameters.unknown.path");
  assert.doesNotMatch(renderDraftParameterFields(state.customDraft, "shared", i18n.t.bind(i18n)), /unknown\.path/);
  assert.equal(updateDraftValue(state, { dataset: { draftPath: "feature_state.nLag", draftScope: "shared", valueType: "integer" }, value: "12" }), true);
  assert.equal(updateDraftValue(state, { dataset: { draftPath: "feature_state.nLag", draftScope: "agent", agent: "2", valueType: "integer" }, value: "16" }), true);
  assert.equal(draftIsDirty(state.customDraft), true);
  const payload = buildCustomProfilePayload(state.customDraft, i18n.t.bind(i18n));
  assert.equal(payload.shared_parameters.feature_state.nLag, 12);
  assert.equal(payload.agents.find(agent => agent.agent === 2).parameters.feature_state.nLag, 16);
  assert.deepEqual(payload.agents.find(agent => agent.agent === 1).parameters, {});
  state.configScope = "agent-2";
  const agent = configView(state, i18n);
  assert.match(agent, /data-draft-scope="agent" data-draft-path="feature_state\.nLag" data-value-type="integer" data-agent="2"/);
  assert.equal((agent.match(/data-draft-input data-draft-scope="agent"/g) ?? []).length, 67);
  assert.match(agent, /Inherit shared value: 12/);
  assert.equal(resetCustomDraft(state, () => {}), true);
  assert.equal(draftIsDirty(state.customDraft), false);
  assert.equal(state.customDraft.shared_parameters.feature_state.nLag, 8);
  assert.equal(updateDraftValue(state, { dataset: { draftPath: "feature_state.nLag", draftScope: "shared", valueType: "integer" }, value: "14" }), true);
  assert.equal(updateDraftValue(state, { dataset: { draftPath: "feature_state.nLag", draftScope: "agent", agent: "3", valueType: "integer" }, value: "18" }), true);
  assert.equal(restoreCustomDraftDefaults(state, () => {}), true);
  assert.equal(state.customDraft.shared_parameters.feature_state.nLag, fixtureReferenceProfile.shared_parameters.feature_state.nLag);
  assert.deepEqual(state.customDraft.agents.map(agent => agent.parameters), [{}, {}, {}]);
  assert.equal(draftIsDirty(state.customDraft), false);
  assert.equal(discardCustomDraft(state, () => {}), true);
  assert.equal(state.customDraft, null);
});

await test("Restore defaults resets a Custom-based draft to Reference shared values and sparse Agent overrides", () => {
  const savedCustom = structuredClone(fixtureReferenceProfile);
  savedCustom.mode = "CUSTOM";
  savedCustom.version_id = "pp_restore_base";
  savedCustom.shared_parameters.feature_state.nLag = 17;
  savedCustom.agents[0].parameters = { feature_state: { nLag: 19 } };
  const state = { profile: structuredClone(fixtureReferenceProfile), customProfile: savedCustom, customDraft: null, customSave: { pending: false, versionId: null, error: null }, configScope: "shared" };
  assert.equal(beginCustomDraft(state, () => {}), true);
  assert.equal(state.customDraft.shared_parameters.feature_state.nLag, 17);
  assert.deepEqual(state.customDraft.agents[0].parameters, { feature_state: { nLag: 19 } });
  assert.equal(restoreCustomDraftDefaults(state, () => {}), true);
  assert.equal(state.customDraft.shared_parameters.feature_state.nLag, fixtureReferenceProfile.shared_parameters.feature_state.nLag);
  assert.deepEqual(state.customDraft.agents.map(agent => agent.parameters), [{}, {}, {}]);
  assert.equal(state.customDraft.baseProfile.version_id, "pp_restore_base");
});

await test("selecting a saved Custom version immediately opens an editable draft", () => {
  const savedCustom = { ...structuredClone(fixtureReferenceProfile), mode: "CUSTOM", version_id: "pp_selected_custom", display_name: "Selected Custom" };
  const state = { profile: structuredClone(fixtureReferenceProfile), customProfile: null, customProfiles: [savedCustom], customDraft: null, customSave: { pending: false, versionId: null, error: null }, customRename: { editing: false, pending: false, error: null }, configScope: "shared" };
  assert.equal(selectCustomProfile(state, "pp_selected_custom", () => {}), true);
  assert.equal(state.customProfile.version_id, "pp_selected_custom");
  assert.equal(state.customDraft.baseProfile.version_id, "pp_selected_custom");
  const view = configView(state, createI18n("en"));
  assert.match(view, /Editable draft/);
  assert.ok((view.match(/data-draft-input data-draft-scope="shared"/g) ?? []).length === 67);
});

await test("Custom draft save is pending-safe, persists a new immutable version, and exposes stable errors", async () => {
  let createCalls = 0;
  let resolveCreate;
  let renders = 0;
  const existingCustom = { ...structuredClone(fixtureReferenceProfile), mode: "CUSTOM", version_id: "pp_fixture_existing", display_name: "Custom parameter version", normalized_sha256: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789" };
  const state = {
    profile: structuredClone(fixtureReferenceProfile), customProfile: null, customDraft: null, customSave: { pending: false, versionId: null, error: null }, configScope: "shared",
    api: { createParameterProfile: () => { createCalls += 1; return new Promise(resolve => { resolveCreate = resolve; }); } }
  };
  const i18n = createI18n("en");
  beginCustomDraft(state, () => { renders += 1; });
  state.customDraft.display_name = "Human reviewed Custom";
  updateDraftValue(state, { dataset: { draftPath: "feature_state.nLag", draftScope: "shared", valueType: "integer" }, value: "10" });
  const first = saveCustomDraft(state, i18n, () => { renders += 1; });
  assert.equal(state.customSave.pending, true);
  assert.equal(createCalls, 1);
  assert.equal(await saveCustomDraft(state, i18n, () => { renders += 1; }), false);
  assert.equal(createCalls, 1);
  assert.match(configView(state, i18n), /Saving new version/);
  resolveCreate(existingCustom);
  assert.equal(await first, true);
  assert.equal(state.customDraft, null);
  assert.equal(state.customProfile.version_id, "pp_fixture_existing");
  const saved = configView(state, i18n);
  assert.match(saved, /<span>Custom<\/span><small>CUSTOM<\/small>/);
  assert.match(saved, /pp_fixture_existing/);
  assert.match(saved, /abcdef012345/);
  assert.ok(saved.includes(i18n.t("config.saved", { version: "pp_fixture_existing" })));
  assert.ok(saved.includes(i18n.t("config.savedImmutable")));
  assert.match(saved, /data-draft-edit/);
  const chinese = createI18n("zh-CN");
  assert.ok(configView(state, chinese).includes(chinese.t("config.saved", { version: "pp_fixture_existing" })));
  assert.ok(renders >= 2);

  const failedState = { profile: structuredClone(fixtureReferenceProfile), customProfile: null, customDraft: null, customSave: { pending: false, versionId: null, error: null }, configScope: "shared", api: { createParameterProfile: async () => { throw new contract.ApiError("PARAMETER_OUT_OF_RANGE", "req_create_1", { message: "C:\\internal\\profiles\\secret.json" }); } } };
  beginCustomDraft(failedState, () => {});
  failedState.customDraft.display_name = "Human reviewed Custom";
  updateDraftValue(failedState, { dataset: { draftPath: "feature_state.nLag", draftScope: "shared", valueType: "integer" }, value: "0" });
  assert.equal(await saveCustomDraft(failedState, chinese, () => {}), false);
  const failed = configView(failedState, chinese);
  assert.match(failed, /参数值超出允许范围。/);
  assert.match(failed, /PARAMETER_OUT_OF_RANGE · req_create_1/);
  assert.doesNotMatch(failed, /internal|secret\.json/);
});

await test("a saved Custom profile can rename only its display name with pending and error states", async () => {
  let calls = 0;
  let resolveRename;
  const original = { ...structuredClone(fixtureReferenceProfile), mode: "CUSTOM", version_id: "pp_rename_fixture", display_name: "Initial Custom", normalized_sha256: "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210" };
  const state = {
    profile: structuredClone(fixtureReferenceProfile), customProfile: original, customProfiles: [original], customDraft: null,
    customSave: { pending: false, versionId: null, error: null }, customRename: { editing: true, pending: false, error: null },
    api: { renameParameterProfile: () => { calls += 1; return new Promise(resolve => { resolveRename = resolve; }); } }
  };
  const english = createI18n("en");
  const first = renameCustomProfile(state, english, "Renamed Custom", () => {});
  assert.equal(state.customRename.pending, true);
  assert.equal(calls, 1);
  assert.equal(await renameCustomProfile(state, english, "Ignored", () => {}), false);
  assert.equal(calls, 1);
  resolveRename({ ...original, display_name: "Renamed Custom" });
  assert.equal(await first, true);
  assert.equal(state.customProfile.display_name, "Renamed Custom");
  assert.equal(state.customProfile.version_id, original.version_id);
  assert.equal(state.customProfile.normalized_sha256, original.normalized_sha256);
  assert.deepEqual(state.customProfile.shared_parameters, original.shared_parameters);
  assert.match(configView(state, english), /Renamed Custom/);
  const chinese = createI18n("zh-CN");
  assert.match(configView({ ...state, customRename: { editing: true, pending: false, error: null } }, chinese), /显示名称/);

  const unavailable = { profile: structuredClone(fixtureReferenceProfile), customProfile: original, customProfiles: [original], customDraft: null, customSave: { pending: false, versionId: null, error: null }, customRename: { editing: true, pending: false, error: null }, api: {} };
  assert.equal(await renameCustomProfile(unavailable, chinese, "新名称", () => {}), false);
  assert.equal(unavailable.customRename.error.title, "当前服务端尚不支持重命名。");
});

await test("workspace, history, and replay parameter entry points render only the frozen task snapshot", async () => {
  const detail = structuredClone(fixtureCompletedSimulation);
  detail.snapshot.parameter_profile.agents[1].parameters = { feature_state: { nLag: 13 } };
  const english = createI18n("en");
  const frozen = renderFrozenParameterProfileSnapshot(detail, english.t.bind(english), true);
  assert.match(frozen, /Parameter profile/);
  assert.match(frozen, /Admission name/);
  assert.match(frozen, /<span>Admission name<\/span><strong title="Reference-compatible">Reference-compatible<\/strong>/);
  assert.match(frozen, /Shared values/);
  assert.match(frozen, /Agent overrides/);
  assert.match(frozen, /Effective values/);
  assert.match(frozen, /agents\.2\.parameters\.feature_state\.nLag/);
  assert.match(frozen, /13/);
  assert.match(frozen, /cannot update a queued, running, or completed task/);
  const withoutValues = structuredClone(detail);
  delete withoutValues.snapshot.parameter_profile;
  const unavailable = renderFrozenParameterProfileSnapshot(withoutValues, english.t.bind(english));
  assert.match(unavailable, /does not expose the full frozen parameter values/);
  assert.doesNotMatch(unavailable, /Lag order/);
  const source = await readFile(new URL("../src/ui.ts", import.meta.url), "utf8");
  assert.match(source, /const requestUrl = new URL\(window\.location\.href\)/);
  assert.match(source, /requestUrl\.searchParams\.get\("run_id"\)/);
  assert.match(source, /requestedView === "history" \|\| requestedView === "replay"/);
  assert.doesNotMatch(source, /frozenParameterProfile\(detail: any, fallbackProfile/);
  const chinese = createI18n("zh-CN");
  const chineseFrozen = renderFrozenParameterProfileSnapshot(detail, chinese.t.bind(chinese), true);
  assert.match(chineseFrozen, /参数模板/);
  assert.match(chineseFrozen, /<span>准入名称<\/span><strong title="参考兼容">参考兼容<\/strong>/);
  assert.match(chineseFrozen, /排队、运行中或已完成任务均不会被热更新/);
  const customDetail = structuredClone(detail);
  customDetail.run_mode = "CUSTOM";
  customDetail.snapshot.parameter_profile.mode = "CUSTOM";
  customDetail.snapshot.parameter_profile.display_name = "Plant east review";
  assert.match(renderFrozenParameterProfileSnapshot(customDetail, english.t.bind(english), true), /Plant east review/);
});

await test("profile display uses neutral reference values and hash values stay inspectable without stretching views", async () => {
  const longHash = "a".repeat(64);
  const detail = structuredClone(fixtureCompletedSimulation);
  detail.display_name = "Reference-compatible run detail";
  detail.parameter_profile_display_name = "Reference-compatible admission";
  detail.parameter_version = "reference-v1";
  detail.parameter_profile_sha256 = longHash;
  detail.snapshot.sha256 = longHash;
  detail.snapshot.parameter_profile.display_name = "Reference-compatible frozen admission";
  detail.snapshot.parameter_profile.version_id = "reference-v1";
  detail.snapshot.parameter_profile.sha256 = longHash;
  detail.snapshot.parameter_profile.normalized_sha256 = longHash;
  detail.snapshot.parameter_profile_sha256 = longHash;
  const history = {
    items: [detail], loading: false, error: null, query: "", status: "", mode: "", nextCursor: null, hasMore: false, total: 1,
    listEpoch: 0, selectionEpoch: 0, selectedRunId: detail.run_id, detail, detailLoading: false, detailError: null,
    artifacts: { artifact_state: "COMMITTED", items: [] }, replay: { agent: 1, loading: false, error: null, data: null, results: null, alarms: null }
  };
  const english = createI18n("en");
  assert.equal(displayProfileText("Reference-compatible admission", english.t.bind(english)), "Reference-compatible admission");
  assert.equal(displayProfileVersion("reference-v1", english.t.bind(english)), "reference-v1");
  const historyMarkup = historyView({ history }, { runId: null, summary: null }, english);
  const replayMarkup = replayView({ history, replayPlayback: { playing: false, speed: 1, position: 0, timer: null }, chart: { series: {} } }, { runId: null, summary: null }, english);
  const workspaceMarkup = workspaceView({ chart: { series: {} } }, { detail, summary: null, runId: detail.run_id, loading: false, selectedAgent: 1, error: null, lastEventId: null, events: [] }, english);
  [historyMarkup, replayMarkup, workspaceMarkup].forEach(markup => {
    assert.match(markup, /Reference-compatible/);
    assert.match(markup, /data-hash-value/);
    assert.match(markup, new RegExp(`title="${longHash}"`));
    assert.match(markup, new RegExp(`aria-label="Copy full hash: ${longHash}"`));
  });
  assert.match(workspaceMarkup, /<aside class="panel parameter-rail"><p class="eyebrow">RUN CONFIGURATION<\/p><h2>Reference-compatible<\/h2>/);
  assert.match(historyMarkup, /<span>Admission name<\/span><strong title="Reference-compatible">Reference-compatible<\/strong>/);
  assert.match(replayMarkup, /<span>Admission name<\/span><strong title="Reference-compatible">Reference-compatible<\/strong>/);
  const savedCustom = { ...structuredClone(fixtureReferenceProfile), mode: "CUSTOM", version_id: "pp_long_hash", display_name: "Custom saved profile", normalized_sha256: longHash };
  const configMarkup = configView({ profile: fixtureReferenceProfile, customProfile: savedCustom, customProfiles: [savedCustom], customDraft: null, customSave: { pending: false, versionId: savedCustom.version_id, profile: savedCustom, error: null }, customRename: { editing: false, pending: false, error: null }, configScope: "shared" }, english);
  assert.match(configMarkup, /Custom saved profile/);
  assert.match(configMarkup, new RegExp(`data-hash-value[^>]*title="${longHash}" aria-label="Copy full hash: ${longHash}"`));
  assert.match(renderHashValue(longHash), new RegExp(`data-hash-value[^>]*title="${longHash}" aria-label="Copy full hash: ${longHash}"`));

  const chinese = createI18n("zh-CN");
  const chineseReplay = replayView({ history, replayPlayback: { playing: false, speed: 1, position: 0, timer: null }, chart: { series: {} } }, { runId: null, summary: null }, chinese);
  assert.match(chineseReplay, /参考兼容/);
  const styles = await readFile(new URL("../public/styles.css", import.meta.url), "utf8");
  assert.match(styles, /\.hash-presentation \{ flex:1 1 auto; min-inline-size:0; max-inline-size:100%; overflow:hidden; \}/);
  assert.match(styles, /\.hash-value \{ display:inline-block; min-inline-size:0; max-inline-size:100%; overflow:hidden;[^}]*text-overflow:ellipsis;[^}]*white-space:nowrap;/);
  assert.match(styles, /\.hash-value-inline \{ max-inline-size:min\(22ch,100%\); \}/);
  assert.match(styles, /\.rail-snapshot > div \{ display:grid; grid-template-columns:minmax\(0,7\.25rem\) minmax\(0,1fr\); align-items:baseline; gap:var\(--space-2\); \}/);
  assert.match(styles, /\.rail-snapshot > div > strong \{ max-inline-size:100%; overflow:hidden; text-align:start; text-overflow:ellipsis; white-space:nowrap; \}/);
  assert.match(styles, /\.rail-snapshot \.hash-presentation,\.rail-snapshot \.hash-value \{ display:block; inline-size:100%; max-inline-size:100%; \}/);
});

await test("Workspace adapts the live nested version and top-level snapshot hash DTO without object coercion", () => {
  const liveDetail = structuredClone(fixtureLiveRunDetail);
  const liveSnapshotHash = "b".repeat(64);
  liveDetail.parameter_version.display_name = "Reference-compatible admission";
  liveDetail.snapshot_sha256 = liveSnapshotHash;
  const english = createI18n("en");
  const presentation = runSnapshotDisplay(liveDetail, english.t.bind(english));
  assert.deepEqual(presentation, {
    parameter: "reference-v1",
    mapping: "identity-v1",
    snapshotHash: liveSnapshotHash,
    parameterHash: fixtureReferenceProfile.normalized_sha256,
    mappingHash: fixtureReferenceProfile.normalized_sha256
  });
  const markup = workspaceView({ chart: { series: {} } }, { detail: liveDetail, summary: null, runId: liveDetail.run_id, loading: false, selectedAgent: 1, error: null, lastEventId: 155, events: [] }, english);
  assert.match(markup, /reference-v1/);
  assert.match(markup, /identity-v1/);
  assert.match(markup, /Mapping<\/span><strong[^>]*>identity-v1/);
  assert.match(markup, /class="rail-group-icon cyan" aria-hidden="true">G/);
  assert.match(markup, /class="rail-group-icon pink" aria-hidden="true">P/);
  assert.match(markup, /class="rail-group-icon cyan" aria-hidden="true">S/);
  assert.match(markup, /class="rail-group-icon green" aria-hidden="true">F/);
  assert.match(markup, /class="rail-group-icon amber" aria-hidden="true">A/);
  assert.match(markup, new RegExp(`data-hash-value[^>]*title="${liveSnapshotHash}" aria-label="Copy full hash: ${liveSnapshotHash}"`));
  assert.doesNotMatch(markup, /\[object Object\]|Not selected/);
  const chineseMarkup = workspaceView({ chart: { series: {} } }, { detail: liveDetail, summary: null, runId: liveDetail.run_id, loading: false, selectedAgent: 1, error: null, lastEventId: 155, events: [] }, createI18n("zh-CN"));
  assert.match(chineseMarkup, /映射<\/span><strong[^>]*>identity-v1/);
});

await test("parameter groups and dynamic preflight states stay localized without changing protocol tokens", () => {
  assert.deepEqual(languages, ["en", "zh-CN"]);
  assert.equal(createI18n().language, "en");
  const i18n = createI18n("en");
  assert.equal(i18n.t("param.group.featureState"), "Feature state");
  assert.equal(i18n.t("chart.displayPoints", { count: 260 }), "260 display points");
  const queued = preflightPresentation(fixtureDatasetValidating, i18n.t.bind(i18n));
  const running = preflightPresentation({ ...fixtureDatasetValidating, preflight: { ...fixtureDatasetValidating.preflight, status: "RUNNING", queue_position: null } }, i18n.t.bind(i18n));
  const valid = preflightPresentation(fixtureDatasetValid, i18n.t.bind(i18n));
  const invalid = preflightPresentation({ ...fixtureDatasetValidating, status: "INVALID", preflight: { ...fixtureDatasetValidating.preflight, status: "FAILED" } }, i18n.t.bind(i18n));
  assert.deepEqual([queued.status, running.status, valid.status, invalid.status], ["QUEUED", "RUNNING", "VALID", "FAILED"]);
  assert.deepEqual([queued.complete, running.complete, valid.complete, invalid.complete], [false, false, true, false]);
  assert.match(invalid.message, /cannot start/i);
  i18n.setLanguage("zh-CN");
  assert.equal(i18n.t("param.group.featureState"), "特征与状态");
  assert.equal(i18n.t("chart.displayPoints", { count: 260 }), "260 个显示点");
  assert.match(preflightPresentation(fixtureDatasetValidating, i18n.t.bind(i18n)).message, /已排队/);
  ["data.description", "data.validating", "data.valid", "data.invalid", "data.stats", "data.awaiting", "data.preflightNoDataset", "data.preflightQueued", "data.preflightRunning", "data.preflightFinalizing", "data.preflight.queued", "data.preflight.running", "data.preflight.validating", "data.preflight.finalizing", "data.preflight.valid", "data.preflight.invalid", "config.copy", "config.defaultCopyName", "config.saved", "state.disabled"].forEach(key => assert.doesNotMatch(i18n.t(key, { version: "v1" }), /Worker|VALID|INVALID|Agent|CUSTOM/));
  assert.equal(i18n.t("data.valid"), "数据集校验通过");
  assert.equal(i18n.t("data.invalid"), "数据集校验未通过");
  assert.equal(i18n.t("config.copy"), "创建自定义默认副本");
  assert.match(i18n.t("data.preflight.running"), /算法工作器预检/);
  i18n.setLanguage("en");
  assert.equal(i18n.t("param.group.featureState"), "Feature state");
  i18n.setLanguage("zh-CN");
  assert.equal(i18n.language, "zh-CN");
  assert.equal(createI18n().language, "en");
});

await test("the localized custom file control exposes its selection without native UI text", () => {
  const i18n = createI18n("en");
  const english = renderFilePicker({ fileName: null }, i18n.t.bind(i18n));
  assert.match(english, /class="file-input-visually-hidden"/);
  assert.match(english, /Choose a CSV file/);
  assert.match(english, /No file selected/);
  assert.doesNotMatch(english, /选择|未选择/);
  i18n.setLanguage("zh-CN");
  const chinese = renderFilePicker({ fileName: "strict-seven-columns.csv" }, i18n.t.bind(i18n));
  assert.match(chinese, /选择 CSV 文件/);
  assert.match(chinese, /strict-seven-columns\.csv/);
  assert.equal(i18n.t("document.title"), "zx/federated-iot-platform:latest · 仿真控制台");
});

await test("the preflight panel keeps upload failure and Worker states mutually truthful", () => {
  const i18n = createI18n("en");
  const noDataset = datasetPreflightPanel(null, { error: new Error("upload failed") }, i18n.t.bind(i18n));
  assert.equal(noDataset.presentation, null);
  assert.match(noDataset.title, /No dataset/);
  assert.doesNotMatch(noDataset.content, /Awaiting Worker preflight/);
  const queued = datasetPreflightPanel(fixtureDatasetValidating, {}, i18n.t.bind(i18n));
  const running = datasetPreflightPanel({ ...fixtureDatasetValidating, preflight: { ...fixtureDatasetValidating.preflight, status: "RUNNING" } }, {}, i18n.t.bind(i18n));
  const invalid = datasetPreflightPanel({ ...fixtureDatasetValidating, status: "INVALID", preflight: { ...fixtureDatasetValidating.preflight, status: "FAILED" } }, {}, i18n.t.bind(i18n));
  assert.deepEqual([queued.presentation.status, running.presentation.status, invalid.presentation.status], ["QUEUED", "RUNNING", "FAILED"]);
  assert.equal(invalid.presentation.complete, false);
  assert.doesNotMatch(invalid.content, /Worker preflight completed successfully/);
});

await test("a terminal dataset error uses a stable code without exposing its raw message", () => {
  const i18n = createI18n("en");
  const failed = { ...fixtureDatasetValidating, status: "INVALID", error: { code: "INSUFFICIENT_SAMPLES", message: "C:\\internal\\datasets\\source.csv" }, preflight: { ...fixtureDatasetValidating.preflight, status: "FAILED" } };
  const display = preflightFailureDisplay(failed, i18n.t.bind(i18n));
  assert.deepEqual(display, { code: "INSUFFICIENT_SAMPLES", message: "Worker preflight found too few usable samples.", stage: null, diagnosticId: null, recoverable: null });
  assert.doesNotMatch(display.message, /internal|source\.csv/);
  const panel = datasetPreflightPanel(failed, {}, i18n.t.bind(i18n));
  assert.equal(panel.presentation.status, "FAILED");
  assert.match(panel.content, /INSUFFICIENT_SAMPLES/);
  assert.match(panel.content, /too few usable samples/);
  assert.doesNotMatch(panel.content, /internal|source\.csv/);
  i18n.setLanguage("zh-CN");
  assert.equal(preflightFailureDisplay(failed, i18n.t.bind(i18n)).message, "算法工作器预检发现可用样本不足。");
});

await test("the stylesheet applies the prototype 1x design scale through shared root and control tokens", async () => {
  const stylesheet = await readFile(new URL("../public/styles.css", import.meta.url), "utf8");
  assert.match(stylesheet, /:root\s*\{[\s\S]*?--scale:\s*1[\s\S]*?--control-block:\s*2\.2857rem[\s\S]*?--header-block:\s*4\.8571rem[\s\S]*?font-size:\s*clamp\(14px,\s*calc\(12\.5px \+ \.12vw\),\s*16px\)/);
  assert.match(stylesheet, /@media \(min-width: 2200px\)\s*\{\s*:root\s*\{\s*font-size:18px/);
  assert.match(stylesheet, /@media \(min-width: 3400px\)\s*\{\s*:root\s*\{\s*font-size:21px/);
  assert.match(stylesheet, /body\s*\{[^}]*font-size:1rem/);
  assert.match(stylesheet, /\.topbar\s*\{[^}]*min-height:\s*var\(--header-block\)/);
  assert.match(stylesheet, /\.button\s*\{[^}]*min-height:\s*var\(--control-block\)/);
  assert.match(stylesheet, /\.system-strip\s*\{[^}]*min-block-size:\s*var\(--status-block\)/);
  assert.match(stylesheet, /\.workspace\s*\{[^}]*grid-template-columns:minmax\(16rem, 20rem\)/);
  assert.doesNotMatch(stylesheet, /\.button\s*\{[^}]*min-height:\s*\d+px/);
  assert.doesNotMatch(stylesheet, /\.agent-tabs button\s*\{[^}]*min-height:\s*\d+px/);
  assert.doesNotMatch(stylesheet, /\.field input\s*\{[^}]*min-height:\s*\d+px/);
  assert.match(stylesheet, /\.parameter-snapshot-agent-content\s*\{[^}]*grid-template-columns:repeat\(2,minmax\(0,1fr\)\)/);
  assert.match(stylesheet, /@media \(max-width: 780px\)\s*\{[\s\S]*?\.parameter-snapshot-agent-content\s*\{[^}]*grid-template-columns:1fr/);
});

await test("the shared visual theme uses the prototype background and semantic tokens across product pages", async () => {
  const stylesheet = await readFile(new URL("../public/styles.css", import.meta.url), "utf8");
  assert.match(stylesheet, /:root\s*\{[\s\S]*?--topbar-bg:[^;]+;[\s\S]*?--system-bg:[^;]+;[\s\S]*?--panel-bg:[^;]+;[\s\S]*?--brand-bg:[^;]+;/);
  assert.match(stylesheet, /body\s*\{[^}]*min-width:\s*320px[^}]*background:radial-gradient\(circle at 70% 0%,rgba\(39,215,231,\.06\),transparent 31rem\),linear-gradient\(180deg,#090e18 0%,var\(--bg\) 100%\)/);
  assert.match(stylesheet, /\.topbar\s*\{[^}]*background:var\(--topbar-bg\)/);
  assert.match(stylesheet, /\.brand-mark\s*\{[^}]*color:\s*var\(--cyan\)[^}]*background:var\(--brand-bg\)[^}]*box-shadow:inset/);
  assert.match(stylesheet, /\.panel\s*\{[^}]*background:var\(--panel-bg\)/);
  assert.match(stylesheet, /\.data-stat-grid > article\s*\{[^}]*background:var\(--panel-bg\)/);
  assert.match(stylesheet, /\.button\.primary\s*\{[^}]*color:var\(--bg\)/);
  assert.match(stylesheet, /\.button\.danger\s*\{[^}]*color:var\(--red\)/);
  assert.match(stylesheet, /\.state-bar \.heavy,\.state-key\.heavy\s*\{[^}]*background:var\(--pink\)/);
  assert.match(stylesheet, /\.diagnostic-legend\s*\{[^}]*color:var\(--muted\)/);
  assert.match(stylesheet, /\.alarm-row\s*\{[^}]*color:var\(--soft\)/);
  assert.match(stylesheet, /\.trace-grid span\s*\{[^}]*color:var\(--muted\)/);
  assert.match(stylesheet, /\.trace-grid strong\s*\{[^}]*color:var\(--soft\)/);
  assert.doesNotMatch(stylesheet, /--violet/);
});

await test("the established page layouts retain local overflow and responsive fallbacks", async () => {
  const stylesheet = await readFile(new URL("../public/styles.css", import.meta.url), "utf8");
  assert.match(stylesheet, /@media \(max-width: 1200px\)\s*\{[\s\S]*?\.workspace\s*\{[^}]*grid-template-columns:1fr/);
  assert.match(stylesheet, /@media \(max-width: 1200px\)\s*\{[\s\S]*?\.data-layout\s*\{[^}]*grid-template-columns:1fr/);
  assert.match(stylesheet, /@media \(max-width: 1200px\)\s*\{[\s\S]*?\.config-layout\s*\{[^}]*grid-template-columns:1fr/);
  assert.match(stylesheet, /\.table-wrap\s*\{[^}]*overflow-x:auto/);
  assert.match(stylesheet, /\.history-table-wrap\s*\{[^}]*scrollbar-gutter:stable/);
  assert.match(stylesheet, /\.replay-transport\s*\{[^}]*flex-wrap:wrap/);
  assert.match(stylesheet, /@media \(max-width:780px\)\s*\{[^}]*\.workspace-diagnostics-lower/);
  assert.match(stylesheet, /tbody tr\s*\{[^}]*transition:background-color \.18s ease,box-shadow \.18s ease/);
  assert.match(stylesheet, /tbody tr:hover td,tbody tr:focus-within td\s*\{[^}]*background:rgba\(255,255,255,\.035\)/);
  assert.match(stylesheet, /\.history-table\s*\{[^}]*font-family:"Segoe UI","Microsoft YaHei UI",sans-serif[^}]*font-variant-numeric:tabular-nums/);
});

await test("each application load starts in English without browser-locale or persisted-language restoration", async () => {
  const source = await readFile(new URL("../src/ui.ts", import.meta.url), "utf8");
  assert.match(source, /const i18n = createI18n\("en"\)/);
  assert.doesNotMatch(source, /navigator\.language|localStorage|sessionStorage/);
  const switched = createI18n("zh-CN");
  assert.equal(switched.language, "zh-CN");
  assert.equal(createI18n().language, "en");
});

await test("the parameter layout keeps desktop navigation and detail scrolling independently accessible", async () => {
  const stylesheet = await readFile(new URL("../public/styles.css", import.meta.url), "utf8");
  const source = await readFile(new URL("../src/ui.ts", import.meta.url), "utf8");
  assert.match(stylesheet, /\.config-layout\s*\{[^}]*grid-template-columns:minmax\(14rem,17\.5rem\) minmax\(0,1fr\)[^}]*align-items:start/);
  assert.match(stylesheet, /\.config-index,\.config-detail-scroll\s*\{[^}]*position:sticky[^}]*inset-block-start:var\(--config-scroll-offset\)[^}]*max-block-size:calc\(100dvh - var\(--config-scroll-offset\) - var\(--space-4\)\)[^}]*overflow-y:auto[^}]*overscroll-behavior:contain[^}]*scrollbar-gutter:stable/);
  assert.match(stylesheet, /\.config-index > :first-child,\.config-detail-scroll > :first-child\s*\{[^}]*margin-block-start:0/);
  assert.match(stylesheet, /\.profile-readonly\s*\{[^}]*flex:0 0 auto[^}]*inline-size:max-content[^}]*white-space:nowrap/);
  assert.match(stylesheet, /@media \(max-width: 1200px\)\s*\{[\s\S]*?\.config-index,\.config-detail-scroll\s*\{[^}]*position:static[^}]*max-block-size:none[^}]*overflow:visible/);
  assert.match(source, /data-config-anchor href="#parameter-group-/);
  assert.match(source, /data-config-detail-scroll tabindex="0"/);
  assert.match(source, /detail\.scrollTo\(\{ top: Math\.max\(0, target\.offsetTop - detail\.offsetTop - 12\), behavior: "smooth" \}\)/);
});

await test("configuration header keeps its description and three mode actions aligned at desktop scale", async () => {
  const stylesheet = await readFile(new URL("../public/styles.css", import.meta.url), "utf8");
  const state = { profile: fixtureReferenceProfile, customProfile: null, customProfiles: [], customDraft: null, customSave: {}, customRename: {} };
  const englishI18n = createI18n();
  const english = configView(state, englishI18n);
  const chineseI18n = createI18n("zh-CN");
  const chinese = configView(state, chineseI18n);
  assert.match(english, /page-head config-page-head/);
  assert.match(english, /button class="button mode-button config-export"/);
  assert.match(english, /<span>Reference-compatible<\/span><small>REFERENCE<\/small>/);
  assert.match(chinese, /<span>参考兼容<\/span><small>REFERENCE<\/small>/);
  assert.match(chinese, /<span>自定义<\/span><small>CUSTOM<\/small>/);
  assert.match(stylesheet, /@media \(min-width: 1201px\)\s*\{[\s\S]*?\.config-page-head\s*\{[^}]*flex-wrap:nowrap[\s\S]*?\.config-page-head \.page-head-copy > p\s*\{[^}]*white-space:nowrap/);
  assert.match(stylesheet, /\.config-actions\s*\{[^}]*flex-wrap:nowrap[^}]*align-items:stretch/);
  assert.match(stylesheet, /\.config-actions > \.button\s*\{[^}]*grid-template-rows:1lh 1lh[^}]*min-block-size:var\(--control-block\)/);
  assert.match(stylesheet, /@media \(max-width: 1200px\)\s*\{[\s\S]*?\.config-actions\s*\{[^}]*flex-wrap:wrap/);
});

await test("dynamic status labels are localized instead of exposing protocol enums as primary text", () => {
  const zh = createI18n("zh-CN");
  const en = createI18n();
  const statuses = ["INVALID", "VALID", "VALIDATING", "QUEUED", "RUNNING", "CANCELLING", "CANCELLED", "FAILED", "FAILED_RECOVERABLE"];
  statuses.forEach(status => {
    assert.notEqual(localizedStatus(status, zh.t.bind(zh)), status);
    assert.notEqual(localizedStatus(status, en.t.bind(en)), status);
  });
  assert.equal(localizedStatus("INVALID", zh.t.bind(zh)), "无效");
  assert.equal(localizedStatus("FAILED_RECOVERABLE", zh.t.bind(zh)), "失败（可恢复）");
  assert.equal(preflightPresentation({ status: "FAILED", preflight: { status: "FAILED" } }, zh.t.bind(zh)).label, "失败");
  assert.equal(preflightPresentation({ status: "INVALID", preflight: { status: "INVALID" } }, zh.t.bind(zh)).label, "无效");
});

await test("SSE-like store updates preserve the configuration detail scroll, focus, and unsaved draft", () => {
  const draft = { shared_parameters: { split: { training_ratio: .71 } }, agents: [] };
  const focused = { calls: [], focus(options) { this.calls.push(options); documentRef.activeElement = this; } };
  const detail = { scrollTop: 187, contains: node => node === focused };
  const documentRef = { activeElement: focused };
  const root = {
    ownerDocument: documentRef,
    querySelector: selector => selector === "[data-config-detail-scroll]" ? detail : null
  };
  const state = { view: "config", customDraft: draft };
  const store = new SimulationStore({});
  let updates = 0;
  store.subscribe(() => {
    if (!store.state.lastEventId) return;
    assert.equal(preserveConfigDynamicState(root, state, () => {
      updates += 1;
      detail.scrollTop = 0;
      documentRef.activeElement = null;
    }), true);
  });
  store.emit({ connection: "connected", lastEventId: 44 });
  assert.equal(updates, 1);
  assert.equal(detail.scrollTop, 187);
  assert.equal(documentRef.activeElement, focused);
  assert.deepEqual(focused.calls, [{ preventScroll: true }]);
  assert.equal(state.customDraft, draft);
});

await test("locale changes restore the language selector focus without changing preserved page context", async () => {
  const focusedLanguage = {
    selectionStart: null,
    selectionEnd: null,
    hasAttribute: attribute => attribute === "data-language"
  };
  const beforeDocument = { activeElement: focusedLanguage };
  const beforeRoot = {
    ownerDocument: beforeDocument,
    querySelector: selector => selector === "[data-config-detail-scroll]" ? { scrollTop: 187 } : selector === "[data-history-table-wrap]" ? { scrollTop: 31 } : null
  };
  const context = captureRenderContext(beforeRoot);
  assert.equal(context.focusSelector, "[data-language]");
  assert.equal(context.detailScrollTop, 187);
  assert.equal(context.historyScrollTop, 31);

  const restoredLanguage = {
    disabled: false,
    focusCalls: [],
    focus(options) { this.focusCalls.push(options); afterDocument.activeElement = this; }
  };
  const afterDocument = { activeElement: null };
  const restoredDetail = { scrollTop: 0 };
  const restoredHistory = { scrollTop: 0 };
  const afterRoot = {
    ownerDocument: afterDocument,
    querySelector: selector => selector === "[data-language]" ? restoredLanguage : selector === "[data-config-detail-scroll]" ? restoredDetail : selector === "[data-history-table-wrap]" ? restoredHistory : null
  };
  const i18n = createI18n("en");
  const state = { language: "en", view: "config", customDraft: { display_name: "kept draft" }, selectedRunId: "run_kept" };
  state.language = "zh-CN";
  i18n.setLanguage(state.language);
  const documentElement = { lang: "en" };
  documentElement.lang = state.language;
  restoreRenderContext(afterRoot, context);
  assert.equal(documentElement.lang, "zh-CN");
  assert.equal(i18n.t("aria.language"), "界面语言");
  assert.equal(state.view, "config");
  assert.equal(state.customDraft.display_name, "kept draft");
  assert.equal(state.selectedRunId, "run_kept");
  assert.equal(restoredDetail.scrollTop, 187);
  assert.equal(restoredHistory.scrollTop, 31);
  assert.equal(afterDocument.activeElement, restoredLanguage);
  assert.deepEqual(restoredLanguage.focusCalls, [{ preventScroll: true }]);
  const source = await readFile(new URL("../src/ui.ts", import.meta.url), "utf8");
  assert.match(source, /document\.documentElement\.lang = state\.language/);
  assert.match(source, /\["data-language", "data-chart", "data-draft-display-name"/);
});

await test("simulation creation always sends the frozen empty per-run overrides for Agent 1, Agent 2, and Agent 3", () => {
  const i18n = createI18n("en");
  const payload = buildSimulationPayload({ dataset: fixtureDatasetValid, profile: fixtureReferenceProfile, customProfile: null }, i18n.t.bind(i18n));
  assert.deepEqual(payload.agent_overrides, [{ agent: 1, parameters: {} }, { agent: 2, parameters: {} }, { agent: 3, parameters: {} }]);
  assert.equal(payload.parameter_profile_version_id, fixtureReferenceProfile.version_id);
  assert.equal(payload.display_name, "S1 simulation");
});

await test("Workspace state matrix distinguishes onboarding, live tasks, unavailable results, and frozen terminal results", () => {
  const english = createI18n("en");
  const state = { chart: { series: { truth: true, local: true, global: true, fused: true, interval: true } } };
  const idle = workspaceView(state, { detail: null, summary: null, selectedAgent: 3, loading: false, error: null, lastEventId: null }, english);
  assert.match(idle, /No simulation configured/);
  assert.match(idle, /Import and validate data, select a saved parameter profile, then start a simulation/);
  assert.match(idle, /data-nav="data"/);
  assert.match(idle, /data-nav="config"/);
  assert.match(idle, /data-agent="1"[^>]*aria-selected="false"[^>]*disabled/);
  assert.match(idle, /data-agent="3"[^>]*disabled/);
  assert.doesNotMatch(idle, /class="active"[^>]*>Agent 3/);
  assert.doesNotMatch(idle, /Select a completed run/);

  const queuedDetail = { ...fixtureCompletedSimulation, status: "QUEUED", current_stage: "PREPROCESSING", cancellable: true, finished_at: null };
  const queued = workspaceView(state, { detail: queuedDetail, summary: null, selectedAgent: 3, loading: false, error: null, lastEventId: 3 }, english);
  assert.match(queued, /Current task/);
  assert.match(queued, /Frozen results and diagnostics become available only after this task completes/);
  assert.match(queued, /PREPROCESSING/);
  assert.doesNotMatch(queued, /data-chart/);

  const failed = workspaceView(state, { detail: { ...queuedDetail, status: "FAILED", current_stage: null }, summary: null, selectedAgent: 1, loading: false, error: null, lastEventId: 4 }, english);
  assert.match(failed, /Frozen results are not available for this task/);
  assert.doesNotMatch(failed, /data-chart/);

  const completed = workspaceView(state, { detail: fixtureCompletedSimulation, summary: fixtureSummary, selectedAgent: 1, loading: false, error: null, lastEventId: 12 }, english);
  assert.match(completed, /data-chart/);
  assert.match(completed, /Frozen results · Agent 1/);
  assert.match(completed, /data-parameter-profile-snapshot/);
  assert.match(completed, /class="frozen-rail-groups" data-parameter-profile-snapshot/);

  const chinese = createI18n("zh-CN");
  assert.match(workspaceView(state, { detail: null, summary: null, selectedAgent: 3, loading: false, error: null, lastEventId: null }, chinese), /尚未配置仿真/);
});

await test("readiness and SSE patches retain shell identity and only alter Workspace live fields", () => {
  const header = {}; const navigation = {}; const main = {};
  const workspace = { getAttribute: key => ({ "data-run-id": "run_live", "data-has-summary": "false" })[key] };
  const status = { className: "", textContent: "" };
  const stage = { textContent: "" };
  const event = { textContent: "" };
  const result = { innerHTML: "" };
  const root = {
    querySelector(selector) {
      return ({ ".topbar": header, ".nav": navigation, "#main-content": main, "[data-workspace-view]": workspace, "[data-workspace-live-status]": status, "[data-workspace-stage]": stage, "[data-workspace-event]": event, "[data-workspace-result-state]": result }[selector] ?? null);
    }
  };
  const task = { detail: { run_id: "run_live", status: "RUNNING", current_stage: "TESTING" }, summary: null, loading: false, lastEventId: 91, connection: "connected", readiness: null, readinessLoading: false, readinessError: null };
  const state = { view: "workspace", dataset: null };
  const i18n = createI18n("en");
  assert.equal(patchDynamicApplication(root, state, task, {}, i18n, "sse"), true);
  assert.equal(root.querySelector(".topbar"), header);
  assert.equal(root.querySelector(".nav"), navigation);
  assert.equal(root.querySelector("#main-content"), main);
  assert.equal(status.textContent, "Running");
  assert.equal(stage.textContent, "TESTING");
  assert.equal(event.textContent, "Last event ID: 91");
  assert.match(result.innerHTML, /Frozen results and diagnostics/);
});

await test("one stage SSE event projects consistently to the shell and every Workspace consumer", async () => {
  let handlers;
  let detailReads = 0;
  const initialDetail = { ...structuredClone(fixtureLiveRunDetail), current_stage: "LOCAL_TRAINING", latest_event_id: 153 };
  const api = {
    getSimulation: async () => {
      detailReads += 1;
      if (detailReads === 1) return structuredClone(initialDetail);
      return new Promise(() => {});
    },
    subscribeSimulationEvents: (_runId, _lastEventId, nextHandlers) => { handlers = nextHandlers; return { close() {} }; }
  };
  const store = new SimulationStore(api);
  await store.selectRun(initialDetail.run_id);
  handlers.onEvent({ id: 156, type: "simulation.stage", data: { run_id: initialDetail.run_id, status: "RUNNING", current_stage: "ANCHOR_AGGREGATING" } });
  assert.equal(store.state.detail.current_stage, "ANCHOR_AGGREGATING");
  assert.equal(store.state.detail.latest_event_id, 156);
  assert.equal(store.state.lastEventId, 156);

  const header = {}; const navigation = {}; const main = {}; const system = { innerHTML: "" };
  const workspace = { getAttribute: key => ({ "data-run-id": initialDetail.run_id, "data-has-summary": "false" })[key] };
  const statuses = [{ className: "", textContent: "" }, { className: "", textContent: "" }];
  const stages = [{ textContent: "LOCAL_TRAINING" }, { textContent: "LOCAL_TRAINING" }];
  const events = [{ textContent: "Last event ID: 153" }, { textContent: "Last event ID: 153" }];
  const result = { innerHTML: "" };
  const first = {
    ".topbar": header, ".nav": navigation, "#main-content": main, "[data-system-strip]": system,
    "[data-workspace-view]": workspace, "[data-workspace-live-status]": statuses[0], "[data-workspace-stage]": stages[0], "[data-workspace-event]": events[0], "[data-workspace-result-state]": result
  };
  const all = { "[data-workspace-live-status]": statuses, "[data-workspace-stage]": stages, "[data-workspace-event]": events };
  const root = { querySelector: selector => first[selector] ?? null, querySelectorAll: selector => all[selector] ?? [] };
  const state = { view: "workspace", dataset: null };
  const i18n = createI18n("en");
  assert.equal(patchDynamicApplication(root, state, store.state, store, i18n, "sse"), true);
  assert.equal(root.querySelector(".topbar"), header);
  assert.equal(root.querySelector(".nav"), navigation);
  assert.equal(root.querySelector("#main-content"), main);
  assert.deepEqual(statuses.map(item => item.textContent), ["Running", "Running"]);
  assert.deepEqual(stages.map(item => item.textContent), ["ANCHOR_AGGREGATING", "ANCHOR_AGGREGATING"]);
  assert.deepEqual(events.map(item => item.textContent), ["Last event ID: 156", "Last event ID: 156"]);
  assert.match(system.innerHTML, /ANCHOR_AGGREGATING/);
  assert.match(system.innerHTML, /Last event ID: 156/);
  store.close();
});

await test("Queue and resource strings never expose internal work-package identifiers", async () => {
  const source = await readFile(new URL("../src/i18n.ts", import.meta.url), "utf8");
  const uiSource = await readFile(new URL("../src/ui.ts", import.meta.url), "utf8");
  assert.doesNotMatch(source, /(?:FE|BE|QA)-\d+/);
  assert.doesNotMatch(uiSource, /(?:FE|BE|QA)-\d+/);
  const state = { queue: { items: [], loading: false, error: null } };
  const empty = queueView(state, { detail: null }, createI18n("en"));
  assert.match(empty, /No active or waiting tasks/);
  assert.doesNotMatch(empty, /prototype|demo|FE-/i);
});

await test("History lists only real terminal tasks and filters run or dataset without selecting a Queue task", () => {
  const completed = structuredClone(fixtureCompletedSimulation);
  const failed = { ...structuredClone(fixtureCompletedSimulation), run_id: "run_fixture_failed", display_name: "Failed source", dataset: { dataset_id: "ds_failed", display_name: "failed-dataset.csv" }, status: "FAILED", artifact_state: "INCOMPLETE", finished_at: "2026-08-16T08:05:00.000+08:00" };
  const queued = { ...structuredClone(fixtureCompletedSimulation), run_id: "run_fixture_queued", status: "QUEUED", finished_at: null, artifact_state: "NOT_STARTED" };
  const state = { history: { items: [completed, failed, queued], loading: false, error: null, query: "", status: "", mode: "", selectedRunId: null, detail: null, detailLoading: false, detailError: null, artifacts: null, replay: { agent: 1, loading: false, error: null, data: null, results: null, alarms: null } } };
  assert.deepEqual(filteredHistoryItems(state.history).map(item => item.run_id), [completed.run_id, failed.run_id]);
  state.history.query = "failed-dataset";
  assert.deepEqual(filteredHistoryItems(state.history).map(item => item.run_id), [failed.run_id]);
  state.history.query = "";
  state.history.status = "COMPLETED";
  assert.deepEqual(filteredHistoryItems(state.history).map(item => item.run_id), [completed.run_id]);
  assert.equal(state.history.selectedRunId, null);
  state.history.status = "";
  const english = createI18n("en");
  const markup = historyView(state, { runId: null, summary: null }, english);
  assert.match(markup, /2 of 2 terminal tasks/);
  assert.match(markup, /run_fixture_completed/);
  assert.doesNotMatch(markup, /run_fixture_queued/);
  assert.match(markup, /data-history-replay="run_fixture_completed"/);
  assert.match(markup, /data-history-replay="run_fixture_failed" disabled/);
  assert.deepEqual(historyArtifactGate(completed), { ready: true, key: "complete", requiredCount: null });
  assert.equal(historyArtifactGate(failed).ready, false);
  const chinese = createI18n("zh-CN");
  assert.match(historyView({ ...state, history: { ...state.history, status: "" } }, { runId: null, summary: null }, chinese), /任务历史/);
});

await test("History uses server-bound search and filters, resets stale cursors, and appends only the next server page", async () => {
  const first = { ...structuredClone(fixtureCompletedSimulation), dataset: { dataset_id: "ds_history_1", display_name: "source-one.csv" } };
  const second = { ...structuredClone(fixtureCompletedSimulation), run_id: "run_history_page_2", display_name: "Second terminal run", dataset: { dataset_id: "ds_history_2", display_name: "source-two.csv" } };
  const requests = [];
  const state = {
    api: {
      listSimulations: query => {
        requests.push(structuredClone(query));
        if (!query.cursor) return Promise.resolve({ items: [first], meta: { request_id: "req_history_1", next_cursor: "cursor_history_2", has_more: true, total: 2 } });
        return Promise.resolve({ items: [second], meta: { request_id: "req_history_2", next_cursor: null, has_more: false, total: 2 } });
      }
    },
    history: { items: [], loading: false, error: null, query: "source", status: "COMPLETED", mode: "REFERENCE", nextCursor: null, hasMore: false, total: null, listEpoch: 0, selectionEpoch: 0, selectedRunId: null, detail: null, detailLoading: false, detailError: null, artifacts: null, replay: { agent: 1, loading: false, error: null, data: null, results: null, alarms: null } }
  };
  const renders = [];
  await loadHistory(state, value => renders.push(value));
  assert.deepEqual(requests[0], { view: "history", limit: 100, search: "source", status: "COMPLETED", run_mode: "REFERENCE" });
  assert.deepEqual(state.history.items.map(item => item.run_id), [first.run_id]);
  assert.equal(state.history.nextCursor, "cursor_history_2");
  assert.equal(state.history.hasMore, true);
  assert.equal(state.history.total, 2);
  assert.match(historyView(state, { runId: null, summary: null }, createI18n("en")), /1 of 2 terminal tasks/);
  assert.match(historyView(state, { runId: null, summary: null }, createI18n("en")), /data-history-more/);
  await loadHistory(state, value => renders.push(value), true);
  assert.equal(requests[1].cursor, "cursor_history_2");
  assert.deepEqual(state.history.items.map(item => item.run_id), [first.run_id, second.run_id]);
  assert.equal(state.history.hasMore, false);

  let resolveFilteredPage;
  state.api.listSimulations = query => { requests.push(structuredClone(query)); return new Promise(resolve => { resolveFilteredPage = resolve; }); };
  updateHistoryFilters(state, { query: "source-two" }, value => renders.push(value));
  assert.deepEqual(state.history.items, []);
  assert.equal(state.history.nextCursor, null);
  assert.equal(state.history.hasMore, false);
  resolveFilteredPage({ items: [second], meta: { request_id: "req_history_filtered", next_cursor: null, has_more: false, total: 1 } });
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.equal(requests[2].search, "source-two");
  assert.equal(requests[2].cursor, undefined);
  assert.deepEqual(state.history.items.map(item => item.run_id), [second.run_id]);
  assert.ok(renders.length >= 6);
  const source = await readFile(new URL("../src/ui.ts", import.meta.url), "utf8");
  assert.match(source, /historyScrollTop/);
  assert.match(source, /data-history-more/);
  assert.match(historyView({ ...state, history: { ...state.history, hasMore: true } }, { runId: null, summary: null }, createI18n("zh-CN")), /加载更多/);
});

await test("selecting a completed History task reads its frozen detail, summary, replay, results, alarms, and artifacts without creating a task", async () => {
  const calls = [];
  const artifacts = { artifact_state: "COMMITTED", manifest_sha256: fixtureReferenceProfile.normalized_sha256, items: [{ name: "artifact_manifest.json", required: true, sha256: fixtureReferenceProfile.normalized_sha256, size_bytes: 99 }] };
  const api = {
    getSimulation: async runId => { calls.push(`detail:${runId}`); return structuredClone(fixtureCompletedSimulation); },
    getSummary: async (_runId, agent) => { calls.push(`summary:${agent}`); return { ...structuredClone(fixtureSummary), selection: { agent, segment: ["EARLY", "MIDDLE", "LATE"][agent - 1] } }; },
    getArtifacts: async runId => { calls.push(`artifacts:${runId}`); return artifacts; },
    getReplay: async (_runId, query) => { calls.push(`replay:${query.agent}`); return { points: structuredClone(fixtureSummary.chart.points) }; },
    getResults: async (_runId, query) => { calls.push(`results:${query.agent}`); return { items: structuredClone(fixtureSummary.chart.points) }; },
    getAlarms: async (_runId, query) => { calls.push(`alarms:${query.agent}`); return { items: [] }; },
    subscribeSimulationEvents: () => ({ close() {} }),
    createSimulation: async () => { throw new Error("History replay must not create a simulation"); }
  };
  const taskStore = new SimulationStore(api);
  const state = { api, view: "history", chart: { zoom: 2, pan: 17, focus: 4, series: { truth: true, local: true, global: true, fused: true, interval: true } }, history: { items: [structuredClone(fixtureCompletedSimulation)], loading: false, error: null, query: "", status: "", mode: "", listEpoch: 0, selectionEpoch: 0, selectedRunId: null, detail: null, detailLoading: false, detailError: null, artifacts: null, replay: { agent: 1, loading: false, error: null, data: null, results: null, alarms: null } } };
  const rendered = [];
  const result = await selectHistoryRun(state, taskStore, fixtureCompletedSimulation.run_id, createI18n("en"), options => rendered.push(options), true);
  assert.equal(result, true);
  assert.equal(state.view, "replay");
  assert.equal(state.history.detail.run_id, fixtureCompletedSimulation.run_id);
  assert.ok(state.history.detail.snapshot.parameter_profile);
  assert.equal(state.history.artifacts.artifact_state, "COMMITTED");
  assert.equal(state.history.replay.data.points.length, fixtureSummary.chart.points.length);
  assert.equal(state.history.replay.results.items.length, fixtureSummary.chart.points.length);
  ["detail:run_fixture_completed", "summary:1", "artifacts:run_fixture_completed", "replay:1", "results:1", "alarms:1"].forEach(expected => assert.ok(calls.includes(expected), expected));
  assert.equal(state.chart.zoom, 1);
  assert.equal(state.chart.pan, 0);
  assert.equal(state.chart.focus, null);
  assert.ok(rendered.length >= 3);
  const replay = replayView(state, taskStore.state, createI18n("en"));
  assert.match(replay, /Frozen replay/);
  assert.match(replay, /data-replay-agent="1"/);
  assert.match(replay, /data-chart/);
  assert.match(replay, /artifact_manifest\.json/);
  assert.doesNotMatch(replay, /Custom draft/);
  taskStore.close();
});

await test("History Inspect opens Workspace only after the selected terminal run passes its authoritative result gate", async () => {
  const detail = { ...structuredClone(fixtureCompletedSimulation), parameter_snapshot: structuredClone(fixtureCompletedSimulation.snapshot.parameter_profile) };
  const artifacts = { artifact_state: "COMMITTED", items: [{ name: "artifact_manifest.json", required: true, sha256: "a".repeat(64), size_bytes: 1 }] };
  const api = {
    getSimulation: async runId => ({ ...detail, run_id: runId }),
    getSummary: async (_runId, agent) => ({ ...structuredClone(fixtureSummary), selection: { agent, segment: "EARLY" } }),
    getResults: async runId => ({ run_id: runId, items: structuredClone(fixtureSummary.chart.points.slice(0, 2)) }),
    getAlarms: async runId => ({ run_id: runId, items: [] }),
    getArtifacts: async runId => ({ ...artifacts, run_id: runId }),
    getReplay: async runId => ({ run_id: runId, points: structuredClone(fixtureSummary.chart.points.slice(0, 2)) }),
    subscribeSimulationEvents: () => ({ close() {} })
  };
  const state = { api, view: "history", chart: { zoom: 1, pan: 0, focus: null, series: {} }, history: { items: [detail], aggregateMetrics: {}, loading: false, error: null, query: "", status: "", mode: "", selectedRunId: null, detail: null, detailLoading: false, detailError: null, artifacts: null, replay: { agent: 1, loading: false, error: null, data: null, results: null, alarms: null } } };
  const store = new SimulationStore(api);
  assert.equal(await selectHistoryRun(state, store, detail.run_id, createI18n("en"), () => {}, false, true), true);
  assert.equal(state.view, "workspace");
  assert.equal(state.history.selectedRunId, detail.run_id);
  assert.equal(state.history.detail.run_id, detail.run_id);
  assert.equal(store.state.runId, detail.run_id);
  store.close();

  const rejected = new contract.ApiError("RESULT_NOT_READY", "req_history_gate");
  const failingApi = { ...api, getSummary: async () => { throw rejected; } };
  const failingState = { ...state, api: failingApi, view: "history", history: { ...state.history, items: [detail], selectedRunId: null, detail: null, detailLoading: false, detailError: null, artifacts: null } };
  const failingStore = new SimulationStore(failingApi);
  assert.equal(await selectHistoryRun(failingState, failingStore, detail.run_id, createI18n("en"), () => {}, false, true), false);
  assert.equal(failingState.view, "history");
  assert.equal(failingState.history.selectedRunId, detail.run_id);
  assert.equal(failingState.history.detailError.code, "RESULT_NOT_READY");
  assert.match(historyView(failingState, failingStore.state, createI18n("en")), /RESULT_NOT_READY|Result files are not ready/);
  failingStore.close();
});

await test("History artifact gates keep failed, cancelled, and incomplete tasks read-only and unavailable for replay/export", () => {
  const i18n = createI18n("en");
  const incomplete = { ...structuredClone(fixtureCompletedSimulation), run_id: "run_incomplete", artifact_state: "INCOMPLETE" };
  const cancelled = { ...structuredClone(fixtureCompletedSimulation), run_id: "run_cancelled", status: "CANCELLED", artifact_state: "INCOMPLETE" };
  const baseHistory = { items: [incomplete, cancelled], loading: false, error: null, query: "", status: "", mode: "", selectedRunId: "run_incomplete", detail: incomplete, detailLoading: false, detailError: null, artifacts: { artifact_state: "INCOMPLETE", items: [] }, replay: { agent: 1, loading: false, error: null, data: null, results: null, alarms: null } };
  assert.equal(historyArtifactGate(incomplete, baseHistory.artifacts).ready, false);
  assert.equal(historyArtifactGate(cancelled, baseHistory.artifacts).key, "terminal");
  const replay = replayView({ chart: { series: {} }, history: baseHistory }, { runId: incomplete.run_id, summary: null }, i18n);
  assert.match(replay, /Replay and downloads require verified required result files/);
  assert.match(replay, /data-replay-export disabled/);
  assert.match(replay, /data-artifact-manifest disabled/);
});

await test("Queue cancel invokes the real cancel adapter and refreshes the list instead of only changing HTML", async () => {
  let cancelCalls = 0; let listCalls = 0;
  const api = {
    cancelSimulation: async runId => { cancelCalls += 1; assert.equal(runId, "run_waiting"); return { run_id: runId, status: "CANCELLED" }; },
    listSimulations: async () => { listCalls += 1; return { items: [] }; }
  };
  const state = { api, queue: { items: [{ run_id: "run_waiting", status: "QUEUED", cancellable: true }], loading: false, error: null } };
  const taskStore = { api, state: { runId: null } };
  await cancelQueuedSimulation("run_waiting", state, taskStore, createI18n("en"), { querySelector: () => null }, () => {});
  assert.equal(cancelCalls, 1);
  assert.equal(listCalls, 1);
  assert.deepEqual(state.queue.items, []);
});

await test("Queue cancellation uses the authoritative cancellable capability and disables only a pending run", async () => {
  const english = createI18n("en"); const chinese = createI18n("zh-CN");
  assert.equal(queueRunIsCancellable({ status: "RUNNING", cancellable: true }), true);
  assert.equal(queueRunIsCancellable({ status: "RUNNING", cancellable: false }), false);
  assert.equal(queueRunIsCancellable({ status: "QUEUED" }), false);
  assert.equal(queueRunIsCancellable({ status: "RUNNING" }), false);
  assert.equal(queueRunIsCancellable({ status: "CANCELLING", cancellable: true }), false);
  assert.equal(queueRunIsCancellable({ status: "COMPLETED" }), false);
  assert.equal(queueRunIsCancellable({ status: "QUEUED", legacyCapability: true }), false);

  const active = { run_id: "run_active", status: "RUNNING", cancellable: true, elapsed_ms: 0 };
  const waiting = { run_id: "run_waiting", status: "QUEUED", cancellable: true, queue_position: 1 };
  const state = { queue: { items: [active, waiting], loading: false, error: null, cancellingRunIds: ["run_active"] } };
  const markup = queueView(state, { detail: active, events: [] }, english);
  assert.match(markup, /data-queue-cancel="run_active" disabled aria-busy="true">Cancel current task/);
  assert.match(markup, /data-queue-cancel="run_waiting">Cancel/);
  assert.match(queueView({ queue: { ...state.queue, cancellingRunIds: [] } }, { detail: active, events: [] }, chinese), /取消当前任务/);
  assert.doesNotMatch(queueView({ queue: { items: [{ ...active, status: "CANCELLING", cancellable: true }], loading: false, error: null } }, { detail: { ...active, status: "CANCELLING", cancellable: true }, events: [] }, english), /data-queue-cancel/);

  let cancelCalls = 0; let releaseCancel; let cancellationAccepted = false; let signalCancelStarted;
  const cancelStarted = new Promise(resolve => { signalCancelStarted = resolve; });
  const api = {
    cancelSimulation: async () => { cancelCalls += 1; cancellationAccepted = true; signalCancelStarted(); await new Promise(resolve => { releaseCancel = resolve; }); return { run_id: waiting.run_id, status: "CANCELLING", cancellable: false }; },
    getSimulation: async () => cancellationAccepted ? { ...waiting, status: "CANCELLING", cancellable: false } : structuredClone(waiting),
    listSimulations: async () => ({ items: [{ ...waiting, status: "CANCELLING", cancellable: false }] })
  };
  const pendingState = { api, queue: { items: [waiting], loading: false, error: null, cancellingRunIds: [] } };
  const taskStore = { api, state: { runId: null }, refresh: async () => {} };
  const renderCalls = [];
  const first = cancelQueuedSimulation(waiting.run_id, pendingState, taskStore, english, { querySelector: () => null }, options => { renderCalls.push(options?.source); });
  await cancelStarted;
  assert.deepEqual(pendingState.queue.cancellingRunIds, [waiting.run_id]);
  assert.equal(await cancelQueuedSimulation(waiting.run_id, pendingState, taskStore, english, { querySelector: () => null }, () => {}), false);
  assert.equal(cancelCalls, 1);
  releaseCancel();
  assert.equal(await first, true);
  assert.deepEqual(pendingState.queue.cancellingRunIds, []);
  assert.ok(renderCalls.includes("queue-cancel-pending"));
  assert.ok(renderCalls.includes("queue-cancel-success"));
  assert.ok(renderCalls.includes("queue-cancel-result"));
});

await test("Queue keeps only the selected authoritative cancelling task in its active slot during a list gap", async () => {
  const english = createI18n("en");
  const cancelling = { run_id: "run_cancelling", status: "CANCELLING", cancellable: false, current_stage: "CANCEL_REQUESTED", elapsed_ms: 1200 };
  const waiting = { run_id: "run_waiting_after_gap", status: "QUEUED", cancellable: true, queue_position: 1 };
  const state = { queue: { items: [waiting], loading: false, error: null, cancellingRunIds: [] } };
  const matchingTask = { runId: cancelling.run_id, detail: cancelling, events: [] };
  assert.equal(queueActiveRun(state.queue.items, matchingTask), cancelling);
  const duringGap = queueView(state, matchingTask, english);
  assert.match(duringGap, /<strong>1 \/ 1<\/strong>/);
  assert.match(duringGap, /data-queue-active data-run-id="run_cancelling"/);
  assert.match(duringGap, /Cancelling/);
  assert.doesNotMatch(duringGap, /data-queue-cancel="run_cancelling"/);

  const staleCancelling = { ...cancelling, status: "CANCELLING" };
  const terminal = queueView({ queue: { items: [staleCancelling, waiting], loading: false, error: null, cancellingRunIds: [] } }, { runId: cancelling.run_id, detail: { ...cancelling, status: "CANCELLED" }, events: [] }, english);
  assert.match(terminal, /<strong>0 \/ 1<\/strong>/);
  assert.doesNotMatch(terminal, /data-run-id="run_cancelling"/);
  assert.match(terminal, /run_waiting_after_gap/);
  const activeWithoutWaiting = queueView({ queue: { items: [], loading: false, error: null, cancellingRunIds: [] } }, matchingTask, english);
  assert.match(activeWithoutWaiting, /No waiting tasks\./);
  assert.doesNotMatch(activeWithoutWaiting, /No active or waiting tasks/);
  assert.match(queueView({ queue: { items: [waiting], loading: false, error: null, cancellingRunIds: [] } }, matchingTask, english), /<thead><tr><th>Position<\/th><th>Run ID<\/th><th>Mode \/ parameters<\/th>/);
  const differentRun = queueView(state, { runId: "run_other", detail: cancelling, events: [] }, english);
  assert.match(differentRun, /<strong>0 \/ 1<\/strong>/);
  assert.doesNotMatch(differentRun, /data-run-id="run_cancelling"/);
  const source = await readFile(new URL("../src/ui.ts", import.meta.url), "utf8");
  assert.equal((source.match(/const active = queueActiveRun\(items, task\);/g) ?? []).length, 2);
});

await test("Queue cancellation reconciles authoritative detail for active and waiting runs without duplicate requests", async () => {
  const active = { run_id: "run_active_cancel", status: "RUNNING", cancellable: true, queue_position: null };
  const waiting = { run_id: "run_waiting_cancel", status: "QUEUED", cancellable: true, queue_position: 1 };
  const runs = new Map([[active.run_id, active], [waiting.run_id, waiting]]);
  const calls = [];
  const api = {
    getSimulation: async runId => structuredClone(runs.get(runId)),
    cancelSimulation: async runId => {
      calls.push(runId);
      const current = runs.get(runId);
      const next = { ...current, status: current.status === "RUNNING" ? "CANCELLING" : "CANCELLED", cancellable: false };
      runs.set(runId, next);
      return structuredClone(next);
    },
    listSimulations: async () => ({ items: [...runs.values()].filter(run => ["RUNNING", "CANCELLING", "QUEUED"].includes(run.status)) })
  };
  const state = { api, view: "queue", queue: { items: [active, waiting], loading: false, error: null, cancellingRunIds: [] } };
  const taskStore = { api, state: { runId: null, detail: null }, refresh: async () => {} };
  const i18n = createI18n("en");
  assert.equal(await cancelQueuedSimulation(active.run_id, state, taskStore, i18n, { querySelector: () => null }, () => {}), true);
  assert.equal(state.queue.items.find(item => item.run_id === active.run_id).status, "CANCELLING");
  assert.equal(await cancelQueuedSimulation(waiting.run_id, state, taskStore, i18n, { querySelector: () => null }, () => {}), true);
  assert.equal(calls.filter(runId => runId === waiting.run_id).length, 1);
  assert.equal(await cancelQueuedSimulation(waiting.run_id, state, taskStore, i18n, { querySelector: () => null }, () => {}), false);
  assert.equal(calls.filter(runId => runId === waiting.run_id).length, 1);
  assert.equal(state.queue.error.code, "RUN_NOT_CANCELLABLE");
  assert.equal(state.queue.error.status, 409);
});

await test("Queue requests the real capacity-sized page and retains its pagination metadata", async () => {
  let query;
  const items = Array.from({ length: 12 }, (_, index) => ({ run_id: `run_queue_${index + 1}`, status: index === 0 ? "RUNNING" : "QUEUED", queue_position: index || null }));
  const api = {
    listSimulations: async nextQuery => { query = nextQuery; return { items, meta: { request_id: "req_queue", next_cursor: "unexpected_cursor", has_more: true, total: 12 } }; },
    getSimulation: async runId => ({ ...items.find(item => item.run_id === runId), cancellable: true })
  };
  const state = { api, view: "queue", queue: { items: [], loading: false, error: null, nextCursor: null, hasMore: false, total: null } };
  const taskStore = { state: { runId: "run_queue_1", detail: { run_id: "run_queue_1", status: "RUNNING" } } };
  await loadQueue(state, () => {}, taskStore);
  assert.deepEqual(query, { view: "queue", limit: 11 });
  assert.equal(state.queue.items.length, 11);
  assert.equal(state.queue.nextCursor, "unexpected_cursor");
  assert.equal(state.queue.hasMore, true);
  assert.equal(state.queue.total, 12);
  assert.equal(state.queue.items.every(item => item.cancellable === true), true);
});

await test("SSE retains a bounded run-scoped event trail for Queue and Workspace without trusting server messages", async () => {
  let handlers;
  const api = {
    getSimulation: async runId => ({ run_id: runId, status: "RUNNING", latest_event_id: 0 }),
    subscribeSimulationEvents: (_runId, _lastEventId, nextHandlers) => { handlers = nextHandlers; return { close() {} }; }
  };
  const store = new SimulationStore(api);
  await store.selectRun("run_events");
  handlers.onEvent({ id: 1, type: "simulation.stage", data: { status: "RUNNING", current_stage: "LOCAL_TRAINING", queue_position: null, message: "C:/internal/path" } });
  assert.deepEqual(store.state.events, [{ id: 1, type: "simulation.stage", data: { status: "RUNNING", current_stage: "LOCAL_TRAINING", queue_position: null, message: "C:/internal/path" } }]);
  const firstQueue = queueView({ queue: { items: [{ run_id: "run_events", status: "RUNNING", cancellable: false }], loading: false, error: null } }, store.state, createI18n("en"));
  assert.match(firstQueue, /simulation\.stage/);
  assert.match(firstQueue, /Running · Stage: LOCAL_TRAINING/);
  assert.doesNotMatch(firstQueue, /C:&#x2F;internal&#x2F;path|C:\/internal\/path/);
  for (let id = 2; id <= 105; id += 1) handlers.onEvent({ id, type: "heartbeat", data: { latest_event_id: id } });
  assert.equal(store.state.events.length, 100);
  assert.equal(store.state.events[0].id, 6);
  const queue = queueView({ queue: { items: [{ run_id: "run_events", status: "RUNNING", cancellable: false }], loading: false, error: null } }, store.state, createI18n("en"));
  assert.match(queue, /heartbeat/);
  store.close();
});

await test("an opened event stream becomes connected without inventing an event ID", async () => {
  const originalFetch = globalThis.fetch;
  const originalHeaders = globalThis.Headers;
  let opened = 0;
  globalThis.Headers = class {
    constructor(values = {}) { this.values = { ...values }; }
    set(name, value) { this.values[name] = value; }
  };
  globalThis.fetch = async () => ({ ok: true, status: 200, body: { getReader: () => ({ read: () => new Promise(() => {}) }) } });
  try {
    const api = new LivePlatformApi("/api/v1");
    const subscription = api.subscribeSimulationEvents("run_open", 164, { onOpen: () => { opened += 1; } });
    await Promise.resolve(); await Promise.resolve();
    assert.equal(opened, 1);
    subscription.close();
  } finally {
    if (originalFetch === undefined) delete globalThis.fetch; else globalThis.fetch = originalFetch;
    if (originalHeaders === undefined) delete globalThis.Headers; else globalThis.Headers = originalHeaders;
  }

  const originalSetTimeout = globalThis.setTimeout;
  const originalClearTimeout = globalThis.clearTimeout;
  let handlers;
  globalThis.setTimeout = () => 19;
  globalThis.clearTimeout = () => {};
  try {
    const store = new SimulationStore({
      getSimulation: async () => ({ run_id: "run_terminal_stream", status: "COMPLETED", latest_event_id: 164 }),
      subscribeSimulationEvents: (_runId, lastEventId, nextHandlers) => { assert.equal(lastEventId, 164); handlers = nextHandlers; return { close() {} }; }
    });
    await store.selectRun("run_terminal_stream");
    assert.equal(store.state.connection, "connecting");
    assert.equal(store.state.lastEventId, 164);
    assert.equal(store.state.events.length, 0);
    handlers.onOpen();
    assert.equal(store.state.connection, "connected");
    assert.equal(store.state.lastEventId, 164);
    handlers.onDisconnect();
    assert.equal(store.state.connection, "disconnected");
    assert.equal(store.state.lastEventId, 164);
    handlers.onOpen();
    assert.equal(store.state.connection, "connected");
    assert.equal(store.state.lastEventId, 164);
    store.close();
  } finally {
    globalThis.setTimeout = originalSetTimeout;
    globalThis.clearTimeout = originalClearTimeout;
  }
});

await test("History and Replay deep links load a terminal run through history before selecting its frozen detail", async () => {
  const terminal = structuredClone(fixtureCompletedSimulation);
  const listQueries = [];
  const calls = [];
  const api = {
    listSimulations: async query => { listQueries.push(structuredClone(query)); return { items: query.run_id === terminal.run_id ? [terminal] : [], meta: { next_cursor: null, has_more: false, total: query.run_id === terminal.run_id ? 1 : 0 } }; },
    getSimulation: async runId => { calls.push(`detail:${runId}`); return structuredClone(terminal); },
    getSummary: async (_runId, agent) => ({ ...structuredClone(fixtureSummary), selection: { agent, segment: "EARLY" } }),
    getArtifacts: async runId => { calls.push(`artifacts:${runId}`); return { artifact_state: "COMMITTED", items: [] }; },
    getReplay: async () => ({ points: [] }), getResults: async () => ({ items: [] }), getAlarms: async () => ({ items: [] }),
    subscribeSimulationEvents: () => ({ close() {} }),
    createSimulation: async () => { throw new Error("A deep link must not create a simulation"); }
  };
  const taskStore = new SimulationStore(api);
  const state = { api, view: "replay", chart: { zoom: 1, pan: 0, focus: null, series: {} }, history: { items: [], loading: false, error: null, query: "", status: "", mode: "", nextCursor: null, hasMore: false, total: null, listEpoch: 0, selectionEpoch: 0, selectedRunId: null, detail: null, detailLoading: false, detailError: null, artifacts: null, replay: { agent: 1, loading: false, error: null, data: null, results: null, alarms: null } } };
  const selected = await openHistoryDeepLink(state, taskStore, terminal.run_id, createI18n("en"), () => {}, true);
  assert.equal(selected, true);
  assert.deepEqual(listQueries, [{ view: "history", limit: 1, run_id: terminal.run_id }]);
  assert.equal(state.view, "replay");
  assert.equal(state.history.selectedRunId, terminal.run_id);
  assert.equal(state.history.detail.run_id, terminal.run_id);
  assert.ok(calls.includes(`detail:${terminal.run_id}`));
  assert.ok(calls.includes(`artifacts:${terminal.run_id}`));
  taskStore.close();

  const nonTerminalApi = { listSimulations: async query => ({ items: [{ run_id: query.run_id, status: "RUNNING" }], meta: { next_cursor: null, has_more: false, total: 0 } }) };
  const nonTerminalState = { api: nonTerminalApi, view: "history", history: { items: [terminal], loading: false, error: null, query: "", status: "", mode: "", selectedRunId: terminal.run_id, detail: terminal, detailLoading: false, detailError: null, artifacts: { artifact_state: "COMMITTED", items: [] }, replay: { agent: 1, loading: false, error: null, data: null, results: null, alarms: null } } };
  const result = await openHistoryDeepLink(nonTerminalState, { selectRun: () => { throw new Error("A non-terminal deep link must not select the active task"); } }, "run_not_terminal", createI18n("en"), () => {});
  assert.equal(result, false);
  assert.equal(nonTerminalState.history.selectedRunId, null);
  assert.equal(nonTerminalState.history.detail, null);
  assert.deepEqual(nonTerminalState.history.items, []);
});

await test("result-file terminology is localized across Workspace, History, Replay, and error display", () => {
  const english = createI18n("en");
  const chinese = createI18n("zh-CN");
  assert.equal(english.t("history.artifacts"), "Result files");
  assert.equal(english.t("history.artifactsComplete"), "Required result files verified");
  assert.equal(english.t("history.manifest"), "Result file manifest");
  assert.equal(chinese.t("history.artifacts"), "结果文件");
  assert.equal(chinese.t("history.artifactsComplete"), "必需结果文件已生成并校验");
  assert.equal(chinese.t("history.manifest"), "结果文件清单");
  assert.doesNotMatch(english.t("error.ARTIFACT_WRITE_FAILED"), /artifact/i);
  assert.doesNotMatch(chinese.t("error.ARTIFACT_WRITE_FAILED"), /制品/);

  const incomplete = { ...structuredClone(fixtureCompletedSimulation), artifact_state: "INCOMPLETE" };
  const history = { items: [incomplete], loading: false, error: null, query: "", status: "", mode: "", selectedRunId: incomplete.run_id, detail: incomplete, detailLoading: false, detailError: null, artifacts: { artifact_state: "INCOMPLETE", items: [] }, replay: { agent: 1, loading: false, error: null, data: null, results: null, alarms: null } };
  const workspaceTask = { detail: incomplete, summary: { ...structuredClone(fixtureSummary), artifact_integrity: { status: "INCOMPLETE", manifest_sha256: "a".repeat(64) } }, runId: incomplete.run_id, loading: false, selectedAgent: 1, error: null, lastEventId: null, events: [] };
  const englishMarkup = `${workspaceView({ chart: { series: {} } }, workspaceTask, english)}${historyView({ history }, workspaceTask, english)}${replayView({ history, chart: { series: {} }, replayPlayback: { playing: false, speed: 1, position: 0, timer: null } }, workspaceTask, english)}`;
  const chineseMarkup = `${workspaceView({ chart: { series: {} } }, workspaceTask, chinese)}${historyView({ history }, workspaceTask, chinese)}${replayView({ history, chart: { series: {} }, replayPlayback: { playing: false, speed: 1, position: 0, timer: null } }, workspaceTask, chinese)}`;
  assert.match(englishMarkup, /Result files incomplete/);
  assert.match(englishMarkup, /Result file manifest/);
  assert.doesNotMatch(englishMarkup, />[^<]*(?:Artifact|artifacts?)[^<]*</);
  assert.match(chineseMarkup, /结果文件不完整/);
  assert.match(chineseMarkup, /结果文件清单/);
  assert.doesNotMatch(chineseMarkup, />[^<]*制品[^<]*</);
});

await test("frozen replay transport uses run-scoped replay points for play, pause, speed, and position", () => {
  const points = structuredClone(fixtureSummary.chart.points.slice(0, 3));
  const state = {
    view: "replay",
    chart: { zoom: 1, pan: 0, focus: null, series: { truth: true, local: true, global: true, fused: true, interval: true } },
    replayPlayback: { playing: true, speed: 1, position: 0, timer: null },
    history: {
      items: [structuredClone(fixtureCompletedSimulation)], loading: false, error: null, query: "", status: "", mode: "", selectedRunId: fixtureCompletedSimulation.run_id,
      detail: structuredClone(fixtureCompletedSimulation), detailLoading: false, detailError: null,
      artifacts: { artifact_state: "COMMITTED", items: [{ name: "artifact_manifest.json", required: true, sha256: fixtureReferenceProfile.normalized_sha256, size_bytes: 1 }] },
      replay: { agent: 1, loading: false, error: null, data: { total_points: 2481, points }, results: { items: points }, alarms: { items: [] } }
    }
  };
  assert.equal(setReplayPlaybackPosition(state, points.length, 1), 1);
  assert.equal(state.chart.focus, 1);
  assert.equal(advanceReplayPlayback(state, points.length), false);
  assert.equal(state.replayPlayback.position, 2);
  assert.equal(state.replayPlayback.playing, false);
  const chart = replayChartSummary(structuredClone(fixtureSummary), state.history.replay.data);
  assert.equal(chart.chart.points, state.history.replay.data.points);
  assert.equal(chart.chart.display_point_count, 3);
  assert.equal(chart.chart.original_point_count, 2481);
  const english = replayView(state, { runId: fixtureCompletedSimulation.run_id, summary: structuredClone(fixtureSummary) }, createI18n("en"));
  assert.match(english, /data-replay-play/);
  assert.match(english, /data-replay-speed/);
  assert.match(english, /data-replay-position type="range" min="0" max="2"/);
  assert.match(english, /Paused · Point 3 of 3/);
  const chinese = replayView(state, { runId: fixtureCompletedSimulation.run_id, summary: structuredClone(fixtureSummary) }, createI18n("zh-CN"));
  assert.match(chinese, /倍速/);
  assert.match(chinese, /已暂停 · 第 3 \/ 3 点/);
});

await test("Workspace accepts only its own completed and verified result-file summary", () => {
  const english = createI18n("en");
  const detail = { ...structuredClone(fixtureCompletedSimulation), status: "COMPLETED", artifact_state: "COMMITTED" };
  const verifiedSummary = { ...structuredClone(fixtureSummary), artifact_integrity: { status: "VERIFIED", manifest_sha256: "f".repeat(64) } };
  assert.deepEqual(workspaceResultFilesPresentation(detail, verifiedSummary), { verified: true, incomplete: false });
  const verified = workspaceView({ chart: { series: {} } }, { detail, summary: verifiedSummary, summaryRunId: detail.run_id, runId: detail.run_id, loading: false, selectedAgent: 1, error: null, lastEventId: 164, events: [] }, english);
  assert.match(verified, /Required result files verified/);
  assert.doesNotMatch(verified, /Result files incomplete/);
  const stale = workspaceView({ chart: { series: {} } }, { detail, summary: verifiedSummary, summaryRunId: "run_stale", runId: detail.run_id, loading: false, selectedAgent: 1, error: null, lastEventId: 164, events: [] }, english);
  assert.doesNotMatch(stale, /Required result files verified/);
  assert.match(stale, /Frozen results are not ready|Frozen results are not available/);
});

await test("all defined parameter leaves have localized primary labels, including absolute tension", () => {
  const english = createI18n("en"); const chinese = createI18n("zh-CN");
  const leaves = [];
  const visit = (value, prefix = "") => Object.entries(value ?? {}).forEach(([key, nested]) => {
    const path = prefix ? `${prefix}.${key}` : key;
    if (nested && typeof nested === "object" && !Array.isArray(nested)) visit(nested, path); else leaves.push(path);
  });
  visit(fixtureReferenceProfile.shared_parameters);
  assert.ok(leaves.length >= 67);
  leaves.forEach(path => {
    const leaf = path.split(".").slice(-1)[0];
    assert.doesNotMatch(parameterLeafLabel(leaf, english.t.bind(english)), /_/);
    assert.doesNotMatch(parameterLeafLabel(leaf, chinese.t.bind(chinese)), /_/);
  });
  assert.equal(parameterLeafLabel("absolute_tension_threshold", english.t.bind(english)), "Absolute tension threshold");
  assert.equal(parameterLeafLabel("absolute_tension_threshold", chinese.t.bind(chinese)), "绝对张力阈值");
  assert.equal(parameterLeafLabel("std_floor", english.t.bind(english)), "Standard deviation floor");
  assert.equal(parameterLeafLabel("agent_override_whitelist", english.t.bind(english)), "Agent override whitelist");
  assert.notEqual(parameterLeafLabel("std_floor", english.t.bind(english)), "std floor");
  assert.notEqual(parameterLeafLabel("agent_override_whitelist", english.t.bind(english)), "agent override whitelist");
  assert.equal(parameterLeafLabel("std_floor", chinese.t.bind(chinese)), "标准差下限");
  assert.equal(parameterLeafLabel("agent_override_whitelist", chinese.t.bind(chinese)), "智能体覆盖白名单");
  const profile = { shared_parameters: { interval: { std_floor: 0.01 }, support: { agent_override_whitelist: ["interval.std_floor"] } }, fixed_items: {} };
  const englishReadout = renderParameterReadout(profile, english.t.bind(english));
  const chineseReadout = renderParameterReadout(profile, chinese.t.bind(chinese));
  assert.match(englishReadout, /Standard deviation floor[\s\S]*?<code class="readout-path">interval\.std_floor<\/code>/);
  assert.match(englishReadout, /Agent override whitelist[\s\S]*?<code class="readout-path">support\.agent_override_whitelist<\/code>/);
  assert.match(chineseReadout, /标准差下限[\s\S]*?<code class="readout-path">interval\.std_floor<\/code>/);
  assert.match(chineseReadout, /智能体覆盖白名单[\s\S]*?<code class="readout-path">support\.agent_override_whitelist<\/code>/);
});

await test("History deep links reject malformed, unknown, and non-terminal runs without falling back", async () => {
  const english = createI18n("en");
  const state = { api: { listSimulations: async () => ({ items: [], meta: { total: 0, has_more: false, next_cursor: null } }) }, view: "history", history: { items: [structuredClone(fixtureCompletedSimulation)], loading: false, error: null, query: "", status: "", mode: "", selectedRunId: fixtureCompletedSimulation.run_id, detail: fixtureCompletedSimulation, detailLoading: false, detailError: null, artifacts: null, replay: { agent: 1, loading: false, error: null, data: null, results: null, alarms: null } } };
  const taskStore = { selectRun: () => { throw new Error("A rejected deep link must not select any task."); } };
  assert.equal(await openHistoryDeepLink(state, taskStore, "invalid run id", english, () => {}), false);
  assert.equal(state.history.deepLinkNotice, "history.deepLinkInvalid");
  assert.equal(state.history.selectedRunId, null);
  assert.equal(await openHistoryDeepLink(state, taskStore, "run_unknown", english, () => {}), false);
  assert.equal(state.history.deepLinkNotice, "history.deepLinkUnavailable");
  const source = await readFile(new URL("../src/ui.ts", import.meta.url), "utf8");
  assert.match(source, /window\.addEventListener\("popstate"/);
  assert.match(source, /history\.deepLinkLoadFailed/);
});

await test("Workspace uses an accessible event drawer and frozen diagnostic panels without product mock values", async () => {
  const english = createI18n("en");
  const detail = structuredClone(fixtureCompletedSimulation);
  const summary = { ...structuredClone(fixtureSummary), artifact_integrity: { status: "VERIFIED", manifest_sha256: "b".repeat(64) } };
  const markup = workspaceView({ chart: { series: {} } }, { detail, summary, summaryRunId: detail.run_id, runId: detail.run_id, loading: false, selectedAgent: 1, error: null, lastEventId: 164, connection: "connected", events: [{ id: 164, type: "simulation.completed", data: { status: "COMPLETED" } }] }, english);
  ["FUSION DIAGNOSTICS", "Recent RMSE and interval half-width", "TRACEABILITY", "data-event-drawer", "data-event-drawer-open", "data-copy-hash", "Configure a new run"].forEach(token => assert.match(markup, new RegExp(token)));
  assert.doesNotMatch(markup, /workspace-events|data-event-inline/);
  assert.doesNotMatch(markup, /Create Demo Run|FE-\d+|BE-\d+|QA-\d+/);
  const source = await readFile(new URL("../src/ui.ts", import.meta.url), "utf8");
  const stylesheet = await readFile(new URL("../public/styles.css", import.meta.url), "utf8");
  assert.match(source, /bindEventDrawerInteractions/);
  assert.match(source, /drawDiagnosticCharts/);
  assert.match(stylesheet, /\.event-drawer \{[^}]*position:fixed/);
  assert.match(stylesheet, /\.stage-stepper \{[^}]*grid-template-columns:repeat\(7/);
});

await test("SSE ignores out-of-order events and a reset refreshes the same run monotonically", async () => {
  let handlers; let calls = 0;
  const api = {
    getSimulation: async runId => {
      calls += 1;
      return calls === 1 ? { run_id: runId, status: "RUNNING", current_stage: "LOCAL_TRAINING", latest_event_id: 153 } : { run_id: runId, status: "RUNNING", current_stage: "TESTING", latest_event_id: 160 };
    },
    subscribeSimulationEvents: (_runId, _lastEventId, nextHandlers) => { handlers = nextHandlers; return { close() {} }; }
  };
  const store = new SimulationStore(api);
  await store.selectRun("run_monotonic");
  handlers.onEvent({ id: 156, type: "simulation.stage", data: { status: "RUNNING", current_stage: "ANCHOR_AGGREGATING" } });
  handlers.onEvent({ id: 155, type: "simulation.stage", data: { status: "RUNNING", current_stage: "LOCAL_TRAINING" } });
  assert.equal(store.state.detail.current_stage, "ANCHOR_AGGREGATING");
  assert.equal(store.state.lastEventId, 156);
  handlers.onEvent({ id: 154, type: "stream.reset", data: {} });
  await Promise.resolve(); await Promise.resolve();
  assert.equal(store.state.detail.current_stage, "TESTING");
  assert.equal(store.state.lastEventId, 160);
  assert.equal(store.state.connection, "connected");
  store.close();
});

await test("Completed Workspace refresh reads summary, result rows, alarm timeline, and result-file metadata for one run", async () => {
  const calls = [];
  const run = structuredClone(fixtureCompletedSimulation);
  const api = {
    getSimulation: async runId => { calls.push(`detail:${runId}`); return run; },
    getSummary: async (runId, agent) => { calls.push(`summary:${runId}:${agent}`); return structuredClone(fixtureSummary); },
    getResults: async (runId, query) => { calls.push(`results:${runId}:${query.agent}:${query.sort}`); return { run_id: runId, items: structuredClone(fixtureSummary.chart.points).slice(-3) }; },
    getAlarms: async (runId, query) => { calls.push(`alarms:${runId}:${query.agent}`); return { run_id: runId, items: structuredClone(fixtureAlarms) }; },
    getArtifacts: async runId => { calls.push(`files:${runId}`); return { run_id: runId, artifact_state: "COMMITTED", items: [] }; },
    subscribeSimulationEvents: (_runId, _lastEventId, handlers) => { handlers.onOpen?.(); return { close() {} }; }
  };
  const store = new SimulationStore(api);
  await store.selectRun(run.run_id);
  assert.deepEqual(calls, [`detail:${run.run_id}`, `summary:${run.run_id}:1`, `results:${run.run_id}:1:index_desc`, `alarms:${run.run_id}:1`, `files:${run.run_id}`]);
  assert.equal(store.state.resultsRunId, run.run_id);
  assert.equal(store.state.alarmsRunId, run.run_id);
  assert.equal(store.state.artifactsRunId, run.run_id);
  assert.equal(store.state.connection, "connected");
  store.close();
});

await test("Data uses the current top-level algorithm preprocessing contract and its exact count oracle", () => {
  const english = createI18n("en");
  const preprocessing = datasetPreprocessing(fixtureDatasetValid);
  assert.equal(preprocessing, fixtureDatasetValid.algorithm_preprocessing);
  assert.equal(datasetPreprocessing({ statistics: { algorithm_preprocessing: preprocessing } }), null);
  assert.deepEqual(datasetPreprocessingCounts(fixtureDatasetValid), {
    rawRows: 129438, invalidNumericRows: 0, stopRows: 79444, suspiciousRows: 376, runningRows: 49618, spikeRows: 4502
  });
  const panel = datasetPreflightPanel(fixtureDatasetValid, { percent: null, error: null }, english.t.bind(english));
  ["129,438", "79,444", "376", "49,618", "4,502", "Schema version", "dataset-preflight.summary.v1", "Preprocessing contract", "preprocessing.v1", "reference-compatible", "2026-08-16T08:00:00.000+08:00", "1,000 ms", "median_window"].forEach(value => assert.match(panel.content, new RegExp(value.replace(/[.+?^${}()|[\]\\]/g, "\\$&"))));
  assert.match(panel.content, /1,000 ms \(1,000 — 1,000 ms\)/);
  const chinese = createI18n("zh-CN");
  assert.match(datasetPreflightPanel(fixtureDatasetValid, { percent: null, error: null }, chinese.t.bind(chinese)).content, /1,000 ms \(1,000 — 1,000 ms\)/);
  const zeroSampling = structuredClone(fixtureDatasetValid);
  zeroSampling.algorithm_preprocessing.time.sampling_period_ms = { median: 0, min: 0, max: 1000 };
  assert.match(datasetPreflightPanel(zeroSampling, { percent: null, error: null }, english.t.bind(english)).content, /0 ms \(0 — 1,000 ms\)/);
  const nullSampling = structuredClone(fixtureDatasetValid);
  nullSampling.algorithm_preprocessing.time.sampling_period_ms = { median: null, min: null, max: null };
  assert.match(datasetPreflightPanel(nullSampling, { percent: null, error: null }, english.t.bind(english)).content, /Sampling<\/td><td colspan="2">—<\/td>/);
});

await test("Data intake follows the S1 offline layout and exposes only authoritative validation states", async () => {
  const english = createI18n("en");
  const empty = dataView({ dataset: null, upload: { percent: null, error: null, fileName: null } }, english);
  ["DATASET INTAKE", "Data Import and Strict Validation", "S1 · Offline CSV", "data-dropzone", "upload-icon", "Drop a CSV here or click to browse", "Maximum 500 MB", "Required header", "SELECTED DATASET"].forEach(token => assert.match(empty, new RegExp(token)));
  assert.match(empty, /The source file is stored read-only with SHA-256\. S1 accepts exactly seven columns with no field mapping or silent sorting\./);
  assert.doesNotMatch(empty, /data-stat-grid|validation-table/);

  const uploading = dataView({ dataset: null, upload: { percent: 45, error: null, fileName: "source.csv" } }, english);
  assert.match(uploading, /Uploading source\.csv: 45%/);
  assert.match(uploading, /data-file[^>]*disabled/);
  assert.equal((uploading.match(/role="alert" aria-live="assertive"/g) ?? []).length, 0);

  const queued = dataView({ dataset: fixtureDatasetValidating, upload: { percent: null, error: null, fileName: fixtureDatasetValidating.display_name } }, english);
  assert.match(queued, /Worker preflight/);
  assert.match(queued, /Queued/);
  ["Original filename", "strict-seven-columns\.csv", "job_fixture_preflight", "Queue position", "#1", "Latest event ID", "#27"].forEach(token => assert.match(queued, new RegExp(token)));
  assert.doesNotMatch(queued, /data-stat-grid|Numeric conversion|Source integrity/);

  const runningDataset = { ...fixtureDatasetValidating, preflight: { ...fixtureDatasetValidating.preflight, status: "RUNNING", queue_position: null, stage: "PREPROCESSING", attempt_id: "attempt_running_1", latest_event_id: 28 } };
  const running = dataView({ dataset: runningDataset, upload: { percent: null, error: null, fileName: runningDataset.original_filename } }, english);
  ["Running", "PREPROCESSING", "attempt_running_1", "#28"].forEach(token => assert.match(running, new RegExp(token)));

  const failed = { ...fixtureDatasetValidating, status: "INVALID", error: { code: "INSUFFICIENT_SAMPLES", message: "C:\\internal\\source.csv", stage: "PREPROCESSING", diagnostic_id: "diag_preflight_7" }, preflight: { ...fixtureDatasetValidating.preflight, status: "FAILED", attempt_id: "attempt_failed_1", latest_event_id: 29 } };
  const invalid = dataView({ dataset: failed, upload: { percent: null, error: null, fileName: failed.display_name } }, english);
  assert.match(invalid, /too few usable samples/);
  assert.equal((invalid.match(/role="alert" aria-live="assertive"/g) ?? []).length, 1);
  ["INSUFFICIENT_SAMPLES", "PREPROCESSING", "diag_preflight_7", "attempt_failed_1", "#29"].forEach(token => assert.match(invalid, new RegExp(token)));
  assert.doesNotMatch(invalid, /internal|source\.csv|data-stat-grid/);

  const valid = dataView({ dataset: fixtureDatasetValid, upload: { percent: null, error: null, fileName: fixtureDatasetValid.display_name } }, english);
  assert.match(valid, /data-stat-grid/);
  assert.equal((valid.match(/<article>/g) ?? []).length >= 6, true);
  ["Original filename", "aligned_4dzdl_zl_sd\.csv", "Validation started", "Validation finished", "Required seven-column header", "Numeric conversion", "Time range", "Sampling", "Preprocessing contract", "Schema version", "Filter path", "Preprocessing parameters", "Input SHA-256", "Source integrity", "Worker preflight", "Use this dataset → Parameters", "129,438", "4,502", "dataset-preflight\.summary\.v1", "preprocessing\.v1", "1,000 ms"].forEach(token => assert.match(valid, new RegExp(token)));
  assert.match(dataStatisticsSection(fixtureDatasetValid, english.t.bind(english)), /data-stat-grid/);
  assert.equal(dataStatisticsSection(fixtureDatasetValidating, english.t.bind(english)), "");
  assert.match(dataValidationReport(fixtureDatasetValid, {}, english.t.bind(english)), /summary hash verified/);

  const chinese = createI18n("zh-CN");
  const chineseValid = dataView({ dataset: fixtureDatasetValid, upload: { percent: null, error: null, fileName: fixtureDatasetValid.display_name } }, chinese);
  ["数据集导入", "数据导入与严格校验", "S1 · 离线 CSV", "拖放 CSV 文件到此处", "已选数据集", "校验报告", "算法工作器预检", "使用此数据集 → 参数配置"].forEach(token => assert.match(chineseValid, new RegExp(token)));
  assert.match(chineseValid, /源文件按 SHA-256 只读保存；S1 严格接受七列，不进行字段映射或静默排序。/);
  assert.doesNotMatch(chineseValid, /Drop a CSV|Selected dataset|Validation report|Worker preflight/);

  const source = await readFile(new URL("../src/ui.ts", import.meta.url), "utf8");
  const patch = source.slice(source.indexOf("function patchDataDynamicState"), source.indexOf("function patchHistoryDynamicState"));
  assert.match(patch, /data-data-stats-content/);
  assert.doesNotMatch(patch, /root\.innerHTML/);
  assert.match(source, /dataTransfer\?\.files/);
  const stylesheet = await readFile(new URL("../public/styles.css", import.meta.url), "utf8");
  assert.match(stylesheet, /\.data-layout\s*\{[^}]*grid-template-columns:minmax\(0,1\.55fr\) minmax\(21\.25rem,\.8fr\)[^}]*align-items:stretch/);
  assert.match(stylesheet, /\.data-layout > \.panel \+ \.panel\s*\{[^}]*margin-block-start:0/);
  assert.match(stylesheet, /\.data-upload-panel > :first-child,\.data-dataset-panel > :first-child\s*\{[^}]*margin-block-start:0/);
  assert.match(stylesheet, /\.data-dataset-panel > \.empty\s*\{[^}]*min-block-size:13rem[^}]*place-items:center/);
  assert.match(stylesheet, /@media \(max-width: 1200px\)\s*\{[\s\S]*?\.data-layout\s*\{[^}]*grid-template-columns:1fr/);
});

await test("Workspace diagnostics use frozen summary/results resources, alarms, and real result-file hashes", async () => {
  const english = createI18n("en");
  const summary = structuredClone(fixtureSummary);
  const resultItems = structuredClone(fixtureSummary.chart.points).slice(-3);
  const fusion = workspaceDiagnosticSeries(summary, { items: resultItems }, "fusion");
  assert.equal(fusion.source, "summary");
  assert.equal(fusion.series[0].values[0], .62);
  assert.equal(fusion.series[1].values[0], .9);
  const recent = workspaceDiagnosticSeries(summary, { items: resultItems, window_size: 2 }, "recent");
  assert.equal(recent.source, "results");
  assert.equal(recent.window, 2);
  assert.equal(recent.series[0].values.length, 2);
  assert.equal(recent.series[1].values.length, 2);
  assert.equal(recent.series[2].values.length, 2);
  assert.equal(workspaceDiagnosticSeries(summary, null, "recent").source, "none");
  summary.preprocessing = { raw_rows: 129438, running_rows: 49618, spike_flags: 4502, contract_version: "preprocessing.v1", summary_sha256: "c".repeat(64) };
  summary.split_summary = { agent_1: { training_samples: 11571, calibration_samples: 2479, testing_samples: 2481 } };
  summary.anchor_summary = { public_anchors: 300 };
  summary.chart.points = resultItems.map((point, index) => ({ ...point, LoadStatus: ["Normal", "Light", "Heavy"][index] }));
  const resultFiles = Array.from({ length: 12 }, (_, index) => ({ name: `result_${index + 1}.json`, sha256: "a".repeat(64), size_bytes: index + 1, required: true }));
  const detail = structuredClone(fixtureCompletedSimulation);
  const task = {
    detail, runId: detail.run_id, summary, summaryRunId: detail.run_id, results: { run_id: detail.run_id, items: resultItems }, resultsRunId: detail.run_id,
    alarms: { run_id: detail.run_id, meta: { total: 2 }, items: fixtureAlarms }, alarmsRunId: detail.run_id, artifacts: { run_id: detail.run_id, items: resultFiles, manifest_sha256: "b".repeat(64) }, artifactsRunId: detail.run_id,
    selectedAgent: 1, loading: false, error: null, lastEventId: 164, connection: "connected", events: []
  };
  const markup = workspaceView({ chart: { series: {} } }, task, english);
  ["data-diagnostic-source=\"summary\"", "data-diagnostic-source=\"results\"", "data-alarm-timeline", "data-workspace-traceability", "12 registered result files", "data-workspace-run-live", "data-event-drawer-open", "Configure a new run", "129,438 → 49,618", "Spike flags: 4,502", "11,571 / 2,479 / 2,481", "300", "12 · VERIFIED", "2 of 2 loaded", "Based on 3 displayed points", "Normal 1", "Light 1", "Heavy 1", "View result file manifest and SHA-256"].forEach(token => assert.match(markup, new RegExp(token)));
  ["series-dot truth", "series-dot local", "series-dot global", "series-dot fused", "series-dot interval"].forEach(token => assert.match(markup, new RegExp(token)));
  ["Fusion weight", "Global support", "Diagnostic series legend"].forEach(token => assert.match(markup, new RegExp(token)));
  assert.doesNotMatch(markup, /diagnostic\.fusionAlpha|diagnostic\.globalSupport/);
  assert.match(markup, /workspace-diagnostics-top[\s\S]*?data-diagnostic-panel="fusion"[\s\S]*?data-diagnostic-panel="recent"[\s\S]*?data-workspace-diagnostics-lower[\s\S]*?data-alarm-timeline[\s\S]*?data-workspace-traceability/);
  assert.doesNotMatch(markup, /Normal 78%|Light 14%|Heavy 8%/);
  assert.doesNotMatch(markup, /Create Demo Run|Artifact/);

  const alarmDialog = renderAlarmDetailDialog(fixtureAlarms[0], 0, detail.run_id, english.t.bind(english));
  ["data-alarm-dialog", "aria-labelledby=", "aria-describedby=", "2026-08-16T08:01:23.000\\+08:00", "Load imbalance", "Agent 1", "83", "LOAD_IMBALANCE"].forEach(token => assert.match(alarmDialog, new RegExp(token)));
  const secondAlarmDialog = renderAlarmDetailDialog(fixtureAlarms[1], 1, detail.run_id, english.t.bind(english));
  ["2026-08-16T08:02:48.000\\+08:00", "Unspecified alarm type", "INTERVAL_WIDENING"].forEach(token => assert.match(secondAlarmDialog, new RegExp(token)));
  const chinese = createI18n("zh-CN");
  const chineseAlarmDialog = renderAlarmDetailDialog(fixtureAlarms[0], 0, detail.run_id, chinese.t.bind(chinese));
  ["告警详情", "负载不平衡", "智能体 1"].forEach(token => assert.match(chineseAlarmDialog, new RegExp(token)));
  assert.doesNotMatch(chineseAlarmDialog, /Alarm details|Load imbalance/);
  const overallAlarm = { ...fixtureAlarms[0], alarm_type: "OVERALL" };
  const overallEnglish = renderAlarmDetailDialog(overallAlarm, 0, detail.run_id, english.t.bind(english));
  const overallChinese = renderAlarmDetailDialog(overallAlarm, 0, detail.run_id, chinese.t.bind(chinese));
  assert.match(overallEnglish, /Overall alarm <code>OVERALL<\/code>/);
  assert.match(overallChinese, /整体告警 <code>OVERALL<\/code>/);
  const replayOverall = replayView({ view: "replay", chart: { series: {}, focus: 0 }, history: { selectedRunId: detail.run_id, detail, artifacts: { artifact_state: "COMMITTED", items: resultFiles }, replay: { agent: 1, loading: false, error: null, data: { points: [{ ...summary.chart.points[0], alarm_type: "OVERALL" }] }, alarms: { items: [overallAlarm] } } } }, { runId: detail.run_id, summary, summaryRunId: detail.run_id }, english);
  assert.match(replayOverall, /Overall alarm <code>OVERALL<\/code>/);
  const chineseMarkup = workspaceView({ chart: { series: {} } }, task, chinese);
  ["融合权重", "全局支持度", "诊断序列图例"].forEach(token => assert.match(chineseMarkup, new RegExp(token)));
  assert.doesNotMatch(chineseMarkup, /diagnostic\.fusionAlpha|diagnostic\.globalSupport/);
  const manifestDialog = renderManifestDialog(resultFiles, "VERIFIED", "b".repeat(64), detail.run_id, english.t.bind(english));
  assert.match(manifestDialog, /12 registered result files/);
  assert.match(manifestDialog, /result_12\.json/);
  assert.match(manifestDialog, /aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/);
  const missingSummary = { ...summary, preprocessing: {}, split_summary: {}, anchor_summary: {}, chart: { ...summary.chart, points: [] } };
  const missing = workspaceView({ chart: { series: {} } }, { ...task, summary: missingSummary, artifacts: null, artifactsRunId: null, alarms: { run_id: detail.run_id, items: [] } }, english);
  assert.match(missing, /Load-status distribution is unavailable for this task/);
  assert.match(missing, /Result-file metadata is unavailable for this task/);
  const missingDiagnostics = missing.slice(missing.indexOf('<div class="workspace-diagnostics">'), missing.indexOf('<div class="event-drawer-backdrop"'));
  assert.doesNotMatch(missingDiagnostics, /129,438|49,618|4,502|11,571|300/);
  const stylesheet = await readFile(new URL("../public/styles.css", import.meta.url), "utf8");
  assert.match(stylesheet, /\.workspace-diagnostics-lower\s*\{[^}]*grid-template-columns:repeat\(2,minmax\(0,1fr\)\)/);
  assert.match(stylesheet, /\.workspace-diagnostics-top\s*\{[^}]*align-items:stretch/);
  assert.match(stylesheet, /\.workspace-diagnostics-top > \.diagnostic-series,\.workspace-diagnostics-lower > \.panel\s*\{[^}]*block-size:100%/);
  assert.match(stylesheet, /\.workspace-diagnostics-top > \.diagnostic-series,\.workspace-diagnostics-lower > \.panel\s*\{[^}]*margin-block-start:0/);
  assert.match(stylesheet, /@media \(max-width:1200px\)\s*\{[^}]*\.workspace-diagnostics-top,\.workspace-diagnostics-lower\s*\{[^}]*grid-template-columns:1fr/);
  assert.match(stylesheet, /\.alarm-list\s*\{[^}]*max-block-size:22rem[^}]*overflow-y:auto[^}]*scrollbar-gutter:stable[^}]*overscroll-behavior:contain/);
  assert.match(stylesheet, /@media \(max-width:780px\)\s*\{[^}]*\.workspace-diagnostics-lower/);
  assert.match(stylesheet, /\.series-dot\.truth\s*\{[^}]*background:var\(--text\)/);
  assert.match(stylesheet, /\.series-dot\.local\s*\{[^}]*background:var\(--pink\)/);
  assert.match(stylesheet, /\.series-dot\.global\s*\{[^}]*background:var\(--amber\)/);
  assert.match(stylesheet, /\.series-dot\.fused\s*\{[^}]*background:var\(--cyan\)/);
  assert.match(stylesheet, /\.series-dot\.interval\s*\{[^}]*border:1px solid var\(--cyan\)/);
  const source = await readFile(new URL("../src/ui.ts", import.meta.url), "utf8");
  assert.match(source, /const colors = kind === "fusion" \? \["#27d7e7", "#f5b95d"\] : \["#f06aa6", "#f5b95d", "#27d7e7"\]/);
  assert.doesNotMatch(source, /#a879ff/);
});

await test("Workspace diagnostics follow the focused or alarm-selected frozen result row without fallback", async () => {
  const english = createI18n("en");
  const detail = structuredClone(fixtureCompletedSimulation);
  const points = [
    { ...structuredClone(fixtureSummary.chart.points[0]), OriginalRunningIndex: 801, FusionAlpha: 0, GlobalSupport: .96, RecentLocalRMSE: 0, RecentGlobalRMSE: .98, FusedHalfWidth: .14 },
    { ...structuredClone(fixtureSummary.chart.points[1]), OriginalRunningIndex: 802, FusionAlpha: .85, GlobalSupport: .97, RecentLocalRMSE: .62, RecentGlobalRMSE: .71, FusedHalfWidth: .19 },
    { ...structuredClone(fixtureSummary.chart.points[2]), OriginalRunningIndex: 14062, FusionAlpha: .33, GlobalSupport: .91, RecentLocalRMSE: .41, RecentGlobalRMSE: .52, FusedHalfWidth: .27 }
  ];
  const summary = { ...structuredClone(fixtureSummary), chart: { ...structuredClone(fixtureSummary.chart), points } };
  const result802 = { ...points[1], result_row_index: 2 };
  const results = { run_id: detail.run_id, items: [points[0], result802], window_size: 3 };
  const focused = { chart: { series: {}, focus: 0 } };
  assert.deepEqual(workspaceDiagnosticSelection(summary, results, null, focused, detail.run_id), { requested: true, locator: "801", point: points[0] });
  const task = { detail, runId: detail.run_id, summary, summaryRunId: detail.run_id, results, resultsRunId: detail.run_id, alarms: { run_id: detail.run_id, items: [{ OriginalRunningIndex: 802 }, { original_running_index: 14062, result_locator: { agent: 1, original_running_index: 14062 } }] }, alarmsRunId: detail.run_id, artifacts: null, artifactsRunId: null, selectedAgent: 1, loading: false, error: null, lastEventId: 164, connection: "connected", events: [] };
  const first = workspaceView(focused, task, english);
  ["data-diagnostic-selected=\"801\"", "data-diagnostic-value=\"fusion-0\">0.00", "data-diagnostic-value=\"fusion-1\">0.96", "data-diagnostic-value=\"recent-0\">0.00", "data-diagnostic-value=\"recent-1\">0.98"].forEach(token => assert.match(first, new RegExp(token)));
  focused.chart.focus = 1;
  assert.strictEqual(workspaceDiagnosticSelection(summary, results, task.alarms, focused, detail.run_id).point, result802);
  const second = workspaceView(focused, task, english);
  ["data-diagnostic-selected=\"802\"", "data-diagnostic-value=\"fusion-0\">0.85", "data-diagnostic-value=\"fusion-1\">0.97", "data-diagnostic-value=\"recent-2\">0.19"].forEach(token => assert.match(second, new RegExp(token)));
  focused.chart.focus = 2;
  assert.deepEqual(workspaceDiagnosticSelection(summary, results, task.alarms, focused, detail.run_id), { requested: true, locator: "14062", point: points[2] });
  const keyboard14062 = workspaceView(focused, task, english);
  ["data-diagnostic-selected=\"14062\"", "data-diagnostic-value=\"fusion-0\">0.33", "data-diagnostic-value=\"fusion-1\">0.91", "data-diagnostic-value=\"recent-0\">0.41", "data-diagnostic-value=\"recent-1\">0.52", "data-diagnostic-value=\"recent-2\">0.27"].forEach(token => assert.match(keyboard14062, new RegExp(token)));
  const alarmSelected = { chart: { series: {}, focus: 0 }, workspaceAlarmDialog: { runId: detail.run_id, index: 0 } };
  assert.equal(workspaceDiagnosticSelection(summary, results, task.alarms, alarmSelected, detail.run_id).locator, "802");
  const alarm14062 = { chart: { series: {}, zoom: 3, pan: 27, focus: 0 }, workspaceAlarmDialog: { runId: detail.run_id, index: 1 } };
  assert.deepEqual(workspaceDiagnosticSelection(summary, results, task.alarms, alarm14062, detail.run_id), { requested: true, locator: "14062", point: points[2] });
  const alarmMarkup = workspaceView(alarm14062, task, english);
  ["data-diagnostic-selected=\"14062\"", "data-diagnostic-value=\"fusion-0\">0.33", "data-diagnostic-value=\"fusion-1\">0.91", "data-diagnostic-value=\"recent-0\">0.41", "data-diagnostic-value=\"recent-1\">0.52", "data-diagnostic-value=\"recent-2\">0.27"].forEach(token => assert.match(alarmMarkup, new RegExp(token)));
  assert.deepEqual({ zoom: alarm14062.chart.zoom, pan: alarm14062.chart.pan, focus: alarm14062.chart.focus }, { zoom: 3, pan: 27, focus: 0 });
  const unavailableSummary = { ...summary, chart: { ...summary.chart, points: [points[0]] } };
  const unavailable = workspaceView({ chart: { series: {}, focus: 1 } }, { ...task, summary: unavailableSummary, results: { ...results, items: [points[0]] } }, english);
  assert.match(unavailable, /The selected result point is unavailable for this task\./);
  assert.doesNotMatch(unavailable, /data-diagnostic-value="fusion-0">0\.85/);
  const source = await readFile(new URL("../src/ui.ts", import.meta.url), "utf8");
  assert.match(source, /canvas\.addEventListener\("keydown"/);
  assert.match(source, /synchronizeDiagnostics\(\)/);
  assert.match(source, /data-workspace-agent/);
});

await test("History renders the authoritative aggregate Fused RMSE independently from the selected Agent summary", async () => {
  const run = { ...structuredClone(fixtureCompletedSimulation), finished_at: "2026-08-22T00:00:00Z", summary_metric: { RMSE: 1.999 } };
  const history = { items: [run], aggregateMetrics: { [run.run_id]: 0.7882963857446627 }, loading: false, error: null, query: "", status: "", mode: "", nextCursor: null, hasMore: false, total: 1, selectedRunId: null, detail: null, detailLoading: false, detailError: null, deepLinkNotice: null, artifacts: null, replay: { agent: 1, loading: false, error: null, data: null, results: null, alarms: null } };
  const selectedAgentTask = { runId: run.run_id, summaryRunId: run.run_id, summary: { metrics: { RMSE: 1.999 } } };
  const alternativeAgentTask = { runId: run.run_id, summaryRunId: run.run_id, summary: { metrics: { RMSE: 4.444 } } };
  const english = createI18n("en");
  const before = historyView({ history }, selectedAgentTask, english);
  const after = historyView({ history }, alternativeAgentTask, english);
  [before, after].forEach(markup => {
    assert.match(markup, /0\.7882963857446627/);
    assert.doesNotMatch(markup, /1\.999|4\.444/);
  });
  const aggregateCalls = [];
  const state = { history: { ...history, items: [], aggregateMetrics: {} }, api: { listSimulations: async () => ({ items: [run], meta: { total: 1 } }), getSummary: async (runId, agent) => { aggregateCalls.push([runId, agent]); return { metrics: { RMSE: 0.7882963857446627 } }; } } };
  await loadHistory(state, () => {});
  assert.deepEqual(aggregateCalls, [[run.run_id, "aggregate"]]);
  assert.equal(state.history.aggregateMetrics[run.run_id], 0.7882963857446627);
});

await test("Replay keyboard point selection follows the same frozen range without changing chart context", () => {
  const state = { chart: { zoom: 3, pan: 27, focus: 0 }, replayPlayback: { playing: false, speed: 1, position: 0, timer: null } };
  const next = replayKeyboardPosition("ArrowRight", 0, 3);
  assert.equal(next, 1);
  assert.equal(setReplayPlaybackPosition(state, 3, next), 1);
  assert.equal(replayKeyboardPosition("End", state.replayPlayback.position, 3), 2);
  assert.equal(setReplayPlaybackPosition(state, 3, 2), 2);
  assert.equal(replayKeyboardPosition("Home", state.replayPlayback.position, 3), 0);
  assert.equal(setReplayPlaybackPosition(state, 3, 0), 0);
  assert.deepEqual({ zoom: state.chart.zoom, pan: state.chart.pan, focus: state.chart.focus }, { zoom: 3, pan: 27, focus: 0 });
});

await test("R24 keeps Data and the frozen Workspace rail responsive at 1024 without global grid alignment dependencies", async () => {
  const english = createI18n("en");
  const workspace = workspaceView({ chart: { series: {} } }, { detail: fixtureCompletedSimulation, summary: fixtureSummary, summaryRunId: fixtureCompletedSimulation.run_id, runId: fixtureCompletedSimulation.run_id, selectedAgent: 1, loading: false, error: null, lastEventId: 12, events: [] }, english);
  ["Dataset", "Parameter version", "Mapping", "Snapshot hash", "rail-group-icon cyan", "rail-group-icon pink", "rail-group-icon green", "rail-group-icon amber", "data-hash-value"].forEach(token => assert.match(workspace, new RegExp(token)));
  const emptyData = dataView({ dataset: null, upload: { percent: null, error: null, fileName: null } }, english);
  const selectedData = dataView({ dataset: fixtureDatasetValid, upload: { percent: null, error: null, fileName: fixtureDatasetValid.display_name } }, english);
  assert.match(emptyData, /class="grid-2 data-layout"/);
  assert.match(selectedData, /class="grid-2 data-layout"/);
  const stylesheet = await readFile(new URL("../public/styles.css", import.meta.url), "utf8");
  assert.match(stylesheet, /@media \(max-width: 1200px\)\s*\{[\s\S]*?\.workspace \{ grid-template-columns:1fr; \}[\s\S]*?\.parameter-rail \{ display:block; \}/);
  assert.match(stylesheet, /@media \(max-width: 1200px\)\s*\{[\s\S]*?\.data-layout \{ grid-template-columns:1fr; \}/);
  const desktopDataLayout = ".data-layout { grid-template-columns:minmax(0,1.55fr) minmax(21.25rem,.8fr); align-items:stretch; }";
  const responsiveMedia = "@media (max-width: 1200px)";
  const desktopDataLayoutIndex = stylesheet.indexOf(desktopDataLayout);
  const responsiveMediaIndex = stylesheet.indexOf(responsiveMedia);
  const responsiveDataLayoutIndex = stylesheet.indexOf(".data-layout { grid-template-columns:1fr; }", responsiveMediaIndex);
  assert.ok(desktopDataLayoutIndex >= 0 && responsiveMediaIndex >= 0 && responsiveDataLayoutIndex >= 0);
  assert.ok(desktopDataLayoutIndex < responsiveDataLayoutIndex);
  assert.match(stylesheet, /\.workspace-diagnostics-top > \.diagnostic-series,\.workspace-diagnostics-lower > \.panel\s*\{[^}]*margin-block-start:0/);
  assert.match(stylesheet, /@media \(max-width:1200px\)\s*\{[^}]*\.workspace-diagnostics-top,\.workspace-diagnostics-lower\s*\{[^}]*grid-template-columns:1fr/);
});

await test("Workspace alarm and manifest dialogs return focus to their real current-run triggers", () => {
  const eventListeners = new Map();
  const alarmTrigger = { dataset: { alarmOpen: "0", alarmRunId: "run_dialog" }, focusCalls: [], addEventListener: (type, handler) => eventListeners.set(`alarm-${type}`, handler), focus(options) { this.focusCalls.push(options); } };
  const alarmDialogListeners = new Map();
  const alarmDialog = { dataset: { open: "true", alarmIndex: "0", alarmRunId: "run_dialog" }, open: false, showModalCalls: 0, focusCalls: [], showModal() { this.showModalCalls += 1; this.open = true; }, focus(options) { this.focusCalls.push(options); }, close() { this.open = false; alarmDialogListeners.get("close")?.(); }, addEventListener: (type, handler) => alarmDialogListeners.set(type, handler) };
  const alarmClose = { addEventListener: (type, handler) => eventListeners.set(`alarm-close-${type}`, handler) };
  const alarmRoot = { querySelectorAll: selector => selector === "[data-alarm-open]" ? [alarmTrigger] : [], querySelector: selector => selector === "[data-alarm-dialog]" ? alarmDialog : selector === "[data-alarm-dialog-close]" ? alarmClose : selector.startsWith("[data-alarm-open=") ? alarmTrigger : null };
  const state = { workspaceAlarmDialog: { runId: "run_dialog", index: 0 }, workspaceManifestDialog: null };
  let alarmRenders = 0;
  bindAlarmDialogInteractions(alarmRoot, state, () => { alarmRenders += 1; });
  assert.equal(alarmDialog.showModalCalls, 1);
  assert.deepEqual(alarmDialog.focusCalls, [{ preventScroll: true }]);
  eventListeners.get("alarm-click")();
  assert.equal(alarmRenders, 1);
  assert.deepEqual(state.workspaceAlarmDialog, { runId: "run_dialog", index: 0 });
  let escapePrevented = false;
  alarmDialogListeners.get("keydown")({ key: "Escape", preventDefault() { escapePrevented = true; } });
  assert.equal(escapePrevented, true);
  assert.equal(state.workspaceAlarmDialog, null);
  assert.deepEqual(alarmTrigger.focusCalls, [{ preventScroll: true }]);

  const manifestListeners = new Map();
  const manifestTrigger = { dataset: { manifestRunId: "run_dialog" }, focusCalls: [], addEventListener: (type, handler) => manifestListeners.set(`manifest-${type}`, handler), focus(options) { this.focusCalls.push(options); } };
  const manifestDialogListeners = new Map();
  const manifestDialog = { dataset: { open: "true", manifestRunId: "run_dialog" }, open: false, showModal() { this.open = true; }, focus() {}, close() { this.open = false; manifestDialogListeners.get("close")?.(); }, addEventListener: (type, handler) => manifestDialogListeners.set(type, handler) };
  const manifestClose = { addEventListener: (type, handler) => manifestListeners.set(`manifest-close-${type}`, handler) };
  const manifestRoot = { querySelectorAll: selector => selector === "[data-manifest-dialog-open]" ? [manifestTrigger] : [], querySelector: selector => selector === "[data-manifest-dialog]" ? manifestDialog : selector === "[data-manifest-dialog-close]" ? manifestClose : selector.startsWith("[data-manifest-dialog-open]") ? manifestTrigger : null };
  bindManifestDialogInteractions(manifestRoot, state, () => {});
  manifestListeners.get("manifest-close-click")();
  assert.equal(state.workspaceManifestDialog, null);
  assert.deepEqual(manifestTrigger.focusCalls, [{ preventScroll: true }]);
});

await test("Queue waiting capacity uses the authoritative SSE count across shell and Queue projections", async () => {
  assert.equal(queueWaitingCount([{ status: "QUEUED" }, { status: "RUNNING" }, { status: "queued" }]), 2);
  assert.equal(queueWaitingCount([]), 0);
  const queueState = { queue: { items: [{ run_id: "run_current", status: "RUNNING" }], waitingCount: 0 } };
  synchronizeQueueWaitingCount(queueState, { events: [{ data: { queued_count: 2 } }] });
  assert.equal(queueWaitingCount(queueState.queue), 2);
  const english = createI18n("en");
  const task = { detail: { run_id: "run_current", status: "RUNNING" }, readiness: { status: "ready", checks: { worker: "ok", database: "ok" } }, events: [] };
  assert.match(systemStrip(task.detail, null, task, english.t.bind(english), queueWaitingCount(queueState.queue)), /Queue: <b>2 \/ 10<\/b>/);
  assert.match(queueView(queueState, task, english), /<strong>2 \/ 10<\/strong>/);
  synchronizeQueueWaitingCount(queueState, { events: [{ data: { queued_count: 0 } }] });
  assert.equal(queueWaitingCount(queueState.queue), 0);
  const source = await readFile(new URL("../src/ui.ts", import.meta.url), "utf8");
  const stylesheet = await readFile(new URL("../public/styles.css", import.meta.url), "utf8");
  assert.match(source, /data-queue-nav-count/);
  assert.match(source, /queueWaitingCount\(state\.queue\)/);
  assert.match(source, /synchronizeQueueWaitingCount\(state, nextTask\)/);
  assert.match(source, /window\.setInterval\(\(\) => \{ void loadQueue\(state, render, taskStore\); \}, 10000\)/);
  assert.match(stylesheet, /\.nav-count\s*\{/);
  assert.doesNotMatch(source, /Queue operations will be connected/);
});

await test("Queue removes a cancelled active run and projects the authoritative promoted run without a stale count", () => {
  const english = createI18n("en");
  const state = { queue: { items: [{ run_id: "run_cancelled", status: "CANCELLING", cancellable: false }, { run_id: "run_promoted", status: "QUEUED", queue_position: 1, cancellable: true }], waitingCount: 1 } };
  synchronizeQueueWaitingCount(state, { events: [{ data: { queued_count: 0 } }] });
  synchronizeQueueProjection(state, { detail: { run_id: "run_cancelled", status: "CANCELLED" } });
  synchronizeQueueProjection(state, { detail: { run_id: "run_promoted", status: "RUNNING", current_stage: "PREPROCESSING", cancellable: true } });
  const task = { runId: "run_promoted", detail: { run_id: "run_promoted", status: "RUNNING", current_stage: "PREPROCESSING", cancellable: true }, events: [] };
  const markup = queueView(state, task, english);
  assert.equal(queueWaitingCount(state.queue), 0);
  assert.match(markup, /<strong>1 \/ 1<\/strong>/);
  assert.match(markup, /<strong>0 \/ 10<\/strong>/);
  assert.match(markup, /run_promoted/);
  assert.doesNotMatch(markup, /run_cancelled|Cancelling/);
});

await test("Production build metadata identifies the frozen API 0.4 contract", async () => {
  const buildScript = await readFile(new URL("../scripts/build.mjs", import.meta.url), "utf8");
  const contractSource = await readFile(new URL("../src/api/contract.ts", import.meta.url), "utf8");
  assert.match(buildScript, /api_contract_version:\s*"0\.4"/);
  assert.doesNotMatch(buildScript, /api_contract_version:\s*"0\.3"/);
  assert.match(contractSource, /^\/\* API contract v0\.4 DTOs shared by the live and testing-only adapters\. \*\//);
});
