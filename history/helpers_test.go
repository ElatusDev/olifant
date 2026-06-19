package history

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBoolStr(t *testing.T) {
	if boolStr(true) != "true" || boolStr(false) != "false" {
		t.Errorf("boolStr = %q/%q", boolStr(true), boolStr(false))
	}
}

func TestIntToStr(t *testing.T) {
	cases := map[int]string{0: "0", 7: "7", 42: "42", -5: "-5", 1000: "1000"}
	for in, want := range cases {
		if got := intToStr(in); got != want {
			t.Errorf("intToStr(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestCapChars(t *testing.T) {
	if got := capChars("short", 100); got != "short" {
		t.Errorf("under cap = %q", got)
	}
	if got := capChars("abcdef", 3); got != "abc" {
		t.Errorf("ascii cap = %q", got)
	}
	if got := capChars("aé", 2); got != "a" { // back off partial rune
		t.Errorf("multibyte cap = %q, want a", got)
	}
}

func TestCapBytes(t *testing.T) {
	if got := capBytes("tiny", 100); got != "tiny" {
		t.Errorf("under cap = %q", got)
	}
	out := capBytes("line1\nline2\nline3\n", 8)
	if !strings.Contains(out, "truncated") {
		t.Errorf("over cap should annotate truncation: %q", out)
	}
	if !strings.HasPrefix(out, "line1") {
		t.Errorf("should keep line-aligned prefix: %q", out)
	}
}

func TestLoadManifest_MissingReturnsEmpty(t *testing.T) {
	m, err := LoadManifest(filepath.Join(t.TempDir(), "none.yaml"))
	if err != nil {
		t.Fatalf("missing manifest should be (empty,nil), got err %v", err)
	}
	if m == nil || m.BuilderVersion == "" {
		t.Errorf("missing manifest = %+v, want defaulted BuilderVersion", m)
	}
	if m.LastSHA("anything") != "" {
		t.Error("fresh manifest should have no last_sha")
	}
}
