import { Loader2, Search, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { api, errorMessage } from "../../api";
import { Alert } from "../../components/ui";
import { useI18n } from "../../i18n";
import { dialogTime, mediaLabel } from "../../lib/auditTimeline";
import type { AuditDialogRow } from "../../types";
import { Avatar } from "../../components/Avatar";

const pageSize = 100;
const kinds = ["", "private", "group", "channel"] as const;

type Props = {
  ownerID: number;
  selected: { type: string; id: number } | null;
  onSelect: (row: AuditDialogRow) => void;
  // onRows 把已加载的会话交给父组件，让从链接直接打开的会话也能显示名称。
  onRows: (rows: AuditDialogRow[]) => void;
};

export function AuditDialogList({ ownerID, selected, onSelect, onRows }: Props) {
  const { t } = useI18n();
  const [kind, setKind] = useState<(typeof kinds)[number]>("");
  const [query, setQuery] = useState("");
  const [debounced, setDebounced] = useState("");
  const [rows, setRows] = useState<AuditDialogRow[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const generation = useRef(0);

  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(query.trim()), 300);
    return () => window.clearTimeout(timer);
  }, [query]);

  async function load(offset: number) {
    const gen = offset === 0 ? ++generation.current : generation.current;
    setLoading(true);
    setError("");
    const params = new URLSearchParams({ owner_user_id: String(ownerID), limit: String(pageSize), offset: String(offset) });
    if (kind) params.set("kind", kind);
    if (debounced) params.set("q", debounced);
    try {
      const result = await api.auditDialogs(params);
      if (gen !== generation.current) return;
      const next = offset === 0 ? result.rows : [...rows, ...result.rows];
      setRows(next);
      setHasMore(result.rows.length === pageSize);
      onRows(next);
    } catch (err) {
      if (gen === generation.current) setError(errorMessage(err));
    } finally {
      if (gen === generation.current) setLoading(false);
    }
  }

  useEffect(() => {
    setRows([]);
    void load(0);
  }, [ownerID, kind, debounced]);

  function onScroll(event: React.UIEvent<HTMLDivElement>) {
    const el = event.currentTarget;
    if (hasMore && !loading && el.scrollHeight - el.scrollTop - el.clientHeight < 120) void load(rows.length);
  }

  return (
    <>
      <div className="audit-list-search">
        <Search size={14} />
        <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("audit.searchChats")} aria-label={t("audit.searchChats")} />
        {query && (
          <button type="button" className="icon-button" onClick={() => setQuery("")} aria-label={t("common.clear")}>
            <X size={14} />
          </button>
        )}
      </div>
      <div className="audit-kind-tabs" role="tablist">
        {kinds.map((value) => (
          <button key={value || "all"} type="button" role="tab" aria-selected={kind === value} className={kind === value ? "active" : ""} onClick={() => setKind(value)}>
            {t(`audit.kind.${value || "all"}`)}
          </button>
        ))}
      </div>
      {error && <Alert>{error}</Alert>}
      <div className="audit-list" onScroll={onScroll}>
        {rows.map((row) => {
          const active = selected?.type === row.PeerType && selected.id === row.PeerID;
          const preview = row.Preview || mediaLabel(row.PreviewMedia, t);
          return (
            <button key={`${row.PeerType}-${row.PeerID}`} type="button" className={`audit-dialog ${active ? "active" : ""}`} onClick={() => onSelect(row)}>
              <Avatar id={row.PeerID} kind={row.PeerType === "channel" ? "channel" : "user"} label={row.Title || String(row.PeerID)} size={42} />
              <span className="audit-dialog-body">
                <span className="audit-dialog-line">
                  <span className="audit-dialog-title">{row.Title || String(row.PeerID)}</span>
                  <span className="audit-dialog-id mono">{row.PeerID}</span>
                  <span className="audit-dialog-time">{dialogTime(row.TopDate, t)}</span>
                </span>
                <span className="audit-dialog-line">
                  <span className="audit-dialog-preview">
                    {row.Kind !== "private" && row.Kind !== "bot" && <span className="audit-dialog-kind">{t(`audit.kind.${row.Kind}`)} · </span>}
                    {row.PreviewOut && row.PreviewMedia !== "service" && <span className="audit-dialog-out">{t("audit.me")}: </span>}
                    {preview || <em>{t("audit.noMessages")}</em>}
                  </span>
                  {row.HasDeletions && (
                    <span className="audit-dialog-deleted" title={t("audit.hasDeletions")}>
                      {t("audit.deletedShort")}
                    </span>
                  )}
                  {row.UnreadCount > 0 && <span className="audit-dialog-unread">{row.UnreadCount > 999 ? "999+" : row.UnreadCount}</span>}
                </span>
              </span>
            </button>
          );
        })}
        {loading && (
          <div className="audit-edge">
            <Loader2 size={16} className="spin" />
          </div>
        )}
        {!loading && rows.length === 0 && !error && <div className="audit-empty">{t("audit.noChats")}</div>}
      </div>
    </>
  );
}
