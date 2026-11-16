package imaging

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"testing"
)

// pngChunks 按顺序返回 PNG 的 (type, payload)（不含签名）。
func pngChunks(t *testing.T, data []byte) [][2][]byte {
	t.Helper()
	if !bytes.HasPrefix(data, pngSignature) {
		t.Fatal("not png")
	}
	var out [][2][]byte
	for i := len(pngSignature); i+12 <= len(data); {
		n := int(binary.BigEndian.Uint32(data[i : i+4]))
		typ := data[i+4 : i+8]
		if i+12+n > len(data) {
			t.Fatalf("truncated chunk at %d", i)
		}
		out = append(out, [2][]byte{typ, data[i+8 : i+8+n]})
		i += 12 + n
	}
	return out
}

func insertPNGChunk(t *testing.T, src []byte, typ string, payload []byte) []byte {
	t.Helper()
	// 插在 IHDR 之后（PNG 要求 IHDR 第一）
	ihdrEnd := len(pngSignature) + 12 + int(binary.BigEndian.Uint32(src[len(pngSignature):]))
	chunk := make([]byte, 0, 12+len(payload))
	var lb [4]byte
	binary.BigEndian.PutUint32(lb[:], uint32(len(payload)))
	chunk = append(chunk, lb[:]...)
	chunk = append(chunk, typ...)
	chunk = append(chunk, payload...)
	crc := crc32.NewIEEE()
	crc.Write([]byte(typ))
	crc.Write(payload)
	binary.BigEndian.PutUint32(lb[:], crc.Sum32())
	chunk = append(chunk, lb[:]...)

	out := make([]byte, 0, len(src)+len(chunk))
	out = append(out, src[:ihdrEnd]...)
	out = append(out, chunk...)
	out = append(out, src[ihdrEnd:]...)
	return out
}

func idatOf(chunks [][2][]byte) []byte {
	var out []byte
	for _, c := range chunks {
		if string(c[0]) == "IDAT" {
			out = append(out, c[1]...)
		}
	}
	return out
}

func TestStripMetadataLosslessPNGDropsTextKeepsPixels(t *testing.T) {
	base := solidPNG(t, 16, 12, color.White)
	withText := insertPNGChunk(t, base, "tEXt", []byte("Comment\x00gps:secret"))
	if !bytes.Contains(withText, []byte("secret")) {
		t.Fatal("fixture missing tEXt payload")
	}

	out, changed, err := StripMetadataLossless(withText)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if bytes.Contains(out, []byte("secret")) {
		t.Error("tEXt 未剥离")
	}
	if !bytes.Equal(idatOf(pngChunks(t, base)), idatOf(pngChunks(t, out))) {
		t.Error("IDAT 像素流被改动，应逐字节保留")
	}
	if _, format, err := image.Decode(bytes.NewReader(out)); err != nil || format != "png" {
		t.Fatalf("输出应可解码: %v %s", err, format)
	}

	// 再剥一次：没有元数据时 changed=false 且字节不变。
	same, changed2, err := StripMetadataLossless(out)
	if err != nil || changed2 || !bytes.Equal(same, out) {
		t.Fatalf("无元数据时应原样返回: changed=%v err=%v", changed2, err)
	}
}

func TestStripMetadataLosslessJPEGDropsAPP1KeepsScan(t *testing.T) {
	base := encodeJPEG(t, 64, 48)
	payload := []byte("Exif\x00\x00GPSFAKE")
	app1 := []byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}
	app1 = append(app1, payload...)
	withExif := append([]byte{0xFF, 0xD8}, app1...)
	withExif = append(withExif, base[2:]...)

	out, changed, err := StripMetadataLossless(withExif)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if bytes.Contains(out, []byte("Exif\x00\x00")) {
		t.Error("APP1 未剥离")
	}
	// SOS 之后的熵编码流必须逐字节一致。
	if !bytes.Equal(jpegScan(t, base), jpegScan(t, out)) {
		t.Error("扫描数据被改动")
	}
	if _, format, err := image.Decode(bytes.NewReader(out)); err != nil || format != "jpeg" {
		t.Fatalf("输出应可解码: %v %s", err, format)
	}

	// Go 标准库编码的 JPEG 只有 JFIF APP0，没有可丢的元数据。
	if _, changed, err := StripMetadataLossless(base); err != nil || changed {
		t.Fatalf("干净 JPEG 不应变化: changed=%v err=%v", changed, err)
	}
}

// jpegScan 返回第一个 SOS marker 起的数据（含 SOS 段头）。
func jpegScan(t *testing.T, data []byte) []byte {
	t.Helper()
	for i := 0; i+1 < len(data); i++ {
		if data[i] == 0xFF && data[i+1] == 0xDA {
			return data[i:]
		}
	}
	t.Fatal("no SOS marker")
	return nil
}
