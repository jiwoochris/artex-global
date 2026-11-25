"use client";

import * as React from "react";

import Link from "next/link";

import { ArrowUpCircleIcon } from "lucide-react";
import { useTranslations } from "next-intl";

import { api } from "@/lib/api";

/**
 * "New version available" hint in the top bar: checks once per full page load and, if there is an update, lights up next to the version number;
 * clicking it goes straight to the "Version & updates" card on the system settings page.
 *
 * The backend caches the GitHub query result for 30 minutes, so checking on every mount here is safe
 * — the unauthenticated GitHub API allows only 60 requests/hour/IP; without that cache, opening a few tabs
 * would burn through the quota, and then an actual update check would no longer work.
 *
 * Query failures are always silent: the top bar is not the place for errors; the user sees the reason by clicking "Check for updates" on the settings page.
 */
export function UpdateBadge() {
  const t = useTranslations("header");
  const [latest, setLatest] = React.useState("");

  React.useEffect(() => {
    let alive = true;
    api
      .checkUpdate()
      .then((r) => {
        // has_update already includes the "versions are comparable" check, so development builds never show this hint.
        if (alive && r.has_update && r.latest) setLatest(r.latest.replace(/^v(?=\d)/, ""));
      })
      .catch(() => {
        // Silent: no network / GitHub rate limiting should never pop an error in the top bar.
      });
    return () => {
      alive = false;
    };
  }, []);

  if (!latest) return null;

  return (
    <Link
      href="/system/settings"
      title={t("updateTitle", { latest })}
      className="inline-flex items-center gap-1.5 rounded-full bg-primary px-2.5 py-1 font-medium text-primary-foreground text-xs transition-opacity hover:opacity-90"
    >
      {/* Breathing dot: the top bar has many elements and plain text is easy to miss; the animation makes it visible at a glance. */}
      <span className="relative flex size-1.5">
        <span className="absolute inline-flex size-full animate-ping rounded-full bg-primary-foreground opacity-75" />
        <span className="relative inline-flex size-1.5 rounded-full bg-primary-foreground" />
      </span>
      <ArrowUpCircleIcon className="size-3.5" />
      <span className="hidden sm:inline">{t("updateBadge", { latest })}</span>
      <span className="sm:hidden">{t("updateBadgeShort")}</span>
    </Link>
  );
}
