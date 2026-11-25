"use client";

import * as React from "react";

import { useTranslations } from "next-intl";

import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";

// Send/newline key combination for the session input box. This is a purely front-end preference, so it is stored
// only in localStorage: not saved on the server and not synced with the account. Switching browsers therefore
// means setting it again. See issue #39: 0.3.2 changed Ctrl+Enter-to-send to Enter-to-send,
// and this brings back the old key combination as an option.
export type ChatSendMode = "enter" | "ctrl-enter";

export const CHAT_SEND_MODE_KEY = "artex_chat_send_mode";
export const DEFAULT_CHAT_SEND_MODE: ChatSendMode = "enter";

// Options for the send-mode dropdown on the settings screen. Labels depend on the locale, so they are returned
// from a hook rather than a module constant (hardcoding Korean in a pure module would break the zh locale
// into Korean). value is a stable identifier and is used as-is for the localStorage key and behavior checks.
export function useChatSendModeOptions(): { value: ChatSendMode; label: string }[] {
  const t = useTranslations("chatSendMode.option");
  return [
    { value: "enter", label: t("enter") },
    { value: "ctrl-enter", label: t("ctrlEnter") },
  ];
}

function parseMode(raw: string | null): ChatSendMode {
  return raw === "ctrl-enter" || raw === "enter" ? raw : DEFAULT_CHAT_SEND_MODE;
}

// Set of subscribers within the same tab. localStorage's storage event fires only in "other" tabs,
// so after changing the setting in the current tab we must emit to notify input boxes in the same tab. Otherwise
// the change only takes effect after a reload.
const listeners = new Set<() => void>();

function subscribe(listener: () => void) {
  listeners.add(listener);
  window.addEventListener("storage", listener);
  return () => {
    listeners.delete(listener);
    window.removeEventListener("storage", listener);
  };
}

// Returns a string literal, so Object.is compares by value and useSyncExternalStore does not fall into
// an infinite loop.
function getSnapshot(): ChatSendMode {
  return parseMode(getLocalStorageValue(CHAT_SEND_MODE_KEY));
}

// The server has no localStorage, so render the default first; after hydration getSnapshot
// corrects the value.
function getServerSnapshot(): ChatSendMode {
  return DEFAULT_CHAT_SEND_MODE;
}

export function useChatSendMode(): ChatSendMode {
  return React.useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}

export function setChatSendMode(mode: ChatSendMode) {
  setLocalStorageValue(CHAT_SEND_MODE_KEY, mode);
  for (const listener of listeners) listener();
}

// shouldSubmitOnKey decides whether a single key press counts as a send.
// isComposing / keyCode 229 means an IME (Korean, Chinese, etc.) is still composing characters,
// so those must always pass through. Otherwise pressing Enter to commit a character would send the message by mistake.
// enter mode excludes only Shift, keeping the behavior identical to 0.3.2 down to the last detail (users who
// did not change the setting keep the same feel). ctrl-enter mode accepts both Ctrl and Cmd (macOS)
// alike.
export function shouldSubmitOnKey(e: React.KeyboardEvent, mode: ChatSendMode): boolean {
  if (e.key !== "Enter") return false;
  if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) return false;
  if (mode === "ctrl-enter") return e.ctrlKey || e.metaKey;
  return !e.shiftKey;
}
