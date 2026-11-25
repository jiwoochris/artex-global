import { useCallback, useEffect, useMemo, useState } from "react";

import { useTranslations } from "next-intl";

import { api } from "@/lib/api";
import type { Agent } from "@/lib/types";

// The names, descriptions and prompt-variable labels of the default (builtin) agents are DB seed data
// and exist only in Chinese (builtinAgents in db/db.go, and the
// 漏洞复测 (vulnerability retest) agent in server/finding_retests.go). DB values cannot be stored per language, and the seed uses ON CONFLICT DO NOTHING,
// so changes would not even reach already-installed instances. The UI therefore looks up a translation by key
// in the message files (the builtinAgents namespace in web/messages/{ko,en,zh,es,ja}.json)
// and falls back to the DB value when there is none. User-created agents always use the DB value.

// retester is seeded in the DB with builtin=false (server FindingRetestAgentKey), but it is a fixed key
// planted by the system, so it is included in translation. Agents with builtin=true (goals/planner/
// mainagent/worker/auto/pentest) are already caught by agent.builtin, so they do not need to be listed here.
const SYSTEM_SEEDED_AGENT_KEYS = new Set(["retester"]);

function isSystemSeeded(agent: { key: string; builtin?: boolean }): boolean {
  return !!agent.builtin || SYSTEM_SEEDED_AGENT_KEYS.has(agent.key);
}

// reporter is planted by the system but can be edited in the UI (seedReporterAgent in server/orchestration.go),
// so it is translated only while the DB value is still the original seed text. Values the user edited are shown as-is.
const EDITABLE_SEEDS: Record<string, { name: string; description: string }> = {
  reporter: {
    name: "보고서 작성",
    description:
      "취약점 상세 보고서 작성: 취약점을 발견하면 자동으로 트리거되어, 증거와 실행 과정을 조회한 뒤 Markdown 보고서를 작성해 해당 취약점에 기록합니다.",
  },
};

// useAgentLabels is a bundle of helpers that return the default agents' names, descriptions and prompt-variable
// labels in the current language. Every display site obtains its labels through these helpers.
export function useAgentLabels() {
  const t = useTranslations("builtinAgents");

  // Callers use these functions as useMemo/useCallback dependencies (conversation grouping, filter computation),
  // so a new bundle is built only when the language (t) changes. A new object on every render would rerun those computations every time.
  return useMemo(() => {
    // name returns the translated name for a default agent, otherwise the DB name (agent.name).
    const name = (agent: { key: string; name: string; builtin?: boolean }): string => {
      const translatable = isSystemSeeded(agent) || EDITABLE_SEEDS[agent.key]?.name === agent.name;
      if (translatable && t.has(`${agent.key}.name`)) return t(`${agent.key}.name`);
      return agent.name;
    };

    // description returns the translated description for a default agent, otherwise the DB description (or an empty
    // string if none). Callers append the empty-description fallback text as before.
    const description = (agent: { key: string; description?: string; builtin?: boolean }): string => {
      const translatable = isSystemSeeded(agent) || EDITABLE_SEEDS[agent.key]?.description === agent.description;
      if (translatable && t.has(`${agent.key}.description`)) return t(`${agent.key}.description`);
      return agent.description ?? "";
    };

    // nameByKey is the variant used when there is only a key and no Agent object (conversation grouping, etc.).
    // It takes a lookup function (one that returns the agent) and returns the translation if found, otherwise the key.
    const nameByKey = (
      key: string,
      lookup: (key: string) => { key: string; name: string; builtin?: boolean } | undefined,
    ): string => {
      const agent = lookup(key);
      // biome-ignore lint/nursery/useNullishCoalescing: an agent with an empty ("") name must still be shown by its key as before, so || is the intended behavior.
      return (agent && name(agent)) || key;
    };

    // varLabel translates a default agent's prompt-variable label (e.g. goals' "任务总目标" (overall task goal)).
    // If there is no translation, the DB label (fallback) is used as-is.
    const varLabel = (agentKey: string, builtin: boolean, varName: string, fallback: string): string => {
      if ((builtin || SYSTEM_SEEDED_AGENT_KEYS.has(agentKey)) && t.has(`${agentKey}.vars.${varName}`)) {
        return t(`${agentKey}.vars.${varName}`);
      }
      return fallback;
    };

    return { name, description, nameByKey, varLabel };
  }, [t]);
}

// Several screens (approval records, etc.) use the agent list only to convert key → display name,
// so it is fetched once and shared at module level. On failure it is fetched again on the next call.
let agentListPromise: Promise<Agent[]> | null = null;
function loadAgentList(): Promise<Agent[]> {
  if (!agentListPromise) {
    agentListPromise = api.agents().catch((err) => {
      agentListPromise = null;
      throw err;
    });
  }
  return agentListPromise;
}

// useAgentNameByKey returns a converter for screens that have only a key and no agent object (e.g.
// conv_agent_key in approval/interception records). Default agents get their name in the current language,
// user agents get the DB name, and if the agent is not in the list or the list has not loaded yet, the key is shown as-is.
export function useAgentNameByKey(): (key: string) => string {
  const labels = useAgentLabels();
  const [agents, setAgents] = useState<Map<string, Agent>>(() => new Map());
  useEffect(() => {
    let alive = true;
    loadAgentList()
      .then((list) => {
        if (alive) setAgents(new Map(list.map((a) => [a.key, a])));
      })
      .catch(() => {
        // If the list cannot be fetched, show the key as-is (retry on the next mount).
      });
    return () => {
      alive = false;
    };
  }, []);
  return useCallback((key: string) => labels.nameByKey(key, (k) => agents.get(k)), [labels, agents]);
}
