import assert from "node:assert/strict";
import test from "node:test";
import fs from "node:fs";
import path from "node:path";
import { catalogs, catalogLocales } from "./catalog.ts";
import { LOCALES, LOCALE_DEFINITIONS, DEFAULT_LOCALE, intlLocale, normalizeLocale, readCookieLocale, withLocaleQuery, withLocaleHeaders } from "./config.ts";
import { placeholders, tags, interpolate, createFormatters } from "./format.ts";
import { __resetLocaleForTests, getLocale, setLocale, initializeLocale, translate, translateIn, subscribeLocale, localeHeaders, localizedUrl } from "./runtime.ts";
import { cloneDemoFixture } from "./demo-fixture.ts";
import { localizedFetch } from "./request.ts";
import { mentionKinds, mentionToken, selectedMentions, mentionSearch } from "../lib/chat-mentions.ts";
import { sourceFiles, scanFile, scanText, SRC_ROOT } from "./han-scan.mjs";

function browser(stored = null, cookie = "") {
  const values = new Map(stored ? [["scopeweaver.locale", stored]] : []);
  globalThis.window = { localStorage: { getItem: (key) => values.get(key) ?? null, setItem: (key,value) => values.set(key,value) } };
  globalThis.document = { cookie, documentElement: { lang: "en" } };
  return values;
}
function reset() { delete globalThis.window; delete globalThis.document; __resetLocaleForTests(); }

test("English is the default; unsupported and malformed preferences safely fall back", () => {
  reset(); assert.equal(getLocale(), "en");
  assert.equal(normalizeLocale("ko-KR"), "ko");
  assert.equal(normalizeLocale("zh-CN"), "zh-Hans");
  assert.equal(readCookieLocale("scopeweaver_locale=%E0%A4%A"), null);
  browser("unsupported", "scopeweaver_locale=ko"); initializeLocale(); assert.equal(getLocale(), "ko");
  reset(); browser("unsupported", "scopeweaver_locale=%bad"); initializeLocale(); assert.equal(getLocale(), "en"); reset();
});

