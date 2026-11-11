package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/yixian-huang/imgli/internal/model"
)

// 登录失败节流策略。账号与 IP 分开计数：
//   - 账号：5 次失败后锁 1 分钟，之后每次再失败翻倍，最多 15 分钟；
//   - IP：10 分钟内累计 30 次失败后锁 5 分钟，之后翻倍，最多 30 分钟。
//
// 两者都是临时锁定，窗口过期或登录成功即清零 —— 不设置永久锁定，
// 避免攻击者故意打错密码把真实用户永久关在门外。
var (
	loginAccountPolicy = loginThrottlePolicy{
		threshold: 5,
		window:    15 * time.Minute,
		baseLock:  time.Minute,
		maxLock:   15 * time.Minute,
	}
	loginIPPolicy = loginThrottlePolicy{
		threshold: 30,
		window:    10 * time.Minute,
		baseLock:  5 * time.Minute,
		maxLock:   30 * time.Minute,
	}
)

// loginThrottlePolicy 单个维度的固定窗口 + 指数退避参数。
type loginThrottlePolicy struct {
	threshold int
	window    time.Duration
	baseLock  time.Duration
	maxLock   time.Duration
}

// LoginThrottledError 同时实现 errors.Is(ErrLoginThrottled) 与 Retry-After 所需时长。
type LoginThrottledError struct {
	RetryAfter time.Duration
}

func (e *LoginThrottledError) Error() string { return ErrLoginThrottled.Error() }
func (e *LoginThrottledError) Unwrap() error { return ErrLoginThrottled }

// loginThrottleKey 把作用域和值收成 SHA-256；表里不落明文账号/IP。
func loginThrottleKey(scope, value string) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + value))
	return hex.EncodeToString(sum[:])
}

// loginAccountKey 用于尚未解析到用户 ID 的账号（不存在或首次失败）。
func loginAccountKey(account string) string {
	account = strings.ToLower(strings.TrimSpace(account))
	if account == "" {
		return ""
	}
	return loginThrottleKey("account", "input:"+account)
}

// loginAccountIDKey 用于已存在用户；用户名和邮箱会归一到同一用户 ID，
// 避免攻击者在两种登录标识之间来回切换绕过账号级计数。
func loginAccountIDKey(userID uint64) string {
	return loginThrottleKey("account", "id:"+strconv.FormatUint(userID, 10))
}

func loginIPThrottleKey(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ""
	}
	return loginThrottleKey("ip", ip)
}

// lockedThrottleRetry 返回 keys 中最长的剩余锁定时间；未锁定时返回 false。
// 查询失败时按未锁定处理：登录主流程马上还要查用户，若数据库真有问题会在那里报错，
// 不应让节流表故障把所有人挡在门外。
func (s *Service) lockedThrottleRetry(keys []string, now time.Time) (time.Duration, bool) {
	s.throttleMu.Lock()
	defer s.throttleMu.Unlock()

	var retry time.Duration
	for _, key := range keys {
		if key == "" {
			continue
		}
		var row model.LoginThrottle
		if err := s.db.First(&row, "key = ?", key).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				slog.Warn("登录节流查询失败", "err", err)
			}
			continue
		}
		if now.Before(row.LockedUntil) {
			if d := row.LockedUntil.Sub(now); d > retry {
				retry = d
			}
		}
	}
	return retry, retry > 0
}

// recordLoginFailure 记录一次失败；若本次触发锁定，返回锁定时长。
func (s *Service) recordLoginFailure(accountKey, ipKey string, now time.Time) time.Duration {
	s.throttleMu.Lock()
	defer s.throttleMu.Unlock()

	s.maybeSweepLoginThrottles(now)

	var retry time.Duration
	for _, item := range []struct {
		key    string
		scope  string
		policy loginThrottlePolicy
	}{
		{accountKey, "account", loginAccountPolicy},
		{ipKey, "ip", loginIPPolicy},
	} {
		if item.key == "" {
			continue
		}
		if d := s.recordOneLoginFailure(item.key, item.scope, item.policy, now); d > retry {
			retry = d
		}
	}
	return retry
}

func (s *Service) recordOneLoginFailure(key, scope string, policy loginThrottlePolicy, now time.Time) time.Duration {
	var row model.LoginThrottle
	err := s.db.First(&row, "key = ?", key).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		row = model.LoginThrottle{Key: key, Scope: scope, WindowStart: now}
	case err != nil:
		slog.Warn("登录节流记录失败", "err", err)
		return 0
	}

	if row.WindowStart.IsZero() || now.Sub(row.WindowStart) > policy.window {
		row.Failures = 0
		row.WindowStart = now
		row.LockedUntil = time.Time{}
	}
	row.Failures++
	row.UpdatedAt = now

	if row.Failures >= policy.threshold {
		over := row.Failures - policy.threshold
		lock := policy.baseLock
		for i := 0; i < over && lock < policy.maxLock; i++ {
			lock *= 2
		}
		if lock > policy.maxLock {
			lock = policy.maxLock
		}
		row.LockedUntil = now.Add(lock)
		if err := s.db.Save(&row).Error; err != nil {
			slog.Warn("登录节流保存失败", "err", err)
			return 0
		}
		return lock
	}

	if err := s.db.Save(&row).Error; err != nil {
		slog.Warn("登录节流保存失败", "err", err)
	}
	return 0
}

// clearLoginThrottle 在登录成功后清空相关计数（账号、历史输入标识、IP）。
func (s *Service) clearLoginThrottle(keys ...string) {
	s.throttleMu.Lock()
	defer s.throttleMu.Unlock()

	var clean []string
	for _, key := range keys {
		if key != "" {
			clean = append(clean, key)
		}
	}
	if len(clean) == 0 {
		return
	}
	if err := s.db.Where("key IN ?", clean).Delete(&model.LoginThrottle{}).Error; err != nil {
		slog.Warn("登录节流清理失败", "err", err)
	}
}

// maybeSweepLoginThrottles 至多每小时清一次 7 天未更新的行，避免失败账号表无限增长。
// 调用方必须持有 throttleMu。
func (s *Service) maybeSweepLoginThrottles(now time.Time) {
	if !s.lastThrottleSweep.IsZero() && now.Sub(s.lastThrottleSweep) < time.Hour {
		return
	}
	s.lastThrottleSweep = now
	if err := s.db.Where("updated_at < ?", now.Add(-7*24*time.Hour)).
		Delete(&model.LoginThrottle{}).Error; err != nil {
		slog.Warn("登录节流清扫失败", "err", err)
	}
}
