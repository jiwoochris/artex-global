"use client";

import * as React from "react";

import Link from "next/link";

import {
  ArrowUpRightIcon,
  BugIcon,
  ChevronRightIcon,
  ClockIcon,
  DownloadIcon,
  InfoIcon,
  SearchIcon,
  ShieldAlertIcon,
  TriangleAlertIcon,
} from "lucide-react";
import { useTranslations } from "next-intl";
import { toast } from "sonner";

import { FindingRetestDialog } from "@/components/finding-retest-dialog";
import { StatusBadge } from "@/components/status-badge";
import { TablePagination } from "@/components/table-pagination";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { api } from "@/lib/api";
import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";
import type {
  ActiveFindingRetest,
  Finding,
  FindingAssetNode,
  FindingGroup,
  FindingStats,
  FindingStatus,
  Severity,
} from "@/lib/types";
import { cn, firstNonEmpty } from "@/lib/utils";

import { AssetTree, assetPathOf } from "./_components/asset-tree";
import {
  FINDING_STATUSES,
  type FindingEdit,
  type FindingReport,
  FindingsTable,
  findingRowKey,
  fmtTime,
  isSameFinding,
  SEVERITIES,
  UNASSIGNED_TASK,
} from "./_components/findings-table";

const FINDING_LIST_PREFERENCE_KEY = "artex_finding_list_preferences";

// List view: flat = one table across tasks (default); grouped = collapsed groups per task;
// asset = asset tree on the left + the findings under that subtree on the right.
type FindingView = "flat" | "grouped" | "asset";

const FINDING_VIEWS: FindingView[] = ["flat", "grouped", "asset"];

// One-shot snapshot of the asset tree. Unlike the other two views, the asset view does not poll: it re-queries
// only when the view is entered, a filter changes, or a finding is mutated on this page.
interface AssetTreeState {
  nodes: FindingAssetNode[];
  findingTotal: number;
  truncated: boolean;
  droppedKinds: string[];
  loaded: boolean;
  loading: boolean;
}

const EMPTY_ASSET_TREE: AssetTreeState = {
  nodes: [],
  findingTotal: 0,
  truncated: false,
  droppedKinds: [],
  loaded: false,
  loading: false,
};

// In the grouped view every expanded task group carries its own pagination state; they are independent of each other.
interface GroupFindingsState {
  items: Finding[];
  total: number;
  page: number;
  pageSize: number;
  loaded: boolean;
  loading: boolean;
}

// The flat view's page number lives in its own state (not in the snapshot), so a filter change resets it and triggers the reload together.
interface FlatFindingsState {
  items: Finding[];
  total: number;
  loaded: boolean;
  loading: boolean;
}

const EMPTY_FLAT_STATE: FlatFindingsState = { items: [], total: 0, loaded: false, loading: false };

function findingGroupKey(group: FindingGroup) {
  return group.task_id === null ? UNASSIGNED_TASK : String(group.task_id);
}

const EMPTY_STATS: FindingStats = {
  total: 0,
  pending: 0,
  critical: 0,
  high: 0,
  medium: 0,
  low: 0,
  vulnclasses: [],
  tasks: [],
};

