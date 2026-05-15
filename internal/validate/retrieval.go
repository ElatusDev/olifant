package validate

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ElatusDev/olifant/internal/chroma"
	"github.com/ElatusDev/olifant/internal/ollama"
)

// embedRequestMaxChars caps the embed-query body. nomic-embed-text via
// Ollama rejects inputs above ~5000 chars regardless of the truncate flag,
// so we cap defensively. Mirrors the constant in internal/challenge.
const embedRequestMaxChars = 3500

// RetrievedHit is one Chroma result row tagged with the source collection
// scope/family so the prompt can render attribution.
type RetrievedHit struct {
	Doc      string
	Meta     map[string]interface{}
	Distance float32
	Scope    string // e.g., "backend/corpus" or "webapp/code_history"
}

// RetrievalConfig parameterises a single Retrieve() call. The embedder model
// and Chroma client settings are read once per run.
type RetrievalConfig struct {
	OllamaURL string
	ChromaURL string
	Embedder  string
	Tenant    string
	Database  string
	Scopes    []string // empty = default 7-scope union
	TopN      int      // global cap after merging all per-collection results; default 8
	Verbose   bool
}

// defaultScopes is the scope union queried when the caller passes none.
// Mirrors challenge.Run's default.
var defaultScopes = []string{"universal", "backend", "webapp", "mobile", "e2e", "infra", "platform-process"}

// codeScopes enumerates the stack scopes for which we ingest code,
// commit history, and historical code snapshots. universal +
// platform-process have neither so the non-corpus families are skipped.
var codeScopes = map[string]bool{
	"backend": true, "webapp": true, "mobile": true, "e2e": true, "infra": true,
}

// Retrieve embeds `query` and queries the four collection families
// (corpus, code, history, code_history) per scope, returning the global
// TopN hits ordered by ascending distance.
//
// Errors in any single collection (missing, transport, query) are logged
// (when Verbose) and skipped — retrieval is best-effort grounding, not a
// hard dependency. Embedder failure is fatal because there's nothing to
// query against.
func Retrieve(ctx context.Context, cfg RetrievalConfig, query string) ([]RetrievedHit, error) {
	if cfg.TopN <= 0 {
		cfg.TopN = 8
	}
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = defaultScopes
	}

	oc := ollama.New(cfg.OllamaURL)
	cc := chroma.New(cfg.ChromaURL, cfg.Tenant, cfg.Database)

	qEmb, err := oc.Embed(ctx, cfg.Embedder, []string{capChars(query, embedRequestMaxChars)})
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	if len(qEmb) != 1 {
		return nil, fmt.Errorf("embed returned %d vectors, expected 1", len(qEmb))
	}

	collFamilies := []string{"corpus", "code", "history", "code_history"}
	var hits []RetrievedHit
	for _, scope := range scopes {
		for _, family := range collFamilies {
			if family != "corpus" && !codeScopes[scope] {
				continue
			}
			collName := family + "_" + strings.ReplaceAll(scope, "-", "_")
			coll, err := cc.EnsureCollection(ctx, collName, nil)
			if err != nil {
				if cfg.Verbose {
					fmt.Printf("  %s: collection unavailable (%v) — skipping\n", collName, err)
				}
				continue
			}
			res, qerr := cc.Query(ctx, coll.ID, chroma.QueryRequest{
				QueryEmbeddings: qEmb,
				NResults:        cfg.TopN,
			})
			if qerr != nil {
				if cfg.Verbose {
					fmt.Printf("  %s: query failed (%v) — skipping\n", collName, qerr)
				}
				continue
			}
			if len(res.Documents) == 0 {
				continue
			}
			for i := range res.Documents[0] {
				hits = append(hits, RetrievedHit{
					Doc:      res.Documents[0][i],
					Meta:     res.Metadatas[0][i],
					Distance: res.Distances[0][i],
					Scope:    scope + "/" + family,
				})
			}
		}
	}

	sort.Slice(hits, func(i, j int) bool { return hits[i].Distance < hits[j].Distance })
	if len(hits) > cfg.TopN {
		hits = hits[:cfg.TopN]
	}
	return hits, nil
}

// renderRetrievedBlock formats hits for inclusion in the validator prompt.
// Same shape challenge uses so the model learns one citation style.
func renderRetrievedBlock(hits []RetrievedHit) string {
	if len(hits) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("RETRIEVED CONTEXT (standards and prior-art chunks ordered by similarity to the claim):\n\n")
	for i, h := range hits {
		source, _ := h.Meta["source"].(string)
		anchor, _ := h.Meta["source_anchor"].(string)
		aid, _ := h.Meta["artifact_id"].(string)
		fmt.Fprintf(&sb, "--- chunk %d (distance=%.4f, scope=%s", i+1, h.Distance, h.Scope)
		if aid != "" {
			fmt.Fprintf(&sb, ", artifact_id=%s", aid)
		}
		if anchor != "" {
			fmt.Fprintf(&sb, ", anchor=%s", anchor)
		} else if source != "" {
			fmt.Fprintf(&sb, ", source=%s", source)
		}
		sb.WriteString(") ---\n")
		sb.WriteString(h.Doc)
		if !strings.HasSuffix(h.Doc, "\n") {
			sb.WriteByte('\n')
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// capChars trims s to maxChars at a UTF-8 boundary. Local copy — the
// challenge package's version is unexported.
func capChars(s string, maxChars int) string {
	if len(s) <= maxChars {
		return s
	}
	end := maxChars
	for end > 0 && (s[end]&0xC0) == 0x80 {
		end--
	}
	return s[:end]
}
