package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yixian-huang/imgli/internal/model"
	"github.com/yixian-huang/imgli/internal/service/auth"
)

// AuthHandlers 认证路由。Secure 表示 cookie 是否加 Secure（BaseURL 为 https）。
type AuthHandlers struct {
	Svc    *auth.Service
	Secure bool
}

func userDTO(u *model.User) map[string]any {
	return map[string]any{
		"id": u.ID, "username": u.Username, "email": u.Email,
		"nickname": u.Nickname, "is_admin": u.IsAdmin,
		"email_verified": u.EmailVerifiedAt != nil,
		"public_profile": u.PublicProfile,
		"preferences":    u.Preferences,
		"avatar_url":     avatarURL(u),
		"watermark_set":  u.WatermarkPath != "",
		"used_storage":   u.UsedStorage,
		// 流量字段由 Profile 等在需要时附加；基础 DTO 含原始计数便于调试。
		"bandwidth_used_month": u.BandwidthUsedMonth,
		"bandwidth_period":     u.BandwidthPeriod,
		"created_at":           u.CreatedAt.Format(time.RFC3339),
	}
}

func (h *AuthHandlers) setSession(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: h.Secure,
		MaxAge: int(auth.SessionTTL.Seconds()),
	})
}

func failAuth(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrEmailTaken):
		Fail(w, http.StatusConflict, "email_taken", "邮箱已被注册")
	case errors.Is(err, auth.ErrUsernameTaken):
		Fail(w, http.StatusConflict, "username_taken", "用户名已被占用")
	case errors.Is(err, auth.ErrAccountConflict):
		Fail(w, http.StatusConflict, "account_conflict", "账号信息冲突，请重试")
	case errors.Is(err, auth.ErrWeakPassword):
		Fail(w, http.StatusBadRequest, "weak_password", "密码至少 8 位且包含字母和数字")
	case errors.Is(err, auth.ErrInvalidInput):
		Fail(w, http.StatusBadRequest, CodeInvalidRequest, "用户名或邮箱格式不合法")
	case errors.Is(err, auth.ErrRegistrationClosed):
		Fail(w, http.StatusForbidden, "registration_closed", "注册未开放")
	case errors.Is(err, auth.ErrInviteRequired):
		Fail(w, http.StatusBadRequest, "invite_required", "邀请模式需提供邀请码")
	case errors.Is(err, auth.ErrInviteInvalid):
		Fail(w, http.StatusBadRequest, "invite_invalid", "邀请码无效或已被使用")
	case errors.Is(err, auth.ErrInvalidCredentials):
		Fail(w, http.StatusUnauthorized, "invalid_credentials", "账号或密码错误")
	case errors.Is(err, auth.ErrLoginThrottled):
		retry := time.Minute
		var te *auth.LoginThrottledError
		if errors.As(err, &te) && te.RetryAfter > 0 {
			retry = te.RetryAfter
		}
		secs := int((retry + time.Second - 1) / time.Second)
		if secs < 1 {
			secs = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		Fail(w, http.StatusTooManyRequests, CodeRateLimited, "登录尝试过于频繁，请稍后再试")
	case errors.Is(err, auth.ErrUserBanned):
		Fail(w, http.StatusForbidden, "banned", "账号已被封禁")
	case errors.Is(err, auth.ErrTokenInvalid):
		Fail(w, http.StatusBadRequest, "token_invalid", "链接无效或已过期")
	case errors.Is(err, auth.ErrAlreadyVerified):
		Fail(w, http.StatusBadRequest, "already_verified", "邮箱已验证")
	default:
		Fail(w, http.StatusInternalServerError, CodeInternal, "服务器内部错误")
	}
}

