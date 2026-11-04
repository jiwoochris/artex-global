// Real backend client. /api/* is proxied to the Go backend (next.config rewrites).
// Returns the domain types in lib/types.ts. Shapes match the backend handlers;
// a few fields the backend serializes differently (e.g. created_at as a unix int)
// are passed through and formatted at the call site.

import type { ChatMention } from "@/lib/chat-mentions";
import { MOCK } from "@/lib/mock/enabled";
import { mockHandle } from "@/lib/mock/handler";
import type {
  ActiveFindingRetest,
  Activity,
  Agent,
  AgentDetail,
  AgentTrigger,
  ArchiveBatchItem,
  Asset,
  AssetInterceptRule,
  AssetInterceptRuleInput,
  Audit,
  BatchCategoryItem,
  BatchControlItem,
  ChatAttachment,
  CommandRecord,
  Company,
  CompanyScopeMutation,
  CompanyScopeRule,
  Conversation,
  ConvTokenSummary,
  CoverageAssetRefs,
  CoverageGraphData,
  DailyTokenBucket,
  DeleteTaskOptions,
  DeleteTaskResult,
  Edge,
  EvidenceBodyPreview,
  ExplorationNodePage,
  ExplorationNodeQuery,
  Finding,
  FindingAssetTree,
  FindingDeepenResponse,
  FindingGroupsPage,
  FindingQuery,
  FindingRetest,
  FindingStats,
  FindingStatus,
  FindingsPage,
  FindingTraffic,
  FindingTrafficDetail,
  IntentAsset,
  InterceptApprovalFilter,
  InterceptApprovalRow,
  InterceptDetail,
  InterceptPending,
  InterceptRule,
  JudgeConfig,
  JudgeUsage,
  LLMPoolStatus,
  LLMProfile,
  LLMRecordDetail,
  LLMRecordItem,
  LLMRetryOverride,
  LLMRetryPolicy,
  LLMTask,
  MCPServer,
  MCPTool,
  MissingSkill,
  ModelTokenStat,
  NotificationChannel,
  NotificationDelivery,
  NotificationFilter,
  NotificationMeta,
  PromptVar,
  PromptVersion,
  SessionTokenUsage,
  Settings,
  Severity,
  SkillCall,
  SkillItem,
  SSProject,
  SSTask,
  Stats,
  Task,
  TaskArchive,
  TaskArchivePage,
  TaskAssetMutation,
  TaskAssetScopeMutation,
  TaskCategory,
  TaskConstraint,
  TaskGoal,
  TaskLLMResolutions,
  TaskNode,
  TaskScopeRow,
  TaskTemplate,
  TokenTotal,
  TokenUsage,
  Tool,
  ToolStat,
  TrafficDetail,
  TrafficEvidenceRef,
  TrafficEvidenceRole,
  TrafficHost,
  TrafficResp,
  UpdateCheck,
  UsageStats,
  WorkspaceFile,
  WorkspaceListing,
} from "@/lib/types";

// ApiError is an API error that carries the HTTP status code. Server error text is localized,
// so screen logic must branch on status, never on the message.
export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

function getToken(): string | null {
  if (typeof window === "undefined") return null;
  return localStorage.getItem("artex_token");
}

export async function http<T>(path: string, init?: RequestInit): Promise<T> {
  if (MOCK) return mockHandle<T>(init?.method ?? "GET", path, init?.body ?? null);
  const token = getToken();
  const r = await fetch(`/api${path}`, {
    ...init,
    headers: {
      ...(init?.body ? { "Content-Type": "application/json" } : {}),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(init?.headers as Record<string, string> | undefined),
    },
  });
  if (r.status === 401) {
    if (typeof window !== "undefined") {
      localStorage.removeItem("artex_token");
      document.cookie = "artex_token=; path=/; max-age=0";
      window.location.href = "/login";
    }
    throw new Error("Not authenticated");
  }
  if (!r.ok) {
    const fallback = `${init?.method ?? "GET"} ${path}: ${r.status}`;
    let message = fallback;
    try {
      const payload = (await r.json()) as { error?: unknown };
      if (typeof payload.error === "string" && payload.error.trim()) {
        message = payload.error.trim();
      }
    } catch {
      // Keep the status-based fallback for empty or non-JSON error responses.
    }
    throw new ApiError(message, r.status);
  }
  if (r.status === 204) return undefined as T;
  return r.json();
}

// sseUrl builds a URL for Server-Sent Events streams.
//
// Production (static export / Docker): the Go backend serves the web UI *and* the
// SSE streams on the same port, so we default to a same-origin (relative) URL.
// This is what makes reverse-proxy deploys work: a page loaded from
// https://<domain>/ connects to https://<domain>/api/..., which the proxy forwards
// to the backend — no need to expose :8787 publicly. Hardcoding :8787 here used to
// break exactly that (https://<domain>:8787/... is unreachable when only 443 is open).
//
// Dev (next dev): SSE must NOT go through the Next.js `/api` rewrite — that proxy
// buffers the streamed response, so event frames never reach the browser (the
// EventSource opens but receives 0 messages). So in dev only we connect straight
// to the Go backend on :8787, whose CORS is open.
//
// Override either default with NEXT_PUBLIC_SSE_BASE (set it to "" to force same-origin).
// Token is appended as ?token= because SSE can't carry cookies cross-origin.
// mockReport returns a canned Markdown report for the demo.
function mockReport(_task?: string): string {
  return `# ARTEX penetration test report: Acme Corp

## Overview
- Scope: acme.com (including the www / admin / api / shop / vpn subdomains)
- Confirmed findings: 6 (3 high · 3 medium · 2 low)
- Engine mode: exploring

## Key findings
1. **[High] Back-office default password** admin.acme.com admin/admin123 → the back office can be taken over completely.
2. **[High] SQL injection** www.acme.com/search?q= → the acme_prod database can be read.
3. **[High] IDOR** api.acme.com/v1/orders?id= → other users' orders (including phone number and address) can be read by bypassing authorization.
4. **[Medium] Reflected XSS**, **.git source code exposure**, **no login rate limiting**.

## Recommendations
- Force a back-office password change, enable MFA, and block default passwords.
- Apply parameterized queries to the search endpoint and encode its output.
- Add object-level authorization checks (IDOR) to the API and switch to a strong JWT key.

> (demo) This report was generated from mock data and exists for the UI demo.`;
}

export function sseUrl(path: string): string {
  const base =
    process.env.NEXT_PUBLIC_SSE_BASE ??
    (process.env.NODE_ENV !== "production" && typeof window !== "undefined"
      ? `${window.location.protocol}//${window.location.hostname}:8787`
      : "");
  const token = getToken();
  const sep = path.includes("?") ? "&" : "?";
  return token ? `${base}${path}${sep}token=${encodeURIComponent(token)}` : `${base}${path}`;
}

const get = <T>(p: string) => http<T>(p);
const post = <T>(p: string, body?: unknown) =>
  http<T>(p, { method: "POST", body: body ? JSON.stringify(body) : undefined });
const put = <T>(p: string, body?: unknown) =>
  http<T>(p, { method: "PUT", body: body ? JSON.stringify(body) : undefined });
const patch = <T>(p: string, body?: unknown) =>
  http<T>(p, { method: "PATCH", body: body ? JSON.stringify(body) : undefined });
const del = <T>(p: string, body?: unknown) =>
  http<T>(p, { method: "DELETE", body: body ? JSON.stringify(body) : undefined });

// Go serializes nil slices as JSON null — coerce to [].
const arr = <T>(x: T[] | null | undefined): T[] => x ?? [];
const tq = (task?: string, sep: "?" | "&" = "?") => (task ? `${sep}task=${encodeURIComponent(task)}` : "");

// findingFilterParams serializes the findings page filters into a query string. The list / grouped /
// asset tree / export paths share it, so adding a filter touches only this function (the backend parses the same shape).
function findingFilterParams(q: Omit<FindingQuery, "page" | "pageSize">): URLSearchParams {
  const p = new URLSearchParams();
  if (q.severity && q.severity !== "all") p.set("severity", q.severity);
  if (q.status && q.status !== "all") p.set("status", q.status);
  if (q.vulnclass && q.vulnclass !== "all") p.set("vulnclass", q.vulnclass);
  if (q.task && q.task !== "all") p.set("task_id", q.task);
  if (q.query?.trim()) p.set("q", q.query.trim());
  if (q.sort) p.set("sort", q.sort);
  if (q.assetScope) p.set("asset_scope", q.assetScope);
  return p;
}

function interceptPageQuery(page: number, size: number, filter: InterceptApprovalFilter) {
  const query = new URLSearchParams({ page: String(page), size: String(size) });
  if (filter.status) query.set("status", filter.status);
  if (filter.decision_source) query.set("decision_source", filter.decision_source);
  return query.toString();
}

