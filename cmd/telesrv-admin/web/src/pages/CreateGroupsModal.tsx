import { Check, CheckCircle2, Copy, Loader2, Pencil, Play, RotateCcw, X } from "lucide-react";
import { useMemo, useState } from "react";
import { createPortal } from "react-dom";
import { APIError, api, errorMessage } from "../api";
import { UserPicker } from "../components/EntityPicker";
import { Alert, Badge } from "../components/ui";
import { type TFunction, useI18n } from "../i18n";
import { type GroupTitleIssue, maxGroupBatchRows, maxGroupTitleLength, normalizeGroupTitles } from "../lib/groupTitles";
import type { AccountRow, CommandResult } from "../types";

// CreateGroupsModal 以某个账号身份批量建超级群（#168）：每行一个群名，理由只填一次，
// 先逐行试跑，确认后逐行提交 channel.create_group 命令。
//
// 不复用 CommandFlow：它一次只处理一个命令，这里一批最多 200 个。理由必填、试跑后才能
// 确认的规则照搬。
//
// command id：试跑不带 id，由后台生成 dry-…。提交带前端生成的固定执行 id
// exec-create-group-<批次>-<行号>-<次数>，确认与重试都用它。中途断网、结果未知的行重试时
// 用同一个 id，已成功的会作为 already_executed 返回原来的群，不会重复建群；服务器明确记下
// 失败（status=failed）的行，同一个 id 重放只会拿回那次失败，所以下次重试换下一个次数。

const path = "/api/actions/create-group";

type DryState = "pending" | "running" | "ok" | "failed";
type ExecState = "idle" | "running" | "ok" | "failed" | "unknown";

type GroupRow = {
  line: number;
  title: string;
  dry: DryState;
  exec: ExecState;
  attempt: number;
  error?: string;
  channelID?: number;
  inviteLink?: string;
  replayed?: boolean;
};

function newBatchID(): string {
  const random = Math.random().toString(36).slice(2, 10);
  return `${Date.now().toString(36)}${random}`;
}

function execCommandID(batchID: string, row: GroupRow): string {
  return `exec-create-group-${batchID}-${row.line}-${row.attempt}`;
}

const failureCodes: Record<string, string> = {
  GROUP_OWNER_NOT_FOUND: "groups.errorOwnerNotFound",
  GROUP_OWNER_INVALID: "groups.errorOwnerInvalid",
  GROUP_OWNER_FROZEN: "groups.errorOwnerFrozen",
  GROUP_TITLE_INVALID: "groups.errorTitleInvalid"
};

function failureText(t: TFunction, result: CommandResult | null, fallback: string): string {
  const key = result?.code ? failureCodes[result.code] : undefined;
  if (key) return t(key);
  return result?.error || result?.message || fallback;
}

// carriedResult 取出失败响应体里的 CommandResult（后台对失败命令返回 502 + 结果）。
function carriedResult(err: unknown): CommandResult | null {
  if (err instanceof APIError && err.result && typeof err.result === "object") {
    const result = err.result as CommandResult;
    if (typeof result.status === "string") return result;
  }
  return null;
}

function issueLabel(t: TFunction, issue: GroupTitleIssue): string {
  return issue === "duplicate" ? t("groups.issueDuplicate") : t("groups.issueTooLong", { max: maxGroupTitleLength });
}

function ownerName(owner: AccountRow | null): string {
  if (!owner) return "";
  const name = `${owner.FirstName} ${owner.LastName}`.trim();
  return name || (owner.Username ? `@${owner.Username}` : owner.Phone);
}

