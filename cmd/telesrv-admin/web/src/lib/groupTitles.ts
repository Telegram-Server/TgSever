// 以账号身份批量建群（#168）的群名规整：每行一个群名，去首尾空白、丢弃空行，标出重复
// 和超长的行。有问题的行不提交；超过单批上限时整批不能试跑。

// 与后端 domain.MaxChannelTitleLength 一致，按字符（码点）计数。
export const maxGroupTitleLength = 128;

// 单批最多的群名行数（规整后，含有问题的行）。
export const maxGroupBatchRows = 200;

export type GroupTitleIssue = "duplicate" | "too_long";

export type GroupTitleLine = {
  title: string;
  issue?: GroupTitleIssue;
};

export type GroupTitleBatch = {
  lines: GroupTitleLine[];
  // 规整后的行数超过 maxGroupBatchRows。
  overLimit: boolean;
};

// normalizeGroupTitles 规整粘贴进来的群名列表。同一群名第二次及以后出现的行标为重复，
// 第一次出现的仍可提交，这样同一个群不会建两次。
export function normalizeGroupTitles(text: string): GroupTitleBatch {
  const seen = new Set<string>();
  const lines: GroupTitleLine[] = [];
  for (const raw of text.split(/\r?\n/)) {
    const title = raw.trim();
    if (!title) continue;
    if (Array.from(title).length > maxGroupTitleLength) {
      lines.push({ title, issue: "too_long" });
    } else if (seen.has(title)) {
      lines.push({ title, issue: "duplicate" });
    } else {
      seen.add(title);
      lines.push({ title });
    }
  }
  return { lines, overLimit: lines.length > maxGroupBatchRows };
}
