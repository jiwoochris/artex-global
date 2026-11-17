/**
 * Script: check-i18n-keys.ts
 *
 * 로케일 메시지 파일(web/messages/{ko,en,zh,es,ja}.json)의 키 정합성 가드.
 * ko.json 을 기준으로 나머지 로케일의 키 집합이 완전히 일치하는지(누락·추가 모두) 검사하고,
 * 불일치가 있으면 비-0 으로 종료한다. 실행: npm run check:i18n
 */

import { diffKeys, flattenKeys, isInSync } from "./i18n-keys";
import fs from "node:fs";
import path from "node:path";

const MESSAGES_DIR = path.resolve(__dirname, "../../messages");
const BASELINE = "ko";
const LOCALES = ["en", "zh", "es", "ja"] as const;

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
console.log(`i18n 키 정합성 검사 (기준: ${BASELINE}.json, ${baseline.size} 키)`);
console.log("─".repeat(64));

for (const locale of LOCALES) {
  const diff = diffKeys(baseline, readLocale(locale));
  if (isInSync(diff)) {
    console.log(`[OK] ${locale}.json — 일치`);
    continue;
  }
  failed = true;
  console.log(`[FAIL] ${locale}.json — 불일치`);
  if (diff.missing.length) console.log(`   누락 ${diff.missing.length}: ${preview(diff.missing)}`);
  if (diff.extra.length) console.log(`   추가 ${diff.extra.length}: ${preview(diff.extra)}`);
}

console.log("─".repeat(64));
if (failed) {
  console.error("i18n 키가 어긋났습니다. 누락/추가 키를 맞춘 뒤 다시 실행하세요.");
  process.exit(1);
}
console.log("모든 로케일의 키 집합이 일치합니다.");
