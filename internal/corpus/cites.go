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

	// Issue-scoped IDs (KB D-563a): entries authored 2026-08-03+ are keyed to
	// the GitHub issue they came from — `AP-<issue><letter>` / `D-<issue><letter>`
	// and the per-stack anti-pattern families — instead of a shared sequential
	// counter (which raced: three KB PRs claimed AP313 on 2026-08-03).
	//
	// The trailing letter is REQUIRED, and that is load-bearing: it separates a
	// KB artifact (`D-563a`) from a *workflow-local* ID (`D-486-1`, `AP-720-2`),
	// which is doc-scoped and must stay unrecognized — extracting one would make
	// it an unresolvable cite and block the doc under the D218 publication gate.
	regexp.MustCompile(`\b(AP|D)-\d+[a-z]\b`),
	regexp.MustCompile(`\b(ABB|ABO|ABC|ABD|ABE|ABS|ABT|AWC|AWH|AWS|AWR|AWT|AWB|AWTA|AWA|AMC|AMP|AMS|AMN|AMH|AME|AMTA)-\d+[a-z]\b`),
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
