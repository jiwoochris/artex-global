import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

// firstNonEmpty picks the first value that is not empty, so UI fallbacks still apply when the server sends ""
// instead of omitting the field (plain ?? would render the empty string).
export function firstNonEmpty(...values: (string | null | undefined)[]): string {
  return values.find((value) => value) ?? "";
}

// Close-decision helper for onInteractOutside on drawers/dialogs (Sheet/Dialog).
//
// Background: Radix overlays inside a drawer (Select dropdown, DropdownMenu, Popover, etc.) portal outside
// the drawer. While an overlay is open, clicking the mask/outside the drawer to dismiss it means this pointerdown
// is handled by both the Select and Sheet DismissableLayer; Select closes first and, being a discrete event, React flushes
// synchronously, so by the time the Sheet handler runs the overlay's data-state has already flipped to closed — checking
// whether an overlay is open "at this moment" is inherently unreliable (verified by measurement).
//
// Correct approach: Radix listens for pointerdown in the bubble phase; in the capture phase (earlier than that) we first
// record "is any overlay open right now", and onInteractOutside reads that recorded value to decide whether to allow the close.
function isRadixOverlayOpenNow(): boolean {
  if (typeof document === "undefined") return false;
  return !!document.querySelector(
    [
      "[data-slot='select-trigger'][data-state='open']",
      "[data-slot='select-content'][data-state='open']",
      "[role='listbox'][data-state='open']",
      "[data-radix-popper-content-wrapper]",
      "[aria-expanded='true'][data-state='open']",
    ].join(","),
  );
}

let overlayOpenAtLastPointerDown = false;
if (typeof document !== "undefined") {
  document.addEventListener(
    "pointerdown",
    () => {
      overlayOpenAtLastPointerDown = isRadixOverlayOpenNow();
    },
    true, // capture: record before the Radix bubble-phase pointerdown handler runs
  );
}

// radixOverlayWasOpenAtPointerDown returns "whether a Radix overlay was open when the most recent pointerdown
// occurred". Drawers/dialogs use it accordingly: with an overlay open, clicking the mask → dismiss only the overlay, not the drawer itself.
export function radixOverlayWasOpenAtPointerDown(): boolean {
  return overlayOpenAtLastPointerDown;
}

// copyText writes text to the clipboard and returns whether it succeeded.
// Background: navigator.clipboard is only available in secure contexts (HTTPS / localhost); when accessed
// over an IP + HTTP it is undefined, so fall back to execCommand("copy").
export async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // Fall through to the fallback
    }
  }
  try {
    const textarea = document.createElement("textarea");
    textarea.value = text;
    textarea.style.position = "fixed";
    textarea.style.left = "-9999px";
    textarea.style.top = "0";
    document.body.appendChild(textarea);
    textarea.focus();
    textarea.select();
    const ok = document.execCommand("copy");
    document.body.removeChild(textarea);
    return ok;
  } catch {
    return false;
  }
}

export const getInitials = (str: string): string => {
  if (typeof str !== "string" || !str.trim()) return "?";

  return (
    str
      .trim()
      .split(/\s+/)
      .filter(Boolean)
      .map((word) => word[0])
      .join("")
      .toUpperCase() || "?"
  );
};

export function formatCurrency(
  amount: number,
  opts?: {
    currency?: string;
    locale?: string;
    minimumFractionDigits?: number;
    maximumFractionDigits?: number;
    noDecimals?: boolean;
  },
) {
  const { currency = "USD", locale = "en-US", minimumFractionDigits, maximumFractionDigits, noDecimals } = opts ?? {};

  const formatOptions: Intl.NumberFormatOptions = {
    style: "currency",
    currency,
    minimumFractionDigits: noDecimals ? 0 : minimumFractionDigits,
    maximumFractionDigits: noDecimals ? 0 : maximumFractionDigits,
  };

  return new Intl.NumberFormat(locale, formatOptions).format(amount);
}
