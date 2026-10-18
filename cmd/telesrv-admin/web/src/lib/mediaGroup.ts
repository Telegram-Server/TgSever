// 相册（媒体组）拼图布局，纯函数。
// 算法移植自 Telegram Desktop 的 ui/grouped_layout.cpp（LayoutMediaGroup），经 KeFu
// （modules/messaging/media-group.ts）转写，保持与官方客户端相同的排法。

export type Size = { width: number; height: number };
export type Sides = { top: boolean; right: boolean; bottom: boolean; left: boolean };
export type GroupItemLayout = { x: number; y: number; width: number; height: number; sides: Sides };

// 聊天里相册的尺寸（tdesktop chat.style 的 maxMediaSize / minPhotoSize / historyGroupSkip）。
export const GROUP_MAX_WIDTH = 430;
export const GROUP_MIN_WIDTH = 100;
export const GROUP_SPACING = 4;

const round = (v: number) => Math.round(v);
// C++ 整数除法。
const idiv = (a: number, b: number) => Math.trunc(a / b);
const sum = (xs: number[]) => xs.reduce((a, b) => a + b, 0);

const S = (top: boolean, right: boolean, bottom: boolean, left: boolean): Sides => ({ top, right, bottom, left });
const at = (x: number, y: number, width: number, height: number, sides: Sides): GroupItemLayout => ({ x, y, width, height, sides });