// Register POST /api/v1/auth/register —— 注册成功即登录（原型：注册后直接跳转）。
func (h *AuthHandlers) Register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username, Email, Password string
		InviteCode                string `json:"invite_code"`
		UTMSource                 string `json:"utm_source"`
		UTMMedium                 string `json:"utm_medium"`
		UTMCampaign               string `json:"utm_campaign"`
		RefererHost               string `json:"referer_host"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, CodeInvalidRequest, "请求体不是合法 JSON")
		return
	}
	meta := auth.SignupMeta{
		UTMSource:   req.UTMSource,
		UTMMedium:   req.UTMMedium,
		UTMCampaign: req.UTMCampaign,
		RefererHost: req.RefererHost,
	}
	// Prefer body referer_host; fall back to HTTP Referer header host only.
	if strings.TrimSpace(meta.RefererHost) == "" {
		if ref := r.Header.Get("Referer"); ref != "" {
			meta.RefererHost = ref
		}
	}
	u, err := h.Svc.RegisterWithMeta(req.Username, req.Email, req.Password, req.InviteCode, meta)
	if err != nil {
		failAuth(w, err)
		return
	}
	tok, _, err := h.Svc.Login(u.Email, req.Password, ClientIP(r), r.UserAgent())
	if err != nil {
		failAuth(w, err)
		return
	}
	h.setSession(w, tok)
	OK(w, userDTO(u))
}

// Login POST /api/v1/auth/login
func (h *AuthHandlers) Login(w http.ResponseWriter, r *http.Request) {
	var req struct{ Account, Password string }
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, CodeInvalidRequest, "请求体不是合法 JSON")
		return
	}
	tok, u, err := h.Svc.Login(req.Account, req.Password, ClientIP(r), r.UserAgent())
	if err != nil {
		failAuth(w, err)
		return
	}
	h.setSession(w, tok)
	OK(w, userDTO(u))
}

// Logout POST /api/v1/auth/logout —— 幂等；清 cookie 并删 session。
func (h *AuthHandlers) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookie); err == nil {
		h.Svc.Logout(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: h.Secure, MaxAge: -1,
	})
	OK(w, nil)
}

// Session GET /api/v1/auth/session —— 当前登录用户（RequireUser 保护）。
func (h *AuthHandlers) Session(w http.ResponseWriter, r *http.Request) {
	OK(w, userDTO(PrincipalFrom(r).User))
}

// ForgotPassword POST /api/v1/auth/forgot-password —— 恒 200,防邮箱枚举。
// 解析请求体后异步调用 service,立即返回——存在/不存在邮箱的响应时序完全一致。
func (h *AuthHandlers) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, CodeInvalidRequest, "请求体不是合法 JSON")
		return
	}
	go func() { _ = h.Svc.ForgotPassword(req.Email) }()
	OK(w, map[string]any{})
}

// ResetPassword POST /api/v1/auth/reset-password {token, password}
func (h *AuthHandlers) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token, Password string
	}
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, CodeInvalidRequest, "请求体不是合法 JSON")
		return
	}
	if err := h.Svc.ResetPasswordByToken(req.Token, req.Password); err != nil {
		failAuth(w, err)
		return
	}
	OK(w, map[string]any{})
}

// VerifyEmail POST /api/v1/auth/verify-email {token} —— 公开,核销验证令牌。
func (h *AuthHandlers) VerifyEmail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, CodeInvalidRequest, "请求体不是合法 JSON")
		return
	}
	if err := h.Svc.VerifyEmail(req.Token); err != nil {
		failAuth(w, err)
		return
	}
	OK(w, map[string]any{})
}

// ConfirmChangeEmail POST /api/v1/auth/confirm-change-email {token}
func (h *AuthHandlers) ConfirmChangeEmail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, CodeInvalidRequest, "请求体不是合法 JSON")
		return
	}
	if err := h.Svc.ConfirmChangeEmail(req.Token); err != nil {
		failAuth(w, err)
		return
	}
	OK(w, map[string]any{"ok": true})
}

// ResendVerification POST /api/v1/auth/resend-verification —— 登录态重发验证邮件。
func (h *AuthHandlers) ResendVerification(w http.ResponseWriter, r *http.Request) {
	if err := h.Svc.ResendVerification(PrincipalFrom(r).User.ID); err != nil {
		failAuth(w, err)
		return
	}
	OK(w, map[string]any{})
}

func avatarURL(u *model.User) string {
	if u.AvatarPath == "" {
		return ""
	}
	return fmt.Sprintf("/avatar/%d?v=%d", u.ID, u.UpdatedAt.Unix())
}
