// Supported UI locales and the default. ARTEX ships English (baseline) and Spanish;
// users pick one of these two in the settings screen.
export const LOCALES = ["en", "es"] as const;
export type Locale = (typeof LOCALES)[number];

// Default display language. `en` is the baseline: every message key must exist there first.
// NEXT_PUBLIC_LOCALE can override it at build time, and at runtime the value the user picked
// (cookie + localStorage) wins.
export const DEFAULT_LOCALE: Locale = "en";

// Native name of each locale, shown in the language picker. These are deliberately not
// translated: a language name must read the same whatever language the UI is in.
export const LOCALE_LABELS: Record<Locale, string> = {
  en: "English",
  es: "Español",
};

// Storage keys for the user's display language. The client reads localStorage; the cookie
// carries the same value and has no reader today (kept for when the server must know the
// locale).
export const LOCALE_COOKIE = "artex_locale";
export const LOCALE_STORAGE_KEY = "artex_locale";

// Narrows an arbitrary value to a supported locale.
export function isLocale(raw: unknown): raw is Locale {
  return typeof raw === "string" && (LOCALES as readonly string[]).includes(raw);
}

// Resolves the active locale at build time. Must stay compatible with the static export
// (output: "export" in next.config), so it reads only the environment variable and never
// dynamic APIs such as cookies()/headers(). An empty or unsupported NEXT_PUBLIC_LOCALE falls
// back to the default. The value only shapes the server-prerendered initial HTML; the real
// display language is overridden on the client from the cookie/localStorage.
export function resolveLocale(): Locale {
  const raw = process.env.NEXT_PUBLIC_LOCALE;
  return isLocale(raw) ? raw : DEFAULT_LOCALE;
}
