import { useCallback, useEffect, useMemo, useState } from "react";

import { useTranslations } from "next-intl";

import { api } from "@/lib/api";
import type { Agent } from "@/lib/types";

// 기본 에이전트(builtin)의 이름·설명·프롬프트 변수 라벨은 DB 시드 데이터라
// 중국어로만 들어 있다(db/db.go 의 builtinAgents, server/finding_retests.go 의
// 漏洞复测 에이전트). DB 값을 언어별로 둘 수 없고 시드가 ON CONFLICT DO NOTHING 이라
// 이미 설치된 인스턴스에는 반영되지도 않으므로, 화면에서는 key 로 메시지 파일
// (web/messages/{ko,en,zh,es,ja}.json 의 builtinAgents 네임스페이스)에서 번역을 찾아
// 보여 주고, 없으면 DB 값을 그대로 쓴다. 사용자가 만든 에이전트는 항상 DB 값.

// retester 는 DB 에 builtin=false 로 시드되지만(server FindingRetestAgentKey) 시스템이
// 심은 고정 key 라 번역 대상에 포함한다. builtin=true 인 에이전트(goals/planner/
// mainagent/worker/auto/pentest)는 agent.builtin 으로 걸러지므로 여기 둘 필요가 없다.
const SYSTEM_SEEDED_AGENT_KEYS = new Set(["retester"]);

function isSystemSeeded(agent: { key: string; builtin?: boolean }): boolean {
  return !!agent.builtin || SYSTEM_SEEDED_AGENT_KEYS.has(agent.key);
}

// reporter 는 시스템이 심지만 UI 에서 고칠 수 있는 에이전트라(server/orchestration.go 의
// seedReporterAgent), DB 값이 시드 원문 그대로일 때만 번역한다. 사용자가 고친 값은 그대로 보여 준다.
const EDITABLE_SEEDS: Record<string, { name: string; description: string }> = {
  reporter: {
    name: "보고서 작성",
    description:
      "취약점 상세 보고서 작성: 취약점을 발견하면 자동으로 트리거되어, 증거와 실행 과정을 조회한 뒤 Markdown 보고서를 작성해 해당 취약점에 기록합니다.",
  },
};

// useAgentLabels 는 기본 에이전트의 이름·설명·프롬프트 변수 라벨을 현재 언어로
// 돌려주는 헬퍼 묶음이다. 모든 표시 지점에서 이 헬퍼를 거쳐 라벨을 얻는다.
export function useAgentLabels() {
  const t = useTranslations("builtinAgents");

  // 호출 쪽이 이 함수들을 useMemo·useCallback 의존성으로 쓰므로(대화 그룹·필터 계산),
  // 언어(t)가 바뀔 때만 새 묶음을 만든다. 매 렌더 새 객체면 그 계산이 매번 다시 돈다.
  return useMemo(() => {
    // name 은 기본 에이전트면 번역 이름을, 아니면 DB 이름(agent.name)을 돌려준다.
    const name = (agent: { key: string; name: string; builtin?: boolean }): string => {
      const translatable = isSystemSeeded(agent) || EDITABLE_SEEDS[agent.key]?.name === agent.name;
      if (translatable && t.has(`${agent.key}.name`)) return t(`${agent.key}.name`);
      return agent.name;
    };

    // description 은 기본 에이전트면 번역 설명을, 아니면 DB 설명(없으면 빈 문자열)을
    // 돌려준다. 빈 설명 폴백 문구는 호출 쪽에서 기존대로 붙인다.
    const description = (agent: { key: string; description?: string; builtin?: boolean }): string => {
      const translatable = isSystemSeeded(agent) || EDITABLE_SEEDS[agent.key]?.description === agent.description;
      if (translatable && t.has(`${agent.key}.description`)) return t(`${agent.key}.description`);
      return agent.description ?? "";
    };

    // nameByKey 는 Agent 객체 없이 key 만 있을 때(대화 그룹 묶기 등) 쓰는 변형이다.
    // 조회기(agent 를 돌려주는 함수)를 받아 있으면 번역, 없으면 key 를 돌려준다.
    const nameByKey = (
      key: string,
      lookup: (key: string) => { key: string; name: string; builtin?: boolean } | undefined,
    ): string => {
      const agent = lookup(key);
      // biome-ignore lint/nursery/useNullishCoalescing: 이름이 빈("") 에이전트도 예전처럼 key 로 대신 보여 줘야 하므로 || 가 의도된 동작이다.
      return (agent && name(agent)) || key;
    };

    // varLabel 은 기본 에이전트의 프롬프트 변수 라벨(예: goals 의 "任务总目标")을
    // 번역한다. 없으면 DB 라벨(fallback)을 그대로 쓴다.
    const varLabel = (agentKey: string, builtin: boolean, varName: string, fallback: string): string => {
      if ((builtin || SYSTEM_SEEDED_AGENT_KEYS.has(agentKey)) && t.has(`${agentKey}.vars.${varName}`)) {
        return t(`${agentKey}.vars.${varName}`);
      }
      return fallback;
    };

    return { name, description, nameByKey, varLabel };
  }, [t]);
}

// 에이전트 목록은 화면 여러 곳(승인 기록 등)이 key → 표시 이름 변환에만 쓰므로
// 한 번 받아 모듈 단위로 공유한다. 실패하면 다음 호출에서 다시 받는다.
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

// useAgentNameByKey 는 에이전트 객체 없이 key 만 가진 화면(승인·가로채기 기록의
// conv_agent_key 등)에서 쓰는 변환기를 돌려준다. 기본 에이전트는 현재 언어 이름,
// 사용자 에이전트는 DB 이름, 목록에 없거나 아직 못 받았으면 key 를 그대로 보여 준다.
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
        // 목록을 못 받으면 key 를 그대로 보여 준다(다음 마운트에서 다시 시도).
      });
    return () => {
      alive = false;
    };
  }, []);
  return useCallback((key: string) => labels.nameByKey(key, (k) => agents.get(k)), [labels, agents]);
}
