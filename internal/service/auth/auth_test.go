package auth

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/yixian-huang/imgli/internal/model"
	"github.com/yixian-huang/imgli/internal/service/settings"
)

// 注：model 仅用于 TestDB 与 SettingRegistrationMode 常量。

func newSvc(t *testing.T) (*Service, *settings.Service) {
	db := model.TestDB(t)
	st := settings.New(db)
	return New(db, st), st
}

func TestPasswordHashRoundTrip(t *testing.T) {
	h, err := HashPassword("s3cret-pw")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$") {
		t.Errorf("非 PHC 编码: %s", h)
	}
	if !VerifyPassword(h, "s3cret-pw") {
		t.Error("正确密码校验失败")
	}
	if VerifyPassword(h, "wrong") {
		t.Error("错误密码不应通过")
	}
}

func TestRegisterFirstUserIsAdmin(t *testing.T) {
	svc, _ := newSvc(t)
	u1, err := svc.Register("alice", "alice@img.li", "passw0rd", "")
	if err != nil {
		t.Fatal(err)
	}
	if !u1.IsAdmin {
		t.Error("首个用户应为管理员")
	}
	if u1.GroupID == 0 {
		t.Error("应分配默认组")
	}
	u2, err := svc.Register("bob", "bob@img.li", "passw0rd", "")
	if err != nil {
		t.Fatal(err)
	}
	if u2.IsAdmin {
		t.Error("第二个用户不应为管理员")
	}
}

func TestRegisterValidationAndDuplicates(t *testing.T) {
	svc, _ := newSvc(t)
	if _, err := svc.Register("alice", "alice@img.li", "short1", ""); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("短密码 err = %v", err)
	}
	if _, err := svc.Register("alice", "alice@img.li", "alllettersonly", ""); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("纯字母密码 err = %v", err)
	}
	if _, err := svc.Register("a", "alice@img.li", "passw0rd", ""); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("超短用户名 err = %v", err)
	}
	if _, err := svc.Register("alice", "not-an-email", "passw0rd", ""); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("非法邮箱 err = %v", err)
	}
	svc.Register("alice", "alice@img.li", "passw0rd", "")
	if _, err := svc.Register("alice2", "ALICE@img.li", "passw0rd", ""); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("重复邮箱（大小写不敏感）err = %v", err)
	}
	if _, err := svc.Register("alice", "other@img.li", "passw0rd", ""); !errors.Is(err, ErrUsernameTaken) {
		t.Errorf("重复用户名 err = %v", err)
	}
}

func TestRegisterClosedMode(t *testing.T) {
	svc, st := newSvc(t)
	st.Set(model.SettingRegistrationMode, "closed")
	if _, err := svc.Register("alice", "alice@img.li", "passw0rd", ""); !errors.Is(err, ErrRegistrationClosed) {
		t.Errorf("err = %v, want ErrRegistrationClosed", err)
	}
}

func TestLoginSessionLifecycle(t *testing.T) {
	svc, _ := newSvc(t)
	svc.Register("alice", "alice@img.li", "passw0rd", "")

	// 邮箱或用户名均可登录
	tok, u, err := svc.Login("alice@img.li", "passw0rd", "1.2.3.4", "ua")
	if err != nil || u.Username != "alice" || tok == "" {
		t.Fatalf("邮箱登录失败: %v", err)
	}
	tok2, _, err := svc.Login("alice", "passw0rd", "1.2.3.4", "ua")
	if err != nil || tok2 == "" {
		t.Fatalf("用户名登录失败: %v", err)
	}

	if _, _, err := svc.Login("alice", "wrong-pw", "1.2.3.4", "ua"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("错误密码 err = %v", err)
	}
	if _, _, err := svc.Login("ghost", "passw0rd", "1.2.3.4", "ua"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("不存在用户 err = %v（不得泄露用户是否存在）", err)
	}

	got, err := svc.UserBySession(tok)
	if err != nil || got == nil || got.Username != "alice" {
		t.Fatalf("UserBySession 失败: %+v, %v", got, err)
	}
	if err := svc.Logout(tok); err != nil {
		t.Fatal(err)
	}
	got, err = svc.UserBySession(tok)
	if err != nil || got != nil {
		t.Errorf("登出后 session 应失效: %+v", got)
	}
	if got, _ := svc.UserBySession("garbage"); got != nil {
		t.Error("垃圾 token 应返回 nil")
	}
}

