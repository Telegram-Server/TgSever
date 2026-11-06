import { RotateCcw, UserPlus, X } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { ChannelPicker, UserAdder } from "../components/EntityPicker";
import { memberFromAccount, membersByID } from "../components/PermissionEditor";
import { SectionTabs } from "../components/SectionTabs";
import { Alert, Badge, LoadingSurface, PageFrame, SectionHead } from "../components/ui";
import { useI18n } from "../i18n";
import { displayPhone, displayUsername, formatDate } from "../lib/format";
import { permissionAccountsManage, usePermissions } from "../permissions";
import type { Navigate } from "../routing";
import type { AccountRow, ChannelRow, ChannelSummary, OnboardingContactSetting, OnboardingDefaults, WhitelistMember } from "../types";

// Mirror domain.MaxOnboardingContacts / MaxOnboardingChannels; the server
// rejects a longer list with ONBOARDING_DEFAULTS_INVALID.
const maxContacts = 50;
const maxChannels = 50;

// Draft is the editable template. members and channels are display only: they
// come from the loaded template plus whatever was added by search, and are never
// compared or submitted.
type Draft = {
  contacts: OnboardingContactSetting[];
  channelIDs: number[];
  members: Record<number, WhitelistMember>;
  channels: Record<number, ChannelSummary>;
};

function draftFrom(d: OnboardingDefaults): Draft {
  const channels: Record<number, ChannelSummary> = {};
  for (const c of d.channels ?? []) channels[c.id] = c;
  return {
    contacts: (d.contacts ?? []).map((c) => ({ ...c })),
    channelIDs: [...(d.channel_ids ?? [])],
    members: membersByID(d.contact_members),
    channels
  };
}

function sameDraft(a: Draft, b: Draft): boolean {
  return (
    a.contacts.length === b.contacts.length &&
    a.contacts.every((c, i) => c.user_id === b.contacts[i].user_id && c.mutual === b.contacts[i].mutual) &&
    a.channelIDs.length === b.channelIDs.length &&
    a.channelIDs.every((id, i) => id === b.channelIDs[i])
  );
}

function summaryFromChannel(row: ChannelRow): ChannelSummary {
  return { id: row.ID, found: true, title: row.Title, username: row.Username, broadcast: row.Broadcast, megagroup: row.Megagroup, deleted: false };
}

