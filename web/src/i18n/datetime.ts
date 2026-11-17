import { DEFAULT_LOCALE, isLocale, LOCALE_STORAGE_KEY, type Locale, resolveLocale } from "@/i18n/config";
import { getLocalStorageValue } from "@/lib/local-storage.client";

// 날짜·시각 표기에 쓰는 BCP47 로케일 태그. 화면 표시 언어(locale-store)를 따라가므로
// 하드코딩된 "ko-KR" 대신 이 함수를 쓴다. 서버 렌더(window 없음)에서는 빌드 기본 locale 을,
// 클라이언트에서는 사용자가 고른 값을 반영한다. 모듈 로드 시점에 한 번 읽는 Intl 포매터에도
// 안전하게 쓸 수 있다.
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
