import type { AssistantReply, Config, Draft, Farm, Report } from "./types";

/** An error the API answered with; report is set for a boundary that failed verification (422). */
export class ApiError extends Error {
  readonly status: number;
  readonly report?: Report;
  constructor(status: number, message: string, report?: Report) {
    super(message);
    this.status = status;
    this.report = report;
  }
}

async function call<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const resp = await fetch(path, {
    method,
    headers: body === undefined ? {} : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  });
  if (resp.status === 204) return undefined as T;
  const data = (await resp.json().catch(() => ({}))) as { error?: string; report?: Report };
  if (!resp.ok) throw new ApiError(resp.status, data.error ?? resp.statusText, data.report);
  return data as T;
}

export const api = {
  config: () => call<Config>("GET", "/api/config"),
  farms: async () => (await call<{ farms: Farm[] }>("GET", "/api/farms")).farms,
  farm: (id: string) => call<Farm>("GET", `/api/farms/${encodeURIComponent(id)}`),
  verify: (d: Partial<Draft>, except: string | undefined, signal?: AbortSignal) =>
    call<Report>("POST", "/api/verify", { ...d, except }, signal),
  create: (d: Draft) => call<Farm>("POST", "/api/farms", d),
  update: (id: string, d: Draft) => call<Farm>("PUT", `/api/farms/${encodeURIComponent(id)}`, d),
  remove: (id: string) => call<undefined>("DELETE", `/api/farms/${encodeURIComponent(id)}`),
  ask: (message: string, conversation?: string) =>
    call<AssistantReply>("POST", "/api/assistant", { message, conversation }),
};
