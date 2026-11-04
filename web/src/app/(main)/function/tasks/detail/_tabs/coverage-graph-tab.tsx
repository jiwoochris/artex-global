"use client";

import * as React from "react";

import type { Graph as G6Graph } from "@antv/g6";
import { AppWindow, Building2, Globe, Link2, type LucideIcon, Radio, RefreshCw, Server, Waypoints } from "lucide-react";
import { useTranslations } from "next-intl";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api } from "@/lib/api";
import type { CoverageAssetRef, CoverageAssetRefs, CoverageGraphEdge, CoverageGraphNode } from "@/lib/types";
import { cn } from "@/lib/utils";

// Children of the same kind shown per parent node by default; the rest collapse behind
// "show more", which pulls this many more at a time.
const FOLD_LIMIT = 20;
const FOLD_STEP = 20;

type Kind = CoverageGraphNode["kind"];

// label is no longer kept here. Node kind names are user-visible, so they are
// translated from `taskDetail.coverageGraph.kind.*` (including canvas labels).
type KindMeta = { icon: LucideIcon; iconBg: string; hex: string; size: number };

const kindMeta: Record<Kind, KindMeta> = {
  company: { icon: Building2, iconBg: "bg-slate-500", hex: "#64748b", size: 46 },
  root_domain: { icon: Globe, iconBg: "bg-indigo-500", hex: "#6366f1", size: 38 },
  subdomain: { icon: Waypoints, iconBg: "bg-blue-500", hex: "#3b82f6", size: 30 },
  ip: { icon: Server, iconBg: "bg-cyan-600", hex: "#0891b2", size: 28 },
  service: { icon: Radio, iconBg: "bg-amber-500", hex: "#f59e0b", size: 26 },
  app: { icon: AppWindow, iconBg: "bg-fuchsia-500", hex: "#d946ef", size: 26 },
  endpoint: { icon: Link2, iconBg: "bg-rose-500", hex: "#f43f5e", size: 20 },
};

// G6 node icons use platform-consistent lucide icons: the lucide SVG path (v1.22) is
// rendered into a white-stroked data URI as the node iconSrc (white reads on both solid
// and grey backgrounds). Inlined by hand to avoid react-dom/server issues in the
// React19/Next client bundle.
function svgUri(inner: string, filled = false): string {
  const attrs = filled
    ? 'fill="#fff" stroke="none"'
    : 'fill="none" stroke="#fff" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"';
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" ${attrs}>${inner}</svg>`;
  return `data:image/svg+xml,${encodeURIComponent(svg)}`;
}

const KIND_ICON: Record<Kind, string> = {
  company: svgUri(
    '<path d="M10 12h4"/><path d="M10 8h4"/><path d="M14 21v-3a2 2 0 0 0-4 0v3"/><path d="M6 10H4a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2V9a2 2 0 0 0-2-2h-2"/><path d="M6 21V5a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v16"/>',
  ),
  root_domain: svgUri(
    '<circle cx="12" cy="12" r="10"/><path d="M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20"/><path d="M2 12h20"/>',
  ),
  subdomain: svgUri(
    '<path d="m10.586 5.414-5.172 5.172"/><path d="m18.586 13.414-5.172 5.172"/><path d="M6 12h12"/><circle cx="12" cy="20" r="2"/><circle cx="12" cy="4" r="2"/><circle cx="20" cy="12" r="2"/><circle cx="4" cy="12" r="2"/>',
  ),
  ip: svgUri(
    '<rect width="20" height="8" x="2" y="2" rx="2" ry="2"/><rect width="20" height="8" x="2" y="14" rx="2" ry="2"/><line x1="6" x2="6.01" y1="6" y2="6"/><line x1="6" x2="6.01" y1="18" y2="18"/>',
  ),
  service: svgUri(
    '<path d="M16.247 7.761a6 6 0 0 1 0 8.478"/><path d="M19.075 4.933a10 10 0 0 1 0 14.134"/><path d="M4.925 19.067a10 10 0 0 1 0-14.134"/><path d="M7.753 16.239a6 6 0 0 1 0-8.478"/><circle cx="12" cy="12" r="2"/>',
  ),
  app: svgUri(
    '<rect x="2" y="4" width="20" height="16" rx="2"/><path d="M10 4v4"/><path d="M2 8h20"/><path d="M6 4v4"/>',
  ),
  endpoint: svgUri(
    '<path d="M9 17H7A5 5 0 0 1 7 7h2"/><path d="M15 7h2a5 5 0 1 1 0 10h-2"/><line x1="8" x2="16" y1="12" y2="12"/>',
  ),
};

