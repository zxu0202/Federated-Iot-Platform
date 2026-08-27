import { isTerminalStatus } from "../api/contract.js";

// SSE is an incremental projection for the selected run; omitted fields retain the last REST detail value.
function projectSseEvent(detail, runId, event) {
  if (!detail || (event?.data?.run_id && event.data.run_id !== runId)) return detail;
  const data = event?.data ?? {};
  const has = key => Object.prototype.hasOwnProperty.call(data, key);
  return {
    ...detail,
    ...(has("status") ? { status: data.status } : {}),
    ...(has("current_stage") ? { current_stage: data.current_stage } : {}),
    ...(has("queue_position") ? { queue_position: data.queue_position } : {}),
    ...(event?.id !== undefined && event?.id !== null ? { latest_event_id: event.id } : {})
  };
}

function monotonicEventId(previous, candidate) {
  const hasPrevious = previous !== null && previous !== undefined && previous !== "";
  const hasCandidate = candidate !== null && candidate !== undefined && candidate !== "";
  if (!hasPrevious && !hasCandidate) return null;
  if (!hasPrevious) return candidate;
  if (!hasCandidate) return previous;
  const previousNumber = Number(previous);
  const candidateNumber = Number(candidate);
  if (Number.isFinite(previousNumber) && Number.isFinite(candidateNumber)) return Math.max(previousNumber, candidateNumber);
  return candidate;
}

// Reconnects can deliver events out of order, so only strictly newer identified events may change task state.
function isNewerEventId(previous, candidate) {
  if (candidate === null || candidate === undefined || candidate === "") return true;
  if (previous === null || previous === undefined || previous === "") return true;
  const previousNumber = Number(previous);
  const candidateNumber = Number(candidate);
  if (Number.isFinite(previousNumber) && Number.isFinite(candidateNumber)) return candidateNumber > previousNumber;
  return String(candidate) !== String(previous);
}

export class SimulationStore {
  api: any;
  state: any;
  listeners: Set<(state: any, notification: { source: string }) => void>;
  subscription: { close: () => void } | null;
  requestController: AbortController | null;
  readinessController: AbortController | null;
  readinessTimer: ReturnType<typeof setInterval> | null;
  reconnectTimer: ReturnType<typeof setTimeout> | null;

