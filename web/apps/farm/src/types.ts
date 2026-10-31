// The farm API's types (apps/farm/farm, apps/farm/farmapi), as JSON.

export type Unit = "m2" | "ha" | "acre" | "gunta" | "cent";

export const units: { id: Unit; label: string }[] = [
  { id: "acre", label: "acres" },
  { id: "gunta", label: "guntas" },
  { id: "cent", label: "cents" },
  { id: "ha", label: "hectares" },
  { id: "m2", label: "m²" },
];

export type Source = "map" | "gps" | "typed";

export interface Point {
  lat: number;
  lng: number;
}

export interface Corner extends Point {
  accuracy_m?: number;
}

export interface Boundary {
  corners: Corner[];
  source?: Source;
}

export interface Area {
  value: number;
  unit: Unit;
}

export type Status = "verified" | "needs_review" | "invalid";
export type Severity = "info" | "warning" | "error";

export interface Check {
  id: string;
  severity: Severity;
  message: string;
  corners?: number[];
}

export interface Measures {
  area_m2: number;
  hectares: number;
  acres: number;
  guntas: number;
  cents: number;
  acres_text: string;
  perimeter_m: number;
  centroid: Point;
  corners: number;
}

export interface Location {
  village?: string;
  sub_district?: string;
  district?: string;
  state?: string;
  country?: string;
  country_code?: string;
  display_name?: string;
}

export interface Report {
  status: Status;
  measures?: Measures;
  location?: Location;
  checks: Check[];
  checked_at: string;
}

export interface Farm {
  id: string;
  name: string;
  declared?: Area;
  boundary: Boundary;
  report: Report;
  profile?: Profile;
  plan?: Plan;
  plan_report?: PlanReport;
  created_at: string;
  updated_at: string;
}

export interface Draft {
  name: string;
  declared?: Area;
  boundary: Boundary;
}

export interface Config {
  assistant: boolean;
  planner?: boolean;
  country: string;
}

export interface AssistantReply {
  conversation: string;
  text: string;
  tools: string[];
}

// Crop planning (apps/farm/agro, apps/farm/planner).

export type Range = [number, number];

export interface Soil {
  id: string;
  name: string;
  names?: Record<string, string>;
  signs: string;
  texture: string;
  ph: Range;
  water_holding: string;
  drainage: string;
  fertility: string;
  found: string;
  suits: string;
  salty?: boolean;
}

export interface Crop {
  id: string;
  name: string;
  names?: Record<string, string>;
  family: string;
  kind: string;
  perennial: boolean;
  days: Range;
  row_cm: number;
  plant_cm: number;
  seasons: string[];
  grown_in: string[];
  climate_analog: boolean;
  tall: boolean;
  intercrops?: string[];
}

export type WaterKind = "canal" | "river" | "tank" | "borewell" | "open_well" | "farm_pond";

export interface Water {
  kind: WaterKind;
  reliable: boolean;
}

export interface Profile {
  soil?: string;
  ph?: number;
  water?: Water[];
  drip?: boolean;
  previous_crop?: string;
}

export interface Note {
  id: string;
  message: string;
}

export interface Window {
  from: string;
  to: string;
  best: string;
  score: number;
}

export interface Option {
  crop: string;
  name: string;
  names?: Record<string, string>;
  kind: string;
  analog?: boolean;
  sow: string;
  harvest: string;
  season: string;
  score: number;
  water: { need_mm: number; rain_mm: number; irrigation_mm: number; gap_mm: number; short_mm: number };
  reasons: Note[];
  risks: Note[];
  in_range?: boolean;
  window?: Window;
  next?: Window;
}

export interface Recommendation {
  farm: string;
  soil?: Soil;
  today: string;
  from?: string;
  to?: string;
  options: Option[];
  notes?: Note[];
}

export interface Part {
  crop: string;
  with?: string;
  pattern?: [number, number];
  share: number;
  sow: string;
}

export interface Zone {
  crop: string;
  with?: string;
  pattern?: [number, number];
  ring: Point[];
  row_cm?: number;
  plant_cm?: number;
  row_bearing: number;
  sow: string;
}

export interface Plan {
  zones: Zone[];
}

export interface PlanCheck {
  id: string;
  severity: Severity;
  zone: number;
  other: number;
  message: string;
}

export interface ZoneMeasures {
  area_m2: number;
  share: number;
  length_m: number;
  width_m: number;
  rows: number;
  plants: number;
  with_plants?: number;
}

export type PlanStatus = "sound" | "needs_review" | "invalid";

export interface PlanReport {
  status: PlanStatus;
  checks: PlanCheck[];
  zones: ZoneMeasures[] | null;
  used_m2: number;
  plot_m2: number;
}