export function CreateGroupsModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const { t } = useI18n();
  const [owner, setOwner] = useState<AccountRow | null>(null);
  const [text, setText] = useState("");
  const [reason, setReason] = useState("");
  const [batchID, setBatchID] = useState(newBatchID);
  // rows 是试跑开始时取下的快照；有快照后输入锁定，直到操作员在尚未提交任何行时重新编辑。
  const [rows, setRows] = useState<GroupRow[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState(false);

  const batch = useMemo(() => normalizeGroupTitles(text), [text]);
  const submittable = batch.lines.filter((line) => !line.issue);
  const skipped = batch.lines.length - submittable.length;

  const locked = rows !== null;
  const dryDone = rows !== null && rows.every((row) => row.dry === "ok" || row.dry === "failed");
  const execRows = rows?.filter((row) => row.dry === "ok") ?? [];
  const execStarted = rows?.some((row) => row.exec !== "idle") ?? false;
  const created = rows?.filter((row) => row.exec === "ok") ?? [];
  const remaining = execRows.filter((row) => row.exec !== "ok");
  const allCreated = execStarted && remaining.length === 0;

  function updateRow(line: number, patch: Partial<GroupRow>) {
    setRows((current) => current?.map((row) => (row.line === line ? { ...row, ...patch } : row)) ?? current);
  }

  function editAgain() {
    setRows(null);
    setError("");
    setBatchID(newBatchID());
  }

  async function dryRun() {
    if (!reason.trim()) {
      setError(t("action.reasonRequired"));
      return;
    }
    if (!owner || submittable.length === 0 || batch.overLimit) return;
    const snapshot: GroupRow[] = submittable.map((line, index) => ({
      line: index + 1,
      title: line.title,
      dry: "pending",
      exec: "idle",
      attempt: 1
    }));
    setRows(snapshot);
    setError("");
    setBusy(true);
    try {
      for (const row of snapshot) {
        updateRow(row.line, { dry: "running" });
        try {
          const result = await api.action(path, { owner_user_id: owner.ID, title: row.title, reason, confirm: false });
          if (result.error || result.status === "failed") {
            updateRow(row.line, { dry: "failed", error: failureText(t, result, t("groups.failed")) });
          } else {
            updateRow(row.line, { dry: "ok", error: undefined });
          }
        } catch (err) {
          updateRow(row.line, { dry: "failed", error: failureText(t, carriedResult(err), errorMessage(err)) });
        }
      }
    } finally {
      setBusy(false);
    }
  }

  // execute 逐行提交还没成功的行；首次确认和重试都走这里。
  async function execute() {
    if (!owner || !rows) return;
    const pending = rows.filter((row) => row.dry === "ok" && row.exec !== "ok");
    if (pending.length === 0) return;
    setError("");
    setBusy(true);
    let anyCreated = false;
    try {
      for (const row of pending) {
        updateRow(row.line, { exec: "running", error: undefined });
        let result: CommandResult | null = null;
        let failure = "";
        try {
          result = await api.action(path, {
            command_id: execCommandID(batchID, row),
            owner_user_id: owner.ID,
            title: row.title,
            reason,
            confirm: true
          });
        } catch (err) {
          result = carriedResult(err);
          failure = errorMessage(err);
        }
        if (result && !result.error && result.status !== "failed" && !failure) {
          const details = result.details ?? {};
          updateRow(row.line, {
            exec: "ok",
            channelID: typeof details.channel_id === "number" ? details.channel_id : undefined,
            inviteLink: typeof details.invite_link === "string" ? details.invite_link : "",
            replayed: result.already_executed
          });
          anyCreated = true;
        } else if (result?.status === "failed" && result.command_id === execCommandID(batchID, row) && result.error !== "COMMAND_ID_CONFLICT") {
          // 服务器记下了这次失败：同一个 id 重放只会拿回它，下次重试换个 id。
          updateRow(row.line, { exec: "failed", attempt: row.attempt + 1, error: failureText(t, result, failure || t("groups.failed")) });
        } else {
          // 结果未知（断网、超时、命令仍在执行）：保留 id，重试时幂等地取回结果。
          updateRow(row.line, { exec: "unknown", error: failureText(t, result, failure || t("groups.unknown")) });
        }
      }
    } finally {
      setBusy(false);
      if (anyCreated) onCreated();
    }
  }

  async function copyLinks() {
    const lines = created.map((row) => `${row.title} → ${row.inviteLink || "-"}`);
    try {
      await navigator.clipboard.writeText(lines.join("\n"));
      setCopied(true);
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  function statusCell(row: GroupRow) {
    if (row.exec === "running" || row.dry === "running") return <Loader2 size={14} className="spin" />;
    if (row.exec === "ok") {
      return <Badge tone="good">{row.replayed ? t("groups.statusExisting") : t("groups.statusCreated")}</Badge>;
    }
    if (row.exec === "failed") return <><Badge tone="danger">{t("groups.statusFailed")}</Badge> {row.error}</>;
    if (row.exec === "unknown") return <><Badge tone="warn">{t("groups.statusUnknown")}</Badge> {row.error}</>;
    if (row.dry === "failed") return <><Badge tone="danger">{t("groups.statusDryFailed")}</Badge> {row.error}</>;
    if (row.dry === "ok") return <Badge tone="good">{t("groups.statusDryOK")}</Badge>;
    return <Badge>{t("groups.statusWaiting")}</Badge>;
  }

  return createPortal(
    <div className="modal-backdrop" role="presentation">
      <section className="modal command-modal" role="dialog" aria-modal="true" aria-label={t("groups.createTitle")}>
        <div className="modal-head">
          <div>
            <div className="eyebrow">{t("channel.pageTitle")}</div>
            <h2>{t("groups.createTitle")}</h2>
            <p className="bot-create-note">{t("groups.createHint")}</p>
          </div>
          <button className="icon-btn" type="button" onClick={onClose} disabled={busy} aria-label={t("common.close")}><X size={15} /></button>
        </div>
        <div className="command-body">
          <div className="command-steps">
            <div className={`command-step ${reason.trim() ? "done" : "active"}`}>
              <span>1</span><strong>{t("action.stepReason")}</strong>
            </div>
            <div className={`command-step ${dryDone ? "done" : reason.trim() ? "active" : ""}`}>
              <span>2</span><strong>{t("action.stepDryRun")}</strong>
            </div>
            <div className={`command-step ${allCreated ? "done" : dryDone && execRows.length > 0 ? "active" : ""}`}>
              <span>3</span><strong>{t("action.stepConfirm")}</strong>
            </div>
          </div>
          {locked ? (
            <p className="bot-create-note">{t("groups.ownerLocked", { owner: ownerName(owner), id: owner?.ID ?? "" })}</p>
          ) : (
            <UserPicker label={t("groups.owner")} value={owner} onChange={setOwner} />
          )}
          <label className="form-field">
            <span>{t("groups.titles", { max: maxGroupBatchRows })}</span>
            <textarea value={text} onChange={(event) => setText(event.target.value)} rows={6} disabled={locked} placeholder={t("groups.titlesPlaceholder")} />
          </label>
          <p className="bot-create-note">{t("groups.summary", { count: submittable.length, skipped })}</p>
          {batch.overLimit && <Alert>{t("groups.overLimit", { max: maxGroupBatchRows, count: batch.lines.length })}</Alert>}
          <label className="form-field">
            <span>{t("action.reason")}</span>
            <textarea value={reason} onChange={(event) => setReason(event.target.value)} rows={2} disabled={locked} placeholder={t("action.reasonPlaceholder")} />
          </label>
          {error && <Alert>{error}</Alert>}
          {batch.lines.length > 0 && (
            <div className="table-wrap">
              <table className="data-table">
                <thead>
                  <tr>
                    <th>#</th>
                    <th>{t("channel.title")}</th>
                    <th>{t("action.status")}</th>
                    <th>{t("groups.inviteLink")}</th>
                  </tr>
                </thead>
                <tbody>
                  {rows
                    ? rows.map((row) => (
                      <tr key={row.line}>
                        <td className="mono">{row.line}</td>
                        <td>{row.title}</td>
                        <td>{statusCell(row)}</td>
                        <td className="mono">{row.exec === "ok" ? (row.inviteLink || "-") : ""}</td>
                      </tr>
                    ))
                    : batch.lines.slice(0, maxGroupBatchRows).map((line, index) => (
                      <tr key={index}>
                        <td className="mono">{index + 1}</td>
                        <td>{line.title}</td>
                        <td>{line.issue ? <Badge tone="warn">{issueLabel(t, line.issue)}</Badge> : <Badge>{t("groups.statusReady")}</Badge>}</td>
                        <td></td>
                      </tr>
                    ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
        <div className="modal-actions">
          <button className="btn" type="button" onClick={onClose} disabled={busy}>{t("common.close")}</button>
          {locked && !execStarted && (
            <button className="btn icon-text" type="button" onClick={editAgain} disabled={busy}>
              <Pencil size={15} /> {t("groups.editAgain")}
            </button>
          )}
          {created.length > 0 && (
            <button className="btn icon-text" type="button" onClick={() => void copyLinks()} disabled={busy}>
              {copied ? <Check size={15} /> : <Copy size={15} />} {copied ? t("common.copied") : t("groups.copyLinks")}
            </button>
          )}
          {!locked && (
            <button
              className="btn icon-text"
              type="button"
              onClick={() => void dryRun()}
              disabled={busy || !owner || submittable.length === 0 || batch.overLimit}
            >
              {busy ? <Loader2 size={15} className="spin" /> : <Play size={15} />} {t("action.runDry")}
            </button>
          )}
          {locked && !execStarted && (
            <button className="btn danger icon-text" type="button" onClick={() => void execute()} disabled={busy || !dryDone || execRows.length === 0}>
              {busy ? <Loader2 size={15} className="spin" /> : <CheckCircle2 size={15} />} {t("groups.confirm", { count: execRows.length })}
            </button>
          )}
          {execStarted && remaining.length > 0 && (
            <button className="btn warn icon-text" type="button" onClick={() => void execute()} disabled={busy}>
              {busy ? <Loader2 size={15} className="spin" /> : <RotateCcw size={15} />} {t("groups.retry", { count: remaining.length })}
            </button>
          )}
        </div>
      </section>
    </div>,
    document.body
  );
}
