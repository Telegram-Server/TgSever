package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"telesrv/internal/admin"
	"telesrv/internal/domain"
	"telesrv/internal/identity"
	"telesrv/internal/procctl"
)

// serverManage gates the whole Server Settings surface -- see
// permissionServerManage's doc comment in security.go for why this is one
// right rather than split review/manage like other sections.
func (s *server) serverManage(handler http.HandlerFunc) http.Handler {
	return s.requireAuthAPI(s.requirePermission(permissionServerManage, handler))
}

// Server Settings writes fall into two groups.
//
// The identity store (internal/identity: the 777000 name and description, the
// icon, the welcome and login-code templates, the sign-up and code login
// overrides) is written by the telesrv server only. In Docker this process
// runs on a read-only root filesystem with the server_state volume mounted
// read-only, so it could never write there; instead each of the seven identity writes below is handed to the
// server as an admin command over the admin API (callAdminAPI /
// callAdminMultipart, like account freezing), and the server writes its own
// identity.Store. The server's command runner owns the command_id idempotency
// and the admin_commands/admin_audit_logs rows, exactly as for every other
// forwarded command, so nothing is recorded here. This process only reads the
// same directory, to show the current settings.
//
// The .env editor edits this process's own .env and stays local, audited by
// the panel's own runner (runOperatorCommand + serverSettingsCommandFn).

// serverSettingsCommandFn is the runOperatorCommand body of a local Server
// Settings write (the .env editor): a dry run reports dryMessage, a real run
// applies.
func serverSettingsCommandFn(meta admin.CommandMeta, dryMessage, doneMessage string, details map[string]any, apply func() error) func(context.Context, pgx.Tx) (admin.CommandResult, error) {
	return func(context.Context, pgx.Tx) (admin.CommandResult, error) {
		if meta.DryRun {
			return admin.CommandResult{Message: dryMessage, Details: details}, nil
		}
		if err := apply(); err != nil {
			return admin.CommandResult{Details: details}, err
		}
		return admin.CommandResult{Message: doneMessage, Details: details}, nil
	}
}

// --- identity (name/description/icon) ---------------------------------

// serverIdentityAPIResponse extends identity.Info's raw fields with the three
// *effective* fallback templates -- s.cfg's WelcomeMessage{Phone,Email}Default
// and LoginCodeMessageDefault, i.e. this admin process's own reading of the
// TELESRV_WELCOME_MESSAGE_*_TEMPLATE / TELESRV_LOGIN_CODE_MESSAGE_TEMPLATE
// env vars (themselves defaulting to the compiled-in copies), which matches
// what the telesrv server falls back to whenever the panel override is unset,
// as long as both processes share the same .env (see
// uiConfig.WelcomeMessagePhoneDefault's doc comment). The panel needs both:
// the raw override (possibly empty) to know whether a field is "explicitly
// set", and the default text to show as "(using default: ...)" / to restore
// on Reset.
type serverIdentityAPIResponse struct {
	identity.Info
	DefaultWelcomeMessagePhoneTemplate string `json:"default_welcome_message_phone_template"`
	DefaultWelcomeMessageEmailTemplate string `json:"default_welcome_message_email_template"`
	// DefaultLoginCodeMessageTemplate is the effective fallback text for
	// the login-code delivery message (s.cfg.LoginCodeMessageDefault) --
	// same "raw override + effective default" contract as the two fields
	// above, see their doc comment.
	DefaultLoginCodeMessageTemplate string `json:"default_login_code_message_template"`
	// The sign-up switch: identity.Info's raw sign_up_enabled override (absent
	// = unset) plus what it resolves to and where that comes from, so the
	// panel can show "Closed -- set here" vs "Open -- from the environment"
	// without re-implementing domain.ResolveSignUpEnabled. The env default is
	// s.cfg.SignUpEnabledDefault, under the same "both processes share one
	// .env" assumption as the template defaults.
	DefaultSignUpEnabled   bool   `json:"default_sign_up_enabled"`
	SignUpEnabledEffective bool   `json:"sign_up_enabled_effective"`
	SignUpSource           string `json:"sign_up_source"`
	// The code login switch, reported exactly like the sign-up switch above:
	// identity.Info's raw code_login_enabled override plus its resolved state
	// and source (domain.ResolveCodeLoginEnabled over
	// s.cfg.CodeLoginEnabledDefault). PasswordLoginEnabled lets the panel
	// explain up front that the server refuses to turn code login off while
	// password login is disabled (admin.CodeCodeLoginLockout).
	DefaultCodeLoginEnabled   bool   `json:"default_code_login_enabled"`
	CodeLoginEnabledEffective bool   `json:"code_login_enabled_effective"`
	CodeLoginSource           string `json:"code_login_source"`
	PasswordLoginEnabled      bool   `json:"password_login_enabled"`
}

