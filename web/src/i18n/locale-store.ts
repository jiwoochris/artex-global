"use client";

import * as React from "react";

import { setClientCookie } from "@/lib/cookie.client";
import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";

import { isLocale, LOCALE_COOKIE, LOCALE_STORAGE_KEY, type Locale, resolveLocale } from "./config";

// The user's display language is a pure frontend preference: it is never sent to the server,
// only kept in localStorage (plus the cookie). It is not synced to the account, so switching
// browser or clearing site data falls back to the default language. The agent output language
// is a separate server-side setting.

function parseLocale(raw: string | null): Locale {
  return isLocale(raw) ? raw : resolveLocale();
}

// Subscribers within the same tab. The storage event only fires in *other* tabs, so a change
// made here must be announced through emit() for this tab to re-render immediately.
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

// There is no localStorage on the server, so render the build default first; after hydration
// getSnapshot corrects it to the user's choice.
function getServerSnapshot(): Locale {
  return resolveLocale();
}

// Subscribes to the active display language. Components using this hook re-render when the
// language changes.
export function useActiveLocale(): Locale {
  return React.useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}

// Switches the display language: writes cookie + localStorage and notifies same-tab
// subscribers. Kept for a year so the choice survives the next visit.
export function setActiveLocale(locale: Locale) {
  if (!isLocale(locale)) return;
  setLocalStorageValue(LOCALE_STORAGE_KEY, locale);
  setClientCookie(LOCALE_COOKIE, locale, 365);
  emit();
}