// layoutMediaGroup 计算相册每一项的位置，以及它贴着哪几条外边（外边决定哪些角画圆角）。
export function layoutMediaGroup(sizes: Size[], maxWidth: number, minWidth: number, spacing: number): GroupItemLayout[] {
  const count = sizes.length;
  if (count === 0) return [];
  const ratios = sizes.map((s) => s.width / s.height);
  const proportions = ratios.map((r) => (r > 1.2 ? "w" : r < 0.8 ? "n" : "q")).join("");
  // 与官方一致：累加初值为 1。
  const averageRatio = (1 + sum(ratios)) / count;
  const maxHeight = maxWidth;
  const maxSizeRatio = maxWidth / maxHeight;

  if (count === 1) {
    const s = sizes[0];
    return [at(0, 0, maxWidth, idiv(s.height * maxWidth, s.width), S(true, true, true, true))];
  }
  if (count >= 5 || ratios.some((r) => r > 2)) return layoutComplex(ratios, averageRatio, maxWidth, minWidth, spacing);

  const [r0, r1, r2, r3] = ratios;

  if (count === 2) {
    if (proportions === "ww" && averageRatio > 1.4 * maxSizeRatio && r1 - r0 < 0.2) {
      const height = round(Math.min(maxWidth / r0, maxWidth / r1, (maxHeight - spacing) / 2));
      return [at(0, 0, maxWidth, height, S(true, true, false, true)), at(0, height + spacing, maxWidth, height, S(false, true, true, true))];
    }
    if (proportions === "ww" || proportions === "qq") {
      const width = idiv(maxWidth - spacing, 2);
      const height = round(Math.min(width / r0, width / r1, maxHeight));
      return [at(0, 0, width, height, S(true, false, true, true)), at(width + spacing, 0, width, height, S(true, true, true, false))];
    }
    const minimalWidth = round(minWidth * 1.5);
    const secondWidth = Math.min(
      round(Math.max(0.4 * (maxWidth - spacing), (maxWidth - spacing) / r0 / (1 / r0 + 1 / r1))),
      maxWidth - spacing - minimalWidth
    );
    const firstWidth = maxWidth - secondWidth - spacing;
    const height = Math.min(maxHeight, round(Math.min(firstWidth / r0, secondWidth / r1)));
    return [at(0, 0, firstWidth, height, S(true, false, true, true)), at(firstWidth + spacing, 0, secondWidth, height, S(true, true, true, false))];
  }

  if (count === 3) {
    if (proportions[0] === "n") {
      const firstHeight = maxHeight;
      const thirdHeight = round(Math.min((maxHeight - spacing) / 2, (r1 * (maxWidth - spacing)) / (r2 + r1)));
      const secondHeight = firstHeight - thirdHeight - spacing;
      const rightWidth = Math.max(minWidth, round(Math.min((maxWidth - spacing) / 2, thirdHeight * r2, secondHeight * r1)));
      const leftWidth = Math.min(round(firstHeight * r0), maxWidth - spacing - rightWidth);
      return [
        at(0, 0, leftWidth, firstHeight, S(true, false, true, true)),
        at(leftWidth + spacing, 0, rightWidth, secondHeight, S(true, true, false, false)),
        at(leftWidth + spacing, secondHeight + spacing, rightWidth, thirdHeight, S(false, true, true, false))
      ];
    }
    const firstWidth = maxWidth;
    const firstHeight = round(Math.min(firstWidth / r0, (maxHeight - spacing) * 0.66));
    const secondWidth = idiv(maxWidth - spacing, 2);
    const secondHeight = Math.min(maxHeight - firstHeight - spacing, round(Math.min(secondWidth / r1, secondWidth / r2)));
    const thirdWidth = firstWidth - secondWidth - spacing;
    return [
      at(0, 0, firstWidth, firstHeight, S(true, true, false, true)),
      at(0, firstHeight + spacing, secondWidth, secondHeight, S(false, false, true, true)),
      at(secondWidth + spacing, firstHeight + spacing, thirdWidth, secondHeight, S(false, true, true, false))
    ];
  }

  // 4 张。
  if (proportions[0] === "w") {
    const w = maxWidth;
    const h0 = round(Math.min(w / r0, (maxHeight - spacing) * 0.66));
    const h = round((maxWidth - 2 * spacing) / (r1 + r2 + r3));
    const w0 = Math.max(minWidth, round(Math.min((maxWidth - 2 * spacing) * 0.4, h * r1)));
    const w2 = round(Math.max(minWidth, (maxWidth - 2 * spacing) * 0.33, h * r3));
    const w1 = w - w0 - w2 - 2 * spacing;
    const h1 = Math.min(maxHeight - h0 - spacing, h);
    return [
      at(0, 0, w, h0, S(true, true, false, true)),
      at(0, h0 + spacing, w0, h1, S(false, false, true, true)),
      at(w0 + spacing, h0 + spacing, w1, h1, S(false, false, true, false)),
      at(w0 + spacing + w1 + spacing, h0 + spacing, w2, h1, S(false, true, true, false))
    ];
  }
  const h = maxHeight;
  const w0 = round(Math.min(h * r0, (maxWidth - spacing) * 0.6));
  const w = round((maxHeight - 2 * spacing) / (1 / r1 + 1 / r2 + 1 / r3));
  const h0 = round(w / r1);
  const h1 = round(w / r2);
  const h2 = h - h0 - h1 - 2 * spacing;
  const w1 = Math.max(minWidth, Math.min(maxWidth - w0 - spacing, w));
  return [
    at(0, 0, w0, h, S(true, false, true, true)),
    at(w0 + spacing, 0, w1, h0, S(true, true, false, false)),
    at(w0 + spacing, h0 + spacing, w1, h1, S(false, true, false, false)),
    at(w0 + spacing, h0 + h1 + 2 * spacing, w1, h2, S(false, true, true, false))
  ];
}

