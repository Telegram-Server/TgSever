import { Ban, Check, ChevronDown, ChevronRight, Copy, RefreshCw, RotateCcw, Send, Upload, X } from "lucide-react";
import { Fragment, useEffect, useRef, useState } from "react";
import { APIError, api, errorMessage, uploadClientUpdate } from "../api";
import { ActionButton } from "../components/ActionButton";
import { CommandFlow } from "../components/CommandFlow";
import { SectionTabs } from "../components/SectionTabs";
import { Alert, Badge, EmptyRow, LoadingSurface, PageFrame } from "../components/ui";
import { useI18n } from "../i18n";
import { formatBytes, formatDate } from "../lib/format";
import { permissionUpdatesPublish, useCan } from "../permissions";
import type { Navigate } from "../routing";
import type { ClientUpdateAndroidEntry, ClientUpdateEntry, ClientUpdateMode, ClientUpdateStatus, CommandResult } from "../types";

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

// The channels an Android release can be published to, and the languages its
// release notes are written in. A client shows the notes in its own language
// and falls back to English.
const androidChannels = ["stable", "beta", "alpha"] as const;
type AndroidChannel = (typeof androidChannels)[number];
const noteLanguages = ["zh", "en", "ru"] as const;
type NoteLanguage = (typeof noteLanguages)[number];

// While an enabled APK is still being registered as a Telegram document the
// page reloads itself, so the operator sees it turn ready without clicking.
const documentPollInterval = 10_000;

// shortSHA keeps the table narrow; the full digest stays in the cell's title so
// it can be compared against `telesrv-update publish` output.
function shortSHA(value: string): string {
  return value.length > 16 ? `${value.slice(0, 16)}…` : value;
}

// Android release names of the API levels a minSdk is likely to name.
const androidReleaseBySDK: Record<number, string> = {
  21: "5.0", 22: "5.1", 23: "6.0", 24: "7.0", 25: "7.1", 26: "8.0", 27: "8.1", 28: "9",
  29: "10", 30: "11", 31: "12", 32: "12L", 33: "13", 34: "14", 35: "15", 36: "16"
};

function documentPending(entry: ClientUpdateAndroidEntry): boolean {
  return !entry.disabled && Boolean(entry.file) && entry.document_ready === false;
}

