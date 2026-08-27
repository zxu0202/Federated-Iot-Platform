import { ApiError, PlatformReadiness } from "./contract.js";

function idempotencyKey() { return `ui-${crypto.getRandomValues(new Uint32Array(4)).join("")}`; }

export class LivePlatformApi {
  baseUrl: string;

  constructor(baseUrl: string) { this.baseUrl = baseUrl.replace(/\/$/, ""); }

  async requestEnvelope(path: string, options: any = {}) {
    const headers = new Headers(options.headers ?? {});
    if (options.json !== undefined) headers.set("Content-Type", "application/json; charset=utf-8");
    const response = await fetch(`${this.baseUrl}${path}`, {
      method: options.method ?? "GET", headers, signal: options.signal,
      body: options.json === undefined ? options.body : JSON.stringify(options.json)
    });
    const payload = response.status === 204 ? null : await response.json().catch(() => null);
    const responseRequestId = payload?.request_id ?? payload?.meta?.request_id ?? response.headers.get("X-Request-ID") ?? null;
    if (!response.ok) {
      const item = payload?.error ?? {};
      throw new ApiError(item.code ?? "INTERNAL_ERROR", payload?.request_id ?? responseRequestId, { ...item, runId: payload?.run_id, status: response.status });
    }
    return { data: payload?.data ?? payload, meta: payload?.meta ?? (responseRequestId ? { request_id: responseRequestId } : {}) };
  }

  async request(path: string, options: any = {}) { return (await this.requestEnvelope(path, options)).data; }

  getDataset(datasetId, signal) { return this.request(`/datasets/${encodeURIComponent(datasetId)}`, { signal }); }
  getReferenceProfile(signal) { return this.request("/configuration/reference-profile", { signal }); }
  getParameterProfiles(signal) { return this.request("/parameter-profiles?mode=CUSTOM", { signal }); }
  getReadiness(signal): Promise<PlatformReadiness> { return this.request("/health/ready", { signal }); }
  listSimulations(query: Record<string, string | number> = {}, signal) {
    const parameters = new URLSearchParams();
    Object.entries(query).forEach(([key, value]) => { if (value !== undefined && value !== null && value !== "") parameters.set(key, String(value)); });
    const suffix = parameters.toString();
    return this.requestEnvelope(`/simulations${suffix ? `?${suffix}` : ""}`, { signal }).then(({ data, meta }) => {
      const page = Array.isArray(data) ? { items: data } : (data ?? {});
      return {
        ...page,
        meta,
        request_id: page.request_id ?? meta?.request_id ?? null,
        next_cursor: page.next_cursor ?? meta?.next_cursor ?? null,
        has_more: page.has_more ?? meta?.has_more ?? false,
        total: page.total ?? meta?.total ?? null
      };
    });
  }
  getSimulation(runId, signal) { return this.request(`/simulations/${encodeURIComponent(runId)}`, { signal }); }
  getSummary(runId, agent, signal) { return this.request(`/simulations/${encodeURIComponent(runId)}/summary?agent=${encodeURIComponent(agent)}`, { signal }); }
  getResults(runId, query: Record<string, string | number> = {}, signal) { return this.request(`/simulations/${encodeURIComponent(runId)}/results${this.queryString(query)}`, { signal }); }
  getAlarms(runId, query: Record<string, string | number> = {}, signal) { return this.request(`/simulations/${encodeURIComponent(runId)}/alarms${this.queryString(query)}`, { signal }); }
  getReplay(runId, query: Record<string, string | number> = {}, signal) { return this.request(`/simulations/${encodeURIComponent(runId)}/replay${this.queryString(query)}`, { signal }); }
  getArtifacts(runId, signal) { return this.request(`/simulations/${encodeURIComponent(runId)}/artifacts`, { signal }); }
  replayExportUrl(runId, agent) { return `${this.baseUrl}/simulations/${encodeURIComponent(runId)}/replay-export?agent=${encodeURIComponent(agent)}&format=zip`; }
  artifactDownloadUrl(runId, artifactName) { return `${this.baseUrl}/simulations/${encodeURIComponent(runId)}/artifacts/${encodeURIComponent(artifactName)}`; }
  cancelSimulation(runId, signal) { return this.request(`/simulations/${encodeURIComponent(runId)}/cancel`, { method: "POST", json: { reason: "operator_requested" }, signal }); }
  createParameterProfile(payload, signal) { return this.request("/parameter-profiles", { method: "POST", json: payload, signal }); }
  renameParameterProfile(versionId, payload, signal) { return this.request(`/parameter-profiles/${encodeURIComponent(versionId)}`, { method: "PATCH", json: payload, signal }); }
  createSimulation(payload, signal) { return this.request("/simulations", { method: "POST", headers: { "Idempotency-Key": idempotencyKey() }, json: payload, signal }); }

  uploadDataset(file, displayName, onProgress, signal) {
    const form = new FormData(); form.append("display_name", displayName); form.append("file", file);
    onProgress?.(0);
    return this.request("/datasets", { method: "POST", body: form, signal }).then(value => { onProgress?.(100); return value; });
  }

  subscribeSimulationEvents(runId, lastEventId, handlers) {
    const controller = new AbortController();
    const close = () => controller.abort();
    const request = async () => {
      try {
        const response = await fetch(`${this.baseUrl}/simulations/${encodeURIComponent(runId)}/events`, {
          headers: { Accept: "text/event-stream", ...(lastEventId ? { "Last-Event-ID": String(lastEventId) } : {}) }, signal: controller.signal
        });
        if (!response.ok || !response.body) throw new Error(`SSE status ${response.status}`);
        if (controller.signal.aborted) return;
        handlers.onOpen?.();
        const reader = response.body.getReader(); const decoder = new TextDecoder(); let buffer = ""; let eventId = lastEventId;
        while (!controller.signal.aborted) {
          const chunk = await reader.read(); if (chunk.done) break;
          buffer += decoder.decode(chunk.value, { stream: true });
          const frames = buffer.split(/\r?\n\r?\n/); buffer = frames.pop() ?? "";
          for (const frame of frames) {
            const fields = Object.fromEntries(frame.split(/\r?\n/).filter(line => line.includes(":")).map(line => {
              const position = line.indexOf(":"); return [line.slice(0, position), line.slice(position + 1).trimStart()];
            }));
            if (fields.id) eventId = Number(fields.id);
            if (fields.event && fields.data) handlers.onEvent?.({ id: eventId, type: fields.event, data: JSON.parse(fields.data) });
          }
        }
        if (!controller.signal.aborted) handlers.onDisconnect?.();
      } catch (error) { if (!controller.signal.aborted) handlers.onDisconnect?.(error); }
    };
    void request();
    return { close };
  }

  queryString(query: Record<string, string | number>) {
    const parameters = new URLSearchParams();
    Object.entries(query).forEach(([key, value]) => { if (value !== undefined && value !== null && value !== "") parameters.set(key, String(value)); });
    const encoded = parameters.toString();
    return encoded ? `?${encoded}` : "";
  }
}
