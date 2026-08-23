#!/usr/bin/env bash
# Seed the local observability stack for the dashboard and print the credentials.
#
# It does the three things that are otherwise easy to get subtly wrong by hand:
#
#   1. seeds the org row. The dashboard's org is not seeded by a migration -
#      migrations own the schema, not a tenant - so without this every request
#      authenticates fine and then finds no plan, which reads as "quota is
#      broken" rather than "this org does not exist".
#   2. mints a JWT whose org_id matches that row. A token for another org is a
#      403 on REST and a 4403 close on the live socket, and both look like a
#      server fault from the browser.
#   3. issues an ingest key, so telemetry can actually be pushed in. A
#      dashboard connected to an empty backend is indistinguishable from a
#      broken one.
#
# Everything is idempotent: re-running re-seeds the org, mints a fresh token and
# issues an additional key. Keys are never reused, because obsplane stores only
# sha256(secret) and cannot show an old one again.
#
# Usage:  ./seed-dev.sh [--org <uuid>] [--plan free|pro|enterprise] [--no-key]
set -euo pipefail

ORG="00000000-0000-0000-0000-000000000001"
PLAN="pro"
ISSUE_KEY=1

while [ $# -gt 0 ]; do
  case "$1" in
    --org)    ORG="$2"; shift 2 ;;
    --plan)   PLAN="$2"; shift 2 ;;
    --no-key) ISSUE_KEY=0; shift ;;
    *) echo "seed-dev: unknown argument: $1" >&2; exit 2 ;;
  esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$DEPLOY_DIR/../../.." && pwd)"
DASHBOARD_DIR="$REPO_ROOT/apps/observability-dashboard"

OBSPLANE_URL="${OBSPLANE_URL:-http://localhost:8090}"
COMPOSE=(docker compose -f "$DEPLOY_DIR/docker-compose.yml")

log() { echo "seed-dev: $*" >&2; }

# --- 1. the org row ---------------------------------------------------------
# plan_code carries the retention window and the quota baseline, so a seeded
# org without one would read every telemetry query back through the Free plan's
# 1-day clamp.
log "seeding org $ORG (plan $PLAN)"
"${COMPOSE[@]}" exec -T postgres psql -v ON_ERROR_STOP=1 -q -U klaro -d klaro_obs -c \
  "INSERT INTO organizations (id, name, plan_code) VALUES ('$ORG', 'dev', '$PLAN')
     ON CONFLICT (id) DO UPDATE SET plan_code = EXCLUDED.plan_code" >/dev/null

# --- 2. the token -----------------------------------------------------------
# admin, not owner: it is the lowest role that can still issue a key and edit a
# rule, so the dashboard is exercised at the privilege it actually needs.
TOKEN="$(node "$SCRIPT_DIR/dev-token.mjs" --org "$ORG" --role admin)"

# --- 3. an ingest key -------------------------------------------------------
KEY=""
if [ "$ISSUE_KEY" = "1" ]; then
  log "issuing an ingest key"
  KEY_JSON="$(curl -fsS -X POST "$OBSPLANE_URL/orgs/$ORG/obs/keys" \
    -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d '{"name":"local-dev"}')"
  # The secret is in this response and nowhere else, ever again.
  KEY="$(printf '%s' "$KEY_JSON" | node -e \
    'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>process.stdout.write(JSON.parse(s).secret??""))')"
  [ -n "$KEY" ] || { echo "seed-dev: no secret in key response: $KEY_JSON" >&2; exit 1; }
fi

# --- 4. the dashboard's environment ----------------------------------------
# .env.local is gitignored, so a real token never reaches a commit. Mock mode is
# switched off here: that is the whole point of having run this script.
ENV_FILE="$DASHBOARD_DIR/.env.local"
cat > "$ENV_FILE" <<ENVEOF
# Written by services/observability/deploy/scripts/seed-dev.sh - do not commit.
NEXT_PUBLIC_OBS_API_BASE=$OBSPLANE_URL
NEXT_PUBLIC_OBS_ORG_ID=$ORG
NEXT_PUBLIC_OBS_TOKEN=$TOKEN
NEXT_PUBLIC_OBS_WS_AUTH_MODE=subprotocol
NEXT_PUBLIC_OBS_MOCK=0
ENVEOF
log "wrote $ENV_FILE"

# stdout is machine-readable so a wrapper (scripts/e2e-fullstack.sh) can eval it.
echo "KLARO_ORG_ID=$ORG"
echo "KLARO_OBS_TOKEN=$TOKEN"
[ -n "$KEY" ] && echo "KLARO_OBS_KEY=$KEY"
exit 0
