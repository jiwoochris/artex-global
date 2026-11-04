/**
 * Script: check-i18n-keys.ts
 *
 * Key-parity guard for the locale message files (web/messages/{en,es}.json). Compares every
 * other locale's key set against the `en` baseline (both missing and extra keys) and exits
 * non-zero on a mismatch. Run: npm run check:i18n
 */

import { diffKeys, flattenKeys, isInSync } from "./i18n-keys";
import fs from "node:fs";
import path from "node:path";

const MESSAGES_DIR = path.resolve(__dirname, "../../messages");
const BASELINE = "en";
const LOCALES = ["es"] as const;

function readLocale(locale: string): Set<string> {
  const file = path.join(MESSAGES_DIR, `${locale}.json`);
  const tree = JSON.parse(fs.readFileSync(file, "utf8")) as unknown;
  return flattenKeys(tree);
}

function preview(keys: string[]): string {
  const shown = keys.slice(0, 20).join(", ");
  return keys.length > 20 ? `${shown} …` : shown;
}

const baseline = readLocale(BASELINE);
let failed = false;

console.log("─".repeat(64));
console.log(`i18n key parity check (baseline: ${BASELINE}.json, ${baseline.size} keys)`);
console.log("─".repeat(64));

for (const locale of LOCALES) {
  const diff = diffKeys(baseline, readLocale(locale));
  if (isInSync(diff)) {
    console.log(`[OK] ${locale}.json — in sync`);
    continue;
  }
  failed = true;
  console.log(`[FAIL] ${locale}.json — out of sync`);
  if (diff.missing.length) console.log(`   missing ${diff.missing.length}: ${preview(diff.missing)}`);
  if (diff.extra.length) console.log(`   extra ${diff.extra.length}: ${preview(diff.extra)}`);
}

console.log("─".repeat(64));
if (failed) {
  console.error("i18n keys are out of sync. Align the missing/extra keys and run again.");
  process.exit(1);
}
console.log("All locale key sets match.");
