import { DEFAULT_LOCALE, isLocale, LOCALE_STORAGE_KEY, type Locale, resolveLocale } from "@/i18n/config";
import { getLocalStorageValue } from "@/lib/local-storage.client";

// BCP47 tag used for date/time formatting. It follows the display language (locale-store), so
// call this instead of hardcoding a tag. Server render (no window) uses the build locale; the
// client reflects the user's choice. Safe for Intl formatters created at module load.
const DATE_LOCALE: Record<Locale, string> = {
  en: "en-US",
  es: "es-ES",
};

export function getDateLocale(): string {
  if (typeof window === "undefined") return DATE_LOCALE[DEFAULT_LOCALE];
  const raw = getLocalStorageValue(LOCALE_STORAGE_KEY);
  const loc: Locale = isLocale(raw) ? raw : resolveLocale();
  return DATE_LOCALE[loc];
}
