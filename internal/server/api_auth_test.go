package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yixian-huang/imgli/internal/config"
	"github.com/yixian-huang/imgli/internal/model"
	"github.com/yixian-huang/imgli/internal/service/settings"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	cfg, _ := config.Load("")
	return New(Options{Cfg: cfg, DB: model.TestDB(t)})
}

type env struct {
	Status  bool            `json:"status"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func doJSON(t *testing.T, s *Server, method, path, body string, cookies []*http.Cookie) (*httptest.ResponseRecorder, env) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var e env
	json.Unmarshal(rec.Body.Bytes(), &e)
	return rec, e
}

func code(t *testing.T, e env) string {
	t.Helper()
	var d struct {
		Code string `json:"code"`
	}
	json.Unmarshal(e.Data, &d)
	return d.Code
}

func TestRegisterLoginSessionFlow(t *testing.T) {
	s := newTestServer(t)

	// 注册即登录：拿到 Set-Cookie
	rec, e := doJSON(t, s, "POST", "/api/v1/auth/register",
		`{"username":"alice","email":"alice@img.li","password":"passw0rd"}`, nil)
	if rec.Code != http.StatusOK || !e.Status {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	var sess *http.Cookie
	for _, c := range cookies {
		if c.Name == "imgli_session" {
			sess = c
		}
	}
	if sess == nil || !sess.HttpOnly || sess.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie 缺失或属性不对: %+v", sess)
	}

	// 会话查询
	rec, e = doJSON(t, s, "GET", "/api/v1/auth/session", "", []*http.Cookie{sess})
	if rec.Code != http.StatusOK {
		t.Fatalf("session: %d", rec.Code)
	}
	var u struct {
		Username string `json:"username"`
		IsAdmin  bool   `json:"is_admin"`
	}
	json.Unmarshal(e.Data, &u)
	if u.Username != "alice" || !u.IsAdmin {
		t.Errorf("首用户应为 admin: %+v", u)
	}

	// 未登录访问受保护路由
	rec, e = doJSON(t, s, "GET", "/api/v1/auth/session", "", nil)
	if rec.Code != http.StatusUnauthorized || code(t, e) != "unauthorized" {
		t.Errorf("匿名 session: %d %s", rec.Code, rec.Body.String())
	}

	// 登出后 session 失效
	doJSON(t, s, "POST", "/api/v1/auth/logout", "", []*http.Cookie{sess})
	rec, _ = doJSON(t, s, "GET", "/api/v1/auth/session", "", []*http.Cookie{sess})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("登出后应 401, got %d", rec.Code)
	}
}

func TestAuthErrorCodes(t *testing.T) {
	s := newTestServer(t)
	doJSON(t, s, "POST", "/api/v1/auth/register",
		`{"username":"alice","email":"alice@img.li","password":"passw0rd"}`, nil)

	rec, e := doJSON(t, s, "POST", "/api/v1/auth/register",
		`{"username":"bob","email":"alice@img.li","password":"passw0rd"}`, nil)
	if rec.Code != http.StatusConflict || code(t, e) != "email_taken" {
		t.Errorf("email_taken: %d %s", rec.Code, rec.Body.String())
	}
	rec, e = doJSON(t, s, "POST", "/api/v1/auth/register",
		`{"username":"carl","email":"carl@img.li","password":"short"}`, nil)
	if rec.Code != http.StatusBadRequest || code(t, e) != "weak_password" {
		t.Errorf("weak_password: %d %s", rec.Code, rec.Body.String())
	}
	rec, e = doJSON(t, s, "POST", "/api/v1/auth/login",
		`{"account":"alice","password":"wrong-pw"}`, nil)
	if rec.Code != http.StatusUnauthorized || code(t, e) != "invalid_credentials" {
		t.Errorf("invalid_credentials: %d %s", rec.Code, rec.Body.String())
	}
	rec, _ = doJSON(t, s, "POST", "/api/v1/auth/login", `{bad json`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("坏 JSON 应 400, got %d", rec.Code)
	}
}

func TestLoginThrottleReturns429(t *testing.T) {
	s := newTestServer(t)
	doJSON(t, s, "POST", "/api/v1/auth/register",
		`{"username":"alice","email":"alice@example.test","password":"passw0rd"}`, nil)

	for i := 0; i < 4; i++ {
		rec, e := doJSON(t, s, "POST", "/api/v1/auth/login",
			`{"account":"alice","password":"wrong-pw"}`, nil)
		if rec.Code != http.StatusUnauthorized || code(t, e) != "invalid_credentials" {
			t.Fatalf("第 %d 次失败应 401 invalid_credentials, got %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	rec, e := doJSON(t, s, "POST", "/api/v1/auth/login",
		`{"account":"alice","password":"wrong-pw"}`, nil)
	if rec.Code != http.StatusTooManyRequests || code(t, e) != "rate_limited" {
		t.Fatalf("达到阈值应 429 rate_limited, got %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 应带 Retry-After")
	}
}

func TestRegistrationClosed(t *testing.T) {
	s := newTestServer(t)
	settings.New(s.opts.DB).Set(model.SettingRegistrationMode, "closed")
	rec, e := doJSON(t, s, "POST", "/api/v1/auth/register",
		`{"username":"alice","email":"alice@img.li","password":"passw0rd"}`, nil)
	if rec.Code != http.StatusForbidden || code(t, e) != "registration_closed" {
		t.Errorf("registration_closed: %d %s", rec.Code, rec.Body.String())
	}
}

func TestOriginCheck(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{}`))
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("跨源写请求应 403, got %d", rec.Code)
	}
	// Origin 与 Host 同宿主（开发代理/直连场景）放行——用登录失败 401 证明进到了业务层
	req = httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{"account":"x","password":"y"}`))
	req.Header.Set("Origin", "http://"+req.Host)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden {
		t.Errorf("同宿主 Origin 不应被拦截")
	}
}

func TestOriginCheckNotBypassedByGarbageAuthHeader(t *testing.T) {
	s := newTestServer(t)
	sess := func() *http.Cookie { // 就地注册拿 cookie
		rec, _ := doJSON(t, s, "POST", "/api/v1/auth/register",
			`{"username":"alice","email":"alice@img.li","password":"passw0rd"}`, nil)
		for _, c := range rec.Result().Cookies() {
			if c.Name == "imgli_session" {
				return c
			}
		}
		t.Fatal("无 session cookie")
		return nil
	}()

	req := httptest.NewRequest("POST", "/api/v1/auth/logout", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Authorization", "x") // 非 Bearer 的垃圾头不得豁免 Origin 校验
	req.AddCookie(sess)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("垃圾 Authorization 头 + 跨源写请求应 403, got %d", rec.Code)
	}
}

// TestForgotPasswordAlways200 存在/不存在邮箱均 200 空 data，防枚举。
func TestForgotPasswordAlways200(t *testing.T) {
	s := newTestServer(t)
	doJSON(t, s, "POST", "/api/v1/auth/register",
		`{"username":"alice","email":"alice@img.li","password":"passw0rd"}`, nil)

	for _, body := range []string{
		`{"email":"alice@img.li"}`,
		`{"email":"ghost@img.li"}`,
	} {
		rec, e := doJSON(t, s, "POST", "/api/v1/auth/forgot-password", body, nil)
		if rec.Code != http.StatusOK || !e.Status {
			t.Errorf("forgot-password %s = %d %s, want 200", body, rec.Code, rec.Body.String())
		}
	}
}

// TestResetPasswordBadToken 坏 token → 400 token_invalid。
func TestResetPasswordBadToken(t *testing.T) {
	s := newTestServer(t)
	rec, e := doJSON(t, s, "POST", "/api/v1/auth/reset-password",
		`{"token":"not-a-real-token","password":"newpassw0rd"}`, nil)
	if rec.Code != http.StatusBadRequest || code(t, e) != "token_invalid" {
		t.Errorf("reset-password bad token: %d %s code=%s", rec.Code, rec.Body.String(), code(t, e))
	}
}

// TestEmailVerifiedInUserDTO 注册响应与 /auth/session 含 email_verified:false。
func TestEmailVerifiedInUserDTO(t *testing.T) {
	s := newTestServer(t)
	rec, e := doJSON(t, s, "POST", "/api/v1/auth/register",
		`{"username":"alice","email":"alice@img.li","password":"passw0rd"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	var reg struct {
		EmailVerified bool `json:"email_verified"`
	}
	if err := json.Unmarshal(e.Data, &reg); err != nil {
		t.Fatal(err)
	}
	if reg.EmailVerified {
		t.Error("注册后 email_verified 应为 false")
	}
	var sess *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "imgli_session" {
			sess = c
		}
	}
	if sess == nil {
		t.Fatal("无 session cookie")
	}
	rec, e = doJSON(t, s, "GET", "/api/v1/auth/session", "", []*http.Cookie{sess})
	if rec.Code != http.StatusOK {
		t.Fatalf("session: %d", rec.Code)
	}
	var session struct {
		EmailVerified bool `json:"email_verified"`
	}
	if err := json.Unmarshal(e.Data, &session); err != nil {
		t.Fatal(err)
	}
	if session.EmailVerified {
		t.Error("session email_verified 应为 false")
	}
}

// TestVerifyEmailBadToken 坏 token → 400 token_invalid。
func TestVerifyEmailBadToken(t *testing.T) {
	s := newTestServer(t)
	rec, e := doJSON(t, s, "POST", "/api/v1/auth/verify-email",
		`{"token":"not-a-real-token"}`, nil)
	if rec.Code != http.StatusBadRequest || code(t, e) != "token_invalid" {
		t.Errorf("verify-email bad token: %d %s code=%s", rec.Code, rec.Body.String(), code(t, e))
	}
}

// TestResendVerificationUnauthorized 未登录 resend → 401。
func TestResendVerificationUnauthorized(t *testing.T) {
	s := newTestServer(t)
	rec, e := doJSON(t, s, "POST", "/api/v1/auth/resend-verification", "", nil)
	if rec.Code != http.StatusUnauthorized || code(t, e) != "unauthorized" {
		t.Errorf("resend 未登录: %d %s code=%s", rec.Code, rec.Body.String(), code(t, e))
	}
}
