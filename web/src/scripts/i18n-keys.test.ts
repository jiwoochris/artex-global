import { describe, expect, it } from "vitest";

import { diffKeys, flattenKeys, isInSync } from "./i18n-keys";
import { readFileSync } from "node:fs";
import path from "node:path";

const MESSAGES_DIR = path.resolve(process.cwd(), "messages");
const LOCALES = ["ko", "en", "zh", "es", "ja"] as const;

function readKeys(locale: string): Set<string> {
  const tree = JSON.parse(readFileSync(path.join(MESSAGES_DIR, `${locale}.json`), "utf8")) as unknown;
  return flattenKeys(tree);
}

describe("i18n key parity guard", () => {
  it("treats all five locale key sets as identical to the ko baseline", () => {
    const baseline = readKeys("ko");
    expect(baseline.size).toBeGreaterThan(0);
    for (const locale of LOCALES) {
      const diff = diffKeys(baseline, readKeys(locale));
      expect(isInSync(diff), `${locale}.json differs: ${JSON.stringify(diff).slice(0, 400)}`).toBe(true);
    }
  });

  it("detects missing and extra keys with the same helper the CLI uses", () => {
    const baseline = new Set(["a", "a.b", "a.c"]);
    const target = new Set(["a", "a.b", "a.d"]);
    const diff = diffKeys(baseline, target);
    expect(diff.missing).toEqual(["a.c"]);
    expect(diff.extra).toEqual(["a.d"]);
    expect(isInSync(diff)).toBe(false);
  });

  it("flattens nested trees and keeps array and primitive leaves", () => {
    expect(flattenKeys({ a: { b: "x" }, c: ["y"] })).toEqual(new Set(["a.b", "c"]));
  });
});