const FOLD_ICON = svgUri(
  '<circle cx="5" cy="12" r="1.6"/><circle cx="12" cy="12" r="1.6"/><circle cx="19" cy="12" r="1.6"/>',
  true,
);

// ---------------------------------------------------------------------------
// Folding: full graph → currently-visible node/edge set (top-down cascading fold).
// ---------------------------------------------------------------------------
type FoldNode = {
  fold: true;
  key: string;
  groupId: string;
  parentKey: string;
  kind: Kind;
  hidden: CoverageGraphNode[];
};
type AssetRenderNode = { fold: false; key: string; kind: Kind; node: CoverageGraphNode };
type RenderNode = AssetRenderNode | FoldNode;

function sortChildren(a: CoverageGraphNode, b: CoverageGraphNode): number {
  if (a.tested !== b.tested) return a.tested ? -1 : 1;
  return (a.label || "").localeCompare(b.label || "");
}

function computeVisible(
  nodes: CoverageGraphNode[],
  edges: CoverageGraphEdge[],
  expanded: Map<string, number>,
): { renderNodes: RenderNode[]; renderEdges: CoverageGraphEdge[] } {
  const byKey = new Map(nodes.map((n) => [n.key, n]));
  const parentOf = new Map<string, string>();
  const childrenOf = new Map<string, string[]>();
  for (const e of edges) {
    if (!byKey.has(e.src) || !byKey.has(e.dst)) continue;
    if (!parentOf.has(e.src)) parentOf.set(e.src, e.dst);
    const arr = childrenOf.get(e.dst);
    if (arr) arr.push(e.src);
    else childrenOf.set(e.dst, [e.src]);
  }

  const renderNodes: RenderNode[] = [];
  const visible = new Set<string>();
  const foldNodes: FoldNode[] = [];

  const queue: string[] = [];
  for (const n of nodes) {
    if (!parentOf.has(n.key)) {
      queue.push(n.key);
      visible.add(n.key);
    }
  }

  for (let qi = 0; qi < queue.length; qi++) {
    const parent = queue[qi];
    const kids = childrenOf.get(parent) ?? [];
    if (kids.length === 0) continue;
    const groups = new Map<Kind, CoverageGraphNode[]>();
    for (const ck of kids) {
      const cn = byKey.get(ck);
      if (!cn) continue;
      const g = groups.get(cn.kind);
      if (g) g.push(cn);
      else groups.set(cn.kind, [cn]);
    }
    for (const [kind, arr] of groups) {
      arr.sort(sortChildren);
      const groupId = `${parent}::${kind}`;
      const shown = expanded.get(groupId) ?? FOLD_LIMIT;
      const visibleChildren = arr.slice(0, shown);
      const hidden = arr.slice(shown);
      for (const c of visibleChildren) {
        if (!visible.has(c.key)) {
          visible.add(c.key);
          queue.push(c.key);
        }
      }
      if (hidden.length > 0) {
        foldNodes.push({ fold: true, key: `fold:${groupId}`, groupId, parentKey: parent, kind, hidden });
      }
    }
  }

  for (const n of nodes) {
    if (visible.has(n.key)) renderNodes.push({ fold: false, key: n.key, kind: n.kind, node: n });
  }
  const renderEdges: CoverageGraphEdge[] = edges.filter((e) => visible.has(e.src) && visible.has(e.dst));
  for (const f of foldNodes) {
    renderNodes.push(f);
    renderEdges.push({ src: f.key, dst: f.parentKey });
  }
  return { renderNodes, renderEdges };
}

