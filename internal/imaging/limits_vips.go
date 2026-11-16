//go:build vips

package imaging

// libvips 的 vips_thumbnail_* 是流式、按需解码 + 磁盘后备，
// 内存峰值不再随像素数线性增长，所以上限按「上传硬上限 256MiB、
// 常见 1 亿像素级大图」放宽。并发仍由 IMGLI_THUMB_CONCURRENCY 限制（默认 1）。
//
// 这两项只约束缩略图/探测；原始上传的大小上限在 server.maxUploadBytes。
const (
	defaultThumbMaxBytes  = 256 << 20   // 256 MiB
	defaultThumbMaxPixels = 400_000_000 // 4 亿像素（约 20000×20000）
)
