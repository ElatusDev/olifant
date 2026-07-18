package cmd

import "testing"

// olifant#96 AC5: `olifant prompt -h|--help|help` prints the action list and
// exits 0 instead of the pre-#96 `unknown action "--help"` error.
func TestPromptHelpExitsZero(t *testing.T) {
	for _, arg := range []string{"help", "--help", "-h"} {
		if code := Prompt([]string{arg}); code != 0 {
			t.Errorf("Prompt(%q) = %d; want 0", arg, code)
		}
	}
	if code := Prompt([]string{"bogus"}); code != 2 {
		t.Errorf("Prompt(bogus) = %d; want 2 (unknown action preserved)", code)
	}
}
