import type { useTranslations } from "next-intl";

import { Card, CardContent } from "@/components/ui/card";

// This file is imported by client pages but is pure presentation/pure functions, so it never calls useTranslations.
// formatBacklog needs i18n unit strings, so callers inject `t` (same pattern as tasks/approval-records).
type Translator = ReturnType<typeof useTranslations>;

export function StatTile({ label, value, hint, tone }: { label: string; value: string; hint?: string; tone?: string }) {
  return (
    <Card size="sm" className="gap-1">
      <CardContent>
        <p className="text-muted-foreground text-xs">{label}</p>
        <p className={`text-lg font-semibold ${tone === "red" ? "text-rose-600" : ""}`}>{value}</p>
        {hint && <p className={`text-xs ${tone === "red" ? "text-rose-600" : "text-muted-foreground"}`}>{hint}</p>}
      </CardContent>
    </Card>
  );
}

// formatBacklog renders a backlog in milliseconds as a human-readable magnitude.
export function formatBacklog(ms: number, t: Translator): string {
  if (!ms) return "—";
  if (ms < 60_000) return t("unit.sec", { n: Math.round(ms / 1000) });
  if (ms < 3_600_000) return t("unit.min", { n: Math.round(ms / 60_000) });
  return t("unit.hour", { n: (ms / 3_600_000).toFixed(1) });
}
