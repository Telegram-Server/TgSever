import { RefreshCw } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { SectionTabs } from "../components/SectionTabs";
import { Alert, Badge, EmptyRow, LoadingSurface, PageFrame } from "../components/ui";
import { useI18n } from "../i18n";
import { formatBytes, formatDate } from "../lib/format";
import type { Navigate } from "../routing";
import type { ClientUpdateEntry, ClientUpdateMode, ClientUpdateStatus } from "../types";

const modeTone: Record<ClientUpdateMode, "good" | "neutral" | "warn"> = {
  in_process: "good",
  external: "warn",
  disabled: "neutral"
};

const modeLabelKey: Record<ClientUpdateMode, string> = {
  in_process: "clientUpdates.modeInProcess",
  external: "clientUpdates.modeExternal",
  disabled: "clientUpdates.modeDisabled"
};

// shortSHA keeps the table narrow; the full digest stays in the cell's title so
// it can be compared against `telesrv-update publish` output.
function shortSHA(value: string): string {
  return value.length > 16 ? `${value.slice(0, 16)}…` : value;
}

// Desktop client updates: what the update service publishes to custom Telegram
// Desktop builds. The status card says whether the service runs in this
// process, in a separate telesrv-update, or not at all, and warns while the
// manifest on disk fails to load (the previous good one keeps being served).
// The table lists every desktop entry, disabled ones included.
export function ClientUpdatesPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const [status, setStatus] = useState<ClientUpdateStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function load() {
    setBusy(true);
    setError("");
    try {
      setStatus(await api.clientUpdates());
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => { void load(); }, []);

  return (
    <PageFrame
      title={t("clientUpdates.title")}
      eyebrow={t("clientUpdates.eyebrow")}
      actions={
        <button className="btn icon-text" type="button" onClick={() => void load()} disabled={busy}>
          <RefreshCw size={15} className={busy ? "spin" : undefined} /> {t("common.refresh")}
        </button>
      }
    >
      <SectionTabs sectionId="server" active="/client-updates" navigate={navigate} />
      {error && <Alert>{error}</Alert>}
      {!status ? (
        !error && <LoadingSurface label={t("common.loading")} />
      ) : (
        <>
          <StatusCard status={status} />
          {status.mode === "in_process" && <EntriesTable entries={status.entries} />}
        </>
      )}
    </PageFrame>
  );
}

function StatusCard({ status }: { status: ClientUpdateStatus }) {
  const { t } = useI18n();
  return (
    <section className="section-block">
      <h2>{t("clientUpdates.status")}</h2>
      <div className="card-body">
        {status.reload_error && (
          <Alert>{t("clientUpdates.reloadFailed", { reason: status.reload_error })}</Alert>
        )}
        <div className="attr-block">
          <div className="result-line">
            <span>{t("clientUpdates.mode")}</span>
            <strong><Badge tone={modeTone[status.mode]}>{t(modeLabelKey[status.mode])}</Badge></strong>
          </div>
          {status.mode !== "disabled" && (
            <div className="result-line">
              <span>{t("clientUpdates.publicURL")}</span>
              <strong className="mono">{status.public_url || t("clientUpdates.notSet")}</strong>
            </div>
          )}
          {status.mode === "external" && status.service_url && (
            <div className="result-line">
              <span>{t("clientUpdates.serviceURL")}</span>
              <strong className="mono">{status.service_url}</strong>
            </div>
          )}
        </div>
        {status.mode === "disabled" && <p className="muted">{t("clientUpdates.disabledHint")}</p>}
        {status.mode === "external" && <p className="muted">{t("clientUpdates.externalHint")}</p>}
      </div>
    </section>
  );
}

function EntriesTable({ entries }: { entries: ClientUpdateEntry[] }) {
  const { t } = useI18n();
  return (
    <section className="section-block">
      <h2>{t("clientUpdates.entries")}</h2>
      <div className="table-wrap">
        <table className="data-table">
          <thead>
            <tr>
              <th>{t("common.platform")}</th>
              <th>{t("clientUpdates.channel")}</th>
              <th>{t("clientUpdates.build")}</th>
              <th>{t("clientUpdates.file")}</th>
              <th>{t("clientUpdates.size")}</th>
              <th>{t("clientUpdates.sha256")}</th>
              <th>{t("clientUpdates.created")}</th>
              <th>{t("common.status")}</th>
            </tr>
          </thead>
          <tbody>
            {entries.map((entry) => (
              <tr key={`${entry.platform}.${entry.channel}`}>
                <td className="mono">{entry.platform}</td>
                <td>{entry.channel}</td>
                <td>
                  {entry.build}
                  {entry.version && <div className="muted">{entry.version}</div>}
                </td>
                <td className="mono">{entry.file}</td>
                <td>{entry.size ? formatBytes(String(entry.size)) : "—"}</td>
                <td className="mono" title={entry.sha256}>{shortSHA(entry.sha256)}</td>
                <td className="muted">{entry.created ? formatDate(entry.created) : "—"}</td>
                <td>
                  {entry.disabled
                    ? <Badge tone="danger">{t("clientUpdates.entryDisabled")}</Badge>
                    : <Badge tone="good">{t("clientUpdates.entryPublished")}</Badge>}
                </td>
              </tr>
            ))}
            {entries.length === 0 && <EmptyRow colSpan={8} />}
          </tbody>
        </table>
      </div>
    </section>
  );
}
