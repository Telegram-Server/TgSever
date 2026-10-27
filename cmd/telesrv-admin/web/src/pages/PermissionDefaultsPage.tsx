import { RotateCcw, ShieldCheck } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { draftFromPermissions, PermissionEditor, permissionFieldsFrom, samePermissionDraft, type PermissionDraft } from "../components/PermissionEditor";
import { SectionTabs } from "../components/SectionTabs";
import { Alert, Badge, LoadingSurface, PageFrame, SectionHead } from "../components/ui";
import { useI18n } from "../i18n";
import { formatDate } from "../lib/format";
import { permissionAccountsManage, usePermissions } from "../permissions";
import type { Navigate } from "../routing";
import type { PermissionDefaults } from "../types";

// TemplateDraft is the editable template: the shared permission draft plus the
// switch that decides whether self sign-ups get the template too.
type TemplateDraft = { permissions: PermissionDraft; applyToSelfSignup: boolean };

function draftFromDefaults(d: PermissionDefaults): TemplateDraft {
  return { permissions: draftFromPermissions({ ...d, user_id: 0 }), applyToSelfSignup: d.apply_to_self_signup };
}

function sameTemplateDraft(a: TemplateDraft, b: TemplateDraft): boolean {
  return a.applyToSelfSignup === b.applyToSelfSignup && samePermissionDraft(a.permissions, b.permissions);
}

// PermissionDefaultsPage is the Accounts section's 默认权限 tab (#161): the one
// permission template new accounts are created with. Anyone with accounts.read
// sees it; saving goes through the command flow (dry run, confirm, audit) and
// needs accounts.manage. The template is a snapshot copied at account creation,
// so saving it never touches existing accounts.
export function PermissionDefaultsPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const { can } = usePermissions();
  const editable = can(permissionAccountsManage);
  const [loaded, setLoaded] = useState<PermissionDefaults | null>(null);
  const [draft, setDraft] = useState<TemplateDraft | null>(null);
  const [loadError, setLoadError] = useState("");
  // Bumped on every load so the editor remounts and its whitelist add box clears.
  const [loadCount, setLoadCount] = useState(0);

  async function load() {
    setLoadError("");
    try {
      const next = await api.permissionDefaults();
      setLoaded(next);
      setDraft(draftFromDefaults(next));
      setLoadCount((n) => n + 1);
    } catch (err) {
      setLoadError(errorMessage(err));
    }
  }

  useEffect(() => {
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  function explainError(err: unknown): string | undefined {
    if (errorMessage(err).includes("PERMISSION_WHITELIST_INVALID")) return t("permDefaults.errorWhitelistInvalid");
    return undefined;
  }

  let body;
  if (loadError) {
    body = <Alert>{t("permDefaults.loadFailed", { error: loadError })}</Alert>;
  } else if (!loaded || !draft) {
    body = <LoadingSurface label={t("permDefaults.loading")} />;
  } else {
    const dirty = !sameTemplateDraft(draft, draftFromDefaults(loaded));
    const p = draft.permissions;
    // An enforced, empty whitelist blocks every private message of every account
    // that gets this template — worth saying out loud before it is saved.
    const emptyWhitelist = p.enabled && p.whitelistEnabled && p.whitelist.length === 0;
    const status = !loaded.found ? (
      <Badge>{t("permDefaults.statusNone")}</Badge>
    ) : p.enabled ? (
      <Badge tone="warn">{t("perm.statusOn")}</Badge>
    ) : (
      <Badge>{t("perm.statusOff")}</Badge>
    );
    body = (
      <section className="section-block account-permissions">
        <SectionHead title={t("permDefaults.title")} text={t("permDefaults.hint")} action={status} />
        {!loaded.found && <p className="muted">{t("permDefaults.none")}</p>}
        {!editable && <p className="muted">{t("permDefaults.readOnly")}</p>}
        <label className="checkline perm-master">
          <input
            type="checkbox"
            checked={draft.applyToSelfSignup}
            disabled={!editable}
            onChange={(e) => setDraft({ ...draft, applyToSelfSignup: e.target.checked })}
          />
          <strong>{t("permDefaults.selfSignup")}</strong>
        </label>
        <p className="muted">{t("permDefaults.selfSignupHint")}</p>
        <PermissionEditor
          key={loadCount}
          value={p}
          onChange={(next) => setDraft({ ...draft, permissions: next })}
          editable={editable}
          navigate={navigate}
          enabledLabel={t("permDefaults.enabled")}
        />
        {emptyWhitelist && <Alert>{t("permDefaults.whitelistEmptyWarning")}</Alert>}
        {loaded.updated_at && <p className="muted">{t("perm.updatedAt", { time: formatDate(loaded.updated_at) })}</p>}
        {editable && (
          <div className="perm-actions">
            {dirty && <span className="muted">{t("perm.unsaved")}</span>}
            <button className="btn icon-text" type="button" disabled={!dirty} onClick={() => setDraft(draftFromDefaults(loaded))}>
              <RotateCcw size={15} /> {t("perm.discard")}
            </button>
            <ActionButton
              label={t("permDefaults.save")}
              icon={<ShieldCheck size={15} />}
              tone="warn"
              path="/api/actions/set-permission-defaults"
              payload={() => ({ ...permissionFieldsFrom(p), apply_to_self_signup: draft.applyToSelfSignup })}
              // A template that was never saved is worth saving as shown even
              // unchanged: it switches new accounts from "no template" to these values.
              disabled={loaded.found && !dirty}
              onError={explainError}
              onDone={() => void load()}
            />
          </div>
        )}
      </section>
    );
  }

  return (
    <PageFrame title={t("permDefaults.title")} eyebrow={t("permDefaults.eyebrow")}>
      <SectionTabs sectionId="accounts" active="/accounts/permission-defaults" navigate={navigate} />
      {body}
    </PageFrame>
  );
}
