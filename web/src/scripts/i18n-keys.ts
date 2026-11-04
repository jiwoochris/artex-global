/**
 * i18n locale key-parity helper.
 *
 * Compares the flattened key sets of two locales against the `en` baseline and reports both
 * missing and extra keys. The CLI (check-i18n-keys.ts) and the Vitest test share this pure
 * logic, so the comparison rule lives in one place.
 */

export interface KeyDiff {
  missing: string[]; // in the baseline but not in the target
  extra: string[]; // in the target but not in the baseline
}

/** Flattens a nested JSON object into "a.b.c" keys. Arrays and primitives count as leaves. */
export function flattenKeys(tree: unknown, prefix = ""): Set<string> {
  const out = new Set<string>();
  if (tree === null || typeof tree !== "object" || Array.isArray(tree)) {
    if (prefix) out.add(prefix);
    return out;
  }
  for (const [key, value] of Object.entries(tree as Record<string, unknown>)) {
    const path = prefix ? `${prefix}.${key}` : key;
    for (const leaf of flattenKeys(value, path)) out.add(leaf);
  }
  return out;
}

/** Returns the missing/extra keys of target relative to baseline, sorted. */
export function diffKeys(baseline: Set<string>, target: Set<string>): KeyDiff {
  const missing = [...baseline].filter((k) => !target.has(k)).sort();
  const extra = [...target].filter((k) => !baseline.has(k)).sort();
  return { missing, extra };
}

/** True when there is neither a missing nor an extra key. */
export function isInSync(diff: KeyDiff): boolean {
  return diff.missing.length === 0 && diff.extra.length === 0;
}
