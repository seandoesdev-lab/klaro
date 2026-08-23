#!/usr/bin/env node
/**
 * Mint a development HS256 JWT for the obsplane public plane.
 *
 * This is a *development* issuer, not a second authentication path: obsplane
 * verifies whatever this prints with the same tenancy.JWTAuthenticator that
 * production uses, against the same jwtauth.Verifier, with the same required
 * claims. The only thing local about it is where the secret comes from
 * (KLARO_DEV_JWT_SECRET / the compose default) and that nobody logged in to
 * get it.
 *
 * That is deliberately not the same as OBS_DEV_AUTH. The dev stub replaces the
 * verifier with a string comparison, which means the JWT path - the one that
 * ships - is never exercised locally, and the first time it runs is in
 * production. Minting a real token instead keeps the local and the deployed
 * code path identical.
 *
 * Node's crypto module does the HMAC; there is no dependency to install, so
 * this stays runnable in a clean checkout ([COST-05]: no new paid dependency,
 * and here not even a free one).
 *
 * Usage:
 *   node dev-token.mjs [--org <uuid>] [--role owner|admin|member|viewer]
 *                      [--sub <subject>] [--ttl <seconds>] [--secret <s>]
 *                      [--issuer <iss>] [--audience <aud>]
 *
 * Prints the token on stdout and nothing else, so it can be captured:
 *   TOKEN=$(node dev-token.mjs)
 */

import { createHmac } from "node:crypto";

/** Defaults mirror deploy/docker-compose.yml. Change both or neither. */
const DEFAULTS = {
  org: "00000000-0000-0000-0000-000000000001",
  role: "admin",
  sub: "dev@klaro.local",
  ttl: 86400,
  secret: process.env.KLARO_DEV_JWT_SECRET ?? "klaro-dev-jwt-secret-change-me-0123456789",
  issuer: process.env.KLARO_DEV_JWT_ISSUER ?? "klaro-dev",
  audience: process.env.KLARO_DEV_JWT_AUDIENCE ?? "klaro-obs",
};

const ROLES = ["owner", "admin", "member", "viewer"];
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

function parseArgs(argv) {
  const out = { ...DEFAULTS };
  for (let i = 0; i < argv.length; i += 2) {
    const key = argv[i].replace(/^--/, "");
    const value = argv[i + 1];
    if (!(key in DEFAULTS) || value === undefined) {
      throw new Error(`unknown or incomplete flag: ${argv[i]}`);
    }
    out[key] = key === "ttl" ? Number(value) : value;
  }
  return out;
}

/**
 * Reject unusable claims here rather than letting obsplane answer 401.
 *
 * A minting script that happily produces a token with a role that does not
 * exist turns a typo into "authentication is broken", which is the least
 * informative possible failure. The floors are obsplane's own: jwtauth.New
 * refuses an HS256 secret under 32 bytes, and tenancy.ParseRole knows four
 * roles.
 */
function validate(a) {
  const problems = [];
  if (!UUID.test(a.org)) problems.push(`--org must be a uuid, got ${a.org}`);
  if (!ROLES.includes(a.role)) problems.push(`--role must be one of ${ROLES.join("|")}, got ${a.role}`);
  if (!Number.isInteger(a.ttl) || a.ttl < 1) problems.push(`--ttl must be a positive integer, got ${a.ttl}`);
  if (Buffer.byteLength(a.secret) < 32) {
    problems.push(
      `the HS256 secret must be at least 32 bytes (obsplane jwtauth.New), got ${Buffer.byteLength(a.secret)}`,
    );
  }
  if (problems.length > 0) throw new Error(problems.join("; "));
}

const b64url = (buf) => Buffer.from(buf).toString("base64url");

function sign({ org, role, sub, ttl, secret, issuer, audience }) {
  const now = Math.floor(Date.now() / 1000);
  const header = { alg: "HS256", typ: "JWT" };
  // exp is required by obsplane: a bearer credential that never expires cannot
  // be revoked by waiting, so every leak of one would be permanent.
  const payload = {
    sub,
    org_id: org,
    role,
    iat: now,
    exp: now + ttl,
    ...(issuer ? { iss: issuer } : {}),
    ...(audience ? { aud: audience } : {}),
  };
  const signing = `${b64url(JSON.stringify(header))}.${b64url(JSON.stringify(payload))}`;
  return `${signing}.${createHmac("sha256", secret).update(signing).digest("base64url")}`;
}

try {
  const args = parseArgs(process.argv.slice(2));
  validate(args);
  process.stdout.write(sign(args) + "\n");
} catch (err) {
  process.stderr.write(`dev-token: ${err.message}\n`);
  process.exit(1);
}