// Client updates: what the update service publishes to custom Telegram Desktop
// builds and to the Android app. The status card says whether the service runs
// in this process, in a separate telesrv-update, or not at all, and warns while
// the manifest on disk fails to load (the previous good one keeps being
// served). The tables list every desktop and Android entry, disabled ones
// included; updates.publish sessions also get their Actions column (disable /
// re-enable).
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

  const androidEntries = status?.android_entries ?? [];
  const pending = androidEntries.some(documentPending);
  useEffect(() => {
    if (!pending) return;
    const timer = window.setTimeout(() => void load(), documentPollInterval);
    return () => window.clearTimeout(timer);
  }, [status, pending]);

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
          {status.mode === "in_process" && canPublish && (
            <PublishCard maxSize={status.max_package_size} onPublished={() => void load()} />
          )}
          {status.mode === "in_process" && (
            <>
              <EntriesTable entries={status.entries} canManage={canPublish} onChanged={() => void load()} />
              <AndroidEntriesTable entries={androidEntries} canManage={canPublish} onChanged={() => void load()} />
            </>
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
// UPDATE_UPLOAD_FAILED is also what a dropped connection reports (api.ts).
const failureHintKey: Record<string, string> = {
  UPDATE_STAGING_NOT_FOUND: "clientUpdates.errorStaging",
  UPDATE_STAGING_EXPIRED: "clientUpdates.errorStaging",
  UPDATE_PUBLISH_IN_PROGRESS: "clientUpdates.errorLocked",
  UPDATE_PACKAGE_TOO_LARGE: "clientUpdates.errorTooLarge",
  UPDATE_STAGING_FULL: "clientUpdates.errorStagingFull",
  UPDATE_UPLOAD_FAILED: "clientUpdates.errorUploadFailed",
  UPDATE_STAGING_STORAGE_FAILED: "clientUpdates.errorStorage",
  UPDATE_ANDROID_IDENTITY_MISMATCH: "clientUpdates.errorAndroidIdentity",
  UPDATE_ANDROID_RELEASE_INVALID: "clientUpdates.errorAndroidRelease"
};

type Preflight = { result: CommandResult; reason: string; allowDowngrade: boolean };

// What the operator fills in for an APK before it is published: the upload
// only stages the package, the channel and notes come with the confirm.
type AndroidRelease = {
  channel: AndroidChannel;
  notes: Record<NoteLanguage, string>;
  canNotSkip: boolean;
  url: string;
};

const emptyRelease: AndroidRelease = { channel: "stable", notes: { zh: "", en: "", ru: "" }, canNotSkip: false, url: "" };

// An apps.android entry as the APK preflight reports it under details.current.
type AndroidCurrent = { version?: string; version_code?: number; file?: string; disabled?: boolean };

function detail(result: CommandResult, key: string): string {
  const value = result.details?.[key];
  return value === undefined || value === null ? "" : String(value);
}

function validURL(value: string): boolean {
  return /^https?:\/\/\S+$/i.test(value);
}

// PublishCard is the write side of the page, rendered only for updates.publish
// sessions. The package is uploaded once: the upload itself is the preflight
// (the server stages the file and, for a desktop package, runs a full dry-run
// publish), and the confirm only names the staging id. The confirm goes through
// CommandFlow, whose dry run verifies the staged file again before the commit
// is allowed.
//
// An APK's preflight only parses it and checks it is the same app (package
// name and signing certificate) as what is published; the channel, release
// notes and the forced-update flag are filled in here afterwards and sent with
// the confirm. Whether it is a rollback depends on the channel, so it is known
// for certain only from the CommandFlow dry run.
//
// A file over maxSize, or a missing reason, is refused here before a byte is
// sent. The server refuses both up front too, but while the browser is still
// sending a large body that answer can surface as a bare network error; one
// that does anyway, for a file over the limit, is reported as too large.
function PublishCard({ maxSize, onPublished }: { maxSize?: number; onPublished: () => void }) {
  const { t } = useI18n();
  const [file, setFile] = useState<File | null>(null);
  const [reason, setReason] = useState("");
  const [allowDowngrade, setAllowDowngrade] = useState(false);
  const [progress, setProgress] = useState<{ loaded: number; total: number } | null>(null);
  const [failure, setFailure] = useState<{ message: string; code?: string } | null>(null);
  const [preflight, setPreflight] = useState<Preflight | null>(null);
  const [release, setRelease] = useState<AndroidRelease>(emptyRelease);
  const [flowResult, setFlowResult] = useState<CommandResult | null>(null);
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
    if (overLimit(file)) {
      setFailure({ message: tooLargeMessage(file.size) });
      return;
    }
    setFailure(null);
    setPreflight(null);
    setRelease(emptyRelease);
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
      if (err instanceof APIError && err.status === 0 && overLimit(file)) {
        setFailure({ message: tooLargeMessage(file.size) });
      } else {
        setFailure({ message, code: carried?.code });
      }
    } finally {
      abortRef.current = null;
      setProgress(null);
    }
  }

  function overLimit(candidate: File | null): boolean {
    return Boolean(candidate && maxSize && candidate.size > maxSize);
  }

  function tooLargeMessage(size: number): string {
    return t("clientUpdates.fileTooLarge", { size: formatBytes(String(size)), limit: formatBytes(String(maxSize ?? 0)) });
  }

  function closeConfirm() {
    setConfirming(false);
    if (published) {
      setPreflight(null);
      setFile(null);
      setReason("");
      setAllowDowngrade(false);
      setRelease(emptyRelease);
    }
  }

  const result = preflight?.result;
  const android = result ? detail(result, "kind") === "android" : false;
  const stagingID = result ? detail(result, "staging_id") : "";
  const unchanged = result?.details?.unchanged === true;
  const fileTooLarge = overLimit(file);
  const previous = result?.details?.previous as Record<string, unknown> | undefined;
  const percent = progress && progress.total > 0 ? Math.min(100, Math.floor((progress.loaded / progress.total) * 100)) : 0;

  // Android: the selected channel's current entry, and whether the package is
  // not newer than it -- then it can only go out as a rollback.
  const current = (android ? result?.details?.current : undefined) as Record<string, AndroidCurrent> | undefined;
  const channelCurrent = current?.[release.channel];
  const versionCode = Number(result?.details?.version_code ?? 0);
  const notNewer = Boolean(channelCurrent && typeof channelCurrent.version_code === "number" && channelCurrent.version_code >= versionCode);
  const notesFilled = noteLanguages.some((lang) => release.notes[lang].trim() !== "");
  const urlOK = release.url.trim() === "" || validURL(release.url.trim());
  const releaseReady = notesFilled && urlOK;

  // Whether the confirm is a rollback is the server's call: the checkbox only
  // permits one, and a package newer than what is served is a plain publish
  // even with it ticked. A desktop package's preflight already says; for an
  // APK the dry run does, and until it has run the channel's current entry is
  // the best guess.
  const rollback = android
    ? (flowResult?.details ? flowResult.details.downgrade === true : notNewer && allowDowngrade)
    : result?.details?.downgrade === true;
  const flowHint = flowResult?.code ? failureHintKey[flowResult.code] : undefined;

  function publishPayload(): Record<string, unknown> {
    if (!preflight) return {};
    if (!android) {
      return { staging_id: stagingID, allow_downgrade: preflight.allowDowngrade };
    }
    const notes = Object.fromEntries(
      noteLanguages.map((lang) => [lang, release.notes[lang].trim()] as const).filter(([, text]) => text !== "")
    );
    return {
      staging_id: stagingID,
      allow_downgrade: allowDowngrade,
      android: { channel: release.channel, notes, can_not_skip: release.canNotSkip, url: release.url.trim() }
    };
  }

  return (
    <section className="section-block">
      <h2>{t("clientUpdates.publishTitle")}</h2>
      <div className="card-body form-stack">
        <p className="muted">{t("clientUpdates.publishHint")}</p>
        <label className="form-field">
          <span>{t("clientUpdates.packageFile")}</span>
          {/* No accept filter: desktop packages (tx64upd…) have no extension,
              and an accept list would hide them in the file picker. */}
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
        {file && fileTooLarge && <Alert>{tooLargeMessage(file.size)}</Alert>}
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
            className="btn icon-text primary"
            type="button"
            disabled={!file || uploading || fileTooLarge}
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
        {result && !android && (
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
        {result && android && (
          <>
            <AndroidPreflight result={result} current={current} />
            {published ? (
              <Badge tone="good">{t("clientUpdates.androidPublished")}</Badge>
            ) : (
              stagingID && (
                <div className="result-box form-stack">
                  <div className="result-title"><strong>{t("clientUpdates.androidReleaseTitle")}</strong></div>
                  <label className="form-field">
                    <span>{t("clientUpdates.channel")}</span>
                    <select
                      value={release.channel}
                      onChange={(event) => setRelease({ ...release, channel: event.target.value as AndroidChannel })}
                    >
                      {androidChannels.map((channel) => <option key={channel} value={channel}>{channel}</option>)}
                    </select>
                  </label>
                  <div className="result-line">
                    <span>{t("clientUpdates.replaces")}</span>
                    <strong className="mono">
                      {channelCurrent
                        ? `${channelCurrent.version ?? "—"} (${channelCurrent.version_code ?? "—"})${channelCurrent.file ? ` · ${channelCurrent.file}` : ""}${channelCurrent.disabled ? ` ${t("clientUpdates.previousDisabled")}` : ""}`
                        : t("clientUpdates.replacesNothing")}
                    </strong>
                  </div>
                  {notNewer && !allowDowngrade && (
                    <Alert>{t("clientUpdates.androidNotNewer", { channel: release.channel, code: channelCurrent?.version_code ?? "" })}</Alert>
                  )}
                  {notNewer && allowDowngrade && <Alert>{t("clientUpdates.rollbackWarning")}</Alert>}
                  <div className="form-field">
                    <span>{t("clientUpdates.androidNotes")}</span>
                    <small className="muted">{t("clientUpdates.androidNotesHint")}</small>
                  </div>
                  {noteLanguages.map((lang) => (
                    <label className="form-field" key={lang}>
                      <span>{t(`clientUpdates.androidLang.${lang}`)}</span>
                      <textarea
                        value={release.notes[lang]}
                        rows={3}
                        onChange={(event) => setRelease({ ...release, notes: { ...release.notes, [lang]: event.target.value } })}
                      />
                    </label>
                  ))}
                  {!notesFilled && <small className="field-warning">{t("clientUpdates.androidNotesRequired")}</small>}
                  <label className="permission-item standalone">
                    <input
                      type="checkbox"
                      checked={release.canNotSkip}
                      onChange={(event) => setRelease({ ...release, canNotSkip: event.target.checked })}
                    />
                    <span className="permission-copy">
                      <strong>{t("clientUpdates.androidForce")}</strong>
                      <small>{t("clientUpdates.androidForceHint")}</small>
                    </span>
                  </label>
                  {release.canNotSkip && <Alert>{t("clientUpdates.androidForceWarning")}</Alert>}
                  <label className="form-field">
                    <span>{t("clientUpdates.androidURL")}</span>
                    <input
                      type="url"
                      value={release.url}
                      placeholder="https://"
                      onChange={(event) => setRelease({ ...release, url: event.target.value })}
                    />
                    <small className="muted">{t("clientUpdates.androidURLHint")}</small>
                    {!urlOK && <small className="field-warning">{t("clientUpdates.androidURLInvalid")}</small>}
                  </label>
                  <div className="toolbar">
                    <button
                      className={`btn icon-text ${rollback ? "danger" : "primary"}`}
                      type="button"
                      disabled={!releaseReady}
                      onClick={() => {
                        setFlowResult(null);
                        setConfirming(true);
                      }}
                    >
                      <Send size={15} /> {t("clientUpdates.publish")}
                    </button>
                  </div>
                </div>
              )
            )}
          </>
        )}
      </div>
      {confirming && preflight && (
        <CommandFlow
          path="/api/actions/client-update-publish"
          label={t("clientUpdates.publishConfirm")}
          payload={publishPayload}
          initialReason={preflight.reason}
          onResult={android ? setFlowResult : undefined}
          extra={
            <>
              <p className="muted">{t("clientUpdates.confirmHint")}</p>
              {android && release.canNotSkip && <Alert>{t("clientUpdates.androidForceWarning")}</Alert>}
              {rollback && <Alert>{t("clientUpdates.rollbackWarning")}</Alert>}
              {android && flowHint && <Alert>{t(flowHint)}</Alert>}
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

// AndroidPreflight shows what the server read out of an uploaded APK, and what
// each channel serves now.
function AndroidPreflight({ result, current }: { result: CommandResult; current?: Record<string, AndroidCurrent> }) {
  const { t } = useI18n();
  const minSDK = Number(result.details?.min_sdk ?? 0);
  const release = androidReleaseBySDK[minSDK];
  const channels = current ? Object.keys(current).sort() : [];
  return (
    <div className="result-box">
      <div className="result-title"><strong>{t("clientUpdates.androidPreflight")}</strong></div>
      <div className="result-line"><span>{t("clientUpdates.androidPackageName")}</span><strong className="mono">{detail(result, "package_name")}</strong></div>
      <div className="result-line">
        <span>{t("clientUpdates.androidVersion")}</span>
        <strong>{t("clientUpdates.androidVersionValue", { version: detail(result, "version"), code: detail(result, "version_code") })}</strong>
      </div>
      <div className="result-line">
        <span>{t("clientUpdates.androidMinSdk")}</span>
        <strong>{release ? t("clientUpdates.androidMinSdkValue", { release, sdk: minSDK }) : `API ${minSDK}`}</strong>
      </div>
      {detail(result, "signing") && (
        <div className="result-line"><span>{t("clientUpdates.androidSigning")}</span><strong>v{detail(result, "signing")}</strong></div>
      )}
      <div className="result-line"><span>{t("clientUpdates.androidCert")}</span><strong><Fingerprint value={detail(result, "cert_sha256")} /></strong></div>
      <div className="result-line"><span>{t("clientUpdates.size")}</span><strong>{formatBytes(detail(result, "size"))}</strong></div>
      <div className="result-line"><span>{t("clientUpdates.sha256")}</span><strong><Fingerprint value={detail(result, "sha256")} /></strong></div>
      <div className="result-line">
        <span>{t("clientUpdates.androidCurrent")}</span>
        <strong className="mono">
          {channels.length === 0
            ? t("clientUpdates.androidCurrentNone")
            : channels.map((channel) => {
                const entry = current![channel];
                return (
                  <div key={channel}>
                    {channel}: {entry.version ?? "—"} ({entry.version_code ?? "—"}){entry.disabled ? ` ${t("clientUpdates.previousDisabled")}` : ""}
                  </div>
                );
              })}
        </strong>
      </div>
    </div>
  );
}

// Fingerprint shows a long hex digest truncated, the whole of it in the
// tooltip, and copies it in full.
function Fingerprint({ value }: { value: string }) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);
  if (!value) return <>—</>;
  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
    } catch {
      // Clipboard access is refused outside a secure context; the tooltip
      // still carries the full value.
    }
  }
  return (
    <span className="mono" title={value}>
      {shortSHA(value)}{" "}
      <button className="btn compact-btn icon-text" type="button" onClick={() => void copy()}>
        {copied ? <Check size={13} /> : <Copy size={13} />} {copied ? t("common.copied") : t("common.copy")}
      </button>
    </span>
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

// AndroidEntriesTable lists apps.android, one row per channel, with the same
// disable (warn) / re-enable (danger) actions as the desktop table. A row
// expands to its release notes, fallback link and digests. Above the table it
// says which enabled entries clients cannot get yet: an APK still being
// registered as a document, or a hand-written entry with no APK at all.
function AndroidEntriesTable({ entries, canManage, onChanged }: { entries: ClientUpdateAndroidEntry[]; canManage: boolean; onChanged: () => void }) {
  const { t } = useI18n();
  const [enabling, setEnabling] = useState<ClientUpdateAndroidEntry | null>(null);
  const [expanded, setExpanded] = useState<string | null>(null);
  const pending = entries.filter(documentPending).map((entry) => entry.channel);
  const noFile = entries.filter((entry) => !entry.disabled && !entry.file).map((entry) => entry.channel);
  const columns = canManage ? 10 : 9;
  return (
    <section className="section-block">
      <h2>{t("clientUpdates.androidEntries")}</h2>
      {pending.length > 0 && <Alert>{t("clientUpdates.androidDocPendingHint", { channels: pending.join(", ") })}</Alert>}
      {noFile.length > 0 && <Alert>{t("clientUpdates.androidNoFileHint", { channels: noFile.join(", ") })}</Alert>}
      <div className="table-wrap">
        <table className="data-table">
          <thead>
            <tr>
              <th aria-label={t("clientUpdates.androidDetails")} />
              <th>{t("clientUpdates.channel")}</th>
              <th>{t("clientUpdates.androidVersion")}</th>
              <th>{t("clientUpdates.androidPackageName")}</th>
              <th>{t("clientUpdates.file")}</th>
              <th>{t("clientUpdates.size")}</th>
              <th>{t("clientUpdates.androidForce")}</th>
              <th>{t("clientUpdates.androidDocument")}</th>
              <th>{t("common.status")}</th>
              {canManage && <th>{t("common.actions")}</th>}
            </tr>
          </thead>
          <tbody>
            {entries.map((entry) => {
              const open = expanded === entry.channel;
              const notes = Object.entries(entry.notes ?? {}).filter(([, text]) => text);
              return (
                <Fragment key={entry.channel}>
                  <tr>
                    <td>
                      <button
                        className="icon-btn"
                        type="button"
                        aria-expanded={open}
                        aria-label={t("clientUpdates.androidDetails")}
                        title={t("clientUpdates.androidDetails")}
                        onClick={() => setExpanded(open ? null : entry.channel)}
                      >
                        {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
                      </button>
                    </td>
                    <td>{entry.channel}</td>
                    <td>
                      {entry.version || "—"}
                      {entry.version_code ? <div className="muted">{entry.version_code}</div> : null}
                    </td>
                    <td className="mono">{entry.package_name || "—"}</td>
                    <td className="mono">{entry.file || "—"}</td>
                    <td>{entry.size ? formatBytes(String(entry.size)) : "—"}</td>
                    <td>{entry.can_not_skip ? <Badge tone="warn">{t("common.yes")}</Badge> : t("common.no")}</td>
                    <td>
                      {!entry.file
                        ? <Badge tone="warn">{t("clientUpdates.androidDocNone")}</Badge>
                        : entry.document_ready === true
                          ? <Badge tone="good">{t("clientUpdates.androidDocReady")}</Badge>
                          : entry.document_ready === false
                            ? <Badge tone="warn">{t("clientUpdates.androidDocPending")}</Badge>
                            : "—"}
                    </td>
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
                            payload={() => ({ platform: "android", channel: entry.channel })}
                            onDone={onChanged}
                          />
                        )}
                      </td>
                    )}
                  </tr>
                  {open && (
                    <tr>
                      <td />
                      <td colSpan={columns - 1}>
                        <div className="attr-block">
                          <div className="result-line">
                            <span>{t("clientUpdates.androidNotes")}</span>
                            <strong>
                              {notes.length === 0
                                ? t("clientUpdates.androidNoNotes")
                                : notes.map(([lang, text]) => (
                                    <div key={lang} style={{ whiteSpace: "pre-wrap" }}>
                                      <span className="muted">{(noteLanguages as readonly string[]).includes(lang) ? t(`clientUpdates.androidLang.${lang}`) : lang}</span> {text}
                                    </div>
                                  ))}
                            </strong>
                          </div>
                          <div className="result-line"><span>{t("clientUpdates.androidURL")}</span><strong className="mono">{entry.url || "—"}</strong></div>
                          <div className="result-line"><span>{t("clientUpdates.sha256")}</span><strong>{entry.sha256 ? <Fingerprint value={entry.sha256} /> : "—"}</strong></div>
                          <div className="result-line"><span>{t("clientUpdates.androidCert")}</span><strong>{entry.cert_sha256 ? <Fingerprint value={entry.cert_sha256} /> : "—"}</strong></div>
                        </div>
                      </td>
                    </tr>
                  )}
                </Fragment>
              );
            })}
            {entries.length === 0 && <EmptyRow colSpan={columns} />}
          </tbody>
        </table>
      </div>
      {enabling && (
        <CommandFlow
          path="/api/actions/client-update-enable"
          label={t("clientUpdates.androidEnableConfirm", { channel: enabling.channel, version: enabling.version || "—" })}
          payload={() => ({ platform: "android", channel: enabling.channel })}
          extra={<Alert>{t("clientUpdates.androidEnableWarning")}</Alert>}
          onApplied={onChanged}
          onClose={() => setEnabling(null)}
        />
      )}
    </section>
  );
}
