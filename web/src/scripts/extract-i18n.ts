/**
 * Script: extract-i18n.ts
 *
 * ARTEX Korean localization — first-pass UI string extractor.
 *
 * Parses every `.ts`/`.tsx` under `src/` into a TypeScript AST and extracts only the hard-coded
 * Chinese strings that are shown to users. There are three kinds of extraction targets:
 *   1) string literals           e.g. title: "仪表盘" ("Dashboard")
 *   2) template literals         e.g. `已删除 ${n} 个对话` → "已删除 {var0} 个对话" ("Deleted {n} conversations")
 *   3) JSX text nodes            e.g. <span>故障转移</span> ("Failover")
 * Comments (`//`, `/* *\/`) are not AST nodes, so they are excluded automatically. Chinese in leading or
 * trailing comments is not a translation target (BRIEF: translating comments is the lowest priority).
 *
 * Outputs (web/messages/):
 *   - zh.json          { key: "original Chinese" } nested by namespace  (original kept for comparison with upstream)
 *   - ko.json          same skeleton, values are "" (filled in with Korean in B3)
 *   - zh.sources.json  per-key source (file:line), kind and placeholders — for B2 wiring, B3 translation and drift tracking
 *
 * Re-extraction is non-destructive. Only the top-level namespaces the extraction produces (based on file paths:
 * app, components, lib, etc.) are refreshed from the code; all other "curated namespaces"
 * (hand-translated nav, search, header, etc.) are preserved as-is from the existing files. Rule:
 * extracted namespaces are an inventory and are not edited by hand, and runtime translations live only in curated
 * namespaces. That way re-extraction never overwrites translations.
 *
 * Namespaces are derived from file paths, e.g. src/app/(main)/system/llm/page.tsx
 *   → app.main.system.llm.page  (route-group parentheses removed, the underscore of _folders removed)
 * The key is the first 8 characters of the source text's sha1. The same phrase gets the same key even if the order or
 * file changes, so re-run diffs stay small (which helps when comparing against upstream updates).
 *
 * Run:  npm run extract:i18n
 */

import * as ts from "typescript";

import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";

const SRC_DIR = path.resolve(__dirname, "..");
const MESSAGES_DIR = path.resolve(__dirname, "../../messages");
const REPO_WEB_DIR = path.resolve(__dirname, "../..");

/** Whether the text contains at least one Han character. The sole criterion for deciding what UI text to translate. */
const HAN = /\p{Script=Han}/u;

/** Directories excluded from extraction (the parsing tool itself and build output). */
const SKIP_DIRS = new Set(["scripts", "node_modules", ".next"]);

type Kind = "string" | "template" | "jsx";

interface Message {
  ns: string;
  key: string;
  text: string;
  kind: Kind;
  placeholders: string[]; // the template's original ${...} expressions (for reference)
  occurrences: string[]; // "relative/path:line"
}

/** Path relative to src → dot-joined namespace. */
function toNamespace(absFile: string): string {
  const rel = path.relative(SRC_DIR, absFile).replace(/\.[tj]sx?$/, "");
  return rel
    .split(path.sep)
    .map((seg) =>
      seg
        .replace(/^\((.*)\)$/, "$1") // (main) → main  (route group)
        .replace(/^_/, "") // _components → components
        .replace(/[^A-Za-z0-9가-힣]+/g, "-")
        .replace(/^-+|-+$/g, ""),
    )
    .filter(Boolean)
    .join(".");
}

function hashKey(text: string): string {
  return crypto.createHash("sha1").update(text).digest("hex").slice(0, 8);
}

/** Collects .ts/.tsx files by recursively walking directories. */
function collectFiles(dir: string, out: string[] = []): string[] {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    if (entry.isDirectory()) {
      if (SKIP_DIRS.has(entry.name)) continue;
      collectFiles(path.join(dir, entry.name), out);
    } else if (/\.(ts|tsx)$/.test(entry.name)) {
      out.push(path.join(dir, entry.name));
    }
  }
  return out;
}

