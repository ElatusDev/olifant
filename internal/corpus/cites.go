package corpus

import (
	"regexp"
	"sort"
)

// citePatterns recognize known platform artifact IDs anywhere in body text.
// Order is irrelevant — all patterns are tried against each chunk.
var citePatterns = []*regexp.Regexp{
	// Decisions, retro discipline rules
	regexp.MustCompile(`\b(D|RL|RK|RM|RS)\d+\b`),
	// Top-level anti-pattern catalog (AP1-AP85+)
	regexp.MustCompile(`\bAP\d+\b`),
	// Security catalog: SB, SI, SW, SM, SX prefixes with numeric suffix
	regexp.MustCompile(`\b(SB|SI|SW|SM|SX)-\d+\b`),
	// Code-quality top-level categories
	regexp.MustCompile(`\b(U|B|W|M|T|E|I)-\d+\b`),
	// Webapp architecture rules: WA-<LETTER>-NN or WA-<LETTER>
	regexp.MustCompile(`\bWA-[A-Z]+(?:-\d+)?\b`),
	// Webapp anti-patterns: AWC/AWH/AWS/AWR/AWT/AWB/AWTA/AWA
	regexp.MustCompile(`\b(AWC|AWH|AWS|AWR|AWT|AWB|AWTA|AWA)-\d+\b`),
	// Mobile anti-patterns: AMC/AMP/AMS/AMN/AMH/AME/AMTA
	regexp.MustCompile(`\b(AMC|AMP|AMS|AMN|AMH|AME|AMTA)-\d+\b`),
	// Backend anti-patterns: ABB/ABO/ABC/ABD/ABE/ABS/ABT
	regexp.MustCompile(`\b(ABB|ABO|ABC|ABD|ABE|ABS|ABT)-\d+\b`),
	// Backend testing rules: TBX/TBU/TBC/TBE/TAP
	regexp.MustCompile(`\b(TBX|TBU|TBC|TBE|TAP)-\d+\b`),
	// Webapp testing rules: TWU/TWC/TWE/TAW
	regexp.MustCompile(`\b(TWU|TWC|TWE|TAW)-\d+\b`),
	// Mobile testing rules: TMU/TMC/TME/TAM
	regexp.MustCompile(`\b(TMU|TMC|TME|TAM)-\d+\b`),
	// Observability rules: OL/OT/OM/OH/OE/OW/OA/OI/AO
	regexp.MustCompile(`\b(OL|OT|OM|OH|OE|OW|OA|OI|AO)-\d+\b`),
	// Schema-source rule
	regexp.MustCompile(`\bSS-\d+\b`),
}

// ExtractCites returns deduplicated, sorted artifact IDs referenced anywhere in body.
func ExtractCites(body string) []string {
	if body == "" {
		return nil
	}
	seen := make(map[string]struct{})
	for _, re := range citePatterns {
		for _, m := range re.FindAllString(body, -1) {
			seen[m] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
