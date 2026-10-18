import { ChevronRight, ExternalLink, Repeat, X } from "lucide-react";
import { useI18n } from "../../i18n";
import { mediaLabel } from "../../lib/auditTimeline";
import { formatUnix } from "../../lib/format";
import { permissionAccountsRead, permissionChannelsRead, usePermissions } from "../../permissions";
import type { Navigate } from "../../routing";
import type { AuditDialogRow, AuditMessage, AuditUser, MessageDeletionRow } from "../../types";
import { Avatar } from "../../components/Avatar";
import { auditActor, userName } from "./AuditBubble";

export type InspectedMessage = { message: AuditMessage; deletion: MessageDeletionRow | undefined; users: Record<string, AuditUser> };

type Props = {
  ownerID: number;
  dialog: AuditDialogRow;
  inspected: InspectedMessage | null;
  canSeeDeletions: boolean;
  navigate: Navigate;
  onSwitchPerspective: (() => void) | null;
  onClearMessage: () => void;
  onClose: () => void;
};

function Field({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="audit-field">
      <span className="audit-field-label">{label}</span>
      <span className={`audit-field-value ${mono ? "mono" : ""}`}>{value || "-"}</span>
    </div>
  );
}

export function AuditInspector({ ownerID, dialog, inspected, canSeeDeletions, navigate, onSwitchPerspective, onClearMessage, onClose }: Props) {
  const { t } = useI18n();
  const { can } = usePermissions();
  const message = inspected?.message;
  const deletion = inspected?.deletion;
  const rawHref = message
    ? dialog.PeerType === "user"
      ? `/messages/private/detail?owner_user_id=${ownerID}&msg_id=${message.ID}`
      : `/messages/groups/detail?channel_id=${dialog.PeerID}&msg_id=${message.ID}`
    : "";

  return (
    <aside className="audit-inspector">
      <div className="audit-pane-head">
        <span className="audit-pane-title">{message ? t("audit.messageTitle", { id: message.ID }) : t("audit.chatInfo")}</span>
        {message && (
          <button type="button" className="icon-button" onClick={onClearMessage} title={t("audit.chatInfo")} aria-label={t("audit.chatInfo")}>
            <ChevronRight size={16} />
          </button>
        )}
        <button type="button" className="icon-button" onClick={onClose} aria-label={t("common.close")}>
          <X size={16} />
        </button>
      </div>
      <div className="audit-inspector-body">
        {message ? (
          <>
            <section className="audit-inspector-section">
              <Field label={t("common.sender")} value={`${userName(inspected.users, message.SenderUserID)} (${message.SenderUserID})`} />
              <Field label={t("deletions.sentAt")} value={formatUnix(message.Date)} />
              {message.EditDate > 0 && <Field label={t("audit.edited")} value={formatUnix(message.EditDate)} />}
              {message.Media && <Field label={t("audit.mediaKind")} value={mediaLabel(message.Media.Kind, t)} />}
              {message.Media?.FileName && <Field label={t("audit.fileName")} value={message.Media.FileName} />}
              {message.GroupedID && <Field label={t("audit.album")} value={message.GroupedID} mono />}
              {message.ReplyToID > 0 && <Field label={t("audit.replyField")} value={`#${message.ReplyToID}`} mono />}
            </section>
            {message.Deleted && (
              <section className="audit-inspector-section danger">
                <div className="audit-inspector-heading">{t("common.deleted")}</div>
                {!canSeeDeletions ? (
                  <p className="audit-note">{t("audit.deletionNeedsRight")}</p>
                ) : !deletion ? (
                  <p className="audit-note">{t("audit.noDeletionRecord")}</p>
                ) : (
                  <>
                    <Field label={t("deletions.deletedBy")} value={`${auditActor(deletion, inspected.users, t)}${deletion.ActorUserID ? ` (${deletion.ActorUserID})` : ""}`} />
                    <Field label={t("deletions.deletedAt")} value={formatUnix(deletion.Date)} />
                    <Field label={t("deletions.source")} value={t(`deletions.source.${deletion.Source}`)} />
                    <Field label={t("deletions.scope")} value={t(`deletions.scope.${deletion.Scope}`)} />
                    <Field label={t("deletions.device")} value={[deletion.DeviceModel, deletion.Platform, deletion.IP].filter(Boolean).join(" · ")} />
                    {deletion.ActorType === "operator" && <Field label={t("deletions.reason")} value={deletion.Reason} />}
                    <button type="button" className="row-link" onClick={() => navigate(`/messages/deletions/detail?id=${deletion.ID}`)}>
                      {t("audit.openDeletion")} <ChevronRight size={14} />
                    </button>
                  </>
                )}
              </section>
            )}
            <button type="button" className="btn icon-text" onClick={() => navigate(rawHref)}>
              <ExternalLink size={14} /> {t("audit.rawData")}
            </button>
          </>
        ) : (
          <>
            <section className="audit-inspector-profile">
              <Avatar id={dialog.PeerID} kind={dialog.PeerType === "channel" ? "channel" : "user"} label={dialog.Title || String(dialog.PeerID)} size={64} />
              <div className="audit-inspector-name">{dialog.Title || String(dialog.PeerID)}</div>
              <div className="audit-note mono">
                {t(`audit.kind.${dialog.Kind}`)} · {dialog.PeerID}
                {dialog.Username ? ` · @${dialog.Username}` : ""}
              </div>
            </section>
            <section className="audit-inspector-section">
              <Field label={t("audit.viewingAs")} value={String(ownerID)} mono />
              <Field label={t("audit.unread")} value={String(dialog.UnreadCount)} />
              <Field label={t("audit.lastActivity")} value={dialog.TopDate ? formatUnix(dialog.TopDate) : "-"} />
            </section>
            {onSwitchPerspective && (
              <button type="button" className="btn icon-text" onClick={onSwitchPerspective}>
                <Repeat size={14} /> {t("audit.switchPerspective")}
              </button>
            )}
            {dialog.PeerType === "user" && can(permissionAccountsRead) && (
              <button type="button" className="btn icon-text" onClick={() => navigate(`/accounts/${dialog.PeerID}`)}>
                <ExternalLink size={14} /> {t("audit.openAccount")}
              </button>
            )}
            {dialog.PeerType === "channel" && can(permissionChannelsRead) && (
              <button type="button" className="btn icon-text" onClick={() => navigate(`/channels/${dialog.PeerID}`)}>
                <ExternalLink size={14} /> {t("audit.openChannel")}
              </button>
            )}
          </>
        )}
      </div>
    </aside>
  );
}
