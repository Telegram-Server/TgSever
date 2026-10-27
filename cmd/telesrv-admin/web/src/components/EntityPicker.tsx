import { Check, Loader2, Search, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { api, errorMessage } from "../api";
import { useI18n } from "../i18n";
import { channelKind, displayName, displayPhone, displayUsername } from "../lib/format";
import type { AccountRow, BotRow, ChannelRow } from "../types";
import { Badge } from "./ui";

// UserPicker searches accounts and picks one. With a value it keeps that
// selection (select mode). UserAdder below is the same search in add mode.
export function UserPicker({
  label,
  value,
  onChange,
  variant = "panel"
}: {
  label: string;
  value: AccountRow | null;
  onChange: (row: AccountRow | null) => void;
  variant?: "panel" | "dropdown";
}) {
  return <UserSearch label={label} value={value} onPick={onChange} onClear={() => onChange(null)} variant={variant} />;
}

// UserAdder is the add mode of the user search: picking a row calls onAdd and
// clears the search box, keeping the results open so several accounts can be
// added in a row. It holds no selection. unavailable names the rows that cannot
// be picked (the account itself, people already added): it returns the badge
// shown in place of the row's status, or undefined for a pickable row.
export function UserAdder({
  label,
  onAdd,
  unavailable,
  disabled = false,
  placeholder
}: {
  label: string;
  onAdd: (row: AccountRow) => void;
  unavailable?: (row: AccountRow) => string | undefined;
  disabled?: boolean;
  placeholder?: string;
}) {
  return (
    <UserSearch
      label={label}
      value={null}
      onPick={onAdd}
      variant="dropdown"
      addMode
      unavailable={unavailable}
      disabled={disabled}
      placeholder={placeholder}
    />
  );
}

function UserSearch({
  label,
  value,
  onPick,
  onClear,
  variant,
  addMode = false,
  unavailable,
  disabled = false,
  placeholder
}: {
  label: string;
  value: AccountRow | null;
  onPick: (row: AccountRow) => void;
  onClear?: () => void;
  variant: "panel" | "dropdown";
  addMode?: boolean;
  unavailable?: (row: AccountRow) => string | undefined;
  disabled?: boolean;
  placeholder?: string;
}) {
  const { t } = useI18n();
  const [query, setQuery] = useState("");
  const [rows, setRows] = useState<AccountRow[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [open, setOpen] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);

  function pick(row: AccountRow) {
    onPick(row);
    if (addMode) {
      setQuery("");
      inputRef.current?.focus();
      return;
    }
    setOpen(false);
  }

  async function search(showResults = false) {
    if (showResults && variant === "dropdown") {
      setOpen(true);
    }
    setBusy(true);
    setError("");
    const params = new URLSearchParams({ limit: "20" });
    if (query.trim()) {
      params.set("q", query.trim());
    }
    try {
      const result = await api.accounts(params);
      setRows(result.rows);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    void search(false);
  }, []);

  return (
    <div
      className={`entity-picker ${variant === "dropdown" ? "entity-picker-dropdown" : ""}`}
      onBlur={(event) => {
        if (variant === "dropdown" && !event.currentTarget.contains(event.relatedTarget as Node | null)) {
          setOpen(false);
        }
      }}
    >
      <div className="picker-head">
        <span>{label}</span>
        {value && onClear ? (
          <button className="link-button" type="button" onClick={() => { onClear(); setOpen(true); }}>
            <X size={13} /> {t("common.clear")}
          </button>
        ) : null}
      </div>
      {value ? (
        <div className="selected-entity">
          <Check size={15} />
          <div>
            <strong>{displayName(value)}</strong>
            <span className="mono">{value.ID}</span>
          </div>
          <span>{displayUsername(value.Username) || displayPhone(value.Phone) || "-"}</span>
        </div>
      ) : null}
      {(!value || variant !== "dropdown") && <div className="picker-search">
        <Search size={15} />
        <input
          ref={inputRef}
          aria-label={label}
          value={query}
          disabled={disabled}
          onChange={(event) => setQuery(event.target.value)}
          onFocus={() => { if (variant === "dropdown") setOpen(true); }}
          onKeyDown={(event) => {
            if (event.key === "Enter") {
              event.preventDefault();
              void search(true);
            }
          }}
          placeholder={placeholder ?? t("picker.userPlaceholder")}
        />
        <button className="btn compact-btn" type="button" onClick={() => void search(true)} disabled={busy || disabled}>
          {busy ? <Loader2 size={14} className="spin" /> : t("common.search")}
        </button>
      </div>}
      {error && <div className="picker-error">{error}</div>}
      {(variant !== "dropdown" || open) && !disabled && <div className="picker-results">
        {rows.map((row) => {
          const blocked = unavailable?.(row);
          return (
            <button
              key={row.ID}
              className={`picker-row ${value?.ID === row.ID ? "selected" : ""} ${blocked ? "is-unavailable" : ""}`}
              type="button"
              disabled={blocked !== undefined}
              onClick={() => pick(row)}
            >
              <span className="mono">{row.ID}</span>
              <strong>{displayName(row)}</strong>
              <span>{displayUsername(row.Username) || displayPhone(row.Phone) || "-"}</span>
              {blocked !== undefined ? (
                <Badge>{blocked}</Badge>
              ) : row.Bot ? (
                <Badge tone="warn">{t("common.bot")}</Badge>
              ) : row.Verified ? (
                <Badge tone="good">{t("picker.verified")}</Badge>
              ) : (
                <Badge>{t("picker.regular")}</Badge>
              )}
            </button>
          );
        })}
        {rows.length === 0 && !busy ? <div className="picker-empty">{t("common.noResults")}</div> : null}
      </div>}
    </div>
  );
}

// BotPicker is the same widget over /api/bots. Verifier status is granted to a bot
// account, and an operator knows the handle rather than the id, so the grant form
// resolves it here instead of asking for a raw number.
export function BotPicker({
  label,
  value,
  onChange
}: {
  label: string;
  value: BotRow | null;
  onChange: (row: BotRow | null) => void;
}) {
  const { t } = useI18n();
  const [query, setQuery] = useState("");
  const [rows, setRows] = useState<BotRow[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function search() {
    setBusy(true);
    setError("");
    const params = new URLSearchParams({ limit: "20" });
    if (query.trim()) {
      params.set("q", query.trim().replace(/^@/, ""));
    }
    try {
      const result = await api.bots(params);
      setRows(result.rows ?? []);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    void search();
  }, []);

  return (
    <div className="entity-picker">
      <div className="picker-head">
        <span>{label}</span>
        {value ? (
          <button className="link-button" type="button" onClick={() => onChange(null)}>
            <X size={13} /> {t("common.clear")}
          </button>
        ) : null}
      </div>
      {value ? (
        <div className="selected-entity">
          <Check size={15} />
          <div>
            <strong>{value.FirstName || "-"}</strong>
            <span className="mono">{value.ID}</span>
          </div>
          <span>{displayUsername(value.Username) || "-"}</span>
        </div>
      ) : null}
      <div className="picker-search">
        <Search size={15} />
        <input
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter") {
              event.preventDefault();
              void search();
            }
          }}
          placeholder={t("picker.botPlaceholder")}
        />
        <button className="btn compact-btn" type="button" onClick={search} disabled={busy}>
          {busy ? <Loader2 size={14} className="spin" /> : t("common.search")}
        </button>
      </div>
      {error && <div className="picker-error">{error}</div>}
      <div className="picker-results">
        {rows.map((row) => (
          <button
            key={row.ID}
            className={`picker-row ${value?.ID === row.ID ? "selected" : ""}`}
            type="button"
            onClick={() => onChange(row)}
          >
            <span className="mono">{row.ID}</span>
            <strong>{row.FirstName || "-"}</strong>
            <span>{displayUsername(row.Username) || "-"}</span>
            {row.System ? <Badge tone="warn">{t("picker.system")}</Badge> : <Badge>{t("picker.regular")}</Badge>}
          </button>
        ))}
        {rows.length === 0 && !busy ? <div className="picker-empty">{t("common.noResults")}</div> : null}
      </div>
    </div>
  );
}

