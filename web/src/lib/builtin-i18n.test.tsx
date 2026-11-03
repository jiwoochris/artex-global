import { NextIntlClientProvider } from "next-intl";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import ko from "../../messages/ko.json";
import { useAgentLabels } from "./agent-i18n";
import { useBuiltinLabels } from "./builtin-i18n";
import toolSources from "./builtin-tool-sources.json";

function labelsOf<T>(useLabels: () => T): T {
  let out: T | undefined;
  const Probe = () => {
    out = useLabels();
    return null;
  };
  renderToStaticMarkup(
    <NextIntlClientProvider locale="ko" messages={ko}>
      <Probe />
    </NextIntlClientProvider>,
  );
  return out as T;
}

describe("useBuiltinLabels", () => {
  const labels = labelsOf(useBuiltinLabels);

  it("시드 원문이면 규칙 이름과 메모를 번역한다", () => {
    expect(labels.ruleName("[内置] 磁盘格式化 mkfs")).toBe(ko.builtinRules.mkfs);
    expect(labels.assetNote("[内置] 政府网站 (.gov)")).toBe(ko.builtinAssetNotes.govGlobal);
  });

  it("사용자가 고친 값은 그대로 둔다", () => {
    expect(labels.ruleName("내 규칙")).toBe("내 규칙");
    expect(labels.assetNote("내 메모")).toBe("내 메모");
    expect(labels.toolDescription({ key: "add_hint", system: true, description: "수정함" })).toBe("수정함");
  });

  it("보고서 소제목만 번역하고 본문과 코드 블록은 그대로 둔다", () => {
    const report = "## 概述\n概述 본문\n\n## PoC\n```text\n## 证据\n```\n\n## 修复建议\nfix";
    expect(labels.reportHeadings(report)).toBe(
      `## ${ko.builtinReportHeadings.overview}\n概述 본문\n\n## PoC\n\`\`\`text\n## 证据\n\`\`\`\n\n## ${ko.builtinReportHeadings.remediation}\nfix`,
    );
  });

  it("커스텀 도구는 번역하지 않는다", () => {
    const description = toolSources.add_hint;
    expect(labels.toolDescription({ key: "add_hint", system: false, description })).toBe(description);
  });

  it("모든 시스템 도구 설명을 번역하며 중괄호가 있어도 실패하지 않는다", () => {
    for (const [key, description] of Object.entries(toolSources)) {
      const translated = labels.toolDescription({ key, system: true, description });
      expect(translated).toBe(ko.builtinTools[key as keyof typeof ko.builtinTools]);
    }
  });
});

describe("useAgentLabels reporter", () => {
  const labels = labelsOf(useAgentLabels);

  it("시드 값일 때만 번역 조회한다", () => {
    expect(labels.name({ key: "reporter", name: "보고서 작성" })).toBe(ko.builtinAgents.reporter.name);
    expect(labels.name({ key: "reporter", name: "내 보고서" })).toBe("내 보고서");
    expect(labels.description({ key: "reporter", description: "내 설명" })).toBe("내 설명");
  });
});
