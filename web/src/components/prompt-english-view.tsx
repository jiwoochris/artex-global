"use client";

import * as React from "react";

import { LanguagesIcon, LoaderIcon } from "lucide-react";
import { useLocale, useTranslations } from "next-intl";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { api, type PromptTranslation } from "@/lib/api";
import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";
import { cn } from "@/lib/utils";

// Read-only English views of agent prompts.
//
// The built-in prompts stay in Chinese (some wrap-up prompts in Korean) because that is
// what the agents were benchmarked with; the model always receives the original. These
// components only show an English translation next to it:
// - a built-in prompt gets its reviewed reference translation (agent/prompts_en);
// - edited or custom text can be machine-translated on request, which spends LLM tokens.

export type EnglishViewMode = "original" | "both" | "english";

const MODE_STORAGE_KEY = "artex.promptEnglishView";

function isMode(v: string | null): v is EnglishViewMode {
  return v === "original" || v === "both" || v === "english";
}

// useEnglishViewMode remembers the chosen view per browser. Without a saved choice it
// shows both side by side for English-speaking users and the original for everyone else.
export function useEnglishViewMode(): [EnglishViewMode, (m: EnglishViewMode) => void] {
  const locale = useLocale();
  const [mode, setMode] = React.useState<EnglishViewMode>(locale === "en" ? "both" : "original");
  React.useEffect(() => {
    try {
      const saved = getLocalStorageValue(MODE_STORAGE_KEY);
      if (isMode(saved)) setMode(saved);
    } catch {
      // storage unavailable: keep the default
    }
  }, []);
  const update = React.useCallback((m: EnglishViewMode) => {
    setMode(m);
    try {
      setLocalStorageValue(MODE_STORAGE_KEY, m);
    } catch {
      // storage unavailable: the choice lasts for this page only
    }
  }, []);
  return [mode, update];
}

// EnglishViewSwitch is the Original / Side by side / English toggle.
export function EnglishViewSwitch({
  mode,
  onChange,
}: {
  mode: EnglishViewMode;
  onChange: (m: EnglishViewMode) => void;
}) {
  const tr = useTranslations("agentEditor.english");
  const options: EnglishViewMode[] = ["original", "both", "english"];
  return (
    <fieldset
      className="m-0 inline-flex min-w-0 items-center gap-1 rounded-md border p-0.5"
      aria-label={tr("viewLabel")}
    >
      <LanguagesIcon className="text-muted-foreground mx-1 size-3.5" aria-hidden />
      {options.map((m) => (
        <Button
          key={m}
          type="button"
          size="sm"
          variant={mode === m ? "secondary" : "ghost"}
          className="h-6 px-2 text-xs"
          aria-pressed={mode === m}
          onClick={() => onChange(m)}
        >
          {tr(`mode.${m}`)}
        </Button>
      ))}
    </fieldset>
  );
}

// EnglishPanel shows the English version of text. It asks the server for the reviewed
// reference translation as the text changes (debounced), and offers machine translation
// when there is none.
export function EnglishPanel({ text, className }: { text: string; className?: string }) {
  const tr = useTranslations("agentEditor.english");
  const [result, setResult] = React.useState<PromptTranslation | null>(null);
  const [forText, setForText] = React.useState<string | null>(null);
  const [loading, setLoading] = React.useState(false);
  const [error, setError] = React.useState("");

  React.useEffect(() => {
    if (!text.trim()) {
      setResult(null);
      setForText(text);
      return;
    }
    let cancelled = false;
    const timer = setTimeout(() => {
      api
        .translatePrompt(text, false)
        .then((r) => {
          if (cancelled) return;
          setResult(r);
          setForText(text);
          setError("");
        })
        .catch((e: unknown) => {
          if (!cancelled) setError(e instanceof Error ? e.message : String(e));
        });
    }, 400);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [text]);

  const machineTranslate = async () => {
    setLoading(true);
    setError("");
    try {
      const r = await api.translatePrompt(text, true);
      setResult(r);
      setForText(text);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  };

  const stale = forText !== text;
  const hasText = result && result.source !== "none" && result.text;

  return (
    <div className={cn("grid min-w-0 content-start gap-2 rounded-md border bg-muted/30 p-3", className)}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-xs font-medium">{tr("title")}</span>
        {result?.source === "reference" && <Badge variant="secondary">{tr("source.reference")}</Badge>}
        {result?.source === "machine" && (
          <Badge variant="outline">
            {result.model ? tr("source.machineModel", { model: result.model }) : tr("source.machine")}
          </Badge>
        )}
        {result?.source === "original" && <Badge variant="outline">{tr("source.original")}</Badge>}
        {stale && text.trim() && <LoaderIcon className="text-muted-foreground size-3.5 animate-spin" />}
      </div>

      {!text.trim() ? (
        <p className="text-muted-foreground text-xs">{tr("empty")}</p>
      ) : hasText ? (
        <pre className="max-h-[60vh] overflow-auto whitespace-pre-wrap break-words font-mono text-xs">
          {result.text}
        </pre>
      ) : result?.source === "none" ? (
        <div className="grid gap-2">
          <p className="text-muted-foreground text-xs leading-relaxed">{tr("noReference")}</p>
          <div>
            <Button type="button" size="sm" variant="outline" onClick={machineTranslate} disabled={loading}>
              {loading ? <LoaderIcon className="animate-spin" /> : <LanguagesIcon />} {tr("machineBtn")}
            </Button>
          </div>
        </div>
      ) : null}

      {result?.source === "machine" && stale && !loading && (
        <div>
          <Button type="button" size="sm" variant="ghost" className="h-7 text-xs" onClick={machineTranslate}>
            <LanguagesIcon /> {tr("retranslateBtn")}
          </Button>
        </div>
      )}
      {result?.source === "machine" && <p className="text-muted-foreground text-xs">{tr("machineNote")}</p>}
      {error && <p className="text-destructive text-xs">{error}</p>}
    </div>
  );
}

// WithEnglish lays out an original (an editor or a read-only block) together with its
// English view, according to mode. englishText is what to translate; it defaults to the
// text being edited, but can differ (for example the built-in default an empty field falls
// back to).
export function WithEnglish({
  mode,
  englishText,
  children,
}: {
  mode: EnglishViewMode;
  englishText: string;
  children: React.ReactNode;
}) {
  if (mode === "original") return <>{children}</>;
  if (mode === "english") return <EnglishPanel text={englishText} />;
  return (
    <div className="grid gap-3 lg:grid-cols-2">
      <div className="min-w-0">{children}</div>
      <EnglishPanel text={englishText} />
    </div>
  );
}
