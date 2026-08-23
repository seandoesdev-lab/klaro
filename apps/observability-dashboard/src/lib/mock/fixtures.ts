/**
 * Mock transport for the obsplane surface (COST-05: local only, no paid service).
 *
 * Every function here returns exactly the shape the matching Go handler returns,
 * so switching NEXT_PUBLIC_OBS_MOCK off changes where data comes from and
 * nothing else. Latency is simulated because the loading states are part of the
 * design (05 section 7) and a mock that resolves instantly never shows them.
 *
 * Values come from a seeded PRNG rather than Math.random so a reload shows the
 * same picture - a chart that reshuffles on every render makes it impossible to
 * tell a real change from noise.
 */

import { ApiError } from "@/lib/api/client";
import type {
  AlertEvent,
  AlertEventFilter,
  AlertRule,
  AlertRuleInput,
  Dashboard,
  DashboardInput,
  LogsPage,
  MetricsResult,
  Quota,
  Resolution,
  Series,
  Trace,
  TracesResult,
} from "@/lib/api/types";

/** Deterministic 32-bit PRNG (mulberry32). */
function seeded(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function hashString(s: string): number {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return h >>> 0;
}

/** Simulated round trip, so skeletons are actually visible. */
export function delay<T>(value: T, ms = 320): Promise<T> {
  return new Promise((resolve) => setTimeout(() => resolve(value), ms));
}

export const MOCK_SERVICES = ["checkout-api", "payments-worker", "catalog-api", "web-bff"];

/** Metric names a demo org would plausibly have. */
export const MOCK_METRICS = [
  "http_server_duration_seconds",
  "http_server_requests_total",
  "process_cpu_utilization",
  "process_memory_usage_bytes",
  "db_client_operation_duration_seconds",
];

/**
 * Pick the resolution the way explorer.pickResolution would: a wide window or a
 * coarse step means the answer came from a rollup, not raw samples.
 */
function pickResolution(fromMs: number, toMs: number, stepSec: number): Resolution {
  const spanHours = (toMs - fromMs) / 3_600_000;
  if (spanHours > 24 * 7 || stepSec >= 3600) return "1h";
  if (spanHours > 24 || stepSec >= 300) return "5m";
  return "raw";
}

export function mockMetrics(args: {
  metric: string;
  fromMs: number;
  toMs: number;
  stepSec: number;
  agg: string;
  serviceFilter?: string;
}): MetricsResult {
  const { metric, fromMs, toMs, stepSec, agg, serviceFilter } = args;
  const stepMs = Math.max(stepSec, 1) * 1000;
  const count = Math.min(Math.max(Math.floor((toMs - fromMs) / stepMs), 2), 720);
  const services = serviceFilter ? [serviceFilter] : MOCK_SERVICES.slice(0, 3);

  const series: Series[] = services.map((service, idx) => {
    const rand = seeded(hashString(metric + ":" + service + ":" + agg));
    const base = 0.12 + idx * 0.07;
    const points: [number, number][] = [];
    let value = base;
    for (let i = 0; i < count; i++) {
      // Random walk with a slow sine underneath: flat noise reads as broken,
      // a pure sine reads as fake.
      value += (rand() - 0.5) * base * 0.25;
      const wave = Math.sin((i / count) * Math.PI * 3 + idx) * base * 0.3;
      const y = Math.max(base * 0.2, value + wave);
      points.push([fromMs + i * stepMs, Number(y.toFixed(4))]);
    }
    return { labels: { __name__: metric, service, klaro_env: "prod" }, points };
  });

  const selector = serviceFilter ? ',service="' + serviceFilter + '"' : "";
  const inner = metric + '{klaro_org_id="mock"' + selector + "}";
  return {
    series,
    resolution: pickResolution(fromMs, toMs, stepSec),
    clamped: false,
    from: new Date(fromMs).toISOString(),
    to: new Date(toMs).toISOString(),
    query: agg ? agg + "_over_time(" + inner + "[" + stepSec + "s])" : inner,
  };
}

export function mockTraces(args: {
  fromMs: number;
  toMs: number;
  service?: string;
  minDurationMs?: number;
  limit?: number;
}): TracesResult {
  const { fromMs, toMs, service, minDurationMs = 0, limit = 50 } = args;
  const rand = seeded(hashString("traces:" + (service ?? "*")));
  const names = ["POST /checkout", "GET /catalog/items", "POST /payments/capture", "GET /cart"];

  const data = Array.from({ length: 40 }, (_, i) => {
    const svc = service ?? MOCK_SERVICES[i % MOCK_SERVICES.length];
    // A long tail rather than a uniform spread, so the >=3s slice ([APM-03])
    // the waterfall exists for actually appears in the list.
    const duration = Math.round(35 + Math.pow(rand(), 4) * 5200);
    return {
      trace_id: hashString(svc + ":" + i).toString(16).padStart(8, "0").repeat(4),
      root_service: svc,
      root_name: names[i % names.length],
      duration_ms: duration,
      start: Math.round(fromMs + rand() * Math.max(toMs - fromMs, 1)),
    };
  })
    .filter((t) => t.duration_ms >= minDurationMs)
    .sort((a, b) => b.start - a.start)
    .slice(0, limit);

  const svcSel = service ? '{resource.service.name="' + service + '"}' : "{}";
  return { data, query: svcSel + " | duration >= " + minDurationMs + "ms" };
}

export function mockTrace(traceId: string): Trace {
  const rand = seeded(hashString(traceId));
  const start = Date.now() - 90_000;
  const total = 800 + Math.round(rand() * 3000);

  const shape = [
    { name: "POST /checkout", service: "web-bff", offset: 0, dur: total, parent: undefined },
    { name: "checkout.validate", service: "checkout-api", offset: 12, dur: total * 0.14, parent: "span-0" },
    { name: "SELECT carts", service: "checkout-api", offset: 30, dur: total * 0.09, parent: "span-1" },
    { name: "payments.capture", service: "payments-worker", offset: total * 0.2, dur: total * 0.62, parent: "span-0" },
    { name: "POST psp.authorize", service: "payments-worker", offset: total * 0.24, dur: total * 0.5, parent: "span-3" },
    { name: "UPDATE orders", service: "checkout-api", offset: total * 0.85, dur: total * 0.1, parent: "span-0" },
  ];

  return {
    trace_id: traceId,
    spans: shape.map((s, i) => ({
      span_id: "span-" + i,
      parent_span_id: s.parent,
      name: s.name,
      service: s.service,
      start: Math.round(start + s.offset),
      duration_ms: Number(s.dur.toFixed(1)),
      // The slowest child is the one worth flagging; everything else passed.
      status: i === 4 && total > 2500 ? "error" : "ok",
    })),
  };
}

export function mockLogs(args: {
  fromMs: number;
  toMs: number;
  contains?: string;
  level?: string;
  service?: string;
  limit?: number;
}): LogsPage {
  const { fromMs, toMs, contains, level, service, limit = 100 } = args;
  // Level travels with the message rather than being drawn separately: a
  // timeout logged as INFO would make the demo data read as nonsense to anyone
  // who actually looks at it.
  const lines = [
    { level: "INFO", text: "checkout completed order_id=%s amount=39900" },
    { level: "WARN", text: "payment capture retry attempt=2 order_id=%s" },
    { level: "ERROR", text: "upstream psp timeout after 3000ms order_id=%s" },
    { level: "INFO", text: "cart cache miss key=cart:%s" },
    { level: "ERROR", text: "db connection pool saturated waiting=7" },
  ];
  const rand = seeded(hashString("logs:" + (contains ?? "") + ":" + (level ?? "") + ":" + (service ?? "")));

  let data = Array.from({ length: 120 }, (_, i) => {
    const line = lines[Math.floor(rand() * lines.length)];
    return {
      ts: Math.round(toMs - (i / 120) * Math.max(toMs - fromMs, 1)),
      level: line.level,
      message: line.text.replace("%s", String(100000 + i)),
      labels: {
        service: service ?? MOCK_SERVICES[i % MOCK_SERVICES.length],
        klaro_env: "prod",
        host: "pod:" + hashString("h" + (i % 4)).toString(16).slice(0, 8),
      },
    };
  });

  if (level) data = data.filter((l) => l.level === level);
  if (service) data = data.filter((l) => l.labels.service === service);
  if (contains) data = data.filter((l) => l.message.includes(contains));
  data = data.slice(0, limit);

  const sel = service
    ? '{klaro_org_id="mock",service="' + service + '"}'
    : '{klaro_org_id="mock"}';
  return { data, query: contains ? sel + " |= " + JSON.stringify(contains) : sel };
}

/* ------------------------------------------------- mutable mock stores --- */

function isoAgo(minutes: number): string {
  return new Date(Date.now() - minutes * 60_000).toISOString();
}

/** Render the MetricsQL the way alerting.validateAndRender would. */
function renderRuleQuery(r: Pick<AlertRule, "query_spec" | "comparator" | "threshold">): string {
  const op = { gt: ">", gte: ">=", lt: "<", lte: "<=" }[r.comparator];
  const spec = r.query_spec;
  const filters = (spec.filters ?? [])
    .map((f) => "," + f.label + (f.negate ? "!=" : "=") + '"' + f.value + '"')
    .join("");
  const sel = spec.metric + '{klaro_org_id="mock"' + filters + "}";
  const step = (spec.step_sec && spec.step_sec > 0 ? spec.step_sec : 60) + "s";

  let expr = sel;
  if (spec.agg === "rate") expr = "rate(" + sel + "[" + step + "])";
  else if (spec.agg === "avg") expr = "avg_over_time(" + sel + "[" + step + "])";
  else if (spec.agg === "sum") expr = "sum_over_time(" + sel + "[" + step + "])";
  else if (spec.agg === "count") expr = "count_over_time(" + sel + "[" + step + "])";
  else if (spec.agg && /^p\d+$/.test(spec.agg)) {
    expr = "quantile_over_time(0." + spec.agg.slice(1) + ", " + sel + "[" + step + "])";
  }
  return expr + " " + op + " " + r.threshold;
}

let ruleSeq = 3;

const rules: AlertRule[] = [
  {
    id: "11111111-1111-4111-8111-111111111111",
    name: "checkout P95 지연 3초 초과",
    signal: "metric",
    query_spec: {
      metric: "http_server_duration_seconds",
      filters: [{ label: "service", value: "checkout-api" }],
      agg: "p95",
      step_sec: 60,
    },
    query:
      'quantile_over_time(0.95, http_server_duration_seconds{klaro_org_id="mock",service="checkout-api"}[60s]) > 3',
    comparator: "gt",
    threshold: 3,
    for_duration_sec: 300,
    severity: "critical",
    channels: [{ type: "email", target: "sre@example.com" }],
    enabled: true,
    created_at: isoAgo(60 * 24 * 12),
    updated_at: isoAgo(60 * 5),
  },
  {
    id: "22222222-2222-4222-8222-222222222222",
    name: "CPU 사용률 85% 초과",
    signal: "metric",
    query_spec: { metric: "process_cpu_utilization", agg: "avg", step_sec: 60 },
    query: 'avg_over_time(process_cpu_utilization{klaro_org_id="mock"}[60s]) > 0.85',
    comparator: "gt",
    threshold: 0.85,
    for_duration_sec: 600,
    severity: "warning",
    channels: [{ type: "slack", target: "#alerts-prod" }],
    enabled: true,
    created_at: isoAgo(60 * 24 * 30),
    updated_at: isoAgo(60 * 24 * 2),
  },
  {
    id: "33333333-3333-4333-8333-333333333333",
    name: "카탈로그 처리량 급감",
    signal: "metric",
    query_spec: {
      metric: "http_server_requests_total",
      filters: [{ label: "service", value: "catalog-api" }],
      agg: "rate",
      step_sec: 300,
    },
    query: 'rate(http_server_requests_total{klaro_org_id="mock",service="catalog-api"}[300s]) < 5',
    comparator: "lt",
    threshold: 5,
    for_duration_sec: 900,
    severity: "info",
    channels: [],
    enabled: false,
    created_at: isoAgo(60 * 24 * 3),
    updated_at: isoAgo(60 * 24 * 3),
  },
];

const events: AlertEvent[] = [
  {
    id: "e1111111-1111-4111-8111-111111111111",
    rule_id: rules[0].id,
    state: "firing",
    value: 4.12,
    labels: { service: "checkout-api", severity: "critical" },
    notified_channels: [{ type: "email", target: "sre@example.com" }],
    started_at: isoAgo(18),
    resolved_at: null,
  },
  {
    id: "e2222222-2222-4222-8222-222222222222",
    rule_id: rules[1].id,
    state: "resolved",
    value: 0.91,
    labels: { host: "pod:8f3ad210", severity: "warning" },
    notified_channels: [{ type: "slack", target: "#alerts-prod" }],
    started_at: isoAgo(240),
    resolved_at: isoAgo(206),
  },
  {
    id: "e3333333-3333-4333-8333-333333333333",
    rule_id: rules[0].id,
    state: "resolved",
    value: 3.4,
    labels: { service: "checkout-api", severity: "critical" },
    started_at: isoAgo(1440),
    resolved_at: isoAgo(1400),
  },
];

export function mockListRules(): AlertRule[] {
  return rules.map((r) => ({ ...r }));
}

export function mockGetRule(id: string): AlertRule {
  const found = rules.find((r) => r.id === id);
  if (!found) throw new ApiError("NOT_FOUND", "알림 룰을 찾을 수 없습니다.", 404);
  return { ...found };
}

export function mockCreateRule(input: AlertRuleInput): AlertRule {
  const name = input.name?.trim();
  if (!name) throw new ApiError("VALIDATION_ERROR", "이름은 필수입니다.", 422);
  if (rules.some((r) => r.name === name)) {
    throw new ApiError("CONFLICT", "같은 이름의 알림 룰이 이미 있습니다.", 409);
  }
  const draft = {
    query_spec: input.query_spec ?? { metric: MOCK_METRICS[0] },
    comparator: input.comparator ?? ("gt" as const),
    threshold: input.threshold ?? 0,
  };
  const rule: AlertRule = {
    id: "44444444-4444-4444-8444-" + String(ruleSeq++).padStart(12, "0"),
    name,
    signal: "metric",
    ...draft,
    query: renderRuleQuery(draft),
    for_duration_sec: input.for_duration_sec ?? 60,
    severity: input.severity ?? "warning",
    channels: input.channels ?? [],
    enabled: input.enabled ?? true,
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  };
  rules.unshift(rule);
  return { ...rule };
}

export function mockUpdateRule(id: string, input: AlertRuleInput): AlertRule {
  const idx = rules.findIndex((r) => r.id === id);
  if (idx < 0) throw new ApiError("NOT_FOUND", "알림 룰을 찾을 수 없습니다.", 404);

  const next: AlertRule = { ...rules[idx] };
  if (input.name !== undefined) next.name = input.name;
  if (input.query_spec !== undefined) next.query_spec = input.query_spec;
  if (input.comparator !== undefined) next.comparator = input.comparator;
  if (input.threshold !== undefined) next.threshold = input.threshold;
  if (input.for_duration_sec !== undefined) next.for_duration_sec = input.for_duration_sec;
  if (input.severity !== undefined) next.severity = input.severity;
  if (input.channels !== undefined) next.channels = input.channels;
  if (input.enabled !== undefined) next.enabled = input.enabled;
  next.query = renderRuleQuery(next);
  next.updated_at = new Date().toISOString();

  rules[idx] = next;
  return { ...next };
}

export function mockDeleteRule(id: string): void {
  const idx = rules.findIndex((r) => r.id === id);
  if (idx < 0) throw new ApiError("NOT_FOUND", "알림 룰을 찾을 수 없습니다.", 404);
  rules.splice(idx, 1);
}

export function mockListEvents(filter: AlertEventFilter): AlertEvent[] {
  let out = events.slice();
  if (filter.state) out = out.filter((e) => e.state === filter.state);
  if (filter.rule_id) out = out.filter((e) => e.rule_id === filter.rule_id);
  if (filter.from) out = out.filter((e) => new Date(e.started_at) >= filter.from!);
  if (filter.to) out = out.filter((e) => new Date(e.started_at) <= filter.to!);
  return out
    .sort((a, b) => b.started_at.localeCompare(a.started_at))
    .slice(0, filter.limit && filter.limit > 0 ? filter.limit : 100)
    .map((e) => ({ ...e }));
}

let dashSeq = 2;

const dashboards: Dashboard[] = [
  {
    id: "d1111111-1111-4111-8111-111111111111",
    name: "체크아웃 서비스 개요",
    description: "결제 경로의 지연·처리량·에러 로그를 한 화면에서 본다.",
    spec: {
      range_sec: 3600,
      refresh_sec: 30,
      panels: [
        {
          id: "p-latency",
          title: "체크아웃 P95 지연",
          type: "timeseries",
          layout: { x: 0, y: 0, w: 8, h: 6 },
          query: {
            signal: "metrics",
            metric: "http_server_duration_seconds",
            filters: [{ label: "service", value: "checkout-api" }],
            agg: "p95",
            step_sec: 60,
          },
        },
        {
          id: "p-rps",
          title: "요청 처리량",
          type: "timeseries",
          layout: { x: 8, y: 0, w: 4, h: 6 },
          query: { signal: "metrics", metric: "http_server_requests_total", agg: "rate", step_sec: 60 },
        },
        {
          id: "p-errors",
          title: "에러 로그",
          type: "logs",
          layout: { x: 0, y: 6, w: 12, h: 6 },
          query: { signal: "logs", contains: "timeout", limit: 50 },
        },
      ],
    },
    created_at: isoAgo(60 * 24 * 20),
    updated_at: isoAgo(60 * 3),
  },
  {
    id: "d2222222-2222-4222-8222-222222222222",
    name: "인프라 리소스",
    description: "호스트 CPU·메모리 추세.",
    spec: {
      range_sec: 21600,
      refresh_sec: 60,
      panels: [
        {
          id: "p-cpu",
          title: "CPU 사용률",
          type: "timeseries",
          layout: { x: 0, y: 0, w: 6, h: 6 },
          query: { signal: "metrics", metric: "process_cpu_utilization", agg: "avg", step_sec: 300 },
        },
        {
          id: "p-mem",
          title: "메모리 사용량",
          type: "timeseries",
          layout: { x: 6, y: 0, w: 6, h: 6 },
          query: { signal: "metrics", metric: "process_memory_usage_bytes", agg: "avg", step_sec: 300 },
        },
      ],
    },
    created_at: isoAgo(60 * 24 * 40),
    updated_at: isoAgo(60 * 24),
  },
];

export function mockListDashboards(): Dashboard[] {
  return dashboards.map((d) => ({ ...d }));
}

export function mockGetDashboard(id: string): Dashboard {
  const found = dashboards.find((d) => d.id === id);
  if (!found) throw new ApiError("NOT_FOUND", "대시보드를 찾을 수 없습니다.", 404);
  return { ...found };
}

export function mockCreateDashboard(input: DashboardInput): Dashboard {
  const name = input.name?.trim();
  if (!name) throw new ApiError("VALIDATION_ERROR", "이름은 필수입니다.", 422);
  const dash: Dashboard = {
    id: "d3333333-3333-4333-8333-" + String(dashSeq++).padStart(12, "0"),
    name,
    description: input.description,
    spec: input.spec ?? { panels: [] },
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  };
  dashboards.unshift(dash);
  return { ...dash };
}

export function mockUpdateDashboard(id: string, input: DashboardInput): Dashboard {
  const idx = dashboards.findIndex((d) => d.id === id);
  if (idx < 0) throw new ApiError("NOT_FOUND", "대시보드를 찾을 수 없습니다.", 404);
  const next = { ...dashboards[idx] };
  if (input.name !== undefined) next.name = input.name;
  if (input.description !== undefined) next.description = input.description;
  if (input.spec !== undefined) next.spec = input.spec;
  next.updated_at = new Date().toISOString();
  dashboards[idx] = next;
  return { ...next };
}

export function mockDeleteDashboard(id: string): void {
  const idx = dashboards.findIndex((d) => d.id === id);
  if (idx < 0) throw new ApiError("NOT_FOUND", "대시보드를 찾을 수 없습니다.", 404);
  dashboards.splice(idx, 1);
}

export function mockQuota(): Quota {
  return {
    plan_code: "pro",
    active_hosts: 14,
    host_limit: 25,
    ingest_bytes: 61_203_884_032,
    ingest_gb: 57.0,
    ingest_limit_gb: 100,
    hosts_exceeded: false,
    ingest_exceeded: false,
    overage: false,
  };
}
