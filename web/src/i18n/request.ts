import { getRequestConfig } from "next-intl/server";

import { type Locale, resolveLocale } from "./config";

// Per-locale message loaders. They are an explicit import map so the bundler does not pull the whole messages/
// directory in as a context module (excluding zh.sources.json, which exists only locally).
const loaders: Record<Locale, () => Promise<{ default: Record<string, unknown> }>> = {
  en: () => import("../../messages/en.json"),
  ko: () => import("../../messages/ko.json"),
  zh: () => import("../../messages/zh.json"),
  es: () => import("../../messages/es.json"),
  ja: () => import("../../messages/ja.json"),
};

// next-intl request config. This setup does not use i18n path routing (segments/middleware), so the
// locale is decided directly here. requestLocale is not referenced, so it does not trigger
// dynamic rendering. This config only sets the default language for server components and prerendering; runtime
// user switching is handled by the client-side IntlRuntimeProvider (messages/en, ko, zh, es, ja).
export default getRequestConfig(async () => {
  const locale = resolveLocale();
  const messages = (await loaders[locale]()).default;
  return { locale, messages };
});
