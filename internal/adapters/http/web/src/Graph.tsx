import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import ForceGraph2D, { type ForceGraphMethods, type NodeObject } from "react-force-graph-2d";
import type { Graph as GraphData, GraphNode } from "./api";

// Luminous, forest-leaning palette — trees are assigned a color in appearance
// order. Greens/cyans lead (the grove identity); the rest distinguish multiple
// trees without leaving the glow aesthetic.
const TREE_COLORS = [
  "#a3be8c", // leaf green
  "#88c0d0", // sky
  "#8fbcbb", // teal
  "#ebcb8b", // amber
  "#b48ead", // mauve
  "#81a1c1", // steel
  "#d08770", // clay
  "#a1d99b",
];

const COL_BG = "#0c0e12";
const COL_CHILD = "rgba(122,130,144,0.18)"; // hierarchy edges, faint
const COL_SEEALSO = "rgba(180,142,173,0.30)"; // cross-links, mauve overlay
const COL_DIM = "rgba(120,130,145,0.10)"; // de-emphasized nodes during light-up

// Stable accessor so it isn't reallocated each render (called per node/frame).
const REPLACE_MODE = () => "replace" as const;

interface Props {
  data: GraphData;
  litNodes: Set<string>;
  litSeq: number; // increments each ask, retriggers the reveal animation
  onSelectLeaf: (docId: string, nodeId: string) => void;
}

type FGNode = NodeObject<GraphNode>;

// useSize tracks an element's pixel size (ForceGraph2D needs explicit w/h).
function useSize<T extends HTMLElement>() {
  const ref = useRef<T>(null);
  const [size, setSize] = useState({ w: 0, h: 0 });
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setSize({ w: el.clientWidth, h: el.clientHeight }));
    ro.observe(el);
    setSize({ w: el.clientWidth, h: el.clientHeight });
    return () => ro.disconnect();
  }, []);
  return [ref, size] as const;
}

function nodeRadius(n: GraphNode): number {
  // Hubs grow with subtree size (log-scaled, capped); leaves stay small.
  if (n.kind === "leaf") return 2;
  return Math.min(13, 3 + Math.log2(n.size + 1));
}

