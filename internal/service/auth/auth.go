// Package auth 账户与会话：注册（首用户即管理员）、登录、登出、session 解析。
package auth

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/yixian-huang/imgli/internal/imaging"
	"github.com/yixian-huang/imgli/internal/model"
	"github.com/yixian-huang/imgli/internal/service/bandwidth"
	"github.com/yixian-huang/imgli/internal/service/settings"
	"github.com/yixian-huang/imgli/internal/token"
)

const SessionTTL = 30 * 24 * time.Hour

// Mailer 邮件发送的窄接口(internal/mail.Service 实现);为空时相关流静默跳过发送。
type Mailer interface {
	SendResetPassword(to, link, lang string) error
	SendVerifyEmail(to, link, lang string) error
	SendChangeEmail(to, link, lang string) error
	SendWelcome(to, baseURL, lang string) error
}

var (
	ErrEmailTaken         = errors.New("auth: 邮箱已被注册")
	ErrUsernameTaken      = errors.New("auth: 用户名已被占用")
	ErrInvalidCredentials = errors.New("auth: 账号或密码错误")
	ErrLoginThrottled     = errors.New("auth: 登录尝试过于频繁")
	ErrRegistrationClosed = errors.New("auth: 注册未开放")
	ErrInviteRequired     = errors.New("auth: 邀请模式需提供邀请码")
	ErrInviteInvalid      = errors.New("auth: 邀请码无效或已被使用")
	ErrUserBanned         = errors.New("auth: 账号已被封禁")
	ErrWeakPassword       = errors.New("auth: 密码至少 8 位且包含字母和数字")
	ErrInvalidInput       = errors.New("auth: 输入不合法")
	ErrAccountConflict    = errors.New("auth: 账号信息冲突")
	ErrTokenInvalid       = errors.New("auth: 链接无效或已过期")
	ErrAlreadyVerified    = errors.New("auth: 邮箱已验证")
	ErrWrongPassword      = errors.New("auth: 密码错误")
)

const (
	resetTokenTTL  = time.Hour
	verifyTokenTTL = 24 * time.Hour // 预留给邮箱验证流
)

