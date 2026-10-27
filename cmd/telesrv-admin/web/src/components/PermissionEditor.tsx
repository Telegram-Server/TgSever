import { X } from "lucide-react";
import { useI18n } from "../i18n";
import { displayPhone, displayUsername } from "../lib/format";
import type { Navigate } from "../routing";
import type { AccountPermissions, AccountRow, WhitelistMember } from "../types";
import { UserAdder } from "./EntityPicker";
import { Badge } from "./ui";

// The eleven per-action flags, in the order the spec (#118) lists them. Each key
// is both the request/response field and the i18n suffix.
export const permissionFlags = [
  "send",
  "delete",
  "edit",
  "clear_history",
  "create_group",
  "create_channel",
  "join_group",
  "join_channel",
  "leave_group",
  "leave_channel",
  "add_contact"
] as const;

export type PermissionFlag = (typeof permissionFlags)[number];

// PermissionDraft is the editable form of one permission setting: the master
// switch, the eleven flags and the send whitelist. It is what PermissionEditor
// shows and changes; the owner decides where it comes from and where it goes.
export type PermissionDraft = Record<PermissionFlag, boolean> & {
  enabled: boolean;
  whitelistEnabled: boolean;
  whitelist: number[];
  // members is how whitelist entries are shown, keyed by id: from the loaded
  // setting's send_whitelist.members, plus each account added by search. It is
  // display only — never compared or submitted — so a setting that belongs to
  // no account (a template) carries it the same way. An id without an entry is
  // shown as a bare id.
  members?: Record<number, WhitelistMember>;
};

// Mirrors domain.MaxSendWhitelistSize; the server rejects a longer list with
// PERMISSION_WHITELIST_INVALID.
export const maxWhitelistSize = 1000;

export function membersByID(members: WhitelistMember[] | undefined): Record<number, WhitelistMember> {
  const out: Record<number, WhitelistMember> = {};
  for (const member of members ?? []) {
    out[member.id] = member;
  }
  return out;
}

export function memberFromAccount(row: AccountRow): WhitelistMember {
  return {
    id: row.ID,
    found: true,
    first_name: row.FirstName,
    last_name: row.LastName,
    username: row.Username,
    phone: row.Phone,
    bot: row.Bot
  };
}

export function draftFromPermissions(p: AccountPermissions): PermissionDraft {
  const draft = {
    enabled: p.enabled,
    whitelistEnabled: p.send_whitelist.enabled,
    whitelist: [...(p.send_whitelist.user_ids ?? [])],
    members: membersByID(p.send_whitelist.members)
  } as PermissionDraft;
  for (const flag of permissionFlags) {
    draft[flag] = p[flag];
  }
  return draft;
}

export function samePermissionDraft(a: PermissionDraft, b: PermissionDraft): boolean {
  return (
    a.enabled === b.enabled &&
    a.whitelistEnabled === b.whitelistEnabled &&
    permissionFlags.every((flag) => a[flag] === b[flag]) &&
    a.whitelist.length === b.whitelist.length &&
    a.whitelist.every((id, i) => id === b.whitelist[i])
  );
}

// permissionFieldsFrom is the setting part of a set-permissions style body: the
// master switch, every flag and the whole whitelist. The whole setting is
// submitted every time, so what is saved is exactly what the operator saw.
export function permissionFieldsFrom(draft: PermissionDraft): Record<string, unknown> {
  const body: Record<string, unknown> = { enabled: draft.enabled };
  for (const flag of permissionFlags) {
    body[flag] = draft[flag];
  }
  body.send_whitelist = { enabled: draft.whitelistEnabled, user_ids: draft.whitelist };
  return body;
}

export type PermissionEditorProps = {
  // The draft shown; the editor never holds its own copy.
  value: PermissionDraft;
  // Called with the whole next draft on every change.
  onChange: (next: PermissionDraft) => void;
  // false renders every control disabled and hides the whitelist add/remove controls.
  editable: boolean;
  // The account the setting belongs to, refused as a whitelist entry. Omit it
  // for a setting that belongs to no account (a template).
  selfID?: number;
  // Turns whitelist members into links to their account pages; omit for plain entries.
  navigate?: Navigate;
};

