import { ChevronDown, ChevronRight, Loader2, Trash2 } from "lucide-react";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { api, errorMessage } from "../../api";
import { Alert } from "../../components/ui";
import { useI18n } from "../../i18n";
import { buildTimeline, clockTime, dayLabel, type TimelineItem } from "../../lib/auditTimeline";
import { formatUnix } from "../../lib/format";
import type { AuditMessage, AuditUser, MessageDeletionRow } from "../../types";
import { AuditBubble, auditActor, type BubbleContext } from "./AuditBubble";

const pageSize = 100;
// 页面里最多保留的消息数；再多就裁掉离视口远的一端，避免长会话越翻越卡。
const maxLoaded = 1000;
// 离顶部 / 底部多近时自动加载下一页（px）。
const edgeThreshold = 160;
const flashMillis = 1600;

export type AuditPeer = { type: "user" | "channel"; id: number };

type Props = {
  ownerID: number;
  peer: AuditPeer;
  isGroup: boolean;
  focusID: number;
  canSeeDeletions: boolean;
  selectedID: number;
  query: string;
  onSelect: (message: AuditMessage, deletion: MessageDeletionRow | undefined, users: Record<string, AuditUser>) => void;
  onOpenImage: (url: string) => void;
};

export function AuditTranscript(props: Props) {
  const { ownerID, peer, isGroup, focusID, canSeeDeletions } = props;
  const { t } = useI18n();
  const scroller = useRef<HTMLDivElement>(null);
  const [messages, setMessages] = useState<AuditMessage[]>([]);
  const [hasOlder, setHasOlder] = useState(false);
  const [hasNewer, setHasNewer] = useState(false);
  const [users, setUsers] = useState<Record<string, AuditUser>>({});
  const [deletions, setDeletions] = useState<Map<number, MessageDeletionRow>>(new Map());
  const [loading, setLoading] = useState<"" | "initial" | "older" | "newer">("");
  const [error, setError] = useState("");
  const [flashID, setFlashID] = useState(0);
  // revealID 让包含它的折叠段（整批清空）展开，好让定位滚得到。
  const [revealID, setRevealID] = useState(0);
  const [atBottom, setAtBottom] = useState(true);
  // 加载后要做的滚动：保持旧内容不动（prepend）、滚到底，或滚到某条消息居中。
  const pendingScroll = useRef<{ kind: "keep"; height: number; top: number } | { kind: "bottom" } | { kind: "message"; id: number } | null>(null);
  const generation = useRef(0);

  const baseParams = useCallback(() => {
    const params = new URLSearchParams({ owner_user_id: String(ownerID), peer_type: peer.type, peer_id: String(peer.id), limit: String(pageSize) });
    return params;
  }, [ownerID, peer.type, peer.id]);

  // loadDeletions 取一页消息对应的删除记录（需要 messages.deletions.read）。
  const loadDeletions = useCallback(
    async (page: AuditMessage[], gen: number) => {
      const deleted = page.filter((m) => m.Deleted);
      if (!canSeeDeletions || deleted.length === 0) return;
      const params = new URLSearchParams({
        peer_type: peer.type,
        peer_id: String(peer.id),
        min_message_id: String(deleted[0].ID),
        max_message_id: String(deleted[deleted.length - 1].ID),
        limit: "100"
      });
      if (peer.type === "user") params.set("owner_user_id", String(ownerID));
      const found = new Map<number, MessageDeletionRow>();
      try {
        for (let round = 0; round < 10; round++) {
          const result = await api.messageDeletions(params);
          for (const row of result.rows) if (!found.has(row.MessageID)) found.set(row.MessageID, row);
          if (result.rows.length < 100) break;
          params.set("before_id", String(result.rows[result.rows.length - 1].ID));
        }
      } catch {
        return;
      }
      if (gen !== generation.current) return;
      setDeletions((current) => {
        const next = new Map(current);
        found.forEach((row, id) => next.set(id, row));
        return next;
      });
    },
    [canSeeDeletions, ownerID, peer.type, peer.id]
  );

  // loadInitial 打开会话：最新一页，或以 aroundID 为中心的一页。
  const loadInitial = useCallback(
    async (aroundID: number) => {
      const gen = ++generation.current;
      setLoading("initial");
      setError("");
      const params = baseParams();
      if (aroundID > 0) params.set("around_id", String(aroundID));
      try {
        const page = await api.auditHistory(params);
        if (gen !== generation.current) return;
        setMessages(page.Messages);
        setHasOlder(page.HasOlder);
        setHasNewer(page.HasNewer);
        setUsers(page.Users);
        setDeletions(new Map());
        pendingScroll.current = aroundID > 0 ? { kind: "message", id: aroundID } : { kind: "bottom" };
        void loadDeletions(page.Messages, gen);
      } catch (err) {
        if (gen === generation.current) setError(errorMessage(err));
      } finally {
        if (gen === generation.current) setLoading("");
      }
    },
    [baseParams, loadDeletions]
  );

  useEffect(() => {
    void loadInitial(focusID);
  }, [loadInitial, focusID]);

  async function loadMore(direction: "older" | "newer") {
    if (loading || messages.length === 0) return;
    const gen = generation.current;
    setLoading(direction);
    const params = baseParams();
    if (direction === "older") params.set("before_id", String(messages[0].ID));
    else params.set("after_id", String(messages[messages.length - 1].ID));
    try {
      const page = await api.auditHistory(params);
      if (gen !== generation.current) return;
      const el = scroller.current;
      setUsers((current) => ({ ...current, ...page.Users }));
      if (direction === "older") {
        if (el) pendingScroll.current = { kind: "keep", height: el.scrollHeight, top: el.scrollTop };
        let next = [...page.Messages, ...messages];
        if (next.length > maxLoaded) {
          next = next.slice(0, maxLoaded);
          setHasNewer(true);
        }
        setMessages(next);
        setHasOlder(page.HasOlder);
      } else {
        let next = [...messages, ...page.Messages];
        if (next.length > maxLoaded) {
          if (el) pendingScroll.current = { kind: "keep", height: el.scrollHeight, top: el.scrollTop };
          next = next.slice(next.length - maxLoaded);
          setHasOlder(true);
        }
        setMessages(next);
        setHasNewer(page.HasNewer);
      }
      void loadDeletions(page.Messages, gen);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading("");
    }
  }

  // 渲染后按 pendingScroll 调整滚动位置。
  useLayoutEffect(() => {
    const el = scroller.current;
    const pending = pendingScroll.current;
    if (!el || !pending) return;
    pendingScroll.current = null;
    if (pending.kind === "bottom") {
      el.scrollTop = el.scrollHeight;
    } else if (pending.kind === "keep") {
      el.scrollTop = el.scrollHeight - pending.height + pending.top;
    } else {
      scrollToMessage(pending.id);
    }
  }, [messages, revealID]);

  function scrollToMessage(id: number) {
    const el = scroller.current;
    const target = el?.querySelector<HTMLElement>(`[data-message-id="${id}"]`) ?? findAlbumRow(el, id);
    if (el && !target && revealID !== id && messages.some((m) => m.ID === id)) {
      // 目标在收起的折叠段里：先展开，下一次渲染后再滚。
      pendingScroll.current = { kind: "message", id };
      setRevealID(id);
      return true;
    }
    if (!el || !target) return false;
    el.scrollTop = target.offsetTop - el.clientHeight / 2 + target.clientHeight / 2;
    setFlashID(id);
    window.setTimeout(() => setFlashID((current) => (current === id ? 0 : current)), flashMillis);
    return true;
  }

  // 相册只在领头消息上标了 data-message-id；跳到其他成员时找它所在的相册。
  function findAlbumRow(el: HTMLElement | null, id: number): HTMLElement | null {
    const message = messages.find((m) => m.ID === id);
    if (!el || !message?.GroupedID) return null;
    const lead = messages.find((m) => m.GroupedID === message.GroupedID);
    return lead ? el.querySelector<HTMLElement>(`[data-message-id="${lead.ID}"]`) : null;
  }

  function jump(id: number) {
    if (messages.some((m) => m.ID === id) && scrollToMessage(id)) return;
    void loadInitial(id);
  }

  function onScroll() {
    const el = scroller.current;
    if (!el) return;
    const bottomGap = el.scrollHeight - el.scrollTop - el.clientHeight;
    setAtBottom(bottomGap < edgeThreshold);
    if (el.scrollTop < edgeThreshold && hasOlder) void loadMore("older");
    else if (bottomGap < edgeThreshold && hasNewer) void loadMore("newer");
  }

  function toBottom() {
    if (hasNewer) {
      void loadInitial(0);
      return;
    }
    const el = scroller.current;
    if (el) el.scrollTop = el.scrollHeight;
  }

  const timeline = useMemo(() => buildTimeline(messages, canSeeDeletions ? deletions : new Map()), [messages, deletions, canSeeDeletions]);
  const byID = useMemo(() => new Map(messages.map((m) => [m.ID, m])), [messages]);

  const ctx: BubbleContext & { revealID: number } = {
    revealID,
    isGroup,
    users,
    deletions,
    canSeeDeletions,
    lookup: (id) => byID.get(id),
    selectedID: props.selectedID,
    flashID,
    query: props.query,
    onSelect: (message) => props.onSelect(message, deletions.get(message.ID), users),
    onJump: jump,
    onOpenImage: props.onOpenImage
  };

  return (
    <div className="audit-transcript">
      {error && <Alert>{error}</Alert>}
      <div className="audit-scroller" ref={scroller} onScroll={onScroll}>
        <div className="audit-edge">
          {loading === "older" || loading === "initial" ? (
            <Loader2 size={16} className="spin" />
          ) : hasOlder ? (
            <button type="button" className="audit-pill" onClick={() => void loadMore("older")}>
              {t("audit.loadOlder")}
            </button>
          ) : messages.length ? (
            <span className="audit-pill muted">{t("audit.beginning")}</span>
          ) : null}
        </div>
        {!loading && messages.length === 0 && !error && <div className="audit-empty">{t("audit.noMessages")}</div>}
        {timeline.map((item) => (
          <TimelineEntry key={item.key} item={item} ctx={ctx} />
        ))}
        {loading === "newer" && (
          <div className="audit-edge">
            <Loader2 size={16} className="spin" />
          </div>
        )}
      </div>
      {(!atBottom || hasNewer) && messages.length > 0 && (
        <button type="button" className="audit-to-bottom" onClick={toBottom} aria-label={t("audit.toLatest")} title={t("audit.toLatest")}>
          <ChevronDown size={18} />
        </button>
      )}
    </div>
  );
}

