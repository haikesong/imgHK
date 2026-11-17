package upload

import (
	"errors"
	"testing"

	"github.com/yixian-huang/imgli/internal/model"
	"github.com/yixian-huang/imgli/internal/service/settings"
)

// CheckSizeAllowed 是分片上传在收第一片之前的预检：游客开关、组上限、配额、月流量。
func TestCheckSizeAllowed(t *testing.T) {
	t.Run("组上限", func(t *testing.T) {
		svc, u, _ := setup(t)
		if err := svc.db.Model(&model.UserGroup{}).Where("id = ?", 1).
			Update("max_file_size", int64(1<<20)).Error; err != nil {
			t.Fatal(err)
		}
		if err := svc.CheckSizeAllowed(u, 2<<20); !errors.Is(err, ErrFileTooLarge) {
			t.Errorf("err = %v, want ErrFileTooLarge", err)
		}
		if err := svc.CheckSizeAllowed(u, 512<<10); err != nil {
			t.Errorf("小文件应放行: %v", err)
		}
	})

	t.Run("配额", func(t *testing.T) {
		svc, u, _ := setup(t)
		if err := svc.db.Model(&model.UserGroup{}).Where("id = ?", 1).
			Updates(map[string]any{"storage_quota": int64(1 << 20), "max_file_size": int64(10 << 20)}).Error; err != nil {
			t.Fatal(err)
		}
		if err := svc.CheckSizeAllowed(u, 900<<10); err != nil {
			t.Fatalf("配额内应放行: %v", err)
		}
		if err := svc.db.Model(&model.User{}).Where("id = ?", u.ID).
			Update("used_storage", int64(512<<10)).Error; err != nil {
			t.Fatal(err)
		}
		if err := svc.CheckSizeAllowed(u, 900<<10); !errors.Is(err, ErrQuotaExceeded) {
			t.Errorf("err = %v, want ErrQuotaExceeded", err)
		}
	})

	t.Run("游客开关关闭", func(t *testing.T) {
		svc, _, _ := setup(t)
		if err := svc.CheckSizeAllowed(nil, 1<<20); !errors.Is(err, ErrGuestNotSupported) {
			t.Errorf("err = %v, want ErrGuestNotSupported", err)
		}
	})

	t.Run("游客开关打开", func(t *testing.T) {
		svc, _, _ := setup(t)
		if err := settings.New(svc.db).Set(model.SettingGuestUpload, true); err != nil {
			t.Fatal(err)
		}
		if err := svc.CheckSizeAllowed(nil, 4<<20); err != nil {
			t.Errorf("游客 4MiB（组上限 5MiB）应放行: %v", err)
		}
		if err := svc.CheckSizeAllowed(nil, 6<<20); !errors.Is(err, ErrFileTooLarge) {
			t.Errorf("err = %v, want ErrFileTooLarge", err)
		}
	})
}