func TestLoginThrottleLocksAccountThenRecovers(t *testing.T) {
	const email = "alice@example.test"
	svc, _ := newSvc(t)
	if _, err := svc.Register("alice", email, "passw0rd", ""); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	for i := 0; i < loginAccountPolicy.threshold-1; i++ {
		if _, _, err := svc.Login("alice", "wrong-pw", "1.2.3.4", "ua"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("第 %d 次应仍为凭据错误, got %v", i+1, err)
		}
	}
	_, _, err := svc.Login("alice", "wrong-pw", "1.2.3.4", "ua")
	if !errors.Is(err, ErrLoginThrottled) {
		t.Fatalf("达到阈值应触发锁定, got %v", err)
	}
	var te *LoginThrottledError
	if !errors.As(err, &te) || te.RetryAfter <= 0 {
		t.Fatalf("锁定错误应带 RetryAfter: %#v", err)
	}
	if _, _, err := svc.Login("alice", "passw0rd", "1.2.3.4", "ua"); !errors.Is(err, ErrLoginThrottled) {
		t.Fatalf("锁定期内正确密码也应拒绝, got %v", err)
	}

	now = now.Add(te.RetryAfter + time.Second)
	if _, _, err := svc.Login("alice", "passw0rd", "1.2.3.4", "ua"); err != nil {
		t.Fatalf("锁定过期后正确密码应成功, got %v", err)
	}
}

func TestLoginSuccessResetsThrottle(t *testing.T) {
	const email = "alice@example.test"
	svc, _ := newSvc(t)
	if _, err := svc.Register("alice", email, "passw0rd", ""); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	for i := 0; i < loginAccountPolicy.threshold-1; i++ {
		if _, _, err := svc.Login("alice", "wrong-pw", "1.2.3.4", "ua"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("登录成功前第 %d 次应仍为凭据错误, got %v", i+1, err)
		}
	}
	if _, _, err := svc.Login("alice", "passw0rd", "1.2.3.4", "ua"); err != nil {
		t.Fatalf("正确密码应成功: %v", err)
	}
	for i := 0; i < loginAccountPolicy.threshold-1; i++ {
		if _, _, err := svc.Login("alice", "wrong-pw", "1.2.3.4", "ua"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("成功清空后第 %d 次应重新计数, got %v", i+1, err)
		}
	}
}

func TestLoginThrottleUsesUserIDAcrossIdentifiers(t *testing.T) {
	const email = "alice@example.test"
	svc, _ := newSvc(t)
	if _, err := svc.Register("alice", email, "passw0rd", ""); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	// 邮箱 2 次 + 用户名 2 次，都应对应同一个 user ID；第 5 次触发锁定。
	for _, account := range []string{email, email, "alice", "alice"} {
		if _, _, err := svc.Login(account, "wrong-pw", "1.2.3.4", "ua"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("%s 登录应仍为凭据错误, got %v", account, err)
		}
	}
	if _, _, err := svc.Login(email, "wrong-pw", "1.2.3.4", "ua"); !errors.Is(err, ErrLoginThrottled) {
		t.Fatalf("切换邮箱/用户名不应绕过账号级锁定, got %v", err)
	}
}