export function ChannelPicker({
  label,
  value,
  onChange
}: {
  label: string;
  value: ChannelRow | null;
  onChange: (row: ChannelRow | null) => void;
}) {
  const { t } = useI18n();
  const [query, setQuery] = useState("");
  const [rows, setRows] = useState<ChannelRow[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function search() {
    setBusy(true);
    setError("");
    const params = new URLSearchParams({ limit: "20" });
    if (query.trim()) {
      params.set("q", query.trim());
    }
    try {
      const result = await api.channels(params);
      setRows(result.rows);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    void search();
  }, []);

  return (
    <div className="entity-picker">
      <div className="picker-head">
        <span>{label}</span>
        {value ? (
          <button className="link-button" type="button" onClick={() => onChange(null)}>
            <X size={13} /> {t("common.clear")}
          </button>
        ) : null}
      </div>
      {value ? (
        <div className="selected-entity">
          <Check size={15} />
          <div>
            <strong>{value.Title || "-"}</strong>
            <span className="mono">{value.ID}</span>
          </div>
          <span>{displayUsername(value.Username) || channelKind(value, t)}</span>
        </div>
      ) : null}
      <div className="picker-search">
        <Search size={15} />
        <input
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter") {
              event.preventDefault();
              void search();
            }
          }}
          placeholder={t("picker.channelPlaceholder")}
        />
        <button className="btn compact-btn" type="button" onClick={search} disabled={busy}>
          {busy ? <Loader2 size={14} className="spin" /> : t("common.search")}
        </button>
      </div>
      {error && <div className="picker-error">{error}</div>}
      <div className="picker-results">
        {rows.map((row) => (
          <button
            key={row.ID}
            className={`picker-row ${value?.ID === row.ID ? "selected" : ""}`}
            type="button"
            onClick={() => onChange(row)}
          >
            <span className="mono">{row.ID}</span>
            <strong>{row.Title || "-"}</strong>
            <span>{displayUsername(row.Username) || channelKind(row, t)}</span>
            {row.Verified ? <Badge tone="good">{t("picker.verified")}</Badge> : <Badge>{channelKind(row, t)}</Badge>}
          </button>
        ))}
        {rows.length === 0 && !busy ? <div className="picker-empty">{t("common.noResults")}</div> : null}
      </div>
    </div>
  );
}
