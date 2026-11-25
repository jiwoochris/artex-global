"use client";

import * as React from "react";

import { setClientCookie } from "@/lib/cookie.client";
import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";

import { isLocale, LOCALE_COOKIE, LOCALE_STORAGE_KEY, type Locale, resolveLocale } from "./config";

// The display language the user picks is a purely frontend preference, so it is not stored on the server, only in
// localStorage (and a cookie). It is not synced with the account, so switching browsers or clearing site data
// reverts to the default language. The agent output language is stored separately in the server settings.

function parseLocale(raw: string | null): Locale {
  return isLocale(raw) ? raw : resolveLocale();
}

// Set of subscribers within the same tab. The storage event fires only in "other" tabs, so after changing the
// language in the current tab, emit must notify same-tab subscribers for them to re-render immediately.
const listeners = new Set<() => void>();

function subscribe(listener: () => void) {
  listeners.add(listener);
  window.addEventListener("storage", listener);
  return () => {
    listeners.delete(listener);
    window.removeEventListener("storage", listener);
  };
}

function emit() {
  for (const listener of listeners) listener();
}

function getSnapshot(): Locale {
  return parseLocale(getLocalStorageValue(LOCALE_STORAGE_KEY));
}

// The server has no localStorage, so it first renders the build's default locale, and after hydration
// getSnapshot corrects it to the value the user picked.
function getServerSnapshot(): Locale {
  return resolveLocale();
}

// Hook that subscribes to the active display language. When the language changes, components using this hook re-render.
export function useActiveLocale(): Locale {
  return React.useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}

// Changes the display language. Writes it to the cookie and localStorage and notifies same-tab subscribers. It persists
// for one year so the choice survives the next visit.
export function setActiveLocale(locale: Locale) {
  if (!isLocale(locale)) return;
  setLocalStorageValue(LOCALE_STORAGE_KEY, locale);
  setClientCookie(LOCALE_COOKIE, locale, 365);
  emit();
}
