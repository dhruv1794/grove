import { useCallback, useEffect, useState } from "react";
import {
  api,
  leafNodeId,
  pathToNode,
  type Status,
  type ForestTree,
  type Graph as GraphData,
  type Doc,
  type Citation,
  type TreeNode,
} from "./api";
import { TreeNodeView } from "./TreeView";
import { Ask } from "./Ask";
import { DocDrawer } from "./DocDrawer";
import { Actions } from "./Actions";
import { Graph } from "./Graph";

// collectOpenByDepth gathers node ids shallower than maxDepth, to expand the
// top of the forest on first load.
function collectOpenByDepth(node: TreeNode, maxDepth: number, acc: Set<string>) {
  if (node.depth < maxDepth && node.children?.length) acc.add(node.id);
  for (const c of node.children ?? []) collectOpenByDepth(c, maxDepth, acc);
}

export function App() {
  const [status, setStatus] = useState<Status | null>(null);
  const [trees, setTrees] = useState<ForestTree[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [selectedNode, setSelectedNode] = useState<string | null>(null);

  // Graph view is opt-in (its data is only fetched once the user switches to it,
  // keeping the default load light). litNodes drives the retrieval light-up;
  // litSeq retriggers the reveal animation on each ask.
  const [view, setViewState] = useState<"tree" | "graph">(
    () => (typeof location !== "undefined" && location.hash === "#graph" ? "graph" : "tree"),
  );
  const setView = (v: "tree" | "graph") => {
    setViewState(v);
    if (typeof history !== "undefined") history.replaceState(null, "", v === "graph" ? "#graph" : "#");
  };
  const [graph, setGraph] = useState<GraphData | null>(null);
  const [graphError, setGraphError] = useState<string | null>(null);
  const [litNodes, setLitNodes] = useState<Set<string>>(new Set());
  const [litSeq, setLitSeq] = useState(0);

  const [drawerOpen, setDrawerOpen] = useState(false);
  const [doc, setDoc] = useState<Doc | null>(null);
  const [docLoading, setDocLoading] = useState(false);
  const [docError, setDocError] = useState<string | null>(null);

  const refresh = useCallback(() => {
    api.status().then(setStatus).catch((e) => setError(String(e.message ?? e)));
    api
      .trees()
      .then((ts) => {
        setTrees(ts);
        setExpanded((prev) => {
          // Keep whatever the user had expanded; open the top levels of any
          // newly-built trees that weren't there before.
          const next = new Set(prev);
          for (const t of ts) collectOpenByDepth(t.root, 2, next);
          return next;
        });
      })
      .catch((e) => setError(String(e.message ?? e)));
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  // Lazily fetch the graph the first time the graph view is opened.
  useEffect(() => {
    if (view !== "graph" || graph) return;
    api
      .graph()
      .then(setGraph)
      .catch((e) => setGraphError(String(e.message ?? e)));
  }, [view, graph]);

  const toggle = (nodeId: string) =>
    setExpanded((s) => {
      const next = new Set(s);
      next.has(nodeId) ? next.delete(nodeId) : next.add(nodeId);
      return next;
    });

  const loadDoc = (docId: string, nodeId: string) => {
    setSelectedNode(nodeId);
    setDrawerOpen(true);
    setDoc(null);
    setDocError(null);
    setDocLoading(true);
    api
      .doc(docId)
      .then(setDoc)
      .catch((e) => setDocError(String(e.message ?? e)))
      .finally(() => setDocLoading(false));
    requestAnimationFrame(() => {
      document.querySelector(`[data-node="${cssEscape(nodeId)}"]`)?.scrollIntoView({ block: "center" });
    });
  };

  // onCite: clicking a citation expands the forest path to that document's leaf,
  // highlights it, and opens the doc drawer.
  const onCite = (c: Citation) => {
    const nodeId = leafNodeId(c.source, c.doc_id);
    const tree = trees?.find((t) => t.source === c.source || t.id === c.source);
    if (tree) {
      const path = pathToNode(tree.root, nodeId);
      if (path) setExpanded((s) => new Set([...s, ...path]));
    }
    loadDoc(c.doc_id, nodeId);
  };

  // onAskNodes: an ask finished — light up the nodes that produced the answer in
  // the graph view (descent path + cited leaves).
  const onAskNodes = (ids: string[]) => {
    setLitNodes(new Set(ids));
    setLitSeq((s) => s + 1);
  };

  const buildModel = status?.config?.build?.model;
  const queryModel = status?.config?.query?.model;

  return (
    <div className="app">
      <header className="bar">
        <h1>🌳 grove</h1>
        {status && <span className="ws">· {status.workspace}</span>}
        <div className="view-toggle" role="tablist" aria-label="view">
          <button className={view === "tree" ? "on" : ""} onClick={() => setView("tree")}>
            tree
          </button>
          <button className={view === "graph" ? "on" : ""} onClick={() => setView("graph")}>
            graph
          </button>
        </div>
        <span className="models">
          {queryModel && (
            <>
              <b>query</b> {queryModel}
            </>
          )}
          {buildModel && (
            <>
              {"   "}
              <b>build</b> {buildModel}
            </>
          )}
        </span>
      </header>

      <div className={`layout ${view === "graph" ? "graph-mode" : ""}`}>
        {view === "graph" ? (
          <div className="pane left graph-pane">
            {graphError && <div className="err">{graphError}</div>}
            {!graph && !graphError && <p className="loading">loading graph…</p>}
            {graph && (
              <Graph data={graph} litNodes={litNodes} litSeq={litSeq} onSelectLeaf={loadDoc} />
            )}
          </div>
        ) : (
          <div className="pane left">
            {error && <div className="err">{error}</div>}

            <Actions sources={status?.sources ?? []} onChanged={refresh} />

          <div className="section">
            <p className="section-title">Sources</p>
            {!status && !error && <p className="loading">loading…</p>}
            {status?.sources?.length === 0 && <p className="placeholder">no sources connected</p>}
            {status?.sources?.map((s) => (
              <div className="src" key={s.name}>
                <span>
                  {s.name} <span className="type">({s.type})</span>
                </span>
                <span className="count">{s.doc_count} docs</span>
              </div>
            ))}
          </div>

          <div className="section">
            <p className="section-title">Forest</p>
            {!trees && !error && <p className="loading">loading…</p>}
            {trees?.length === 0 && (
              <p className="placeholder">
                no built trees — run <code>grove build</code>
              </p>
            )}
            {trees?.map((t) => (
              <div key={t.id}>
                <div className="tree-name">
                  {t.name}
                  <small>
                    {t.node_count} nodes · {t.doc_count} docs
                  </small>
                </div>
                <TreeNodeView
                  node={t.root}
                  expanded={expanded}
                  selected={selectedNode}
                  onToggle={toggle}
                  onSelect={loadDoc}
                />
              </div>
            ))}
            </div>
          </div>
        )}

        <div className="pane right">
          <Ask source={null} onCite={onCite} onNodes={onAskNodes} />
        </div>
      </div>

      <DocDrawer
        open={drawerOpen}
        doc={doc}
        loading={docLoading}
        error={docError}
        onClose={() => setDrawerOpen(false)}
      />
    </div>
  );
}

// cssEscape is a minimal escape for attribute selectors (node ids contain ':'
// and '/'); CSS.escape exists in browsers but guard for older engines.
function cssEscape(s: string): string {
  return typeof CSS !== "undefined" && CSS.escape ? CSS.escape(s) : s.replace(/["\\]/g, "\\$&");
}
