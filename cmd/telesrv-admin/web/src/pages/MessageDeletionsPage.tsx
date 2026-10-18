import { ArrowLeft, ChevronRight, Search } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { Alert, Badge, EmptyRow, JsonBlock, LoadingSurface, Metric, PageFrame, QueryPanel, SectionHead, Summary } from "../components/ui";
import { useI18n, type TFunction } from "../i18n";
import { formatUnix, toUnixSeconds } from "../lib/format";
import type { Navigate } from "../routing";
import type { MessageDeletionDetail, MessageDeletionListResponse, MessageDeletionRow } from "../types";

const sources = ["user", "history_clear", "saved_history", "topic_history", "participant_history", "discussion_cascade", "ttl", "moderation", "admin", "bot"];

// The filters a link may preset through the query string, so a message page
// can open the log already narrowed to that message or chat.
const filterParams = ["actor_user_id", "owner_user_id", "sender_user_id", "peer_type", "peer_id", "message_id", "source"] as const;
type FilterParam = (typeof filterParams)[number];

export function deletionActor(row: MessageDeletionRow, t: TFunction): string {
  switch (row.ActorType) {
    case "operator":
      return t("deletions.actorOperator", { operator: row.OperatorID || "-" });
    case "system":
      return t("deletions.actorSystem");
    case "bot":
      return t("deletions.actorBot", { id: row.ActorUserID });
    default:
      return String(row.ActorUserID);
  }
}

export function deletionPeer(row: MessageDeletionRow, t: TFunction): string {
  return row.PeerType === "channel"
    ? t("deletions.peerChannel", { id: row.PeerID })
    : t("deletions.peerPrivate", { owner: row.OwnerUserID, peer: row.PeerID });
}

function deletionDevice(row: MessageDeletionRow): string {
  const device = [row.DeviceModel, row.Platform].filter(Boolean).join(" · ");
  return [device, row.IP].filter(Boolean).join(" / ") || "-";
}

function scopeTone(scope: string): "danger" | "warn" | "neutral" {
  if (scope === "both" || scope === "everyone") return "danger";
  return "neutral";
}

