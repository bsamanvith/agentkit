import type {
  AssistantReply,
  Config,
  Crop,
  Draft,
  Farm,
  Part,
  Plan,
  PlanReport,
  Profile,
  Recommendation,
  Report,
  Soil,
} from "./types";

/**
 * An error the API answered with; report is set for a boundary that failed
 * verification, planReport for a plan with errors (both 422).
 */
export class ApiError extends Error {
  readonly status: number;
  readonly report?: Report;
  readonly planReport?: PlanReport;
  constructor(status: number, message: string, report?: Report, planReport?: PlanReport) {
    super(message);
    this.status = status;
    this.report = report;
    this.planReport = planReport;
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
  const data = (await resp.json().catch(() => ({}))) as { error?: string; report?: Report; plan_report?: PlanReport };
  if (!resp.ok) throw new ApiError(resp.status, data.error ?? resp.statusText, data.report, data.plan_report);
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
  soils: async () => (await call<{ soils: Soil[] }>("GET", "/api/soils")).soils,
  crops: async () => (await call<{ crops: Crop[] }>("GET", "/api/crops")).crops,
  setProfile: (id: string, p: Profile) => call<Farm>("PUT", `/api/farms/${encodeURIComponent(id)}/profile`, p),
  recommend: (id: string, ask: { around?: string; crops?: string[]; limit?: number }) =>
    call<Recommendation>("POST", `/api/farms/${encodeURIComponent(id)}/recommend`, ask),
  layout: (id: string, parts: Part[]) =>
    call<{ plan: Plan; report: PlanReport }>("POST", `/api/farms/${encodeURIComponent(id)}/layout`, { parts }),
  savePlan: (id: string, plan: Plan) => call<Farm>("PUT", `/api/farms/${encodeURIComponent(id)}/plan`, plan),
  clearPlan: (id: string) => call<Farm>("DELETE", `/api/farms/${encodeURIComponent(id)}/plan`),
  ask: (message: string, conversation?: string) =>
    call<AssistantReply>("POST", "/api/assistant", { message, conversation }),
};
