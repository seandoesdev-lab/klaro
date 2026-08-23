/**
 * One typed function per obsplane endpoint, and the single place mock mode is
 * switched on.
 *
 * The branch lives here rather than inside a fake `fetch` so the query-string
 * construction - which is the part that has to agree with the Go handlers - is
 * exercised in both modes by the same code path above it.
 */

import { config } from "@/lib/config";
import * as mock from "@/lib/mock/fixtures";
import { request } from "./client";
import type {
  AlertEvent,
  AlertEventFilter,
  AlertRule,
  AlertRuleInput,
  Dashboard,
  DashboardInput,
  ListEnvelope,
  LogsPage,
  Matcher,
  MetricsResult,
  Quota,
  Trace,
  TracesResult,
} from "./types";

/** obsplane parses RFC3339 or a unix timestamp; ISO is the unambiguous one. */
function iso(d: Date): string {
  return d.toISOString();
}

/**
 * Value of the first positive matcher on `label`, or undefined.
 *
 * Undefined rather than "" matters: the mock treats an empty string as a real
 * (empty) value and would filter everything out, while the wire form simply
 * omits an incomplete matcher.
 */
function matcherValue(filters: Matcher[], label: string): string | undefined {
  const found = filters.find((f) => f.label === label && !f.negate && f.value !== "");
  return found?.value;
}

/**
 * Serialise matchers the way explorer.ParseFilters reads them back:
 * one repeated `filter` parameter per matcher, `label=value` or `label!=value`.
 */
function appendFilters(params: URLSearchParams, filters: Matcher[]): void {
  for (const f of filters) {
    if (!f.label || !f.value) continue;
    params.append("filter", f.label + (f.negate ? "!=" : "=") + f.value);
  }
}

/* -------------------------------------------------------------- metrics -- */

export interface MetricsArgs {
  from: Date;
  to: Date;
  metric: string;
  filters: Matcher[];
  agg: string;
  stepSec: number;
  signal?: AbortSignal;
}

export async function queryMetrics(args: MetricsArgs): Promise<MetricsResult> {
  if (config.mock) {
    return mock.delay(
      mock.mockMetrics({
        metric: args.metric || "http_server_duration_seconds",
        fromMs: args.from.getTime(),
        toMs: args.to.getTime(),
        stepSec: args.stepSec,
        agg: args.agg,
        serviceFilter: matcherValue(args.filters, "service"),
      }),
    );
  }

  const params = new URLSearchParams({ from: iso(args.from), to: iso(args.to) });
  if (args.metric) params.set("metric", args.metric);
  if (args.agg) params.set("agg", args.agg);
  if (args.stepSec > 0) params.set("step", String(args.stepSec));
  appendFilters(params, args.filters);

  return request<MetricsResult>("/obs/metrics/query", { params, signal: args.signal });
}

/* --------------------------------------------------------------- traces -- */

export interface TracesArgs {
  from: Date;
  to: Date;
  service?: string;
  minDurationMs?: number;
  limit?: number;
  signal?: AbortSignal;
}

export async function searchTraces(args: TracesArgs): Promise<TracesResult> {
  if (config.mock) {
    return mock.delay(
      mock.mockTraces({
        fromMs: args.from.getTime(),
        toMs: args.to.getTime(),
        service: args.service,
        minDurationMs: args.minDurationMs,
        limit: args.limit,
      }),
    );
  }

  const params = new URLSearchParams({ from: iso(args.from), to: iso(args.to) });
  if (args.service) params.set("service", args.service);
  if (args.minDurationMs && args.minDurationMs > 0) {
    params.set("min_duration_ms", String(args.minDurationMs));
  }
  if (args.limit && args.limit > 0) params.set("limit", String(args.limit));

  return request<TracesResult>("/obs/traces", { params, signal: args.signal });
}

export async function getTrace(traceId: string, signal?: AbortSignal): Promise<Trace> {
  if (config.mock) return mock.delay(mock.mockTrace(traceId));
  return request<Trace>("/obs/traces/" + encodeURIComponent(traceId), { signal });
}

/* ----------------------------------------------------------------- logs -- */

export interface LogsArgs {
  from: Date;
  to: Date;
  filters: Matcher[];
  /** Literal substring; obsplane refuses regex and raw LogQL by design. */
  contains?: string;
  limit?: number;
  signal?: AbortSignal;
}

