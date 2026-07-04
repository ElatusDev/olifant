// Package promptgate resolves citations in generated prompt/workflow documents
// against the live knowledge base. It composes — never modifies — the challenge
// CiteValidator (D-OP3): bare artifact IDs (D210, AP164, …) resolve against a
// fresh scan of the Layer-1 canonical sources, because the CNL dictionary is
// append-only and drifts behind the decision/anti-pattern sequences; terms and
// file-path cites delegate to the validator unchanged.
//
// A cite that resolves live but whose source file's indexed SHA differs from
// the working tree is reported stale, not unresolved (D-OP7): validity comes
// from the live sources, staleness is an index-quality signal for the operator.
package promptgate

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ElatusDev/olifant/internal/challenge"
	"github.com/ElatusDev/olifant/internal/corpus"
)

// Verdict classifies one citation.
type Verdict string

const (
	VerdictResolved   Verdict = "resolved"
	VerdictStale      Verdict = "stale"
	VerdictUnresolved Verdict = "unresolved"
)

// Resolution is the per-cite outcome. Source names the Layer-1 file (or cited
// path) that grounds a resolved/stale verdict; empty when unresolved.
type Resolution struct {
	Cite    string  `yaml:"cite"`
	Verdict Verdict `yaml:"verdict"`
	Source  string  `yaml:"source,omitempty"`
}

// layer1Sources are the canonical KB files scanned for artifact IDs, relative
// to kbRoot. Globs are expanded at construction.
var layer1Sources = []string{
	"decisions/log.md",
	"anti-patterns/catalog.md",
	"standards/*.md",
	"patterns/*.md",
}

// Resolver resolves cites against the live KB + the challenge validator.
// Build once via NewResolver, reuse across many Resolve calls.
type Resolver struct {
	validator *challenge.CiteValidator
	// artifactIDs maps a bare ID (e.g. "D210") to the kb-relative source file
	// that mentions it.
	artifactIDs map[string]string
	// indexedSHAs maps kb-relative source path → blob SHA recorded in the
	// corpus manifest at index time. Empty when no manifest exists.
	indexedSHAs map[string]string
	// liveSHAs maps kb-relative path → current git blob SHA. Empty when the
	// KB is not a git checkout.
	liveSHAs map[string]string
}

// NewResolver scans the Layer-1 sources under kbRoot and loads the corpus
// manifest (if present) for staleness detection. platformRoot feeds the
// underlying CiteValidator's file-path resolution.
func NewResolver(platformRoot, kbRoot string) (*Resolver, error) {
	v, err := challenge.NewCiteValidator(platformRoot, kbRoot)
	if err != nil {
		return nil, fmt.Errorf("cite validator: %w", err)
	}
	r := &Resolver{
		validator:   v,
		artifactIDs: map[string]string{},
		indexedSHAs: map[string]string{},
		liveSHAs:    map[string]string{},
	}

	for _, pattern := range layer1Sources {
		matches, gErr := filepath.Glob(filepath.Join(kbRoot, filepath.FromSlash(pattern)))
		if gErr != nil {
			continue
		}
		for _, path := range matches {
			body, rErr := os.ReadFile(path)
			if rErr != nil {
				continue
			}
			rel, relErr := filepath.Rel(kbRoot, path)
			if relErr != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			for _, id := range corpus.ExtractCites(string(body)) {
				if _, seen := r.artifactIDs[id]; !seen {
					r.artifactIDs[id] = rel
				}
			}
		}
	}

	r.indexedSHAs = manifestSourceSHAs(filepath.Join(kbRoot, "corpus", "v1", "manifest.yaml"))
	r.liveSHAs = gitBlobSHAs(kbRoot)
	return r, nil
}

// KnownArtifactCount reports how many distinct artifact IDs the live scan found.
func (r *Resolver) KnownArtifactCount() int { return len(r.artifactIDs) }

// Resolve classifies one cite. Resolution order: live artifact-ID scan, then
// the validator's term/path universe. Staleness overlays a resolved verdict
// when the grounding source file is tracked by the corpus manifest under a
// different SHA than the working tree.
func (r *Resolver) Resolve(cite string) Resolution {
	cite = strings.TrimSpace(cite)
	if cite == "" {
		return Resolution{Cite: cite, Verdict: VerdictUnresolved}
	}
	if src, ok := r.artifactIDs[cite]; ok {
		return Resolution{Cite: cite, Verdict: r.overlayStale(src), Source: src}
	}
	if r.validator.Resolves(cite) {
		// Path cites can be staleness-checked when they point into the KB;
		// dictionary terms have no single source file to compare.
		src := strings.TrimPrefix(cite, "knowledge-base/")
		if hash := strings.IndexByte(src, '#'); hash >= 0 {
			src = src[:hash]
		}
		if _, tracked := r.indexedSHAs[src]; tracked {
			return Resolution{Cite: cite, Verdict: r.overlayStale(src), Source: src}
		}
		return Resolution{Cite: cite, Verdict: VerdictResolved}
	}
	return Resolution{Cite: cite, Verdict: VerdictUnresolved}
}

// overlayStale returns stale when the source is in the manifest with a SHA
// that no longer matches the working tree; resolved otherwise (including when
// either side is unknown — absence of evidence is not staleness).
func (r *Resolver) overlayStale(kbRelSource string) Verdict {
	indexed, inManifest := r.indexedSHAs[kbRelSource]
	live, inTree := r.liveSHAs[kbRelSource]
	if inManifest && inTree && indexed != live {
		return VerdictStale
	}
	return VerdictResolved
}

// manifestSourceSHAs loads path→sha from a corpus v1 manifest. Missing or
// malformed manifests yield an empty map — staleness detection degrades to
// "never stale" rather than failing the gate (D-OP7).
func manifestSourceSHAs(manifestPath string) map[string]string {
	out := map[string]string{}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return out
	}
	var m corpus.Manifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return out
	}
	for _, s := range m.Sources {
		if s.Path != "" && s.SHA != "" {
			out[filepath.ToSlash(s.Path)] = s.SHA
		}
	}
	return out
}

// gitBlobSHAs returns path→blob-SHA for the git checkout at root, or an empty
// map when root is not a repo (mirrors the corpus builder's non-fatal stance).
func gitBlobSHAs(root string) map[string]string {
	out := map[string]string{}
	cmd := exec.Command("git", "ls-files", "-s")
	cmd.Dir = root
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return out
	}
	if err := cmd.Start(); err != nil {
		return out
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		fields := strings.Fields(line[:tab])
		if len(fields) < 2 {
			continue
		}
		out[filepath.ToSlash(line[tab+1:])] = fields[1]
	}
	_ = cmd.Wait()
	return out
}