func (s *server) handleServerIdentityAPI(w http.ResponseWriter, r *http.Request) {
	info, err := s.identity.Get()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, serverIdentityAPIResponse{
		Info:                               info,
		DefaultWelcomeMessagePhoneTemplate: s.cfg.WelcomeMessagePhoneDefault,
		DefaultWelcomeMessageEmailTemplate: s.cfg.WelcomeMessageEmailDefault,
		DefaultLoginCodeMessageTemplate:    s.cfg.LoginCodeMessageDefault,
		DefaultSignUpEnabled:               s.cfg.SignUpEnabledDefault,
		SignUpEnabledEffective:             domain.ResolveSignUpEnabled(info.SignUpEnabled, s.cfg.SignUpEnabledDefault),
		SignUpSource:                       admin.SignUpSource(info.SignUpEnabled),
		DefaultCodeLoginEnabled:            s.cfg.CodeLoginEnabledDefault,
		CodeLoginEnabledEffective:          domain.ResolveCodeLoginEnabled(info.CodeLoginEnabled, s.cfg.CodeLoginEnabledDefault),
		CodeLoginSource:                    admin.SignUpSource(info.CodeLoginEnabled),
		PasswordLoginEnabled:               s.cfg.PasswordLoginEnabled,
	})
}

// --- sign-up switch -------------------------------------------------------

type setSignUpEnabledAPIRequest struct {
	CommandID string `json:"command_id"`
	Reason    string `json:"reason"`
	Confirm   bool   `json:"confirm"`
	// Mode is "on", "off" or "follow_env". A string rather than a nullable
	// bool so that a body that simply forgets the field is rejected instead
	// of silently clearing the override.
	Mode string `json:"mode"`
}

// handleSetSignUpEnabledAPI sets or clears the sign-up override in
// identity.json, through the server (admin.Service.SetSignUpEnabled). telesrv
// re-reads it on every sign-up decision (see auth.IdentitySignUpAllowed), so
// the change -- including the telesrv_sign_up_disabled app-config key and its
// hash -- takes effect without a restart.
//
// An unknown mode is refused here with 422 before anything is forwarded; the
// server applies the same check (admin.SignUpOverrideForMode).
func (s *server) handleSetSignUpEnabledAPI(w http.ResponseWriter, r *http.Request) {
	var body setSignUpEnabledAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	mode := strings.TrimSpace(body.Mode)
	if _, ok := admin.SignUpOverrideForMode(mode); !ok {
		writeAPIError(w, http.StatusUnprocessableEntity, `mode must be "on", "off" or "follow_env"`)
		return
	}
	req := admin.SetSignUpEnabledRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "set-sign-up-enabled"),
		Mode:        mode,
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/server-settings/set-sign-up-enabled", req)
	writeCommandResultAPI(w, result, err)
}

// --- code login switch ----------------------------------------------------

type setCodeLoginEnabledAPIRequest struct {
	CommandID string `json:"command_id"`
	Reason    string `json:"reason"`
	Confirm   bool   `json:"confirm"`
	// Mode is "on", "off" or "follow_env", as for the sign-up switch.
	Mode string `json:"mode"`
}

