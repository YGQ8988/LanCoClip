package lancoclip

import "testing"

func TestNormalizeTextRepairsLegacyEncoding(t *testing.T) {
	// GB18030 bytes for 中文测试, which is what pbpaste emits without a locale.
	legacy := string([]byte{0xd6, 0xd0, 0xce, 0xc4, 0xb2, 0xe2, 0xca, 0xd4})
	if got := normalizeText(legacy); got != "中文测试" {
		t.Errorf("normalizeText(GB18030) = %q, want %q", got, "中文测试")
	}

	for _, s := range []string{"中文测试：你好，世界！", "plain ascii", "", "emoji 🎉 ok"} {
		if got := normalizeText(s); got != s {
			t.Errorf("normalizeText(%q) changed valid UTF-8 to %q", s, got)
		}
	}
}