function containsMessage(items: TimelineItem[], id: number): boolean {
  return id > 0 && items.some((item) => item.kind === "bubble" && item.messages.some((m) => m.ID === id));
}

function TimelineEntry({ item, ctx }: { item: TimelineItem; ctx: BubbleContext & { revealID: number } }) {
  const { t } = useI18n();
  const [toggled, setOpen] = useState(false);
  const open = toggled || (item.kind === "cleared" && (containsMessage(item.items, ctx.revealID) || containsMessage(item.items, ctx.selectedID)));
  switch (item.kind) {
    case "day":
      return (
        <div className="audit-center">
          <span className="audit-pill">{dayLabel(item.date, t)}</span>
        </div>
      );
    case "service":
      return (
        <div className="audit-center" data-message-id={item.message.ID}>
          <span className="audit-pill">
            {t("audit.serviceMessage", { kind: item.message.Service.replace(/_/g, " ") })} · {clockTime(item.message.Date)}
          </span>
        </div>
      );
    case "bubble":
      return <AuditBubble item={item} ctx={ctx} />;
    case "cleared":
      return (
        <div className="audit-cleared">
          <button type="button" className="audit-cleared-line" onClick={() => setOpen((v) => !v)} aria-expanded={open}>
            <span className="audit-cleared-rule" />
            <span className="audit-cleared-text">
              <Trash2 size={12} />
              {t("audit.clearedBatch", {
                actor: auditActor(item.deletion, ctx.users, t),
                source: t(`deletions.source.${item.deletion.Source}`),
                scope: t(`deletions.scope.${item.deletion.Scope}`),
                time: formatUnix(item.deletion.Date),
                count: item.count
              })}
              {open ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
            </span>
            <span className="audit-cleared-rule" />
          </button>
          {open && (
            <div className="audit-cleared-body">
              {item.items.map((child) => (
                <TimelineEntry key={child.key} item={child} ctx={ctx} />
              ))}
            </div>
          )}
        </div>
      );
  }
}