// handleSetCodeLoginEnabledAPI sets or clears the code login override in
// identity.json, through the server (admin.Service.SetCodeLoginEnabled).
// telesrv re-reads it on every code login decision (see
// auth.IdentityCodeLoginAllowed), so the change -- including the
// telesrv_code_login_disabled app-config key and its hash -- takes effect
// without a restart.
//
// An unknown mode is refused here with 422 before anything is forwarded. The
// lock-out rule (no turning code login off while password login is disabled)
// is the server's alone: it knows its own TELESRV_PASSWORD_LOGIN_ENABLED, and
// its CODE_LOGIN_LOCKOUT refusal is passed through unchanged.
func (s *server) handleSetCodeLoginEnabledAPI(w http.ResponseWriter, r *http.Request) {
	var body setCodeLoginEnabledAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	mode := strings.TrimSpace(body.Mode)
	if _, ok := admin.CodeLoginOverrideForMode(mode); !ok {
		writeAPIError(w, http.StatusUnprocessableEntity, `mode must be "on", "off" or "follow_env"`)
		return
	}
	req := admin.SetCodeLoginEnabledRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "set-code-login-enabled"),
		Mode:        mode,
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/server-settings/set-code-login-enabled", req)
	writeCommandResultAPI(w, result, err)
}

