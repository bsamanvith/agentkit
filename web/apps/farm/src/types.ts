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
  country: string;
}

export interface AssistantReply {
  conversation: string;
  text: string;
  tools: string[];
}
