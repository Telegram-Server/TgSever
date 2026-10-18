import { ArrowLeft, Info, Loader2, Repeat, Search, X } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../../api";
import { Avatar } from "../../components/Avatar";
import { UserPicker } from "../../components/EntityPicker";
import { Alert } from "../../components/ui";
import { useI18n } from "../../i18n";
import { dialogTime, mediaLabel } from "../../lib/auditTimeline";
import { permissionAccountsRead, permissionMessageDeletionsRead, usePermissions } from "../../permissions";
import type { Navigate } from "../../routing";
import type { AccountRow, AuditDialogRow, AuditMessage, AuditUser } from "../../types";
import { highlightText, userName } from "./AuditBubble";
import { AuditDialogList } from "./AuditDialogList";
import { AuditInspector, type InspectedMessage } from "./AuditInspector";
import { Lightbox } from "./AuditMedia";
import { AuditTranscript, type AuditPeer } from "./AuditTranscript";

// 会话审计页：以某个账号的视角看它的会话，已删除的消息照常显示并标出删除信息。
// 账号、会话和要定位的消息都放在查询参数里（owner / peer_type / peer_id / msg），
// 从其他页面跳进来、刷新和后退都能还原。

// 与 06-audit.css 中检查器变成抽屉的断点一致。
const inspectorDrawerWidth = 1180;

export function auditHref(owner: number, peer?: { type: string; id: number }, messageID?: number): string {
  const params = new URLSearchParams({ owner: String(owner) });
  if (peer) {
    params.set("peer_type", peer.type);
    params.set("peer_id", String(peer.id));
  }
  if (messageID) params.set("msg", String(messageID));
  return `/messages/audit?${params.toString()}`;
}

function minimalAccount(id: number): AccountRow {
  return { ID: id, FirstName: String(id), LastName: "", Phone: "", Username: "", Collectibles: [] } as unknown as AccountRow;
}

function placeholderDialog(type: "user" | "channel", id: number): AuditDialogRow {
  return {
    PeerType: type,
    PeerID: id,
    Kind: type === "user" ? "private" : "group",
    Title: String(id),
    Username: "",
    TopMessageID: 0,
    TopDate: 0,
    Preview: "",
    PreviewOut: false,
    PreviewMedia: "",
    UnreadCount: 0,
    HasDeletions: false
  };
}