/** Rebuilds a template literal as "literal parts + {varN}". Returns null = no Han characters in the literal parts. */
function reconstructTemplate(node: ts.TemplateExpression): { text: string; placeholders: string[] } | null {
  let text = node.head.text;
  const placeholders: string[] = [];
  node.templateSpans.forEach((span, i) => {
    placeholders.push(span.expression.getText());
    text += `{var${i}}${span.literal.text}`;
  });
  // If Han characters appear only inside ${} and not in the fixed text, they are captured separately as inner nodes.
  if (!HAN.test(node.head.text) && !node.templateSpans.some((s) => HAN.test(s.literal.text))) {
    return null;
  }
  return { text, placeholders };
}

/** Normalizes JSX text: trims both ends + collapses internal runs of whitespace/newlines into one space (same as browser rendering rules). */
function normalizeJsxText(raw: string): string {
  return raw.replace(/\s+/g, " ").trim();
}

const messages = new Map<string, Message>(); // "ns\u0000key" → Message
const scannedFiles = collectFiles(SRC_DIR).sort();
const capturedLines = new Map<string, Set<number>>(); // file → set of line numbers hit by extracted nodes
let fileWithHan = 0;

function record(ns: string, text: string, kind: Kind, placeholders: string[], relFile: string, line: number) {
  const key = hashKey(text);
  const id = `${ns}\u0000${key}`;
  let msg = messages.get(id);
  if (!msg) {
    msg = { ns, key, text, kind, placeholders, occurrences: [] };
    messages.set(id, msg);
  }
  const occ = `${relFile}:${line}`;
  if (!msg.occurrences.includes(occ)) msg.occurrences.push(occ);
}

