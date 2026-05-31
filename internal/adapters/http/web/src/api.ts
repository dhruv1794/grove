// Typed client for the grove web API. Shapes mirror the Go JSON in
// internal/adapters/http (lowercase) and internal/grove.

export interface SourceStatus {
  name: string;
  type: string;
  doc_count: number;
  last_sync_at: string;
}

export interface Status {
  workspace: string;
  config: {
    build?: { model?: string };
    query?: { model?: string };
  };
  sources: SourceStatus[];
}

export interface TreeNode {
  id: string;
  title: string;
  depth: number;
  is_target?: boolean;
  children?: TreeNode[];
}

export interface ForestTree {
  id: string;
  name: string;
  source: string;
  doc_count: number;
  node_count: number;
  root: TreeNode;
}

export interface GraphNode {
  id: string;
  title: string;
  kind: "root" | "dir" | "topic" | "leaf";
  tree: string;
  depth: number;
  size: number;
  doc_id?: string;
}

export interface GraphLink {
  source: string;
  target: string;
  kind: "child" | "seealso";
  strength?: number;
}

export interface Graph {
  nodes: GraphNode[];
  links: GraphLink[];
}

export interface Doc {
  id: string;
  source: string;
  source_ref: string;
  title: string;
  content: string;
  hierarchy?: string[];
}

export interface Citation {
  n: number;
  doc_id: string;
  title: string;
  source: string;
  source_ref: string;
  location?: string;
  cross_link?: boolean;
}

export interface Cost {
  calls: number;
  usage: { prompt_tokens: number; completion_tokens: number; total_tokens: number };
  usd: number;
  estimated: boolean;
}

export interface AskResult {
  answer: string;
  citations: Citation[] | null;
  retrieval_trace: { search_path: { tree: string; node: string; node_id?: string; reason: string }[] | null };
  retrieval_nodes?: string[] | null;
  cost: Cost;
  abstained?: boolean;
}

export interface AskRequest {
  query: string;
  mode: string;
  source?: string;
  retrieve_only?: boolean;
}

export interface AskHandlers {
  token: (t: string) => void;
  done: (r: AskResult) => void;
  error: (msg: string) => void;
}

// postSSE POSTs a JSON body and consumes the resulting SSE stream, dispatching
// each (event, data) to onEvent. EventSource only supports GET, so we read the
// fetch body stream and parse SSE by hand. Returns after the stream ends.
async function postSSE(
  path: string,
  body: unknown,
  onEvent: (event: string, data: unknown) => void,
  onError: (msg: string) => void,
  signal?: AbortSignal,
): Promise<void> {
  let res: Response;
  try {
    res = await fetch(path, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
      signal,
    });
  } catch {
    onError(BACKEND_DOWN); // network error: backend unreachable
    return;
  }
  if (!res.ok || !res.body) {
    onError(await errorMessage(res));
    return;
  }

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    let idx: number;
    while ((idx = buf.indexOf("\n\n")) !== -1) {
      const block = buf.slice(0, idx);
      buf = buf.slice(idx + 2);
      let event = "message";
      let data = "";
      for (const line of block.split("\n")) {
        if (line.startsWith("event:")) event = line.slice(6).trim();
        else if (line.startsWith("data:")) data += line.slice(5).trim();
      }
      if (!data) continue;
      try {
        onEvent(event, JSON.parse(data));
      } catch {
        /* skip malformed event */
      }
    }
  }
}

// streamAsk POSTs a question and consumes the SSE stream: `token` events feed
// `token`, then a terminal `done` (full result) or `error`.
export async function streamAsk(req: AskRequest, on: AskHandlers, signal?: AbortSignal): Promise<void> {
  await postSSE(
    "/api/ask",
    req,
    (event, data) => {
      if (event === "token") on.token((data as { text: string }).text);
      else if (event === "done") on.done(data as AskResult);
      else if (event === "error") on.error((data as { message: string }).message);
    },
    on.error,
    signal,
  );
}

export interface ProgressEvent {
  op: string;
  source: string;
  total: number;
  done: number;
  doc?: string;
}

export interface OpHandlers {
  progress: (p: ProgressEvent) => void;
  done: (result: unknown) => void;
  error: (msg: string) => void;
}

// streamOp drives a long mutation (build/sync/embed/connect): `progress` events
// feed `progress`, then a terminal `done` (result) or `error`.
export async function streamOp(path: string, body: unknown, on: OpHandlers, signal?: AbortSignal): Promise<void> {
  await postSSE(
    path,
    body,
    (event, data) => {
      if (event === "progress") on.progress(data as ProgressEvent);
      else if (event === "done") on.done(data);
      else if (event === "error") on.error((data as { message: string }).message);
    },
    on.error,
    signal,
  );
}

// leafNodeId reconstructs a leaf node's id from a citation: leaf ids are
// "<tree>:doc:<doc-id>" and the tree id equals the source name (see core.Node /
// indexer.assembleTree).
export function leafNodeId(source: string, docId: string): string {
  return `${source}:doc:${docId}`;
}

// pathToNode returns the ids from the forest root down to targetId (inclusive),
// or null if the target isn't under root — used to expand the tree to a citation.
export function pathToNode(root: TreeNode, targetId: string): string[] | null {
  if (root.id === targetId) return [root.id];
  for (const c of root.children ?? []) {
    const sub = pathToNode(c, targetId);
    if (sub) return [root.id, ...sub];
  }
  return null;
}

interface ApiError {
  code: string;
  message: string;
  remediation?: string;
}

const BACKEND_DOWN =
  "Can't reach the grove backend. Is it running? Start it with `grove serve --web` (or `make dev` for hot-reload).";

// errorMessage turns a failed Response into a user-facing message. The grove
// API always returns a JSON {code,message,remediation} envelope, so a non-JSON
// body on a 5xx means the request never reached the backend — e.g. the Vite dev
// proxy 500s when nothing is listening on :8799. Surface that as actionable
// guidance instead of a bare "500 Internal Server Error".
async function errorMessage(r: Response): Promise<string> {
  const e = (await r.json().catch(() => null)) as ApiError | null;
  if (e && e.message) return e.remediation ? `${e.message} — ${e.remediation}` : e.message;
  if (r.status >= 500) return BACKEND_DOWN;
  return `${r.status} ${r.statusText}`;
}

async function get<T>(path: string): Promise<T> {
  let r: Response;
  try {
    r = await fetch(path);
  } catch {
    throw new Error(BACKEND_DOWN); // network error: backend unreachable
  }
  if (!r.ok) throw new Error(await errorMessage(r));
  return r.json() as Promise<T>;
}

export const api = {
  status: () => get<Status>("/api/status"),
  trees: () => get<ForestTree[]>("/api/trees"),
  graph: () => get<Graph>("/api/graph"),
  tree: (id: string) => get<ForestTree>(`/api/tree/${encodeURIComponent(id)}`),
  doc: (id: string) => get<Doc>(`/api/doc/${encodeURIComponent(id)}`),
};

// docIdFromNode derives a leaf node's document id. Leaf node ids are
// "<tree>:doc:<doc-id>" (see core.Node); return null for internal nodes.
export function docIdFromNode(nodeId: string): string | null {
  const marker = ":doc:";
  const i = nodeId.indexOf(marker);
  return i === -1 ? null : nodeId.slice(i + marker.length);
}
