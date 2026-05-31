import { useState } from "react";
import { streamOp, type ProgressEvent, type SourceStatus } from "./api";

type Kind = "connect" | "build" | "sync" | "embed";

interface Props {
  sources: SourceStatus[];
  onChanged: () => void; // refresh status + forest after a successful mutation
}

function summarize(kind: Kind, r: any): string {
  switch (kind) {
    case "connect":
      return `connected ${r.name}: ${r.doc_count} docs (${r.refetched} fetched, ${r.skipped} skipped)`;
    case "build":
      return `${r.trees} trees · ${r.nodes} nodes (${r.cache_hits} cached, ${r.cache_miss} generated) · ${r.tally?.calls ?? 0} calls · ${r.elapsed}`;
    case "sync": {
      const ch = (r.sources ?? []).reduce(
        (a: number, s: any) => a + s.created + s.modified + s.deleted,
        0,
      );
      return ch === 0 ? "up to date — nothing changed" : `synced: ${ch} doc change(s)` + (r.build ? `, rebuilt ${r.build.nodes} nodes` : "");
    }
    case "embed":
      return `embedded ${r.embedded} docs (${r.skipped} skipped${r.chunks_embedded ? `, ${r.chunks_embedded} chunks` : ""})`;
  }
}

export function Actions({ sources, onChanged }: Props) {
  const [open, setOpen] = useState<Kind | null>(null);
  const [running, setRunning] = useState(false);
  const [prog, setProg] = useState<ProgressEvent | null>(null);
  const [result, setResult] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  // form fields
  const [connType, setConnType] = useState("local");
  const [path, setPath] = useState("");
  const [name, setName] = useState("");
  const [model, setModel] = useState("");
  const [source, setSource] = useState("");
  const [rebuild, setRebuild] = useState(false);
  const [force, setForce] = useState(false);
  const [chunks, setChunks] = useState(false);

  const run = async (kind: Kind, apiPath: string, body: unknown) => {
    setRunning(true);
    setProg(null);
    setResult(null);
    setError(null);
    await streamOp(apiPath, body, {
      progress: setProg,
      done: (r) => {
        setResult(summarize(kind, r));
        setProg(null);
        onChanged();
      },
      error: setError,
    });
    setRunning(false);
  };

  const submit = () => {
    if (running || !open) return;
    if (open === "connect") run("connect", "/api/connect", { type: connType, path, name: name || undefined });
    else if (open === "build") run("build", "/api/build", { model: model || undefined, source: source || undefined, rebuild });
    else if (open === "sync") run("sync", "/api/sync", { model: model || undefined, source: source || undefined, force });
    else if (open === "embed") run("embed", "/api/embed", { model: model || undefined, source: source || undefined, chunks });
  };

  const toggle = (k: Kind) => {
    setOpen((o) => (o === k ? null : k));
    setResult(null);
    setError(null);
  };

  const pct = prog && prog.total > 0 ? Math.round((prog.done / prog.total) * 100) : 0;

  const sourceSelect = (
    <select value={source} onChange={(e) => setSource(e.target.value)} disabled={running}>
      <option value="">all sources</option>
      {sources.map((s) => (
        <option key={s.name} value={s.name}>
          {s.name}
        </option>
      ))}
    </select>
  );

  return (
    <div className="actions">
      <div className="action-tabs">
        {(["connect", "build", "sync", "embed"] as Kind[]).map((k) => (
          <button key={k} className={`atab ${open === k ? "on" : ""}`} onClick={() => toggle(k)} disabled={running}>
            {k}
          </button>
        ))}
      </div>

      {open && (
        <div className="action-form">
          {open === "connect" && (
            <>
              <select value={connType} onChange={(e) => setConnType(e.target.value)} disabled={running}>
                <option value="local">local</option>
                <option value="obsidian">obsidian</option>
                <option value="gdrive">gdrive</option>
                <option value="confluence">confluence</option>
              </select>
              <input placeholder="path (or folder)" value={path} onChange={(e) => setPath(e.target.value)} disabled={running} />
              <input placeholder="name (optional)" value={name} onChange={(e) => setName(e.target.value)} disabled={running} />
            </>
          )}
          {open === "build" && (
            <>
              {sourceSelect}
              <input placeholder="model (default: config build.model)" value={model} onChange={(e) => setModel(e.target.value)} disabled={running} />
              <label className="chk">
                <input type="checkbox" checked={rebuild} onChange={(e) => setRebuild(e.target.checked)} disabled={running} /> rebuild
              </label>
            </>
          )}
          {open === "sync" && (
            <>
              {sourceSelect}
              <input placeholder="model (default: config build.model)" value={model} onChange={(e) => setModel(e.target.value)} disabled={running} />
              <label className="chk">
                <input type="checkbox" checked={force} onChange={(e) => setForce(e.target.checked)} disabled={running} /> force
              </label>
            </>
          )}
          {open === "embed" && (
            <>
              {sourceSelect}
              <input placeholder="model (default: bge-m3)" value={model} onChange={(e) => setModel(e.target.value)} disabled={running} />
              <label className="chk">
                <input type="checkbox" checked={chunks} onChange={(e) => setChunks(e.target.checked)} disabled={running} /> chunks
              </label>
            </>
          )}
          <button className="run-btn" onClick={submit} disabled={running || (open === "connect" && !path)}>
            {running ? "running…" : `run ${open}`}
          </button>
        </div>
      )}

      {prog && (
        <div className="prog">
          <div className="prog-bar">
            <div className="prog-fill" style={{ width: `${pct}%` }} />
          </div>
          <div className="prog-label">
            {prog.op} {prog.source} · {prog.done}/{prog.total}
            {prog.doc ? ` · ${prog.doc}` : ""}
          </div>
        </div>
      )}
      {result && <div className="action-result">{result}</div>}
      {error && <div className="action-error">error: {error}</div>}
    </div>
  );
}
