"use client";

import type { ReactNode } from "react";
import * as React from "react";

import { NextIntlClientProvider } from "next-intl";

import enMessages from "../../messages/en.json";
import esMessages from "../../messages/es.json";
import type { Locale } from "./config";
import { useActiveLocale } from "./locale-store";

// Both locales are bundled statically so switching the language in the settings screen
// re-renders every string on the spot — no rebuild, no page reload, no async load (which
// also keeps it compatible with the static export, output: "export").
const MESSAGES: Record<Locale, Record<string, unknown>> = {
  en: enMessages as Record<string, unknown>,
  es: esMessages as Record<string, unknown>,
};

// Time zone of the runtime environment (browser or prerender server).
const TIME_ZONE = Intl.DateTimeFormat().resolvedOptions().timeZone;

// Provider that owns runtime language switching. It replaces the root layout's
// NextIntlClientProvider and supplies the messages for the locale the user picked
// (cookie + localStorage).
export function IntlRuntimeProvider({ children }: Readonly<{ children: ReactNode }>) {
  const locale = useActiveLocale();

  // Keep <html lang> in sync with the language. The root layout seeds it on the server from
  // the build locale; overriding it with the client choice makes screen readers and browser
  // translation pick the right language.
  React.useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  // Unlike the root layout's provider this one does not inherit the server request config, so
  // timeZone must be passed explicitly. Without it next-intl logs ENVIRONMENT_FALLBACK during
  // prerender.
  return (
    <NextIntlClientProvider locale={locale} messages={MESSAGES[locale]} timeZone={TIME_ZONE}>
      {children}
    </NextIntlClientProvider>
  );
}