  constructor(api: any) { this.api = api; this.state = { runId: null, detail: null, summary: null, summaryRunId: null, results: null, resultsRunId: null, resultsError: null, alarms: null, alarmsRunId: null, alarmsError: null, artifacts: null, artifactsRunId: null, artifactsError: null, selectedAgent: 1, readiness: null, readinessLoading: false, readinessError: null, lastEventId: null, events: [], connection: "idle", loading: false, error: null }; this.listeners = new Set(); this.subscription = null; this.requestController = null; this.readinessController = null; this.readinessTimer = null; this.reconnectTimer = null; }
  subscribe(listener) { this.listeners.add(listener); listener(this.state, { source: "initial" }); return () => this.listeners.delete(listener); }
  emit(change, source = "state") { this.state = { ...this.state, ...change }; this.listeners.forEach(listener => listener(this.state, { source })); }
  closeRunSubscription() { this.subscription?.close(); this.subscription = null; this.requestController?.abort(); this.requestController = null; if (this.reconnectTimer !== null) clearTimeout(this.reconnectTimer); this.reconnectTimer = null; }
  close() { this.closeRunSubscription(); this.readinessController?.abort(); this.readinessController = null; if (this.readinessTimer !== null) clearInterval(this.readinessTimer); this.readinessTimer = null; }
  async selectRun(runId) {
    this.closeRunSubscription(); this.emit({ runId, detail: null, summary: null, summaryRunId: null, results: null, resultsRunId: null, resultsError: null, alarms: null, alarmsRunId: null, alarmsError: null, artifacts: null, artifactsRunId: null, artifactsError: null, selectedAgent: 1, lastEventId: null, events: [], connection: "connecting", loading: true, error: null }, "run-selection");
    await this.refresh(); this.connect();
  }
  async selectAgent(agent) {
    if (![1, 2, 3].includes(agent)) return;
    if (this.state.selectedAgent === agent && this.state.summary?.selection?.agent === agent) return;
    this.emit({ selectedAgent: agent, summary: null, summaryRunId: null, results: null, resultsRunId: null, resultsError: null, alarms: null, alarmsRunId: null, alarmsError: null, loading: Boolean(this.state.runId), error: null }, "agent-selection");
    await this.refresh();
  }
  startReadinessRefresh(intervalMs = 10000) {
    if (this.readinessTimer !== null) return;
    void this.refreshReadiness();
    this.readinessTimer = setInterval(() => { void this.refreshReadiness(); }, intervalMs);
  }
  async refreshReadiness() {
    if (this.state.readinessLoading) return;
    if (!this.api.getReadiness) {
      this.emit({ readiness: null, readinessLoading: false, readinessError: new Error("Readiness endpoint is unavailable.") }, "readiness");
      return;
    }
    this.readinessController?.abort();
    const controller = new AbortController();
    this.readinessController = controller;
    this.emit({ readiness: null, readinessLoading: true, readinessError: null }, "readiness");
    try {
      const readiness = await this.api.getReadiness(controller.signal);
      if (this.readinessController !== controller) return;
      this.emit({ readiness, readinessLoading: false, readinessError: null }, "readiness");
      this.readinessController = null;
    } catch (error) {
      if (this.readinessController === controller && (!(error instanceof Error) || error.name !== "AbortError")) {
        this.emit({ readiness: null, readinessLoading: false, readinessError: error }, "readiness");
        this.readinessController = null;
      }
    }
  }
  async refresh() {
    if (!this.state.runId) return;
    const runId = this.state.runId;
    const selectedAgent = this.state.selectedAgent;
    this.requestController?.abort(); this.requestController = new AbortController();
    try {
      const detail = await this.api.getSimulation(runId, this.requestController.signal);
      let summary: any = null; let results: any = null; let alarms: any = null; let artifacts: any = null;
      let summaryError: any = null; let resultsError: any = null; let alarmsError: any = null; let artifactsError: any = null;
      if (detail.status === "COMPLETED") {
        // Result pages are intentionally bounded; selected diagnostics may use only the same run's frozen summary samples.
        const [summaryResponse, resultsResponse, alarmsResponse, artifactsResponse] = await Promise.allSettled([
          this.api.getSummary ? this.api.getSummary(runId, selectedAgent, this.requestController.signal) : Promise.resolve(null),
          this.api.getResults ? this.api.getResults(runId, { agent: selectedAgent, limit: 100, sort: "index_desc" }, this.requestController.signal) : Promise.resolve(null),
          this.api.getAlarms ? this.api.getAlarms(runId, { agent: selectedAgent, limit: 100 }, this.requestController.signal) : Promise.resolve(null),
          this.api.getArtifacts ? this.api.getArtifacts(runId, this.requestController.signal) : Promise.resolve(null)
        ]);
        if (summaryResponse.status === "fulfilled") summary = summaryResponse.value; else summaryError = summaryResponse.reason;
        if (resultsResponse.status === "fulfilled") results = resultsResponse.value; else resultsError = resultsResponse.reason;
        if (alarmsResponse.status === "fulfilled") alarms = alarmsResponse.value; else alarmsError = alarmsResponse.reason;
        if (artifactsResponse.status === "fulfilled") artifacts = artifactsResponse.value; else artifactsError = artifactsResponse.reason;
      }
      if (runId !== this.state.runId || selectedAgent !== this.state.selectedAgent) return;
      if (summary?.selection?.agent !== undefined && summary.selection.agent !== selectedAgent) throw new Error("Summary selection does not match the selected Agent.");
      const lastEventId = monotonicEventId(this.state.lastEventId, detail.latest_event_id);
      const projectedDetail = lastEventId === null ? detail : { ...detail, latest_event_id: lastEventId };
      this.emit({ detail: projectedDetail, summary, summaryRunId: summary ? runId : null, results, resultsRunId: results ? runId : null, resultsError, alarms, alarmsRunId: alarms ? runId : null, alarmsError, artifacts, artifactsRunId: artifacts ? runId : null, artifactsError, loading: false, error: summaryError, lastEventId }, "simulation-refresh");
    } catch (error) { if (runId === this.state.runId && selectedAgent === this.state.selectedAgent && (!(error instanceof Error) || error.name !== "AbortError")) this.emit({ loading: false, error }, "simulation-refresh"); }
  }
  connect() {
    const runId = this.state.runId; if (!runId) return;
    this.subscription = this.api.subscribeSimulationEvents(runId, this.state.lastEventId, {
      onOpen: () => {
        if (runId !== this.state.runId) return;
        this.emit({ connection: "connected" }, "sse");
      },
      onEvent: event => {
        if (runId !== this.state.runId) return;
        if (event.type === "stream.reset") {
          this.emit({ connection: "connected" }, "sse");
          void this.refresh();
          return;
        }
        if (!isNewerEventId(this.state.lastEventId, event?.id)) return;
        const events = [...(Array.isArray(this.state.events) ? this.state.events : []), { id: event.id ?? null, type: event.type ?? "message", data: event.data ?? {} }].slice(-100);
        const detail = projectSseEvent(this.state.detail, runId, event);
        this.emit({ detail, lastEventId: event.id ?? this.state.lastEventId, events, connection: "connected" }, "sse");
        if (event.data?.status || isTerminalStatus(event.data?.status)) void this.refresh();
      },
      onDisconnect: () => {
        if (runId !== this.state.runId) return;
        this.emit({ connection: "disconnected" }, "sse");
        this.reconnectTimer = setTimeout(() => { if (runId === this.state.runId) { void this.refresh().then(() => this.connect()); } }, 5000);
      }
    });
  }
}