// ---------------------------------------------------------------------------
// G6 data mapping. Custom fields live on the node top level (per the official G6 v5
// force example: style/layout callbacks read d.<field> directly).
// ---------------------------------------------------------------------------
type G6NodeDatum = {
  id: string;
  kind: Kind;
  fold: boolean;
  tested: boolean;
  inScope: boolean;
  lbl: string;
  size: number;
};

function trunc(s: string, n = 26): string {
  return s.length > n ? `${s.slice(0, n - 1)}…` : s;
}

// Node text in the graph: a service shows no full URL, only port·title·status code; an
// endpoint shows only its path. Other kinds reuse the label the backend supplied.
function graphLabel(n: CoverageGraphNode): string {
  if (n.kind === "service") {
    const parts: string[] = [];
    if (n.port) parts.push(`:${n.port}`);
    if (n.page_title) parts.push(n.page_title);
    if (n.status_code) parts.push(String(n.status_code));
    // biome-ignore lint/nursery/useNullishCoalescing: display fallback — an empty string must fall through to the next candidate (ip, label), so || is intended (?? would render the empty string as-is)
    return parts.length ? parts.join(" · ") : n.domain || n.ip || n.label;
  }
  if (n.kind === "endpoint" && n.url) {
    try {
      return new URL(n.url).pathname || "/";
    } catch {
      /* Invalid URL: fall back to the full label */
    }
  }
  return n.label;
}

function toG6Nodes(
  renderNodes: RenderNode[],
  kindLabel: (k: Kind) => string,
  moreLabel: (count: number, k: Kind) => string,
): G6NodeDatum[] {
  return renderNodes.map((rn) => {
    if (rn.fold) {
      return {
        id: rn.key,
        kind: rn.kind,
        fold: true,
        tested: false,
        inScope: false,
        lbl: moreLabel(rn.hidden.length, rn.kind),
        size: 24,
      };
    }
    return {
      id: rn.key,
      kind: rn.kind,
      fold: false,
      tested: rn.node.tested,
      inScope: rn.node.in_scope,
      lbl: trunc(graphLabel(rn.node) || kindLabel(rn.kind)),
      size: kindMeta[rn.kind].size,
    };
  });
}

// G6's types put custom fields under data, but the official force example (and the
// runtime) reads d.<field> from the top level. Callback params are typed unknown to match
// the G6 signature and nd() casts them back to our top-level shape.
const nd = (d: unknown) => d as G6NodeDatum;

// tested=solid highlight; in-scope untested=grey; out of scope/folded=lighter grey + dashed stroke.
function nodeFill(d: G6NodeDatum): string {
  if (d.fold) return "#f1f5f9";
  if (!d.inScope) return "#e2e8f0";
  if (d.tested) return kindMeta[d.kind].hex;
  return "#94a3b8";
}
function nodeStroke(d: G6NodeDatum): string {
  if (d.fold || !d.inScope) return "#94a3b8";
  if (d.tested) return "#0f766e";
  return "#64748b";
}

// ---------------------------------------------------------------------------
// Drawer: an asset node opens details; a folded node shows the hidden list + "show more".
// ---------------------------------------------------------------------------
function DetailRow({ label, children }: { label: string; children: React.ReactNode }) {
  if (children === undefined || children === null || children === "") return null;
  return (
    <div className="flex items-start gap-3 py-1 text-sm">
      <span className="text-muted-foreground w-16 shrink-0">{label}</span>
      <span className="text-foreground min-w-0 flex-1 break-words">{children}</span>
    </div>
  );
}