export const api = {
  // Backend app version (injected via ldflags on release, defaults to "dev").
  health: () => get<{ ok: boolean; service: string; version: string }>("/health"),

  // ---- auth ----
  authStatus: () => get<{ initialized: boolean; setup_token_required?: boolean }>("/auth/status"),
  login: (username: string, password: string) => post<{ token: string }>("/auth/login", { username, password }),
  // First-run setup also requires the setup token printed only on the server console (prevents remote takeover).
  initPassword: (password: string, setupToken: string) =>
    post<{ token: string }>("/auth/init", { password, setup_token: setupToken }),
  // Changing the password invalidates every earlier token, so the session continues with the new token from the response.
  changePassword: (oldPassword: string, newPassword: string) =>
    post<{ ok: boolean; token?: string }>("/auth/change-password", {
      old_password: oldPassword,
      new_password: newPassword,
    }),

  // ---- tasks ----
  tasks: () =>
    get<{ tasks: Task[]; active: string }>("/tasks").then((r) => ({ tasks: arr(r.tasks), active: r.active ?? "" })),
  task: (id: string) => get<Task>(`/tasks/${encodeURIComponent(id)}`),
  createTask: (input: {
    name?: string;
    categoryId?: number;
    description: string;
    goal: string;
    llmProfileIds?: number[];
    sourceTaskIds?: string[];
    companyIds?: number[];
    timeoutSeconds?: number;
    seedFirstIntent?: boolean;
    planHeartbeatSeconds?: number;
    coverageEnabled?: boolean;
    interceptRules?: AssetInterceptRuleInput[];
  }) =>
    post<Task>("/tasks", {
      name: input.name ?? "",
      category_id: input.categoryId ?? null,
      description: input.description,
      goal: input.goal,
      llm_profile_ids: input.llmProfileIds ?? [],
      source_task_ids: input.sourceTaskIds ?? [],
      company_ids: input.companyIds ?? [],
      timeout_seconds: input.timeoutSeconds ?? 0,
      seed_first_intent: input.seedFirstIntent ?? false,
      plan_heartbeat_seconds: input.planHeartbeatSeconds ?? 0, // 0 = the backend normalizes it to the default 600 (10min)
      coverage_enabled: input.coverageEnabled ?? true, // on by default; false = disable the asset coverage feature
      intercept_rules: input.interceptRules ?? [], // task-level asset intercept rules
    }),
  taskCategories: () => get<{ categories: TaskCategory[] }>("/task-categories").then((r) => arr(r.categories)),
  updateTask: (id: string, input: { name?: string; pinned?: boolean }) => patch<Task>(`/tasks/${id}`, input),
  renameTask: (id: string, name: string) => patch<Task>(`/tasks/${id}`, { name }),
  pinTask: (id: string, pinned: boolean) => patch<Task>(`/tasks/${id}`, { pinned }),
  createTaskCategory: (name: string) => post<TaskCategory>("/task-categories", { name }),
  renameTaskCategory: (id: number, name: string) => patch<TaskCategory>(`/task-categories/${id}`, { name }),
  deleteTaskCategory: (id: number) => del<{ deleted: number }>(`/task-categories/${id}`),
  updateTaskCategory: (taskId: string, categoryId?: number) =>
    patch<Task>(`/tasks/${taskId}/category`, { category_id: categoryId ?? null }),
  // categoryId omitted/undefined = remove from the category (the backend receives null)
  updateTasksCategory: (taskIds: string[], categoryId?: number) =>
    post<{ items: BatchCategoryItem[]; category: TaskCategory | null }>("/tasks/category/batch", {
      task_ids: taskIds,
      category_id: categoryId ?? null,
    }),
  taskTemplates: () => get<{ templates: TaskTemplate[] }>("/task-templates").then((r) => arr(r.templates)),
  createTaskTemplate: (
    input: Pick<TaskTemplate, "name" | "description" | "goal" | "category_id" | "intercept_rules">,
  ) => post<TaskTemplate>("/task-templates", input),
  updateTaskTemplate: (
    id: number,
    input: Partial<Pick<TaskTemplate, "name" | "description" | "goal" | "category_id" | "intercept_rules">>,
  ) => patch<TaskTemplate>(`/task-templates/${id}`, input),
  deleteTaskTemplate: (id: number) => del<{ deleted: number }>(`/task-templates/${id}`),
  updateTaskLLMProfiles: (id: string, llmProfileIds: number[], activeLLMProfileId?: number) =>
    put<{
      id: string;
      llm_profile_ids: number[];
      active_llm_profile_id?: number;
      llm_failover_state: string;
      reopened_intents: number;
      switch_event?: Activity;
    }>(`/tasks/${id}/llm`, {
      llm_profile_ids: llmProfileIds,
      active_llm_profile_id: activeLLMProfileId ?? null,
    }),
  deleteTask: (id: string, options: DeleteTaskOptions) => del<DeleteTaskResult>(`/tasks/${id}`, options),
  controlTask: (id: string, action: "pause" | "resume") =>
    post<{ id: string; paused: boolean; queued: boolean; status: string }>(`/tasks/${id}/control`, { action }),
  controlTasksBatch: (taskIds: string[], action: "pause" | "resume") =>
    post<{ items: BatchControlItem[] }>("/tasks/control/batch", { task_ids: taskIds, action }),
  taskArchives: (input?: { page?: number; size?: number; q?: string; state?: string }) => {
    const query = new URLSearchParams();
    query.set("page", String(input?.page ?? 1));
    query.set("size", String(input?.size ?? 20));
    if (input?.q) query.set("q", input.q);
    if (input?.state) query.set("state", input.state);
    return get<TaskArchivePage>(`/task-archives?${query.toString()}`).then((response) => ({
      ...response,
      items: arr(response.items),
    }));
  },
  taskArchive: (id: number) => get<TaskArchive>(`/task-archives/${id}`),
  archiveTask: (id: string) => post<TaskArchive>(`/tasks/${id}/archive`),
  archiveTasks: (taskIds: string[]) =>
    post<{ items: ArchiveBatchItem[] }>("/tasks/archive/batch", { task_ids: taskIds }),
  restoreTaskArchive: (id: number) => post<TaskArchive>(`/task-archives/${id}/restore`),
  restoreTaskArchives: (archiveIds: number[]) =>
    post<{ items: ArchiveBatchItem[] }>("/task-archives/restore/batch", { archive_ids: archiveIds }),
  deleteTaskArchive: (id: number) => del<TaskArchive>(`/task-archives/${id}`),
  deleteTaskArchives: (archiveIds: number[]) =>
    post<{ items: ArchiveBatchItem[] }>("/task-archives/delete/batch", { archive_ids: archiveIds }),
  controlIntent: (
    taskId: string,
    intentId: string,
    action: "pause" | "resume" | "cancel",
    reason?: string,
    // cancel only: soft (default, pseudo-delete — the intent is marked deleted and the reason is recorded) | hard (real delete, cascading to exclusive descendants).
    mode?: "soft" | "hard",
  ) =>
    post<{
      id: number;
      state: "paused" | "open" | "deleted" | "";
      deleted?: { intents: number; facts: number; findings: number; activities: number };
    }>(`/tasks/${taskId}/intents/${intentId}/control`, { action, reason: reason ?? "", mode: mode ?? "soft" }),
  sendWorkerMessage: (taskId: string, intentId: string, message: string, requestId: string) =>
    post<{
      id: number;
      state: "running";
      accepted: true;
      request_id: string;
    }>(`/tasks/${taskId}/intents/${intentId}/messages`, { message, request_id: requestId }),
  taskLLMResolution: (id: string) => get<TaskLLMResolutions>(`/tasks/${id}/llm/resolution`),
  // Re-run an intent that did not succeed (blocked/exhausted/stopped): sets it back to open and the worker claims and re-runs it from scratch.
  rerunIntent: (taskId: string, intentId: string) =>
    post<{ id: string; reopened: number }>(`/tasks/${taskId}/intents/${intentId}/rerun`),
  // Re-run every blocked intent of this task in bulk (one click when a single network/LLM outage blocked many).
  rerunBlocked: (taskId: string) => post<{ id: string; reopened: number }>(`/tasks/${taskId}/intents/rerun-blocked`),
  setActive: (id: string) => post<{ active: string }>("/active", { id }),
  // ---- stats ----
  stats: (task?: string) => get<Stats>(`/stats${tq(task)}`),
  // Asset test coverage (rough estimate, for reference): share of in-scope assets touched by a fact + total/tested per type.
  taskCoverage: (id: string) =>
    get<{
      enabled: boolean; // whether asset coverage is enabled; the other fields are zero when false
      scope_rows: number;
      denominator: number;
      tested: number;
      pct: number | null;
      by_type: { type: string; total: number; tested: number }[];
    }>(`/tasks/${id}/coverage`),
  // Asset coverage graph: every in-scope asset plus the root-domain/company nodes that connect them, with tested/in_scope.
  taskCoverageGraph: (id: string) => get<CoverageGraphData>(`/tasks/${id}/coverage-graph`),
  // Global llm_usage aggregate (dashboard token view): totals per profile + daily buckets.
  usageStats: (days = 365) => get<UsageStats>(`/tokens/usage?days=${days}`),
  // ---- target management (overview) ----
  // Every target of this task (text/vulnclass/state).
  taskGoals: (id: string) =>
    get<{ goals: TaskGoal[] | null }>(`/tasks/${id}/goals`).then((response) => ({ goals: arr(response.goals) })),
  // Manually added target: written into the graph, the planner is notified, and the task is revived.
  addGoal: (id: string, text: string, vulnclass?: string) =>
    post<TaskGoal>(`/tasks/${id}/goals`, { text, vulnclass: vulnclass ?? "" }),
  // Manually edited target text (and vulnclass): tells the planner "old became new" and revives the task.
  updateGoal: (id: string, goalId: string, text: string, vulnclass?: string) =>
    patch<TaskGoal>(`/tasks/${id}/goals/${goalId}`, { text, vulnclass: vulnclass ?? "" }),
  // Manually deleted target (hard delete): the planner is notified; deletion does not revive the task.
  deleteGoal: (id: string, goalId: string) => del<{ ok: boolean }>(`/tasks/${id}/goals/${goalId}`),

  // ---- operation constraint management (overview) ----
  // Every operation constraint of this task (allow/deny).
  taskConstraints: (id: string) =>
    get<{ constraints: TaskConstraint[] | null }>(`/tasks/${id}/constraints`).then((response) => ({
      constraints: arr(response.constraints),
    })),
  // Add a constraint (the planner is not notified; the next planning round reads it naturally).
  addConstraint: (id: string, text: string, kind: TaskConstraint["kind"]) =>
    post<TaskConstraint>(`/tasks/${id}/constraints`, { text, kind }),
  // Edit a constraint (text + allow/deny).
  updateConstraint: (id: string, constraintId: string, text: string, kind: TaskConstraint["kind"]) =>
    patch<TaskConstraint>(`/tasks/${id}/constraints/${constraintId}`, { text, kind }),
  // Delete a constraint.
  deleteConstraint: (id: string, constraintId: string) =>
    del<{ ok: boolean }>(`/tasks/${id}/constraints/${constraintId}`),

  // ---- task-level asset intercept/allow rules (overview) ----
  taskInterceptRules: (id: string) =>
    get<{ rules: AssetInterceptRule[] | null }>(`/tasks/${id}/intercept-rules`).then((r) => arr(r.rules)),
  createTaskInterceptRule: (id: string, rule: AssetInterceptRuleInput) =>
    post<AssetInterceptRule>(`/tasks/${id}/intercept-rules`, rule),
  updateTaskInterceptRule: (id: string, ruleId: number, rule: AssetInterceptRuleInput) =>
    put<AssetInterceptRule>(`/tasks/${id}/intercept-rules/${ruleId}`, rule),
  deleteTaskInterceptRule: (id: string, ruleId: number) =>
    del<{ deleted: boolean }>(`/tasks/${id}/intercept-rules/${ruleId}`),
  toggleTaskInterceptRule: (id: string, ruleId: number, enabled: boolean) =>
    post<{ ok: boolean; enabled: boolean }>(`/tasks/${id}/intercept-rules/${ruleId}/toggle`, { enabled }),

  // Test scope entries of this task (including scope inherited from the source task).
  taskScope: (id: string) => get<{ scope: TaskScopeRow[] }>(`/tasks/${id}/scope`),
  // Manually add a test scope entry (kind=company/root_domain/subdomain/ip/cidr).
  addTaskScope: (id: string, kind: string, value: string, reason?: string) =>
    post<TaskScopeRow>(`/tasks/${id}/scope`, { kind, value, reason }),
  // Remove one test scope entry from this task.
  deleteTaskScope: (id: string, scopeId: number) => del<{ ok: boolean }>(`/tasks/${id}/scope/${scopeId}`),
  // Intents / facts / findings of one asset inside this task (used by the coverage graph node drawer).
  taskAssetRefs: (id: string, assetId: number) => get<CoverageAssetRefs>(`/tasks/${id}/asset-refs?asset_id=${assetId}`),

  // ---- workspace file manager (workDir) ----
  workspaceList: (path = "") => get<WorkspaceListing>(`/workspace/list?path=${encodeURIComponent(path)}`),
  workspaceRead: (path: string) => get<WorkspaceFile>(`/workspace/read?path=${encodeURIComponent(path)}`),
  workspaceWrite: (path: string, content: string) =>
    post<{ ok: boolean; path: string }>(`/workspace/write`, { path, content }),
  workspaceMkdir: (path: string) => post<{ ok: boolean; path: string }>(`/workspace/mkdir`, { path }),
  workspaceDelete: (path: string) => del<{ ok: boolean }>(`/workspace/delete?path=${encodeURIComponent(path)}`),
  workspaceUpload: async (dir: string, files: File[]) => {
    if (MOCK) return { uploaded: files.length };
    const fd = new FormData();
    for (const f of files) fd.append("file", f);
    const token = getToken();
    const r = await fetch(`/api/workspace/upload?path=${encodeURIComponent(dir)}`, {
      method: "POST",
      headers: { ...(token ? { Authorization: `Bearer ${token}` } : {}) },
      body: fd,
    });
    if (!r.ok) throw new Error(`Upload failed: ${r.status}`);
    return r.json() as Promise<{ uploaded: number }>;
  },
  workspaceDownload: async (path: string) => {
    let blob: Blob;
    if (MOCK) {
      blob = new Blob([`(demo) Sample download content for ${path}.`], { type: "text/plain" });
    } else {
      const token = getToken();
      const r = await fetch(`/api/workspace/download?path=${encodeURIComponent(path)}`, {
        headers: { ...(token ? { Authorization: `Bearer ${token}` } : {}) },
      });
      if (!r.ok) throw new Error(`Download failed: ${r.status}`);
      blob = await r.blob();
    }
    const objUrl = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = objUrl;
    a.download = path.split("/").pop() || "download";
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(objUrl);
  },

  // ---- assets ----
  // Server-side paginated: pass limit/offset, get back the page + full match total.
  assets: (type = "", limit = 50, offset = 0) =>
    get<{ count: number; total: number; assets: Asset[] }>(`/assets?type=${type}&limit=${limit}&offset=${offset}`).then(
      (r) => ({ assets: r?.assets ?? [], total: r?.total ?? r?.count ?? 0 }),
    ),
  searchAssets: (dsl: string, type = "", limit = 50, offset = 0) =>
    get<{ count: number; total: number; assets: Asset[] }>(
      `/assets?dsl=${encodeURIComponent(dsl)}${type ? `&type=${encodeURIComponent(type)}` : ""}&limit=${limit}&offset=${offset}`,
    ).then((r) => ({ assets: r?.assets ?? [], total: r?.total ?? r?.count ?? 0 })),
  assetCounts: (taskId = "") =>
    get<Record<string, number>>(`/assets/counts${taskId ? `?task_id=${encodeURIComponent(taskId)}` : ""}`),
  deleteAssets: (ids: number[]) =>
    http<{ deleted: number }>("/assets", { method: "DELETE", body: JSON.stringify({ ids }) }),
  // task-scoped view of the same endpoint — server-side paginated like `assets`
  taskAssets: (taskId: string, type = "", limit = 50, offset = 0) =>
    get<{ count: number; total: number; assets: Asset[] }>(
      `/assets?task_id=${encodeURIComponent(taskId)}&type=${encodeURIComponent(type)}&limit=${limit}&offset=${offset}`,
    ).then((r) => ({ assets: r?.assets ?? [], total: r?.total ?? r?.count ?? 0 })),
  // DSL search scoped to a task — the backend forces the task_id filter, so it
  // always stays within that task's assets (same DSL grammar as `searchAssets`).
  searchTaskAssets: (taskId: string, dsl: string, type = "", limit = 50, offset = 0) =>
    get<{ count: number; total: number; assets: Asset[] }>(
      `/assets?task_id=${encodeURIComponent(taskId)}&dsl=${encodeURIComponent(dsl)}&type=${encodeURIComponent(type)}&limit=${limit}&offset=${offset}`,
    ).then((r) => ({ assets: r?.assets ?? [], total: r?.total ?? r?.count ?? 0 })),
  attachTaskAssets: (taskId: string, assetIds: number[], sourceSummary: string) =>
    post<TaskAssetMutation>(`/tasks/${taskId}/assets`, {
      asset_ids: assetIds,
      source_summary: sourceSummary,
    }),
  registerTaskAssetScopes: (taskId: string, scope: CompanyScopeRule[]) =>
    post<TaskAssetScopeMutation>(`/tasks/${taskId}/assets`, { scope }),
  detachTaskAsset: (taskId: string, assetId: number) => del<{ detached: number }>(`/tasks/${taskId}/assets/${assetId}`),
  taskIntentAssets: (taskId: string) =>
    get<{ assets: IntentAsset[] }>(`/tasks/${taskId}/intent-assets`).then((r) => arr(r.assets)),

  // ---- companies (company + asset scope; the single source of ownership) ----
  companies: () => get<Company[]>("/companies").then(arr),
  createCompany: (name: string, scope: CompanyScopeRule[]) =>
    post<{ id: number; created: boolean; scope_added?: number; scope_invalid?: number; scope_errors?: string[] }>(
      "/companies",
      {
        name,
        scope,
      },
    ),
  addCompanyScope: (id: number, scope: CompanyScopeRule[], reason = "") =>
    post<CompanyScopeMutation>(`/companies/${id}/scope`, {
      scope,
      reason,
    }),
  updateCompanyScope: (id: number, scope: CompanyScopeRule[], reason = "") =>
    post<CompanyScopeMutation>(`/companies/${id}/scope`, {
      scope,
      reason,
      reset: true,
    }),
  deleteCompany: (id: number, deleteAssets = false) =>
    http<{ deleted: number; assets_deleted: number }>(`/companies/${id}`, {
      method: "DELETE",
      body: JSON.stringify({ delete_assets: deleteAssets }),
    }),

  // ---- exploration (per task) ----
  frontier: (task?: string) => get<TaskNode[]>(`/exploration/frontier${tq(task)}`).then(arr),
  findings: (task?: string) => get<Finding[]>(`/exploration/findings${tq(task)}`).then(arr),
  findingsPage: (q: FindingQuery) => {
    const p = findingFilterParams(q);
    p.set("page", String(q.page));
    p.set("limit", String(q.pageSize));
    return get<FindingsPage>(`/exploration/findings?${p.toString()}`);
  },
  findingGroups: (q: FindingQuery) => {
    const p = findingFilterParams(q);
    p.set("page", String(q.page));
    p.set("limit", String(q.pageSize));
    return get<FindingGroupsPage>(`/exploration/findings/groups?${p.toString()}`);
  },
  // findingAssetTree returns the left tree of the "by asset" view: only assets that have findings plus their ancestors,
  // with subtree aggregate counts on the nodes. Not paginated — the tree is navigation structure, fetched in one go.
  findingAssetTree: (q: Omit<FindingQuery, "page" | "pageSize">) =>
    get<FindingAssetTree>(`/exploration/findings/asset-tree?${findingFilterParams(q).toString()}`),
  findingStats: () => get<FindingStats>("/exploration/findings/stats"),
  // exportFindings triggers the findings export and downloads the file. scope=selected passes ids (finding_id list);
  // scope=filtered passes the current filters (FindingQuery filter fields); scope=all ignores the filters.
  exportFindings: async (opts: {
    format: "md-single" | "md-zip" | "csv" | "json";
    scope: "filtered" | "all" | "selected";
    filters?: Omit<FindingQuery, "page" | "pageSize">;
    ids?: string[];
  }) => {
    const p = new URLSearchParams({ format: opts.format, scope: opts.scope });
    if (opts.scope === "selected") {
      p.set("ids", (opts.ids ?? []).join(","));
    } else if (opts.scope === "filtered" && opts.filters) {
      for (const [k, v] of findingFilterParams(opts.filters)) p.set(k, v);
    }
    const token = getToken();
    const r = await fetch(`/api/exploration/findings/export?${p.toString()}`, {
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    });
    if (!r.ok) throw new Error(`export: ${r.status}`);
    const blob = await r.blob();
    // Prefer the backend Content-Disposition filename; fall back to the default name.
    const disp = r.headers.get("Content-Disposition") ?? "";
    const m = disp.match(/filename="?([^"]+)"?/);
    const filename = m?.[1] ?? `findings-export`;
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
  },
  getFinding: (id: string, contextTaskId?: string) =>
    get<Finding>(
      `/exploration/findings/${id}${contextTaskId ? `?context_task=${encodeURIComponent(contextTaskId)}` : ""}`,
    ),
  findingTraffic: (id: string, contextTask?: string) =>
    get<FindingTraffic>(
      `/exploration/findings/${id}/traffic${contextTask ? `?context_task=${encodeURIComponent(contextTask)}` : ""}`,
    ),
  bindFindingTraffic: (id: string, traffic_refs: TrafficEvidenceRef[], contextTask?: string) =>
    post<FindingTraffic>(
      `/exploration/findings/${id}/traffic${contextTask ? `?context_task=${encodeURIComponent(contextTask)}` : ""}`,
      { traffic_refs },
    ),
  editFindingTraffic: (
    id: string,
    bindingId: string,
    version: number,
    fields: { role: TrafficEvidenceRole; note: string },
    contextTask?: string,
  ) =>
    patch<FindingTraffic>(
      `/exploration/findings/${id}/traffic/${bindingId}${contextTask ? `?context_task=${encodeURIComponent(contextTask)}` : ""}`,
      { version, ...fields },
    ),
  removeFindingTraffic: (id: string, bindingId: string, version: number, contextTask?: string) =>
    del<FindingTraffic>(
      `/exploration/findings/${id}/traffic/${bindingId}${contextTask ? `?context_task=${encodeURIComponent(contextTask)}` : ""}`,
      { version },
    ),
  orderFindingTraffic: (id: string, binding_ids: string[], version: number, contextTask?: string) =>
    http<FindingTraffic>(
      `/exploration/findings/${id}/traffic/order${contextTask ? `?context_task=${encodeURIComponent(contextTask)}` : ""}`,
      { method: "PUT", body: JSON.stringify({ binding_ids, version }) },
    ),
  findingTrafficDetail: (id: string, bindingId: string, contextTask?: string) =>
    get<FindingTrafficDetail>(
      `/exploration/findings/${id}/traffic/${bindingId}${contextTask ? `?context_task=${encodeURIComponent(contextTask)}` : ""}`,
    ),
  findingTrafficBody: (
    id: string,
    bindingId: string,
    side: "request" | "response",
    offset: number,
    contextTask?: string,
  ) =>
    get<EvidenceBodyPreview>(
      `/exploration/findings/${id}/traffic/${bindingId}/body?side=${side}&offset=${offset}${contextTask ? `&context_task=${encodeURIComponent(contextTask)}` : ""}`,
    ),
  downloadFindingTrafficBody: async (
    id: string,
    bindingId: string,
    side: "request" | "response",
    contextTask?: string,
  ) => {
    const token = getToken();
    const response = await fetch(
      `/api/exploration/findings/${id}/traffic/${bindingId}/body?side=${side}&download=1${contextTask ? `&context_task=${encodeURIComponent(contextTask)}` : ""}`,
      { headers: token ? { Authorization: `Bearer ${token}` } : {} },
    );
    if (!response.ok) {
      const error = await response.json().catch(() => ({ error: "Download failed" }));
      throw new Error(error.error ?? "Download failed");
    }
    const blob = await response.blob();
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `evidence-${bindingId}-${side}.bin`;
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  },
  // Finding lineage: subgraph from this finding node back to the task's initial node (nodes + relations).
  findingLineage: (id: string) => get<{ nodes: TaskNode[]; edges: Edge[] }>(`/exploration/findings/${id}/lineage`),
  setFindingStatus: (id: string, status: FindingStatus) => patch<Finding>(`/exploration/findings/${id}`, { status }),
  setFindingSeverity: (id: string, severity: Severity) => patch<Finding>(`/exploration/findings/${id}`, { severity }),
  // Save a finding's name/category/severity in one call (inline row editing in the findings list); only present fields are sent.
  updateFinding: (
    id: string,
    fields: { name?: string; vulnclass?: string; severity?: Severity; status?: FindingStatus },
  ) => patch<Finding>(`/exploration/findings/${id}`, fields),
  // Delete a finding: removes the findings record + the source exploration node (so it disappears from the findings list, the task findings tab and the exploration graph at once).
  deleteFinding: (id: string) => del<{ deleted: boolean; id: number }>(`/exploration/findings/${id}`),
  findingRetests: (id: string) =>
    get<{ retests: FindingRetest[] }>(`/exploration/findings/${encodeURIComponent(id)}/retests`).then((r) =>
      arr(r.retests),
    ),
  activeFindingRetests: () =>
    get<{ retests: ActiveFindingRetest[] }>("/exploration/findings/retests/active").then((r) => arr(r.retests)),
  startFindingRetest: (id: string, notes: string) =>
    post<{ retest: FindingRetest; created: boolean }>(`/exploration/findings/${encodeURIComponent(id)}/retests`, {
      notes,
    }),
  deepenFinding: (id: string, description: string) =>
    post<FindingDeepenResponse>(`/exploration/findings/${id}/deepen`, { description }),
  intents: (task?: string) => get<TaskNode[]>(`/exploration/intents${tq(task)}`).then(arr),
  tokenStats: (task?: string) =>
    get<{ workers: TokenUsage[]; sessions: SessionTokenUsage[]; total: TokenTotal }>(
      `/exploration/tokens${tq(task)}`,
    ).then((r) => ({
      workers: arr(r.workers),
      sessions: arr(r.sessions),
      total: r.total,
    })),
  tokenDaily: (days = 30) => get<DailyTokenBucket[]>(`/tokens/daily?days=${days}`).then(arr),
  conversationTokens: () =>
    get<ConvTokenSummary[]>("/tokens/conversations")
      .then(arr)
      .catch(() => [] as ConvTokenSummary[]),
  explorationGraph: (task?: string) => get<{ nodes: TaskNode[]; edges: Edge[] }>(`/exploration/graph${tq(task)}`),
  // Broadcast board: exploration nodes paginated by creation order on the server (newest first by default).
  explorationNodes: (task: string, query: ExplorationNodeQuery = {}) => {
    const q = new URLSearchParams();
    if (task) q.set("task", task);
    q.set("page", String(query.page ?? 1));
    q.set("size", String(query.size ?? 20));
    if (query.kinds?.length) q.set("kind", query.kinds.join(","));
    if (query.states?.length) q.set("state", query.states.join(","));
    if (query.q?.trim()) q.set("q", query.q.trim());
    if (query.order === "asc") q.set("order", "asc");
    return get<ExplorationNodePage>(`/exploration/nodes?${q.toString()}`).then((r) => ({
      items: arr(r.items),
      total: r.total ?? 0,
      page: r.page ?? 1,
      size: r.size ?? query.size ?? 20,
      edges: arr(r.edges),
      refs: r.refs ?? {},
      assets: r.assets ?? {},
    }));
  },
  activity: (task?: string, opts?: { intent?: string; since?: number; limit?: number }) => {
    const q = new URLSearchParams();
    if (task) q.set("task", task);
    if (opts?.intent) q.set("intent", opts.intent);
    if (opts?.since) q.set("since", String(opts.since));
    if (opts?.limit) q.set("limit", String(opts.limit));
    return get<{ items: Activity[]; cursor: number }>(`/exploration/activity?${q.toString()}`).then((r) => ({
      items: arr(r.items),
      cursor: r.cursor ?? 0,
    }));
  },
  activityDetail: (id: number, task?: string) => get<{ detail: string }>(`/exploration/activity/${id}${tq(task)}`),
  // Reverse-paginated session history. session = "main" | "plan" | "intent:<id>".
  // before=0 → latest page; before=<id> → the older page ending before that id.
  // snapshotCursor is the TASK-level max id at query time — open the task SSE at
  // since=snapshotCursor so history (id≤cursor) and the live tail (id>cursor) meet
  // gap-free. hasMore = still-older steps exist (drives scroll-up loading).
  activityHistory: (task: string, session: string, before = 0, limit = 200) => {
    const q = new URLSearchParams({ session, limit: String(limit) });
    if (task) q.set("task", task);
    if (before > 0) q.set("before", String(before));
    return get<{ items: Activity[]; snapshot_cursor: number; earliest_cursor: number; has_more: boolean }>(
      `/exploration/activity/history?${q.toString()}`,
    ).then((r) => ({
      items: arr(r.items),
      snapshotCursor: r.snapshot_cursor ?? 0,
      earliestCursor: r.earliest_cursor ?? 0,
      hasMore: !!r.has_more,
    }));
  },
  // Paged worker (intent) session list — reaches past the legacy fixed 300 cap.
  // before=0 → newest page; before=<id> → older page. has_more = older intents exist.
  intentsPage: (task: string, before = 0, limit = 300) => {
    const q = new URLSearchParams({ limit: String(limit) });
    if (task) q.set("task", task);
    q.set("before", String(before > 0 ? before : 0));
    q.set("page", "1"); // marker so the backend returns the paged {items,has_more} shape
    return get<{ items: TaskNode[]; has_more: boolean }>(`/exploration/intents?${q.toString()}`).then((r) => ({
      items: arr(r.items),
      hasMore: !!r.has_more,
    }));
  },

  // Main-agent conversation segments of a task. Each segment is a resettable session
  // (clean transcript/context) over the same task; `current` is the writable one.
  mainSessions: (task: string) =>
    get<{ sessions: { seq: number; created_at: string }[]; current: number }>(
      `/exploration/main-sessions${tq(task)}`,
    ).then((r) => ({ sessions: arr(r.sessions), current: r.current ?? 0 })),
  // Start a fresh main-agent session segment (does not touch the task's graph/assets/goal).
  newMainSession: (task: string) =>
    post<{ seq: number; created_at: string; current: number }>(`/exploration/main-session/new${tq(task)}`),

  // ---- traffic / audit / report / chat ----
  audit: (task?: string) => get<Audit>(`/audit${tq(task)}`),
  traffic: (
    page = 0,
    size = 100,
    host = "",
    method = "",
    q = "",
    opts: {
      body?: string;
      path?: string;
      status?: string;
      respMin?: string;
      respMax?: string;
      sort?: string;
      order?: string;
    } = {},
  ) =>
    get<TrafficResp>(
      `/traffic?page=${page}&size=${size}` +
        (host ? `&host=${encodeURIComponent(host)}` : "") +
        (method && method !== "all" ? `&method=${encodeURIComponent(method)}` : "") +
        (q ? `&q=${encodeURIComponent(q)}` : "") +
        (opts.body ? `&body=${encodeURIComponent(opts.body)}` : "") +
        (opts.path ? `&path=${encodeURIComponent(opts.path)}` : "") +
        (opts.status && opts.status !== "all" ? `&status=${encodeURIComponent(opts.status)}` : "") +
        (opts.respMin ? `&resp_min=${encodeURIComponent(opts.respMin)}` : "") +
        (opts.respMax ? `&resp_max=${encodeURIComponent(opts.respMax)}` : "") +
        (opts.sort ? `&sort=${encodeURIComponent(opts.sort)}` : "") +
        (opts.order ? `&order=${encodeURIComponent(opts.order)}` : ""),
    ),
  trafficExchange: (id: string) => get<TrafficDetail>(`/traffic/exchange?id=${encodeURIComponent(id)}`),
  trafficHosts: () => get<{ hosts: TrafficHost[] }>(`/traffic/hosts`),
  trafficDeleteHost: (host: string) => del<{ deleted: number }>(`/traffic?host=${encodeURIComponent(host)}`),
  trafficDeleteHosts: (hosts: string[]) => del<{ deleted: number }>(`/traffic/hosts`, { hosts }),
  // Purges every exchange and compacts the index; `reclaimed` is the bytes of
  // index handed back to the filesystem. Evidence bound to findings is kept.
  trafficDeleteAll: () => del<{ deleted: number; reclaimed: number }>(`/traffic/all`),

  // ---- app settings (runtime toggles) ----
  settings: () => get<Settings>(`/settings`),
  setSettings: (patch: Partial<Settings>) => put<Settings>(`/settings`, patch),
  // Run a real "test" search with the given (or saved) config to verify it works.
  testWebSearch: (patch: {
    web_search_backend?: string;
    web_search_proxy?: string;
    brave_search_api_key?: string;
    tavily_search_api_key?: string;
  }) => post<{ ok: boolean; error?: string; count?: number; backend?: string }>(`/settings/web-search/test`, patch),

  // ---- finding IM push ----
  // Channels are multi-instance resources (several bots of the same type, each with its own filter rules), so they live in their own group
  // instead of being squeezed into the flat settings key/value store.
  notifyMeta: () => get<NotificationMeta>(`/notify/meta`),
  notifyChannels: () => get<{ channels: NotificationChannel[] }>(`/notify/channels`).then((r) => arr(r.channels)),
  notifyCreateChannel: (payload: {
    name: string;
    kind: string;
    enabled?: boolean;
    mode?: string;
    config: Record<string, unknown>;
    filter?: NotificationFilter;
    rate_per_min?: number;
  }) => post<{ id: number }>(`/notify/channels`, payload),
  // PATCH semantics: only the fields being changed are sent. Echoing the masked value in config back means "keep the current value".
  notifyUpdateChannel: (
    id: number,
    payload: {
      name?: string;
      kind?: string;
      enabled?: boolean;
      mode?: string;
      config?: Record<string, unknown>;
      filter?: NotificationFilter;
      rate_per_min?: number;
    },
  ) => patch<{ id: number }>(`/notify/channels/${id}`, payload),
  notifyDeleteChannel: (id: number) => del<{ ok: boolean }>(`/notify/channels/${id}`),
  // Also sends one test message; on failure the backend returns the channel's raw error for configuration debugging.
  notifyTestChannel: (id: number) => post<{ ok: boolean; latency_ms: number }>(`/notify/channels/${id}/test`),
  notifyDeliveries: (q: { channelId?: number; state?: string; page?: number; pageSize?: number } = {}) => {
    const p = new URLSearchParams();
    if (q.channelId) p.set("channel_id", String(q.channelId));
    if (q.state) p.set("state", q.state);
    p.set("page", String(q.page ?? 1));
    p.set("page_size", String(q.pageSize ?? 50));
    return get<{ deliveries: NotificationDelivery[]; total: number; page: number; page_size: number }>(
      `/notify/deliveries?${p.toString()}`,
    ).then((r) => ({ ...r, deliveries: arr(r.deliveries) }));
  },
  notifyRetryDelivery: (id: number) => post<{ ok: boolean }>(`/notify/deliveries/${id}/retry`),
  report: async (task?: string) => {
    if (MOCK) return mockReport(task);
    const token = getToken();
    const r = await fetch(`/api/report${tq(task)}`, {
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    });
    if (!r.ok) throw new Error(`report: ${r.status}`);
    return r.text();
  },
  chatMentions: (kind: string, query: string, signal?: AbortSignal, cursor = "") =>
    http<{ items: ChatMention[]; next_cursor?: string }>(
      `/chat/mentions?${new URLSearchParams({ kind, q: query, cursor })}`,
      { signal },
    ),
  chat: (message: string, task?: string, attachments?: ChatAttachment[], seg?: number) =>
    post<{ reply: string; mode: string }>(`/chat${tq(task)}`, { message, attachments, seg }),
  chatStatus: (taskId: string) => get<{ running: boolean }>(`/tasks/${taskId}/chat/status`),
  // Mode 1, file upload: lands in the session/task workdir uploads/ and returns a relative path the agent can Read.
  chatUpload: async (scope: "task" | "session" | "staging", id: string, files: File[]) => {
    if (MOCK)
      return {
        attachments: files.map((f) => ({
          name: f.name,
          path: `uploads/${f.name}`,
          size: f.size,
          abs: `/mock/${f.name}`,
        })),
      };
    const fd = new FormData();
    for (const f of files) fd.append("file", f);
    const token = getToken();
    const r = await fetch(`/api/chat/upload?scope=${scope}&id=${encodeURIComponent(id)}`, {
      method: "POST",
      headers: { ...(token ? { Authorization: `Bearer ${token}` } : {}) },
      body: fd,
    });
    if (!r.ok) throw new Error(`Upload failed: ${r.status} ${await r.text()}`);
    return r.json() as Promise<{ attachments: ChatAttachment[] }>;
  },
  stopChat: (taskId: string) => post<{ status: string }>(`/tasks/${taskId}/chat/stop`, {}),
  gc: (ttl = 86400) => post<{ removed: number }>(`/gc?ttl=${ttl}`, {}),

  // ---- LLM ----
  getLLM: () =>
    get<{
      configured: boolean;
      provider: string;
      model: string;
      base_url: string;
      proxy?: string;
      key_set: boolean;
      rate_per_second?: number;
      rate_per_minute?: number;
      context_window_k?: number;
      thinking_type?: string;
      reasoning_effort?: string;
    }>("/llm"),
  setLLM: (
    provider: string,
    model: string,
    base_url: string,
    api_key: string,
    rate_per_second = 0,
    rate_per_minute = 0,
    proxy = "",
    context_window_k = 0,
  ) => post("/llm", { provider, model, base_url, proxy, api_key, rate_per_second, rate_per_minute, context_window_k }),
  testLLM: (
    provider: string,
    model: string,
    base_url: string,
    api_key: string,
    proxy = "",
    thinking_type = "",
    reasoning_effort = "",
    profile_id?: number,
    streaming = true, // test with the profile's real streaming mode, so a "streaming works, non-streaming does not" profile never slips into a session
    session_header_key = "", // non-empty = the test request also carries this custom session header (value = one-off session id)
  ) =>
    // reply = the model's actual reply (already truncated); a profile that returns nothing at all is failed by the backend
    post<{ ok: boolean; error?: string; latency_ms?: number; model?: string; reply?: string }>("/llm/test", {
      provider,
      model,
      base_url,
      proxy,
      api_key,
      thinking_type,
      reasoning_effort,
      profile_id,
      streaming,
      session_header_key,
    }),
  llmProfiles: () => get<{ profiles: LLMProfile[] }>("/llm/profiles").then((r) => arr(r.profiles)),
  saveLLMProfile: (p: {
    id?: number; // omit/0 = create; set = update that profile
    name: string;
    format: string;
    model: string;
    base_url?: string;
    proxy?: string;
    api_key?: string; // blank on update keeps the existing key
    rate_per_second?: number;
    rate_per_minute?: number;
    context_window_k?: number;
    thinking_type?: string; // "" (not sent) | "disabled" | "enabled"
    reasoning_effort?: string; // "" (not sent) | "low" | "medium" | "high" | "xhigh" | "max"
    priority?: number; // polling rank, higher is tried first
    pool_exclude?: boolean; // true = never used as a failover target
    streaming?: boolean; // true (default) = streaming | false = non-streaming
    max_tokens?: number; // output limit per reply; 0 = not sent, the server default decides
    max_tokens_field?: string; // "" = max_tokens (default) | "max_completion_tokens" (openai format only)
    session_header_key?: string; // non-empty = send this HTTP header on every request with the current session id as value; "" = not sent
    retry?: LLMRetryOverride; // retry override for this profile; 0 in every field = follow the global retry policy
  }) => post<{ id: number }>("/llm/profiles", p),
  deleteLLMProfile: (id: string) => del<{ deleted: number }>(`/llm/profiles/${id}`),
  activateLLMProfile: (id: string) => post<{ ok: boolean }>("/llm/profiles/active", { id: Number(id) }),
  // Effective order of the polling chain + circuit breaker state of each profile.
  llmPool: () => get<LLMPoolStatus>("/llm/pool"),
  // Clear the breaker so the next call retries this profile immediately; omit id to clear all.
  resetLLMPool: (id?: string) => post<LLMPoolStatus>("/llm/pool/reset", { id: id ? Number(id) : 0 }),
  // Global retry policy (attempts + interval for each of the five layers). All 0 = built-in defaults everywhere.
  llmRetryPolicy: () => get<LLMRetryPolicy>("/llm/retry-policy"),
  saveLLMRetryPolicy: (p: LLMRetryPolicy) => post<LLMRetryPolicy>("/llm/retry-policy", p),
  fetchLLMModels: (provider: string, base_url: string, api_key: string, proxy = "", profile_id?: number) =>
    post<{ ok: boolean; error?: string; models?: string[] }>("/llm/models", {
      provider,
      base_url,
      api_key,
      proxy,
      profile_id,
    }),

  // ---- agents ----
  agents: () => get<{ agents: Agent[] }>("/agents").then((r) => arr(r.agents)),
  getAgent: (key: string) => get<AgentDetail>(`/agents/${key}`),
  createAgent: (key: string, name: string, description = "") => post<Agent>("/agents", { key, name, description }),
  updateAgent: (key: string, name: string, description = "") =>
    patch<{ ok: boolean }>(`/agents/${key}`, { name, description }),
  deleteAgent: (key: string) => del<{ deleted: string }>(`/agents/${key}`),

  // ---- conversations (chat page) ----
  conversations: () => get<{ conversations: Conversation[] }>("/conversations").then((r) => arr(r.conversations)),
  createConversation: (agent_key: string, title = "", llm_profile_id?: number | null) =>
    post<Conversation>("/conversations", { agent_key, title, llm_profile_id: llm_profile_id ?? null }),
  updateConversation: (id: number, input: { title?: string; pinned?: boolean }) =>
    patch<Conversation>(`/conversations/${id}`, input),
  renameConversation: (id: number, title: string) => patch<Conversation>(`/conversations/${id}`, { title }),
  pinConversation: (id: number, pinned: boolean) => patch<Conversation>(`/conversations/${id}`, { pinned }),
  updateConversationProfile: (id: number, llm_profile_id: number | null) =>
    patch<{ ok: boolean }>(`/conversations/${id}/profile`, { llm_profile_id }),
  deleteConversation: (id: number) => del<{ deleted: number }>(`/conversations/${id}`),
  deleteConversations: (ids: number[]) =>
    post<{ items: { id: number; ok: boolean; error?: string }[] }>("/conversations/delete/batch", { ids }),
  // Incremental tail: steps after `since` (id ASC) — live poll + post-send fetch.
  conversationMessages: (id: number, since = 0) =>
    get<{ items: Activity[]; cursor: number; running: boolean }>(`/conversations/${id}/messages?since=${since}`).then(
      (r) => ({ items: arr(r.items), cursor: r.cursor ?? 0, running: !!r.running }),
    ),
  // Reverse pagination: the latest `limit` steps (before=0) or the page of older
  // steps ending before id `before`. hasMore = still-older steps exist.
  conversationHistory: (id: number, before = 0, limit = 200) => {
    const q = new URLSearchParams({ limit: String(limit) });
    if (before > 0) q.set("before", String(before));
    return get<{ items: Activity[]; cursor: number; running: boolean; hasMore: boolean }>(
      `/conversations/${id}/messages?${q.toString()}`,
    ).then((r) => ({ items: arr(r.items), cursor: r.cursor ?? 0, running: !!r.running, hasMore: !!r.hasMore }));
  },
  conversationMsgDetail: (id: number, seq: number) =>
    get<{ detail: string }>(`/conversations/${id}/messages/${seq}`).then((r) => r.detail ?? ""),
  sendConversationMessage: (id: number, message: string, attachments?: ChatAttachment[]) =>
    post<{ status: string }>(`/conversations/${id}/messages`, { message, attachments }),
  stopConversation: (id: number) => post<{ status: string }>(`/conversations/${id}/stop`, {}),
  saveAgentPrompt: (key: string, template: string, note = "") =>
    put<{ version: number }>(`/agents/${key}/prompt`, { template, note }),
  resetAgentPrompt: (key: string) => post<{ version: number }>(`/agents/${key}/prompt/reset`, {}),
  // Settlement prompt (used when a timeout or step budget runs out); an empty prompt = clear the override and use the built-in default;
  // omitting max_turns keeps it untouched, 0 = use the built-in default turn count
  saveAgentWrapup: (key: string, prompt: string, maxTurns?: number) =>
    put<{ ok: boolean }>(`/agents/${key}/wrapup`, { prompt, max_turns: maxTurns }),
  resetAgentWrapup: (key: string) =>
    post<{ ok: boolean; wrapup_default: string; wrapup_max_turns_default: number }>(`/agents/${key}/wrapup/reset`, {}),
  // Task-level settlement prompt (worker/planner only); empty prompt = clear and use the built-in default
  saveAgentTaskTimeoutWrapup: (key: string, prompt: string, maxTurns?: number) =>
    put<{ ok: boolean }>(`/agents/${key}/wrapup/task-timeout`, { prompt, max_turns: maxTurns }),
  resetAgentTaskTimeoutWrapup: (key: string) =>
    post<{ ok: boolean; task_timeout_wrapup_default: string; task_timeout_wrapup_max_turns_default: number }>(
      `/agents/${key}/wrapup/task-timeout/reset`,
      {},
    ),
  // P3 triggers (custom agents only)
  agentTriggers: (key: string) =>
    get<{ triggers: AgentTrigger[] }>(`/agents/${key}/triggers`).then((r) => arr(r.triggers)),
  createTrigger: (key: string, t: Omit<AgentTrigger, "id" | "agent_key" | "last_fire">) =>
    post<AgentTrigger>(`/agents/${key}/triggers`, t),
  updateTrigger: (id: number, t: Omit<AgentTrigger, "id" | "agent_key" | "last_fire">) =>
    patch<{ ok: boolean }>(`/triggers/${id}`, t),
  deleteTrigger: (id: number) => del<{ deleted: number }>(`/triggers/${id}`),
  saveAgentConfig: (
    key: string,
    patch: {
      llm_profile_id?: number | null; // number = bind; null = unbind (follow task/global); omitted = leave unchanged
      max_turns?: number;
      run_seconds?: number;
      web_search?: boolean;
      interactive_shell?: boolean;
      trigger_run_mode?: "serial" | "parallel";
      trigger_merge_mode?: "by_task" | "all" | "none";
      trigger_max_parallel?: number;
    },
  ) => put<{ ok: boolean }>(`/agents/${key}/config`, patch),
  agentPromptVersions: (key: string) =>
    get<{ versions: PromptVersion[] }>(`/agents/${key}/prompts`).then((r) => arr(r.versions)),
  agentVariables: (key: string) =>
    get<{ variables: PromptVar[] }>(`/agents/${key}/variables`).then((r) => arr(r.variables)),
  previewAgentPrompt: (key: string, template: string, sample?: Record<string, string>) =>
    post<{ rendered: string; error?: string }>(`/agents/${key}/prompt/preview`, { template, sample }),
  getAgentVisibility: (key: string) => get<{ mcp: number[]; skill: string[] }>(`/agents/${key}/visibility`),
  setAgentVisibility: (key: string, mcp: number[], skill: string[]) =>
    put<{ ok: boolean }>(`/agents/${key}/visibility`, { mcp, skill }),

  // ---- tools (built-in tool catalog) ----
  tools: () => get<{ tools: Tool[] }>("/tools").then((r) => arr(r.tools)),
  saveTool: (key: string, patch: Pick<Tool, "description" | "schema" | "agents" | "enabled">) =>
    put<{ ok: boolean }>(`/tools/${key}`, patch),
  resetTool: (key: string) => post<{ ok: boolean }>(`/tools/${key}/reset`, {}),
  // custom tools
  createCustomTool: (
    t: Pick<Tool, "key" | "description" | "schema" | "agents" | "enabled" | "kind" | "exec" | "deferred">,
  ) => post<{ key: string }>("/tools/custom", t),
  updateCustomTool: (
    key: string,
    t: Pick<Tool, "description" | "schema" | "agents" | "enabled" | "kind" | "exec" | "deferred">,
  ) => put<{ ok: boolean }>(`/tools/custom/${key}`, t),
  deleteCustomTool: (key: string) => del<{ deleted: string }>(`/tools/custom/${key}`),
  testCustomTool: (body: { kind: string; exec: Record<string, unknown>; params: Record<string, unknown> }) =>
    post<{ output: string; is_error: boolean }>("/tools/custom/test", body),
  detectPython: () => post<{ python_interpreter: string }>("/settings/python/detect", {}),

  // ---- mcp ----
  mcpServers: () => get<{ servers: MCPServer[] }>("/mcp").then((r) => arr(r.servers)),
  saveMcpServer: (m: Partial<MCPServer>) => post<{ id: number }>("/mcp", m),
  deleteMcpServer: (id: number) => del<{ deleted: number }>(`/mcp/${id}`),
  mcpTools: (id: number) => get<{ tools: MCPTool[] }>(`/mcp/${id}/tools`).then((r) => arr(r.tools)),
  refreshMcpServer: (id: number) => post<{ tools: MCPTool[] }>(`/mcp/${id}/refresh`, {}).then((r) => arr(r.tools)),

  // ---- asset sync (ScopeSentry data source) ----
  ssStatus: () =>
    get<{ exists: boolean; configured: boolean; enabled: boolean; reachable: boolean; url?: string; tools: string[] }>(
      "/sync/scopesentry/status",
    ),
  ssDatasource: (body: { url?: string; api_key?: string }) =>
    post<{ id: number; enabled: boolean }>("/sync/scopesentry/datasource", body),
  ssProjects: (page = 1, size = 50, search = "") =>
    get<{ projects: SSProject[]; tag: Record<string, number> }>(
      `/sync/scopesentry/projects?page=${page}&size=${size}${search ? `&search=${encodeURIComponent(search)}` : ""}`,
    ).then((r) => ({ projects: arr(r.projects), tag: r.tag ?? {} })),
  ssTasks: (page = 1, size = 50, search = "") =>
    get<{ tasks: SSTask[] }>(
      `/sync/scopesentry/tasks?page=${page}&size=${size}${search ? `&search=${encodeURIComponent(search)}` : ""}`,
    ).then((r) => arr(r.tasks)),
  ssSync: (body: {
    dimension: "project" | "task";
    targets: string[];
    asset_types: string[];
    create_company?: boolean;
    page_size?: number;
  }) =>
    post<{
      synced: Record<string, number>;
      companies: string[] | null;
      warnings: string[] | null;
      errors: string[] | null;
    }>("/sync/scopesentry/sync", body),

  // ---- skills (filesystem) ----
  skills: () => get<{ skills: SkillItem[] }>("/skills").then((r) => arr(r.skills)),
  createSkill: (s: {
    name: string;
    description: string;
    license?: string;
    compatibility?: string;
    mcps?: string[];
    instructions?: string;
  }) => post<{ name: string }>("/skills", s),
  // uploadSkill installs a skill from a .zip (multipart). Surfaces the backend
  // error text (e.g. "The skill already exists" / "SKILL.md was not found") so the UI can show a precise message.
  uploadSkill: async (file: File, overwrite = false): Promise<{ name: string; files: number }> => {
    if (MOCK) return { name: file.name.replace(/\.zip$/i, ""), files: 1 };
    const fd = new FormData();
    fd.append("file", file);
    const token = getToken();
    const r = await fetch(`/api/skills/upload${overwrite ? "?overwrite=true" : ""}`, {
      method: "POST",
      body: fd,
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    });
    const body = await r.json().catch(() => ({}));
    if (!r.ok) throw new ApiError(body?.error || `Upload failed (${r.status})`, r.status);
    return body;
  },
  deleteSkill: (name: string) => del<{ deleted: string }>(`/skills/${name}`),
  updateSkillMeta: (
    name: string,
    meta: { mcps?: string[]; description?: string; license?: string; compatibility?: string },
  ) => put<{ ok: boolean }>(`/skills/${name}/meta`, meta),
  createSkillDir: (skill: string, path: string) => post<{ dir: string }>(`/skills/${skill}/dirs`, { path }),
  skillFiles: (name: string) => get<{ files: string[] }>(`/skills/${name}/files`).then((r) => r.files),
  readSkillFile: (name: string, file: string) =>
    get<{ content: string; file: string }>(`/skills/${name}/files/${file}`).then((r) => r.content),
  writeSkillFile: (name: string, file: string, content: string) =>
    put<{ ok: boolean }>(`/skills/${name}/files/${file}`, { content }),
  deleteSkillPath: (skill: string, path: string) => del<{ deleted: string }>(`/skills/${skill}/files/${path}`),
  skillUsage: (name: string, limit = 50) =>
    get<{ calls: SkillCall[] }>(`/skills/${name}/usage?limit=${limit}`).then((r) => arr(r.calls)),
  missingSkills: (limit = 20) =>
    get<{ missing: MissingSkill[] }>(`/skills/missing?limit=${limit}`).then((r) => arr(r.missing)),

  // ---- visibility (MCP resource side) ---- (agent ids are strings per spec)
  resourceVisibility: (kind: string, id: number) =>
    get<{ agents: string[] }>(`/visibility/${kind}/${id}`).then((r) => arr(r.agents)),
  toggleVisibility: (agentId: string, kind: string, resourceId: number, visible: boolean) =>
    post<{ ok: boolean }>("/visibility/toggle", { agent_id: agentId, kind, resource_id: resourceId, visible }),

  // ---- visibility (skill, by name) ----
  skillVisibility: (name: string) => get<{ agents: string[] }>(`/visibility/skill/${name}`).then((r) => arr(r.agents)),
  toggleSkillVisibility: (agentId: string, skillName: string, visible: boolean) =>
    post<{ ok: boolean }>("/visibility/skill/toggle", { agent_id: agentId, skill_name: skillName, visible }),

  // ---- intercept rules ----
  interceptRules: () => get<{ rules: InterceptRule[] }>("/intercept/rules").then((r) => arr(r.rules)),
  createInterceptRule: (rule: Omit<InterceptRule, "id" | "created_at" | "updated_at">) =>
    post<InterceptRule>("/intercept/rules", rule),
  updateInterceptRule: (id: number, rule: Omit<InterceptRule, "id" | "created_at" | "updated_at">) =>
    put<InterceptRule>(`/intercept/rules/${id}`, rule),
  deleteInterceptRule: (id: number) => del<{ deleted: number }>(`/intercept/rules/${id}`),
  toggleInterceptRule: (id: number, enabled: boolean) =>
    post<{ ok: boolean; enabled: boolean }>(`/intercept/rules/${id}/toggle`, { enabled }),

  // ---- asset intercept rules (asset intercept: global blacklist) ----
  assetInterceptRules: () => get<{ rules: AssetInterceptRule[] }>("/asset-intercept/rules").then((r) => arr(r.rules)),
  createAssetInterceptRule: (rule: Pick<AssetInterceptRule, "enabled" | "kind" | "pattern" | "note">) =>
    post<AssetInterceptRule>("/asset-intercept/rules", rule),
  updateAssetInterceptRule: (id: number, rule: Pick<AssetInterceptRule, "enabled" | "kind" | "pattern" | "note">) =>
    put<AssetInterceptRule>(`/asset-intercept/rules/${id}`, rule),
  deleteAssetInterceptRule: (id: number) => del<{ deleted: number }>(`/asset-intercept/rules/${id}`),
  toggleAssetInterceptRule: (id: number, enabled: boolean) =>
    post<{ ok: boolean; enabled: boolean }>(`/asset-intercept/rules/${id}/toggle`, { enabled }),

  // ---- intercept pending (ask) ----
  interceptPending: () => get<{ pending: InterceptPending[] }>("/intercept/pending").then((r) => arr(r.pending)),
  interceptGetOne: (id: number) => get<InterceptPending>(`/intercept/pending/${id}`),
  interceptDecide: (id: number, decision: "allowed" | "denied") =>
    post<{ ok: boolean }>(`/intercept/pending/${id}/decide`, { decision }),
  interceptExecution: (id: number, conversationId?: number) =>
    get<import("@/lib/types").InterceptExecution>(
      `/intercept/history/${id}/execution${conversationId ? `?conversation=${conversationId}` : ""}`,
    ),
  interceptDetail: (id: number) => get<InterceptDetail>(`/intercept/history/${id}`),
  interceptHistory: () => get<{ items: InterceptApprovalRow[] }>("/intercept/history").then((r) => arr(r.items)),
  interceptHistoryPage: (page = 1, size = 20, filter: InterceptApprovalFilter = {}) =>
    get<{ items: InterceptApprovalRow[]; total?: number }>(
      `/intercept/history?${interceptPageQuery(page, size, filter)}`,
    ).then((r) => ({
      items: arr(r.items),
      total: r.total ?? r.items?.length ?? 0,
    })),
  interceptTask: (taskId: string) =>
    get<{ items: InterceptApprovalRow[] }>(`/intercept/task/${taskId}`).then((r) => arr(r.items)),
  interceptTaskPage: (taskId: string, page = 1, size = 20, filter: InterceptApprovalFilter = {}) =>
    get<{ items: InterceptApprovalRow[]; total?: number }>(
      `/intercept/task/${encodeURIComponent(taskId)}?${interceptPageQuery(page, size, filter)}`,
    ).then((r) => ({
      items: arr(r.items),
      total: r.total ?? r.items?.length ?? 0,
    })),

  // ---- intercept tool-config (global tool intercept scope) ----
  interceptGetToolConfig: async (): Promise<{ enabled_tools: string[] }> => {
    if (MOCK) return { enabled_tools: ["bash"] };
    const token = getToken();
    const r = await fetch("/api/intercept/tool-config", {
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    });
    if (!r.ok) throw new Error(await r.text());
    return r.json();
  },
  interceptSetToolConfig: async (enabledTools: string[]): Promise<void> => {
    if (MOCK) return;
    const token = getToken();
    const r = await fetch("/api/intercept/tool-config", {
      method: "PUT",
      headers: {
        "Content-Type": "application/json",
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      body: JSON.stringify({ enabled_tools: enabledTools }),
    });
    if (!r.ok) throw new Error(await r.text());
  },

  // ---- intercept LLM judge (model fallback approval, global config) ----
  interceptGetJudgeConfig: () => get<JudgeConfig>("/intercept/judge"),
  interceptSetJudgeConfig: (cfg: JudgeConfig) => put<{ ok: boolean }>("/intercept/judge", cfg),
  interceptJudgeUsage: (days = 30) => get<JudgeUsage>(`/intercept/judge/usage?days=${days}`),

  // ---- commands (tool execution history, any tool) ----
  commands: (params?: { task?: string; q?: string; page?: number; size?: number }) => {
    const sp = new URLSearchParams();
    if (params?.task) sp.set("task", params.task);
    if (params?.q) sp.set("q", params.q);
    sp.set("page", String(params?.page ?? 0));
    sp.set("size", String(params?.size ?? 50));
    return get<{ commands: CommandRecord[]; total: number }>(`/commands?${sp}`);
  },
  // Call count per tool; reuses the list's task/q filters, and counts the whole result set rather than the current page.
  commandStats: (params?: { task?: string; q?: string }) => {
    const sp = new URLSearchParams();
    if (params?.task) sp.set("task", params.task);
    if (params?.q) sp.set("q", params.q);
    return get<{ stats: ToolStat[] }>(`/commands/stats?${sp}`);
  },

  // ---- LLM records ----
  llmRecords: (params?: { model?: string; session?: string; task?: string; page?: number; size?: number }) => {
    const sp = new URLSearchParams();
    if (params?.model) sp.set("model", params.model);
    if (params?.session) sp.set("session", params.session);
    if (params?.task) sp.set("task", params.task);
    if (params?.page !== undefined) sp.set("page", String(params.page));
    sp.set("size", String(params?.size ?? 50));
    return get<{ records: LLMRecordItem[]; total: number }>(`/llm/records?${sp}`);
  },
  llmRecordDetail: (id: number) => get<LLMRecordDetail>(`/llm/records/${id}`),
  llmTasks: () => get<{ tasks: LLMTask[] }>(`/llm/records/tasks`),
  llmRecordsDeleteTask: (task: string) => del<{ deleted: number }>(`/llm/records?task=${encodeURIComponent(task)}`),
  // Token usage of this task aggregated by model (from the always-on llm_usage ledger, exact per call,
  // covering per-agent bindings / polling / interrupted consumption).
  tokensByModel: (task: string) =>
    get<{ models: ModelTokenStat[] }>(`/llm/records/by-model?task=${encodeURIComponent(task)}`),

  // ---- one-click update ----
  // The check is authoritative on the backend: the download happens there, and a browser that can reach GitHub while the server cannot is common
  // (server on an intranet, proxy configured only in the browser), where updating would necessarily fail.
  // The backend caches GitHub query results for 30 minutes (the unauthenticated GitHub API allows 60 requests/hour/IP and the
  // top bar queries on every full page load, so without that cache the quota would be exhausted quickly). force=true bypasses the cache,
  // for when the user explicitly clicks "Check for updates".
  checkUpdate: (force = false) => get<UpdateCheck>(`/update/check${force ? "?force=1" : ""}`),
  // Returns at 202; the actual download runs in the background and progress is streamed over /api/update/stream.
  applyUpdate: () => post<{ ok: boolean; target: string }>(`/update/apply`),
  rollbackUpdate: () => post<{ ok: boolean }>(`/update/rollback`),
};
