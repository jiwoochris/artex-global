import { DEFAULT_LOCALE, type Locale, resolveLocale } from "@/i18n/config";

import packageJson from "../../package.json";

const currentYear = new Date().getFullYear();

// Browser tab title and search-engine description. layout.tsx exports these as static
// metadata, so they render into every page's <title> and <meta name="description"> and follow
// the build locale (en or es). Titles use a colon ("Product: description") rather than an em
// dash. Being build-time constants, they do not react to runtime language switching — a
// one-line tab title is not worth more machinery.
const META_BY_LOCALE: Record<Locale, { title: string; description: string }> = {
  en: {
    title: "ARTEX: Autonomous Penetration Testing Console",
    description: "An LLM-driven system console for autonomous penetration testing",
  },
  es: {
    title: "ARTEX: Consola de Pruebas de Penetración Autónomas",
    description: "Una consola de sistema impulsada por LLM para pruebas de penetración autónomas",
  },
};

export const APP_CONFIG = {
  name: "ARTEX",
  version: packageJson.version,
  copyright: `© ${currentYear}, ARTEX.`,
  meta: META_BY_LOCALE[resolveLocale()] ?? META_BY_LOCALE[DEFAULT_LOCALE],
};
