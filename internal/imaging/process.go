package imaging

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
)

// clampJPEGQuality 将质量夹到 [1,100]；0 或非法 → 默认 90。
func clampJPEGQuality(q int) int {
	if q < 1 || q > 100 {
		return 90
	}
	return q
}

// StripMetadata 去掉 JPEG EXIF/GPS 等附属元数据（及 PNG 文本块等），仅 jpeg/png。
//
// 先走容器级无损剥离（StripMetadataLossless）：像素流逐字节保留，1 亿像素的大图
// 也不用解码/重编码。只有容器解析失败时才退回解码+重编码的旧路径。
// 无元数据的干净图原样返回（字节不变）。
func StripMetadata(data []byte, jpegQuality int) ([]byte, error) {
	if out, _, err := StripMetadataLossless(data); err == nil {
		return out, nil
	}
	img, format, err := decodeJP(data)
	if err != nil {
		return nil, err
	}
	return encodeAs(format, img, jpegQuality)
}

// Scale 长边超过 maxEdge 时等比缩(CatmullRom);未超出时原样返回输入(不解码重编)。
// jpegQuality 仅在实际缩放并重编码时生效；0 或非法 → 90。
func Scale(data []byte, maxEdge, jpegQuality int) ([]byte, error) {
	src, format, err := decodeJP(data)
	if err != nil {
		return nil, err
	}
	scaled, did := scaleImage(src, maxEdge)
	if !did {
		return data, nil
	}
	return encodeAs(format, scaled, jpegQuality)
}

// WatermarkImage 把 PNG 水印图按九宫格 position+margin 叠加到底图,opacity∈(0,1] 经
// alpha mask 生效。mark 任一边超过底图对应边 90% 时先等比缩至 90%。mark 非 PNG → ErrUnsupported。
// jpegQuality: 0 或非法时按 90。
func WatermarkImage(data, mark []byte, position string, opacity float64, margin, jpegQuality int) ([]byte, error) {
	base, format, err := decodeJP(data)
	if err != nil {
		return nil, err
	}
	dst, err := watermarkImageImage(base, mark, position, opacity, margin)
	if err != nil {
		return nil, err
	}
	return encodeAs(format, dst, jpegQuality)
}

// WatermarkText 站点文字水印:字号 max(12, sizeRatio×长边),白字+四向 1px 黑描边
// (任意底色可读),margin=字号/2,按九宫格 position 定位(按测量宽高贴边不出界)。
// jpegQuality: 0 或非法时按 90。
func WatermarkText(data []byte, text, position string, opacity, sizeRatio float64, jpegQuality int) ([]byte, error) {
	base, format, err := decodeJP(data)
	if err != nil {
		return nil, err
	}
	dst, err := watermarkTextImage(base, text, position, opacity, sizeRatio)
	if err != nil {
		return nil, err
	}
	return encodeAs(format, dst, jpegQuality)
}

// anchor 九宫格定位:tl|tc|tr|ml|mc|mr|bl|bc|br,返回 mark 左上角坐标。
func anchor(position string, w, h, mw, mh, margin int) (x, y int) {
	switch position {
	case "tl":
		x, y = margin, margin
	case "tc":
		x, y = (w-mw)/2, margin
	case "tr":
		x, y = w-mw-margin, margin
	case "ml":
		x, y = margin, (h-mh)/2
	case "mc":
		x, y = (w-mw)/2, (h-mh)/2
	case "mr":
		x, y = w-mw-margin, (h-mh)/2
	case "bl":
		x, y = margin, h-mh-margin
	case "bc":
		x, y = (w-mw)/2, h-mh-margin
	case "br":
		x, y = w-mw-margin, h-mh-margin
	default:
		// 非法值按 br
		x, y = w-mw-margin, h-mh-margin
	}
	// 双向钳制在画布内:大 margin 不得把水印推出画布(否则处理成功却不可见;codex 终审)。
	// 先钳上界(mw>w 时 w-mw<0,随后下界拉回 0),再钳下界。
	if maxX := w - mw; x > maxX {
		x = maxX
	}
	if maxY := h - mh; y > maxY {
		y = maxY
	}
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return x, y
}

// decodeJP 仅认 jpeg/png。
func decodeJP(data []byte) (image.Image, string, error) {
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", ErrUnsupported
	}
	if format != "jpeg" && format != "png" {
		return nil, "", ErrUnsupported
	}
	return img, format, nil
}

// encodeAs 按 format 重编码。jpegQuality 仅 JPEG 生效；0/非法 → 90。
func encodeAs(format string, img image.Image, jpegQuality int) ([]byte, error) {
	var buf bytes.Buffer
	switch format {
	case "jpeg":
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: clampJPEGQuality(jpegQuality)}); err != nil {
			return nil, err
		}
	case "png":
		if err := png.Encode(&buf, img); err != nil {
			return nil, err
		}
	default:
		return nil, ErrUnsupported
	}
	return buf.Bytes(), nil
}