export async function queryLogs(args: LogsArgs): Promise<LogsPage> {
  if (config.mock) {
    return mock.delay(
      mock.mockLogs({
        fromMs: args.from.getTime(),
        toMs: args.to.getTime(),
        contains: args.contains,
        level: matcherValue(args.filters, "level"),
        service: matcherValue(args.filters, "service"),
        limit: args.limit,
      }),
    );
  }

  const params = new URLSearchParams({ from: iso(args.from), to: iso(args.to) });
  if (args.contains) params.set("query", args.contains);
  if (args.limit && args.limit > 0) params.set("limit", String(args.limit));
  appendFilters(params, args.filters);

  return request<LogsPage>("/obs/logs", { params, signal: args.signal });
}

/* -------------------------------------------------------------- alerting -- */

export async function listAlertRules(signal?: AbortSignal): Promise<AlertRule[]> {
  if (config.mock) return mock.delay(mock.mockListRules());
  const res = await request<ListEnvelope<AlertRule>>("/obs/alert-rules", { signal });
  return res.data ?? [];
}

export async function getAlertRule(id: string, signal?: AbortSignal): Promise<AlertRule> {
  if (config.mock) return mock.delay(mock.mockGetRule(id));
  return request<AlertRule>("/obs/alert-rules/" + encodeURIComponent(id), { signal });
}

export async function createAlertRule(input: AlertRuleInput): Promise<AlertRule> {
  if (config.mock) return mock.delay(mock.mockCreateRule(input));
  return request<AlertRule>("/obs/alert-rules", { method: "POST", body: input });
}

export async function updateAlertRule(id: string, input: AlertRuleInput): Promise<AlertRule> {
  if (config.mock) return mock.delay(mock.mockUpdateRule(id, input));
  return request<AlertRule>("/obs/alert-rules/" + encodeURIComponent(id), {
    method: "PATCH",
    body: input,
  });
}

export async function deleteAlertRule(id: string): Promise<void> {
  if (config.mock) {
    await mock.delay(mock.mockDeleteRule(id));
    return;
  }
  await request<void>("/obs/alert-rules/" + encodeURIComponent(id), { method: "DELETE" });
}

export async function listAlertEvents(
  filter: AlertEventFilter = {},
  signal?: AbortSignal,
): Promise<AlertEvent[]> {
  if (config.mock) return mock.delay(mock.mockListEvents(filter));

  const params = new URLSearchParams();
  if (filter.state) params.set("state", filter.state);
  if (filter.rule_id) params.set("rule_id", filter.rule_id);
  if (filter.from) params.set("from", iso(filter.from));
  if (filter.to) params.set("to", iso(filter.to));
  if (filter.limit && filter.limit > 0) params.set("limit", String(filter.limit));

  const res = await request<ListEnvelope<AlertEvent>>("/obs/alert-events", { params, signal });
  return res.data ?? [];
}

/* ------------------------------------------------------------ dashboards -- */

export async function listDashboards(signal?: AbortSignal): Promise<Dashboard[]> {
  if (config.mock) return mock.delay(mock.mockListDashboards());
  const res = await request<ListEnvelope<Dashboard>>("/obs/dashboards", { signal });
  return res.data ?? [];
}

export async function getDashboard(id: string, signal?: AbortSignal): Promise<Dashboard> {
  if (config.mock) return mock.delay(mock.mockGetDashboard(id));
  return request<Dashboard>("/obs/dashboards/" + encodeURIComponent(id), { signal });
}

export async function createDashboard(input: DashboardInput): Promise<Dashboard> {
  if (config.mock) return mock.delay(mock.mockCreateDashboard(input));
  return request<Dashboard>("/obs/dashboards", { method: "POST", body: input });
}

export async function updateDashboard(id: string, input: DashboardInput): Promise<Dashboard> {
  if (config.mock) return mock.delay(mock.mockUpdateDashboard(id, input));
  return request<Dashboard>("/obs/dashboards/" + encodeURIComponent(id), {
    method: "PATCH",
    body: input,
  });
}

export async function deleteDashboard(id: string): Promise<void> {
  if (config.mock) {
    await mock.delay(mock.mockDeleteDashboard(id));
    return;
  }
  await request<void>("/obs/dashboards/" + encodeURIComponent(id), { method: "DELETE" });
}

/* ----------------------------------------------------------------- quota -- */

export async function getQuota(signal?: AbortSignal): Promise<Quota> {
  if (config.mock) return mock.delay(mock.mockQuota());
  return request<Quota>("/obs/quota", { signal });
}
