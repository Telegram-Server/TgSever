import { UserPlus, X } from "lucide-react";
import { useState } from "react";
import { createPortal } from "react-dom";
import { errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { useI18n } from "../i18n";

// CreateAccountModal creates a regular user account through the
// create-account admin command. Phone and name rules are enforced server-side
// by the same code as a normal sign-up; the form only keeps obviously empty
// fields from opening the confirm flow.
export function CreateAccountModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const { t } = useI18n();
  const [phone, setPhone] = useState("");
  const [firstName, setFirstName] = useState("");
  const [lastName, setLastName] = useState("");

  const valid = phone.replace(/\D/g, "").length > 0 && firstName.trim().length > 0;

  return createPortal(
    <div className="modal-backdrop" role="presentation">
      <section className="modal command-modal" role="dialog" aria-modal="true" aria-label={t("account.createTitle")}>
        <div className="modal-head">
          <div>
            <div className="eyebrow">{t("account.pageTitle")}</div>
            <h2>{t("account.createTitle")}</h2>
            <p className="bot-create-note">{t("account.createHint")}</p>
          </div>
          <button className="icon-btn" type="button" onClick={onClose} aria-label={t("common.close")}><X size={15} /></button>
        </div>
        <div className="command-body">
          <div className="bot-create-fields">
            <label className="duration-field">
              <span>{t("account.createPhone")}</span>
              <input value={phone} onChange={(event) => setPhone(event.target.value)} type="tel" placeholder="+15550001234" />
            </label>
            <label className="duration-field">
              <span>{t("account.createFirstName")}</span>
              <input value={firstName} onChange={(event) => setFirstName(event.target.value)} maxLength={64} />
            </label>
            <label className="duration-field">
              <span>{t("account.createLastName")}</span>
              <input value={lastName} onChange={(event) => setLastName(event.target.value)} maxLength={64} />
            </label>
          </div>
        </div>
        <div className="modal-actions">
          <button className="btn" type="button" onClick={onClose}>{t("common.close")}</button>
          <ActionButton
            label={t("account.create")}
            icon={<UserPlus size={15} />}
            tone="neutral"
            disabled={!valid}
            path="/api/actions/create-account"
            payload={() => ({ phone: phone.trim(), first_name: firstName.trim(), last_name: lastName.trim() })}
            onError={(err) => errorMessage(err).includes("PHONE_NUMBER_OCCUPIED") ? t("account.createPhoneOccupied") : undefined}
            onDone={onCreated}
          />
        </div>
      </section>
    </div>,
    document.body
  );
}
