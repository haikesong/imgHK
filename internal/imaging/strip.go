package imaging

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// ErrStripUnsupported 容器级元数据剥离不认识该格式（不是 JPEG/PNG）。
// 调用方可退回解码重编码路径；不能当成「图片损坏」。
var ErrStripUnsupported = errors.New("imaging: metadata strip unsupported")

// StripMetadataLossless 只重写容器、不重编码像素，去掉携带隐私的元数据：
//
//	JPEG: APP1(Exif/XMP)、APP12、APP13(IPTC/Photoshop)、COM
//	PNG:  eXIf、tEXt、zTXt、iTXt、tIME
//
// 像素流（JPEG 的 SOS 之后、PNG 的 IDAT）逐字节拷贝，所以 1 亿像素的大图
// 也只花一次内存拷贝的时间。ICC/Adobe/色彩相关段与块一律保留，避免偏色。
//
// 返回值 changed=false 表示容器里本来就没有可丢的元数据，此时原样返回 data。
// 解析失败返回包装后的 ErrStripUnsupported（调用方可降级为重编码）。
func StripMetadataLossless(data []byte) (out []byte, changed bool, err error) {
	switch {
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8:
		return stripJPEG(data)
	case bytes.HasPrefix(data, pngSignature):
		return stripPNG(data)
	default:
		return nil, false, ErrStripUnsupported
	}
}

var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}

// jpegMetadataMarker 返回该 marker 是否只携带元数据、可以整段丢弃。
// 只丢隐私相关的 APP1/12/13 与 COM；APP0(JFIF)、APP2(ICC)、APP14(Adobe)
// 影响显示或色彩解释，保留。
func jpegMetadataMarker(m byte) bool {
	switch m {
	case 0xE1, 0xEC, 0xED, 0xFE:
		return true
	}
	return false
}

// jpegNextMarker 从 i 开始找下一个真正的 marker 起点。
// 熵编码段里 0xFF00 是字节填充、0xFFD0-D7 是 RSTn，都不算 marker；
// 0xFFFF 是 marker 前的填充字节，跳过。
func jpegNextMarker(data []byte, i int) int {
	for i < len(data)-1 {
		if data[i] != 0xFF {
			i++
			continue
		}
		b := data[i+1]
		switch {
		case b == 0xFF: // marker 前的填充 FF，只前进一个字节，别把真 marker 吞掉
			i++
			continue
		case b == 0x00, b >= 0xD0 && b <= 0xD7: // 熵编码填充 / RSTn
			i += 2
			continue
		default:
			return i
		}
	}
	return len(data)
}

func stripJPEG(data []byte) ([]byte, bool, error) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, false, ErrStripUnsupported
	}
	out := make([]byte, 0, len(data))
	out = append(out, 0xFF, 0xD8)
	changed := false
	i := 2
	for i < len(data) {
		if data[i] != 0xFF {
			// 非熵编码区出现裸数据：原样拷贝到下一个 marker，宽松处理。
			j := jpegNextMarker(data, i)
			out = append(out, data[i:j]...)
			i = j
			continue
		}
		for i+1 < len(data) && data[i+1] == 0xFF { // fill bytes
			i++
		}
		if i+1 >= len(data) {
			return nil, false, ErrStripUnsupported
		}
		m := data[i+1]
		switch {
		case m == 0x01 || (m >= 0xD0 && m <= 0xD7):
			out = append(out, 0xFF, m)
			i += 2
		case m == 0xD9: // EOI
			out = append(out, 0xFF, 0xD9)
			i += 2
			if i < len(data) { // EOI 之后的尾随字节（部分相机/工具会写）
				out = append(out, data[i:]...)
			}
			return finishJPEG(out, data, changed)
		case m == 0xD8: // 多余的 SOI
			out = append(out, 0xFF, 0xD8)
			i += 2
		default:
			if i+4 > len(data) {
				return nil, false, ErrStripUnsupported
			}
			ln := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
			if ln < 2 || i+2+ln > len(data) {
				return nil, false, ErrStripUnsupported
			}
			end := i + 2 + ln
			if m == 0xDA { // SOS：段头之后是熵编码数据
				out = append(out, data[i:end]...)
				j := jpegNextMarker(data, end)
				out = append(out, data[end:j]...)
				i = j
				continue
			}
			if jpegMetadataMarker(m) {
				changed = true
			} else {
				out = append(out, data[i:end]...)
			}
			i = end
		}
	}
	// 没读到 EOI：容器不完整，交给调用方降级处理。
	return nil, false, ErrStripUnsupported
}

func finishJPEG(out, data []byte, changed bool) ([]byte, bool, error) {
	if !changed {
		return data, false, nil
	}
	return out, true, nil
}

// pngMetadataChunk 返回该 chunk 是否只携带元数据、可以整块丢弃。
// 列出的都是文本/时间/EXIF 块；色彩与透明相关块（iCCP/sRGB/gAMA/cHRM/tRNS 等）保留。
func pngMetadataChunk(typ string) bool {
	switch typ {
	case "eXIf", "tEXt", "zTXt", "iTXt", "tIME":
		return true
	}
	return false
}

func stripPNG(data []byte) ([]byte, bool, error) {
	if len(data) < len(pngSignature) || !bytes.HasPrefix(data, pngSignature) {
		return nil, false, ErrStripUnsupported
	}
	out := make([]byte, 0, len(data))
	out = append(out, pngSignature...)
	changed := false
	i := len(pngSignature)
	for i+8 <= len(data) {
		ln := int(binary.BigEndian.Uint32(data[i : i+4]))
		typ := string(data[i+4 : i+8])
		end := i + 8 + ln + 4
		if ln < 0 || end > len(data) {
			return nil, false, ErrStripUnsupported
		}
		if pngMetadataChunk(typ) {
			changed = true
		} else {
			out = append(out, data[i:end]...)
		}
		i = end
		if typ == "IEND" {
			if i < len(data) {
				out = append(out, data[i:]...)
			}
			if !changed {
				return data, false, nil
			}
			return out, true, nil
		}
	}
	return nil, false, ErrStripUnsupported
}
