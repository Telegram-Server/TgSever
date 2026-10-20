package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"telesrv/internal/domain"
)

// 服务器设置（Server Settings → Server identity / 登录通知模板 / 注册开关）的写入命令。
//
// identity 存储（internal/identity，目录为 Config.IdentityDir）只由主服务器写：
// Docker 部署下管理后台的根文件系统是只读的，server_state 卷也以只读方式挂给后台，
// 后台进程根本写不进去。所以后台把这 6 种写操作作为管理命令经 admin API 交给主服务器，
// 由这里写入服务端自己的 identity.Store；后台只读同一目录用来展示当前设置。
//
// 与其他管理命令一样走 runCommand：带 command_id、理由、试运行，按 command_id 幂等，
// 写入 admin_commands 并由 FinishCommand 追加 admin_audit_logs。参数校验在 runCommand
// 之前完成（与 SetAccountFrozen、SetAccountAvatar 一致），不合法的请求不产生命令记录。
// 文件写入发生在命令记录之后、完成之前；写入失败时命令记为失败并把错误带回调用方。

const (
	ActionSetServerIdentity           = "server.set_identity"
	ActionSetWelcomeMessageTemplates  = "server.set_welcome_message_templates"
	ActionSetLoginCodeMessageTemplate = "server.set_login_code_message_template"
	ActionSetSignUpEnabled            = "server.set_sign_up_enabled"
	ActionUploadServerIcon            = "server.upload_icon"
	ActionRemoveServerIcon            = "server.remove_icon"
)

// 服务器设置命令的稳定错误码，供后台与调用方按码区分。
const (
	CodeServerIdentityUnavailable = "SERVER_IDENTITY_UNAVAILABLE"
	CodeLoginCodeTemplateInvalid  = "LOGIN_CODE_TEMPLATE_INVALID"
	CodeSignUpModeInvalid         = "SIGN_UP_MODE_INVALID"
	CodeServerIconInvalid         = "SERVER_ICON_INVALID"
)

// MaxServerIconBytes 是服务器图标的大小上限。admin API 的 multipart 上传按它加 1MiB
// 的表单余量限制请求体。
const MaxServerIconBytes = 2 << 20 // 2 MiB

var serverIconExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true,
}

// ServerIdentityStore 是命令写入的那一半 identity.Store，*identity.Store 直接满足。
type ServerIdentityStore interface {
	SetText(name, description string) error
	SetWelcomeMessageTemplates(phone, email string) error
	SetLoginCodeMessageTemplate(template string) error
	SetSignUpOverride(enabled *bool) error
	SetIcon(data []byte, ext string) error
	RemoveIcon() error
}

type SetServerIdentityRequest struct {
	CommandMeta
	Name        string `json:"name"`
	Description string `json:"description"`
}

type SetWelcomeMessageTemplatesRequest struct {
	CommandMeta
	PhoneTemplate string `json:"phone_template"`
	EmailTemplate string `json:"email_template"`
}

type SetLoginCodeMessageTemplateRequest struct {
	CommandMeta
	Template string `json:"template"`
}

// SetSignUpEnabledRequest 的 Mode 取 "on"、"off" 或 "follow_env"。用字符串而不是可空
// 布尔值，是为了让漏掉这个字段的请求被拒绝，而不是悄悄清除覆盖。
type SetSignUpEnabledRequest struct {
	CommandMeta
	Mode string `json:"mode"`
}

// UploadServerIconRequest 经 multipart 上传（metadata JSON + file），与
// SetAccountAvatarRequest 相同：图标字节不进 JSON，命令记录里只留文件名与内容摘要，
// 摘要由服务端计算，所以同一 command_id 换一张图会冲突而不是被当成重放。
type UploadServerIconRequest struct {
	CommandMeta
	FileName      string `json:"file_name"`
	ContentSHA256 string `json:"content_sha256"`
	Data          []byte `json:"-"`
}

type RemoveServerIconRequest struct {
	CommandMeta
}

// SignUpOverrideForMode 把注册开关的 mode 映射为 identity 的三态覆盖
// （nil = 跟随 TELESRV_SIGN_UP_ENABLED）。ok=false 表示 mode 不认识。
func SignUpOverrideForMode(mode string) (override *bool, ok bool) {
	switch strings.TrimSpace(mode) {
	case "on":
		v := true
		return &v, true
	case "off":
		v := false
		return &v, true
	case "follow_env":
		return nil, true
	}
	return nil, false
}

