import { DEFAULT_LOCALE, isLocale, LOCALE_STORAGE_KEY, type Locale, resolveLocale } from "@/i18n/config";
import { getLocalStorageValue } from "@/lib/local-storage.client";

// BCP47 locale tag used for date/time formatting. It follows the display language (locale-store), so use this
// function instead of a hard-coded "ko-KR". During server rendering (no window) it reflects the build's default locale,
// and on the client the value the user picked. It is also safe to use for Intl formatters that read it once at
// module load time.
const DATE_LOCALE: Record<Locale, string> = {
  en: "en-US",
  ko: "ko-KR",
  zh: "zh-CN",
  es: "es-ES",
  ja: "ja-JP",
};

export function getDateLocale(): string {
  if (typeof window === "undefined") return DATE_LOCALE[DEFAULT_LOCALE];
  const raw = getLocalStorageValue(LOCALE_STORAGE_KEY);
  const loc: Locale = isLocale(raw) ? raw : resolveLocale();
  return DATE_LOCALE[loc];
}
