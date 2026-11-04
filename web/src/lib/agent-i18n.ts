import { useCallback, useEffect, useMemo, useState } from "react";

import { useTranslations } from "next-intl";

import { api } from "@/lib/api";
import type { Agent } from "@/lib/types";

// Names, descriptions and prompt-variable labels of builtin agents are DB seed data and
// exist only in Chinese (builtinAgents in db/db.go, the vulnerability-retest agent in
// server/finding_retests.go). A DB column cannot hold per-language values, and the seed runs
// ON CONFLICT DO NOTHING so it never reaches installed instances either. The UI therefore
// looks the translation up by key in the message files (the builtinAgents namespace of
// web/messages/{en,es}.json) and falls back to the DB value when a key is missing. Agents
// created by users always render the DB value.

// retester is seeded with builtin=false (server FindingRetestAgentKey) but is a fixed key
// planted by the system, so it is translatable too. Agents with builtin=true (goals/planner/
// mainagent/worker/auto/pentest) are filtered by agent.builtin and need no entry here.
const SYSTEM_SEEDED_AGENT_KEYS = new Set(["retester"]);

function isSystemSeeded(agent: { key: string; builtin?: boolean }): boolean {
  return !!agent.builtin || SYSTEM_SEEDED_AGENT_KEYS.has(agent.key);
}

// useAgentLabels returns a helper bundle that renders builtin agent names, descriptions and
// prompt-variable labels in the active language. Every display site gets labels through it.
export function useAgentLabels() {
  const t = useTranslations("builtinAgents");

  // Callers use these functions as useMemo/useCallback dependencies (conversation grouping,
  // filter computation), so the bundle is rebuilt only when the language (t) changes. A new
  // object per render would recompute everything on every render.
  return useMemo(() => {
    // name returns the translated name for builtin agents, otherwise the DB name (agent.name).
    const name = (agent: { key: string; name: string; builtin?: boolean }): string => {
      if (isSystemSeeded(agent) && t.has(`${agent.key}.name`)) return t(`${agent.key}.name`);
      return agent.name;
    };

    // description returns the translated description for builtin agents, otherwise the DB
    // description (empty string when absent). Callers keep appending their own fallback text
    // for an empty description.
    const description = (agent: { key: string; description?: string; builtin?: boolean }): string => {
      if (isSystemSeeded(agent) && t.has(`${agent.key}.description`)) return t(`${agent.key}.description`);
      return agent.description ?? "";
    };

    // nameByKey is the variant used when only a key is available (conversation grouping). It
    // takes a lookup function: a hit yields the label, a miss yields the key.
    const nameByKey = (
      key: string,
      lookup: (key: string) => { key: string; name: string; builtin?: boolean } | undefined,
    ): string => {
      const agent = lookup(key);
      // biome-ignore lint/nursery/useNullishCoalescing: an agent with an empty ("") name must still show its key, so || is intentional.
      return (agent && name(agent)) || key;
    };

    // varLabel translates a builtin agent's prompt-variable label (e.g. goals' "overall task objective")
    // and falls back to the DB label when the key is missing.
    const varLabel = (agentKey: string, builtin: boolean, varName: string, fallback: string): string => {
      if ((builtin || SYSTEM_SEEDED_AGENT_KEYS.has(agentKey)) && t.has(`${agentKey}.vars.${varName}`)) {
        return t(`${agentKey}.vars.${varName}`);
      }
      return fallback;
    };

    return { name, description, nameByKey, varLabel };
  }, [t]);
}

// Several screens (approval records, …) need the agent list only to map key → display name,
// so it is fetched once and shared at module scope. A failure clears it for the next call.
let agentListPromise: Promise<Agent[]> | null = null;
function loadAgentList(): Promise<Agent[]> {
  agentListPromise ??= api.agents().catch((err) => {
    agentListPromise = null;
    throw err;
  });
  return agentListPromise;
}

// useAgentNameByKey returns the mapper used by screens that only hold a key (conv_agent_key in
// approval/intercept records, …): builtin agents render their translated name, user agents
// their DB name, and a key missing from the list (or a list still loading) renders as-is.
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
        // When the list fails to load the key renders as-is (the next mount retries).
      });
    return () => {
      alive = false;
    };
  }, []);
  return useCallback((key: string) => labels.nameByKey(key, (k) => agents.get(k)), [labels, agents]);
}
