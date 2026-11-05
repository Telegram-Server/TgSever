package store

import (
	"context"

	"telesrv/internal/domain"
)

// AppUpdateDocumentStore 记住客户端更新安装包（按 SHA-256）注册成的文档，同一个包只入库一次。
type AppUpdateDocumentStore interface {
	// AppUpdateDocument 读取安装包对应的文档记录；没有时 found=false。
	AppUpdateDocument(ctx context.Context, sha256 string) (domain.AppUpdateDocument, bool, error)
	// PutAppUpdateDocument 记下安装包对应的文档。已有记录时保持原样（先到者胜），调用方
	// 应随后用 AppUpdateDocument 重读。
	PutAppUpdateDocument(ctx context.Context, doc domain.AppUpdateDocument) error
}