export function AuditTab({ navigate, search }: { navigate: Navigate; search: URLSearchParams }) {
  const { t } = useI18n();
  const { can } = usePermissions();
  const ownerID = Number(search.get("owner") || "0");
  const rawPeerType = search.get("peer_type");
  const peerType: AuditPeer["type"] | null = rawPeerType === "channel" || rawPeerType === "user" ? rawPeerType : null;
  const peerID = Number(search.get("peer_id") || "0");
  const focusID = Number(search.get("msg") || "0");
  const peer: AuditPeer | null = peerType && peerID > 0 ? { type: peerType, id: peerID } : null;
  const canSeeDeletions = can(permissionMessageDeletionsRead);

  const [owner, setOwner] = useState<AccountRow | null>(null);
  const [dialogs, setDialogs] = useState<AuditDialogRow[]>([]);
  // 窄屏上检查器是盖在聊天上的抽屉，默认收起。
  const [inspectorOpen, setInspectorOpen] = useState(() => window.innerWidth > inspectorDrawerWidth);
  const [inspected, setInspected] = useState<InspectedMessage | null>(null);
  const [lightbox, setLightbox] = useState("");
  const [searchOpen, setSearchOpen] = useState(false);
  const [searchText, setSearchText] = useState("");
  const [searchSender, setSearchSender] = useState("");
  const [searchHits, setSearchHits] = useState<AuditMessage[] | null>(null);
  const [searchUsers, setSearchUsers] = useState<Record<string, AuditUser>>({});
  const [searching, setSearching] = useState(false);
  const [searchError, setSearchError] = useState("");
  const [highlight, setHighlight] = useState("");

  // URL 里的账号：有 accounts.read 时补全名称，否则只显示 ID。
  useEffect(() => {
    if (ownerID <= 0) {
      setOwner(null);
      return;
    }
    if (owner?.ID === ownerID) return;
    setOwner(minimalAccount(ownerID));
    if (can(permissionAccountsRead)) {
      api.account(ownerID).then(
        (detail) => setOwner(detail.Account),
        () => undefined
      );
    }
  }, [ownerID]);

  // 换会话时清掉上一会话的检查器、搜索和高亮。
  useEffect(() => {
    setInspected(null);
    setSearchHits(null);
    setSearchText("");
    setHighlight("");
    setSearchOpen(false);
  }, [ownerID, peerType, peerID]);

  const dialog = peer ? dialogs.find((row) => row.PeerType === peer.type && row.PeerID === peer.id) ?? placeholderDialog(peer.type, peer.id) : null;

  async function runSearch() {
    if (!peer || !searchText.trim()) return;
    setSearching(true);
    setSearchError("");
    const params = new URLSearchParams({ owner_user_id: String(ownerID), peer_type: peer.type, peer_id: String(peer.id), q: searchText.trim(), limit: "100" });
    if (/^\d+$/.test(searchSender.trim())) params.set("sender_user_id", searchSender.trim());
    try {
      const result = await api.auditSearch(params);
      setSearchHits(result.rows);
      setSearchUsers(result.users ?? {});
      setHighlight(searchText.trim());
    } catch (err) {
      setSearchError(errorMessage(err));
    } finally {
      setSearching(false);
    }
  }

  // 私聊里切到对方账号的视角：对方那份副本里，自己单方面删掉的消息仍然完好。
  const switchPerspective =
    dialog && dialog.PeerType === "user" && dialog.Kind === "private" && dialog.PeerID !== ownerID
      ? () => navigate(auditHref(dialog.PeerID, { type: "user", id: ownerID }))
      : null;

  return (
    <div className={`audit-shell ${dialog ? "has-chat" : ""} ${dialog && inspectorOpen ? "has-inspector" : ""}`}>
      <section className="audit-sidebar">
        <div className="audit-pane-head owner">
          <UserPicker variant="dropdown" label={t("audit.account")} value={owner} onChange={(row) => navigate(row ? auditHref(row.ID) : "/messages/audit")} />
        </div>
        {searchOpen && dialog ? (
          <div className="audit-search-panel">
            <form
              className="audit-search-form"
              onSubmit={(event) => {
                event.preventDefault();
                void runSearch();
              }}
            >
              <input autoFocus value={searchText} onChange={(event) => setSearchText(event.target.value)} placeholder={t("audit.searchInChat")} />
              <input value={searchSender} onChange={(event) => setSearchSender(event.target.value)} placeholder={t("audit.searchSender")} />
              <button type="submit" className="btn primary icon-text">
                {searching ? <Loader2 size={14} className="spin" /> : <Search size={14} />} {t("audit.search")}
              </button>
            </form>
            {searchError && <Alert>{searchError}</Alert>}
            <div className="audit-list">
              {searchHits?.map((hit) => (
                <button key={hit.ID} type="button" className="audit-hit" onClick={() => navigate(auditHref(ownerID, peer ?? undefined, hit.ID))}>
                  <span className="audit-dialog-line">
                    <span className="audit-dialog-title">{hit.Out ? t("audit.me") : userName(searchUsers, hit.SenderUserID)}</span>
                    {hit.Deleted && <span className="audit-dialog-deleted">{t("audit.deletedShort")}</span>}
                    <span className="audit-dialog-time">{dialogTime(hit.Date, t)}</span>
                  </span>
                  <span className="audit-dialog-preview">{hit.Body ? highlightText(hit.Body, highlight) : mediaLabel(hit.Media?.Kind ?? "", t)}</span>
                </button>
              ))}
              {searchHits && searchHits.length === 0 && <div className="audit-empty">{t("common.noResults")}</div>}
            </div>
          </div>
        ) : ownerID > 0 ? (
          <AuditDialogList
            ownerID={ownerID}
            selected={peer}
            onSelect={(row) => navigate(auditHref(ownerID, { type: row.PeerType, id: row.PeerID }))}
            onRows={setDialogs}
          />
        ) : (
          <div className="audit-empty">{t("audit.pickAccount")}</div>
        )}
      </section>

      <section className="audit-main">
        {dialog && peer ? (
          <>
            <div className="audit-pane-head">
              <button type="button" className="icon-button audit-back" onClick={() => navigate(auditHref(ownerID))} aria-label={t("audit.backToChats")}>
                <ArrowLeft size={16} />
              </button>
              <Avatar id={dialog.PeerID} kind={dialog.PeerType === "channel" ? "channel" : "user"} label={dialog.Title || String(dialog.PeerID)} size={36} />
              <div className="audit-chat-title">
                <div className="audit-chat-name">{dialog.Title || String(dialog.PeerID)}</div>
                <div className="audit-chat-sub mono">
                  {t(`audit.kind.${dialog.Kind}`)} · {dialog.PeerID} · {t("audit.viewingAsShort", { id: ownerID })}
                </div>
              </div>
              {highlight && (
                <button type="button" className="audit-pill" onClick={() => setHighlight("")} title={t("common.clear")}>
                  <mark>{highlight}</mark> <X size={12} />
                </button>
              )}
              <button type="button" className={`icon-button ${searchOpen ? "active" : ""}`} aria-pressed={searchOpen} onClick={() => setSearchOpen((v) => !v)} title={t("audit.searchInChat")}>
                <Search size={16} />
              </button>
              {switchPerspective && (
                <button type="button" className="icon-button" onClick={switchPerspective} title={t("audit.switchPerspective")}>
                  <Repeat size={16} />
                </button>
              )}
              <button type="button" className={`icon-button ${inspectorOpen ? "active" : ""}`} aria-pressed={inspectorOpen} onClick={() => setInspectorOpen((v) => !v)} title={t("audit.chatInfo")}>
                <Info size={16} />
              </button>
            </div>
            <AuditTranscript
              key={`${ownerID}-${peer.type}-${peer.id}`}
              ownerID={ownerID}
              peer={peer}
              isGroup={peer.type === "channel" && dialog.Kind !== "channel"}
              focusID={focusID}
              canSeeDeletions={canSeeDeletions}
              selectedID={inspected?.message.ID ?? 0}
              query={highlight}
              onSelect={(message, deletion, users) => {
                setInspected({ message, deletion, users });
                setInspectorOpen(true);
              }}
              onOpenImage={setLightbox}
            />
          </>
        ) : (
          <div className="audit-empty">{ownerID > 0 ? t("audit.pickChat") : t("audit.pickAccount")}</div>
        )}
      </section>

      {dialog && inspectorOpen && (
        <AuditInspector
          ownerID={ownerID}
          dialog={dialog}
          inspected={inspected}
          canSeeDeletions={canSeeDeletions}
          navigate={navigate}
          onSwitchPerspective={switchPerspective}
          onClearMessage={() => setInspected(null)}
          onClose={() => setInspectorOpen(false)}
        />
      )}
      {lightbox && <Lightbox url={lightbox} onClose={() => setLightbox("")} />}
    </div>
  );
}
