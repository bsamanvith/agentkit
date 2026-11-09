import type {
  AssistantReply,
  Climate,
  Config,
  Crop,
  Design,
  DesignReport,
  Distribution,
  DistributionResult,
  Draft,
  Farm,
  Group,
  GroupDraft,
  Part,
  Plan,
  PlaceMatch,
  PlanReport,
  Profile,
  Recommendation,
  Report,
  ResearchProvider,
  Soil,
  Survey,
  SunReport,
  Want,
} from "./types";

/**
 * An error the API answered with; report is set for a boundary that failed
 * verification, planReport for a plan with errors and designReport for a
 * layout with errors (all 422).
 */
export class ApiError extends Error {
  readonly status: number;
  readonly report?: Report;
  readonly planReport?: PlanReport;
  readonly designReport?: DesignReport;
  constructor(status: number, message: string, report?: Report, planReport?: PlanReport, designReport?: DesignReport) {
    super(message);
    this.status = status;
    this.report = report;
    this.planReport = planReport;
    this.designReport = designReport;
  }
}

export async function call<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const resp = await fetch(path, {
    method,
    headers: body === undefined ? {} : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  });
  if (resp.status === 204) return undefined as T;
  const data = (await resp.json().catch(() => ({}))) as {
    error?: string;
    report?: Report;
    plan_report?: PlanReport;
    design_report?: DesignReport;
  };
  if (!resp.ok)
    throw new ApiError(resp.status, data.error ?? resp.statusText, data.report, data.plan_report, data.design_report);
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
  climate: (id: string) => call<Climate>("GET", `/api/farms/${encodeURIComponent(id)}/climate`),
  sun: (id: string) => call<SunReport>("GET", `/api/farms/${encodeURIComponent(id)}/sun`),
  recommend: (
    id: string,
    ask: { around?: string; spread?: number; crops?: string[]; limit?: number; calendar?: boolean },
  ) => call<Recommendation>("POST", `/api/farms/${encodeURIComponent(id)}/recommend`, ask),
  layout: (id: string, parts: Part[]) =>
    call<{ plan: Plan; report: PlanReport }>("POST", `/api/farms/${encodeURIComponent(id)}/layout`, { parts }),
  savePlan: (id: string, plan: Plan) => call<Farm>("PUT", `/api/farms/${encodeURIComponent(id)}/plan`, plan),
  clearPlan: (id: string) => call<Farm>("DELETE", `/api/farms/${encodeURIComponent(id)}/plan`),
  survey: (id: string, refresh = false) =>
    call<Survey>(refresh ? "POST" : "GET", `/api/farms/${encodeURIComponent(id)}/survey`),
  suggestDesign: (id: string, want: Want) =>
    call<{ design: Design; report: DesignReport }>("POST", `/api/farms/${encodeURIComponent(id)}/design/suggest`, want),
  checkDesign: (id: string, d: Design) =>
    call<DesignReport>("POST", `/api/farms/${encodeURIComponent(id)}/design/check`, d),
  saveDesign: (id: string, d: Design) => call<Farm>("PUT", `/api/farms/${encodeURIComponent(id)}/design`, d),
  clearDesign: (id: string) => call<Farm>("DELETE", `/api/farms/${encodeURIComponent(id)}/design`),
  groups: async () => (await call<{ groups: Group[] }>("GET", "/api/groups")).groups,
  createGroup: (d: GroupDraft) => call<Group>("POST", "/api/groups", d),
  updateGroup: (id: string, d: GroupDraft) => call<Group>("PUT", `/api/groups/${encodeURIComponent(id)}`, d),
  removeGroup: (id: string) => call<undefined>("DELETE", `/api/groups/${encodeURIComponent(id)}`),
  distribution: (id: string) => call<DistributionResult>("GET", `/api/groups/${encodeURIComponent(id)}/distribution`),
  previewDistribution: (id: string, d: Distribution, signal?: AbortSignal) =>
    call<DistributionResult>("POST", `/api/groups/${encodeURIComponent(id)}/distribution/preview`, d, signal),
  saveDistribution: (id: string, d: Distribution) =>
    call<{ group: Group; result: DistributionResult }>("PUT", `/api/groups/${encodeURIComponent(id)}/distribution`, d),
  clearDistribution: (id: string) => call<Group>("DELETE", `/api/groups/${encodeURIComponent(id)}/distribution`),
  research: async () => (await call<{ providers: ResearchProvider[] }>("GET", "/api/research")).providers,
  connectResearch: async (provider: string, key: string) =>
    (await call<{ providers: ResearchProvider[] }>("PUT", `/api/research/${encodeURIComponent(provider)}`, { key }))
      .providers,
  disconnectResearch: async (provider: string) =>
    (await call<{ providers: ResearchProvider[] }>("DELETE", `/api/research/${encodeURIComponent(provider)}`))
      .providers,
  unread: () => call<{ unread: number }>("GET", "/api/notices?unread=1&limit=1"),
  places: async (q: string) =>
    (await call<{ places: PlaceMatch[] }>("GET", `/api/places?q=${encodeURIComponent(q)}&limit=5`)).places,
  ask: (message: string, conversation?: string) =>
    call<AssistantReply>("POST", "/api/assistant", { message, conversation }),
};
