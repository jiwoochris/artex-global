/**
 * i18n 로케일 키 정합성 헬퍼.
 *
 * ko.json 을 기준(baseline)으로 각 로케일의 "평탄화된 키 집합"을 비교해 누락(missing)과
 * 추가(extra)를 모두 찾아낸다. CLI(check-i18n-keys.ts)와 Vitest 테스트가 이 순수 로직을
 * 공유하므로 검사 규칙이 한 곳에서만 정의된다.
 */

export interface KeyDiff {
  missing: string[]; // baseline 에는 있고 target 에는 없는 키
  extra: string[]; // baseline 에는 없고 target 에는 있는 키
}

/** 중첩 JSON 객체를 "a.b.c" 형태의 키 집합으로 평탄화한다. 배열·원시값은 리프로 취급한다. */
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

/** baseline 대비 target 의 누락·추가 키를 정렬된 배열로 돌려준다. */
export function diffKeys(baseline: Set<string>, target: Set<string>): KeyDiff {
  const missing = [...baseline].filter((k) => !target.has(k)).sort();
  const extra = [...target].filter((k) => !baseline.has(k)).sort();
  return { missing, extra };
}

/** 누락·추가가 모두 비어 있으면 true. */
export function isInSync(diff: KeyDiff): boolean {
  return diff.missing.length === 0 && diff.extra.length === 0;
}