export default function FindingsPage() {
  const t = useTranslations("findings");
  const tStatus = useTranslations("status");
  const [view, setView] = React.useState<FindingView>("flat");
  const [severity, setSeverity] = React.useState<"all" | Severity>("all");
  const [status, setStatus] = React.useState<"all" | FindingStatus>("all");
  const [vulnclass, setVulnclass] = React.useState<string>("all");
  const [task, setTask] = React.useState<string>("all");
  const [sort, setSort] = React.useState<"severity" | "time">("severity");
  const [search, setSearch] = React.useState("");
  const [query, setQuery] = React.useState("");
  const [expanded, setExpanded] = React.useState<string | null>(null);
  const [flat, setFlat] = React.useState<FlatFindingsState>(EMPTY_FLAT_STATE);
  const [flatPage, setFlatPage] = React.useState(1);
  const [flatPageSize, setFlatPageSize] = React.useState(20);
  const [assetTree, setAssetTree] = React.useState<AssetTreeState>(EMPTY_ASSET_TREE);
  const [assetScope, setAssetScope] = React.useState<string | null>(null);
  const [groups, setGroups] = React.useState<FindingGroup[]>([]);
  const [groupTotal, setGroupTotal] = React.useState(0);
  const [expandedGroups, setExpandedGroups] = React.useState<Set<string>>(() => new Set());
  const [groupFindings, setGroupFindings] = React.useState<Record<string, GroupFindingsState>>({});
  const [total, setTotal] = React.useState(0);
  const [stats, setStats] = React.useState<FindingStats>(EMPTY_STATS);
  const [statsLoaded, setStatsLoaded] = React.useState(false);
  const [preferencesHydrated, setPreferencesHydrated] = React.useState(false);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(10);
  const [deepenFinding, setDeepenFinding] = React.useState<Finding | null>(null);
  const [retestFinding, setRetestFinding] = React.useState<Finding | null>(null);
  const [activeRetests, setActiveRetests] = React.useState<Record<string, ActiveFindingRetest>>({});
  const activeRetestFingerprint = Object.values(activeRetests)
    .map((item) => item.id)
    .join(",");
  const retestRefreshVersion = React.useRef(0);
  const [deepenDescription, setDeepenDescription] = React.useState("");
  const [deepening, setDeepening] = React.useState(false);
  const filterFingerprint = JSON.stringify([severity, status, vulnclass, task, sort, query]);
  const activeFilterFingerprint = React.useRef(filterFingerprint);
  activeFilterFingerprint.current = filterFingerprint;

  // One lightweight request covers all rows and views, avoiding a full retest history per row; the next poll waits for the previous round to finish.
  React.useEffect(() => {
    let disposed = false;
    let failed = false;
    let timer: ReturnType<typeof setTimeout>;
    async function refreshRetests() {
      const version = retestRefreshVersion.current;
      try {
        const rows = await api.activeFindingRetests();
        if (disposed || version !== retestRefreshVersion.current) return;
        setActiveRetests(Object.fromEntries(rows.map((item) => [item.finding_id, item])));
        failed = false;
      } catch (error) {
        if (!disposed && !failed) toast.error(t("toast.retestStatusFailed", { error: (error as Error).message }));
        failed = true;
      } finally {
        if (!disposed) timer = setTimeout(() => void refreshRetests(), 3000);
      }
    }
    void refreshRetests();
    return () => {
      disposed = true;
      clearTimeout(timer);
    };
  }, [t]);

  React.useEffect(() => {
    const raw = getLocalStorageValue(FINDING_LIST_PREFERENCE_KEY);
    if (raw) {
      try {
        const parsed = JSON.parse(raw) as {
          view?: unknown;
          severity?: unknown;
          status?: unknown;
          vulnclass?: unknown;
          task?: unknown;
          sort?: unknown;
        };
        if (FINDING_VIEWS.includes(parsed.view as FindingView)) setView(parsed.view as FindingView);
        if (parsed.severity === "all" || SEVERITIES.includes(parsed.severity as Severity)) {
          setSeverity(parsed.severity as "all" | Severity);
        }
        if (parsed.status === "all" || FINDING_STATUSES.includes(parsed.status as FindingStatus)) {
          setStatus(parsed.status as "all" | FindingStatus);
        }
        if (typeof parsed.vulnclass === "string" && parsed.vulnclass) setVulnclass(parsed.vulnclass);
        if (typeof parsed.task === "string" && parsed.task) setTask(parsed.task);
        if (parsed.sort === "severity" || parsed.sort === "time") setSort(parsed.sort);
      } catch {
        // Ignore malformed or legacy preferences and retain the defaults.
      }
    }
    setPreferencesHydrated(true);
  }, []);

  React.useEffect(() => {
    if (!preferencesHydrated) return;
    setLocalStorageValue(
      FINDING_LIST_PREFERENCE_KEY,
      JSON.stringify({ view, severity, status, vulnclass, task, sort }),
    );
  }, [preferencesHydrated, severity, sort, status, task, view, vulnclass]);

  React.useEffect(() => {
    const timer = window.setTimeout(() => setQuery(search.trim()), 300);
    return () => window.clearTimeout(timer);
  }, [search]);

  // setFindings rewrites the same finding in both view caches, so switching views never shows stale state.
  const setFindings = React.useCallback((update: (current: Finding[]) => Finding[]) => {
    setFlat((current) => ({ ...current, items: update(current.items) }));
    setGroupFindings((current) => {
      const next: Record<string, GroupFindingsState> = {};
      for (const [key, state] of Object.entries(current)) {
        next[key] = { ...state, items: update(state.items) };
      }
      return next;
    });
  }, []);

  // Bulk export selection: keyed by finding_id (standalone table id) and kept across pages.
  const [selectedIds, setSelectedIds] = React.useState<Set<string>>(() => new Set());
  // Export dialog state: scope (current filters / all / selected) × format (md single file / md split zip / csv / json).
  const [exportOpen, setExportOpen] = React.useState(false);
  const [exportScope, setExportScope] = React.useState<"filtered" | "all" | "selected">("filtered");
  const [exportFormat, setExportFormat] = React.useState<"md-single" | "md-zip" | "csv" | "json">("md-single");
  const [exporting, setExporting] = React.useState(false);

  const toggleSelected = React.useCallback((id: string, checked: boolean) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  }, []);

  const toggleSelectedPage = React.useCallback((ids: string[], checked: boolean) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      for (const id of ids) {
        if (checked) next.add(id);
        else next.delete(id);
      }
      return next;
    });
  }, []);

  // Opening the export dialog defaults the scope to "selected" when rows are checked, otherwise to "current filters".
  function openExport() {
    setExportScope(selectedIds.size > 0 ? "selected" : "filtered");
    setExportOpen(true);
  }

  async function doExport() {
    setExporting(true);
    try {
      await api.exportFindings({
        format: exportFormat,
        scope: exportScope,
        filters: { severity, status, vulnclass, task, query, sort },
        ids: [...selectedIds],
      });
      setExportOpen(false);
      toast.success(t("toast.exportStarted"));
    } catch (e) {
      toast.error(t("toast.exportFailed", { error: (e as Error).message }));
    } finally {
      setExporting(false);
    }
  }

  const flatRequest = React.useRef(0);
  const assetTreeRequest = React.useRef(0);
  const groupRequests = React.useRef<Record<string, number>>({});
  const groupsRequest = React.useRef(0);
  const flatStateRef = React.useRef(flat);
  const expandedGroupsRef = React.useRef(expandedGroups);
  const groupFindingsRef = React.useRef(groupFindings);
  const visibleGroupKeysRef = React.useRef<Set<string>>(new Set());
  flatStateRef.current = flat;
  expandedGroupsRef.current = expandedGroups;
  groupFindingsRef.current = groupFindings;
  visibleGroupKeysRef.current = new Set(groups.map(findingGroupKey));

  // The asset view's right-hand list = flat list + selected subtree filter, so both views share one list state.
  const activeAssetScope = view === "asset" ? assetScope : null;

  // loadFlat fetches the current page of the flat view; the task filter is applied by the backend and shared with the grouped view.
  const loadFlat = React.useCallback(async () => {
    const requestFilter = filterFingerprint;
    if (activeFilterFingerprint.current !== requestFilter) return;
    const request = ++flatRequest.current;
    setFlat((current) => ({ ...current, loading: true }));
    try {
      const result = await api.findingsPage({
        page: flatPage,
        pageSize: flatPageSize,
        severity,
        status,
        vulnclass,
        task,
        query,
        sort,
        assetScope: activeAssetScope ?? undefined,
      });
      if (request !== flatRequest.current || activeFilterFingerprint.current !== requestFilter) return;
      setFlat({ items: result.items, total: result.total, loaded: true, loading: false });
    } catch {
      if (request !== flatRequest.current || activeFilterFingerprint.current !== requestFilter) return;
      // Polling keeps the last successful snapshot visible.
      setFlat((current) => ({ ...current, loading: false }));
    }
  }, [activeAssetScope, filterFingerprint, flatPage, flatPageSize, severity, status, vulnclass, task, query, sort]);

  // loadAssetTree fetches the whole asset tree. The tree does not follow the selected node (otherwise one selection
  // would collapse it into a single chain), so no assetScope is passed here.
  const loadAssetTree = React.useCallback(async () => {
    const requestFilter = filterFingerprint;
    if (activeFilterFingerprint.current !== requestFilter) return;
    const request = ++assetTreeRequest.current;
    setAssetTree((current) => ({ ...current, loading: true }));
    try {
      const result = await api.findingAssetTree({ severity, status, vulnclass, task, query, sort });
      if (request !== assetTreeRequest.current || activeFilterFingerprint.current !== requestFilter) return;
      setAssetTree({
        nodes: result.nodes ?? [],
        findingTotal: result.finding_total ?? 0,
        truncated: Boolean(result.truncated),
        droppedKinds: result.dropped_kinds ?? [],
        loaded: true,
        loading: false,
      });
    } catch (e) {
      if (request !== assetTreeRequest.current || activeFilterFingerprint.current !== requestFilter) return;
      setAssetTree((current) => ({ ...current, loading: false }));
      toast.error(t("toast.assetTreeFailed", { error: (e as Error).message }));
    }
  }, [filterFingerprint, severity, status, vulnclass, task, query, sort, t]);

  const refreshGroups = React.useCallback(async () => {
    const requestFilter = filterFingerprint;
    if (activeFilterFingerprint.current !== requestFilter) return;
    const request = ++groupsRequest.current;
    try {
      const result = await api.findingGroups({
        page,
        pageSize,
        severity,
        status,
        vulnclass,
        task,
        query,
        sort,
      });
      if (request !== groupsRequest.current || activeFilterFingerprint.current !== requestFilter) return;
      setGroups(result.items);
      setGroupTotal(result.total);
      setTotal(result.finding_total);
    } catch {
      // Polling keeps the last successful snapshot visible.
    }
  }, [filterFingerprint, page, pageSize, severity, status, vulnclass, task, query, sort]);

  const loadGroup = React.useCallback(
    async (key: string, groupPage: number, groupPageSize: number) => {
      const request = (groupRequests.current[key] ?? 0) + 1;
      const requestFilter = filterFingerprint;
      if (activeFilterFingerprint.current !== requestFilter) return;
      groupRequests.current[key] = request;
      setGroupFindings((current) => ({
        ...current,
        [key]: {
          items: current[key]?.items ?? [],
          total: current[key]?.total ?? 0,
          page: groupPage,
          pageSize: groupPageSize,
          loaded: current[key]?.loaded ?? false,
          loading: true,
        },
      }));
      try {
        const result = await api.findingsPage({
          page: groupPage,
          pageSize: groupPageSize,
          severity,
          status,
          vulnclass,
          task: key,
          query,
          sort,
        });
        if (groupRequests.current[key] !== request || activeFilterFingerprint.current !== requestFilter) return;
        setGroupFindings((current) => ({
          ...current,
          [key]: {
            items: result.items,
            total: result.total,
            page: result.page,
            pageSize: result.page_size,
            loaded: true,
            loading: false,
          },
        }));
      } catch {
        if (groupRequests.current[key] !== request || activeFilterFingerprint.current !== requestFilter) return;
        setGroupFindings((current) => ({
          ...current,
          [key]: {
            ...(current[key] ?? {
              items: [],
              total: 0,
              page: groupPage,
              pageSize: groupPageSize,
              loaded: false,
            }),
            loading: false,
          },
        }));
      }
    },
    [filterFingerprint, severity, status, vulnclass, query, sort],
  );

  React.useEffect(() => {
    for (const [key, state] of Object.entries(groupFindings)) {
      if (!state.loaded || state.loading) continue;
      const lastPage = Math.max(1, Math.ceil(state.total / state.pageSize));
      if (state.page > lastPage) void loadGroup(key, lastPage, state.pageSize);
    }
  }, [groupFindings, loadGroup]);

  const toggleGroup = React.useCallback(
    (key: string) => {
      const opening = !expandedGroups.has(key);
      const next = new Set(expandedGroups);
      if (opening) next.add(key);
      else next.delete(key);
      setExpandedGroups(next);
      const state = groupFindings[key];
      if (opening && !state?.loaded && !state?.loading) {
        void loadGroup(key, state?.page ?? 1, state?.pageSize ?? 10);
      }
    },
    [expandedGroups, groupFindings, loadGroup],
  );

  // Refresh the current view after an inline mutation: the flat view reloads its page, the grouped view reloads the group headers + the group holding that finding.
  const refreshAfterMutation = React.useCallback(
    (finding: Finding, removed = false) => {
      if (view === "asset") {
        // The asset view does not poll, so recompute the tree counts after a mutation as well.
        void loadFlat();
        void loadAssetTree();
        return;
      }
      if (view === "flat") {
        // When the last page is emptied the out-of-range fix-up effect steps the page back and reloads with it.
        void loadFlat();
        return;
      }
      void refreshGroups();
      const key = finding.task_id ?? UNASSIGNED_TASK;
      const state = groupFindingsRef.current[key];
      if (state?.loaded) {
        const nextTotal = Math.max(0, state.total - (removed ? 1 : 0));
        const lastPage = Math.max(1, Math.ceil(nextTotal / state.pageSize));
        void loadGroup(key, Math.min(state.page, lastPage), state.pageSize);
      }
    },
    [loadAssetTree, loadFlat, loadGroup, refreshGroups, view],
  );

  // Reset every view's pagination and expansion when a shared finding filter changes.
  React.useEffect(() => {
    void filterFingerprint;
    setPage(1);
    setExpanded(null);
    setExpandedGroups(new Set());
    setGroupFindings({});
    setFlatPage(1);
    setFlat(EMPTY_FLAT_STATE);
    // A filter change also changes the tree, so the previously selected node may be gone; fall back to "all assets".
    setAssetScope(null);
    setAssetTree(EMPTY_ASSET_TREE);
  }, [filterFingerprint]);

  // A different asset node is a different result set, so return to page one.
  React.useEffect(() => {
    void assetScope;
    setFlatPage(1);
  }, [assetScope]);

  // The asset tree is queried once when the view is entered / the filters change (and explicitly re-fetched by
  // refreshAfterMutation after a finding is mutated on this page); it never polls.
  React.useEffect(() => {
    if (!preferencesHydrated || view !== "asset") return;
    void activeRetestFingerprint; // a finished retest can change the asset counts under the status filter.
    void loadAssetTree();
  }, [activeRetestFingerprint, loadAssetTree, preferencesHydrated, view]);

  // Poll only the current view: the flat view refreshes its page, the grouped view its group headers and each expanded group (their pagination is independent).
  // The asset view is queried once (see the return below); its left tree is navigation structure and does not need a recompute every 5 seconds.
  // Wait for the preferences to hydrate before the first request, otherwise it fetches once with the default view/filters for nothing.
  React.useEffect(() => {
    if (!preferencesHydrated) return;
    void activeRetestFingerprint; // also covers the asset view (which polls on no timer): retest completion refreshes disposition state too.
    const refresh = () => {
      if (view === "flat" || view === "asset") {
        if (!flatStateRef.current.loading) void loadFlat();
        return;
      }
      void refreshGroups();
      for (const key of expandedGroupsRef.current) {
        if (!visibleGroupKeysRef.current.has(key)) continue;
        const state = groupFindingsRef.current[key];
        if (state?.loaded && !state.loading) void loadGroup(key, state.page, state.pageSize);
      }
    };
    refresh();
    if (view === "asset") return;
    const timer = setInterval(refresh, 5000);
    return () => clearInterval(timer);
  }, [activeRetestFingerprint, loadFlat, loadGroup, preferencesHydrated, refreshGroups, view]);

  React.useEffect(() => {
    const lastPage = Math.max(1, Math.ceil(groupTotal / pageSize));
    if (page > lastPage) setPage(lastPage);
  }, [groupTotal, page, pageSize]);

  React.useEffect(() => {
    if (!flat.loaded) return;
    const lastPage = Math.max(1, Math.ceil(flat.total / flatPageSize));
    if (flatPage > lastPage) setFlatPage(lastPage);
  }, [flat.loaded, flat.total, flatPage, flatPageSize]);

  // Whole-table aggregates (stat cards + vuln-class options) — independent of the
  // current page, so they stay exact.
  React.useEffect(() => {
    let alive = true;
    const load = () => {
      api
        .findingStats()
        .then((s) => {
          if (alive) {
            setStats(s);
            setStatsLoaded(true);
          }
        })
        .catch(() => {
          // Keep the previous aggregate snapshot until the next poll.
        });
    };
    load();
    const t = setInterval(load, 5000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, []);

  React.useEffect(() => {
    if (!statsLoaded) return;
    if (vulnclass !== "all" && !stats.vulnclasses.includes(vulnclass)) setVulnclass("all");
    if (
      task !== "all" &&
      task !== UNASSIGNED_TASK &&
      !(stats.tasks ?? []).some((option) => String(option.id) === task)
    ) {
      setTask("all");
    }
  }, [stats, statsLoaded, task, vulnclass]);

  // updateStatus optimistically flips one finding's triage state, reverting on error.
  const updateStatus = React.useCallback(
    async (f: Finding, next: FindingStatus) => {
      if (!f.finding_id || next === f.status) return;
      const prev = f.status;
      setFindings((cur) => cur.map((x) => (isSameFinding(x, f) ? { ...x, status: next } : x)));
      try {
        await api.setFindingStatus(f.finding_id, next);
        toast.success(t("toast.marked", { label: tStatus(`finding.${next}`) }));
        // refresh stat cards (pending count) and drop the row if it no longer matches the status filter
        api
          .findingStats()
          .then(setStats)
          .catch(() => {
            // The row update remains valid even if the aggregate refresh fails.
          });
        if (status !== "all" && next !== status) {
          setFindings((cur) => cur.filter((x) => !isSameFinding(x, f)));
          setTotal((t) => Math.max(0, t - 1));
          setFlat((cur) => ({ ...cur, total: Math.max(0, cur.total - 1) }));
        }
        refreshAfterMutation(f);
      } catch (e) {
        setFindings((cur) => cur.map((x) => (isSameFinding(x, f) ? { ...x, status: prev } : x)));
        toast.error(t("toast.updateFailed", { error: (e as Error).message }));
      }
    },
    [refreshAfterMutation, setFindings, status, t, tStatus],
  );

  // Cached detail report of an inline expanded row, keyed by the globally stable row key. report is a large Markdown blob that
  // list queries omit, so it is fetched once per finding_id on expand; done with empty text = no report for this finding yet.
  const [reports, setReports] = React.useState<Record<string, FindingReport>>({});

  // Inline edit buffer: name/category/severity of the currently expanded row, initialized from that row on expand and cleared on collapse.
  // Only one row is expanded at a time, so a single buffer suffices.
  const [edit, setEdit] = React.useState<FindingEdit | null>(null);
  const [saving, setSaving] = React.useState(false);

  // toggle expands/collapses a row; on a new expand it initializes the edit buffer and (if not fetched yet) pulls the report cache by finding_id.
  const toggleRow = React.useCallback(
    (f: Finding) => {
      const key = findingRowKey(f);
      const willOpen = expanded !== key;
      setExpanded(willOpen ? key : null);
      if (!willOpen) {
        setEdit(null);
        return;
      }
      setEdit({ name: f.name ?? "", vulnclass: f.vulnclass, severity: f.severity });
      if (!f.finding_id || reports[key]) return;
      const fid = f.finding_id;
      setReports((r) => ({ ...r, [key]: { status: "loading", text: "" } }));
      api
        .getFinding(fid)
        .then((full) => setReports((r) => ({ ...r, [key]: { status: "done", text: full.report ?? "" } })))
        .catch(() => setReports((r) => ({ ...r, [key]: { status: "error", text: "" } })));
    },
    [expanded, reports],
  );

  // saveEdit persists the expanded row's name/category/severity, writes it back to the local list and refreshes the stats (the category dropdown and severity counts may change).
  const saveEdit = React.useCallback(
    async (f: Finding) => {
      if (!f.finding_id || !edit) return;
      setSaving(true);
      try {
        const updated = await api.updateFinding(f.finding_id, {
          name: edit.name.trim(),
          vulnclass: edit.vulnclass.trim(),
          severity: edit.severity,
        });
        setFindings((cur) =>
          cur.map((x) =>
            isSameFinding(x, f)
              ? { ...x, name: updated.name, vulnclass: updated.vulnclass, severity: updated.severity }
              : x,
          ),
        );
        toast.success(t("toast.saved"));
        api
          .findingStats()
          .then(setStats)
          .catch(() => {
            // The edit remains valid even if the aggregate refresh fails.
          });
        refreshAfterMutation(f);
      } catch (e) {
        toast.error(t("toast.saveFailed", { error: (e as Error).message }));
      } finally {
        setSaving(false);
      }
    },
    [edit, refreshAfterMutation, setFindings, t],
  );

  // deleteFinding deletes a finding (needs a second confirmation): on success it removes it from the list, collapses the row and refreshes the stats.
  const deleteFinding = React.useCallback(
    async (f: Finding) => {
      if (!f.finding_id) return;
      try {
        await api.deleteFinding(f.finding_id);
        setFindings((cur) => cur.filter((x) => !isSameFinding(x, f)));
        setSelectedIds((current) => {
          const next = new Set(current);
          next.delete(f.finding_id as string);
          return next;
        });
        setTotal((t) => Math.max(0, t - 1));
        setFlat((cur) => ({ ...cur, total: Math.max(0, cur.total - 1) }));
        const rowKey = findingRowKey(f);
        setExpanded((cur) => (cur === rowKey ? null : cur));
        toast.success(t("toast.deleted"));
        api
          .findingStats()
          .then(setStats)
          .catch(() => {
            // The deletion remains valid even if the aggregate refresh fails.
          });
        refreshAfterMutation(f, true);
      } catch (e) {
        toast.error(t("toast.deleteFailed", { error: (e as Error).message }));
      }
    },
    [refreshAfterMutation, setFindings, t],
  );

  const openDeepen = React.useCallback((f: Finding) => {
    setDeepenFinding(f);
    setDeepenDescription("");
  }, []);

  async function submitDeepen() {
    if (!deepenFinding?.finding_id || !deepenDescription.trim() || deepening) return;
    setDeepening(true);
    try {
      const result = await api.deepenFinding(deepenFinding.finding_id, deepenDescription.trim());
      toast.success(
        result.queued
          ? t("toast.deepenQueued", { id: result.intent_id })
          : t("toast.deepenCreated", { id: result.intent_id }),
      );
      refreshAfterMutation(deepenFinding);
      setDeepenFinding(null);
      setDeepenDescription("");
    } catch (error) {
      toast.error(t("toast.deepenFailed", { error: (error as Error).message }));
    } finally {
      setDeepening(false);
    }
  }

  const statCards = [
    { label: t("stat.total"), value: stats.total, icon: BugIcon },
    { label: t("stat.pending"), value: stats.pending, tone: "text-amber-500", icon: ClockIcon },
    { label: t("severity.critical"), value: stats.critical, tone: "text-rose-600", icon: ShieldAlertIcon },
    { label: t("severity.high"), value: stats.high, tone: "text-red-500", icon: TriangleAlertIcon },
    { label: t("severity.medium"), value: stats.medium, tone: "text-amber-500", icon: TriangleAlertIcon },
    { label: t("severity.low"), value: stats.low, tone: "text-slate-500", icon: InfoIcon },
  ];

  // Row count of "current filters" in the export dialog: both views share the filters, only the counting source differs.
  // The flat and asset views share the flat list state; the grouped view's count comes from the group endpoint's finding_total.
  const filteredTotal = view === "grouped" ? total : flat.total;
  const assetPath = React.useMemo(
    () => (view === "asset" ? assetPathOf(assetTree.nodes, assetScope) : []),
    [assetScope, assetTree.nodes, view],
  );

  const rowProps = {
    selectedIds,
    onToggleSelected: toggleSelected,
    onToggleSelectedPage: toggleSelectedPage,
    expandedKey: expanded,
    onToggleRow: toggleRow,
    reports,
    edit,
    onEditChange: setEdit,
    saving,
    onSave: saveEdit,
    onStatusChange: updateStatus,
    onRetest: setRetestFinding,
    activeRetests,
    onDeepen: openDeepen,
    onDelete: deleteFinding,
  };

  // The flat view and the right side of the asset view are the same table + the same pagination, only the filters differ.
  const flatListCard = (
    <Card className="gap-0 py-0">
      <CardContent className="px-0">
        {flat.loading && !flat.loaded ? (
          <div className="flex min-h-36 items-center justify-center">
            <Spinner />
          </div>
        ) : (
          <>
            <FindingsTable items={flat.items} selectAllLabel={t("selectAllPage")} {...rowProps} />
            <TablePagination
              page={flatPage}
              pageSize={flatPageSize}
              total={flat.total}
              onPageChange={setFlatPage}
              onPageSizeChange={(nextSize) => {
                setFlatPageSize(nextSize);
                setFlatPage(1);
              }}
              pageSizeOptions={[10, 20, 50, 100]}
            />
          </>
        )}
      </CardContent>
    </Card>
  );

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">{t("title")}</h1>
          <p className="text-muted-foreground text-sm">{t("subtitle")}</p>
        </div>
        <Tabs value={view} onValueChange={(v) => setView(v as FindingView)}>
          <TabsList>
            <TabsTrigger value="flat">{t("view.flat")}</TabsTrigger>
            <TabsTrigger value="grouped">{t("view.grouped")}</TabsTrigger>
            <TabsTrigger value="asset">{t("view.asset")}</TabsTrigger>
          </TabsList>
        </Tabs>
      </div>
      <div className="flex flex-1 flex-col gap-4 md:gap-6">
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-6">
          {statCards.map((stat) => {
            const StatIcon = stat.icon;
            return (
              <Card key={stat.label} className="gap-1 py-4">
                <CardHeader className="px-4">
                  <CardDescription>{stat.label}</CardDescription>
                  <CardTitle className={cn("flex items-center gap-2 text-2xl tabular-nums", stat.tone)}>
                    <StatIcon className="size-5" aria-hidden="true" />
                    {stat.value}
                  </CardTitle>
                </CardHeader>
              </Card>
            );
          })}
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <InputGroup className="w-full sm:w-72">
            <InputGroupInput
              type="search"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder={t("search.placeholder")}
              aria-label={t("search.placeholder")}
            />
            <InputGroupAddon>
              <SearchIcon aria-hidden="true" />
            </InputGroupAddon>
          </InputGroup>

          <ToggleGroup
            type="single"
            value={severity}
            onValueChange={(value) => value && setSeverity(value as "all" | Severity)}
            variant="outline"
            size="sm"
            spacing={0}
          >
            {(
              [
                ["all", t("severity.all")],
                ["critical", t("severity.critical")],
                ["high", t("severity.high")],
                ["medium", t("severity.medium")],
                ["low", t("severity.low")],
              ] as const
            ).map(([val, label]) => (
              <ToggleGroupItem key={val} value={val} aria-label={t("severity.ariaFilter", { label })}>
                {label}
              </ToggleGroupItem>
            ))}
          </ToggleGroup>

          <Select value={status} onValueChange={(v) => setStatus(v as "all" | FindingStatus)}>
            <SelectTrigger size="sm" className="w-32">
              <SelectValue placeholder={t("status.placeholder")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("status.all")}</SelectItem>
              {FINDING_STATUSES.map((st) => (
                <SelectItem key={st} value={st}>
                  {tStatus(`finding.${st}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          <Select value={vulnclass} onValueChange={setVulnclass}>
            <SelectTrigger size="sm" className="w-40">
              <SelectValue placeholder={t("vulnclass.placeholder")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("vulnclass.all")}</SelectItem>
              {stats.vulnclasses.map((vc) => (
                <SelectItem key={vc} value={vc}>
                  {vc}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          <Select value={task} onValueChange={setTask}>
            <SelectTrigger size="sm" className="w-48">
              <SelectValue placeholder={t("task.placeholder")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("task.all")}</SelectItem>
              <SelectItem value={UNASSIGNED_TASK}>{t("task.unassigned")}</SelectItem>
              {(stats.tasks ?? []).map((taskOpt) => {
                const id = String(taskOpt.id);
                const label = taskOpt.name || taskOpt.description || t("task.deletedShort", { id });
                return (
                  <SelectItem key={id} value={id}>
                    <span className="flex w-full items-center gap-2">
                      <span className="max-w-[14rem] truncate" title={label}>
                        {label}
                      </span>
                      <span className="inline-flex items-center gap-1 text-muted-foreground tabular-nums">
                        <BugIcon className="size-3.5" aria-hidden="true" />
                        {taskOpt.count}
                      </span>
                    </span>
                  </SelectItem>
                );
              })}
            </SelectContent>
          </Select>

          <Select value={sort} onValueChange={(v) => setSort(v as "severity" | "time")}>
            <SelectTrigger size="sm" className="w-36">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="severity">{t("sort.severity")}</SelectItem>
              <SelectItem value="time">{t("sort.time")}</SelectItem>
            </SelectContent>
          </Select>

          <div className="ml-auto flex items-center gap-3">
            {selectedIds.size > 0 && (
              <span className="text-xs text-muted-foreground tabular-nums">
                {t("selectedCount", { count: selectedIds.size })}
              </span>
            )}
            <Button size="sm" variant="outline" onClick={openExport}>
              <DownloadIcon /> {t("export")}
            </Button>
          </div>
        </div>

        {view === "flat" && flatListCard}

        {view === "asset" && (
          <div className="grid min-h-0 items-start gap-4 lg:grid-cols-[20rem_minmax(0,1fr)] xl:grid-cols-[24rem_minmax(0,1fr)]">
            <Card className="gap-0 py-3 lg:sticky lg:top-4">
              <CardContent className="flex flex-col px-3">
                {assetTree.loading && !assetTree.loaded ? (
                  <div className="flex min-h-36 items-center justify-center">
                    <Spinner />
                  </div>
                ) : (
                  <AssetTree
                    nodes={assetTree.nodes}
                    selected={assetScope}
                    onSelect={setAssetScope}
                    loading={assetTree.loading}
                    truncated={assetTree.truncated}
                    droppedKinds={assetTree.droppedKinds}
                    findingTotal={assetTree.findingTotal}
                    onRefresh={() => void loadAssetTree()}
                  />
                )}
              </CardContent>
            </Card>
            <div className="flex min-w-0 flex-col gap-2">
              <div className="flex min-w-0 flex-wrap items-center gap-1 text-sm text-muted-foreground">
                <button
                  type="button"
                  className={cn("hover:text-foreground", assetScope === null && "font-medium text-foreground")}
                  onClick={() => setAssetScope(null)}
                >
                  {t("asset.all")}
                </button>
                {assetPath.map((node) => (
                  <React.Fragment key={node.key}>
                    <ChevronRightIcon className="size-3.5 shrink-0" aria-hidden="true" />
                    <button
                      type="button"
                      className={cn(
                        "max-w-[16rem] truncate hover:text-foreground",
                        node.key === assetScope && "font-medium text-foreground",
                      )}
                      title={node.label}
                      onClick={() => setAssetScope(node.key)}
                    >
                      {node.display}
                    </button>
                  </React.Fragment>
                ))}
                <span className="ml-auto shrink-0 text-xs tabular-nums">
                  {t("asset.totalCount", { count: flat.total })}
                </span>
              </div>
              {flatListCard}
            </div>
          </div>
        )}

        {view === "grouped" && (
          <div className="flex flex-col gap-3">
            {groups.map((group) => {
              const key = findingGroupKey(group);
              const groupOpen = expandedGroups.has(key);
              const state = groupFindings[key] ?? {
                items: [],
                total: group.count,
                page: 1,
                pageSize: 10,
                loaded: false,
                loading: false,
              };
              return (
                <Card key={key} className="gap-0 py-0">
                  <CardHeader className="px-4 py-3">
                    <div className="flex min-w-0 flex-wrap items-center gap-3">
                      <button
                        type="button"
                        className="flex min-w-0 flex-1 items-center gap-3 text-left"
                        aria-expanded={groupOpen}
                        onClick={() => toggleGroup(key)}
                      >
                        <ChevronRightIcon
                          className={cn(
                            "size-4 shrink-0 text-muted-foreground transition-transform",
                            groupOpen && "rotate-90",
                          )}
                        />
                        <div className="flex min-w-0 flex-col gap-1">
                          <CardTitle className="truncate text-sm">
                            {group.task_id === null
                              ? t("task.unassigned")
                              : group.task_name
                                ? t("group.withName", { name: group.task_name, id: group.task_id })
                                : t("group.taskOnly", { id: group.task_id })}
                          </CardTitle>
                          <CardDescription className="truncate" title={group.task_description}>
                            {group.task_description || t("group.sourceUnavailable")}
                          </CardDescription>
                        </div>
                      </button>
                      <div className="flex flex-wrap items-center gap-2">
                        {group.task_status && <StatusBadge domain="task" value={group.task_status} dot />}
                        {SEVERITIES.map((level) => {
                          const count = group[level];
                          if (count === 0) return null;
                          return (
                            <span key={level} className="inline-flex items-center gap-1">
                              <StatusBadge domain="severity" value={level} dot />
                              <span className="text-xs tabular-nums text-muted-foreground">{count}</span>
                            </span>
                          );
                        })}
                        <span className="text-xs tabular-nums text-muted-foreground">
                          {fmtTime(group.last_found_at)}
                        </span>
                        {group.task_id !== null && (
                          <Button size="icon-sm" variant="ghost" asChild>
                            <Link
                              href={`/function/tasks/detail?id=${group.task_id}`}
                              aria-label={t("group.viewTask", { id: group.task_id })}
                            >
                              <ArrowUpRightIcon />
                            </Link>
                          </Button>
                        )}
                      </div>
                    </div>
                  </CardHeader>
                  {groupOpen && (
                    <CardContent className="px-0">
                      {state.loading && !state.loaded ? (
                        <div className="flex min-h-36 items-center justify-center">
                          <Spinner />
                        </div>
                      ) : (
                        <>
                          <FindingsTable items={state.items} selectAllLabel={t("selectAllGroup")} {...rowProps} />
                          <TablePagination
                            page={state.page}
                            pageSize={state.pageSize}
                            total={state.total}
                            onPageChange={(nextPage) => void loadGroup(key, nextPage, state.pageSize)}
                            onPageSizeChange={(nextSize) => void loadGroup(key, 1, nextSize)}
                          />
                        </>
                      )}
                    </CardContent>
                  )}
                </Card>
              );
            })}
            {groups.length === 0 && (
              <Card>
                <CardContent className="py-12 text-center text-sm text-muted-foreground">{t("empty")}</CardContent>
              </Card>
            )}
            <TablePagination
              page={page}
              pageSize={pageSize}
              total={groupTotal}
              onPageChange={setPage}
              onPageSizeChange={(nextPageSize) => {
                setPageSize(nextPageSize);
                setPage(1);
              }}
              pageSizeOptions={[5, 10, 20]}
            />
          </div>
        )}
      </div>

      {retestFinding?.finding_id ? (
        <FindingRetestDialog
          key={retestFinding.finding_id}
          findingId={retestFinding.finding_id}
          findingName={firstNonEmpty(retestFinding.name, retestFinding.vulnclass, retestFinding.summary)}
          onStarted={(retest) => {
            const findingId = retestFinding.finding_id;
            if (!findingId || retest.conversation_id == null || !["pending", "running"].includes(retest.status)) return;
            retestRefreshVersion.current++;
            const active: ActiveFindingRetest = {
              id: retest.id,
              finding_id: findingId,
              conversation_id: retest.conversation_id,
              status: retest.status === "pending" ? "pending" : "running",
            };
            setActiveRetests((current) => ({ ...current, [findingId]: active }));
          }}
          onClose={() => setRetestFinding(null)}
        />
      ) : null}

      <Dialog
        open={deepenFinding !== null}
        onOpenChange={(open) => {
          if (open || deepening) return;
          setDeepenFinding(null);
          setDeepenDescription("");
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("deepen.title")}</DialogTitle>
            <DialogDescription className="break-words">
              {t("deepen.description", {
                id: deepenFinding?.task_id ?? "",
                name: firstNonEmpty(deepenFinding?.name, deepenFinding?.vulnclass, deepenFinding?.summary),
              })}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="finding-deepen-description">{t("deepen.label")}</FieldLabel>
              <Textarea
                id="finding-deepen-description"
                value={deepenDescription}
                onChange={(event) => setDeepenDescription(event.target.value)}
                maxLength={4000}
                placeholder={t("deepen.placeholder")}
                disabled={deepening}
              />
              <FieldDescription className="flex justify-between gap-3">
                <span>{t("deepen.hint")}</span>
                <span className="shrink-0 tabular-nums">{deepenDescription.length} / 4000</span>
              </FieldDescription>
            </Field>
          </FieldGroup>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => {
                setDeepenFinding(null);
                setDeepenDescription("");
              }}
              disabled={deepening}
            >
              {t("common.cancel")}
            </Button>
            <Button onClick={submitDeepen} disabled={deepening || !deepenDescription.trim()}>
              {deepening && <Spinner data-icon="inline-start" />}
              {t("deepen.submit")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={exportOpen} onOpenChange={setExportOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("exportDialog.title")}</DialogTitle>
            <DialogDescription>{t("exportDialog.description")}</DialogDescription>
          </DialogHeader>

          <div className="flex flex-col gap-5 py-1">
            <div className="flex flex-col gap-2">
              <span className="text-xs text-muted-foreground">{t("exportDialog.scopeLabel")}</span>
              <RadioGroup value={exportScope} onValueChange={(v) => setExportScope(v as typeof exportScope)}>
                <label htmlFor="export-scope-filtered" className="flex items-center gap-2 text-sm">
                  <RadioGroupItem id="export-scope-filtered" value="filtered" />{" "}
                  {t("exportDialog.scopeFiltered", { count: filteredTotal })}
                </label>
                <label htmlFor="export-scope-all" className="flex items-center gap-2 text-sm">
                  <RadioGroupItem id="export-scope-all" value="all" /> {t("exportDialog.scopeAll")}
                </label>
                <label
                  htmlFor="export-scope-selected"
                  className={cn("flex items-center gap-2 text-sm", selectedIds.size === 0 && "text-muted-foreground")}
                >
                  <RadioGroupItem id="export-scope-selected" value="selected" disabled={selectedIds.size === 0} />
                  {t("exportDialog.scopeSelected", { count: selectedIds.size })}
                </label>
              </RadioGroup>
            </div>

            <div className="flex flex-col gap-2">
              <span className="text-xs text-muted-foreground">{t("exportDialog.formatLabel")}</span>
              <RadioGroup value={exportFormat} onValueChange={(v) => setExportFormat(v as typeof exportFormat)}>
                <label htmlFor="export-format-md-single" className="flex items-center gap-2 text-sm">
                  <RadioGroupItem id="export-format-md-single" value="md-single" /> {t("exportDialog.formatMdSingle")}
                </label>
                <label htmlFor="export-format-md-zip" className="flex items-center gap-2 text-sm">
                  <RadioGroupItem id="export-format-md-zip" value="md-zip" /> {t("exportDialog.formatMdZip")}
                </label>
                <label htmlFor="export-format-csv" className="flex items-center gap-2 text-sm">
                  <RadioGroupItem id="export-format-csv" value="csv" /> {t("exportDialog.formatCsv")}
                </label>
                <label htmlFor="export-format-json" className="flex items-center gap-2 text-sm">
                  <RadioGroupItem id="export-format-json" value="json" /> {t("exportDialog.formatJson")}
                </label>
              </RadioGroup>
            </div>
          </div>

          <DialogFooter>
            <Button variant="outline" onClick={() => setExportOpen(false)} disabled={exporting}>
              {t("common.cancel")}
            </Button>
            <Button onClick={doExport} disabled={exporting || (exportScope === "selected" && selectedIds.size === 0)}>
              <DownloadIcon /> {exporting ? t("exportDialog.exporting") : t("exportDialog.submit")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