export function Graph({ data, litNodes, litSeq, onSelectLeaf }: Props) {
  const [wrapRef, size] = useSize<HTMLDivElement>();
  const fgRef = useRef<ForceGraphMethods<FGNode> | undefined>(undefined);
  const [showCrossLinks, setShowCrossLinks] = useState(false);
  const nowRef = useRef(0);
  const litAtRef = useRef(0);

  // Per-tree color, assigned in first-seen order.
  const treeColor = useMemo(() => {
    const m = new Map<string, string>();
    for (const n of data.nodes) if (!m.has(n.tree)) m.set(n.tree, TREE_COLORS[m.size % TREE_COLORS.length]);
    return (tree: string) => m.get(tree) ?? TREE_COLORS[0];
  }, [data]);

  // The simulation graph is hierarchy-only (clean tree clusters); cross-links
  // are drawn as a non-physical overlay so they don't collapse the layout into a
  // hairball. Cloned because ForceGraph2D mutates node/link objects in place.
  const graphData = useMemo(
    () => ({
      nodes: data.nodes.map((n) => ({ ...n })),
      links: data.links.filter((l) => l.kind === "child").map((l) => ({ source: l.source, target: l.target })),
    }),
    [data],
  );

  // Index of node id → live (post-layout) node object, for drawing the overlay
  // cross-links by coordinate. Rebuilt whenever the graph data changes.
  const nodeIndex = useMemo(() => {
    const m = new Map<string, FGNode>();
    for (const n of graphData.nodes) m.set(n.id!, n as FGNode);
    return m;
  }, [graphData]);

  const seeAlso = useMemo(() => data.links.filter((l) => l.kind === "seealso"), [data]);

  // During a light-up only the cross-links whose endpoints are both lit get
  // drawn — precompute that subset so the per-frame overlay iterates a handful
  // of edges instead of rescanning all ~thousands of seealso links each frame.
  const litCrossLinks = useMemo(
    () => (litNodes.size ? seeAlso.filter((l) => litNodes.has(l.source) && litNodes.has(l.target)) : []),
    [seeAlso, litNodes],
  );

  // Tune forces + fit once mounted/data-changed.
  useEffect(() => {
    const fg = fgRef.current;
    if (!fg) return;
    (fg.d3Force("charge") as unknown as { strength: (s: number) => unknown } | undefined)?.strength(-55);
    (fg.d3Force("link") as unknown as { distance: (d: number) => unknown } | undefined)?.distance(18);
  }, [graphData]);

  // Light-up: stamp the start time so nodeCanvasObject can cascade the reveal
  // root→leaf by depth, then gently frame the lit nodes.
  useEffect(() => {
    if (!litNodes.size) return;
    litAtRef.current = performance.now();
    const fg = fgRef.current;
    if (!fg) return;
    const t = setTimeout(() => {
      const lit = graphData.nodes.filter((n) => litNodes.has(n.id!));
      if (lit.length > 1) fg.zoomToFit(900, 80, (n) => litNodes.has((n as FGNode).id!));
    }, 350);
    return () => clearTimeout(t);
    // litSeq bumps on every ask (even a repeat of the same question), so it is
    // the sole retrigger; litNodes/graphData are read fresh from the closure.
  }, [litSeq]);

  const hasLit = litNodes.size > 0;

  // revealed returns how lit a node is in [0,1]: the reveal sweeps outward by
  // depth (≈160ms/level) so the highlight animates from the root to the leaves.
  const revealed = (n: GraphNode): number => {
    const elapsed = nowRef.current - litAtRef.current;
    const delay = n.depth * 160;
    if (elapsed < delay) return 0;
    return Math.min(1, (elapsed - delay) / 280);
  };

  const drawNode = (n: FGNode, ctx: CanvasRenderingContext2D, scale: number) => {
    const node = n as GraphNode & FGNode;
    const r = nodeRadius(node);
    const base = treeColor(node.tree);
    const lit = hasLit && litNodes.has(node.id!);
    const rev = lit ? revealed(node) : 0;
    ctx.save();
    if (hasLit && !lit) {
      ctx.globalAlpha = 1;
      ctx.fillStyle = COL_DIM;
    } else {
      ctx.fillStyle = base;
      if (lit) {
        const pulse = 0.55 + 0.45 * Math.sin(nowRef.current / 240 + node.depth * 0.6);
        ctx.shadowColor = base;
        ctx.shadowBlur = (10 + 16 * pulse) * Math.max(0.15, rev);
        ctx.globalAlpha = 0.35 + 0.65 * rev;
      } else if (node.kind !== "leaf") {
        ctx.shadowColor = base;
        ctx.shadowBlur = 5;
      }
    }
    ctx.beginPath();
    ctx.arc(node.x!, node.y!, lit ? r + 1.5 * rev : r, 0, 2 * Math.PI);
    ctx.fill();
    ctx.restore();

    // Label-cull: only hubs (and any lit node) get labels, and only once there's
    // room — big hubs always, smaller ones when zoomed in. Matches Obsidian's
    // "hide labels when zoomed out" behavior so 1.9k nodes stay legible.
    const showLabel = lit || (node.kind !== "leaf" && (node.size > 40 || scale > 1.8)) || scale > 4;
    if (!showLabel) return;
    const label = node.title ?? "";
    if (!label) return;
    const fontSize = Math.max(2.5, 11 / scale);
    ctx.font = `${fontSize}px ui-monospace, monospace`;
    ctx.textAlign = "left";
    ctx.textBaseline = "middle";
    ctx.globalAlpha = hasLit && !lit ? 0.25 : 1;
    ctx.fillStyle = lit ? "#f0f4ec" : "#9aa3b0";
    ctx.fillText(label.length > 38 ? label.slice(0, 37) + "…" : label, node.x! + r + 2, node.y!);
    ctx.globalAlpha = 1;
  };

  // drawSeg strokes one cross-link between two indexed nodes.
  const drawSeg = (ctx: CanvasRenderingContext2D, l: { source: string; target: string }, lit: boolean, dim: boolean) => {
    const a = nodeIndex.get(l.source);
    const b = nodeIndex.get(l.target);
    if (!a || a.x == null || !b || b.x == null) return;
    ctx.beginPath();
    ctx.moveTo(a.x, a.y!);
    ctx.lineTo(b.x!, b.y!);
    if (lit) {
      ctx.strokeStyle = "rgba(163,190,140,0.7)";
      ctx.lineWidth = 0.8;
    } else {
      ctx.strokeStyle = COL_SEEALSO;
      ctx.lineWidth = 0.4;
      if (dim) ctx.globalAlpha = 0.15;
    }
    ctx.stroke();
    ctx.globalAlpha = 1;
  };

  // drawCrossLinks overlays node_seealso edges beneath the nodes. When toggled
  // on, the whole graph is drawn (lit pairs highlighted); otherwise only the
  // lit pairs are drawn during a light-up — and that subset is precomputed, so
  // an idle or non-toggled graph does no per-frame seealso work.
  const drawCrossLinks = (ctx: CanvasRenderingContext2D) => {
    if (showCrossLinks) {
      for (const l of seeAlso) drawSeg(ctx, l, hasLit && litNodes.has(l.source) && litNodes.has(l.target), hasLit);
    } else if (hasLit) {
      for (const l of litCrossLinks) drawSeg(ctx, l, true, false);
    }
  };

  return (
    <div className="graph-wrap" ref={wrapRef}>
      <div className="graph-controls">
        <button
          className={`gctl ${showCrossLinks ? "on" : ""}`}
          onClick={() => setShowCrossLinks((v) => !v)}
          title="overlay node_seealso cross-links"
        >
          cross-links
        </button>
        <button className="gctl" onClick={() => fgRef.current?.zoomToFit(600, 60)} title="fit graph to view">
          fit
        </button>
        <span className="graph-legend">
          <i className="lg hub" /> hub
          <i className="lg leaf" /> doc
          {hasLit && (
            <>
              <i className="lg lit" /> answer
            </>
          )}
        </span>
      </div>
      {size.w > 0 && (
        <ForceGraph2D
          ref={fgRef}
          width={size.w}
          height={size.h}
          graphData={graphData}
          backgroundColor={COL_BG}
          nodeId="id"
          nodeLabel={(n) => `${(n as GraphNode).title} · ${(n as GraphNode).kind}`}
          nodeRelSize={1}
          linkColor={() => COL_CHILD}
          linkWidth={() => 0.5}
          cooldownTicks={120}
          onEngineStop={() => fgRef.current?.zoomToFit(600, 60)}
          onRenderFramePre={(ctx) => {
            nowRef.current = performance.now();
            drawCrossLinks(ctx);
          }}
          nodeCanvasObject={drawNode}
          nodeCanvasObjectMode={REPLACE_MODE}
          onNodeClick={(n) => {
            const node = n as GraphNode;
            if (node.doc_id) onSelectLeaf(node.doc_id, node.id);
          }}
        />
      )}
    </div>
  );
}
