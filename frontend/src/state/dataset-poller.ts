export const DATASET_POLL_INTERVAL_MS = 2000;

const ACTIVE_DATASET_STATUSES = new Set(["VALIDATING", "QUEUED", "RUNNING"]);
const TERMINAL_DATASET_STATUSES = new Set(["VALID", "INVALID", "FAILED"]);

export function needsDatasetPolling(dataset) {
  const datasetStatus = String(dataset?.status ?? "").toUpperCase();
  const preflightStatus = String(dataset?.preflight?.status ?? "").toUpperCase();
  if (TERMINAL_DATASET_STATUSES.has(datasetStatus) || TERMINAL_DATASET_STATUSES.has(preflightStatus)) return false;
  return ACTIVE_DATASET_STATUSES.has(datasetStatus) || ACTIVE_DATASET_STATUSES.has(preflightStatus);
}

export class DatasetPoller {
  api: any;
  onDataset: (dataset: any) => void;
  onError: (datasetId: string, error: unknown) => void;
  timer: ReturnType<typeof setTimeout> | null;
  controller: AbortController | null;
  datasetId: string | null;
  generation: number;
  requestActive: boolean;

  constructor(api, handlers) {
    this.api = api;
    this.onDataset = handlers.onDataset;
    this.onError = handlers.onError;
    this.timer = null;
    this.controller = null;
    this.datasetId = null;
    this.generation = 0;
    this.requestActive = false;
  }

  watch(dataset) {
    this.stop();
    if (!dataset?.dataset_id || !needsDatasetPolling(dataset)) return;
    this.datasetId = dataset.dataset_id;
    const generation = this.generation;
    void this.poll(generation);
  }

  stop() {
    this.generation += 1;
    if (this.timer !== null) clearTimeout(this.timer);
    this.timer = null;
    this.controller?.abort();
    this.controller = null;
    this.datasetId = null;
    this.requestActive = false;
  }

  close() { this.stop(); }

  schedule(generation) {
    if (generation !== this.generation || !this.datasetId) return;
    this.timer = setTimeout(() => { this.timer = null; void this.poll(generation); }, DATASET_POLL_INTERVAL_MS);
  }

  async poll(generation) {
    if (generation !== this.generation || !this.datasetId || this.requestActive) return;
    this.requestActive = true;
    const datasetId = this.datasetId;
    const controller = new AbortController();
    this.controller = controller;
    let continuePolling = false;
    try {
      const dataset = await this.api.getDataset(datasetId, controller.signal);
      if (generation !== this.generation || datasetId !== this.datasetId) return;
      this.onDataset(dataset);
      continuePolling = needsDatasetPolling(dataset);
      if (!continuePolling) this.datasetId = null;
    } catch (error) {
      if (generation !== this.generation || datasetId !== this.datasetId || (error instanceof Error && error.name === "AbortError")) return;
      this.onError(datasetId, error);
      continuePolling = true;
    } finally {
      if (generation !== this.generation || datasetId !== this.datasetId && this.datasetId !== null) return;
      if (this.controller === controller) this.controller = null;
      this.requestActive = false;
      if (continuePolling) this.schedule(generation);
    }
  }
}
