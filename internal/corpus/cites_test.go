package corpus

import (
	"strings"
	"testing"
)

// TestExtractCites_IssueScopedIDs covers the issue-scoped ID scheme (KB D-563a):
// entries keyed to their originating GitHub issue must be recognized as cites,
// while workflow-local IDs of a similar shape must NOT be — extracting the
// latter would turn a doc-scoped label into an unresolvable cite and block the
// doc under the D218 publication gate.
func TestExtractCites_IssueScopedIDs(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    []string
		notWant []string
	}{
		{
			name: "issue-scoped anti-pattern and decision are cites",
			body: "See AP-486a and D-563a for the rationale.",
			want: []string{"AP-486a", "D-563a"},
		},
		{
			name: "multiple entries from one issue disambiguate by letter",
			body: "Both AP-490a and AP-490b came out of that retro.",
			want: []string{"AP-490a", "AP-490b"},
		},
		{
			name: "issue-scoped backend anti-pattern family",
			body: "Mirror entry ABS-486a documents the same trap.",
			want: []string{"ABS-486a"},
		},
		{
			name:    "workflow-local IDs are NOT cites",
			body:    "Decision D-486-1 and D-720-2 are workflow-local.",
			notWant: []string{"D-486", "D-486-1", "D-720", "D-720-2"},
		},
		{
			name: "legacy numeric IDs still resolve (frozen, not renamed)",
			body: "Legacy AP59, D135 and ABS-25 remain valid citations.",
			want: []string{"AP59", "D135", "ABS-25"},
		},
		{
			name: "legacy and issue-scoped coexist in one doc",
			body: "AP313 was raced; AP-563a is the fix.",
			want: []string{"AP313", "AP-563a"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractCites(tc.body)
			index := make(map[string]struct{}, len(got))
			for _, id := range got {
				index[id] = struct{}{}
			}
			for _, want := range tc.want {
				if _, ok := index[want]; !ok {
					t.Errorf("ExtractCites(%q) = %v, missing expected cite %q", tc.body, got, want)
				}
			}
			for _, notWant := range tc.notWant {
				if _, ok := index[notWant]; ok {
					t.Errorf("ExtractCites(%q) = %v, must NOT extract %q (workflow-local, would block the doc)", tc.body, got, notWant)
				}
			}
		})
	}
}

// TestExtractCites_IssueScopedRequiresLetter pins the disambiguation rule: the
// trailing letter is what separates a KB artifact from a workflow-local label.
func TestExtractCites_IssueScopedRequiresLetter(t *testing.T) {
	got := strings.Join(ExtractCites("AP-486 without a letter is not an artifact ID"), ",")
	if strings.Contains(got, "AP-486") {
		t.Errorf("ExtractCites extracted a letterless issue-scoped ID (%q); the letter is required to disambiguate from workflow-local IDs", got)
	}
}
