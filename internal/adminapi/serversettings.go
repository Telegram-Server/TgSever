package adminapi

import (
	"context"
	"net/http"

	"telesrv/internal/admin"
)

// 服务器设置命令：管理后台把 identity 存储（777000 名称与简介、头像、欢迎消息模板、
// 登录码模板、注册开关覆盖、验证码登录开关覆盖）的写入交给主服务器执行，见 internal/admin/serveridentity.go。
// 路由与历史路由一样挂在 authenticated() 下：只有主 token 能调用，作用域 token 不行。

// serverSettingsService 是可选的，与 itemPricesService 同理：不提供它的轻量测试替身
// 保持原有契约，这些路由回答 501。
type serverSettingsService interface {
	SetServerIdentity(context.Context, admin.SetServerIdentityRequest) (admin.CommandResult, error)
	SetWelcomeMessageTemplates(context.Context, admin.SetWelcomeMessageTemplatesRequest) (admin.CommandResult, error)
	SetLoginCodeMessageTemplate(context.Context, admin.SetLoginCodeMessageTemplateRequest) (admin.CommandResult, error)
	SetSignUpEnabled(context.Context, admin.SetSignUpEnabledRequest) (admin.CommandResult, error)
	SetCodeLoginEnabled(context.Context, admin.SetCodeLoginEnabledRequest) (admin.CommandResult, error)
	UploadServerIcon(context.Context, admin.UploadServerIconRequest) (admin.CommandResult, error)
	RemoveServerIcon(context.Context, admin.RemoveServerIconRequest) (admin.CommandResult, error)
}

// 生产用的 admin.Service 必须提供这些命令，否则后台的服务器设置会在运行时才得到 501。
var _ serverSettingsService = (*admin.Service)(nil)

func (s *Server) registerServerSettingsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/server-settings/set-identity", s.authenticated(s.handleSetServerIdentity))
	mux.HandleFunc("POST /v1/server-settings/set-welcome-message-templates", s.authenticated(s.handleSetWelcomeMessageTemplates))
	mux.HandleFunc("POST /v1/server-settings/set-login-code-message-template", s.authenticated(s.handleSetLoginCodeMessageTemplate))
	mux.HandleFunc("POST /v1/server-settings/set-sign-up-enabled", s.authenticated(s.handleSetSignUpEnabled))
	mux.HandleFunc("POST /v1/server-settings/set-code-login-enabled", s.authenticated(s.handleSetCodeLoginEnabled))
	mux.HandleFunc("POST /v1/server-settings/upload-icon", s.authenticated(s.handleUploadServerIcon))
	mux.HandleFunc("POST /v1/server-settings/remove-icon", s.authenticated(s.handleRemoveServerIcon))
}

func (s *Server) serverSettings(w http.ResponseWriter) (serverSettingsService, bool) {
	svc, ok := s.svc.(serverSettingsService)
	if !ok {
		writeError(w, http.StatusNotImplemented, "server settings are not configured")
	}
	return svc, ok
}

func (s *Server) handleSetServerIdentity(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.serverSettings(w)
	if !ok {
		return
	}
	var req admin.SetServerIdentityRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := svc.SetServerIdentity(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleSetWelcomeMessageTemplates(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.serverSettings(w)
	if !ok {
		return
	}
	var req admin.SetWelcomeMessageTemplatesRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := svc.SetWelcomeMessageTemplates(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleSetLoginCodeMessageTemplate(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.serverSettings(w)
	if !ok {
		return
	}
	var req admin.SetLoginCodeMessageTemplateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := svc.SetLoginCodeMessageTemplate(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleSetSignUpEnabled(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.serverSettings(w)
	if !ok {
		return
	}
	var req admin.SetSignUpEnabledRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := svc.SetSignUpEnabled(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleSetCodeLoginEnabled(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.serverSettings(w)
	if !ok {
		return
	}
	var req admin.SetCodeLoginEnabledRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := svc.SetCodeLoginEnabled(r.Context(), req)
	writeCommandResult(w, result, err)
}

// handleUploadServerIcon 收 multipart/form-data（"metadata" JSON 字段 + "file" 文件），
// 与头像上传相同。不用 JSON+base64：base64 让体积膨胀约 33%，而 decodeJSON 把请求体
// 限制在 1MiB，接近上限的图标会被截断。请求体按 MaxServerIconBytes 加 1MiB 表单余量
// 限制，文件本身按 MaxServerIconBytes 截断检查；扩展名等校验由 Service 负责。
func (s *Server) handleUploadServerIcon(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.serverSettings(w)
	if !ok {
		return
	}
	defer r.Body.Close()
	var fileName string
	var data []byte
	if !decodeFileUpload(w, r, admin.MaxServerIconBytes, "icon file", &fileName, &data) {
		return
	}
	var req admin.UploadServerIconRequest
	if !decodeMultipartMetadata(w, r, &req) {
		return
	}
	req.FileName = fileName
	req.Data = data
	result, err := svc.UploadServerIcon(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleRemoveServerIcon(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.serverSettings(w)
	if !ok {
		return
	}
	var req admin.RemoveServerIconRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := svc.RemoveServerIcon(r.Context(), req)
	writeCommandResult(w, result, err)
}
