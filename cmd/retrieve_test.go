package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInferScopes(t *testing.T) {
	root := "/plat"
	cases := []struct {
		cwd  string
		want []string
	}{
		{"/plat/platform-core-api/security", []string{"backend", "universal"}},
		{"/plat/AkademiaPlusWebApp", []string{"webapp", "universal"}},
		{"/plat/akademia-plus-go/src", []string{"mobile", "universal"}},
		{"/plat/elatusdev-infra", []string{"infra", "universal"}},
		{"/plat/core-api-e2e", []string{"e2e", "universal"}},
		{"/plat", nil},        // platform root itself
		{"/elsewhere/x", nil}, // outside the tree
		{"/plat/unknown-repo", nil},
	}
	for _, c := range cases {
		got := inferScopes(c.cwd, root)
		if len(got) != len(c.want) {
			t.Errorf("inferScopes(%q) = %v, want %v", c.cwd, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("inferScopes(%q) = %v, want %v", c.cwd, got, c.want)
				break
			}
		}
	}
}

func TestRetrieveEconomy_SumsLocalKBSourcesOnly(t *testing.T) {
	kb := t.TempDir()
	if err := os.MkdirAll(filepath.Join(kb, "decisions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kb, "decisions", "log.md"), make([]byte, 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	got := retrieveEconomy(kb, []string{
		"decisions/log.md",             // counted: 1000
		"core-api@abc123:src/Foo.java", // repo chunk — skipped
		"missing/file.md",              // absent — skipped
	})
	if got != 1000 {
		t.Errorf("economy = %d, want 1000", got)
	}
}

func TestRetrieve_MissingQuestionAndStackDown(t *testing.T) {
	if code := Retrieve([]string{"-no-record"}); code != 2 {
		t.Errorf("missing question: exit %d, want 2", code)
	}
	// Fake stack answers embed; a real question against fakeStack must succeed
	// without any /api/generate call (retrieval-only path).
	fakeStack(t, "unused")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "knowledge-base"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "knowledge-base", "README.md"), []byte("kb"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	if code := Retrieve([]string{"-no-record", "-scope", "backend", "how is tenant scoping enforced"}); code != 0 {
		t.Errorf("fake-stack retrieve: exit %d, want 0", code)
	}
	if code := Retrieve([]string{"-no-record", "-format", "md", "-scope", "backend", "same question md"}); code != 0 {
		t.Errorf("md format: exit %d, want 0", code)
	}
}
