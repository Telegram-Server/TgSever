import { Download, FileText, ImageOff, Mic, Music, Play, X } from "lucide-react";
import { useEffect, useState, type CSSProperties } from "react";
import { useI18n } from "../../i18n";
import { formatDuration, mediaLabel } from "../../lib/auditTimeline";
import { formatBytes } from "../../lib/format";
import { albumGeometry, fitBox } from "../../lib/mediaGroup";
import type { AuditMedia, AuditMessage } from "../../types";

export const photoURL = (id: string, full = false) => `/api/messages/media/photo/${id}${full ? "?full=1" : ""}`;
export const documentThumbURL = (id: string, thumb: string) => `/api/messages/media/document/${id}?thumb=${encodeURIComponent(thumb)}`;
export const documentURL = (id: string) => `/api/messages/media/document/${id}`;

// 气泡里媒体的显示上限（单张图片 / 视频按此框等比缩小，不放大）。
const singleMaxWidth = 360;
const singleMaxHeight = 320;
const albumRadius = 10;

const visualKinds = new Set(["photo", "video", "gif"]);

// isVisual 判断媒体能否进相册拼图：图片、视频、GIF（圆形视频消息除外）。
export function isVisual(media: AuditMedia | null): boolean {
  return Boolean(media && visualKinds.has(media.Kind));
}

// previewURL 是一项媒体的缩略图地址；没有可显示的图时为空。
function previewURL(media: AuditMedia): string {
  if (media.PhotoID) return photoURL(media.PhotoID);
  if (media.DocumentID && media.ThumbType) return documentThumbURL(media.DocumentID, media.ThumbType);
  return "";
}

function Unavailable({ media, style }: { media: AuditMedia; style?: CSSProperties }) {
  const { t } = useI18n();
  return (
    <div className="audit-media-missing" style={style} title={media.PhotoID || media.DocumentID}>
      <ImageOff size={18} />
      <span>{t("audit.mediaUnavailable")}</span>
    </div>
  );
}

// Visual 画一张图片、视频封面或贴纸。图片点击看原图；视频不在线播放，点击下载原文件；
// 贴纸只显示。
function Visual({
  media,
  onOpenImage,
  fill,
  style,
  src = previewURL(media)
}: {
  media: AuditMedia;
  onOpenImage: (url: string) => void;
  fill: boolean;
  style?: CSSProperties;
  src?: string;
}) {
  const { t } = useI18n();
  const [failed, setFailed] = useState(false);
  const isPhoto = media.Kind === "photo";
  const className = `audit-visual ${fill ? "fill" : ""} ${media.Kind === "round" ? "round" : ""} ${media.Kind === "sticker" ? "sticker" : ""}`;
  const body =
    !src || failed ? (
      <Unavailable media={media} style={fill ? undefined : style} />
    ) : (
      <img src={src} alt={mediaLabel(media.Kind, t)} loading="lazy" onError={() => setFailed(true)} />
    );
  if (media.Kind === "sticker") {
    return (
      <div className={className} style={style}>
        {body}
      </div>
    );
  }
  if (isPhoto) {
    return (
      <button type="button" className={className} style={fill ? undefined : style} onClick={() => !failed && onOpenImage(photoURL(media.PhotoID, true))}>
        {body}
      </button>
    );
  }
  return (
    <a className={className} style={fill ? undefined : style} href={documentURL(media.DocumentID)} download title={t("audit.downloadOriginal")}>
      {body}
      <span className="audit-play">
        <Play size={18} />
      </span>
      {media.Duration > 0 || media.Kind === "gif" ? (
        <span className="audit-duration">{media.Kind === "gif" ? "GIF" : formatDuration(media.Duration)}</span>
      ) : null}
    </a>
  );
}

