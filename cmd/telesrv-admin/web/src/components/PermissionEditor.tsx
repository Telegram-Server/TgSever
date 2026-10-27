import { Plus, X } from "lucide-react";
import { useState } from "react";
import { useI18n } from "../i18n";
import type { Navigate } from "../routing";
import type { AccountPermissions } from "../types";

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
};

// Mirrors domain.MaxSendWhitelistSize; the server rejects a longer list with
// PERMISSION_WHITELIST_INVALID.
export const maxWhitelistSize = 1000;

export function draftFromPermissions(p: AccountPermissions): PermissionDraft {
  const draft = {
    enabled: p.enabled,
    whitelistEnabled: p.send_whitelist.enabled,
    whitelist: [...(p.send_whitelist.user_ids ?? [])]
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
  // Turns whitelist ids into links to their account pages; omit for plain ids.
  navigate?: Navigate;
};

// PermissionEditor is the controlled 权限控制 form: the master switch, the
// eleven action flags and the private-message send whitelist. It reads and
// writes nothing itself — loading, saving and dirty tracking belong to the
// owner. The whitelist add box is its only local state; remount it (key) to
// clear that box.
export function PermissionEditor({ value: draft, onChange, editable, selfID, navigate }: PermissionEditorProps) {
  const { t } = useI18n();
  const [addValue, setAddValue] = useState("");

  // With the master switch off nothing is enforced, so the flags grey out —
  // but they keep their values, which apply again when it is switched back on.
  const itemsDisabled = !editable || !draft.enabled;
  const whitelistDisabled = itemsDisabled || !draft.whitelistEnabled;

  const addTrimmed = addValue.trim();
  const addID = /^\d+$/.test(addTrimmed) ? Number(addTrimmed) : 0;
  let addProblem = "";
  if (addTrimmed !== "") {
    if (!Number.isSafeInteger(addID) || addID <= 0) addProblem = t("perm.whitelistInvalidID");
    else if (selfID !== undefined && addID === selfID) addProblem = t("perm.whitelistSelf");
    else if (draft.whitelist.includes(addID)) addProblem = t("perm.whitelistDuplicate");
    else if (draft.whitelist.length >= maxWhitelistSize) addProblem = t("perm.whitelistFull", { max: maxWhitelistSize });
  }

  function update(patch: Partial<PermissionDraft>) {
    onChange({ ...draft, ...patch });
  }

  function addToWhitelist() {
    if (addTrimmed === "" || addProblem) return;
    update({ whitelist: [...draft.whitelist, addID].sort((a, b) => a - b) });
    setAddValue("");
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
          <div className="perm-whitelist-ids">
            {draft.whitelist.map((id) => (
              <span key={id} className="perm-whitelist-id">
                {navigate ? (
                  <button type="button" className="link-button mono" onClick={() => navigate(`/accounts/${id}`)}>{id}</button>
                ) : (
                  <span className="mono">{id}</span>
                )}
                {editable && (
                  <button
                    type="button"
                    className="icon-btn"
                    aria-label={t("perm.whitelistRemove", { id })}
                    title={t("perm.whitelistRemove", { id })}
                    disabled={whitelistDisabled}
                    onClick={() => update({ whitelist: draft.whitelist.filter((value) => value !== id) })}
                  >
                    <X size={12} />
                  </button>
                )}
              </span>
            ))}
          </div>
        )}
        {editable && (
          <div className="perm-whitelist-add">
            <input
              aria-label={t("perm.whitelistAddLabel")}
              value={addValue}
              onChange={(e) => setAddValue(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  addToWhitelist();
                }
              }}
              inputMode="numeric"
              placeholder={t("perm.whitelistAddPlaceholder")}
              disabled={whitelistDisabled}
            />
            <button className="btn icon-text" type="button" onClick={addToWhitelist} disabled={whitelistDisabled || addTrimmed === "" || addProblem !== ""}>
              <Plus size={15} /> {t("perm.whitelistAdd")}
            </button>
          </div>
        )}
        {addProblem && <p className="muted">{addProblem}</p>}
      </div>
    </>
  );
}
