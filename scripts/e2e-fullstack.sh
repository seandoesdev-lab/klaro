#!/usr/bin/env bash
# One command that brings the whole local stack up and proves the dashboard can
# see real data through it.
#
# It exists because "it works on my machine" for this system means eleven
# containers, an org row, a signed token, an ingest key, a metric that survives
# three hops, and a Next build - and every one of those has a failure mode that
# looks like one of the others from the browser. Each step below prints PASS or
# FAIL under its own name, so the first broken link is named rather than guessed.
#
# Steps:
#   1. docker compose up --build -d      (postgres redis obsplane collector
#                                         VM cluster tempo loki vmalert mailhog)
#   2. wait for obsplane /readyz         (it migrates on boot, so this is not
#                                         instant even when nothing is wrong)
#   3. seed org + mint JWT + issue ingest key, write the dashboard .env.local
#   4. CORS preflight from the dashboard origin
#   5. live WS (subprotocol auth) + OTLP ingest + Explorer read-back
#   6. host metrics through the agent -> gateway -> VM -> GET /obs/hosts + SLO
#   7. next build
#
# Usage:  ./scripts/e2e-fullstack.sh [--no-build] [--keep-up|--down]
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEPLOY_DIR="$REPO_ROOT/services/observability/deploy"
DASHBOARD_DIR="$REPO_ROOT/apps/observability-dashboard"
COMPOSE=(docker compose -f "$DEPLOY_DIR/docker-compose.yml")

OBSPLANE_URL="${OBSPLANE_URL:-http://localhost:8090}"
OTLP_URL="${OTLP_URL:-http://localhost:4318}"
DASHBOARD_ORIGIN="${DASHBOARD_ORIGIN:-http://localhost:3100}"
BUILD_FLAG="--build"
TEARDOWN=0

while [ $# -gt 0 ]; do
  case "$1" in
    --no-build) BUILD_FLAG=""; shift ;;
    --keep-up)  TEARDOWN=0; shift ;;
    --down)     TEARDOWN=1; shift ;;
    *) echo "e2e-fullstack: unknown argument: $1" >&2; exit 2 ;;
  esac
done

FAILURES=0
pass() { printf 'PASS  %s%s\n' "$1" "${2:+ - $2}"; }
fail() { printf 'FAIL  %s%s\n' "$1" "${2:+ - $2}"; FAILURES=$((FAILURES + 1)); }
step() { printf '\n=== %s\n' "$1"; }

finish() {
  printf '\n'
  if [ "$TEARDOWN" = "1" ]; then
    step "docker compose down"
    "${COMPOSE[@]}" down -v >/dev/null 2>&1 || true
  fi
  if [ "$FAILURES" -eq 0 ]; then
    echo "e2e-fullstack: all steps passed"
    echo "dashboard:  cd apps/observability-dashboard && npm run dev   ->  $DASHBOARD_ORIGIN/live"
    exit 0
  fi
  echo "e2e-fullstack: $FAILURES step(s) failed"
  exit 1
}

# --- 1. bring the stack up --------------------------------------------------
step "docker compose up $BUILD_FLAG"
if "${COMPOSE[@]}" up $BUILD_FLAG -d; then
  pass "compose up" "$("${COMPOSE[@]}" ps --services | wc -l | tr -d ' ') services"
else
  fail "compose up" "see the output above"
  finish
fi

# --- 2. wait for obsplane ---------------------------------------------------
# /readyz, not /healthz: liveness answers before the database is reachable, and a
# request that races the boot migration fails in a way that looks like a bug.
step "wait for obsplane readiness"
READY=0
for _ in $(seq 1 90); do
  if curl -fsS "$OBSPLANE_URL/readyz" >/dev/null 2>&1; then READY=1; break; fi
  sleep 2
done
if [ "$READY" = "1" ]; then
  pass "obsplane /readyz"
else
  fail "obsplane /readyz" "not ready within 180s; try: docker compose logs obsplane"
  finish
fi

# --- 3. seed the org, the token and an ingest key ---------------------------
step "seed org + dev JWT + ingest key"
SEED_ENV="$(mktemp)"
if OBSPLANE_URL="$OBSPLANE_URL" bash "$DEPLOY_DIR/scripts/seed-dev.sh" >"$SEED_ENV"; then
  # shellcheck disable=SC1090
  set -a; . "$SEED_ENV"; set +a
  rm -f "$SEED_ENV"
  pass "seed-dev.sh" "org $KLARO_ORG_ID, token minted, ingest key issued"
else
  fail "seed-dev.sh" "see the output above"
  rm -f "$SEED_ENV"
  finish
fi

# --- 4. CORS preflight ------------------------------------------------------
# The browser sends this before every cross-origin fetch and attaches no
# credential to it, so it has to be answered ahead of authentication. A 401 here
# surfaces as a CORS error in the console and hides the real cause.
step "CORS preflight from $DASHBOARD_ORIGIN"
PREFLIGHT="$(curl -sS -o /dev/null -D - -X OPTIONS \
  -H "Origin: $DASHBOARD_ORIGIN" \
  -H 'Access-Control-Request-Method: GET' \
  -H 'Access-Control-Request-Headers: authorization' \
  "$OBSPLANE_URL/orgs/$KLARO_ORG_ID/obs/metrics/query" 2>&1)"