func TestLoginThrottleLocksIPAcrossAccounts(t *testing.T) {
	svc, _ := newSvc(t)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	const ip = "203.0.113.9"
	ipKey := loginIPThrottleKey(ip)
	for i := 0; i < loginIPPolicy.threshold; i++ {
		svc.recordLoginFailure(loginAccountKey(fmt.Sprintf("ghost%d", i)), ipKey, now)
	}
	if _, _, err := svc.Login("alice", "passw0rd", ip, "ua"); !errors.Is(err, ErrLoginThrottled) {
		t.Fatalf("同一 IP 跨账号累计失败应触发 IP 锁定, got %v", err)
	}
}

func TestBannedUserRejected(t *testing.T) {
	svc, _ := newSvc(t)
	u, _ := svc.Register("alice", "alice@img.li", "passw0rd", "")
	tok, _, _ := svc.Login("alice", "passw0rd", "", "")

	// 封禁直接改库（同包测试可访问 svc.db 私有字段）
	if err := svc.db.Model(u).Update("status", "banned").Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Login("alice", "passw0rd", "", ""); !errors.Is(err, ErrUserBanned) {
		t.Errorf("封禁用户登录 err = %v", err)
	}
	if got, _ := svc.UserBySession(tok); got != nil {
		t.Error("封禁用户已有 session 应立即失效")
	}
}

func TestExpiredSessionCleaned(t *testing.T) {
	svc, _ := newSvc(t)
	svc.Register("alice", "alice@img.li", "passw0rd", "")
	tok, _, _ := svc.Login("alice", "passw0rd", "", "")
	// 手动过期
	if err := svc.db.Model(&model.Session{}).Where("user_id IS NOT NULL").
		Update("expires_at", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.UserBySession(tok); got != nil {
		t.Error("过期 session 应返回 nil")
	}
	var n int64
	svc.db.Model(&model.Session{}).Count(&n)
	if n != 0 {
		t.Error("过期 session 行应被顺手删除")
	}
}

func TestChangePasswordKillsSessions(t *testing.T) {
	svc, _ := newSvc(t)
	u, _ := svc.Register("alice", "alice@img.li", "passw0rd", "")
	tok, _, _ := svc.Login("alice", "passw0rd", "", "")

	if err := svc.ChangePassword(u.ID, "wrong", "newpassw0rd"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("旧密码错误 err = %v", err)
	}
	if err := svc.ChangePassword(u.ID, "passw0rd", "weak"); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("弱新密码 err = %v", err)
	}
	if err := svc.ChangePassword(u.ID, "passw0rd", "newpassw0rd"); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.UserBySession(tok); got != nil {
		t.Error("改密后旧 session 应全部失效")
	}
	if _, _, err := svc.Login("alice", "newpassw0rd", "", ""); err != nil {
		t.Errorf("新密码登录失败: %v", err)
	}
}

func TestQuotaAndNickname(t *testing.T) {
	svc, _ := newSvc(t)
	u, _ := svc.Register("alice", "alice@img.li", "passw0rd", "")

	used, total, err := svc.Quota(u.ID)
	if err != nil || used != 0 || total != 10<<30 {
		t.Errorf("quota = %d/%d, err=%v（默认组 10GB）", used, total, err)
	}
	if err := svc.UpdateNickname(u.ID, "爱丽丝"); err != nil {
		t.Fatal(err)
	}
	if err := svc.UpdateNickname(u.ID, ""); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("空昵称 err = %v", err)
	}
}

