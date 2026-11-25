import { useMemo } from "react";

import { useTranslations } from "next-intl";

import toolSources from "./builtin-tool-sources.json";

// Default block-rule names, asset-rule notes and tool descriptions are DB seed data, so they cannot be stored per language.
// They are translated only in the UI, via the message files (builtinRules, builtinAssetNotes, builtinTools in web/messages/*.json),
// and when the DB value differs from the original seed text (a user-edited value, or a description changed in code) it is shown as-is.
// A tool description's DB value is the instruction passed to the model, so the translation is never put into the edit form.
// The source text contains curly braces, so it is read with t.raw to avoid ICU parsing.

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

// The Chinese section headings mandated by the reporter agent prompt (agent/promptcatalog.go). Stored reports
// are not modified; the headings are swapped only for display. Copy/export keep the original text. PoC is not translated.
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

    // Lines inside a code block are original evidence text, so leave them untouched.
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