// OnboardingDefaultsPage is the Accounts section's 新账号入驻 tab: the default
// contacts and the groups and channels every new account gets, whether an
// operator created it here or the person signed up in a client (code, password
// or guest). Anyone with accounts.read sees it; saving goes through the command
// flow and needs accounts.manage. The template is copied when an account is
// created, so saving it never touches existing accounts.
export function OnboardingDefaultsPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const { can } = usePermissions();
  const editable = can(permissionAccountsManage);
  const [loaded, setLoaded] = useState<OnboardingDefaults | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [loadError, setLoadError] = useState("");
  // Bumped on every load so the search boxes remount and clear.
  const [loadCount, setLoadCount] = useState(0);

  async function load() {
    setLoadError("");
    try {
      const next = await api.onboardingDefaults();
      setLoaded(next);
      setDraft(draftFrom(next));
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
    if (errorMessage(err).includes("ONBOARDING_DEFAULTS_INVALID")) return t("onboarding.errorInvalid");
    return undefined;
  }

  let body;
  if (loadError) {
    body = <Alert>{t("onboarding.loadFailed", { error: loadError })}</Alert>;
  } else if (!loaded || !draft) {
    body = <LoadingSurface label={t("onboarding.loading")} />;
  } else {
    const d = draft;
    const dirty = !sameDraft(d, draftFrom(loaded));
    const contactsFull = d.contacts.length >= maxContacts;
    const channelsFull = d.channelIDs.length >= maxChannels;

    const contactUnavailable = (row: AccountRow) => (d.contacts.some((c) => c.user_id === row.ID) ? t("onboarding.added") : undefined);
    const addContact = (row: AccountRow) => {
      if (contactUnavailable(row) !== undefined || contactsFull) return;
      setDraft({ ...d, contacts: [...d.contacts, { user_id: row.ID, mutual: false }], members: { ...d.members, [row.ID]: memberFromAccount(row) } });
    };
    const addChannel = (row: ChannelRow | null) => {
      if (!row || d.channelIDs.includes(row.ID) || channelsFull) return;
      setDraft({ ...d, channelIDs: [...d.channelIDs, row.ID], channels: { ...d.channels, [row.ID]: summaryFromChannel(row) } });
    };
    const memberName = (id: number) => {
      const m = d.members[id];
      if (!m) return String(id);
      if (!m.found) return t("onboarding.missingAccount", { id });
      return `${m.first_name} ${m.last_name}`.trim() || String(id);
    };
    const channelName = (id: number) => {
      const c = d.channels[id];
      if (!c) return String(id);
      if (!c.found || c.deleted) return t("onboarding.missingChannel", { id });
      return c.title || String(id);
    };

    body = (
      <>
        <section className="section-block account-permissions">
          <SectionHead
            title={t("onboarding.contactsTitle")}
            text={t("onboarding.contactsHint")}
            action={!loaded.found ? <Badge>{t("onboarding.statusNone")}</Badge> : undefined}
          />
          {!editable && <p className="muted">{t("onboarding.readOnly")}</p>}
          <div className="muted">{t("onboarding.count", { count: d.contacts.length, max: maxContacts })}</div>
          {d.contacts.length === 0 ? (
            <p className="muted">{t("onboarding.contactsEmpty")}</p>
          ) : (
            <ul className="perm-whitelist-members">
              {d.contacts.map((c) => {
                const m = d.members[c.user_id];
                const name = memberName(c.user_id);
                const secondary = m ? displayUsername(m.username) || displayPhone(m.phone) : "";
                return (
                  <li key={c.user_id} className="perm-whitelist-member">
                    <button type="button" className="link-button" onClick={() => navigate(`/accounts/${c.user_id}`)}>{name}</button>
                    {secondary && <span className="muted">{secondary}</span>}
                    <span className="mono muted">{c.user_id}</span>
                    {m?.bot && <Badge tone="warn">{t("common.bot")}</Badge>}
                    <label className="checkline">
                      <input
                        type="checkbox"
                        checked={c.mutual}
                        // A bot has no address book to add the new account to.
                        disabled={!editable || Boolean(m?.bot)}
                        onChange={(e) => setDraft({ ...d, contacts: d.contacts.map((x) => (x.user_id === c.user_id ? { ...x, mutual: e.target.checked } : x)) })}
                      />
                      {t("onboarding.mutual")}
                    </label>
                    {editable && (
                      <button
                        type="button"
                        className="icon-btn"
                        aria-label={t("onboarding.remove", { name })}
                        title={t("onboarding.remove", { name })}
                        onClick={() => setDraft({ ...d, contacts: d.contacts.filter((x) => x.user_id !== c.user_id) })}
                      >
                        <X size={12} />
                      </button>
                    )}
                  </li>
                );
              })}
            </ul>
          )}
          <p className="muted">{t("onboarding.mutualHint")}</p>
          {editable && (
            <div className="perm-whitelist-add">
              <UserAdder
                key={`contacts-${loadCount}`}
                label={t("onboarding.contactSearchLabel")}
                placeholder={t("onboarding.contactSearchPlaceholder")}
                onAdd={addContact}
                unavailable={contactUnavailable}
                disabled={contactsFull}
              />
            </div>
          )}
        </section>

        <section className="section-block account-permissions">
          <SectionHead title={t("onboarding.channelsTitle")} text={t("onboarding.channelsHint")} />
          <div className="muted">{t("onboarding.count", { count: d.channelIDs.length, max: maxChannels })}</div>
          {d.channelIDs.length === 0 ? (
            <p className="muted">{t("onboarding.channelsEmpty")}</p>
          ) : (
            <ul className="perm-whitelist-members">
              {d.channelIDs.map((id) => {
                const c = d.channels[id];
                const name = channelName(id);
                return (
                  <li key={id} className="perm-whitelist-member">
                    <button type="button" className="link-button" onClick={() => navigate(`/channels/${id}`)}>{name}</button>
                    {c?.username && <span className="muted">{displayUsername(c.username)}</span>}
                    <span className="mono muted">{id}</span>
                    {c?.found && <Badge>{c.broadcast ? t("onboarding.kindChannel") : t("onboarding.kindGroup")}</Badge>}
                    {editable && (
                      <button
                        type="button"
                        className="icon-btn"
                        aria-label={t("onboarding.remove", { name })}
                        title={t("onboarding.remove", { name })}
                        onClick={() => setDraft({ ...d, channelIDs: d.channelIDs.filter((x) => x !== id) })}
                      >
                        <X size={12} />
                      </button>
                    )}
                  </li>
                );
              })}
            </ul>
          )}
          {editable && !channelsFull && (
            <div className="perm-whitelist-add">
              <ChannelPicker key={`channels-${loadCount}-${d.channelIDs.length}`} label={t("onboarding.channelSearchLabel")} value={null} onChange={addChannel} />
            </div>
          )}
        </section>

        {loaded.updated_at && <p className="muted">{t("onboarding.updatedAt", { time: formatDate(loaded.updated_at) })}</p>}
        {editable && (
          <div className="perm-actions">
            {dirty && <span className="muted">{t("onboarding.unsaved")}</span>}
            <button className="btn icon-text" type="button" disabled={!dirty} onClick={() => setDraft(draftFrom(loaded))}>
              <RotateCcw size={15} /> {t("onboarding.discard")}
            </button>
            <ActionButton
              label={t("onboarding.save")}
              icon={<UserPlus size={15} />}
              tone="warn"
              path="/api/actions/set-onboarding-defaults"
              payload={() => ({ contacts: d.contacts, channel_ids: d.channelIDs })}
              disabled={!dirty}
              onError={explainError}
              onDone={() => void load()}
            />
          </div>
        )}
      </>
    );
  }

  return (
    <PageFrame title={t("onboarding.title")} eyebrow={t("onboarding.eyebrow")}>
      <SectionTabs sectionId="accounts" active="/accounts/onboarding" navigate={navigate} />
      {body}
    </PageFrame>
  );
}
