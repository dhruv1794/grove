import { type Doc } from "./api";

interface Props {
  open: boolean;
  doc: Doc | null;
  loading: boolean;
  error: string | null;
  onClose: () => void;
}

export function DocDrawer({ open, doc, loading, error, onClose }: Props) {
  if (!open) return null;
  return (
    <div className="drawer">
      <div className="drawer-head">
        <span className="drawer-title">{doc?.title || (loading ? "loading…" : "document")}</span>
        <button className="drawer-close" onClick={onClose} title="close">
          ✕
        </button>
      </div>
      <div className="drawer-body">
        {loading && <p className="loading">loading document…</p>}
        {error && <p className="err">{error}</p>}
        {doc && (
          <>
            <div className="ref">
              {doc.source} · {doc.source_ref}
            </div>
            <pre>{doc.content}</pre>
          </>
        )}
      </div>
    </div>
  );
}
