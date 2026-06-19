package corpus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestChunkMarkdown_OversizeSubdivide drives the H3-subdivide and
// paragraph-split branches: an H2 section far over mdMaxChunkChars, both
// with and without H3 sub-headings.
func TestChunkMarkdown_OversizeSubdivide(t *testing.T) {
	para := strings.Repeat("word ", 120) + "\n\n" // ~600 chars/para
	big := strings.Repeat(para, 6)                // ~3.6k chars → over the 1500 cap

	// Case A: oversize H2 with H3 sub-sections → split on H3.
	withH3 := "# Doc\n\n## Big Section\n\n### Sub One\n\n" + big + "\n### Sub Two\n\n" + big + "\n"
	// Case B: oversize H2 with no H3 → split by paragraph.
	noH3 := "# Doc\n\n## Flat Big\n\n" + big + "\n"

	for name, body := range map[string]string{"with-h3": withH3, "no-h3": noH3} {
		p := filepath.Join(t.TempDir(), "doc.md")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		chunks, err := chunkMarkdown(p, "patterns/x.md", "backend", "pattern", "sha")
		if err != nil {
			t.Fatalf("%s: chunkMarkdown: %v", name, err)
		}
		if len(chunks) < 2 {
			t.Errorf("%s: oversize section should split into multiple chunks, got %d", name, len(chunks))
		}
		for _, c := range chunks {
			if len(c.Body) == 0 {
				t.Errorf("%s: empty chunk body", name)
			}
		}
	}
}
