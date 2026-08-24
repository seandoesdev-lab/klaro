/**
 * Wire types, mirrored from services/observability.
 *
 * Every type here names the Go struct it mirrors. When obsplane changes a json
 * tag, the compile error should land in this file rather than somewhere deep in
 * a component - so nothing outside this module reads a raw response.
 */

/* ---------------------------------------------------------------- errors -- */

/** internal/platform/httpx.Payload */
export interface ApiErrorPayload {
  code: string;
  message: string;
  details?: unknown;
}

/** internal/platform/httpx.Body */
export interface ApiErrorBody {
  error: ApiErrorPayload;
}

export const ErrorCode = {
  Unauthenticated: "UNAUTHENTICATED",
  Forbidden: "FORBIDDEN",
  NotFound: "NOT_FOUND",
  Conflict: "CONFLICT",
  Validation: "VALIDATION_ERROR",
  QuotaExceeded: "QUOTA_EXCEEDED",
  Internal: "INTERNAL_ERROR",
  /** Client-side only: the request never reached obsplane. */
  Network: "NETWORK_ERROR",
} as const;

export type ErrorCodeValue = (typeof ErrorCode)[keyof typeof ErrorCode];

/* ------------------------------------------------------------- explorer -- */

/** internal/explorer.Matcher */
export interface Matcher {
  label: string;
  value: string;
  negate?: boolean;
}

/**
 * internal/explorer.Sample - marshalled as the pair [tsMillis, value].
 * A tuple, not an object: that is literally what the wire carries.
 */
export type Sample = [number, number];

/** internal/explorer.Series */
export interface Series {
  labels: Record<string, string>;
  points: Sample[];
}

/** internal/explorer.ResolutionRaw / Resolution5m / Resolution1h */
export type Resolution = "raw" | "5m" | "1h";

/** internal/explorer.MetricsResult */
export interface MetricsResult {
  series: Series[];
  resolution: Resolution;
  /** The window reached past what the plan keeps and was narrowed. */
  clamped: boolean;
  from: string;
  to: string;
  /** The MetricsQL obsplane generated, returned so the org matcher is visible. */
  query: string;
}

/** internal/explorer.Aggregate accepts exactly these. "" means raw series. */
export const AGGREGATIONS = ["", "rate", "avg", "sum", "count", "p50", "p95", "p99"] as const;
export type Aggregation = (typeof AGGREGATIONS)[number];

/** internal/explorer.TraceSummary */
export interface TraceSummary {
  trace_id: string;
  root_service: string;
  root_name?: string;
  duration_ms: number;
  /** unix milliseconds */
  start: number;
}

/** internal/explorer.TracesResult */
export interface TracesResult {
  data: TraceSummary[];
  query: string;
}

/** internal/explorer.Span */
export interface Span {
  span_id: string;
  parent_span_id?: string;
  name: string;
  service: string;
  /** unix milliseconds */
  start: number;
  duration_ms: number;
  status: string;
  /** The resource service.instance.id: this span join key for metrics. */
  host?: string;
}

/** internal/explorer.Trace */
export interface Trace {
  trace_id: string;
  spans: Span[];
}

/** internal/explorer.LogEntry */
export interface LogEntry {
  /** unix milliseconds */
  ts: number;
  level: string;
  message: string;
  /** Correlation keys, promoted out of Loki structured metadata. */
  trace_id?: string;
  span_id?: string;
  labels: Record<string, string>;
}

/** internal/explorer.LogsPage */
export interface LogsPage {
  data: LogEntry[];
  next?: string;
  query: string;
}

/* ---------------------------------------------------------- correlation -- */

/**
 * internal/explorer.CorrelationScope - one service+host pair the trace touched.
 *
 * Returned so the metric list is explainable: these series were chosen because
 * this instance held the span that took most of the time.
 */
export interface CorrelationScope {
  service: string;
  host?: string;
  spans: number;
  /** Summed span duration, not wall clock: concurrent siblings both count. */
  duration_ms: number;
  errors: number;
}

/** internal/explorer.CorrelatedMetric */
export interface CorrelatedMetric {
  /** Unique within a response; safe to use as a React key. */
  key: string;
  metric: string;
  service: string;
  host?: string;
  series: Series[];
  resolution: Resolution;
  query: string;
}

/**
 * internal/explorer.Correlated - one trace joined to its logs and metrics.
 *
 * `notes` is not decoration. A signal obsplane could not read comes back as an
 * empty list plus a note, so a view that ignores notes shows "nothing was
 * logged" when the truth is "the logs backend is down".
 */
export interface CorrelatedTrace {
  trace_id: string;
  spans: Span[];
  /** The padded window the logs and metrics were read over (RFC3339). */
  from: string;
  to: string;
  scopes: CorrelationScope[];
  logs: LogEntry[];
  logs_query?: string;
  metrics: CorrelatedMetric[];
  notes?: string[];
}

/* ------------------------------------------------------------- alerting -- */

/** internal/alerting.QuerySpec */
export interface QuerySpec {
  metric: string;
  filters?: Matcher[];
  agg?: string;
  step_sec?: number;
}

/** internal/alerting.Channel */
export interface Channel {
  type: "email" | "slack";
  target: string;
}

export const COMPARATORS = ["gt", "gte", "lt", "lte"] as const;
export type Comparator = (typeof COMPARATORS)[number];

export const SEVERITIES = ["info", "warning", "critical"] as const;
export type Severity = (typeof SEVERITIES)[number];

