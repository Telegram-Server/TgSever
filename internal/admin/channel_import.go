package admin

import (
	"context"

	"telesrv/internal/app/channelimport"
	"telesrv/internal/domain"
)

// ChannelImportService 是频道导入的业务入口（生产里是 app/channelimport.Service）。
//
// 它不走 runCommand：一次克隆是几千次批量写入和媒体上传，逐条记审计会把命令表撑满，而这些
// 写入本身就是幂等的——消息按来源 id、媒体按来源文件标识、任务按来源频道去重。调用方由
// channels.import 权限限定，删除消息时的归属写进删除记录。
type ChannelImportService interface {
	Create(ctx context.Context, req channelimport.CreateRequest) (channelimport.CreateResult, error)
	Get(ctx context.Context, importID int64) (channelimport.Status, error)
	PutMedia(ctx context.Context, importID int64, upload channelimport.MediaUpload) (channelimport.MediaResult, error)
	ImportMessages(ctx context.Context, importID int64, messages []channelimport.Message) (channelimport.BatchResult, error)
	Finalize(ctx context.Context, importID int64, req channelimport.FinalizeRequest) (channelimport.Status, error)
	DeleteMessages(ctx context.Context, importID int64, sourceIDs []int, actor string) (channelimport.DeleteResult, error)
	SetMeta(ctx context.Context, importID int64, meta channelimport.Meta) (channelimport.Status, error)
	Detach(ctx context.Context, importID int64) (channelimport.Status, error)

	// 只读查询，供克隆程序的界面使用。
	Ping(ctx context.Context) error
	List(ctx context.Context, filter domain.ChannelImportFilter) ([]channelimport.ListedImport, error)
	Channel(ctx context.Context, channelID int64) (channelimport.ChannelInfo, error)
	SearchChannels(ctx context.Context, search channelimport.ChannelSearch) ([]channelimport.ChannelInfo, error)
	ChannelMessages(ctx context.Context, channelID int64, beforeID, limit int) ([]channelimport.MessageView, error)
	ResolveMessageIDs(ctx context.Context, importID int64, sourceIDs []int) (map[int]int, error)
}

// ChannelImport 返回频道导入的业务入口；没有接入时第二个返回值为 false。
func (s *Service) ChannelImport() (ChannelImportService, bool) {
	if s == nil || s.channelImports == nil {
		return nil, false
	}
	return s.channelImports, true
}
