import type { TFunction } from "../i18n";
import type { AuditMessage, MessageDeletionRow } from "../types";

// 审计查看页的时间线：把一页按时间正序的消息整理成要画的条目。
//
//   - 同一天的消息前面放一个日期分隔；
//   - 同一发送人、同一 grouped_id 的相邻消息合成一个相册气泡；
//   - 同一发送人连续发的气泡只在第一条上显示名字、最后一条旁显示头像；
//   - 被同一次"清空历史 / 删除成员全部消息 / 删除话题"删掉的连续消息折叠成一段，
//     默认收起，只显示一条分隔线。

export type BubbleItem = {
  kind: "bubble";
  key: string;
  // 相册的全部成员（普通消息只有一条），按 ID 正序；第一条是"领头"消息。
  messages: AuditMessage[];
  firstOfRun: boolean;
  lastOfRun: boolean;
};

export type TimelineItem =
  | { kind: "day"; key: string; date: number }
  | { kind: "service"; key: string; message: AuditMessage }
  | BubbleItem
  | { kind: "cleared"; key: string; deletion: MessageDeletionRow; items: TimelineItem[]; count: number };

// 批量删除的来源：同一批次连着删掉的一段在时间线上折叠显示。
const batchSources = new Set(["history_clear", "participant_history", "topic_history", "saved_history"]);
// 少于这个数的批量删除不折叠，逐条显示更清楚。
const minFoldedBatch = 3;

function dayKey(unix: number): string {
  const d = new Date(unix * 1000);
  return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;
}

export function buildTimeline(messages: AuditMessage[], deletions: Map<number, MessageDeletionRow>): TimelineItem[] {
  const flat: TimelineItem[] = [];
  let lastDay = "";
  for (let i = 0; i < messages.length; i++) {
    const message = messages[i];
    const day = dayKey(message.Date);
    if (day !== lastDay) {
      flat.push({ kind: "day", key: `day-${message.ID}`, date: message.Date });
      lastDay = day;
    }
    if (message.Service) {
      flat.push({ kind: "service", key: `svc-${message.ID}`, message });
      continue;
    }
    const album = [message];
    while (
      message.GroupedID &&
      i + 1 < messages.length &&
      messages[i + 1].GroupedID === message.GroupedID &&
      messages[i + 1].SenderUserID === message.SenderUserID
    ) {
      album.push(messages[++i]);
    }
    flat.push({ kind: "bubble", key: `msg-${message.ID}`, messages: album, firstOfRun: true, lastOfRun: true });
  }
  markRuns(flat);
  return foldBatches(flat, deletions);
}

// markRuns 按"同一发送人、中间没有日期或服务消息隔开"划分连续段。
function markRuns(items: TimelineItem[]) {
  for (let i = 0; i < items.length; i++) {
    const item = items[i];
    if (item.kind !== "bubble") continue;
    const prev = items[i - 1];
    const next = items[i + 1];
    const sender = item.messages[0].SenderUserID;
    item.firstOfRun = !(prev && prev.kind === "bubble" && prev.messages[0].SenderUserID === sender);
    item.lastOfRun = !(next && next.kind === "bubble" && next.messages[0].SenderUserID === sender);
  }
}

function batchOf(item: TimelineItem, deletions: Map<number, MessageDeletionRow>): MessageDeletionRow | undefined {
  if (item.kind !== "bubble") return undefined;
  const deletion = deletions.get(item.messages[0].ID);
  return deletion && batchSources.has(deletion.Source) ? deletion : undefined;
}

// foldBatches 把同一删除批次里连续的气泡收进一个 cleared 段。日期分隔夹在段中间时
// 一并收进去，展开后仍按天显示。
function foldBatches(items: TimelineItem[], deletions: Map<number, MessageDeletionRow>): TimelineItem[] {
  const out: TimelineItem[] = [];
  for (let i = 0; i < items.length; i++) {
    const deletion = batchOf(items[i], deletions);
    if (!deletion) {
      out.push(items[i]);
      continue;
    }
    const group: TimelineItem[] = [];
    let j = i;
    let count = 0;
    for (; j < items.length; j++) {
      const item = items[j];
      if (item.kind === "day") {
        const next = items[j + 1];
        if (next && batchOf(next, deletions)?.BatchID === deletion.BatchID) {
          group.push(item);
          continue;
        }
        break;
      }
      if (batchOf(item, deletions)?.BatchID !== deletion.BatchID) break;
      group.push(item);
      count += (item as BubbleItem).messages.length;
    }
    if (count < minFoldedBatch) {
      out.push(...group);
    } else {
      out.push({ kind: "cleared", key: `batch-${deletion.BatchID}-${i}`, deletion, items: group, count });
    }
    i = j - 1;
  }
  return out;
}

// clockTime 是气泡里的时分。
export function clockTime(unix: number): string {
  const d = new Date(unix * 1000);
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

function sameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

// dialogTime 是会话列表里的时间：今天显示时分，昨天显示"昨天"，今年显示月/日，更早带年份。
export function dialogTime(unix: number, t: TFunction, now = new Date()): string {
  if (!unix) return "";
  const d = new Date(unix * 1000);
  if (sameDay(d, now)) return clockTime(unix);
  const yesterday = new Date(now);
  yesterday.setDate(now.getDate() - 1);
  if (sameDay(d, yesterday)) return t("audit.yesterday");
  if (d.getFullYear() === now.getFullYear()) return `${d.getMonth() + 1}/${d.getDate()}`;
  return `${d.getFullYear()}/${d.getMonth() + 1}/${d.getDate()}`;
}

// dayLabel 是时间线里的日期分隔文字。
export function dayLabel(unix: number, t: TFunction, now = new Date()): string {
  const d = new Date(unix * 1000);
  if (sameDay(d, now)) return t("audit.today");
  const yesterday = new Date(now);
  yesterday.setDate(now.getDate() - 1);
  if (sameDay(d, yesterday)) return t("audit.yesterday");
  if (d.getFullYear() === now.getFullYear()) return t("audit.monthDay", { month: d.getMonth() + 1, day: d.getDate() });
  return t("audit.yearMonthDay", { year: d.getFullYear(), month: d.getMonth() + 1, day: d.getDate() });
}

// mediaLabel 是会话列表预览和回复引用里媒体的文字说明。
export function mediaLabel(kind: string, t: TFunction): string {
  switch (kind) {
    case "":
      return "";
    case "photo":
    case "video":
    case "gif":
    case "round":
    case "voice":
    case "audio":
    case "sticker":
    case "file":
    case "document":
    case "contact":
    case "geo":
    case "geo_live":
    case "venue":
    case "poll":
    case "dice":
    case "story":
    case "web_page":
    case "invoice":
    case "giveaway":
    case "todo":
    case "service":
      return t(`audit.media.${kind}`);
    default:
      return t("audit.media.other");
  }
}

// formatDuration 把秒数显示成 m:ss 或 h:mm:ss。
export function formatDuration(seconds: number): string {
  const s = Math.max(0, Math.round(seconds));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const rest = String(s % 60).padStart(2, "0");
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${rest}` : `${m}:${rest}`;
}
