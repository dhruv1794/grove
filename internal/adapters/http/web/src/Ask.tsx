import { useRef, useState, type KeyboardEvent } from "react";
import { streamAsk, type Citation, type Cost } from "./api";

interface Turn {
  id: number;
  q: string;
  mode: string;
  answer: string;
  citations: Citation[];
  abstained: boolean;
  cost?: Cost;
  streaming: boolean;
  error?: string;
}

const MODES = ["fast", "balanced", "quality", "deep"] as const;

// citedNums extracts the [n] markers an answer actually references (mirrors
// grove.CitedNums) so we can show only the sources the answer used.
function citedNums(answer: string): Set<number> {
  const out = new Set<number>();
  for (const m of answer.matchAll(/\[([\d,\s]+)\]/g)) {
    for (const f of m[1].split(/[\s,]+/)) {
      const n = parseInt(f, 10);
      if (!isNaN(n)) out.add(n);
    }
  }
  return out;
}

function visibleCitations(t: Turn): Citation[] {
  if (t.streaming) return [];
  if (t.abstained) return t.citations; // abstention lists the closest matches
  const cited = citedNums(t.answer);
  if (cited.size === 0) return t.citations; // GUI: show retrieved rather than nothing
  return t.citations.filter((c) => cited.has(c.n));
}

function costLine(c?: Cost): string {
  if (!c) return "";
  const usd = c.usd > 0 ? ` · $${c.usd.toFixed(4)}${c.estimated ? "*" : ""}` : "";
  return `${c.calls} calls · ${c.usage.prompt_tokens} in / ${c.usage.completion_tokens} out${usd}`;
}

interface Props {
  source: string | null;
  onCite: (c: Citation) => void;
  // onNodes fires when an answer completes with the node ids that produced it
  // (descent path + cited leaves), for the graph light-up.
  onNodes?: (nodeIds: string[]) => void;
}

export function Ask({ source, onCite, onNodes }: Props) {
  const [q, setQ] = useState("");
  const [mode, setMode] = useState<string>("balanced");
  const [retrieveOnly, setRetrieveOnly] = useState(false);
  const [turns, setTurns] = useState<Turn[]>([]);
  const [busy, setBusy] = useState(false);
  const nextId = useRef(1);
  const scrollRef = useRef<HTMLDivElement>(null);

  const update = (id: number, patch: Partial<Turn>) =>
    setTurns((ts) => ts.map((t) => (t.id === id ? { ...t, ...patch } : t)));

  const scrollDown = () =>
    requestAnimationFrame(() => {
      const el = scrollRef.current;
      if (el) el.scrollTop = el.scrollHeight;
    });

  const submit = async () => {
    const query = q.trim();
    if (!query || busy) return;
    const id = nextId.current++;
    const turn: Turn = {
      id,
      q: query,
      mode,
      answer: "",
      citations: [],
      abstained: false,
      streaming: true,
    };
    setTurns((ts) => [...ts, turn]);
    setQ("");
    setBusy(true);
    scrollDown();

    await streamAsk(
      { query, mode, source: source ?? undefined, retrieve_only: retrieveOnly },
      {
        token: (text) => {
          setTurns((ts) => ts.map((t) => (t.id === id ? { ...t, answer: t.answer + text } : t)));
          scrollDown();
        },
        done: (r) => {
          update(id, {
            streaming: false,
            answer: r.answer || "",
            citations: r.citations ?? [],
            abstained: !!r.abstained,
            cost: r.cost,
          });
          if (r.retrieval_nodes?.length) onNodes?.(r.retrieval_nodes);
          scrollDown();
        },
        error: (msg) => {
          update(id, { streaming: false, error: msg });
          scrollDown();
        },
      },
    );
    setBusy(false);
  };

  const onKey = (e: KeyboardEvent) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      submit();
    }
  };

  return (
    <div className="ask">
      <div className="ask-form">
        <textarea
          className="ask-input"
          placeholder="Ask the forest…  (Enter to send, Shift+Enter for newline)"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={onKey}
          rows={2}
        />
        <div className="ask-controls">
          <select value={mode} onChange={(e) => setMode(e.target.value)} title="retrieval mode">
            {MODES.map((m) => (
              <option key={m} value={m}>
                {m}
              </option>
            ))}
          </select>
          <label className="ro" title="return sources without synthesizing an answer">
            <input
              type="checkbox"
              checked={retrieveOnly}
              onChange={(e) => setRetrieveOnly(e.target.checked)}
            />
            retrieve-only
          </label>
          <button className="ask-btn" onClick={submit} disabled={busy || !q.trim()}>
            {busy ? "…" : "Ask"}
          </button>
        </div>
      </div>

      <div className="turns" ref={scrollRef}>
        {turns.length === 0 && (
          <p className="placeholder">Ask a question to search and synthesize a cited answer.</p>
        )}
        {turns.map((t) => (
          <div className="turn" key={t.id}>
            <div className="q">
              <span className="q-bar" /> {t.q}
            </div>
            {t.error ? (
              <div className="err">error: {t.error}</div>
            ) : (
              <div className={`a ${t.abstained ? "abstained" : ""}`}>
                {t.answer || (t.streaming ? <span className="loading">thinking…</span> : "")}
                {t.streaming && t.answer && <span className="cursor">▋</span>}
              </div>
            )}
            {visibleCitations(t).length > 0 && (
              <div className="cites">
                <div className="cites-title">sources</div>
                {visibleCitations(t).map((c) => (
                  <button className="cite" key={c.n} onClick={() => onCite(c)}>
                    <span className="cite-n">[{c.n}]</span>
                    <span className="cite-title">{c.title || c.source_ref}</span>
                    {c.cross_link && <span className="cite-xl">cross-link</span>}
                    <span className="cite-ref">{c.source_ref}</span>
                  </button>
                ))}
              </div>
            )}
            {!t.streaming && !t.error && t.cost && (
              <div className="meta">
                {t.mode} · {costLine(t.cost)}
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}