test("persisted Korean starts with the English hydration snapshot and initializes afterward", () => {
  reset(); browser("ko");
  assert.equal(getLocale(), "en"); assert.equal(translate("common.save"), "Save");
  assert.equal(localeHeaders()["Accept-Language"], "ko", "first API call honors persisted preference before effects");
  let calls=0; const unsubscribe=subscribeLocale(()=>calls++); initializeLocale();
  assert.equal(getLocale(), "ko"); assert.equal(translate("common.save"), "저장"); assert.equal(calls,1);
  assert.match(document.cookie, /scopeweaver_locale=ko; Path=\//); assert.equal(document.documentElement.lang,"ko");
  setLocale("en"); assert.equal(window.localStorage.getItem("scopeweaver.locale"),"en"); assert.equal(calls,2);
  unsubscribe(); reset();
});

function checkCatalog(source, translated, locale) {
  assert.deepEqual(Object.keys(source).sort(), Object.keys(translated).sort(), `${locale}: keys`);
  for (const key of Object.keys(source)) {
    assert.equal(typeof translated[key], "string", `${locale}:${key}`);
    assert.ok(translated[key].trim().length, `${locale}:${key}: empty`);
    assert.deepEqual(placeholders(source[key]), placeholders(translated[key]), `${locale}:${key}: placeholders`);
    assert.deepEqual(tags(source[key]), tags(translated[key]), `${locale}:${key}: tags`);
    assert.doesNotMatch(translated[key], /Untranslated|미번역|未翻译/u, `${locale}:${key}`);
    if (locale === "en" || locale === "ko") assert.doesNotMatch(translated[key], /\p{Script=Han}/u, `${locale}:${key}`);
  }
}

test("every shipped catalog is complete and preserves placeholders and rich tags", () => {
  // 3266 shipped keys + 4 OpenCode Go provider template keys (providers.opencodeGo,
  // providers.opencodeGoHelp, and the two keys they added to the provider table).
  assert.equal(Object.keys(catalogs.en).length, 3272);
  assert.deepEqual(catalogLocales.sort(), [...LOCALES].sort());
  assert.deepEqual(Object.keys(catalogs).sort(), [...LOCALES].sort());
  for (const locale of LOCALES) checkCatalog(catalogs.en, catalogs[locale], locale);
  // This punctuation-only formatter has no translatable words. No prose may
  // silently become an English fallback copy in the Chinese resource.
  const unchangedChinese = Object.entries(catalogs.en).filter(([key, value]) => catalogs["zh-Hans"][key] === value).map(([key]) => key);
  assert.deepEqual(unchangedChinese, ["app.metricValue"]);
  assert.equal(interpolate("Task {id}: {name}", {id:7,name:"사용자 原文 {unchanged}"}), "Task 7: 사용자 原文 {unchanged}");
  assert.equal(interpolate("{missing}", {}), "{missing}");
});

test("output-limit help states what 0 actually sends on each call path", () => {
  // 0 is not omitted: agent turns send 32768, direct Anthropic calls 8192, native Ollama its own default.
  const qualifiers = { en: [/agent/i, /Anthropic/, /Ollama/], ko: [/에이전트/, /Anthropic/, /Ollama/], "zh-Hans": [/代理/, /Anthropic/, /Ollama/] };
  for (const locale of LOCALES) {
    const key = "interface.m1411";
    const text = catalogs[locale][key];
    assert.match(text, /32,?768/, `${locale}:${key}: agent-turn value`);
    for (const q of qualifiers[locale]) assert.match(text, q, `${locale}:${key}: ${q}`);
    assert.match(text, /8,?192/, `${locale}:${key}: direct Anthropic value`);
    assert.doesNotMatch(text, /server default|provider default|서버 기본값|제공자 기본값|服务端默认值|服务商默认值/, `${locale}:${key}: 0 is not a provider default`);
    // The GLM template now sets its own output limit; its help must state the values it fills.
    const glm = catalogs[locale]["providers.modelHelp"];
    assert.match(glm, /16,?384/, `${locale}: GLM template output limit`);
    assert.match(glm, /\blow\b/, `${locale}: GLM template effort`);
    assert.doesNotMatch(glm, /server default|provider default|서버 기본값|제공자 기본값|服务端默认值|服务商默认值/, `${locale}: GLM help`);
  }
});

test("integrity checks reject missing, empty, placeholder, rich-tag and untranslated fixtures", () => {
  const source = { title: "<b>Task {id}</b>" };
  for (const fixture of [{}, {title:" "}, {title:"<b>任务</b>"}, {title:"<code>任务 {id}</code>"}, {title:"<b>任务 {id}"}, {title:"<b><b>任务 {id}</b>"}, {title:"<b>未翻译 {id}</b>"}]) {
    assert.throws(() => checkCatalog(source, fixture, "zh-Hans"));
  }
  assert.throws(() => checkCatalog(source, {title:"<b>任务 {id}</b>"}, "en"));
  assert.throws(() => checkCatalog(source, {title:"<b>任务 {id}</b>"}, "ko"));
});

test("frontend and backend share canonical definitions, aliases and matching vectors", () => {
  const definition = JSON.parse(fs.readFileSync(new URL("../../../locale/definitions.json", import.meta.url)));
  const { vectors } = JSON.parse(fs.readFileSync(new URL("../../../locale/matching-vectors.json", import.meta.url)));
  assert.equal(DEFAULT_LOCALE, definition.default);
  assert.deepEqual(LOCALE_DEFINITIONS, definition.locales);
  assert.deepEqual(LOCALES, definition.locales.map(({id}) => id));
  for (const { id, aliases, intlLocale: tag } of definition.locales) {
    assert.equal(intlLocale(id), tag);
    for (const alias of aliases) assert.equal(normalizeLocale(alias), id, alias);
  }
  for (const {input, expected} of vectors) assert.equal(normalizeLocale(input) ?? "", expected, input);
  // Contract fixtures deliberately fail if a consumer omits a locale or alias.
  assert.throws(() => assert.deepEqual(LOCALES, ["en", "ko"]));
  assert.throws(() => assert.deepEqual(LOCALE_DEFINITIONS, definition.locales.map(d => ({...d, aliases:[]}))));
});


test("locale transport preserves caller headers, queries, and fragments", async () => {
  assert.equal(withLocaleQuery("/api/report?task=3#section","ko"),"/api/report?task=3&lang=ko#section");
  assert.equal(withLocaleQuery("/api/report?lang=en","ko"),"/api/report?lang=en");
  assert.deepEqual(withLocaleHeaders("ko", {Authorization:"Bearer demo", "Content-Type":"application/json"}),{"Accept-Language":"ko",Authorization:"Bearer demo","Content-Type":"application/json"});
  reset(); browser(); setLocale("ko"); const original=globalThis.fetch; let observed;
  globalThis.fetch=async(input,init)=>{observed={input,init};return new Response("ok")};
  try {
    await localizedFetch("/api/report?task=1",{headers:{Authorization:"Bearer example"}});
    assert.equal(observed.input,"/api/report?task=1&lang=ko");
    assert.equal(new Headers(observed.init.headers).get("accept-language"),"ko");
    assert.equal(new Headers(observed.init.headers).get("authorization"),"Bearer example");
  } finally {globalThis.fetch=original;reset();}
});

test("authored demo data changes language; edited and cloned user values do not", () => {
  reset(); browser();
  const fixture=cloneDemoFixture({title:"Dashboard",nested:[{name:"Save"}]},true);
  assert.equal(fixture.title,"Dashboard"); setLocale("ko"); assert.equal(fixture.title,"대시보드"); assert.equal(fixture.nested[0].name,"저장");
  Object.assign(fixture,{title:"My dashboard 原文"}); const runtimeCopy=cloneDemoFixture(fixture);
  setLocale("en"); assert.equal(fixture.title,"My dashboard 原文");assert.equal(runtimeCopy.title,"My dashboard 原文");
  fixture.title="Save"; const editedCopy=cloneDemoFixture(fixture); setLocale("ko");assert.equal(editedCopy.title,"Save","user text matching a catalog entry is still user text");reset();
});

test("localized mention labels preserve legacy wire tokens and user content", () => {
  reset();browser();setLocale("ko");assert.equal(mentionKinds[0].label,"취약점");
  assert.equal(mentionSearch("취약점 로그인").kind,"finding");assert.equal(mentionSearch("漏洞test").kind,"finding");
  const token=mentionToken({kind:"finding",id:12,label:"사용자 原文",description:""});assert.equal(token,"@[漏洞#12 사용자 原文]");
  assert.equal(selectedMentions(token)[0].label,"취약점 #12 · 사용자 原文");reset();
});

test("date and number formatters follow the selected locale", () => {
  const date=new Date("2026-10-02T00:00:00Z");
  const en=createFormatters("en"), ko=createFormatters("ko");
  assert.notEqual(en.date(date,{timeZone:"UTC"}),ko.date(date,{timeZone:"UTC"}));
  assert.equal(ko.number(12345),new Intl.NumberFormat("ko-KR").format(12345));
});

const legacyWire=new Set(["漏洞","资产","企业","接口","应用","域名","子域名","服务"]);
function isLegacyHanLiteral(rel, text) {
  rel=rel.replaceAll("\\","/");
  return (rel==="lib/chat-mentions.ts" && legacyWire.has(text)) ||
    (rel==="components/approval-records.tsx" && text==="/^\\[(?:模型|Model|모델)\\]\\s*/") ||
    (rel==="components/transcript.tsx" && text==="/(?:工具|Tool|도구)\\s+(\\S+)\\s+(?:请求|requests?|요청)/i") ||
    (rel==="lib/company-scope.ts" && text==="/icp|备案/i");
}

test("legacy Han exceptions are exact and equivalent for POSIX and Windows paths", () => {
  const exceptions=[
    ...[...legacyWire].map(text=>["lib/chat-mentions.ts",text]),
    ["components/approval-records.tsx","/^\\[(?:模型|Model|모델)\\]\\s*/"],
    ["components/transcript.tsx","/(?:工具|Tool|도구)\\s+(\\S+)\\s+(?:请求|requests?|요청)/i"],
    ["lib/company-scope.ts","/icp|备案/i"],
  ];
  for(const [file,text] of exceptions) {
    for(const rel of [file,file.replaceAll("/","\\")]) {
      assert.equal(isLegacyHanLiteral(rel,text),true,rel);
      assert.equal(isLegacyHanLiteral(rel,"未翻译"),false,rel);
      assert.equal(isLegacyHanLiteral(rel,`${text}新增`),false,rel);
    }
    assert.equal(isLegacyHanLiteral("lib/other.ts",text),false);
  }
});

test("authored UI contains no untranslated Han literals; legacy parsers are explicit exceptions", () => {
  const failures=[];
  for(const file of sourceFiles()) {
    const rel=path.relative(SRC_ROOT,file);
    if(rel.endsWith(".test.mjs"))continue; // Tests intentionally preserve multilingual user input.
    for(const hit of scanFile(file)) {
      if(isLegacyHanLiteral(rel,hit.text))continue;
      failures.push(`${rel}:${hit.line} ${hit.text}`);
    }
  }
  assert.deepEqual(failures,[]);
});

test("authored source and build configuration comments are English", async () => {
  const { hasHanComment } = await import("./han-scan.mjs");
  const files=[...sourceFiles(),path.resolve("next.config.mjs")];
  assert.deepEqual(files.filter(file=>hasHanComment(file)).map(file=>path.relative(SRC_ROOT,file)),[]);
});


test("dashboard count and token phrases preserve natural English and Korean spacing", () => {
  assert.equal(translateIn("en", "app.metricValue", { label: "Critical", value: 2 }), "Critical 2");
  assert.equal(translateIn("ko", "app.metricValue", { label: "심각", value: 2 }), "심각 2");
  const tokens = { input: "2.7M", cache: "1.7M", output: "211.7k" };
  assert.equal(translateIn("en", "app.tokenSummary", tokens), "In 2.7M (including cache 1.7M) · Out 211.7k");
  assert.equal(translateIn("ko", "app.tokenSummary", tokens), "입력 2.7M(캐시 1.7M 포함) · 출력 211.7k");
});


test("rich settings paragraphs preserve emphasis and translated theme announcements", () => {
  for (const key of Object.keys(catalogs.en).filter(key => key.startsWith("settings."))) {
    assert.deepEqual(tags(catalogs.en[key]), tags(catalogs.ko[key]), key);
  }
  assert.equal(translateIn("ko", "app.cycleTheme", { theme: "밝게" }), "현재 테마: 밝게. 클릭하여 테마 변경");
  assert.equal(translateIn("en", "settings.pagination", { from: 1, to: 4, total: 4 }), "1–4 / 4 records");
  assert.equal(translateIn("ko", "settings.pagination", { from: 1, to: 4, total: 4 }), "1–4 / 총 4건");
});


test("Chinese preference preserves English hydration, persistence and cookie privacy fallback", () => {
  reset(); browser("zh-CN");
  assert.equal(getLocale(), "en");
  assert.equal(localeHeaders()["Accept-Language"], "zh-Hans");
  assert.equal(localizedUrl("/api/events?cursor=7#tail"), "/api/events?cursor=7&lang=zh-Hans#tail");
  initializeLocale(); assert.equal(translate("common.save"), "保存");
  assert.equal(document.documentElement.lang, "zh-Hans");
  assert.equal(window.localStorage.getItem("scopeweaver.locale"), "zh-Hans");
  assert.match(document.cookie, /scopeweaver_locale=zh-Hans/);
  reset(); browser(null, "scopeweaver_locale=zh-SG");
  window.localStorage.getItem=()=>{throw new Error("privacy mode")};
  window.localStorage.setItem=()=>{throw new Error("privacy mode")};
  initializeLocale(); assert.equal(getLocale(), "zh-Hans"); reset();
});

test("Chinese authored fixtures localize while custom, edited and raw values survive switches", () => {
  reset(); browser();
  const authored=cloneDemoFixture({title:catalogs["zh-Hans"]["common.save"]}, true);
  const raw=cloneDemoFixture({title:catalogs["zh-Hans"]["common.save"]});
  setLocale("ko"); assert.equal(authored.title, "저장"); assert.equal(raw.title, "保存");
  setLocale("zh-Hans"); assert.equal(authored.title, "保存");
  authored.title="사용자 原文 <b>{id}</b>";
  setLocale("en"); assert.equal(authored.title, "사용자 原文 <b>{id}</b>"); reset();
});

test("Chinese API and SSE transport keep caller overrides, auth, cursors and fragments", async () => {
  assert.equal(withLocaleQuery("/api/events?cursor=3&lang=ko#tail", "zh-Hans"), "/api/events?cursor=3&lang=ko#tail");
  assert.equal(withLocaleHeaders("zh-Hans", {"accept-language":"ko"})["accept-language"], "ko");
  reset(); browser("zh-Hans"); const original=globalThis.fetch; let observed;
  globalThis.fetch=async(input,init)=>{observed={input,init};return new Response("ok")};
  try {
    await localizedFetch("/api/report?task=1#raw", {headers:{Authorization:"Bearer example"}});
    assert.equal(observed.input, "/api/report?task=1&lang=zh-Hans#raw");
    assert.equal(new Headers(observed.init.headers).get("accept-language"), "zh-Hans");
    assert.equal(new Headers(observed.init.headers).get("authorization"), "Bearer example");
  } finally {globalThis.fetch=original; reset();}
});

test("Han allowance covers direct Chinese catalog values only and preserves ordinary-source guards", () => {
  const resource=path.join(SRC_ROOT, "i18n/messages/zh-Hans.ts");
  assert.deepEqual(scanText('export const common = { save: "保存" };', resource), []);
  for (const [text, file] of [
    ['const title = "漏译";', path.join(SRC_ROOT,"ordinary.ts")],
    ['const view = <p>漏译</p>;', path.join(SRC_ROOT,"ordinary.tsx")],
    ['export const common = { save: translate("漏译") };', resource],
    ['const ordinary = "漏译";', resource],
    ['export const unexpected = { save: "漏译" };', resource],
  ]) assert.ok(scanText(text, file).length, text);
});

test("Chinese Intl and date-fns formatters map calendars, ranges and explicit caller locale", async () => {
  const {DATE_FNS_LOCALES, calendarLocale, formatRangeDate}=await import("./calendar.ts");
  assert.deepEqual(Object.keys(DATE_FNS_LOCALES).sort(), [...LOCALES].sort());
  const date=new Date(2026,9,2), zh=createFormatters("zh-Hans");
  assert.equal(zh.date(date), date.toLocaleDateString("zh-CN"));
  assert.equal(zh.number(12345.6), new Intl.NumberFormat("zh-CN").format(12345.6));
  assert.equal(formatRangeDate("en",date), "2 Oct 2026");
  assert.equal(formatRangeDate("ko",date), "2 10월 2026");
  assert.equal(formatRangeDate("zh-Hans",date), "2 10月 2026");
  assert.equal(calendarLocale("zh-Hans").code, "zh-CN");
  assert.equal(calendarLocale("zh-Hans", DATE_FNS_LOCALES.en), DATE_FNS_LOCALES.en);
});

test("Turbopack's explicit repository root includes the shared locale declaration", async () => {
  const { default: nextConfig } = await import("../../next.config.mjs");
  const repository = fs.realpathSync(new URL("../../../", import.meta.url));
  const definition = fs.realpathSync(new URL("../../../locale/definitions.json", import.meta.url));
  assert.equal(fs.realpathSync(nextConfig.turbopack.root), repository, "keep root explicit and bounded to this repository");
  const relative = path.relative(nextConfig.turbopack.root, definition);
  assert.ok(relative && !relative.startsWith("..") && !path.isAbsolute(relative), "Turbopack must resolve the shared declaration inside its root");
});
