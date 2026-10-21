#!/usr/bin/env node
// Issue a ScopeWeaver admin JWT offline.
//
// The backend signs tokens with HS256 over a key derived as
// HMAC-SHA256(jwt.key, <stored bcrypt hash>). Both inputs live on this
// machine, so a token can be produced without the plaintext password.
//
// This is a LOCAL self-hosted convenience for the operator of this machine.
// It is not a login bypass against a remote deployment: it needs filesystem
// read access to jwt.key and to the database holding the password hash.
//
// Usage:
//   node issue-token.js --root <repo dir> [--dsn <pg dsn>] [--hash-file <f>]
//                       [--psql <path to psql.exe>] [--out <file>]
//
// The token is printed to stdout. The password hash never leaves this host.

import { readFileSync, writeFileSync, existsSync, readdirSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { createHmac } from "node:crypto";
import path from "node:path";

function parseArgs(argv) {
  const args = { root: process.cwd(), dsn: "", out: "", hashFile: "", psql: "" };
  for (let i = 0; i < argv.length; i += 1) {
    const key = argv[i];
    if (key === "--root") args.root = argv[++i];
    else if (key === "--dsn") args.dsn = argv[++i];
    else if (key === "--out") args.out = argv[++i];
    else if (key === "--hash-file") args.hashFile = argv[++i];
    else if (key === "--psql") args.psql = argv[++i];
  }
  return args;
}

function readConfig(root) {
  try {
    return JSON.parse(readFileSync(path.join(root, "config.json"), "utf8"));
  } catch {
    return null;
  }
}

// Locate psql.exe: explicit --psql, PATH, then the standard Windows install layout.
function findPsql(explicit) {
  if (explicit) return explicit;
  const pathHit = process.env.PATH || "";
  for (const dir of pathHit.split(path.delimiter)) {
    if (!dir) continue;
    const candidate = path.join(dir, "psql.exe");
    if (existsSync(candidate)) return candidate;
  }
  const base = "C:\\Program Files\\PostgreSQL";
  if (existsSync(base)) {
    const versions = readdirSync(base).sort().reverse();
    for (const v of versions) {
      const candidate = path.join(base, v, "bin", "psql.exe");
      if (existsSync(candidate)) return candidate;
    }
  }
  throw new Error("psql.exe not found. Pass --psql <path>.");
}

// Read the stored bcrypt hash from PostgreSQL.
function readHash(root, dsnOverride, psql, hashFile) {
  if (hashFile) return readFileSync(hashFile, "utf8").trim();
  const cfg = readConfig(root);
  const dsn = dsnOverride || process.env.ARTEX_PG_DSN ||
    (cfg && cfg.database
      ? `postgresql://${cfg.database.user}:${cfg.database.password}@127.0.0.1:${cfg.database.port}/${cfg.database.dbname}`
      : "");
  if (!dsn) throw new Error("No database DSN. Pass --dsn or set ARTEX_PG_DSN.");
  const url = new URL(dsn);
  const sql = "SELECT value FROM settings WHERE key='auth.password_hash'";
  const out = execFileSync(psql, [
    "-w", "-h", url.hostname, "-p", url.port || "5432",
    "-U", decodeURIComponent(url.username), "-d", url.pathname.replace(/^\//, ""),
    "-t", "-A", "-c", sql,
  ], { env: { ...process.env, PGPASSWORD: decodeURIComponent(url.password) }, encoding: "utf8" });
  const hash = out.trim();
  if (!hash.startsWith("$2")) throw new Error("No bcrypt password hash stored; cannot sign a token.");
  return hash;
}

function base64url(value) {
  return Buffer.from(value).toString("base64").replace(/=+$/, "").replace(/\+/g, "-").replace(/\//g, "_");
}

function sign(payload, key) {
  const header = base64url(JSON.stringify({ alg: "HS256", typ: "JWT" }));
  const body = base64url(JSON.stringify(payload));
  const data = `${header}.${body}`;
  const sig = base64url(createHmac("sha256", key).update(data).digest());
  return `${data}.${sig}`;
}

const args = parseArgs(process.argv.slice(2));
const root = path.resolve(args.root);
const hash = readHash(root, args.dsn, findPsql(args.psql), args.hashFile);
const rawKey = readFileSync(path.join(root, "jwt.key"), "utf8").trim();
const signingKey = createHmac("sha256", rawKey).update(hash).digest();
const now = Math.floor(Date.now() / 1000);
const token = sign({ sub: "ARTEX", exp: now + 7 * 24 * 3600, iat: now }, signingKey);
if (args.out) writeFileSync(args.out, token, "utf8");
process.stdout.write(token);
