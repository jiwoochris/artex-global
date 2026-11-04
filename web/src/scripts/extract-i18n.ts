/**
 * Script: extract-i18n.ts
 *
 * Source-level CJK audit for the English/Spanish UI. The app ships exactly two locales
 * (`en`, `es`), so nothing user-visible may be hardcoded in Chinese, Korean or Japanese.
 *
 * Parses every `.ts`/`.tsx` under `src/` with the TypeScript AST and reports CJK text found in
 *   1) string literals            e.g. title: "dashboard"
 *   2) template literals          e.g. `deleted ${n} conversations`
 *   3) JSX text nodes             e.g. <span>failover</span>
 * Comments are not AST nodes, so they never show up here.
 *
 * The report is informational (exit code 0): a few CJK literals are legitimate — backend wire
 * format tokens (chat mention labels, `[模型]` judge sentinels) and DB-seeded Chinese agent
 * prompts that are mirrored verbatim. Everything else must live in `web/messages/{en,es}.json`.
 *
 * Run: npm run extract:i18n
 */

import * as ts from "typescript";

import fs from "node:fs";
import path from "node:path";

const SRC_DIR = path.resolve(__dirname, "..");
const REPO_WEB_DIR = path.resolve(__dirname, "../..");

/** Any CJK character: Han, Hangul syllables, Hiragana/Katakana. */
const CJK = /[\p{Script=Han}\p{Script=Hangul}\p{Script=Hiragana}\p{Script=Katakana}]/u;

/** Directories to skip (tooling itself, build output). */
const SKIP_DIRS = new Set(["scripts", "node_modules", ".next"]);

type Kind = "string" | "template" | "jsx";

interface Hit {
  file: string; // path relative to web/
  line: number;
  kind: Kind;
  text: string;
}

/** Collects .ts/.tsx files recursively. */
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

/** JSX text normalization: trim ends and collapse inner whitespace (same as the browser). */
function normalizeJsxText(raw: string): string {
  return raw.replace(/\s+/g, " ").trim();
}

const hits: Hit[] = [];
const scannedFiles = collectFiles(SRC_DIR).sort();
const capturedLines = new Map<string, Set<number>>(); // file → lines holding an extracted node

for (const absFile of scannedFiles) {
  const source = fs.readFileSync(absFile, "utf8");
  if (!CJK.test(source)) continue;

  const file = path.relative(REPO_WEB_DIR, absFile);
  const scriptKind = absFile.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS;
  const sf = ts.createSourceFile(absFile, source, ts.ScriptTarget.Latest, true, scriptKind);
  const lineOf = (pos: number) => sf.getLineAndCharacterOfPosition(pos).line + 1;
  const record = (pos: number, kind: Kind, text: string) => {
    const line = lineOf(pos);
    hits.push({ file, line, kind, text });
    const set = capturedLines.get(file) ?? new Set<number>();
    set.add(line);
    capturedLines.set(file, set);
  };

  const visit = (node: ts.Node): void => {
    if (ts.isStringLiteralLike(node) && !ts.isTemplateExpression(node.parent)) {
      // String literals and templates without substitutions. The head/middle of a template
      // with substitutions is not reached here.
      if (CJK.test(node.text)) record(node.getStart(sf), "string", node.text);
    } else if (ts.isTemplateExpression(node)) {
      const text = [node.head.text, ...node.templateSpans.map((s) => s.literal.text)].join("${…}");
      if (CJK.test(text)) record(node.getStart(sf), "template", text);
    } else if (ts.isJsxText(node)) {
      const text = normalizeJsxText(node.text);
      if (text && CJK.test(text)) record(node.getStart(sf), "jsx", text);
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
}

// --- report -----------------------------------------------------------------
const byKind: Record<Kind, number> = { string: 0, template: 0, jsx: 0 };
const byFile = new Map<string, number>();
for (const hit of hits) {
  byKind[hit.kind]++;
  byFile.set(hit.file, (byFile.get(hit.file) ?? 0) + 1);
}

// CJK source lines that hold no extracted node are almost all comments.
let cjkLines = 0;
let remainingLines = 0;
for (const absFile of scannedFiles) {
  const file = path.relative(REPO_WEB_DIR, absFile);
  const captured = capturedLines.get(file) ?? new Set<number>();
  fs.readFileSync(absFile, "utf8")
    .split(/\r?\n/)
    .forEach((line, i) => {
      if (!CJK.test(line)) return;
      cjkLines++;
      if (!captured.has(i + 1)) remainingLines++;
    });
}

const topFiles = [...byFile.entries()].sort((a, b) => b[1] - a[1]).slice(0, 15);

console.log("─".repeat(64));
console.log("ARTEX i18n source audit (locales: en, es)");
console.log("─".repeat(64));
console.log(`files scanned        : ${scannedFiles.length} (.ts/.tsx under src/, scripts excluded)`);
console.log(`CJK literals found   : ${hits.length}`);
console.log(`  ├─ string literals  : ${byKind.string}`);
console.log(`  ├─ template literals: ${byKind.template}`);
console.log(`  └─ JSX text         : ${byKind.jsx}`);
console.log("");
console.log(`CJK source lines     : ${cjkLines}`);
console.log(`  └─ not in a literal : ${remainingLines} (almost all code comments)`);
console.log("");
console.log("Files with the most CJK literals:");
for (const [file, n] of topFiles) console.log(`  ${String(n).padStart(4)}  ${file}`);
console.log("");
console.log("Every entry above must be a wire-format token, a verbatim mirror of DB-seeded Chinese,");
console.log("or moved into web/messages/{en,es}.json.");
console.log("─".repeat(64));
