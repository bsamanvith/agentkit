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
  survey?: Survey;
  design?: Design;
  design_report?: DesignReport;
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
  surveyor?: boolean;
  research?: boolean;
  prices?: boolean;
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
  height_m: number;
  water_mm: Range;
  intercrops?: string[];
  light?: Light;
  day_length?: DayHabit;
}

/** How much sun a crop wants, and how day length sets its flowering. */
export type Light = "full" | "part" | "shade";
export type DayHabit = "neutral" | "short" | "long";

/** A month's climate normals at a plot. */
export interface ClimateMonth {
  temp: number;
  temp_max: number;
  temp_min: number;
  rain_mm: number;
  humidity: number;
  sun_mj?: number;
  clear_sun_mj?: number;
}

export interface Climate {
  point: Point;
  months: ClimateMonth[];
  source: string;
}

/** The sun at a plot over a typical day of one month. */
export interface SunMonth {
  month: number;
  day_length_h: number;
  sun_hours: number;
  noon: { elevation: number; azimuth: number };
  noon_incidence: number;
  flat_mj: number;
  clear_mj?: number;
  slope_mj: number;
  slope_factor: number;
  cloud_loss: number;
}

export interface SunReport {
  farm: string;
  point: Point;
  slope?: { tilt_deg: number; aspect_deg: number };
  slope_note: string;
  months: SunMonth[];
  seasons: { season: string; from: number; to: number }[];
  notes: string[];
  source: string;
  today: string;
}

/** One sowing date's score, for a calendar. */
export interface Sowing {
  sow: string;
  score: number;
  season: string;
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
  light?: {
    want: Light;
    sun_mj: number;
    sun_hours: number;
    dimmest_month?: string;
    dimmest_mj?: number;
    slope_factor: number;
    day_length_h: Range;
  };
  calendar?: Sowing[];
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

// The land and its surroundings (apps/farm/land, apps/farm/osm).

export type FeatureKind =
  | "road"
  | "track"
  | "path"
  | "rail"
  | "river"
  | "stream"
  | "canal"
  | "drain"
  | "water"
  | "wetland"
  | "building"
  | "settlement"
  | "farmland"
  | "trees"
  | "industry"
  | "power_line"
  | "well"
  | "facility";

export interface Feature {
  id: string;
  kind: FeatureKind;
  class?: string;
  name?: string;
  ref?: string;
  closed?: boolean;
  points: Point[];
}

export type ShapeKind = "square" | "rectangle" | "long_strip" | "triangle" | "trapezium" | "irregular";

export interface Edge {
  from: number;
  length_m: number;
  bearing: number;
  faces: string;
}

export interface Box {
  center: Point;
  length_m: number;
  width_m: number;
  bearing: number;
  corners: Point[];
}

export interface Shape {
  area_m2: number;
  perimeter_m: number;
  corners: number;
  centroid: Point;
  box: Box;
  elongation: number;
  compactness: number;
  rectangularity: number;
  convexity: number;
  kind: ShapeKind;
  edges: Edge[];
  long_side: string;
  notes?: string[];
}

export interface Nearby {
  id: string;
  kind: FeatureKind;
  class?: string;
  name?: string;
  ref?: string;
  distance_m: number;
  direction: string;
  edge: number;
  touches?: boolean;
  crosses?: boolean;
  inside?: boolean;
  at: Point;
  from: Point;
}

export interface Terrain {
  source: string;
  points: Point[];
  heights: number[];
  lowest: number;
  highest: number;
  fall_m: number;
  slope_pct: number;
  downhill?: string;
  flat: boolean;
  resolution_m: number;
}

export interface LandNote {
  id: string;
  severity: Severity;
  message: string;
  feature?: string;
  element: number;
}

export interface Survey {
  shape: Shape;
  terrain?: Terrain;
  radius_m: number;
  nearest: Nearby[];
  nearby: Nearby[];
  counts: Partial<Record<FeatureKind, number>>;
  features: Feature[];
  notes: LandNote[];
  sources: string[];
  missing?: string[];
  surveyed_at: string;
}

export type ElementKind = "gate" | "path" | "shed" | "pond" | "border";

export interface Element {
  kind: ElementKind;
  ring?: Point[];
  hole?: Point[];
  at?: Point;
  length_m?: number;
  width_m?: number;
  depth_m?: number;
  trees?: number;
  tree_spacing_m?: number;
  why?: string;
}

export interface Design {
  elements: Element[];
}

export interface ElementMeasure {
  area_m2: number;
  share: number;
  volume_m3?: number;
}

export type DesignStatus = "sound" | "needs_review" | "invalid";

export interface DesignReport {
  status: DesignStatus;
  checks: LandNote[];
  elements: ElementMeasure[];
  plot_m2: number;
  fields_m2: number;
}

export interface Want {
  gate?: boolean;
  path?: boolean;
  shed?: boolean;
  pond?: boolean;
  border?: boolean;
  path_width_m?: number;
  shed_length_m?: number;
  shed_width_m?: number;
  pond_side_m?: number;
  pond_depth_m?: number;
  border_width_m?: number;
  tree_spacing_m?: number;
}

// Web research (apps/farm/research).

export interface ResearchProvider {
  id: string;
  name: string;
  sign_up: string;
  connected: boolean;
  updated_at?: string;
}

// Plot groups and their plant distribution (apps/farm/farm, apps/farm/planting).

export type Place = "block" | "border" | "alley";
export type Arrange = "strips" | "alternate" | "by_plot";
export type Rows = "long_side" | "north_south" | "east_west";

export interface PlantGroup {
  id: string;
  name: string;
  crops: string[];
  place?: Place;
  share?: number;
  width_m?: number;
  every_m?: number;
}

export interface Distribution {
  plants: PlantGroup[];
  arrange?: Arrange;
  strip_m?: number;
  rows?: Rows;
}

export interface Group {
  id: string;
  name: string;
  plots: string[];
  distribution?: Distribution;
  created_at: string;
  updated_at: string;
}

export interface GroupDraft {
  name: string;
  plots: string[];
}

export type EffectKind = "shade" | "edge" | "spacing" | "water" | "neighbours" | "wind" | "layout";

export interface Effect {
  id: string;
  kind: EffectKind;
  severity: Severity;
  plant?: string;
  other?: string;
  plot?: string;
  message: string;
  area_m2?: number;
}

export interface Region {
  plant: string;
  ring: Point[];
  hole?: Point[];
  area_m2: number;
  length_m: number;
  width_m: number;
  rows: number;
  trees?: Point[];
}

export interface PlotLayout {
  plot: string;
  name: string;
  row_bearing: number;
  area_m2: number;
  regions: Region[];
}

export interface PlantMeasure {
  plant: string;
  area_m2: number;
  share: number;
  want?: number;
  plots: number;
  pieces: number;
  counts: { crop: string; plants: number }[];
  height_m: number;
  water_mm: number;
  edge_share: number;
  shaded_m2: number;
  narrowest_m: number;
}

export type DistributionStatus = "sound" | "needs_review" | "invalid";

export interface DistributionResult {
  status: DistributionStatus;
  distribution: Distribution;
  plots: PlotLayout[];
  plants: PlantMeasure[];
  effects: Effect[];
  area_m2: number;
}
