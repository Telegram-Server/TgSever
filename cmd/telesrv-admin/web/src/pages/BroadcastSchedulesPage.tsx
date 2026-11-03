import { CalendarClock, Pause, Pencil, Play, RefreshCw, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { Alert, Badge, EmptyRow, Metric, PageFrame } from "../components/ui";
import { useI18n } from "../i18n";
import { formatDate } from "../lib/format";
import { permissionBroadcastsSchedule, usePermissions } from "../permissions";
import type { BroadcastScheduleListResponse, BroadcastScheduleRow } from "../types";
import { BroadcastScheduleModal, formatTimeOfDay, weekdayOrder } from "./BroadcastScheduleModal";

// formatInZone shows a run time on the schedule's own wall clock, which is what
// the operator typed; the browser's zone would make "14:00" read as something else.
function formatInZone(value: string | null, zone: string): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  try {
    return date.toLocaleString(undefined, { timeZone: zone });
  } catch {
    return formatDate(value);
  }
}

export function BroadcastSchedulesPage() {
  const { t } = useI18n();
  const { can } = usePermissions();
  const canManage = can(permissionBroadcastsSchedule);
  const [data, setData] = useState<BroadcastScheduleListResponse | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // undefined: closed; null: creating; a row: editing that row.
  const [editing, setEditing] = useState<BroadcastScheduleRow | null | undefined>(undefined);

  async function load() {
    setBusy(true);
    setError("");
    try {
      setData(await api.broadcastSchedules());
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => { void load(); }, []);

  const rows = data?.rows ?? [];
  const active = rows.filter((row) => row.Enabled).length;
  const skipped = rows.filter((row) => row.LastSkipReason !== "").length;

  function describeDays(days: number[]): string {
    if (days.length === 7) return t("broadcastSchedule.everyDay");
    return weekdayOrder.filter((day) => days.includes(day)).map((day) => t(`broadcastSchedule.day${day}`)).join(t("broadcastSchedule.daySeparator"));
  }

  return (
    <PageFrame
      title={t("broadcastSchedule.pageTitle")}
      eyebrow={t("broadcastSchedule.pageHint")}
      actions={<div className="toolbar">
        {canManage && <button className="btn warn" type="button" onClick={() => setEditing(null)}><CalendarClock size={15} /> {t("broadcastSchedule.create")}</button>}
        <button className="btn" type="button" onClick={() => load()} disabled={busy}><RefreshCw size={15} /> {t("common.refresh")}</button>
      </div>}
    >
      {error && <Alert>{error}</Alert>}
      {editing !== undefined && (
        // onSaved only reloads: the confirm result is shown by the ActionButton
        // inside the modal, and closing the modal here would unmount it unread.
        <BroadcastScheduleModal schedule={editing} onClose={() => setEditing(undefined)} onSaved={() => void load()} />
      )}
      <div className="metric-row">
        <Metric label={t("broadcastSchedule.total")} value={String(rows.length)} />
        <Metric label={t("broadcastSchedule.active")} value={String(active)} tone={active > 0 ? "good" : "neutral"} />
        <Metric label={t("broadcastSchedule.withSkips")} value={String(skipped)} tone={skipped > 0 ? "warn" : "neutral"} />
      </div>
      <div className="table-wrap">
        <table className="data-table">
          <thead><tr>
            <th>{t("broadcastSchedule.id")}</th><th>{t("broadcast.message")}</th><th>{t("broadcastSchedule.rule")}</th>
            <th>{t("broadcast.status")}</th><th>{t("broadcastSchedule.nextRun")}</th><th>{t("broadcastSchedule.lastRun")}</th>
            <th>{t("broadcastSchedule.lastSkip")}</th><th>{t("broadcast.createdBy")}</th>
            {canManage && <th>{t("common.actions")}</th>}
          </tr></thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.ID}>
                <td className="mono">{row.ID}</td>
                <td title={row.Message}>{row.Message.length > 100 ? `${row.Message.slice(0, 100)}…` : row.Message}</td>
                <td>{describeDays(row.Weekdays)} {formatTimeOfDay(row.TimeOfDay)}<br /><small className="mono">{row.Timezone}</small></td>
                <td><Badge tone={row.Enabled ? "good" : "neutral"}>{t(row.Enabled ? "broadcastSchedule.statusEnabled" : "broadcastSchedule.statusPaused")}</Badge></td>
                <td>{formatInZone(row.NextRunAt, row.Timezone) || "-"}</td>
                <td>
                  {row.LastRunAt ? <>{formatInZone(row.LastRunAt, row.Timezone)}<br /><small className="mono">#{row.LastBroadcastID}</small></> : "-"}
                  <br /><small>{t("broadcastSchedule.counts", { runs: row.RunCount, skips: row.SkippedCount })}</small>
                </td>
                <td>
                  {row.LastSkipReason
                    ? <><Badge tone="warn">{t(`broadcastSchedule.skip.${row.LastSkipReason}`)}</Badge><br /><small>{formatInZone(row.LastSkippedAt, row.Timezone)}</small></>
                    : "-"}
                </td>
                <td>{row.UpdatedBy || row.CreatedBy || "-"}</td>
                {canManage && (
                  <td>
                    <div className="row-actions">
                      <button className="btn compact-btn" type="button" onClick={() => setEditing(row)}><Pencil size={14} /> {t("broadcastSchedule.edit")}</button>
                      <ActionButton
                        compact
                        tone={row.Enabled ? "neutral" : "primary"}
                        label={t(row.Enabled ? "broadcastSchedule.pause" : "broadcastSchedule.resume")}
                        icon={row.Enabled ? <Pause size={14} /> : <Play size={14} />}
                        path="/api/actions/update-broadcast-schedule"
                        payload={() => ({
                          schedule_id: row.ID,
                          message: row.Message,
                          timezone: row.Timezone,
                          time_of_day: row.TimeOfDay,
                          weekdays: row.Weekdays,
                          enabled: !row.Enabled
                        })}
                        refreshOnClose
                        onDone={() => void load()}
                      />
                      <ActionButton
                        compact
                        tone="danger"
                        label={t("broadcastSchedule.delete")}
                        icon={<Trash2 size={14} />}
                        path="/api/actions/delete-broadcast-schedule"
                        payload={() => ({ schedule_id: row.ID })}
                        refreshOnClose
                        onDone={() => void load()}
                      />
                    </div>
                  </td>
                )}
              </tr>
            ))}
            {rows.length === 0 && <EmptyRow colSpan={canManage ? 9 : 8} />}
          </tbody>
        </table>
      </div>
    </PageFrame>
  );
}