// PermissionEditor is the controlled 权限控制 form: the master switch, the
// eleven action flags and the private-message send whitelist. It reads and
// writes nothing itself — loading, saving and dirty tracking belong to the
// owner. Whitelist entries are added by searching accounts; the search box is
// its only local state, and remounting it (key) clears that box.
export function PermissionEditor({ value: draft, onChange, editable, selfID, navigate }: PermissionEditorProps) {
  const { t } = useI18n();

  // With the master switch off nothing is enforced, so the flags grey out —
  // but they keep their values, which apply again when it is switched back on.
  const itemsDisabled = !editable || !draft.enabled;
  const whitelistDisabled = itemsDisabled || !draft.whitelistEnabled;
  const whitelistFull = draft.whitelist.length >= maxWhitelistSize;
  const members = draft.members ?? {};

  function update(patch: Partial<PermissionDraft>) {
    onChange({ ...draft, ...patch });
  }

  // The account itself and people already listed show in the search results
  // but cannot be picked.
  function unavailable(row: AccountRow): string | undefined {
    if (selfID !== undefined && row.ID === selfID) return t("perm.whitelistSelfBadge");
    if (draft.whitelist.includes(row.ID)) return t("perm.whitelistAddedBadge");
    return undefined;
  }

  function addToWhitelist(row: AccountRow) {
    if (unavailable(row) !== undefined || whitelistFull) return;
    update({
      whitelist: [...draft.whitelist, row.ID].sort((a, b) => a - b),
      members: { ...members, [row.ID]: memberFromAccount(row) }
    });
  }

  function memberName(id: number): string {
    const member = members[id];
    if (!member) return String(id);
    if (!member.found) return t("perm.whitelistMissing", { id });
    return `${member.first_name} ${member.last_name}`.trim() || String(id);
  }

  return (
    <>
      <label className="checkline perm-master">
        <input type="checkbox" checked={draft.enabled} disabled={!editable} onChange={(e) => update({ enabled: e.target.checked })} />
        <strong>{t("perm.enabled")}</strong>
      </label>
      <p className="muted">{draft.enabled ? t("perm.enabledHint") : t("perm.disabledHint")}</p>
      <div className={`perm-grid ${draft.enabled ? "" : "is-off"}`}>
        {permissionFlags.map((flag) => (
          <label key={flag} className="checkline">
            <input type="checkbox" checked={draft[flag]} disabled={itemsDisabled} onChange={(e) => update({ [flag]: e.target.checked } as Partial<PermissionDraft>)} />
            {t(`perm.flag.${flag}`)}
          </label>
        ))}
      </div>
      <div className={`perm-whitelist ${draft.enabled ? "" : "is-off"}`}>
        <label className="checkline">
          <input type="checkbox" checked={draft.whitelistEnabled} disabled={itemsDisabled} onChange={(e) => update({ whitelistEnabled: e.target.checked })} />
          <strong>{t("perm.whitelistEnabled")}</strong>
        </label>
        <p className="muted">{t("perm.whitelistHint")}</p>
        <div className="muted">{t("perm.whitelistCount", { count: draft.whitelist.length, max: maxWhitelistSize })}</div>
        {draft.whitelist.length === 0 ? (
          <p className="muted">{t("perm.whitelistEmpty")}</p>
        ) : (
          <ul className="perm-whitelist-members">
            {draft.whitelist.map((id) => {
              const member = members[id];
              const name = memberName(id);
              const secondary = member ? displayUsername(member.username) || displayPhone(member.phone) : "";
              return (
                <li key={id} className="perm-whitelist-member">
                  {navigate ? (
                    <button type="button" className="link-button" onClick={() => navigate(`/accounts/${id}`)}>{name}</button>
                  ) : (
                    <strong>{name}</strong>
                  )}
                  {secondary && <span className="muted">{secondary}</span>}
                  <span className="mono muted">{id}</span>
                  {member?.bot && <Badge tone="warn">{t("common.bot")}</Badge>}
                  {editable && (
                    <button
                      type="button"
                      className="icon-btn"
                      aria-label={t("perm.whitelistRemove", { name })}
                      title={t("perm.whitelistRemove", { name })}
                      disabled={whitelistDisabled}
                      onClick={() => update({ whitelist: draft.whitelist.filter((value) => value !== id) })}
                    >
                      <X size={12} />
                    </button>
                  )}
                </li>
              );
            })}
          </ul>
        )}
        {editable && (
          <div className="perm-whitelist-add">
            <UserAdder
              label={t("perm.whitelistSearchLabel")}
              placeholder={t("perm.whitelistSearchPlaceholder")}
              onAdd={addToWhitelist}
              unavailable={unavailable}
              disabled={whitelistDisabled || whitelistFull}
            />
          </div>
        )}
        {editable && whitelistFull && <p className="muted">{t("perm.whitelistFull", { max: maxWhitelistSize })}</p>}
      </div>
    </>
  );
}
