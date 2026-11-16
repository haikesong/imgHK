//go:build !vips

package imaging

// 纯 Go 解码缩略图没有 shrink-on-load：源图先整体进内存再缩放，必须保守。
// 16MP ≈ RGBA 64MiB 峰值；压缩体 24MiB 覆盖常见截图，拒绝巨型 GIF/PNG 炸弹。
const (
	defaultThumbMaxBytes  = 24 << 20
	defaultThumbMaxPixels = 16_000_000
)
