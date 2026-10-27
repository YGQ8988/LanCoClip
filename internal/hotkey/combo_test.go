package hotkey

import "testing"

func TestParseValid(t *testing.T) {
	cases := []struct {
		in   string
		want Combo
	}{
		{"meta+shift+q", Combo{Meta: true, Shift: true, Key: "q"}},
		{"cmd+shift+v", Combo{Meta: true, Shift: true, Key: "v"}},
		{"ctrl+alt+f2", Combo{Ctrl: true, Alt: true, Key: "f2"}},
		{"Ctrl + Shift + Q", Combo{Ctrl: true, Shift: true, Key: "q"}},
		{"alt+,", Combo{Alt: true, Key: ","}},
	}
	for _, c := range cases {
		got, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q) unexpected error: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("Parse(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestParseInvalid(t *testing.T) {
	cases := []string{
		"",           // 空
		"q",          // 无修饰键
		"shift+q",    // 仅 shift
		"meta",       // 缺主键
		"meta+shift", // 缺主键
		"meta+q+x",   // 两个主键
		"meta+foo",   // 不支持的主键
		"meta++q",    // 空段
	}
	for _, in := range cases {
		if _, err := Parse(in); err == nil {
			t.Fatalf("Parse(%q) should fail", in)
		}
	}
}

func TestSame(t *testing.T) {
	a, _ := Parse("cmd+shift+q")
	b, _ := Parse("meta+shift+q")
	if !Same(a, b) {
		t.Fatal("cmd and meta aliases should be the same combo")
	}
	c, _ := Parse("ctrl+shift+q")
	if Same(a, c) {
		t.Fatal("meta+shift+q vs ctrl+shift+q should differ")
	}
}
