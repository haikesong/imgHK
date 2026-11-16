package upload

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/yixian-huang/imgli/internal/imaging"
	"github.com/yixian-huang/imgli/internal/model"
	"github.com/yixian-huang/imgli/internal/service/settings"
)

// burn 就地处理临时文件：单次解码 → 缩放/水印 → 末次编码（keep 或 webp）。
// 仅 jpg/jpeg/png；gif/webp 等原样跳过（含 output_format=webp 时亦不转）。
// settings 仅「键不存在」按默认降级,其余读取错误上抛中止。
// 处理链 ErrUnsupported → 整链放弃、原字节不动、记 slog.Warn。
//
// 只开 strip_exif 时走容器级无损剥离（不解码像素），大图也不会因重编码超时；
// 需要缩放/水印/转 WebP 时仍走解码管线，并对超过 MaxProcessPixels 的图整链跳过。
func (s *Service) burn(tmpPath string, meta imaging.Meta, u *model.User) (bool, error) {
	switch strings.ToLower(meta.Ext) {
	case "jpg", "jpeg", "png":
	default:
		return false, nil
	}
	var proc Processing
	if err := s.st.Get(model.SettingProcessing, &proc); err != nil {
		if !errors.Is(err, settings.ErrNotFound) {
			return false, err
		}
		proc = DefaultProcessing()
	}
	// 用户图片水印可用性:登录用户 + 偏好开启 + 目录已装配 + 文件存在
	var markData []byte
	var markPos string
	var markOp float64
	var markMargin int
	if u != nil && u.Preferences.Watermark.Enabled && s.WatermarkDir != "" {
		if b, err := os.ReadFile(filepath.Join(s.WatermarkDir, fmt.Sprintf("%d.png", u.ID))); err == nil {
			markData = b
			w := u.Preferences.Watermark
			markPos = w.Position
			if markPos == "" {
				markPos = "br"
			}
			markOp = w.Opacity
			if markOp == 0 {
				markOp = 0.5
			}
			markMargin = w.Margin
		}
	}
	textOn := proc.TextWatermark.Enabled && strings.TrimSpace(proc.TextWatermark.Text) != ""
	stripOn := proc.StripExifEnabled()
	wantWebP := proc.EffectiveOutputFormat() == OutputWebP
	if wantWebP && !imaging.WebPEncodeAvailable() {
		// 配置层应已拒绝；运行时无编码器则降级 keep，避免上传失败
		slog.Warn("处理管线 WebP 编码不可用，跳过格式转换")
		wantWebP = false
	}
	if proc.MaxEdge == 0 && !textOn && markData == nil && !stripOn && !wantWebP {
		return false, nil
	}

	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return false, err
	}

	// 只开 strip_exif：容器级无损剥离，像素流逐字节保留。
	// 旧的「解码→重编码」路径在 1 亿像素级 PNG 上要跑数分钟，会先撞上
	// Cloudflare 100 秒源站超时（用户看到 524 + 「响应格式错误」）。
	if proc.MaxEdge == 0 && !textOn && markData == nil && !wantWebP {
		out, changed, serr := imaging.StripMetadataLossless(data)
		if serr == nil {
			if !changed {
				return false, nil
			}
			if err := os.WriteFile(tmpPath, out, 0o600); err != nil {
				return false, err
			}
			return true, nil
		}
		// 容器解析失败：退回下面的重编码路径（仍会去掉元数据）。
		slog.Warn("容器级元数据剥离失败，退回重编码", "err", serr)
	}

	// 解码型处理链的内存峰值和时间随像素数线性增长。超大图整链跳过，
	// 原字节保留：宁可少做缩放/水印，也不要 OOM 或让上传超时。
	if px := int64(meta.Width) * int64(meta.Height); px > MaxProcessPixels {
		slog.Warn("图片处理跳过：像素超过上限",
			"width", meta.Width, "height", meta.Height, "pixels", px, "limit", MaxProcessPixels)
		return false, nil
	}

	res, err := imaging.ProcessPipeline(data, imaging.PipelineOpts{
		MaxEdge:       proc.MaxEdge,
		JpegQuality:   proc.EffectiveJPEGQuality(),
		OutputWebP:    wantWebP,
		WebPQuality:   proc.EffectiveWebPQuality(),
		SkipIfLarger:  proc.WebPSkipIfLargerEnabled(),
		ForceReencode: stripOn,
		TextEnabled:   textOn,
		Text:          strings.TrimSpace(proc.TextWatermark.Text),
		TextPosition:  proc.TextWatermark.Position,
		TextOpacity:   proc.TextWatermark.Opacity,
		TextSizeRatio: proc.TextWatermark.SizeRatio,
		MarkPNG:       markData,
		MarkPosition:  markPos,
		MarkOpacity:   markOp,
		MarkMargin:    markMargin,
	})
	if errors.Is(err, imaging.ErrUnsupported) {
		slog.Warn("处理管线整链跳过(内容不可解码)", "step", "pipeline")
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !res.Changed || bytes.Equal(res.Data, data) {
		return false, nil
	}
	if err := os.WriteFile(tmpPath, res.Data, 0o600); err != nil {
		return false, err
	}
	return true, nil
}

// enqueueModerate 投递机审任务。是否 enabled 仍在任务执行时判断；此处按
// login_sample_rate 做登录用户抽检（游客全审）。runner 可能为 nil（单测）。