// FileRow 是文件、语音、音频的卡片：图标、名称、时长或大小、下载。
function FileRow({ media }: { media: AuditMedia }) {
  const { t } = useI18n();
  const Icon = media.Kind === "voice" ? Mic : media.Kind === "audio" ? Music : FileText;
  const name = media.FileName || mediaLabel(media.Kind, t);
  const details = [media.Duration > 0 ? formatDuration(media.Duration) : "", media.Size > 0 ? formatBytes(String(media.Size)) : ""]
    .filter(Boolean)
    .join(" · ");
  return (
    <div className="audit-file">
      <Icon size={22} className="audit-file-icon" />
      <div className="audit-file-text">
        <div className="audit-file-name">{name}</div>
        {details && <div className="audit-file-meta">{details}</div>}
      </div>
      {media.DocumentID && (
        <a className="audit-file-download" href={documentURL(media.DocumentID)} download title={t("audit.downloadOriginal")}>
          <Download size={16} />
        </a>
      )}
    </div>
  );
}

// SingleMedia 画一条消息自带的一项媒体。
export function SingleMedia({ media, onOpenImage }: { media: AuditMedia; onOpenImage: (url: string) => void }) {
  const { t } = useI18n();
  if (media.Kind === "sticker") {
    // 静态贴纸本身就是图片；动画贴纸（TGS / WebM）只能显示它的缩略图。
    const box = fitBox(media.W || 160, media.H || 160, 180, 180);
    const src = media.ThumbType ? documentThumbURL(media.DocumentID, media.ThumbType) : media.MimeType === "image/webp" ? documentURL(media.DocumentID) : "";
    return <Visual media={media} src={src} onOpenImage={onOpenImage} fill={false} style={{ width: box.width, height: box.height }} />;
  }
  if (media.Kind === "round") {
    return <Visual media={media} onOpenImage={onOpenImage} fill={false} style={{ width: 220, height: 220 }} />;
  }
  if (isVisual(media)) {
    const box = fitBox(media.W, media.H, singleMaxWidth, singleMaxHeight);
    return <Visual media={media} onOpenImage={onOpenImage} fill={false} style={{ width: box.width, height: box.height }} />;
  }
  if (media.Kind === "file" || media.Kind === "voice" || media.Kind === "audio") {
    return <FileRow media={media} />;
  }
  return <span className="audit-media-chip">{mediaLabel(media.Kind, t)}</span>;
}

// MediaAlbum 画一个相册：全是图片 / 视频时按官方拼图排，否则竖排文件卡片。
// 被删除的成员叠一层虚线框。
export function MediaAlbum({
  messages,
  deleted,
  onOpenImage
}: {
  messages: AuditMessage[];
  deleted: (message: AuditMessage) => boolean;
  onOpenImage: (url: string) => void;
}) {
  const items = messages.filter((m) => m.Media);
  if (!items.every((m) => isVisual(m.Media))) {
    return (
      <div className="audit-album-list">
        {items.map((m) => (
          <div key={m.ID} className={deleted(m) ? "audit-album-deleted" : ""}>
            <SingleMedia media={m.Media as AuditMedia} onOpenImage={onOpenImage} />
          </div>
        ))}
      </div>
    );
  }
  const geometry = albumGeometry(
    items.map((m) => ({ width: m.Media?.W ?? 0, height: m.Media?.H ?? 0 })),
    albumRadius
  );
  return (
    // 宽度给定像素值：气泡按内容定宽，百分比宽度会互相依赖塌成 0；max-width 让窄气泡里整组等比缩小。
    <div className="audit-album" style={{ aspectRatio: `${geometry.width} / ${geometry.height}`, width: geometry.width }}>
      {items.map((m, i) => {
        const cell = geometry.cells[i];
        return (
          <div
            key={m.ID}
            className={`audit-album-cell ${deleted(m) ? "audit-album-deleted" : ""}`}
            style={{ left: cell.left, top: cell.top, width: cell.width, height: cell.height, borderRadius: cell.radius }}
          >
            <Visual media={m.Media as AuditMedia} onOpenImage={onOpenImage} fill />
          </div>
        );
      })}
    </div>
  );
}

// Lightbox 全屏查看原图；点击任意处或按 Esc 关闭。
export function Lightbox({ url, onClose }: { url: string; onClose: () => void }) {
  const { t } = useI18n();
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => event.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div className="audit-lightbox" role="dialog" aria-modal="true" onClick={onClose}>
      <button type="button" className="audit-lightbox-close" aria-label={t("common.close")} onClick={onClose}>
        <X size={20} />
      </button>
      <img src={url} alt="" onClick={(event) => event.stopPropagation()} />
    </div>
  );
}
