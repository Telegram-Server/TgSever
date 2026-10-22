import { Plus, RotateCcw, ShieldCheck, X } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { ActionButton } from "./ActionButton";
import { Alert, Badge, LoadingSurface, SectionHead } from "./ui";
import { useI18n } from "../i18n";
import { formatDate } from "../lib/format";
import { permissionAccountsManage, usePermissions } from "../permissions";
import type { Navigate } from "../routing";
import type { AccountPermissions } from "../types";

// The eleven per-action flags, in the order the spec (#118) lists them. Each key
// is both the request/response field and the i18n suffix.
const permissionFlags = [
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

type PermissionFlag = (typeof permissionFlags)[number];

type Draft = Record<PermissionFlag, boolean> & {
  enabled: boolean;
  whitelistEnabled: boolean;
  whitelist: number[];
};

// Mirrors domain.MaxSendWhitelistSize; the server rejects a longer list with
// PERMISSION_WHITELIST_INVALID.
const maxWhitelistSize = 1000;

function draftFrom(p: AccountPermissions): Draft {
  const draft = {
    enabled: p.enabled,
    whitelistEnabled: p.send_whitelist.enabled,
    whitelist: [...(p.send_whitelist.user_ids ?? [])]
  } as Draft;
  for (const flag of permissionFlags) {
    draft[flag] = p[flag];
  }
  return draft;
}

function sameDraft(a: Draft, b: Draft): boolean {
  return (
    a.enabled === b.enabled &&
    a.whitelistEnabled === b.whitelistEnabled &&
    permissionFlags.every((flag) => a[flag] === b[flag]) &&
    a.whitelist.length === b.whitelist.length &&
    a.whitelist.every((id, i) => id === b.whitelist[i])
  );
}

// The whole setting is submitted every time: set-permissions replaces what it is
// given, so the saved state is exactly what the operator saw here.
function payloadFrom(userID: number, draft: Draft): Record<string, unknown> {
  const body: Record<string, unknown> = { user_id: userID, enabled: draft.enabled };
  for (const flag of permissionFlags) {
    body[flag] = draft[flag];
  }
  body.send_whitelist = { enabled: draft.whitelistEnabled, user_ids: draft.whitelist };
  return body;
}

// AccountPermissionsSection is the account detail page's 权限控制 section:
// the master switch, the eleven action flags and the private-message send
// whitelist. Anyone who can open the page (accounts.read) sees it; only
// accounts.manage can change and save it.
export function AccountPermissionsSection({ userID, navigate }: { userID: number; navigate: Navigate }) {
  const { t } = useI18n();
  const { can } = usePermissions();
  const editable = can(permissionAccountsManage);
  const [loaded, setLoaded] = useState<AccountPermissions | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [loadError, setLoadError] = useState("");
  const [addValue, setAddValue] = useState("");

  async function load() {
    setLoadError("");
    try {
      const next = await api.accountPermissions(userID);
      setLoaded(next);
      setDraft(draftFrom(next));
      setAddValue("");
    } catch (err) {
      setLoadError(errorMessage(err));
    }
  }

  useEffect(() => {
    setLoaded(null);
    setDraft(null);
    void load();
  }, [userID]);

  if (loadError) {
    return (
      <section className="section-block">
        <SectionHead title={t("perm.title")} />
        <Alert>{t("perm.loadFailed", { error: loadError })}</Alert>
      </section>
    );
  }
  if (!loaded || !draft) {
    return (
      <section className="section-block">
        <SectionHead title={t("perm.title")} />
        <LoadingSurface label={t("perm.loading")} />
      </section>
    );
  }

  const dirty = !sameDraft(draft, draftFrom(loaded));
  // With the master switch off nothing is enforced, so the flags grey out —
  // but they keep their values, which apply again when it is switched back on.
  const itemsDisabled = !editable || !draft.enabled;
  const whitelistDisabled = itemsDisabled || !draft.whitelistEnabled;

  const addTrimmed = addValue.trim();
  const addID = /^\d+$/.test(addTrimmed) ? Number(addTrimmed) : 0;
  let addProblem = "";
  if (addTrimmed !== "") {
    if (!Number.isSafeInteger(addID) || addID <= 0) addProblem = t("perm.whitelistInvalidID");
    else if (addID === userID) addProblem = t("perm.whitelistSelf");
    else if (draft.whitelist.includes(addID)) addProblem = t("perm.whitelistDuplicate");
    else if (draft.whitelist.length >= maxWhitelistSize) addProblem = t("perm.whitelistFull", { max: maxWhitelistSize });
  }

  function update(patch: Partial<Draft>) {
    setDraft((current) => (current ? { ...current, ...patch } : current));
  }

  function addToWhitelist() {
    if (addTrimmed === "" || addProblem || !draft) return;
    update({ whitelist: [...draft.whitelist, addID].sort((a, b) => a - b) });
    setAddValue("");
  }

  // The server names the failure with a code at the front of its message; turn
  // the two an operator can cause from this form into plain words.
  function explainError(err: unknown): string | undefined {
    const message = errorMessage(err);
    if (message.includes("PERMISSION_WHITELIST_INVALID")) return t("perm.errorWhitelistInvalid");
    if (message.includes("USER_INVALID")) return t("perm.errorUserInvalid");
    return undefined;
  }

  return (
    <section className="section-block account-permissions">
      <SectionHead
        title={t("perm.title")}
        text={t("perm.hint")}
        action={draft.enabled ? <Badge tone="warn">{t("perm.statusOn")}</Badge> : <Badge>{t("perm.statusOff")}</Badge>}
      />
      {!editable && <p className="muted">{t("perm.readOnly")}</p>}
      <label className="checkline perm-master">
        <input type="checkbox" checked={draft.enabled} disabled={!editable} onChange={(e) => update({ enabled: e.target.checked })} />
        <strong>{t("perm.enabled")}</strong>
      </label>
      <p className="muted">{draft.enabled ? t("perm.enabledHint") : t("perm.disabledHint")}</p>
      <div className={`perm-grid ${draft.enabled ? "" : "is-off"}`}>
        {permissionFlags.map((flag) => (
          <label key={flag} className="checkline">
            <input type="checkbox" checked={draft[flag]} disabled={itemsDisabled} onChange={(e) => update({ [flag]: e.target.checked } as Partial<Draft>)} />
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
                <button type="button" className="link-button mono" onClick={() => navigate(`/accounts/${id}`)}>{id}</button>
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
      {loaded.updated_at && <p className="muted">{t("perm.updatedAt", { time: formatDate(loaded.updated_at) })}</p>}
      {editable && (
        <div className="perm-actions">
          {dirty && <span className="muted">{t("perm.unsaved")}</span>}
          <button className="btn icon-text" type="button" disabled={!dirty} onClick={() => setDraft(draftFrom(loaded))}>
            <RotateCcw size={15} /> {t("perm.discard")}
          </button>
          <ActionButton
            label={t("perm.save")}
            icon={<ShieldCheck size={15} />}
            tone="warn"
            path="/api/actions/set-permissions"
            payload={() => payloadFrom(userID, draft)}
            disabled={!dirty}
            onError={explainError}
            onDone={() => void load()}
          />
        </div>
      )}
    </section>
  );
}