export function MessageDeletionsTab({ navigate, search }: { navigate: Navigate; search: URLSearchParams }) {
  const { t } = useI18n();
  const [filters, setFilters] = useState<Record<FilterParam, string>>(() => {
    const preset = {} as Record<FilterParam, string>;
    for (const key of filterParams) preset[key] = search.get(key) ?? "";
    return preset;
  });
  const [minDate, setMinDate] = useState("");
  const [maxDate, setMaxDate] = useState("");
  const [limit, setLimit] = useState("100");
  const [data, setData] = useState<MessageDeletionListResponse | null>(null);
  const [cursor, setCursor] = useState<number[]>([]);
  const [error, setError] = useState("");

  function setFilter(key: FilterParam, value: string) {
    setFilters((current) => ({ ...current, [key]: value }));
  }

  async function load(beforeID = 0) {
    setError("");
    const params = new URLSearchParams({ limit });
    for (const key of filterParams) {
      const value = filters[key].trim();
      if (value) params.set(key, value);
    }
    const from = toUnixSeconds(minDate);
    const to = toUnixSeconds(maxDate);
    if (from) params.set("min_date", String(from));
    if (to) params.set("max_date", String(to));
    if (beforeID) params.set("before_id", String(beforeID));
    try {
      setData(await api.messageDeletions(params));
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  useEffect(() => {
    void load();
  }, []);

  function nextPage() {
    const rows = data?.rows ?? [];
    if (!rows.length) return;
    const last = rows[rows.length - 1].ID;
    setCursor((stack) => [...stack, last]);
    void load(last);
  }

  function firstPage() {
    setCursor([]);
    void load();
  }

  const rows = data?.rows ?? [];
  const pageSize = Number(limit) || 100;

  return (
    <>
      {error && <Alert>{error}</Alert>}
      <QueryPanel>
        <form className="toolbar message-query deletion-filters" onSubmit={(event) => { event.preventDefault(); firstPage(); }}>
          <input value={filters.actor_user_id} onChange={(event) => setFilter("actor_user_id", event.target.value)} placeholder={t("deletions.actorPlaceholder")} />
          <input value={filters.sender_user_id} onChange={(event) => setFilter("sender_user_id", event.target.value)} placeholder={t("deletions.senderPlaceholder")} />
          <input value={filters.owner_user_id} onChange={(event) => setFilter("owner_user_id", event.target.value)} placeholder={t("deletions.ownerPlaceholder")} />
          <select value={filters.peer_type} onChange={(event) => setFilter("peer_type", event.target.value)} aria-label={t("deletions.peerType")}>
            <option value="">{t("deletions.anyChat")}</option>
            <option value="user">{t("deletions.privateChats")}</option>
            <option value="channel">{t("deletions.groupChats")}</option>
          </select>
          <input value={filters.peer_id} onChange={(event) => setFilter("peer_id", event.target.value)} placeholder={t("deletions.peerPlaceholder")} />
          <input className="small-input" value={filters.message_id} onChange={(event) => setFilter("message_id", event.target.value)} placeholder={t("common.messageId")} />
          <select value={filters.source} onChange={(event) => setFilter("source", event.target.value)} aria-label={t("deletions.source")}>
            <option value="">{t("deletions.anySource")}</option>
            {sources.map((source) => <option key={source} value={source}>{t(`deletions.source.${source}`)}</option>)}
          </select>
          <label className="inline-field">{t("deletions.from")} <input type="datetime-local" value={minDate} onChange={(event) => setMinDate(event.target.value)} /></label>
          <label className="inline-field">{t("deletions.to")} <input type="datetime-local" value={maxDate} onChange={(event) => setMaxDate(event.target.value)} /></label>
          <input className="small-input" value={limit} onChange={(event) => setLimit(event.target.value)} placeholder={t("messages.limitPlaceholder")} />
          <button className="btn primary icon-text" type="submit"><Search size={15} /> {t("deletions.search")}</button>
          {cursor.length ? <button className="btn" type="button" onClick={firstPage}>{t("deletions.firstPage")}</button> : null}
          {rows.length >= pageSize ? <button className="btn icon-text" type="button" onClick={nextPage}><ChevronRight size={15} /> {t("messages.nextPage")}</button> : null}
        </form>
      </QueryPanel>
      <div className="metric-row">
        <Metric label={t("messages.currentPage")} value={String(rows.length)} />
        <Metric label={t("deletions.removedForOthers")} value={String(rows.filter((row) => row.Scope !== "self").length)} tone="danger" />
        <Metric label={t("deletions.byStaff")} value={String(rows.filter((row) => row.ActorType === "operator").length)} />
        <Metric label={t("deletions.automatic")} value={String(rows.filter((row) => row.ActorType === "system").length)} />
      </div>
      <div className="table-wrap">
        <table className="data-table">
          <thead>
            <tr>
              <th>{t("deletions.deletedAt")}</th>
              <th>{t("deletions.deletedBy")}</th>
              <th>{t("deletions.source")}</th>
              <th>{t("deletions.scope")}</th>
              <th>{t("deletions.chat")}</th>
              <th>{t("common.messageId")}</th>
              <th>{t("common.sender")}</th>
              <th>{t("messages.body")}</th>
              <th>{t("deletions.device")}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.ID}>
                <td>{formatUnix(row.Date)}</td>
                <td className="mono">{deletionActor(row, t)}</td>
                <td>{t(`deletions.source.${row.Source}`)}</td>
                <td><Badge tone={scopeTone(row.Scope)}>{t(`deletions.scope.${row.Scope}`)}</Badge></td>
                <td className="mono">{deletionPeer(row, t)}</td>
                <td className="mono">{row.MessageID}</td>
                <td className="mono">{row.SenderUserID}</td>
                <td className="truncate">{row.Body}</td>
                <td className="truncate">{deletionDevice(row)}</td>
                <td>
                  <button className="row-link" onClick={() => navigate(`/messages/deletions/detail?id=${row.ID}`)}>
                    {t("common.detail")} <ChevronRight size={14} />
                  </button>
                </td>
              </tr>
            ))}
            {rows.length === 0 && <EmptyRow colSpan={10} />}
          </tbody>
        </table>
      </div>
    </>
  );
}