function RefList({ title, items }: { title: string; items: CoverageAssetRef[] }) {
  const t = useTranslations("taskDetail.coverageGraph");
  if (items.length === 0) return null;
  return (
    <div>
      <h4 className="text-muted-foreground mb-1 text-xs font-medium">
        {title} ({items.length})
      </h4>
      <div className="flex flex-col gap-1">
        {items.map((r) => (
          <div
            key={`${r.kind}-${r.id}`}
            className="flex flex-wrap items-start gap-2 rounded-md bg-muted/50 px-2 py-1.5 text-xs"
          >
            <span className="shrink-0 font-mono text-muted-foreground">#{r.id}</span>
            {r.state && <span className="shrink-0 text-muted-foreground">{r.state}</span>}
            <span className="min-w-32 flex-1 break-words text-foreground">{r.summary || "—"}</span>
            {r.inherited && r.source_task_id && (
              <Badge variant="outline" className="shrink-0">
                {t("sheet.refSource", { id: r.source_task_id })}
              </Badge>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

function AssetSheet({
  node,
  taskId,
  onOpenChange,
}: {
  node: CoverageGraphNode | null;
  taskId: string;
  onOpenChange: (open: boolean) => void;
}) {
  const t = useTranslations("taskDetail.coverageGraph");
  const meta = node ? kindMeta[node.kind] : null;
  const Icon = meta?.icon;
  const kindLabel = node ? t(`kind.${node.kind}`) : "";
  const raw = node ? JSON.stringify(node, null, 2) : "";
  const [refs, setRefs] = React.useState<CoverageAssetRefs | null>(null);

  React.useEffect(() => {
    setRefs(null);
    if (!node?.asset_id) return;
    let cancelled = false;
    api
      .taskAssetRefs(taskId, node.asset_id)
      .then((r) => {
        if (!cancelled) setRefs(r);
      })
      .catch(() => {
        /* No related items or an error: hide this section */
      });
    return () => {
      cancelled = true;
    };
  }, [node, taskId]);
  return (
    <Sheet open={node !== null} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex w-full flex-col gap-0 p-0 data-[side=right]:sm:max-w-md">
        {node && meta && Icon && (
          <>
            <SheetHeader className="border-b p-4">
              <div className="flex items-center gap-2.5 pr-8">
                <span
                  className={cn("flex size-8 shrink-0 items-center justify-center rounded-lg shadow-sm", meta.iconBg)}
                >
                  <Icon className="size-4 text-white" />
                </span>
                <div className="min-w-0">
                  <SheetTitle className="leading-tight">{kindLabel}</SheetTitle>
                  <span className="text-muted-foreground truncate font-mono text-xs" title={node.label}>
                    {node.label}
                  </span>
                </div>
              </div>
            </SheetHeader>
            <ScrollArea className="min-h-0 flex-1">
              <div className="flex w-full min-w-0 flex-col gap-4 p-4">
                <section>
                  <h4 className="text-muted-foreground mb-1 text-xs font-medium">{t("sheet.attributes")}</h4>
                  <DetailRow label={t("sheet.attrType")}>{kindLabel}</DetailRow>
                  <DetailRow label={t("sheet.attrTestState")}>
                    {node.in_scope ? (
                      node.tested ? (
                        <span className="text-emerald-600 dark:text-emerald-400">{t("sheet.tested")}</span>
                      ) : (
                        <span className="text-neutral-500">{t("sheet.untested")}</span>
                      )
                    ) : (
                      <span className="text-neutral-400">{t("sheet.outOfScope")}</span>
                    )}
                  </DetailRow>
                  <DetailRow label={t("sheet.attrDomain")}>{node.domain}</DetailRow>
                  <DetailRow label={t("sheet.attrRootDomain")}>{node.root_domain}</DetailRow>
                  <DetailRow label="IP">{node.ip}</DetailRow>
                  <DetailRow label={t("sheet.attrPort")}>{node.port ? node.port : undefined}</DetailRow>
                  <DetailRow label="URL">
                    {node.url ? <span className="font-mono text-xs break-all">{node.url}</span> : undefined}
                  </DetailRow>
                  <DetailRow label={t("sheet.attrTitle")}>{node.page_title}</DetailRow>
                  <DetailRow label={t("sheet.attrStatusCode")}>
                    {node.status_code ? node.status_code : undefined}
                  </DetailRow>
                  <DetailRow label={t("sheet.attrApp")}>{node.app_name}</DetailRow>
                  <DetailRow label={t("sheet.attrAssetId")}>
                    {node.asset_id ? <span className="font-mono text-xs">{node.asset_id}</span> : undefined}
                  </DetailRow>
                </section>
                {refs && (refs.intents.length > 0 || refs.facts.length > 0 || refs.findings.length > 0) && (
                  <section className="flex flex-col gap-3 border-t pt-3">
                    <RefList title={t("sheet.refIntents")} items={refs.intents} />
                    <RefList title={t("sheet.refFacts")} items={refs.facts} />
                    <RefList title={t("sheet.refFindings")} items={refs.findings} />
                  </section>
                )}
                <section className="border-t pt-3">
                  <h4 className="text-muted-foreground mb-1.5 text-xs font-medium">{t("sheet.rawData")}</h4>
                  <pre className="bg-muted/50 text-foreground max-w-full overflow-hidden rounded-md border p-3 font-mono text-xs leading-relaxed break-all whitespace-pre-wrap">
                    {raw}
                  </pre>
                </section>
              </div>
            </ScrollArea>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}

function FoldSheet({
  fold,
  onOpenChange,
  onShowMore,
  onPick,
}: {
  fold: FoldNode | null;
  onOpenChange: (open: boolean) => void;
  onShowMore: (groupId: string) => void;
  onPick: (n: CoverageGraphNode) => void;
}) {
  const t = useTranslations("taskDetail.coverageGraph");
  const meta = fold ? kindMeta[fold.kind] : null;
  const Icon = meta?.icon;
  const kindLabel = fold ? t(`kind.${fold.kind}`) : "";
  return (
    <Sheet open={fold !== null} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex w-full flex-col gap-0 p-0 data-[side=right]:sm:max-w-md">
        {fold && meta && Icon && (
          <>
            <SheetHeader className="border-b p-4">
              <SheetTitle className="text-base">
                {t("sheet.foldTitle", { kind: kindLabel, count: fold.hidden.length })}
              </SheetTitle>
              <p className="text-muted-foreground text-xs">{t("sheet.foldHint")}</p>
            </SheetHeader>
            <ScrollArea className="min-h-0 flex-1">
              <div className="flex flex-col gap-1 p-3">
                {fold.hidden.map((n) => (
                  <button
                    key={n.key}
                    type="button"
                    onClick={() => onPick(n)}
                    className="hover:bg-accent flex items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm"
                  >
                    <span
                      className={cn(
                        "flex size-5 shrink-0 items-center justify-center rounded",
                        n.tested ? meta.iconBg : "bg-neutral-400 dark:bg-neutral-600",
                      )}
                    >
                      <Icon className="size-3 text-white" />
                    </span>
                    <span className="min-w-0 flex-1 truncate font-mono text-xs" title={n.label}>
                      {n.label}
                    </span>
                    {n.tested && <span className="size-1.5 shrink-0 rounded-full bg-emerald-500" />}
                  </button>
                ))}
              </div>
            </ScrollArea>
            <div className="border-t p-3">
              <Button className="w-full" variant="outline" onClick={() => onShowMore(fold.groupId)}>
                {t("sheet.showMore", { step: FOLD_STEP })}
              </Button>
            </div>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}

// ---------------------------------------------------------------------------
function GraphInner({ taskId, coverageEnabled = true }: { taskId: string; coverageEnabled?: boolean }) {
  const [data, setData] = React.useState<{
    nodes: CoverageGraphNode[];
    edges: CoverageGraphEdge[];
  } | null>(null);
  const [expanded, setExpanded] = React.useState<Map<string, number>>(new Map());
  const [selectedAsset, setSelectedAsset] = React.useState<CoverageGraphNode | null>(null);
  const [selectedFoldId, setSelectedFoldId] = React.useState<string | null>(null);
  const [loading, setLoading] = React.useState(true);

  const t = useTranslations("taskDetail.coverageGraph");
  const kindLabel = React.useCallback((k: Kind) => t(`kind.${k}`), [t]);
  const moreLabel = React.useCallback(
    (count: number, k: Kind) => t("graph.moreCount", { kind: t(`kind.${k}`), count }),
    [t],
  );

  const containerRef = React.useRef<HTMLDivElement>(null);
  const graphRef = React.useRef<G6Graph | null>(null);
  // The click handler needs the latest key→RenderNode map (read the ref inside the G6 event callback).
  const renderMapRef = React.useRef<Map<string, RenderNode>>(new Map());
  const gDataRef = React.useRef<{ nodes: G6NodeDatum[]; edges: { source: string; target: string }[] }>({
    nodes: [],
    edges: [],
  });

  const fetchGraph = React.useCallback(() => {
    setLoading(true);
    api
      .taskCoverageGraph(taskId)
      .then((g) => {
        // Asset coverage feature off (B1): still render the graph (in-scope assets / company
        // relations stay visible) but flatten the tested state — no test progress, no tested highlight.
        const nodes = coverageEnabled ? (g.nodes ?? []) : (g.nodes ?? []).map((n) => ({ ...n, tested: false }));
        setData({ nodes, edges: g.edges ?? [] });
      })
      .catch(() => {
        /* Keep the previous data */
      })
      .finally(() => setLoading(false));
  }, [taskId, coverageEnabled]);

  React.useEffect(() => {
    fetchGraph();
  }, [fetchGraph]);

  const { renderNodes, renderEdges } = React.useMemo(
    () =>
      data
        ? computeVisible(data.nodes, data.edges, expanded)
        : { renderNodes: [] as RenderNode[], renderEdges: [] as CoverageGraphEdge[] },
    [data, expanded],
  );

  // Structural signature: rebuild the graph + rerun layout only when the visible node/edge set changes, avoiding pointless churn.
  const sig = React.useMemo(
    () =>
      `${renderNodes
        .map((n) => `${n.key}:${n.fold ? "f" : n.node.tested ? "t" : "u"}`)
        .sort()
        .join(",")}|${renderEdges.length}`,
    [renderNodes, renderEdges],
  );

  // Keep gDataRef + renderMapRef up to date (read by events and graph-data application).
  gDataRef.current = {
    nodes: toG6Nodes(renderNodes, kindLabel, moreLabel),
    edges: renderEdges.map((e) => ({ source: e.src, target: e.dst })),
  };
  const rmap = new Map<string, RenderNode>();
  for (const rn of renderNodes) rmap.set(rn.key, rn);
  renderMapRef.current = rmap;

  const applyData = React.useCallback(() => {
    const graph = graphRef.current;
    if (!graph || graph.destroyed) return;
    graph.setData(gDataRef.current);
    // render() runs the d3-force layout asynchronously; if the component unmounts or is
    // destroyed before the layout settles, g6 touches transform on a cleared context and
    // throws (see runtime/layout transformDataAfterLayout). That is a pure teardown race,
    // so swallow it without affecting behaviour; real render errors (graph not destroyed) still log.
    void graph.render().catch((err) => {
      if (!graph.destroyed) console.error("[coverage-graph] render:", err);
    });
  }, []);

  // Build the graph (once). Dynamic import avoids the window dependency during SSR/static export.
  React.useEffect(() => {
    let destroyed = false;
    let graph: G6Graph | null = null;
    void (async () => {
      const { Graph } = await import("@antv/g6");
      if (destroyed || !containerRef.current) return;
      graph = new Graph({
        container: containerRef.current,
        autoResize: true,
        autoFit: "view",
        background: "#f0f2f7",
        node: {
          style: {
            size: (d: unknown) => nd(d).size,
            fill: (d: unknown) => nodeFill(nd(d)),
            stroke: (d: unknown) => nodeStroke(nd(d)),
            lineWidth: (d: unknown) => (nd(d).fold || !nd(d).inScope ? 1 : 1.5),
            lineDash: (d: unknown) => (nd(d).fold || !nd(d).inScope ? [3, 3] : [0]),
            iconSrc: (d: unknown) => (nd(d).fold ? FOLD_ICON : KIND_ICON[nd(d).kind]),
            iconWidth: (d: unknown) => Math.max(12, nd(d).size * 0.55),
            iconHeight: (d: unknown) => Math.max(12, nd(d).size * 0.55),
            labelText: (d: unknown) => nd(d).lbl,
            labelFontSize: 10,
            labelPlacement: "bottom",
            labelFill: "#475569",
            labelBackground: true,
            labelBackgroundFill: "rgba(255,255,255,0.75)",
            labelBackgroundRadius: 3,
            labelPadding: [1, 3],
          },
        },
        edge: {
          style: { stroke: "#cbd5e1", lineWidth: 1, endArrow: false },
        },
        layout: {
          type: "d3-force",
          collide: { radius: (d: unknown) => (nd(d).size ? nd(d).size : 20) + 8 },
          link: {
            distance: (edge: unknown) => {
              // Top-level nodes (company/root domain) sit far from children, leaves closer. edge.source is an id or an already-resolved node.
              const s = (edge as { source: string | { id?: string } }).source;
              const srcId = typeof s === "string" ? s : (s?.id ?? "");
              const src = renderMapRef.current.get(srcId);
              const k = src && !src.fold ? src.kind : "endpoint";
              return k === "company" || k === "root_domain" ? 120 : 60;
            },
          },
          manyBody: {
            strength: (d: unknown) => (nd(d).kind === "endpoint" || nd(d).fold ? -60 : -200),
          },
        },
        behaviors: ["drag-element-force", "drag-canvas", "zoom-canvas"],
      });
      graph.on("node:click", (evt: unknown) => {
        const id = (evt as { target?: { id?: string } }).target?.id;
        if (!id) return;
        const rn = renderMapRef.current.get(id);
        if (!rn) return;
        if (rn.fold) setSelectedFoldId(rn.key);
        else setSelectedAsset(rn.node);
      });
      graphRef.current = graph;
      applyData();
    })();
    return () => {
      destroyed = true;
      // Stop the layout before destroying: keep the "layout in flight, context cleared" race window as short as possible.
      try {
        graph?.stopLayout();
      } catch {
        /* The graph may not exist yet or has no layout context */
      }
      graph?.destroy();
      graphRef.current = null;
    };
  }, [applyData]);

  // Visible set changed → refill data + layout. sig only triggers the relayout (applyData reads gDataRef).
  // biome-ignore lint/correctness/useExhaustiveDependencies: sig is a deliberate relayout trigger dependency
  React.useEffect(() => {
    applyData();
  }, [sig, applyData]);

  const showMore = React.useCallback((groupId: string) => {
    setExpanded((prev) => {
      const m = new Map(prev);
      m.set(groupId, (m.get(groupId) ?? FOLD_LIMIT) + FOLD_STEP);
      return m;
    });
  }, []);

  const selectedFold =
    selectedFoldId != null
      ? ((renderNodes.find((n) => n.fold && n.key === selectedFoldId) as FoldNode | undefined) ?? null)
      : null;
  React.useEffect(() => {
    if (selectedFoldId != null && !selectedFold) setSelectedFoldId(null);
  }, [selectedFoldId, selectedFold]);

  const total = data?.nodes.length ?? 0;
  const tested = data?.nodes.filter((n) => n.in_scope && n.tested).length ?? 0;
  const inScope = data?.nodes.filter((n) => n.in_scope).length ?? 0;

  return (
    <div className="relative h-full w-full">
      <div ref={containerRef} className="h-full w-full" />

      {/* Legend + stats + refresh (overlay) */}
      <div className="bg-card/95 pointer-events-auto absolute top-3 left-3 flex max-w-[340px] flex-col gap-2.5 rounded-lg border p-3 text-xs shadow-sm backdrop-blur">
        <div className="flex items-center justify-between gap-3">
          {total > 0 ? (
            <span className="text-muted-foreground">
              {t("graph.scopeIn")} <span className="text-foreground font-semibold tabular-nums">{inScope}</span>
              {coverageEnabled && (
                <>
                  {" "}
                  · {t("graph.tested")}{" "}
                  <span className="font-semibold tabular-nums text-emerald-600 dark:text-emerald-400">{tested}</span>
                </>
              )}
            </span>
          ) : (
            <span className="text-muted-foreground">{loading ? t("graph.loading") : t("graph.empty")}</span>
          )}
          <Button variant="ghost" size="icon" className="size-6" onClick={fetchGraph} title={t("graph.refresh")}>
            <RefreshCw className={cn("size-3.5", loading && "animate-spin")} />
          </Button>
        </div>
        <div className="flex flex-wrap gap-x-3 gap-y-1.5">
          {(Object.keys(kindMeta) as Kind[]).map((k) => {
            const m = kindMeta[k];
            const Icon = m.icon;
            return (
              <span key={k} className="text-foreground inline-flex items-center gap-1.5">
                <span className={cn("flex size-4 items-center justify-center rounded", m.iconBg)}>
                  <Icon className="size-2.5 text-white" />
                </span>
                {t(`kind.${k}`)}
              </span>
            );
          })}
        </div>
        <div className="border-border/60 text-muted-foreground flex flex-wrap gap-x-3 gap-y-1.5 border-t pt-2">
          {coverageEnabled && (
            <>
              <span className="inline-flex items-center gap-1.5">
                <span className="size-3 rounded-full bg-emerald-500" /> {t("graph.legendTested")}
              </span>
              <span className="inline-flex items-center gap-1.5">
                <span className="size-3 rounded-full bg-neutral-400" /> {t("graph.legendUntested")}
              </span>
            </>
          )}
          <span className="inline-flex items-center gap-1.5">
            <span className="size-3 rounded-full border border-dashed border-neutral-400 bg-neutral-200" />{" "}
            {t("graph.legendOutOfScope")}
          </span>
        </div>
        <p className="text-muted-foreground/80 border-border/60 border-t pt-2 leading-relaxed">{t("graph.hint")}</p>
      </div>

      <AssetSheet node={selectedAsset} taskId={taskId} onOpenChange={(o) => !o && setSelectedAsset(null)} />
      <FoldSheet
        fold={selectedFold}
        onOpenChange={(o) => !o && setSelectedFoldId(null)}
        onShowMore={showMore}
        onPick={(n) => {
          setSelectedFoldId(null);
          setSelectedAsset(n);
        }}
      />
    </div>
  );
}

export function CoverageGraphTab({ taskId, coverageEnabled = true }: { taskId: string; coverageEnabled?: boolean }) {
  return (
    <Card>
      <CardContent className="p-0">
        <div className="h-[72vh] w-full overflow-hidden rounded-xl">
          <GraphInner taskId={taskId} coverageEnabled={coverageEnabled} />
        </div>
      </CardContent>
    </Card>
  );
}
