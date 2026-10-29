// 以账号身份批量建群（#168）的群名规整：每行一个群名，去首尾空白、丢弃空行，标出重复
// 和超长的行。有问题的行不提交；超过单批上限时整批不能试跑。

// 与后端 domain.MaxChannelTitleLength 一致，按字符（码点）计数。
export const maxGroupTitleLength = 128;

// 单批最多的群名行数（规整后，含有问题的行）。
export const maxGroupBatchRows = 200;

// goSpace 与 Go unicode.IsSpace 的字符集一致：Latin-1 的 \t \n \v \f \r、空格、U+0085、U+00A0，
// 以及 Unicode White_Space 中的 U+1680、U+2000–U+200A、U+2028、U+2029、U+202F、U+205F、U+3000。
// 不用 String.prototype.trim：它不去 U+0085、却会去 U+FEFF，与后端 strings.TrimSpace 不一致，
// 两行在前端看来不同、到后端却规整成同名。
const goSpace = "\\t\\n\\v\\f\\r \\u0085\\u00A0\\u1680\\u2000-\\u200A\\u2028\\u2029\\u202F\\u205F\\u3000";
const goTrimPattern = new RegExp(`^[${goSpace}]+|[${goSpace}]+$`, "g");

// trimGoSpace 等价于 Go strings.TrimSpace。
export function trimGoSpace(text: string): string {
  return text.replace(goTrimPattern, "");
}

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
    const title = trimGoSpace(raw);
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
