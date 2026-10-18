import { Forward, Pencil, Pin, Trash2 } from "lucide-react";
import type { ReactNode } from "react";
import { Avatar } from "../../components/Avatar";
import { useI18n, type TFunction } from "../../i18n";
import { clockTime, mediaLabel, type BubbleItem } from "../../lib/auditTimeline";
import { formatUnix } from "../../lib/format";
import type { AuditMessage, AuditUser, MessageDeletionRow } from "../../types";
import { deletionActor } from "../MessageDeletionsPage";
import { MediaAlbum, SingleMedia } from "./AuditMedia";

export function userName(users: Record<string, AuditUser>, id: number): string {
  return users[String(id)]?.Name || String(id);
}

// deletionTone 决定被删气泡的虚线颜色：只删了自己这边（对方可能还看得到）是琥珀色，
// 双方或所有人是红色，到期自动删除是灰色；没有删除记录可看时也是灰色。
export function deletionTone(deletion: MessageDeletionRow | undefined): "self" | "all" | "auto" | "unknown" {
  if (!deletion) return "unknown";
  if (deletion.Source === "ttl") return "auto";
  return deletion.Scope === "self" ? "self" : "all";
}

// auditActor 是删除人的显示名：用户和机器人用这一页的名字对照，操作员和系统沿用删除记录页的写法。
export function auditActor(deletion: MessageDeletionRow, users: Record<string, AuditUser>, t: TFunction): string {
  if ((deletion.ActorType === "user" || deletion.ActorType === "bot") && users[String(deletion.ActorUserID)]) {
    return userName(users, deletion.ActorUserID);
  }
  return deletionActor(deletion, t);
}

function deletionTag(deletion: MessageDeletionRow | undefined, users: Record<string, AuditUser>, t: TFunction): string {
  if (!deletion) return t("common.deleted");
  return t("audit.deletedTag", {
    actor: auditActor(deletion, users, t),
    scope: t(`deletions.scope.${deletion.Scope}`),
    time: formatUnix(deletion.Date)
  });
}

// highlightText 把关键词用 <mark> 标出（不区分大小写）。
export function highlightText(text: string, query: string): ReactNode {
  const needle = query.trim();
  if (!needle) return text;
  const lower = text.toLowerCase();
  const target = needle.toLowerCase();
  const parts: ReactNode[] = [];
  let from = 0;
  for (let at = lower.indexOf(target); at >= 0; at = lower.indexOf(target, from)) {
    if (at > from) parts.push(text.slice(from, at));
    parts.push(<mark key={at}>{text.slice(at, at + needle.length)}</mark>);
    from = at + needle.length;
  }
  parts.push(text.slice(from));
  return parts;
}

export type BubbleContext = {
  isGroup: boolean;
  users: Record<string, AuditUser>;
  deletions: Map<number, MessageDeletionRow>;
  canSeeDeletions: boolean;
  lookup: (id: number) => AuditMessage | undefined;
  selectedID: number;
  flashID: number;
  query: string;
  onSelect: (message: AuditMessage) => void;
  onJump: (id: number) => void;
  onOpenImage: (url: string) => void;
};

export function AuditBubble({ item, ctx }: { item: BubbleItem; ctx: BubbleContext }) {
  const { t } = useI18n();
  const lead = item.messages[0];
  const out = lead.Out;
  const caption = item.messages.find((m) => m.Body) ?? lead;
  const deletedMembers = item.messages.filter((m) => m.Deleted);
  const allDeleted = deletedMembers.length === item.messages.length;
  const deletion = ctx.canSeeDeletions ? ctx.deletions.get((deletedMembers[0] ?? lead).ID) : undefined;
  const tone = allDeleted ? deletionTone(deletion) : null;
  const edited = item.messages.reduce((latest, m) => Math.max(latest, m.EditDate), 0);
  const selected = item.messages.some((m) => m.ID === ctx.selectedID);
  const flashing = item.messages.some((m) => m.ID === ctx.flashID);
  const bare = item.messages.length === 1 && lead.Media?.Kind === "sticker" && !lead.Body;
  const showSender = ctx.isGroup && !out && item.firstOfRun;
  const showAvatar = ctx.isGroup && !out;
  const reply = lead.ReplyToID ? ctx.lookup(lead.ReplyToID) : undefined;

  const classes = ["audit-bubble", out ? "out" : "in"];
  if (tone) classes.push("deleted", `deleted-${tone}`);
  if (bare) classes.push("bare");
  if (selected) classes.push("selected");
  if (item.lastOfRun && !bare) classes.push("tail");

  return (
    <div className={`audit-row ${out ? "out" : "in"} ${item.lastOfRun ? "run-end" : ""} ${flashing ? "flash" : ""}`} data-message-id={lead.ID}>
      {showAvatar && (
        <div className="audit-row-avatar">
          {item.lastOfRun && <Avatar id={lead.SenderUserID} label={userName(ctx.users, lead.SenderUserID)} size={32} />}
        </div>
      )}
      <div className={classes.join(" ")} onClick={() => ctx.onSelect(deletedMembers[0] ?? lead)}>
        {tone && (
          <div className={`audit-deleted-tag ${tone}`}>
            <Trash2 size={12} /> {deletionTag(deletion, ctx.users, t)}
          </div>
        )}
        {!tone && deletedMembers.length > 0 && (
          <div className="audit-deleted-tag unknown">
            <Trash2 size={12} /> {t("audit.partlyDeleted", { count: deletedMembers.length })}
          </div>
        )}
        {showSender && (
          <div className="audit-sender">
            {userName(ctx.users, lead.SenderUserID)} <span className="mono">{lead.SenderUserID}</span>
          </div>
        )}
        {lead.ForwardFromName || lead.ForwardFromPeerID ? (
          <div className="audit-forward">
            <Forward size={12} />{" "}
            {t("audit.forwardedFrom", {
              from: lead.ForwardFromName || (lead.ForwardFromPeerType === "user" ? userName(ctx.users, lead.ForwardFromPeerID) : String(lead.ForwardFromPeerID))
            })}
          </div>
        ) : null}
        {lead.ReplyToID > 0 && (
          <button
            type="button"
            className="audit-reply"
            onClick={(event) => {
              event.stopPropagation();
              ctx.onJump(lead.ReplyToID);
            }}
          >
            <span className="audit-reply-name">{reply ? userName(ctx.users, reply.SenderUserID) : t("audit.replyTo", { id: lead.ReplyToID })}</span>
            <span className="audit-reply-text">{reply ? reply.Body || mediaLabel(reply.Media?.Kind ?? "", t) : t("audit.replyNotLoaded")}</span>
          </button>
        )}
        {item.messages.length > 1 ? (
          <MediaAlbum messages={item.messages} deleted={(m) => m.Deleted && !allDeleted} onOpenImage={ctx.onOpenImage} />
        ) : lead.Media ? (
          <SingleMedia media={lead.Media} onOpenImage={ctx.onOpenImage} />
        ) : null}
        {caption.Body && <div className="audit-text">{highlightText(caption.Body, ctx.query)}</div>}
        <span className="audit-meta">
          {lead.Pinned && <Pin size={11} aria-label={t("messages.pinned")} />}
          {edited > 0 && (
            <span className="audit-edited" title={t("audit.editedAt", { time: formatUnix(edited) })}>
              <Pencil size={10} /> {t("audit.edited")}
            </span>
          )}
          <span title={formatUnix(lead.Date)}>{clockTime(lead.Date)}</span>
        </span>
      </div>
    </div>
  );
}
