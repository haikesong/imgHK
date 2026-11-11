package imaging

import (
	"testing"

	"golang.org/x/image/font/sfnt"
)

// TestSubsetFontCoversWatermarkSamples 子集字体须含测试/品牌水印用字，避免 silent tofu。
func TestSubsetFontCoversWatermarkSamples(t *testing.T) {
	face, err := textFace(24)
	if err != nil {
		t.Fatal(err)
	}
	_ = face
	if parsedFont == nil {
		t.Fatal("parsedFont nil")
	}
	samples := []string{
		"HKSimg",
		"图床",
		"白栗©2026",
		"白栗测试水印一段较长的文字内容",
		"ABCDEFGxyz 0123456789",
		"「图床」·—…",
	}
	var buf sfnt.Buffer
	for _, s := range samples {
		for _, r := range s {
			if r == ' ' {
				continue
			}
			gid, err := parsedFont.GlyphIndex(&buf, r)
			if err != nil {
				t.Fatalf("%q U+%04X: %v", s, r, err)
			}
			if gid == 0 {
				t.Errorf("%q 缺字形 U+%04X %c（更新 fonts/charset.txt 后跑 scripts/subset-watermark-font.sh）", s, r, r)
			}
		}
		if miss := MissingWatermarkRunes(s); len(miss) != 0 {
			t.Errorf("MissingWatermarkRunes(%q)=%q want empty", s, string(miss))
		}
	}
}

func TestMissingWatermarkRunesFindsUnsupported(t *testing.T) {
	// 古彝文音节（U+A000 段）不在 Noto SC 子集内；用冷门字符探测 notdef。
	miss := MissingWatermarkRunes("测试\uA000水印")
	if len(miss) == 0 {
		t.Fatal("应报告未覆盖字符")
	}
	found := false
	for _, r := range miss {
		if r == '\uA000' {
			found = true
		}
	}
	if !found {
		t.Errorf("miss=%q 应含 U+A000", string(miss))
	}
}

// TestEmbeddedFontMuchSmallerThanFullNoto 回归：子集体积应远小于完整 SC(~8MB)。
func TestEmbeddedFontMuchSmallerThanFullNoto(t *testing.T) {
	n := len(notoSansSC)
	if n < 100_000 {
		t.Fatalf("embed 字体过小(%d)，可能损坏", n)
	}
	// 完整 NotoSansSC-Regular.otf 约 8MB；子集目标 < 2MB
	if n > 2_000_000 {
		t.Fatalf("embed 字体 %d bytes 过大：应使用 subset（见 fonts/README.md）", n)
	}
}