// 注册开关状态的来源：未设置覆盖时跟随环境变量，否则以面板覆盖为准。
const (
	SignUpSourceEnv      = "env"
	SignUpSourceOverride = "override"
)

// SignUpSource 返回给定覆盖值下注册开关状态的来源。
func SignUpSource(override *bool) string {
	if override != nil {
		return SignUpSourceOverride
	}
	return SignUpSourceEnv
}

// ServerIconExt 校验图标上传并返回小写扩展名（带前导点）：扩展名必须是 png/jpg/jpeg/
// webp/gif 之一，内容非空且不超过 MaxServerIconBytes。
func ServerIconExt(fileName string, data []byte) (string, error) {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(fileName)))
	if !serverIconExts[ext] {
		return "", codedError(CodeServerIconInvalid, fmt.Errorf("unsupported icon extension %q", ext))
	}
	if len(data) == 0 || len(data) > MaxServerIconBytes {
		return "", codedError(CodeServerIconInvalid, fmt.Errorf("icon file is empty or too large (max 2MiB)"))
	}
	return ext, nil
}

func (s *Service) serverIdentityStore() (ServerIdentityStore, error) {
	if s == nil || s.serverIdentity == nil {
		return nil, codedError(CodeServerIdentityUnavailable, fmt.Errorf("server identity store is not configured"))
	}
	return s.serverIdentity, nil
}

// runServerIdentityCommand 是 6 种写操作共用的命令体：试运行只返回 dryMessage，
// 正式执行调用 apply 写入 identity 存储。
//
// 文件写入无法回滚，所以命令记录不跟随请求取消：后台的请求在写入之后被掐断时，
// FinishCommand 仍要把命令记完并追加审计，否则改动已生效，命令却停在 running、没有审计。
func (s *Service) runServerIdentityCommand(ctx context.Context, meta CommandMeta, action string, request any, details map[string]any, dryMessage, doneMessage string, apply func() error) (CommandResult, error) {
	return s.runCommand(context.WithoutCancel(ctx), meta, action, 0, domain.Peer{}, request, func() (CommandResult, error) {
		if meta.DryRun {
			return CommandResult{Message: dryMessage, Details: details}, nil
		}
		if err := apply(); err != nil {
			return CommandResult{Details: details}, err
		}
		return CommandResult{Message: doneMessage, Details: details}, nil
	})
}

// SetServerIdentity 更新 777000 的名称与简介。systemidentity.Watcher 轮询同一目录，
// 名称变化后无需重启即同步到官方系统账号。
func (s *Service) SetServerIdentity(ctx context.Context, req SetServerIdentityRequest) (CommandResult, error) {
	store, err := s.serverIdentityStore()
	if err != nil {
		return CommandResult{}, err
	}
	details := map[string]any{"name": req.Name, "description": req.Description}
	return s.runServerIdentityCommand(ctx, req.CommandMeta, ActionSetServerIdentity, req, details,
		"server identity validated", "server identity updated",
		func() error { return store.SetText(req.Name, req.Description) })
}

// SetWelcomeMessageTemplates 设置（空串即清除）登录成功后欢迎消息的手机号/邮箱模板覆盖。
// 模板正文可能很长，审计详情只记录每个渠道是否设置；完整请求仍在命令记录里。
func (s *Service) SetWelcomeMessageTemplates(ctx context.Context, req SetWelcomeMessageTemplatesRequest) (CommandResult, error) {
	store, err := s.serverIdentityStore()
	if err != nil {
		return CommandResult{}, err
	}
	details := map[string]any{
		"phone_template_set": strings.TrimSpace(req.PhoneTemplate) != "",
		"email_template_set": strings.TrimSpace(req.EmailTemplate) != "",
	}
	return s.runServerIdentityCommand(ctx, req.CommandMeta, ActionSetWelcomeMessageTemplates, req, details,
		"login-notification templates validated", "login-notification templates updated",
		func() error { return store.SetWelcomeMessageTemplates(req.PhoneTemplate, req.EmailTemplate) })
}

