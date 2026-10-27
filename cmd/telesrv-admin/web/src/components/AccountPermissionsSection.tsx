import { RotateCcw, ShieldCheck } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { ActionButton } from "./ActionButton";
import { draftFromPermissions, PermissionEditor, permissionFieldsFrom, samePermissionDraft, type PermissionDraft } from "./PermissionEditor";
import { Alert, Badge, LoadingSurface, SectionHead } from "./ui";
import { useI18n } from "../i18n";
import { formatDate } from "../lib/format";
import { permissionAccountsManage, usePermissions } from "../permissions";
import type { Navigate } from "../routing";
import type { AccountPermissions } from "../types";

function payloadFrom(userID: number, draft: PermissionDraft): Record<string, unknown> {
  return { user_id: userID, ...permissionFieldsFrom(draft) };
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
  const [draft, setDraft] = useState<PermissionDraft | null>(null);
  const [loadError, setLoadError] = useState("");
  // Bumped on every load so the editor remounts and its whitelist add box
  // clears, as it did when that box lived here.
  const [loadCount, setLoadCount] = useState(0);

  async function load() {
    setLoadError("");
    try {
      const next = await api.accountPermissions(userID);
      setLoaded(next);
      setDraft(draftFromPermissions(next));
      setLoadCount((n) => n + 1);
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

  const dirty = !samePermissionDraft(draft, draftFromPermissions(loaded));

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
      <PermissionEditor key={loadCount} value={draft} onChange={setDraft} editable={editable} selfID={userID} navigate={navigate} />
      {loaded.updated_at && <p className="muted">{t("perm.updatedAt", { time: formatDate(loaded.updated_at) })}</p>}
      {editable && (
        <div className="perm-actions">
          {dirty && <span className="muted">{t("perm.unsaved")}</span>}
          <button className="btn icon-text" type="button" disabled={!dirty} onClick={() => setDraft(draftFromPermissions(loaded))}>
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
