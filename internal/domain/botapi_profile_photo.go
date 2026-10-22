package domain

// BotAPIProfilePhotoKind 是 Bot API InputProfilePhoto 的 type 字段。
type BotAPIProfilePhotoKind string

const (
	// BotAPIProfilePhotoStatic 对应 InputProfilePhotoStatic：一张静态图片。
	BotAPIProfilePhotoStatic BotAPIProfilePhotoKind = "static"
)

// BotAPIProfilePhoto 是网关解析好的 InputProfilePhoto（setMyProfilePhoto 的 photo 参数）。
// 官方规定头像不能复用 file_id / URL，文件总是本次请求 multipart 上传的新文件，所以这里
// 直接携带字节。新种类（animated）加一个 Kind 常量和它专属的字段（如主帧时间戳）即可。
type BotAPIProfilePhoto struct {
	Kind     BotAPIProfilePhotoKind
	FileName string
	MimeType string
	Data     []byte
}