var (
	usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{3,32}$`)
	emailRe    = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	hasLetter  = regexp.MustCompile(`[a-zA-Z]`)
	hasDigit   = regexp.MustCompile(`[0-9]`)
	dummyHash  string // 等时防枚举用哑哈希，init 一次
)

func init() {
	dummyHash, _ = HashPassword("imgli-timing-equalizer")
}

func validPassword(pw string) bool {
	return len(pw) >= 8 && hasLetter.MatchString(pw) && hasDigit.MatchString(pw)
}

type Service struct {
	db      *gorm.DB
	st      *settings.Service
	Mailer  Mailer // 可选;server 组装注入
	BaseURL string // 外链基础地址,如 https://img.li

	// 登录失败节流：状态落 DB，进程内用一把锁串行化读改写（当前单实例部署）。
	throttleMu        sync.Mutex
	lastThrottleSweep time.Time
	now               func() time.Time // 测试可替换
}

func New(db *gorm.DB, st *settings.Service) *Service {
	return &Service{db: db, st: st, now: time.Now}
}

// Register 创建用户（无归因）。等价于 RegisterWithMeta(..., SignupMeta{})。
func (s *Service) Register(username, email, password, inviteCode string) (*model.User, error) {
	return s.RegisterWithMeta(username, email, password, inviteCode, SignupMeta{})
}

// RegisterWithMeta 创建用户并可写入注册时刻轻量归因。open 直接注册(忽略邀请码);
// invite 需有效邀请码(事务内核销,条件 UPDATE 防并发双用);closed 拒绝。
// 第一个注册用户自动成为管理员(spec §7)。
func (s *Service) RegisterWithMeta(username, email, password, inviteCode string, meta SignupMeta) (*model.User, error) {
	mode := s.st.RegistrationMode()
	code := strings.ToUpper(strings.TrimSpace(inviteCode))
	switch mode {
	case "open":
		// 忽略邀请码
	case "invite":
		if code == "" {
			return nil, ErrInviteRequired
		}
	default:
		return nil, ErrRegistrationClosed
	}
	username = strings.TrimSpace(username)
	email = strings.ToLower(strings.TrimSpace(email))
	if !usernameRe.MatchString(username) || !emailRe.MatchString(email) {
		return nil, ErrInvalidInput
	}
	if !validPassword(password) {
		return nil, ErrWeakPassword
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	meta = meta.Sanitize()
	var u *model.User
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&model.User{}).Where("email = ?", email).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return ErrEmailTaken
		}
		if err := tx.Model(&model.User{}).Where("username = ?", username).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return ErrUsernameTaken
		}
		var group model.UserGroup
		if err := tx.Where("is_default = ?", true).First(&group).Error; err != nil {
			return err
		}
		inviteUsed := mode == "invite"
		u = &model.User{
			Username: username, Email: email, PasswordHash: hash,
			Nickname: username, GroupID: group.ID,
			Status:            "active",
			SignupChannel:     DeriveChannel(inviteUsed, meta),
			SignupUTMSource:   meta.UTMSource,
			SignupUTMMedium:   meta.UTMMedium,
			SignupUTMCampaign: meta.UTMCampaign,
			SignupRefererHost: meta.RefererHost,
		}
		if err := tx.Create(u).Error; err != nil {
			return err
		}
		if mode == "invite" {
			var inv model.InviteCode
			res := tx.Model(&model.InviteCode{}).
				Where("code = ? AND used_by IS NULL AND (expires_at IS NULL OR expires_at > ?)", code, time.Now()).
				Updates(map[string]any{"used_by": u.ID, "used_at": time.Now()})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return ErrInviteInvalid
			}
			// capture invite id for attribution (best-effort after use)
			if err := tx.Where("code = ?", code).First(&inv).Error; err == nil {
				_ = tx.Model(u).Update("signup_invite_code_id", inv.ID).Error
				id := inv.ID
				u.SignupInviteCodeID = &id
			}
		}
		// 首管理员认领：settings 主键唯一性保证恰有一人成功。必须用 ON CONFLICT
		// DO NOTHING 而非「插入失败再认错」——Postgres 里语句一失败整个事务即
		// aborted,后续 COMMIT 会变 rollback(SQLite 无此语义,曾因此只在 PG 上炸)。
		claim := tx.Clauses(clause.OnConflict{DoNothing: true}).
			Create(&model.Setting{Key: "first_admin_claimed", Value: fmt.Sprintf("%d", u.ID)})
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected == 0 {
			return nil // 已有首管理员
		}
		u.IsAdmin = true
		return tx.Model(u).Update("is_admin", true).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			// 与显式 Count 检查赛跑失败：唯一索引兜底
			return nil, ErrAccountConflict
		}
		return nil, err
	}
	s.sendVerifyEmail(u)
	s.sendWelcomeEmail(u)
	return u, nil
}

// Login 校验凭据并创建 session，返回明文 token（仅此一次）。
// 失败按账号与 IP 双维度计数；达到阈值后返回 ErrLoginThrottled（带 RetryAfter），
// 成功则清空计数。它和 handler 的每分钟限流互为补充：这里挡的是持续猜密码，
// 不是单纯的请求速率。
func (s *Service) Login(account, password, ip, ua string) (string, *model.User, error) {
	account = strings.TrimSpace(account)
	now := s.now()
	ipKey := loginIPThrottleKey(ip)
	if retry, locked := s.lockedThrottleRetry([]string{ipKey}, now); locked {
		return "", nil, &LoginThrottledError{RetryAfter: retry}
	}

	var u model.User
	err := s.db.Where("email = ? OR username = ?", strings.ToLower(account), account).First(&u).Error
	inputKey := loginAccountKey(account)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if retry, locked := s.lockedThrottleRetry([]string{inputKey}, now); locked {
			return "", nil, &LoginThrottledError{RetryAfter: retry}
		}
		// 与密码错误同码，不泄露用户是否存在；等时防枚举
		VerifyPassword(dummyHash, password)
		if retry := s.recordLoginFailure(inputKey, ipKey, now); retry > 0 {
			return "", nil, &LoginThrottledError{RetryAfter: retry}
		}
		return "", nil, ErrInvalidCredentials
	}
	if err != nil {
		return "", nil, err
	}

	// 用户名和邮箱统一按 user ID 计数，避免切换登录标识绕过账号级限制。
	accountKey := loginAccountIDKey(u.ID)
	if retry, locked := s.lockedThrottleRetry([]string{accountKey}, now); locked {
		return "", nil, &LoginThrottledError{RetryAfter: retry}
	}
	if !VerifyPassword(u.PasswordHash, password) {
		if retry := s.recordLoginFailure(accountKey, ipKey, now); retry > 0 {
			return "", nil, &LoginThrottledError{RetryAfter: retry}
		}
		return "", nil, ErrInvalidCredentials
	}
	if u.Status == "banned" {
		return "", nil, ErrUserBanned
	}

	// 成功即清空账号和 IP 计数；inputKey 用于清理切换登录标识留下的旧行。
	s.clearLoginThrottle(accountKey, inputKey, ipKey)
	raw, err := s.CreateSession(&u, ip, ua)
	if err != nil {
		return "", nil, err
	}
	return raw, &u, nil
}

// CreateSession 为已验证用户签发 session 明文 token。
func (s *Service) CreateSession(u *model.User, ip, ua string) (string, error) {
	raw, err := token.Random()
	if err != nil {
		return "", err
	}
	sess := model.Session{
		ID: token.Hash(raw), UserID: u.ID,
		ExpiresAt: time.Now().Add(SessionTTL), IP: ip, UA: ua,
	}
	if err := s.db.Create(&sess).Error; err != nil {
		return "", err
	}
	return raw, nil
}

func (s *Service) Logout(rawToken string) error {
	return s.db.Delete(&model.Session{}, "id = ?", token.Hash(rawToken)).Error
}

// UserBySession 解析 session。无效/过期/封禁一律 (nil, nil)——匿名继续，由上层拦截。
func (s *Service) UserBySession(rawToken string) (*model.User, error) {
	if rawToken == "" {
		return nil, nil
	}
	var sess model.Session
	err := s.db.First(&sess, "id = ?", token.Hash(rawToken)).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if time.Now().After(sess.ExpiresAt) {
		s.db.Delete(&model.Session{}, "id = ?", sess.ID)
		return nil, nil
	}
	var u model.User
	if err := s.db.First(&u, sess.UserID).Error; err != nil {
		return nil, err
	}
	if u.Status == "banned" {
		return nil, nil
	}
	return &u, nil
}

// UpdateNickname 更新昵称（1-64 个字符）。
func (s *Service) UpdateNickname(userID uint64, nickname string) error {
	nickname = strings.TrimSpace(nickname)
	if n := utf8.RuneCountInString(nickname); n < 1 || n > 64 {
		return ErrInvalidInput
	}
	return s.db.Model(&model.User{}).Where("id = ?", userID).
		Update("nickname", nickname).Error
}

// SetPublicProfile 设置用户是否参与广场/公开主页。用户不存在返回 ErrInvalidInput。
func (s *Service) SetPublicProfile(userID uint64, v bool) error {
	res := s.db.Model(&model.User{}).Where("id = ?", userID).Update("public_profile", v)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrInvalidInput
	}
	return nil
}

var validCopyFormats = map[string]bool{"": true, "url": true, "markdown": true, "html": true, "bbcode": true, "share": true}
var validLangs = map[string]bool{"": true, "zh": true, "en": true}

// UpdatePreferences 全量替换用户上传偏好。校验:可见性/复制格式/语言枚举、
// 默认相册须属主存在、默认策略须在用户组允许清单内且 enabled。
func (s *Service) UpdatePreferences(userID uint64, p model.Preferences) error {
	if p.DefaultVisibility != "" && p.DefaultVisibility != "public" && p.DefaultVisibility != "private" {
		return ErrInvalidInput
	}
	if !validCopyFormats[p.AutoCopyFormat] {
		return ErrInvalidInput
	}
	if !validLangs[p.Lang] {
		return ErrInvalidInput
	}
	if p.DefaultAlbumID != nil {
		var n int64
		if err := s.db.Model(&model.Album{}).
			Where("id = ? AND user_id = ?", *p.DefaultAlbumID, userID).Count(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			return ErrInvalidInput
		}
	}
	if p.DefaultPolicyID != nil {
		var u model.User
		if err := s.db.First(&u, userID).Error; err != nil {
			return err
		}
		var g model.UserGroup
		if err := s.db.First(&g, u.GroupID).Error; err != nil {
			return err
		}
		allowed := false
		for _, id := range g.AllowedPolicyIDs {
			if id == *p.DefaultPolicyID {
				allowed = true
				break
			}
		}
		if !allowed {
			return ErrInvalidInput
		}
		var n int64
		if err := s.db.Model(&model.StoragePolicy{}).
			Where("id = ? AND enabled = ?", *p.DefaultPolicyID, true).Count(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			return ErrInvalidInput
		}
	}
	if w := p.Watermark; true {
		if w.Position != "" && !imaging.Positions[w.Position] {
			return ErrInvalidInput
		}
		if w.Opacity < 0 || w.Opacity > 1 {
			return ErrInvalidInput
		}
		if w.Margin < 0 || w.Margin > 256 {
			return ErrInvalidInput
		}
	}
	// 走结构体 Select+Updates：preferences 带 serializer:json，
	// Update("col", value) 不经字段序列化器，struct 会直接递给驱动报错。
	return s.db.Model(&model.User{}).Where("id = ?", userID).
		Select("preferences").Updates(model.User{Preferences: p}).Error
}

// SetAvatarPath 更新头像路径(空串=清除)。Update 会顺带刷新 UpdatedAt,
// userDTO 以其为头像 URL 版本号破缓存。0 行更新报 ErrRecordNotFound——
// 用户可能已在并发注销中消失,调用方须据此清理刚落盘的头像文件(codex 终审)。
func (s *Service) SetAvatarPath(userID uint64, path string) error {
	res := s.db.Model(&model.User{}).Where("id = ?", userID).
		Update("avatar_path", path)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// SetWatermarkPath 更新水印图路径(空串=清除)。0 行更新报 ErrRecordNotFound——
// 用户可能已在并发注销中消失,调用方须据此清理刚落盘的水印文件(codex 终审)。
func (s *Service) SetWatermarkPath(userID uint64, path string) error {
	res := s.db.Model(&model.User{}).Where("id = ?", userID).
		Update("watermark_path", path)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// PolicyOption 用户可选存储策略(GET /user/policies)。
type PolicyOption struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
}

// UserPolicies 当前用户组允许且 enabled 的策略,保 allowed_policy_ids 原序。
func (s *Service) UserPolicies(u *model.User) ([]PolicyOption, error) {
	var g model.UserGroup
	if err := s.db.First(&g, u.GroupID).Error; err != nil {
		return nil, err
	}
	out := make([]PolicyOption, 0, len(g.AllowedPolicyIDs))
	for _, id := range g.AllowedPolicyIDs {
		var p model.StoragePolicy
		if err := s.db.First(&p, "id = ? AND enabled = ?", id, true).Error; err != nil {
			continue
		}
		out = append(out, PolicyOption{ID: p.ID, Name: p.Name})
	}
	return out, nil
}

// ChangePassword 校验旧密码后更新，并删除该用户全部 session（全设备登出）。
func (s *Service) ChangePassword(userID uint64, oldPW, newPW string) error {
	var u model.User
	if err := s.db.First(&u, userID).Error; err != nil {
		return err
	}
	if !VerifyPassword(u.PasswordHash, oldPW) {
		return ErrInvalidCredentials
	}
	if !validPassword(newPW) {
		return ErrWeakPassword
	}
	hash, err := HashPassword(newPW)
	if err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&u).Update("password_hash", hash).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Session{}, "user_id = ?", u.ID).Error
	})
}

// createAuthToken 作废同用户同用途旧未用令牌并建新令牌,返回明文。
func (s *Service) createAuthToken(tx *gorm.DB, userID uint64, purpose string, ttl time.Duration) (string, error) {
	return s.createAuthTokenPayload(tx, userID, purpose, "", ttl)
}

func (s *Service) createAuthTokenPayload(tx *gorm.DB, userID uint64, purpose, payload string, ttl time.Duration) (string, error) {
	raw, err := token.Random()
	if err != nil {
		return "", err
	}
	now := time.Now()
	if err := tx.Model(&model.AuthToken{}).
		Where("user_id = ? AND purpose = ? AND used_at IS NULL", userID, purpose).
		Update("used_at", now).Error; err != nil {
		return "", err
	}
	if err := tx.Create(&model.AuthToken{
		UserID: userID, Purpose: purpose, TokenHash: token.Hash(raw),
		Payload: payload, ExpiresAt: now.Add(ttl),
	}).Error; err != nil {
		return "", err
	}
	return raw, nil
}

// RequestChangeEmail 校验密码后向新邮箱发确认链接（token payload=新邮箱）。
func (s *Service) RequestChangeEmail(userID uint64, password, newEmail string) error {
	newEmail = strings.ToLower(strings.TrimSpace(newEmail))
	if !emailRe.MatchString(newEmail) {
		return ErrInvalidInput
	}
	var u model.User
	if err := s.db.First(&u, userID).Error; err != nil {
		return err
	}
	if !VerifyPassword(u.PasswordHash, password) {
		return ErrWrongPassword
	}
	if strings.EqualFold(u.Email, newEmail) {
		return ErrInvalidInput
	}
	var n int64
	if err := s.db.Model(&model.User{}).Where("email = ? AND id <> ?", newEmail, userID).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return ErrEmailTaken
	}
	if s.Mailer == nil {
		return nil // 无邮件能力时静默（与 verify 一致尽量不阻断；也可返回）
	}
	var raw string
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var e error
		raw, e = s.createAuthTokenPayload(tx, userID, "change_email", newEmail, resetTokenTTL)
		return e
	})
	if err != nil {
		return err
	}
	link := strings.TrimRight(s.BaseURL, "/") + "/confirm-email?token=" + raw
	lang := u.Preferences.Lang
	go func() {
		if err := s.Mailer.SendChangeEmail(newEmail, link, lang); err != nil {
			slog.Warn("change-email 发送失败", "err", err)
		}
	}()
	return nil
}

// ConfirmChangeEmail 核销 change_email 令牌，更新邮箱、标记已验证并吊销全部 session。
func (s *Service) ConfirmChangeEmail(rawToken string) error {
	var at model.AuthToken
	if err := s.db.Where("token_hash = ? AND purpose = ? AND used_at IS NULL AND expires_at > ?",
		token.Hash(strings.TrimSpace(rawToken)), "change_email", time.Now()).First(&at).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrTokenInvalid
		}
		return err
	}
	newEmail := strings.ToLower(strings.TrimSpace(at.Payload))
	if newEmail == "" {
		return ErrTokenInvalid
	}
	now := time.Now()
	return s.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.AuthToken{}).
			Where("id = ? AND used_at IS NULL AND expires_at > ?", at.ID, now).
			Update("used_at", now)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrTokenInvalid
		}
		var n int64
		if err := tx.Model(&model.User{}).Where("email = ? AND id <> ?", newEmail, at.UserID).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return ErrEmailTaken
		}
		if err := tx.Model(&model.User{}).Where("id = ?", at.UserID).Updates(map[string]any{
			"email": newEmail, "email_verified_at": now,
		}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Session{}, "user_id = ?", at.UserID).Error
	})
}

// sendWelcomeEmail 注册后可选欢迎信：welcome_email=false 或 Mailer 空时跳过；SMTP 未配仅 warn 吞掉。
func (s *Service) sendWelcomeEmail(u *model.User) {
	if s.Mailer == nil || u == nil || strings.TrimSpace(u.Email) == "" {
		return
	}
	if !s.st.GetBool(model.SettingWelcomeEmail, true) {
		return
	}
	email := u.Email
	lang := u.Preferences.Lang
	base := s.BaseURL
	go func() {
		if err := s.Mailer.SendWelcome(email, base, lang); err != nil {
			if err.Error() == "mail: SMTP 未配置" {
				return
			}
			slog.Warn("welcome-email 发送失败", "err", err)
		}
	}()
}

// sendVerifyEmail 同步建 24h 验证令牌、异步发送(失败仅日志)。Mailer 为空整体跳过。
func (s *Service) sendVerifyEmail(u *model.User) {
	if s.Mailer == nil {
		return
	}
	var raw string
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		raw, err = s.createAuthToken(tx, u.ID, "verify_email", verifyTokenTTL)
		return err
	})
	if err != nil {
		slog.Warn("verify-email 建令牌失败", "err", err)
		return
	}
	link := strings.TrimRight(s.BaseURL, "/") + "/verify-email?token=" + raw
	email := u.Email
	lang := u.Preferences.Lang
	go func() {
		if err := s.Mailer.SendVerifyEmail(email, link, lang); err != nil {
			slog.Warn("verify-email 发送失败", "err", err)
		}
	}()
}

// VerifyEmail 核销验证令牌并标记邮箱已验证。
func (s *Service) VerifyEmail(rawToken string) error {
	var at model.AuthToken
	if err := s.db.Where("token_hash = ? AND purpose = ? AND used_at IS NULL AND expires_at > ?",
		token.Hash(strings.TrimSpace(rawToken)), "verify_email", time.Now()).First(&at).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrTokenInvalid
		}
		return err
	}
	now := time.Now()
	return s.db.Transaction(func(tx *gorm.DB) error {
		// 原子过期/已用复查(Task 3 终态语义,verify 仅核销单条)
		res := tx.Model(&model.AuthToken{}).
			Where("id = ? AND used_at IS NULL AND expires_at > ?", at.ID, now).
			Update("used_at", now)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrTokenInvalid
		}
		return tx.Model(&model.User{}).Where("id = ?", at.UserID).Update("email_verified_at", now).Error
	})
}

// ResendVerification 重发验证邮件(已验证拒绝)。
func (s *Service) ResendVerification(userID uint64) error {
	var u model.User
	if err := s.db.First(&u, userID).Error; err != nil {
		return err
	}
	if u.EmailVerifiedAt != nil {
		return ErrAlreadyVerified
	}
	s.sendVerifyEmail(&u)
	return nil
}

// ForgotPassword 忘记密码:恒返回 nil(防枚举——存在与否、发送成败对调用方不可分辨,
// 内部问题仅 slog.Warn)。邮箱存在则建 1h reset 令牌并发邮件。
func (s *Service) ForgotPassword(email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	var u model.User
	if err := s.db.Where("email = ?", email).First(&u).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			slog.Warn("forgot-password 查库失败", "err", err)
		}
		return nil
	}
	var raw string
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		raw, err = s.createAuthToken(tx, u.ID, "reset_password", resetTokenTTL)
		return err
	})
	if err != nil {
		slog.Warn("forgot-password 建令牌失败", "err", err)
		return nil
	}
	if s.Mailer == nil {
		slog.Warn("forgot-password 未配置 Mailer,跳过发送", "user", u.ID)
		return nil
	}
	// 异步发送,避免 SMTP I/O 时延成为邮箱存在性旁路。
	link := strings.TrimRight(s.BaseURL, "/") + "/reset-password?token=" + raw
	to := u.Email
	lang := u.Preferences.Lang // 空串 → mail 默认中文;用户不存在时本路径不达
	go func() {
		if err := s.Mailer.SendResetPassword(to, link, lang); err != nil {
			slog.Warn("forgot-password 发送失败", "err", err)
		}
	}()
	return nil
}

// ResetPasswordByToken 凭一次性令牌改密:成功事务内改密+作废该用户全部未用未过期 reset 令牌+吊销全部 session。
func (s *Service) ResetPasswordByToken(rawToken, newPassword string) error {
	if !validPassword(newPassword) {
		return ErrWeakPassword
	}
	var at model.AuthToken
	if err := s.db.Where("token_hash = ? AND purpose = ? AND used_at IS NULL AND expires_at > ?",
		token.Hash(strings.TrimSpace(rawToken)), "reset_password", time.Now()).First(&at).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrTokenInvalid
		}
		return err
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	now := time.Now()
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.User{}).Where("id = ?", at.UserID).Update("password_hash", hash).Error; err != nil {
			return err
		}
		// 作废该用户全部未用且未过期的 reset 令牌(含本次所用),堵住并发 forgot 遗留危害面。
		// expires_at 用新鲜 time.Now()，避免外层 now 与事务执行间时钟窗口放行刚过期令牌。
		res := tx.Model(&model.AuthToken{}).
			Where("user_id = ? AND purpose = ? AND used_at IS NULL AND expires_at > ?", at.UserID, "reset_password", time.Now()).
			Update("used_at", now)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrTokenInvalid // 与并发使用/过期赛跑失败
		}
		return tx.Delete(&model.Session{}, "user_id = ?", at.UserID).Error
	})
}

// QuotaInfo 配额与上传限制（/user/quota 响应所需的全部字段）。
type QuotaInfo struct {
	Used            int64
	Total           int64
	MaxFileSize     int64
	AllowedExts     []string
	BandwidthUsed   int64  // 本月出站已用（字节，账期见 BandwidthPeriod）
	BandwidthQuota  int64  // 组月硬顶；0=不限
	BandwidthPeriod string // YYYY-MM Asia/Shanghai
	// 组级上传选项 / 生命周期（0=不限或关闭，语义同 UserGroup 字段）。
	DefaultExpiresIn int
	MaxExpiresIn     int
	DefaultMaxViews  int
	MaxMaxViews      int
	RetentionDays    int
	ForceMaxAgeDays  int
}

// QuotaInfo 返回用户已用/总配额与所属组的上传限制。
func (s *Service) QuotaInfo(userID uint64) (*QuotaInfo, error) {
	var u model.User
	if err := s.db.First(&u, userID).Error; err != nil {
		return nil, err
	}
	var g model.UserGroup
	if err := s.db.First(&g, u.GroupID).Error; err != nil {
		return nil, err
	}
	period := bandwidth.CurrentPeriod()
	return &QuotaInfo{
		Used: u.UsedStorage, Total: g.StorageQuota,
		MaxFileSize: g.MaxFileSize, AllowedExts: g.AllowedExts,
		BandwidthUsed:    bandwidth.EffectiveUsed(&u, period),
		BandwidthQuota:   g.BandwidthQuotaMonth,
		BandwidthPeriod:  period,
		DefaultExpiresIn: g.DefaultExpiresIn, MaxExpiresIn: g.MaxExpiresIn,
		DefaultMaxViews: g.DefaultMaxViews, MaxMaxViews: g.MaxMaxViews,
		RetentionDays: g.RetentionDays, ForceMaxAgeDays: g.ForceMaxAgeDays,
	}, nil
}

// Quota 返回已用/总配额（字节）。保留旧签名，转调 QuotaInfo。
func (s *Service) Quota(userID uint64) (used, total int64, err error) {
	qi, err := s.QuotaInfo(userID)
	if err != nil {
		return 0, 0, err
	}
	return qi.Used, qi.Total, nil
}
