import { useMemo } from "react";

import { useTranslations } from "next-intl";

import toolSources from "./builtin-tool-sources.json";

// 기본 차단 규칙 이름·자산 규칙 메모·도구 설명은 DB 시드 데이터라 언어별로 둘 수 없다.
// 화면에서만 메시지 파일(web/messages/*.json 의 builtinRules, builtinAssetNotes, builtinTools)로
// 번역하고, DB 값이 시드 원문과 다르면(사용자가 고친 값, 코드에서 바뀐 설명) 그대로 보여 준다.
// 도구 설명 DB 값은 모델에게 전달되는 지침이므로 편집 폼에는 번역문을 넣지 않는다.
// 원문에 중괄호가 있어 ICU 파싱을 거치지 않도록 t.raw 로 읽는다.

const RULE_KEYS: Record<string, string> = {
  "[内置] 递归强制删除 rm -rf": "rmRf",
  "[内置] 删除系统关键目录": "rmSystemDirs",
  "[内置] 磁盘格式化 mkfs": "mkfs",
  "[内置] 覆写磁盘设备 dd": "ddOverwrite",
  "[内置] Fork 炸弹": "forkBomb",
  "[内置] 关机 / 重启": "shutdownReboot",
  "[内置] 杀死全部进程": "killAll",
  "[内置] 磁盘擦除 shred / wipe": "shredWipe",
  "[内置] 清空防火墙规则": "flushFirewall",
  "[内置] SQL DROP DATABASE / TABLE / SCHEMA": "sqlDrop",
  "[内置] SQL TRUNCATE": "sqlTruncate",
  "[内置] MongoDB drop / dropDatabase": "mongoDrop",
  "[内置] Redis FLUSHALL / FLUSHDB": "redisFlush",
  "[内置] curl / wget 发送 DELETE 请求": "curlWgetDelete",
  "[内置] Python HTTP 客户端 DELETE（requests/httpx/aiohttp）": "pythonHttpDelete",
  "[内置] 脚本中声明 HTTP DELETE 方法（JS/通用）": "scriptHttpDelete",
  "[内置] 批量清空 / 清除接口路径": "bulkClearPath",
  "[内置] 破坏性系统命令": "destructiveCommand",
  "[内置] 数据外泄管道": "exfilPipe",
  "[内置] 删除类接口路径": "deletePath",
};

const ASSET_NOTE_KEYS: Record<string, string> = {
  "[内置] 政府网站 (.gov)": "govGlobal",
  "[内置] 政府网站 (.gov.cn)": "govCn",
  "[内置] 教育网站 (.edu)": "eduGlobal",
  "[内置] 教育网站 (.edu.cn)": "eduCn",
};

const TOOL_SOURCES: Record<string, string> = toolSources;

// 보고서 에이전트 프롬프트(agent/promptcatalog.go)가 지정한 중국어 소제목. 저장된 보고서는
// 고치지 않고 표시할 때만 바꾼다. 복사·내보내기는 원문 그대로다. PoC 는 번역하지 않는다.
const REPORT_HEADING_KEYS: Record<string, string> = {
  概述: "overview",
  影响与危害: "impact",
  受影响范围: "scope",
  复现步骤: "reproduce",
  证据: "evidence",
  根因分析: "rootCause",
  修复建议: "remediation",
};

export function useBuiltinLabels() {
  const tRule = useTranslations("builtinRules");
  const tNote = useTranslations("builtinAssetNotes");
  const tTool = useTranslations("builtinTools");
  const tHeading = useTranslations("builtinReportHeadings");

  return useMemo(() => {
    const ruleName = (name: string): string => {
      const key = RULE_KEYS[name];
      return key && tRule.has(key) ? (tRule.raw(key) as string) : name;
    };

    const assetNote = (note: string): string => {
      const key = ASSET_NOTE_KEYS[note];
      return key && tNote.has(key) ? (tNote.raw(key) as string) : note;
    };

    const toolDescription = (tool: { key: string; system: boolean; description: string }): string => {
      if (tool.system && TOOL_SOURCES[tool.key] === tool.description && tTool.has(tool.key)) {
        return tTool.raw(tool.key) as string;
      }
      return tool.description;
    };

    // 코드 블록 안의 줄은 증거 원문이므로 건드리지 않는다.
    const reportHeadings = (markdown: string): string => {
      let inFence = false;
      return markdown
        .split("\n")
        .map((line) => {
          if (/^\s*(```|~~~)/.test(line)) inFence = !inFence;
          if (inFence) return line;
          const match = /^(#{1,6}\s+)(.+?)\s*$/.exec(line);
          const key = match && REPORT_HEADING_KEYS[match[2]];
          return match && key ? match[1] + (tHeading.raw(key) as string) : line;
        })
        .join("\n");
    };

    return { ruleName, assetNote, toolDescription, reportHeadings };
  }, [tRule, tNote, tTool, tHeading]);
}