func TestSetPublicProfile(t *testing.T) {
	svc, _ := newSvc(t)
	u, err := svc.Register("pubprof", "pubprof@img.li", "passw0rd1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetPublicProfile(u.ID, true); err != nil {
		t.Fatal(err)
	}
	var got model.User
	if err := svc.db.First(&got, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !got.PublicProfile {
		t.Error("PublicProfile 应为 true")
	}
	if err := svc.SetPublicProfile(u.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.db.First(&got, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.PublicProfile {
		t.Error("PublicProfile 应回到 false")
	}
	if err := svc.SetPublicProfile(999999, true); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("不存在用户 err = %v, want ErrInvalidInput", err)
	}
}

func TestQuotaInfo(t *testing.T) {
	svc, _ := newSvc(t)
	u, err := svc.Register("quotainfo", "qi@img.li", "passw0rd1", "")
	if err != nil {
		t.Fatal(err)
	}
	var g model.UserGroup
	if err := svc.db.First(&g, u.GroupID).Error; err != nil {
		t.Fatal(err)
	}
	qi, err := svc.QuotaInfo(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if qi.Used != 0 || qi.Total != g.StorageQuota {
		t.Errorf("used/total = %d/%d, want 0/%d", qi.Used, qi.Total, g.StorageQuota)
	}
	if qi.MaxFileSize != g.MaxFileSize {
		t.Errorf("max_file_size = %d, want %d", qi.MaxFileSize, g.MaxFileSize)
	}
	if len(qi.AllowedExts) == 0 || len(qi.AllowedExts) != len(g.AllowedExts) {
		t.Errorf("allowed_exts = %v, want %v", qi.AllowedExts, g.AllowedExts)
	}
}

func TestRegisterDuplicateRaceMapsToConflict(t *testing.T) {
	svc, _ := newSvc(t)
	if _, err := svc.Register("alice", "alice@img.li", "passw0rd", ""); err != nil {
		t.Fatal(err)
	}
	// 显式并发不易稳定复现，这里直接验证：唯一索引冲突被翻译为可识别错误而非裸 500
	_, err := svc.Register("alice", "other@img.li", "passw0rd", "")
	if !errors.Is(err, ErrUsernameTaken) && !errors.Is(err, ErrAccountConflict) {
		t.Errorf("重复用户名应为 taken/conflict, got %v", err)
	}
}

// 造一张邀请码;expires 为零值表示不过期。
func seedInvite(t *testing.T, db *gorm.DB, code string, expires *time.Time) *model.InviteCode {
	t.Helper()
	ic := &model.InviteCode{Code: code, CreatedBy: 1, ExpiresAt: expires}
	if err := db.Create(ic).Error; err != nil {
		t.Fatal(err)
	}
	return ic
}

func TestRegisterInviteMode(t *testing.T) {
	db := model.TestDB(t)
	st := settings.New(db)
	if err := st.Set(model.SettingRegistrationMode, "invite"); err != nil {
		t.Fatal(err)
	}
	svc := New(db, st)

	// 无码
	if _, err := svc.Register("alice", "alice@img.li", "passw0rd", ""); !errors.Is(err, ErrInviteRequired) {
		t.Errorf("无码 err = %v, want ErrInviteRequired", err)
	}
	// 坏码
	if _, err := svc.Register("alice", "alice@img.li", "passw0rd", "IL-XXXX-XXXX"); !errors.Is(err, ErrInviteInvalid) {
		t.Errorf("坏码 err = %v, want ErrInviteInvalid", err)
	}
	// 过期码
	past := time.Now().Add(-time.Hour)
	seedInvite(t, db, "IL-EXPD-EXPD", &past)
	if _, err := svc.Register("alice", "alice@img.li", "passw0rd", "IL-EXPD-EXPD"); !errors.Is(err, ErrInviteInvalid) {
		t.Errorf("过期码 err = %v, want ErrInviteInvalid", err)
	}
	// 好码:注册成功且核销(记 used_by/used_at);小写输入应被规整
	seedInvite(t, db, "IL-G88D-G88D", nil)
	u, err := svc.RegisterWithMeta("alice", "alice@img.li", "passw0rd", " il-g88d-g88d ", SignupMeta{
		UTMSource: "should-lose-to-invite",
	})
	if err != nil {
		t.Fatalf("好码注册失败: %v", err)
	}
	var ic model.InviteCode
	if err := db.Where("code = ?", "IL-G88D-G88D").First(&ic).Error; err != nil {
		t.Fatal(err)
	}
	if ic.UsedBy == nil || *ic.UsedBy != u.ID || ic.UsedAt == nil {
		t.Errorf("码未核销: used_by=%v used_at=%v", ic.UsedBy, ic.UsedAt)
	}
	if u.SignupChannel != ChannelInvite {
		t.Errorf("signup_channel=%q want invite", u.SignupChannel)
	}
	// 二次使用同码:拒绝,且用户不残留
	if _, err := svc.Register("bob", "bob@img.li", "passw0rd", "IL-G88D-G88D"); !errors.Is(err, ErrInviteInvalid) {
		t.Errorf("已用码 err = %v, want ErrInviteInvalid", err)
	}
	var n int64
	db.Model(&model.User{}).Where("username = ?", "bob").Count(&n)
	if n != 0 {
		t.Error("已用码注册失败后不应残留用户(事务回滚)")
	}
}

func TestRegisterOpenModeIgnoresInvite(t *testing.T) {
	db := model.TestDB(t)
	svc := New(db, settings.New(db)) // 缺省 open
	if _, err := svc.Register("alice", "alice@img.li", "passw0rd", "IL-JUNK-JUNK"); err != nil {
		t.Errorf("open 模式应忽略邀请码, err = %v", err)
	}
}

func TestRegisterWithMetaUTMAndReferer(t *testing.T) {
	db := model.TestDB(t)
	svc := New(db, settings.New(db))
	u, err := svc.RegisterWithMeta("utmuser", "utm@img.li", "passw0rd1", "", SignupMeta{
		UTMSource: "github", UTMMedium: "social", UTMCampaign: "v02",
		RefererHost: "https://news.example/path",
	})
	if err != nil {
		t.Fatal(err)
	}
	if u.SignupChannel != ChannelUTM {
		t.Fatalf("channel=%s want utm", u.SignupChannel)
	}
	if u.SignupUTMSource != "github" || u.SignupRefererHost != "news.example" {
		t.Fatalf("meta=%+v", u)
	}
	u2, err := svc.RegisterWithMeta("refuser", "ref@img.li", "passw0rd1", "", SignupMeta{
		RefererHost: "blog.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	if u2.SignupChannel != ChannelReferer || u2.SignupRefererHost != "blog.example" {
		t.Fatalf("ref user=%+v", u2)
	}
}

// fakeMailer 并发安全邮件桩;异步发送经 waitReset/waitVerify 轮询。
type fakeMailer struct {
	welcome                          [][3]string
	mu                               sync.Mutex
	resetTo, resetLink, resetLang    string
	verifyTo, verifyLink, verifyLang string
	resetCalls                       int
	verifyCalls                      int
}

func newFakeMailer() *fakeMailer { return &fakeMailer{} }

func (f *fakeMailer) SendResetPassword(to, link, lang string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resetTo, f.resetLink, f.resetLang = to, link, lang
	f.resetCalls++
	return nil
}
func (f *fakeMailer) SendChangeEmail(to, link, lang string) error {
	return nil
}

func (f *fakeMailer) SendWelcome(to, baseURL, lang string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.welcome = append(f.welcome, [3]string{to, baseURL, lang})
	return nil
}

func (f *fakeMailer) SendVerifyEmail(to, link, lang string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verifyTo, f.verifyLink, f.verifyLang = to, link, lang
	f.verifyCalls++
	return nil
}

// waitReset 轮询直到至少 minCalls 次 SendResetPassword,返回 (to, link, lang);超时则 Fatal。
func (f *fakeMailer) waitReset(t *testing.T, minCalls int) (to, link, lang string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		if f.resetCalls >= minCalls {
			to, link, lang = f.resetTo, f.resetLink, f.resetLang
			f.mu.Unlock()
			return to, link, lang
		}
		f.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等待 fakeMailer.SendResetPassword resetCalls>=%d 超时(2s)", minCalls)
	return "", "", ""
}

// waitVerify 轮询直到至少 1 次 SendVerifyEmail,返回 (to, link, lang);超时则 Fatal。
func (f *fakeMailer) waitVerify(t *testing.T) (to, link, lang string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		if f.verifyCalls >= 1 {
			to, link, lang = f.verifyTo, f.verifyLink, f.verifyLang
			f.mu.Unlock()
			return to, link, lang
		}
		f.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等待 fakeMailer.SendVerifyEmail 超时(2s)")
	return "", "", ""
}

func TestForgotPasswordFlow(t *testing.T) {
	svc, _ := newSvc(t)
	fm := newFakeMailer()
	svc.Mailer = fm
	svc.BaseURL = "http://localhost:8686"
	u, err := svc.Register("alice", "alice@img.li", "passw0rd", "")
	if err != nil {
		t.Fatal(err)
	}
	// 不存在的邮箱:恒 nil 且不发 reset 信
	if err := svc.ForgotPassword("ghost@img.li"); err != nil {
		t.Fatalf("不存在邮箱应恒 nil, got %v", err)
	}
	time.Sleep(50 * time.Millisecond) // 给异步路径一点时间(不应有)
	fm.mu.Lock()
	if fm.resetTo != "" || fm.resetCalls != 0 {
		fm.mu.Unlock()
		t.Error("不存在邮箱不应发信")
	} else {
		fm.mu.Unlock()
	}
	// 存在:异步发信,链接含 /reset-password?token=;默认空 lang 透传
	if err := svc.ForgotPassword("alice@img.li"); err != nil {
		t.Fatal(err)
	}
	to, link, lang := fm.waitReset(t, 1)
	if to != "alice@img.li" || !strings.Contains(link, "http://localhost:8686/reset-password?token=") {
		t.Fatalf("发信不符: to=%q link=%q", to, link)
	}
	if lang != "" {
		t.Errorf("默认 Preferences.Lang 应空, got %q", lang)
	}
	tok1 := strings.TrimPrefix(link, "http://localhost:8686/reset-password?token=")
	// 再次请求:旧 token 作废,只最新有效
	if err := svc.ForgotPassword("alice@img.li"); err != nil {
		t.Fatal(err)
	}
	_, link2, _ := fm.waitReset(t, 2)
	tok2 := strings.TrimPrefix(link2, "http://localhost:8686/reset-password?token=")
	if err := svc.ResetPasswordByToken(tok1, "newpass-111"); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("旧 token 应失效, err = %v", err)
	}
	// 建一个 session,重置后应被吊销
	sess, _, err := svc.Login("alice", "passw0rd", "1.1.1.1", "ua")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ResetPasswordByToken(tok2, "weak"); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("弱密码 err = %v", err)
	}
	if err := svc.ResetPasswordByToken(tok2, "newpass-111"); err != nil {
		t.Fatalf("重置失败: %v", err)
	}
	// 重置成功后该用户不应再有未用 reset 令牌
	var unused int64
	if err := svc.db.Model(&model.AuthToken{}).
		Where("user_id = ? AND purpose = ? AND used_at IS NULL", u.ID, "reset_password").
		Count(&unused).Error; err != nil {
		t.Fatal(err)
	}
	if unused != 0 {
		t.Errorf("重置后未用 reset 令牌应全作废, got %d", unused)
	}
	if got, _ := svc.UserBySession(sess); got != nil {
		t.Error("重置后旧 session 应失效")
	}
	if _, _, err := svc.Login("alice", "newpass-111", "1.1.1.1", "ua"); err != nil {
		t.Errorf("新密码应可登录: %v", err)
	}
	if err := svc.ResetPasswordByToken(tok2, "newpass-222"); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("token 应一次性, err = %v", err)
	}
}

func TestForgotPasswordNoMailerStillNil(t *testing.T) {
	svc, _ := newSvc(t)
	svc.Register("alice", "alice@img.li", "passw0rd", "")
	if err := svc.ForgotPassword("alice@img.li"); err != nil {
		t.Errorf("Mailer 为空也应恒 nil(仅日志), got %v", err)
	}
}

func TestRegisterSendsVerifyEmail(t *testing.T) {
	svc, _ := newSvc(t)
	fm := newFakeMailer()
	svc.Mailer = fm
	svc.BaseURL = "http://localhost:8686"
	u, err := svc.Register("alice", "alice@img.li", "passw0rd", "")
	if err != nil {
		t.Fatal(err)
	}
	// token 同步落库(注册返回时已存在)
	var n int64
	svc.db.Model(&model.AuthToken{}).Where("user_id = ? AND purpose = 'verify_email' AND used_at IS NULL", u.ID).Count(&n)
	if n != 1 {
		t.Fatalf("注册后应有 1 条未用 verify token, got %d", n)
	}
	to, link, lang := fm.waitVerify(t) // 异步发送最终到达
	if to != "alice@img.li" || !strings.Contains(link, "/verify-email?token=") {
		t.Errorf("verify 邮件不符: %q %q", to, link)
	}
	if lang != "" {
		t.Errorf("注册默认 lang 应空, got %q", lang)
	}
	raw := strings.TrimPrefix(link, "http://localhost:8686/verify-email?token=")
	if err := svc.VerifyEmail(raw); err != nil {
		t.Fatalf("验证失败: %v", err)
	}
	var u2 model.User
	svc.db.First(&u2, u.ID)
	if u2.EmailVerifiedAt == nil {
		t.Error("EmailVerifiedAt 未置")
	}
	if err := svc.VerifyEmail(raw); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("token 一次性, err = %v", err)
	}
	if err := svc.ResendVerification(u.ID); !errors.Is(err, ErrAlreadyVerified) {
		t.Errorf("已验证重发 err = %v, want ErrAlreadyVerified", err)
	}
}

