// Supported locales and the default. artex-global defaults to English and adds Korean, Chinese, Spanish and Japanese,
// so users can switch among five languages in their settings. Chinese ("zh") is also kept for comparison with the upstream original.
export const LOCALES = ["en", "ko", "zh", "es", "ja"] as const;
export type Locale = (typeof LOCALES)[number];

// The default display language is English. Korean, Chinese, Spanish and Japanese are switched to via settings. At build time
// it can be overridden with NEXT_PUBLIC_LOCALE, and at runtime the value the user picked on the settings screen (cookie/localStorage) has the final say.
export const DEFAULT_LOCALE: Locale = "en";

// Each locale's native-language name shown in the language picker. Whatever language you are viewing in, a language's
// own name should read in that language, so these are fixed labels rather than translatable text.
export const LOCALE_LABELS: Record<Locale, string> = {
  en: "English",
  ko: "한국어",
  zh: "中文",
  es: "Español",
  ja: "日本語",
};

// Key under which the user's chosen display language is stored. The UI reads the localStorage value. The same value
// is also written to a cookie, but nothing reads it yet (it is kept for when the server needs to determine the language).
export const LOCALE_COOKIE = "artex_locale";
export const LOCALE_STORAGE_KEY = "artex_locale";

// Guard that narrows an arbitrary string to a supported locale.
export function isLocale(raw: unknown): raw is Locale {
  return typeof raw === "string" && (LOCALES as readonly string[]).includes(raw);
}

// Determines the active locale at build time. It must be compatible with the static export (output: "export" in
// next.config), so it reads only environment variables and does not use dynamic APIs such as cookies() or headers().
// If NEXT_PUBLIC_LOCALE is empty or not in the supported list, it falls back to the default. This value is used only in
// the initial HTML of the server prerender; the actual display language is overridden on the client from the cookie.
export function resolveLocale(): Locale {
  const raw = process.env.NEXT_PUBLIC_LOCALE;
  return isLocale(raw) ? raw : DEFAULT_LOCALE;
}
