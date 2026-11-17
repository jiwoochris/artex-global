import { describe, expect, it } from "vitest";

import { DEFAULT_LOCALE, isLocale, LOCALE_LABELS, LOCALES, resolveLocale } from "./config";

describe("i18n config", () => {
  it("exposes the supported locales with native labels", () => {
    expect([...LOCALES]).toEqual(["en", "ko", "zh", "es", "ja"]);
    expect(Object.keys(LOCALE_LABELS).sort()).toEqual([...LOCALES].sort());
    expect(LOCALE_LABELS.ko).toBe("한국어");
    expect(LOCALE_LABELS.ja).toBe("日本語");
  });

  it("isLocale narrows known locale strings only", () => {
    expect(isLocale("ko")).toBe(true);
    expect(isLocale("es")).toBe(true);
    expect(isLocale("ja")).toBe(true);
    expect(isLocale("fr")).toBe(false);
    expect(isLocale("")).toBe(false);
    expect(isLocale(null)).toBe(false);
    expect(isLocale(undefined)).toBe(false);
    expect(isLocale(1)).toBe(false);
  });

  it("resolveLocale falls back to the default unless NEXT_PUBLIC_LOCALE is supported", () => {
    const original = process.env.NEXT_PUBLIC_LOCALE;
    try {
      process.env.NEXT_PUBLIC_LOCALE = "es";
      expect(resolveLocale()).toBe("es");

      process.env.NEXT_PUBLIC_LOCALE = "fr";
      expect(resolveLocale()).toBe(DEFAULT_LOCALE);

      delete process.env.NEXT_PUBLIC_LOCALE;
      expect(resolveLocale()).toBe(DEFAULT_LOCALE);
    } finally {
      if (original === undefined) delete process.env.NEXT_PUBLIC_LOCALE;
      else process.env.NEXT_PUBLIC_LOCALE = original;
    }
  });
});