func TestRegisterNoMailerSkipsVerify(t *testing.T) {
	svc, _ := newSvc(t)
	u, err := svc.Register("alice", "alice@img.li", "passw0rd", "")
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	svc.db.Model(&model.AuthToken{}).Where("user_id = ?", u.ID).Count(&n)
	if n != 0 {
		t.Errorf("Mailer 为空不应建 verify token, got %d", n)
	}
}

// TestMailerReceivesUserLang 注册/忘记密码/重发均把 Preferences.Lang 传给 Mailer。
func TestMailerReceivesUserLang(t *testing.T) {
	svc, _ := newSvc(t)
	fm := newFakeMailer()
	svc.Mailer = fm
	svc.BaseURL = "http://localhost:8686"

	u, err := svc.Register("alice", "alice@img.li", "passw0rd", "")
	if err != nil {
		t.Fatal(err)
	}
	// 先收到空 lang 的注册验证信
	_, _, lang0 := fm.waitVerify(t)
	if lang0 != "" {
		t.Fatalf("注册默认 lang 空, got %q", lang0)
	}

	// 设 Preferences.Lang=en 后重发验证
	u.Preferences.Lang = "en"
	if err := svc.db.Model(u).Select("preferences").Updates(u).Error; err != nil {
		t.Fatal(err)
	}
	fm.mu.Lock()
	fm.verifyCalls = 0
	fm.mu.Unlock()
	if err := svc.ResendVerification(u.ID); err != nil {
		t.Fatal(err)
	}
	_, _, lang := fm.waitVerify(t)
	if lang != "en" {
		t.Errorf("ResendVerification lang = %q, want en", lang)
	}

	// ForgotPassword 用用户 lang
	if err := svc.ForgotPassword("alice@img.li"); err != nil {
		t.Fatal(err)
	}
	_, _, resetLang := fm.waitReset(t, 1)
	if resetLang != "en" {
		t.Errorf("ForgotPassword lang = %q, want en", resetLang)
	}
}