// 5 张及以上，或有超宽图：枚举 2–4 行的分行方案，取总高度最接近 maxWidth*4/3 的一种。
function layoutComplex(rawRatios: number[], averageRatio: number, maxWidth: number, minWidth: number, spacing: number): GroupItemLayout[] {
  const ratios = rawRatios.map((r) => (averageRatio > 1.1 ? Math.min(Math.max(r, 1), 2.75) : Math.min(Math.max(r, 0.6667), 1)));
  const count = ratios.length;
  const maxHeight = idiv(maxWidth * 4, 3);
  const attempts: Array<{ counts: number[]; heights: number[] }> = [];
  const multiHeight = (offset: number, n: number) => (maxWidth - (n - 1) * spacing) / sum(ratios.slice(offset, offset + n));
  const push = (counts: number[]) => {
    let offset = 0;
    const heights = counts.map((n) => {
      const height = multiHeight(offset, n);
      offset += n;
      return height;
    });
    attempts.push({ counts, heights });
  };

  for (let first = 1; first !== count; first++) {
    const second = count - first;
    if (first > 3 || second > 3) continue;
    push([first, second]);
  }
  for (let first = 1; first < count - 1; first++) {
    for (let second = 1; second < count - first; second++) {
      const third = count - first - second;
      if (first > 3 || second > (averageRatio < 0.85 ? 4 : 3) || third > 3) continue;
      push([first, second, third]);
    }
  }
  for (let first = 1; first < count - 1; first++) {
    for (let second = 1; second < count - first; second++) {
      for (let third = 1; third < count - first - second; third++) {
        const fourth = count - first - second - third;
        if (first > 3 || second > 3 || third > 3 || fourth > 3) continue;
        push([first, second, third, fourth]);
      }
    }
  }

  let best = attempts[0];
  let bestDiff = Number.POSITIVE_INFINITY;
  for (const attempt of attempts) {
    const total = sum(attempt.heights) + spacing * (attempt.counts.length - 1);
    const tooShort = Math.min(...attempt.heights) < minWidth ? 1.5 : 1;
    const growing = attempt.counts.some((n, i) => i > 0 && attempt.counts[i - 1] > n) ? 1.5 : 1;
    const diff = Math.abs(total - maxHeight) * tooShort * growing;
    if (diff < bestDiff) {
      best = attempt;
      bestDiff = diff;
    }
  }

  const out: GroupItemLayout[] = [];
  let index = 0;
  let y = 0;
  const rows = best.counts.length;
  best.counts.forEach((cols, row) => {
    const lineHeight = best.heights[row];
    const height = round(lineHeight);
    let x = 0;
    for (let col = 0; col < cols; col++) {
      // 每行最后一格吃掉取整误差，保证右边对齐。
      const width = col === cols - 1 ? maxWidth - x : round(ratios[index] * lineHeight);
      out.push(at(x, y, width, height, S(row === 0, col === cols - 1, row === rows - 1, col === 0)));
      x += width + spacing;
      index++;
    }
    y += height + spacing;
  });
  return out;
}

export type AlbumCell = { left: string; top: string; width: string; height: string; radius: string };
export type AlbumGeometry = { width: number; height: number; cells: AlbumCell[] };

const pct = (v: number, total: number) => `${((v / total) * 100).toFixed(4)}%`;

// albumGeometry 把像素布局换算成百分比：容器设定宽高比后，整组随气泡宽度等比缩放。
// 只有贴着相册外边的角画圆角；缺少尺寸的项按正方形排。
export function albumGeometry(sizes: Array<{ width: number; height: number }>, radius: number): AlbumGeometry {
  const layout = layoutMediaGroup(
    sizes.map((s) => (s.width > 0 && s.height > 0 ? s : { width: 1, height: 1 })),
    GROUP_MAX_WIDTH,
    GROUP_MIN_WIDTH,
    GROUP_SPACING
  );
  const width = Math.max(1, ...layout.map((l) => l.x + l.width));
  const height = Math.max(1, ...layout.map((l) => l.y + l.height));
  const corner = (on: boolean) => (on ? `${radius}px` : "0");
  return {
    width,
    height,
    cells: layout.map((l) => ({
      left: pct(l.x, width),
      top: pct(l.y, height),
      width: pct(l.width, width),
      height: pct(l.height, height),
      radius: `${corner(l.sides.top && l.sides.left)} ${corner(l.sides.top && l.sides.right)} ${corner(l.sides.bottom && l.sides.right)} ${corner(
        l.sides.bottom && l.sides.left
      )}`
    }))
  };
}

// fitBox 把单张媒体按框等比缩小（不放大），返回显示尺寸。
export function fitBox(width: number, height: number, maxWidth: number, maxHeight: number): Size {
  if (width <= 0 || height <= 0) return { width: maxWidth, height: Math.round(maxWidth * 0.75) };
  const scale = Math.min(1, maxWidth / width, maxHeight / height);
  return { width: Math.max(1, Math.round(width * scale)), height: Math.max(1, Math.round(height * scale)) };
}
