package upload

import (
	"errors"

	"github.com/yixian-huang/imgli/internal/model"
	"github.com/yixian-huang/imgli/internal/service/bandwidth"
)

// CheckSizeAllowed 在开始接收一个（可能是几百 MB 的）文件之前做预检：
// 用户组、游客开关、单文件上限、存储配额、本月流量。
//
// Save 里的检查仍是权威判定（事务内还会再查一次配额）；这里只是让分片会话在
// 传第一片之前就失败，避免用户白传几十 MB。size 为客户端声明的原始大小，
// HEIF 之类转码后大小会变，因此最终仍以 Save 的检查为准。
func (s *Service) CheckSizeAllowed(u *model.User, size int64) error {
	if u != nil && u.ID == 0 {
		u = nil
	}
	var group model.UserGroup
	if u == nil {
		if !s.GuestEnabled() {
			return ErrGuestNotSupported
		}
		if err := s.db.Where("is_guest = ?", true).First(&group).Error; err != nil {
			return err
		}
	} else {
		if err := s.db.First(&group, u.GroupID).Error; err != nil {
			return err
		}
	}
	if group.MaxFileSize > 0 && size > group.MaxFileSize {
		return ErrFileTooLarge
	}
	if u != nil && group.StorageQuota > 0 {
		var used int64
		if err := s.db.Model(&model.User{}).Where("id = ?", u.ID).
			Select("used_storage").Scan(&used).Error; err != nil {
			return err
		}
		if used+size > group.StorageQuota {
			return ErrQuotaExceeded
		}
	}
	if u != nil {
		if err := bandwidth.Check(s.db, u.ID); errors.Is(err, bandwidth.ErrExceeded) {
			return ErrBandwidthExceeded
		} else if err != nil {
			return err
		}
	}
	return nil
}
