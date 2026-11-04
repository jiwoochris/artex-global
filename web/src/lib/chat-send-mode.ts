"use client";

import * as React from "react";

import { useTranslations } from "next-intl";

import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";

// Send/newline key combination for the session composer. A pure frontend preference, stored
// in localStorage only: never sent to the server, never synced to the account, so switching
// browser means setting it again. See issue #39: 0.3.2 changed Ctrl+Enter send into Enter
// send, and the old combination is offered here as an option.
export type ChatSendMode = "enter" | "ctrl-enter";

export const CHAT_SEND_MODE_KEY = "artex_chat_send_mode";
export const DEFAULT_CHAT_SEND_MODE: ChatSendMode = "enter";

// Options for the send-mode dropdown in the settings screen. Labels depend on the locale, so
// they come from a hook instead of a module constant (a hardcoded label in a pure module would
// break every other language). `value` is a stable identifier used for the localStorage key and
// for behavior checks.
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

// Subscribers within the same tab. The storage event only fires in *other* tabs, so after a
// change here emit() must tell the composer in this tab; otherwise it takes a reload to apply.
const listeners = new Set<() => void>();

function subscribe(listener: () => void) {
  listeners.add(listener);
  window.addEventListener("storage", listener);
  return () => {
    listeners.delete(listener);
    window.removeEventListener("storage", listener);
  };
}

// Returns a string literal, so Object.is compares by value and useSyncExternalStore does not
// loop forever.
function getSnapshot(): ChatSendMode {
  return parseMode(getLocalStorageValue(CHAT_SEND_MODE_KEY));
}

// There is no localStorage on the server, so render the default first; after hydration
// getSnapshot corrects it.
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

// shouldSubmitOnKey decides whether a single key press means "send".
// isComposing / keyCode 229 mean an IME is composing characters (any script), so those presses
// must pass through; otherwise confirming with Enter would send the message prematurely.
// enter mode excludes only Shift, keeping 0.3.2 behavior identical (nobody who leaves the
// setting alone notices a difference). ctrl-enter mode accepts Ctrl and Cmd (macOS).
export function shouldSubmitOnKey(e: React.KeyboardEvent, mode: ChatSendMode): boolean {
  if (e.key !== "Enter") return false;
  if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) return false;
  if (mode === "ctrl-enter") return e.ctrlKey || e.metaKey;
  return !e.shiftKey;
}
