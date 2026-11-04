import { getRequestConfig } from "next-intl/server";

import { type Locale, resolveLocale } from "./config";

// Per-locale message loader. An explicit import map keeps the bundler from pulling the whole
// messages/ directory in as a context module.
const loaders: Record<Locale, () => Promise<{ default: Record<string, unknown> }>> = {
  en: () => import("../../messages/en.json"),
  es: () => import("../../messages/es.json"),
};

// next-intl request config. This app does not use next-intl path routing (no locale segment,
// no middleware), so the locale is decided here. requestLocale is not consulted, which keeps
// the render static. This config only sets the language of server components/prerender;
// runtime switching is handled by the client-side IntlRuntimeProvider (messages/en·es).
export default getRequestConfig(async () => {
  const locale = resolveLocale();
  const messages = (await loaders[locale]()).default;
  return { locale, messages };
});