// handleServerIconAPI serves the icon's raw bytes for the panel's own
// preview. There is no public server-icon route in telesrv -- the icon is
// purely a panel concept (unlike owpengram's /owpengram/server-info), so the
// bytes never leave the admin session.
func (s *server) handleServerIconAPI(w http.ResponseWriter, r *http.Request) {
	data, ext, ok := s.identity.Icon()
	if !ok {
		writeAPIError(w, http.StatusNotFound, "no icon configured")
		return
	}
	contentType := map[string]string{
		".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
		".webp": "image/webp", ".gif": "image/gif",
	}[ext]
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// handlePublicServerIconAPI serves the brand mark to the unauthenticated login
// screen. See the route comment in server.go for why this one is public and
// what it is allowed to disclose. It shares the Content-Type table with the
// gated variant so the login page gets the same bytes either way.
func (s *server) handlePublicServerIconAPI(w http.ResponseWriter, _ *http.Request) {
	data, ext, ok := s.identity.Icon()
	if !ok {
		writeAPIError(w, http.StatusNotFound, "no icon configured")
		return
	}
	contentType := map[string]string{
		".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
		".webp": "image/webp", ".gif": "image/gif",
	}[ext]
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

type setServerIdentityAPIRequest struct {
	CommandID   string `json:"command_id"`
	Reason      string `json:"reason"`
	Confirm     bool   `json:"confirm"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (s *server) handleSetServerIdentityAPI(w http.ResponseWriter, r *http.Request) {
	var body setServerIdentityAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.SetServerIdentityRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "set-server-identity"),
		Name:        body.Name,
		Description: body.Description,
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/server-settings/set-identity", req)
	writeCommandResultAPI(w, result, err)
}

// --- login-notification templates ---------------------------------------

type setWelcomeMessageTemplatesAPIRequest struct {
	CommandID     string `json:"command_id"`
	Reason        string `json:"reason"`
	Confirm       bool   `json:"confirm"`
	PhoneTemplate string `json:"phone_template"`
	EmailTemplate string `json:"email_template"`
}

// handleSetWelcomeMessageTemplatesAPI sets (or, with an empty string,
// clears) the admin-panel override for the 777000 login-notification
// message's phone/email template -- see identity.Store.SetWelcomeMessageTemplates
// and domain.ResolveWelcomeMessageTemplate. Deliberately a separate endpoint
// from set-server-identity: brand identity (name/description/icon) and
// login-notification copy are different concerns that happen to share the
// same on-disk identity.json, and keeping them as separate actions/buttons
// means editing one never risks silently blanking the other.
func (s *server) handleSetWelcomeMessageTemplatesAPI(w http.ResponseWriter, r *http.Request) {
	var body setWelcomeMessageTemplatesAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.SetWelcomeMessageTemplatesRequest{
		CommandMeta:   s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "set-welcome-message-templates"),
		PhoneTemplate: body.PhoneTemplate,
		EmailTemplate: body.EmailTemplate,
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/server-settings/set-welcome-message-templates", req)
	writeCommandResultAPI(w, result, err)
}

// --- login-code delivery message template --------------------------------

type setLoginCodeMessageTemplateAPIRequest struct {
	CommandID string `json:"command_id"`
	Reason    string `json:"reason"`
	Confirm   bool   `json:"confirm"`
	Template  string `json:"template"`
}

// handleSetLoginCodeMessageTemplateAPI sets (or, with an empty string,
// clears) the admin-panel override for the 777000 login-code delivery
// message -- see identity.Store.SetLoginCodeMessageTemplate and
// domain.ResolveLoginCodeMessageTemplate. A dedicated endpoint (not folded
// into set-welcome-message-templates): this message embeds the actual OTP
// code via the {{code}} placeholder, so a save here carries an extra,
// security-relevant validation the login-notification templates don't
// need -- a template missing {{code}} (or containing it more than once)
// would either silently drop the code from the message or leave it
// ambiguous which occurrence carries it, so it is rejected outright with a
// 422 here, before anything is forwarded (the server repeats the check in
// admin.Service.SetLoginCodeMessageTemplate). Clearing the override (empty
// string) is exempt -- it always resolves to a valid built-in/env default.
func (s *server) handleSetLoginCodeMessageTemplateAPI(w http.ResponseWriter, r *http.Request) {
	var body setLoginCodeMessageTemplateAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	if t := strings.TrimSpace(body.Template); t != "" {
		if err := domain.ValidateLoginCodeMessageTemplate(t); err != nil {
			writeAPIError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	}
	req := admin.SetLoginCodeMessageTemplateRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "set-login-code-message-template"),
		Template:    body.Template,
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/server-settings/set-login-code-message-template", req)
	writeCommandResultAPI(w, result, err)
}

// --- server icon upload/remove -------------------------------------------

type uploadServerIconAPIRequest struct {
	CommandID string `json:"command_id"`
	Reason    string `json:"reason"`
	Confirm   bool   `json:"confirm"`
}

// handleUploadServerIconAPI takes multipart/form-data (a "metadata" JSON
// field + a "file" field), the same shape handleSetAccountAvatarAPI uses --
// deliberately not JSON+base64 like the other Server Settings actions:
// base64 inflates a file ~33%, and decodeAction's plain io.LimitReader caps
// the request body at 1MiB regardless of admin.MaxServerIconBytes, so a real
// multi-hundred-KB icon would fail decoding ("unexpected EOF" from the
// truncated body) before this handler ever saw it. Multipart sidesteps that
// entirely -- the size cap below is enforced on the actual file bytes. The
// file is forwarded to the server the same way (callAdminMultipart to
// /v1/server-settings/upload-icon, capped at admin.MaxServerIconBytes there
// too); the extension and size checks here only keep an obviously bad upload
// from making the round trip.
func (s *server) handleUploadServerIconAPI(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, admin.MaxServerIconBytes+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid multipart form: "+err.Error())
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	var body uploadServerIconAPIRequest
	dec := json.NewDecoder(strings.NewReader(r.FormValue("metadata")))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid metadata: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "icon file is required")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, admin.MaxServerIconBytes+1))
	if err != nil || len(data) == 0 || len(data) > admin.MaxServerIconBytes {
		writeAPIError(w, http.StatusBadRequest, "icon file is empty or too large (max 2MiB)")
		return
	}
	if _, err := admin.ServerIconExt(header.Filename, data); err != nil {
		writeAPIError(w, http.StatusBadRequest, "unsupported icon extension")
		return
	}
	req := admin.UploadServerIconRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "upload-server-icon"),
		FileName:    header.Filename,
	}
	result, err := s.callAdminMultipart(r.Context(), "/v1/server-settings/upload-icon", req, header.Filename, data)
	writeCommandResultAPI(w, result, err)
}

type removeServerIconAPIRequest struct {
	CommandID string `json:"command_id"`
	Reason    string `json:"reason"`
	Confirm   bool   `json:"confirm"`
}

func (s *server) handleRemoveServerIconAPI(w http.ResponseWriter, r *http.Request) {
	var body removeServerIconAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.RemoveServerIconRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "remove-server-icon"),
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/server-settings/remove-icon", req)
	writeCommandResultAPI(w, result, err)
}

// --- .env editing --------------------------------------------------------

func (s *server) handleServerEnvAPI(w http.ResponseWriter, r *http.Request) {
	groups, err := s.envCtl.ReadEnvGroups()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// A missing/ungrouped .env.example yields a nil slice; the panel expects a
	// JSON array, so never serialize it as null.
	if groups == nil {
		groups = []procctl.EnvGroup{}
	}
	writeJSON(w, http.StatusOK, groups)
}

type updateServerEnvAPIRequest struct {
	CommandID string            `json:"command_id"`
	Reason    string            `json:"reason"`
	Confirm   bool              `json:"confirm"`
	Values    map[string]string `json:"values"`
}

// handleUpdateServerEnvAPI writes only the keys handed over (preserving every
// untouched .env value), then tells the operator the server needs a restart
// to pick them up -- telesrv loads its config once at startup.
func (s *server) handleUpdateServerEnvAPI(w http.ResponseWriter, r *http.Request) {
	var body updateServerEnvAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	meta := s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "update-server-env")
	// .env holds secrets (DSNs, tokens, passwords), so the trail records which
	// keys changed and never their values; the values only feed the command_id
	// fingerprint, through envValuesFingerprint's keyed MAC.
	keys := make([]string, 0, len(body.Values))
	for key := range body.Values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	details := map[string]any{"keys_changed": len(body.Values), "keys": keys}
	const (
		action      = "server.update_env"
		dryMessage  = "would update .env -- takes effect on telesrv restart"
		doneMessage = ".env updated -- restart telesrv for changes to take effect"
	)
	apply := func() error { return s.envCtl.WriteEnvValues(body.Values) }
	result, err := s.runOperatorCommand(r.Context(), meta, action, map[string]any{"keys": keys}, envValuesFingerprint(s.cfg.SessionKey, keys, body.Values),
		serverSettingsCommandFn(meta, dryMessage, doneMessage, details, apply))
	var unavailable *operatorStoreUnavailableError
	if errors.As(err, &unavailable) {
		// The .env editor is the repair path for a broken database setting
		// (TELESRV_POSTGRES_DSN), and the break-glass login works without the
		// database for exactly that. Refusing the write because the audit store
		// is down would leave no way out, so write without the trail, say so in
		// the log and in the result, and keep every other Server Settings write
		// strictly audited.
		result, err = s.updateServerEnvUnaudited(meta, action, keys, details, dryMessage, doneMessage, apply, unavailable)
	}
	writeCommandResultAPI(w, result, err)
}

// updateServerEnvUnaudited is handleUpdateServerEnvAPI's fallback when the
// command store cannot be reached. It applies the same validation as an
// audited command, but there is no command row, so command_id is not
// idempotent here and the result carries audit_recorded=false.
func (s *server) updateServerEnvUnaudited(meta admin.CommandMeta, action string, keys []string, details map[string]any, dryMessage, doneMessage string, apply func() error, cause error) (admin.CommandResult, error) {
	meta, err := normalizeOperatorCommandMeta(meta)
	if err != nil {
		return admin.CommandResult{}, err
	}
	details["audit_recorded"] = false
	result := admin.CommandResult{CommandID: meta.CommandID, Action: action, DryRun: meta.DryRun, Status: "completed", Details: details}
	if meta.DryRun {
		result.Message = dryMessage + " (audit store unavailable: this change will not be audited)"
		return result, nil
	}
	if err := apply(); err != nil {
		result.Status = "failed"
		return result, err
	}
	log.Printf("WARNING: %s by %q applied WITHOUT an audit record (command_id=%s, keys=%v, reason=%q): audit store unavailable: %v",
		action, meta.Actor, meta.CommandID, keys, meta.Reason, cause)
	result.Message = doneMessage + " (audit store unavailable: not audited)"
	return result, nil
}

// envValuesFingerprint binds the submitted .env values into command_id's
// identity without making the stored fingerprint an offline oracle for them.
// A plain SHA-256 of key=value could be brute-forced by anyone who can read
// admin_commands (a short password or token falls quickly), so the digest is
// an HMAC keyed by a key derived from the panel session key
// (TELESRV_ADMIN_SESSION_KEY): a secret the panel already requires, that never
// reaches the database, and that is stable across restarts, so replaying a
// command_id after a restart still returns the first run. Rotating the session
// key changes the MAC, and a replay after that reports COMMAND_ID_CONFLICT
// instead of already-executed -- the safe direction.
func envValuesFingerprint(sessionKey []byte, sortedKeys []string, values map[string]string) string {
	derive := hmac.New(sha256.New, sessionKey)
	_, _ = derive.Write([]byte("telesrv-admin/update-server-env/fingerprint"))
	mac := hmac.New(sha256.New, derive.Sum(nil))
	for _, key := range sortedKeys {
		_, _ = mac.Write([]byte(key + "=" + values[key] + "\x00"))
	}
	return hex.EncodeToString(mac.Sum(nil))
}

// --- status --------------------------------------------------------------

// serverStatusAPIResponse is the Services tab's read-only view: how this
// machine's telesrv deployment is doing right now. Unlike owpengram's restart
// machinery there is no process control here -- gramsrv ships restart/update
// as deploy scripts -- so the page reports reachability of the pieces the
// server depends on: the database, the optional ephemeral Redis store, and
// the MTProto listener itself.
type serverStatusAPIResponse struct {
	Host serverHostInfo `json:"host"`
	// Postgres is the store every feature reads and writes through.
	Postgres serviceHealth `json:"postgres"`
	// Redis is optional in telesrv: when TELESRV_REDIS_ADDR is empty the
	// server runs without it and the panel says so instead of reporting a
	// false alarm.
	Redis serviceHealth `json:"redis"`
	// MTProto is the core server's TCP listener (config.ListenAddr), probed
	// from 127.0.0.1.
	MTProto serviceHealth `json:"mtproto"`
	// Docker is the live Compose container list, best-effort -- see
	// dockerStatusBestEffort.
	Docker serverDockerStatus `json:"docker"`
}

type serverHostInfo struct {
	Hostname string `json:"hostname"`
	// Distro is the human-readable OS name ("Ubuntu 24.04.1 LTS"), which is
	// what an operator wants on the card; Hostname stays for diagnostics.
	Distro    string `json:"distro"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	GoVersion string `json:"go_version"`
}

type serviceHealth struct {
	// Configured reports whether this deployment should expect the service
	// at all. An unconfigured service is reported separately from a
	// configured-but-down one.
	Configured bool   `json:"configured"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
}

func (s *server) handleServerStatusAPI(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	hostname, _ := os.Hostname()
	status := serverStatusAPIResponse{
		Host: serverHostInfo{
			Hostname:  hostname,
			Distro:    hostDistro(),
			OS:        runtime.GOOS,
			Arch:      runtime.GOARCH,
			GoVersion: runtime.Version(),
		},
		Postgres: s.probe(ctx, statusProbe(s.read.Ping)),
		Redis:    s.probe(ctx, s.probeRedis),
		MTProto:  s.probe(ctx, s.probeMTProto),
		Docker:   dockerStatusBestEffort(ctx, s.cfg.RepoRoot),
	}
	writeJSON(w, http.StatusOK, status)
}

// hostDistro is the display name of the host OS, preferring /etc/os-release's
// PRETTY_NAME on Linux and falling back to the bare GOOS elsewhere (Windows and
// macOS dev hosts, where os-release does not exist).
func hostDistro() string {
	if runtime.GOOS != "linux" {
		return runtime.GOOS
	}
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "Linux"
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key != "PRETTY_NAME" {
			continue
		}
		if value = strings.Trim(strings.TrimSpace(value), `"`); value != "" {
			return value
		}
	}
	return "Linux"
}

// probe wraps one connectivity check into a serviceHealth record.
func (s *server) probe(ctx context.Context, fn func(context.Context) error) serviceHealth {
	health := serviceHealth{Configured: true}
	err := fn(ctx)
	if err == nil {
		health.OK = true
		return health
	}
	if errors.Is(err, errServiceUnconfigured) {
		health.Configured = false
		return health
	}
	health.Error = err.Error()
	return health
}

// statusProbe adapts a plain func to the same signature the other probes
// have, so all three collapse onto one code path.
func statusProbe(fn func(context.Context) error) func(context.Context) error { return fn }

func (s *server) probeRedis(ctx context.Context) error {
	addr := strings.TrimSpace(s.cfg.RedisAddr)
	if addr == "" {
		return errServiceUnconfigured
	}
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: s.cfg.RedisPassword,
		DB:       s.cfg.RedisDB,
	})
	defer client.Close()
	return client.Ping(ctx).Err()
}

func (s *server) probeMTProto(ctx context.Context) error {
	if s.cfg.ServerPort <= 0 {
		return errServiceUnconfigured
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(s.cfg.ServerPort)))
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

var errServiceUnconfigured = errors.New("not configured on this deployment")