export function MessageDeletionDetailPage({ id, navigate }: { id: number; navigate: Navigate }) {
  const { t } = useI18n();
  const [detail, setDetail] = useState<MessageDeletionDetail | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    setError("");
    setDetail(null);
    api.messageDeletion(id).then(setDetail, (err) => setError(errorMessage(err)));
  }, [id]);

  if (error) {
    return <Alert>{error}</Alert>;
  }
  if (!detail) {
    return <LoadingSurface label={t("common.loading")} />;
  }

  const row = detail.Deletion;
  return (
    <PageFrame
      title={t("deletions.detailTitle", { id: row.MessageID })}
      eyebrow={t("deletions.detailEyebrow")}
      actions={<button className="btn icon-text" onClick={() => navigate("/messages/deletions")}><ArrowLeft size={15} /> {t("deletions.back")}</button>}
    >
      <div className="stacked-sections">
        <section className="entity-head">
          <div>
            <div className="entity-title">{deletionPeer(row, t)}</div>
            <div className="entity-subtitle">{t("deletions.detailSubtitle", { actor: deletionActor(row, t), date: formatUnix(row.Date) })}</div>
          </div>
          <div className="entity-badges">
            <Badge tone={scopeTone(row.Scope)}>{t(`deletions.scope.${row.Scope}`)}</Badge>
            <Badge>{t(`deletions.source.${row.Source}`)}</Badge>
          </div>
        </section>
        <div className="summary-grid">
          <Summary label={t("deletions.deletedBy")} value={deletionActor(row, t)} mono />
          <Summary label={t("deletions.deletedAt")} value={formatUnix(row.Date)} />
          <Summary label={t("common.sender")} value={String(row.SenderUserID)} mono />
          <Summary label={t("deletions.sentAt")} value={formatUnix(row.MessageDate)} />
          <Summary label={t("deletions.device")} value={deletionDevice(row)} />
          <Summary label={t("deletions.session")} value={row.OriginAuthKeyID === "0" ? "-" : `${row.OriginAuthKeyID} / ${row.OriginSessionID}`} mono />
          {row.ActorType === "operator" && <Summary label={t("deletions.command")} value={row.CommandID || "-"} mono />}
          {row.ActorType === "operator" && <Summary label={t("deletions.reason")} value={row.Reason || "-"} />}
        </div>
        <section className="section-block">
          <SectionHead title={t("deletions.content")} text={t("deletions.contentHint")} />
          <div className="deleted-body">{row.Body || t("deletions.noText")}</div>
          <JsonBlock value={detail.SnapshotJSON} />
        </section>
        <section className="section-block">
          <SectionHead title={t("deletions.batch")} text={t("deletions.batchHint", { count: detail.Batch.length })} />
          <div className="table-wrap">
            <table className="data-table">
              <thead><tr><th>{t("deletions.chat")}</th><th>{t("common.messageId")}</th><th>{t("common.sender")}</th><th>{t("messages.body")}</th><th></th></tr></thead>
              <tbody>
                {detail.Batch.map((item) => (
                  <tr key={item.ID}>
                    <td className="mono">{deletionPeer(item, t)}</td>
                    <td className="mono">{item.MessageID}</td>
                    <td className="mono">{item.SenderUserID}</td>
                    <td className="truncate">{item.Body}</td>
                    <td>
                      {item.ID !== row.ID && (
                        <button className="row-link" onClick={() => navigate(`/messages/deletions/detail?id=${item.ID}`)}>
                          {t("common.detail")} <ChevronRight size={14} />
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
                {detail.Batch.length === 0 && <EmptyRow colSpan={5} />}
              </tbody>
            </table>
          </div>
        </section>
      </div>
    </PageFrame>
  );
}
