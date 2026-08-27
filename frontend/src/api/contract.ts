/* API contract v0.4 DTOs shared by the live and testing-only adapters. */
export const API_VERSION = "v1";
export const DATASET_COLUMNS = ["Time_base", "dzdl_1", "dzdl_2", "dzdl_3", "dzdl_4", "zl", "sd"];
export const AGENTS = [1, 2, 3];
export const TERMINAL_STATUSES = new Set(["COMPLETED", "CANCELLED", "FAILED", "FAILED_RECOVERABLE"]);

export interface PlatformReadiness {
  status: string;
  checks: Record<string, string>;
  worker_contract_version?: string;
}

export class ApiError extends Error {
  code: string;
  field: string | null;
  recoverable: boolean;
  requestId: string | null;
  runId: string | null;
  status: number | null;

  constructor(code: string, requestId: string | null, options: any = {}) {
    super(options.message ?? code);
    this.name = "ApiError";
    this.code = code;
    this.field = options.field ?? null;
    this.recoverable = Boolean(options.recoverable);
    this.requestId = requestId ?? null;
    this.runId = options.runId ?? null;
    this.status = options.status ?? null;
  }
}

export function isTerminalStatus(status) { return TERMINAL_STATUSES.has(status); }
export function formatApiError(error, translate) {
  if (!(error instanceof ApiError)) return { title: translate("error.unknown"), detail: null };
  const known = translate(`error.${error.code}`);
  return { title: known === `error.${error.code}` ? translate("error.unknown") : known, detail: `${error.code}${error.requestId ? ` · ${error.requestId}` : ""}` };
}

export function validateAgentCollection(agents) {
  if (!Array.isArray(agents) || agents.length !== 3) return false;
  return [...agents].sort().every((agent, index) => agent === AGENTS[index]);
}