if printf '%s' "$PREFLIGHT" | grep -qi "204" &&
   printf '%s' "$PREFLIGHT" | grep -qi "access-control-allow-origin: $DASHBOARD_ORIGIN" &&
   printf '%s' "$PREFLIGHT" | grep -qi "access-control-allow-headers:.*Authorization"; then
  pass "CORS preflight" "204 with the origin and Authorization allowed"
else
  fail "CORS preflight" "$(printf '%s' "$PREFLIGHT" | head -8 | tr '\n' ' ')"
fi

# --- 5. live WS + ingest + read-back ---------------------------------------
step "live WS (subprotocol auth) + OTLP ingest + Explorer read-back"
if OBSPLANE_URL="$OBSPLANE_URL" OTLP_URL="$OTLP_URL" \
   KLARO_ORG_ID="$KLARO_ORG_ID" KLARO_OBS_TOKEN="$KLARO_OBS_TOKEN" KLARO_OBS_KEY="$KLARO_OBS_KEY" \
   node "$REPO_ROOT/scripts/verify-live.mjs"; then
  pass "verify-live.mjs" "handshake, ingest, live frame and REST samples"
else
  fail "verify-live.mjs" "see its own per-check output above"
fi

# --- 6. host metrics reach VictoriaMetrics and come back as an inventory ----
# The one check that covers the whole P1b chain in a single assertion: the
# hostmetrics agent scrapes, the gateway stamps the org, vminsert routes on the
# AccountID, and the inventory joins the registry row back onto the series. Any
# broken link shows up here as an empty list.
#
# It waits rather than asserting immediately: seed-dev.sh recreates the agent
# with the key it just issued, and the first scrape plus the usage flush have to
# land before there is anything to join.
step "hostmetrics -> VictoriaMetrics -> GET /obs/hosts"
HOSTS_JSON=""
for _ in $(seq 1 30); do
  HOSTS_JSON="$(curl -fsS "$OBSPLANE_URL/orgs/$KLARO_ORG_ID/obs/hosts"     -H "Authorization: Bearer $KLARO_OBS_TOKEN" 2>/dev/null || true)"
  if printf '%s' "$HOSTS_JSON" | grep -q '"host_ident"'; then break; fi
  sleep 4
done
HOST_SUMMARY="$(printf '%s' "$HOSTS_JSON" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{
  let r; try { r = JSON.parse(s); } catch { process.stdout.write("unparseable response"); return; }
  const h = (r.data ?? [])[0];
  if (!h) { process.stdout.write("no hosts registered"); return; }
  const cpu = h.cpu_pct == null ? "cpu unread" : "cpu " + h.cpu_pct.toFixed(1) + "%";
  const mem = h.mem_pct == null ? "mem unread" : "mem " + h.mem_pct.toFixed(1) + "%";
  process.stdout.write(r.data.length + " host(s), " + h.host_ident + ": " + cpu + ", " + mem +
    (r.metrics_available ? "" : " (metrics backend unavailable)"));})')"
# A registry row with no readings means the join key drifted (host_ident vs the
# instance label), which is a different failure from "nothing reported at all".
if printf '%s' "$HOSTS_JSON" | grep -q '"cpu_pct":[0-9]'; then
  pass "GET /obs/hosts" "$HOST_SUMMARY"
elif printf '%s' "$HOSTS_JSON" | grep -q '"host_ident"'; then
  fail "GET /obs/hosts" "registered but no readings joined - $HOST_SUMMARY"
else
  fail "GET /obs/hosts" "$HOST_SUMMARY; try: docker compose logs otel-hostagent"
fi

# The SLO endpoint has to answer with a well-formed measurement even when the
# agent has only just started - an empty fleet is an empty state, never 100%.
UPTIME_JSON="$(curl -fsS "$OBSPLANE_URL/orgs/$KLARO_ORG_ID/obs/slo/uptime?step=60&target=0.99"   -H "Authorization: Bearer $KLARO_OBS_TOKEN" 2>/dev/null || true)"
if printf '%s' "$UPTIME_JSON" | grep -q '"expected_buckets"'; then
  pass "GET /obs/slo/uptime" "$(printf '%s' "$UPTIME_JSON" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{
    const r=JSON.parse(s);
    process.stdout.write((r.availability*100).toFixed(2)+"% of "+r.expected_buckets+" buckets, "+r.hosts.length+" host(s)");})')"
else
  fail "GET /obs/slo/uptime" "$(printf '%s' "$UPTIME_JSON" | head -c 200)"
fi

# --- 7. the dashboard builds ------------------------------------------------
# A build, not a dev server: it type-checks every screen, which is where a drift
# between the API types here and the Go handlers would actually show up.
step "next build"
if [ ! -d "$DASHBOARD_DIR/node_modules" ]; then
  echo "installing dashboard dependencies"
  (cd "$DASHBOARD_DIR" && npm ci --no-audit --no-fund) ||
    (cd "$DASHBOARD_DIR" && npm install --no-audit --no-fund)
fi
BUILD_LOG="$(mktemp)"
if (cd "$DASHBOARD_DIR" && npm run build >"$BUILD_LOG" 2>&1); then
  pass "next build"
  rm -f "$BUILD_LOG"
else
  fail "next build" "$(tail -20 "$BUILD_LOG" | tr '\n' ' ')"
  echo "full build log: $BUILD_LOG"
fi

finish
