"use client";

import { CheckIcon } from "lucide-react";
import { useTranslations } from "next-intl";

import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import type { NotificationFilter } from "@/lib/types";

// asText / inputType are value helpers local to this file (tightly tied to control rendering), so they don't live in channel-fields.
import { type FieldDef, type FieldKind, SEVERITY_OPTIONS } from "./channel-fields";

// asText renders any config value as a string usable in an input.
// config comes from JSON, so a value may be string / number / boolean / array / null;
// here we only care whether it fits in a text box; actual serialization is buildConfig's job.
function asText(v: unknown): string {
  if (typeof v === "string") return v;
  if (v === null || v === undefined) return "";
  return String(v);
}

// inputType maps a field type to the input's type attribute.
function inputType(kind: FieldKind): "text" | "password" | "number" {
  if (kind === "password") return "password";
  if (kind === "number") return "number";
  return "text";
}

// ConfigField renders the control that matches the field definition.
//
// Masked fields are the only subtlety here: the input **does not show** the masked value itself, only a one-line
// "saved" hint. That way the UI has a single rule — text in the box is what the user typed,
// an empty box is an empty value. If "__masked__:…abc123" were put into the input, users would take it for placeholder text
// they're supposed to delete, making it easier to wipe the credential by mistake.
//
// def.label / def.help / def.options[].label are i18n keys in the notifyPage namespace,
// resolved here with t() (see channel-fields.ts).
export function ConfigField({
  def,
  value,
  isSecret,
  onChange,
}: {
  def: FieldDef;
  value: unknown;
  isSecret: boolean;
  onChange: (v: unknown) => void;
}) {
  const t = useTranslations("notifyPage");
  const id = `n-cfg-${def.key}`;
  const raw = asText(value);
  // Masked value echoed by the backend: looks like "__masked__:…abc123", where the tail is a recognizable fragment of the original value.
  const masked = isSecret && raw.startsWith("__masked__");
  const maskedTail = masked ? (raw.split("…")[1] ?? "") : "";

  if (def.kind === "switch") {
    return (
      <div className="flex items-center gap-2 text-sm">
        <Switch checked={value === true} onCheckedChange={onChange} aria-label={t(def.label)} />
        {t(def.label)}
        {def.help && <span className="text-muted-foreground">({t(def.help)})</span>}
      </div>
    );
  }

  if (def.kind === "select") {
    return (
      <div className="grid gap-2">
        <Label>{t(def.label)}</Label>
        <Select value={raw || def.options?.[0]?.value} onValueChange={onChange}>
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {(def.options ?? []).map((o) => (
              <SelectItem key={o.value} value={o.value}>
                {t(o.label)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    );
  }

  // Controls are dispatched by field type. An if chain is used instead of nested ternaries because there are four kinds of controls to tell apart,
  // and three levels of ternaries already make you stop and count parentheses.
  function control() {
    if (def.kind === "textarea" || def.kind === "kv") {
      return (
        <Textarea
          id={id}
          className="font-mono"
          placeholder={def.placeholder}
          value={masked ? "" : raw}
          onChange={(e) => onChange(e.target.value)}
        />
      );
    }
    if (def.kind === "list") {
      return (
        <Input
          id={id}
          value={Array.isArray(value) ? (value as string[]).join(", ") : raw}
          onChange={(e) => onChange(e.target.value)}
          placeholder={def.placeholder}
        />
      );
    }
    return (
      <Input
        id={id}
        className={def.kind === "text" ? "font-mono" : ""}
        type={inputType(def.kind)}
        placeholder={def.placeholder}
        value={masked ? "" : raw}
        onChange={(e) => onChange(e.target.value)}
      />
    );
  }

  const hint = masked ? (
    <p className="text-muted-foreground flex items-center gap-1 text-xs">
      <CheckIcon className="size-3" />
      {maskedTail ? t("masked.savedTail", { tail: maskedTail }) : t("masked.saved")}
    </p>
  ) : (
    def.help && <p className="text-muted-foreground text-xs">{t(def.help)}</p>
  );

  return (
    <div className="grid gap-2">
      <Label htmlFor={id}>{t(def.label)}</Label>
      {control()}
      {hint}
    </div>
  );
}

// FilterSummary condenses the filter conditions into one line, so the card shows what the channel pushes without expanding it.
export function FilterSummary({ filter }: { filter: NotificationFilter }) {
  const t = useTranslations("notifyPage");
  const parts: string[] = [];
  if (filter.min_severity) {
    const opt = SEVERITY_OPTIONS.find((o) => o.value === filter.min_severity);
    parts.push(opt ? t(opt.label) : filter.min_severity);
  }
  if (filter.vulnclass_include?.length) parts.push(t("summary.includeCount", { n: filter.vulnclass_include.length }));
  if (filter.vulnclass_exclude?.length) parts.push(t("summary.excludeCount", { n: filter.vulnclass_exclude.length }));
  if (filter.task_ids?.length) parts.push(t("summary.taskCount", { n: filter.task_ids.length }));
  if (filter.asset_ids?.length) parts.push(t("summary.assetCount", { n: filter.asset_ids.length }));
  if (filter.on_status_change) parts.push(t("summary.statusChange"));
  if (parts.length === 0) {
    return <p className="text-muted-foreground text-sm">{t("summary.all")}</p>;
  }
  return <p className="text-muted-foreground text-sm">{parts.join(" · ")}</p>;
}