for (const absFile of scannedFiles) {
  const source = fs.readFileSync(absFile, "utf8");
  if (!HAN.test(source)) continue;
  fileWithHan++;

  const relFile = path.relative(REPO_WEB_DIR, absFile);
  const ns = toNamespace(absFile);
  const scriptKind = absFile.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS;
  const sf = ts.createSourceFile(absFile, source, ts.ScriptTarget.Latest, true, scriptKind);
  const lineOf = (pos: number) => sf.getLineAndCharacterOfPosition(pos).line + 1;
  const markLine = (pos: number) => {
    const set = capturedLines.get(relFile) ?? new Set<number>();
    set.add(lineOf(pos));
    capturedLines.set(relFile, set);
  };

  const visit = (node: ts.Node): void => {
    if (ts.isStringLiteralLike(node) && !ts.isTemplateExpression(node.parent)) {
      // String literals + templates without substitutions. (The head/middle of templates with substitutions don't match here.)
      if (HAN.test(node.text)) {
        record(ns, node.text, "string", [], relFile, lineOf(node.getStart(sf)));
        markLine(node.getStart(sf));
      }
    } else if (ts.isTemplateExpression(node)) {
      const r = reconstructTemplate(node);
      if (r) {
        record(ns, r.text, "template", r.placeholders, relFile, lineOf(node.getStart(sf)));
        markLine(node.getStart(sf));
      }
    } else if (ts.isJsxText(node)) {
      const text = normalizeJsxText(node.text);
      if (text && HAN.test(text)) {
        record(ns, text, "jsx", [], relFile, lineOf(node.getStart(sf)));
        markLine(node.getStart(sf));
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
}

// --- Build nested JSON ---------------------------------------------------------
type Tree = { [k: string]: Tree | string };

function setNested(root: Tree, nsPath: string, key: string, value: string) {
  const segs = nsPath.split(".");
  let node: Tree = root;
  for (const seg of segs) {
    if (typeof node[seg] !== "object") node[seg] = {};
    node = node[seg] as Tree;
  }
  node[key] = value;
}

/** Sorts keys recursively to minimize re-run diffs. */
function sortTree(t: Tree): Tree {
  const out: Tree = {};
  for (const k of Object.keys(t).sort()) {
    const v = t[k];
    out[k] = typeof v === "string" ? v : sortTree(v);
  }
  return out;
}

const zhTree: Tree = {};
const koTree: Tree = {};
const sources: Record<string, { text: string; kind: Kind; placeholders: string[]; occurrences: string[] }> = {};

for (const msg of messages.values()) {
  setNested(zhTree, msg.ns, msg.key, msg.text);
  setNested(koTree, msg.ns, msg.key, "");
  sources[`${msg.ns}.${msg.key}`] = {
    text: msg.text,
    kind: msg.kind,
    placeholders: msg.placeholders,
    occurrences: msg.occurrences.sort(),
  };
}

const sortedSources: typeof sources = {};
for (const k of Object.keys(sources).sort()) sortedSources[k] = sources[k];

fs.mkdirSync(MESSAGES_DIR, { recursive: true });
const write = (name: string, data: unknown) =>
  fs.writeFileSync(path.join(MESSAGES_DIR, name), `${JSON.stringify(data, null, 2)}\n`, "utf8");

/** Reads an existing message file. Returns an empty tree if it is missing or corrupt. */
function readTree(name: string): Tree {
  try {
    return JSON.parse(fs.readFileSync(path.join(MESSAGES_DIR, name), "utf8")) as Tree;
  } catch {
    return {};
  }
}

/**
 * Non-destructive merge. The top-level namespaces of the freshly extracted tree (fresh) use the code as the source of truth,
 * so they are taken as-is; top-level namespaces present only in the existing file (existing), i.e. hand-translated curated
 * namespaces, are preserved. Stale keys inside extracted namespaces (ones that disappeared upstream) naturally
 * drop out (which helps when comparing against upstream).
 */
function mergeCurated(fresh: Tree, existing: Tree): Tree {
  const extractedTop = new Set(Object.keys(fresh));
  const out: Tree = { ...fresh };
  for (const [ns, subtree] of Object.entries(existing)) {
    if (!extractedTop.has(ns)) out[ns] = subtree;
  }
  return out;
}

write("zh.json", sortTree(mergeCurated(zhTree, readTree("zh.json"))));
write("ko.json", sortTree(mergeCurated(koTree, readTree("ko.json"))));
write("zh.sources.json", sortedSources);

// --- Summary report ------------------------------------------------------------
const byKind: Record<Kind, number> = { string: 0, template: 0, jsx: 0 };
const byNs = new Map<string, number>();
let totalOccurrences = 0;
for (const msg of messages.values()) {
  byKind[msg.kind]++;
  byNs.set(msg.ns, (byNs.get(msg.ns) ?? 0) + 1);
  totalOccurrences += msg.occurrences.length;
}

// Coverage estimate: the share of source lines containing Han characters that were hit by an extracted node.
// The rest are almost all comments (= not translation targets).
let hanLines = 0;
let capturedHanLines = 0;
for (const absFile of scannedFiles) {
  const relFile = path.relative(REPO_WEB_DIR, absFile);
  const lines = fs.readFileSync(absFile, "utf8").split(/\r?\n/);
  const capSet = capturedLines.get(relFile) ?? new Set<number>();
  lines.forEach((ln, i) => {
    if (HAN.test(ln)) {
      hanLines++;
      if (capSet.has(i + 1)) capturedHanLines++;
    }
  });
}

const topNs = [...byNs.entries()].sort((a, b) => b[1] - a[1]).slice(0, 15);

console.log("─".repeat(64));
console.log("ARTEX i18n 추출 완료");
console.log("─".repeat(64));
console.log(`스캔한 파일            : ${scannedFiles.length} (src/ 아래 .ts/.tsx, scripts 제외)`);
console.log(`한자 포함 파일         : ${fileWithHan}`);
console.log(`고유 메시지(키)        : ${messages.size}`);
console.log(`  ├─ 문자열 리터럴      : ${byKind.string}`);
console.log(`  ├─ 템플릿 리터럴      : ${byKind.template}`);
console.log(`  └─ JSX 텍스트         : ${byKind.jsx}`);
console.log(`총 출현 위치           : ${totalOccurrences} (중복 사용 포함)`);
console.log(`네임스페이스 수        : ${byNs.size}`);
console.log("");
console.log(`한자 포함 소스 줄       : ${hanLines}`);
console.log(`  └─ 추출로 포착된 줄   : ${capturedHanLines} (${((capturedHanLines / hanLines) * 100).toFixed(1)}%)`);
console.log(`     나머지 ${hanLines - capturedHanLines} 줄은 대부분 코드 주석(번역 비대상)`);
console.log("");
console.log("메시지 많은 네임스페이스 상위 15:");
for (const [ns, n] of topNs) console.log(`  ${String(n).padStart(4)}  ${ns}`);
console.log("");
console.log(`출력: web/messages/{zh.json, ko.json, zh.sources.json}`);
console.log("─".repeat(64));
