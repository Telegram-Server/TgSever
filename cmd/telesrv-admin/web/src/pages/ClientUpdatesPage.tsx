import { Ban, RefreshCw, RotateCcw, Send, Upload, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { APIError, api, errorMessage, uploadClientUpdate } from "../api";
import { ActionButton } from "../components/ActionButton";
import { CommandFlow } from "../components/CommandFlow";
import { SectionTabs } from "../components/SectionTabs";
import { Alert, Badge, EmptyRow, LoadingSurface, PageFrame } from "../components/ui";
import { useI18n } from "../i18n";
import { formatBytes, formatDate } from "../lib/format";
import { permissionUpdatesPublish, useCan } from "../permissions";
import type { Navigate } from "../routing";
import type { ClientUpdateEntry, ClientUpdateMode, ClientUpdateStatus, CommandResult } from "../types";

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
// The table lists every desktop entry, disabled ones included; updates.publish
// sessions also get its Actions column (disable / re-enable).
export function ClientUpdatesPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const canPublish = useCan(permissionUpdatesPublish);
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
          {status.mode === "in_process" && canPublish && <PublishCard onPublished={() => void load()} />}
          {status.mode === "in_process" && (
            <EntriesTable entries={status.entries} canManage={canPublish} onChanged={() => void load()} />
          )}
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

// Codes the admin API attaches to a failed upload or publish, and the hint
// that tells the operator what to do next. Other failures (a bad signature, a
// canary package, a build that is not newer) show the server's own reason.
const failureHintKey: Record<string, string> = {
  UPDATE_STAGING_NOT_FOUND: "clientUpdates.errorStaging",
  UPDATE_STAGING_EXPIRED: "clientUpdates.errorStaging",
  UPDATE_PUBLISH_IN_PROGRESS: "clientUpdates.errorLocked",
  UPDATE_PACKAGE_TOO_LARGE: "clientUpdates.errorTooLarge"
};

type Preflight = { result: CommandResult; reason: string; allowDowngrade: boolean };

function detail(result: CommandResult, key: string): string {
  const value = result.details?.[key];
  return value === undefined || value === null ? "" : String(value);
}

// PublishCard is the write side of the page, rendered only for updates.publish
// sessions. The package is uploaded once: the upload itself is the preflight
// (the server stages the file and runs a full dry-run publish), and the confirm
// only names the staging id. The confirm goes through CommandFlow, whose dry
// run verifies the staged file again before the commit is allowed.
function PublishCard({ onPublished }: { onPublished: () => void }) {
  const { t } = useI18n();
  const [file, setFile] = useState<File | null>(null);
  const [reason, setReason] = useState("");
  const [allowDowngrade, setAllowDowngrade] = useState(false);
  const [progress, setProgress] = useState<{ loaded: number; total: number } | null>(null);
  const [failure, setFailure] = useState<{ message: string; code?: string } | null>(null);
  const [preflight, setPreflight] = useState<Preflight | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [published, setPublished] = useState(false);
  const abortRef = useRef<(() => void) | null>(null);
  const uploading = progress !== null;

  useEffect(() => () => abortRef.current?.(), []);

  async function start() {
    if (!file) return;
    if (!reason.trim()) {
      setFailure({ message: t("action.reasonRequired") });
      return;
    }
    setFailure(null);
    setPreflight(null);
    setPublished(false);
    setProgress({ loaded: 0, total: file.size });
    const params = { reason: reason.trim(), allowDowngrade };
    const upload = uploadClientUpdate(file, params, (loaded, total) => setProgress({ loaded, total }));
    abortRef.current = upload.abort;
    try {
      setPreflight({ result: await upload.done, ...params });
    } catch (err) {
      const carried = err instanceof APIError ? (err.result as Partial<CommandResult> | undefined) : undefined;
      // The server's text reads "CODE: reason"; the code picks the hint below.
      let message = errorMessage(err);
      if (carried?.code && message.startsWith(`${carried.code}: `)) {
        message = message.slice(carried.code.length + 2);
      }
      setFailure({ message, code: carried?.code });
    } finally {
      abortRef.current = null;
      setProgress(null);
    }
  }

  function closeConfirm() {
    setConfirming(false);
    if (published) {
      setPreflight(null);
      setFile(null);
      setReason("");
      setAllowDowngrade(false);
    }
  }

  const result = preflight?.result;
  const stagingID = result ? detail(result, "staging_id") : "";
  const unchanged = result?.details?.unchanged === true;
  const rollback = Boolean(preflight?.allowDowngrade) || result?.details?.downgrade === true;
  const previous = result?.details?.previous as Record<string, unknown> | undefined;
  const percent = progress && progress.total > 0 ? Math.min(100, Math.floor((progress.loaded / progress.total) * 100)) : 0;

  return (
    <section className="section-block">
      <h2>{t("clientUpdates.publishTitle")}</h2>
      <div className="card-body form-stack">
        <p className="muted">{t("clientUpdates.publishHint")}</p>
        <label className="form-field">
          <span>{t("clientUpdates.packageFile")}</span>
          <input
            type="file"
            disabled={uploading}
            onChange={(event) => {
              setFile(event.target.files?.[0] ?? null);
              setPreflight(null);
              setFailure(null);
              setPublished(false);
            }}
          />
        </label>
        {file && <div className="muted">{file.name} · {formatBytes(String(file.size))}</div>}
        <label className="form-field">
          <span>{t("action.reason")}</span>
          <textarea
            value={reason}
            rows={2}
            disabled={uploading}
            onChange={(event) => setReason(event.target.value)}
            placeholder={t("action.reasonPlaceholder")}
          />
        </label>
        <label className="permission-item standalone">
          <input type="checkbox" checked={allowDowngrade} disabled={uploading} onChange={(event) => setAllowDowngrade(event.target.checked)} />
          <span className="permission-copy">
            <strong>{t("clientUpdates.allowDowngrade")}</strong>
            <small>{t("clientUpdates.allowDowngradeHint")}</small>
          </span>
        </label>
        <div className="toolbar">
          <button
            className={`btn icon-text ${allowDowngrade ? "danger" : "primary"}`}
            type="button"
            disabled={!file || uploading}
            onClick={() => void start()}
          >
            <Upload size={15} /> {t("clientUpdates.upload")}
          </button>
          {uploading && (
            <button className="btn icon-text" type="button" onClick={() => abortRef.current?.()}>
              <X size={15} /> {t("clientUpdates.cancelUpload")}
            </button>
          )}
        </div>
        {progress && (
          <div className="progress-cell progress-wide">
            <div className="progress-bar"><span style={{ width: `${percent}%` }} /></div>
            <small>
              {progress.loaded >= progress.total
                ? t("clientUpdates.verifying")
                : t("clientUpdates.uploading", {
                    percent,
                    loaded: formatBytes(String(progress.loaded)),
                    total: formatBytes(String(progress.total))
                  })}
            </small>
          </div>
        )}
        {failure && (
          <Alert>
            {t("clientUpdates.preflightFailed", { reason: failure.message })}
            {failure.code && failureHintKey[failure.code] && <> {t(failureHintKey[failure.code])}</>}
          </Alert>
        )}
        {result && (
          <div className="result-box">
            <div className="result-title"><strong>{t("clientUpdates.preflight")}</strong></div>
            <div className="result-line"><span>{t("clientUpdates.target")}</span><strong className="mono">{detail(result, "platform")}.{detail(result, "channel")}</strong></div>
            <div className="result-line"><span>{t("clientUpdates.build")}</span><strong>{detail(result, "build")}</strong></div>
            <div className="result-line"><span>{t("clientUpdates.created")}</span><strong>{detail(result, "created") ? formatDate(detail(result, "created")) : "—"}</strong></div>
            <div className="result-line"><span>{t("clientUpdates.file")}</span><strong className="mono">{detail(result, "file")}</strong></div>
            <div className="result-line"><span>{t("clientUpdates.size")}</span><strong>{formatBytes(detail(result, "size"))}</strong></div>
            <div className="result-line"><span>{t("clientUpdates.sha256")}</span><strong className="mono">{detail(result, "sha256")}</strong></div>
            <div className="result-line">
              <span>{t("clientUpdates.replaces")}</span>
              <strong className="mono">
                {previous
                  ? `${String(previous.build)} · ${String(previous.file)}${previous.disabled ? ` ${t("clientUpdates.previousDisabled")}` : ""}`
                  : t("clientUpdates.replacesNothing")}
              </strong>
            </div>
            {unchanged && <Badge tone="good">{t("clientUpdates.alreadyPublished")}</Badge>}
            {!unchanged && rollback && <Alert>{t("clientUpdates.rollbackWarning")}</Alert>}
            {published && <Badge tone="good">{t("clientUpdates.published")}</Badge>}
            {!unchanged && !published && stagingID && (
              <div className="toolbar">
                <button className={`btn icon-text ${rollback ? "danger" : "primary"}`} type="button" onClick={() => setConfirming(true)}>
                  <Send size={15} /> {t("clientUpdates.publish")}
                </button>
              </div>
            )}
          </div>
        )}
      </div>
      {confirming && preflight && (
        <CommandFlow
          path="/api/actions/client-update-publish"
          label={t("clientUpdates.publishConfirm")}
          payload={() => ({ staging_id: stagingID, allow_downgrade: preflight.allowDowngrade })}
          initialReason={preflight.reason}
          extra={
            <>
              <p className="muted">{t("clientUpdates.confirmHint")}</p>
              {rollback && <Alert>{t("clientUpdates.rollbackWarning")}</Alert>}
            </>
          }
          onApplied={() => {
            setPublished(true);
            onPublished();
          }}
          onClose={closeConfirm}
        />
      )}
    </section>
  );
}

// EntriesTable lists the manifest's desktop entries. With canManage (an
// updates.publish session) each row gets one action: Disable for a served
// entry -- a warn-level confirm, it only stops serving the build -- or
// Re-enable for a disabled one, a danger-level confirm because the entry may
// be a build that was pulled for a reason. Both need a reason, run a dry run
// first, and refresh the list once applied.
function EntriesTable({ entries, canManage, onChanged }: { entries: ClientUpdateEntry[]; canManage: boolean; onChanged: () => void }) {
  const { t } = useI18n();
  const [enabling, setEnabling] = useState<ClientUpdateEntry | null>(null);
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
              {canManage && <th>{t("common.actions")}</th>}
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
                {canManage && (
                  <td>
                    {entry.disabled ? (
                      <button className="btn danger compact-btn icon-text" type="button" onClick={() => setEnabling(entry)}>
                        <RotateCcw size={14} /> {t("clientUpdates.enable")}
                      </button>
                    ) : (
                      <ActionButton
                        compact
                        tone="warn"
                        icon={<Ban size={14} />}
                        label={t("clientUpdates.disable")}
                        path="/api/actions/client-update-disable"
                        payload={() => ({ platform: entry.platform, channel: entry.channel })}
                        onDone={onChanged}
                      />
                    )}
                  </td>
                )}
              </tr>
            ))}
            {entries.length === 0 && <EmptyRow colSpan={canManage ? 9 : 8} />}
          </tbody>
        </table>
      </div>
      {enabling && (
        <CommandFlow
          path="/api/actions/client-update-enable"
          label={t("clientUpdates.enableConfirm", { entry: `${enabling.platform}.${enabling.channel}`, build: enabling.build })}
          payload={() => ({ platform: enabling.platform, channel: enabling.channel })}
          extra={<Alert>{t("clientUpdates.enableWarning")}</Alert>}
          onApplied={onChanged}
          onClose={() => setEnabling(null)}
        />
      )}
    </section>
  );
}