/** internal/alerting.Rule */
export interface AlertRule {
  id: string;
  name: string;
  /** vmalert is metric-only; obsplane refuses anything else. */
  signal: "metric";
  query_spec: QuerySpec;
  /** Rendered MetricsQL, read-only. */
  query: string;
  comparator: Comparator;
  threshold: number;
  for_duration_sec: number;
  severity: Severity;
  channels: Channel[];
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

/** internal/alerting.Input - every field optional so PATCH is a partial. */
export interface AlertRuleInput {
  name?: string;
  signal?: "metric";
  query_spec?: QuerySpec;
  comparator?: Comparator;
  threshold?: number;
  for_duration_sec?: number;
  severity?: Severity;
  channels?: Channel[];
  enabled?: boolean;
}

/** internal/alerting.Event */
export interface AlertEvent {
  id: string;
  rule_id: string;
  state: string;
  value: number | null;
  labels?: Record<string, string>;
  notified_channels?: Channel[];
  started_at: string;
  resolved_at: string | null;
}

export interface AlertEventFilter {
  state?: string;
  rule_id?: string;
  from?: Date;
  to?: Date;
  limit?: number;
}

/* ----------------------------------------------------------- dashboards -- */

/** internal/dashboards.Layout */
export interface PanelLayout {
  x: number;
  y: number;
  w: number;
  h: number;
}

/** internal/dashboards.PanelQuery - one struct for all three signals. */
export interface PanelQuery {
  signal: "metrics" | "traces" | "logs";
  metric?: string;
  filters?: Matcher[];
  agg?: string;
  step_sec?: number;
  service?: string;
  min_duration_ms?: number;
  contains?: string;
  limit?: number;
}

/** internal/dashboards.PanelTypes */
export const PANEL_TYPES = ["timeseries", "stat", "table", "logs", "traces"] as const;
export type PanelType = (typeof PANEL_TYPES)[number];

/** internal/dashboards.Panel */
export interface Panel {
  id: string;
  title: string;
  type: PanelType;
  layout: PanelLayout;
  query: PanelQuery;
}

/** internal/dashboards.Spec */
export interface DashboardSpec {
  panels: Panel[];
  range_sec?: number;
  refresh_sec?: number;
}

/** internal/dashboards.Dashboard */
export interface Dashboard {
  id: string;
  name: string;
  description?: string;
  spec: DashboardSpec;
  created_at: string;
  updated_at: string;
}

/** internal/dashboards.Input */
export interface DashboardInput {
  name?: string;
  description?: string;
  spec?: DashboardSpec;
}

/* ----------------------------------------------------------------- live -- */

/** internal/live.Point */
export interface LivePoint {
  labels: Record<string, string>;
  value: number;
}

/** internal/live.Frame */
export interface LiveFrame {
  /** unix milliseconds */
  ts: number;
  stream: LiveStream;
  points: LivePoint[];
}

/** internal/live.Streams */
export const LIVE_STREAMS = ["metric", "service"] as const;
export type LiveStream = (typeof LIVE_STREAMS)[number];

/* ---------------------------------------------------------------- quota -- */

/** internal/ingestkey.Quota - the subset the header chip renders. */
export interface Quota {
  plan_code?: string;
  active_hosts: number;
  host_limit: number | null;
  ingest_bytes: number;
  ingest_gb: number;
  ingest_limit_gb: number | null;
  hosts_exceeded: boolean;
  ingest_exceeded: boolean;
  overage: boolean;
}

/* --------------------------------------------------- infrastructure -- */

/**
 * internal/explorer.HostVitals.
 *
 * Every reading is nullable because the Go side made it a pointer: null is
 * "this host reports no such metric", which is a different fact from 0 and has
 * to stay different all the way to the cell that renders it.
 */
export interface HostVitals {
  cpu_pct: number | null;
  mem_pct: number | null;
  disk_pct: number | null;
  load1: number | null;
}

/**
 * internal/inventory.Host - a registry row with its latest readings.
 *
 * status is "stale", never "down": the platform observes reporting, not
 * reachability (inventory.StatusStale says why).
 */
export interface Host extends HostVitals {
  host_ident: string;
  service?: string;
  env?: string;
  first_seen_at: string;
  last_seen_at: string;
  status: "up" | "stale";
}

/** internal/inventory.Result */
export interface HostsResult {
  data: Host[];
  active_window_sec: number;
  total: number;
  active: number;
  /** False when the readings could not be fetched; the registry half still is. */
  metrics_available: boolean;
}

/** internal/explorer.HostSeriesKeys, in display order. */
export const HOST_SERIES_KEYS = [
  "cpu_pct",
  "mem_pct",
  "disk_pct",
  "load1",
  "net_rx_bps",
  "net_tx_bps",
  "disk_read_bps",
  "disk_write_bps",
] as const;
export type HostSeriesKey = (typeof HOST_SERIES_KEYS)[number];

/** internal/explorer.HostSeriesResult */
export interface HostSeriesResult {
  host_ident: string;
  series: Partial<Record<HostSeriesKey, Series>>;
  clamped: boolean;
  from: string;
  to: string;
  /** The MetricsQL obsplane generated per series, returned read-only. */
  queries: Partial<Record<HostSeriesKey, string>>;
}

/** internal/explorer.UptimeHost */
export interface UptimeHost {
  host_ident: string;
  observed_buckets: number;
  expected_buckets: number;
  /** 0..1, observed over expected. */
  availability: number;
  met: boolean;
}

/** internal/explorer.UptimeResult */
export interface UptimeResult {
  /** 0..1 objective the achievement is measured against. */
  target: number;
  step_sec: number;
  from: string;
  to: string;
  clamped: boolean;
  hosts: UptimeHost[];
  availability: number;
  observed_buckets: number;
  expected_buckets: number;
  query: string;
}

/** Envelope obsplane uses for every list endpoint. */
export interface ListEnvelope<T> {
  data: T[];
}
