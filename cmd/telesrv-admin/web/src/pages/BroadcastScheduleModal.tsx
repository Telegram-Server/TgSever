import { CalendarClock, X } from "lucide-react";
import { useState } from "react";
import { createPortal } from "react-dom";
import { ActionButton } from "../components/ActionButton";
import { useI18n } from "../i18n";
import type { BroadcastScheduleRow } from "../types";

export const defaultBroadcastTimezone = "Asia/Shanghai";

// Weekdays are stored with Go's numbering (0 = Sunday); the form lists Monday first.
export const weekdayOrder = [1, 2, 3, 4, 5, 6, 0];
const everyDay = [0, 1, 2, 3, 4, 5, 6];
const workdays = [1, 2, 3, 4, 5];

const commonTimezones = [
  "Asia/Shanghai",
  "Asia/Hong_Kong",
  "Asia/Taipei",
  "Asia/Singapore",
  "Asia/Tokyo",
  "Asia/Seoul",
  "Asia/Dubai",
  "Europe/Moscow",
  "Europe/London",
  "Europe/Berlin",
  "America/New_York",
  "America/Los_Angeles",
  "UTC"
];

export function formatTimeOfDay(minutes: number): string {
  return `${String(Math.floor(minutes / 60)).padStart(2, "0")}:${String(minutes % 60).padStart(2, "0")}`;
}

function parseTimeOfDay(value: string): number | null {
  const match = /^(\d{2}):(\d{2})$/.exec(value);
  if (!match) return null;
  const hours = Number(match[1]);
  const minutes = Number(match[2]);
  if (hours > 23 || minutes > 59) return null;
  return hours * 60 + minutes;
}

// validTimezone asks the browser's own zone database. The server has the final
// word (it rejects unknown zones on dry-run); this only catches typos early.
function validTimezone(zone: string): boolean {
  try {
    new Intl.DateTimeFormat(undefined, { timeZone: zone });
    return true;
  } catch {
    return false;
  }
}

function sameDays(a: number[], b: number[]): boolean {
  return a.length === b.length && a.every((day) => b.includes(day));
}

export function BroadcastScheduleModal({
  schedule,
  onClose,
  onSaved
}: {
  schedule: BroadcastScheduleRow | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useI18n();
  const [message, setMessage] = useState(schedule?.Message ?? "");
  const [time, setTime] = useState(formatTimeOfDay(schedule?.TimeOfDay ?? 14 * 60));
  const [weekdays, setWeekdays] = useState<number[]>(schedule?.Weekdays ?? everyDay);
  const [timezone, setTimezone] = useState(schedule?.Timezone ?? defaultBroadcastTimezone);
  const [enabled, setEnabled] = useState(schedule?.Enabled ?? true);

  const messageBytes = new TextEncoder().encode(message.trim()).length;
  const timeOfDay = parseTimeOfDay(time);
  const zone = timezone.trim();
  const zoneOK = zone !== "" && validTimezone(zone);
  const valid = message.trim().length > 0 && messageBytes <= 4096 && timeOfDay !== null && weekdays.length > 0 && zoneOK;

  function toggleDay(day: number) {
    setWeekdays((days) => days.includes(day) ? days.filter((d) => d !== day) : [...days, day]);
  }

  const editing = schedule !== null;
  return createPortal(
    <div className="modal-backdrop" role="presentation">
      <section className="modal command-modal" role="dialog" aria-modal="true" aria-label={t(editing ? "broadcastSchedule.editTitle" : "broadcastSchedule.createTitle")}>
        <div className="modal-head">
          <div>
            <div className="eyebrow">{t("layout.broadcastSchedules")}</div>
            <h2>{t(editing ? "broadcastSchedule.editTitle" : "broadcastSchedule.createTitle")}</h2>
            <p className="bot-create-note">{t("broadcastSchedule.createHint")}</p>
          </div>
          <button className="icon-btn" type="button" onClick={onClose} aria-label={t("common.close")}><X size={15} /></button>
        </div>
        <div className="command-body">
          <label className="form-field">
            <span>{t("broadcast.message")}</span>
            <textarea rows={7} maxLength={4096} value={message} onChange={(event) => setMessage(event.target.value)} placeholder={t("broadcast.messagePlaceholder")} />
            <small className="bot-create-note">{t("broadcast.messageBytes", { count: messageBytes })}</small>
          </label>
          <div className="form-field">
            <span>{t("broadcastSchedule.weekdays")}</span>
            <div className="toolbar">
              {weekdayOrder.map((day) => (
                <label key={day} className="checkline">
                  <input type="checkbox" checked={weekdays.includes(day)} onChange={() => toggleDay(day)} /> {t(`broadcastSchedule.day${day}`)}
                </label>
              ))}
            </div>
            <div className="toolbar">
              <button className={`btn compact-btn ${sameDays(weekdays, everyDay) ? "primary" : ""}`} type="button" onClick={() => setWeekdays(everyDay)}>{t("broadcastSchedule.everyDay")}</button>
              <button className={`btn compact-btn ${sameDays(weekdays, workdays) ? "primary" : ""}`} type="button" onClick={() => setWeekdays(workdays)}>{t("broadcastSchedule.workdays")}</button>
            </div>
            {weekdays.length === 0 && <small className="bot-create-note">{t("broadcastSchedule.weekdaysRequired")}</small>}
          </div>
          <label className="form-field">
            <span>{t("broadcastSchedule.time")}</span>
            <input type="time" value={time} onChange={(event) => setTime(event.target.value)} />
          </label>
          <label className="form-field">
            <span>{t("broadcastSchedule.timezone")}</span>
            <input list="broadcast-schedule-zones" value={timezone} onChange={(event) => setTimezone(event.target.value)} placeholder={defaultBroadcastTimezone} />
            <datalist id="broadcast-schedule-zones">
              {commonTimezones.map((name) => <option key={name} value={name} />)}
            </datalist>
            <small className="bot-create-note">{zoneOK ? t("broadcastSchedule.timezoneHint") : t("broadcastSchedule.timezoneInvalid")}</small>
          </label>
          <label className="checkline">
            <input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} /> {t("broadcastSchedule.enabled")}
          </label>
          <p className="bot-create-note">{t("broadcastSchedule.targetNote")}</p>
        </div>
        <div className="modal-actions">
          <button className="btn" type="button" onClick={onClose}>{t("common.close")}</button>
          <ActionButton
            label={t(editing ? "broadcastSchedule.save" : "broadcastSchedule.create")}
            icon={<CalendarClock size={15} />}
            tone="warn"
            disabled={!valid}
            path={editing ? "/api/actions/update-broadcast-schedule" : "/api/actions/create-broadcast-schedule"}
            payload={() => ({
              ...(schedule ? { schedule_id: schedule.ID } : {}),
              message: message.trim(),
              timezone: zone,
              time_of_day: timeOfDay ?? 0,
              weekdays: [...weekdays].sort((a, b) => a - b),
              enabled
            })}
            onDone={onSaved}
          />
        </div>
      </section>
    </div>,
    document.body
  );
}