// SetLoginCodeMessageTemplate 设置（空串即清除）登录码投递消息的模板覆盖。非空模板必须
// 恰好包含一次 {{code}}（domain.ValidateLoginCodeMessageTemplate），否则消息里要么没有
// 验证码、要么分不清哪一处是验证码；清除覆盖总是回退到合法的默认文案，不需要校验。
func (s *Service) SetLoginCodeMessageTemplate(ctx context.Context, req SetLoginCodeMessageTemplateRequest) (CommandResult, error) {
	if t := strings.TrimSpace(req.Template); t != "" {
		if err := domain.ValidateLoginCodeMessageTemplate(t); err != nil {
			return CommandResult{}, codedError(CodeLoginCodeTemplateInvalid, err)
		}
	}
	store, err := s.serverIdentityStore()
	if err != nil {
		return CommandResult{}, err
	}
	details := map[string]any{"template_set": strings.TrimSpace(req.Template) != ""}
	return s.runServerIdentityCommand(ctx, req.CommandMeta, ActionSetLoginCodeMessageTemplate, req, details,
		"login-code message template validated", "login-code message template updated",
		func() error { return store.SetLoginCodeMessageTemplate(req.Template) })
}

// SetSignUpEnabled 设置或清除注册开关的覆盖。主服务器每次判断是否允许注册都现读
// identity.json（auth.IdentitySignUpAllowed），因此无需重启即生效。effective 按服务端
// 自己的 TELESRV_SIGN_UP_ENABLED 计算，也就是服务端实际执行的结果。
func (s *Service) SetSignUpEnabled(ctx context.Context, req SetSignUpEnabledRequest) (CommandResult, error) {
	mode := strings.TrimSpace(req.Mode)
	override, ok := SignUpOverrideForMode(mode)
	if !ok {
		return CommandResult{}, codedError(CodeSignUpModeInvalid, fmt.Errorf(`mode must be "on", "off" or "follow_env"`))
	}
	store, err := s.serverIdentityStore()
	if err != nil {
		return CommandResult{}, err
	}
	// 没接上环境默认值时不猜：猜成 false 会在 follow_env 时报告「已关闭」，而服务端实际
	// 可能是开放的。
	if s.signUpEnabledDefault == nil {
		return CommandResult{}, codedError(CodeServerIdentityUnavailable, fmt.Errorf("sign-up environment default is not configured"))
	}
	req.Mode = mode
	effective := domain.ResolveSignUpEnabled(override, s.signUpEnabledDefault())
	details := map[string]any{
		"mode":                      mode,
		"sign_up_enabled_effective": effective,
		"sign_up_source":            SignUpSource(override),
	}
	state := "closed"
	if effective {
		state = "open"
	}
	return s.runServerIdentityCommand(ctx, req.CommandMeta, ActionSetSignUpEnabled, req, details,
		fmt.Sprintf("Would set sign-up to %s (effective: %s).", mode, state),
		fmt.Sprintf("Sign-up set to %s (effective: %s).", mode, state),
		func() error { return store.SetSignUpOverride(override) })
}

// UploadServerIcon 替换服务器图标。图标同时是 777000 的头像来源，由
// systemidentity.Watcher 按图标指纹的变化同步。
func (s *Service) UploadServerIcon(ctx context.Context, req UploadServerIconRequest) (CommandResult, error) {
	ext, err := ServerIconExt(req.FileName, req.Data)
	if err != nil {
		return CommandResult{}, err
	}
	store, err := s.serverIdentityStore()
	if err != nil {
		return CommandResult{}, err
	}
	sum := sha256.Sum256(req.Data)
	req.ContentSHA256 = hex.EncodeToString(sum[:])
	data := req.Data
	details := map[string]any{"bytes": len(data), "ext": ext, "sha256": req.ContentSHA256}
	return s.runServerIdentityCommand(ctx, req.CommandMeta, ActionUploadServerIcon, req, details,
		"server icon validated", "server icon updated",
		func() error { return store.SetIcon(data, ext) })
}

// RemoveServerIcon 删除服务器图标（没有图标时什么也不做）；777000 回到内置默认头像。
func (s *Service) RemoveServerIcon(ctx context.Context, req RemoveServerIconRequest) (CommandResult, error) {
	store, err := s.serverIdentityStore()
	if err != nil {
		return CommandResult{}, err
	}
	return s.runServerIdentityCommand(ctx, req.CommandMeta, ActionRemoveServerIcon, req, nil,
		"server icon removal validated", "server icon removed", store.RemoveIcon)
}
