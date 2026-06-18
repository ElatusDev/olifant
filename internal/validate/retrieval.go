package validate

import (
	"context"
	"fmt"
	"strings"

	"github.com/ElatusDev/olifant/internal/chroma"
	"github.com/ElatusDev/olifant/internal/ollama"
	"github.com/ElatusDev/olifant/internal/retrieval"
)

// RetrievedHit is one Chroma result row (shared shape — see internal/retrieval).
type RetrievedHit = retrieval.Hit

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

	qEmb, err := retrieval.Embed(ctx, oc, cfg.Embedder, query, retrieval.DefaultEmbedMaxChars)
	if err != nil {
		return nil, err
	}

	hits := retrieval.QueryScopedFamilies(ctx, cc, qEmb, retrieval.FamilyConfig{
		Families:       []string{"corpus", "code", "history", "code_history"},
		AlwaysFamilies: map[string]bool{"corpus": true},
		CodeScopes:     codeScopes,
		Scopes:         scopes,
		TopN:           cfg.TopN,
		Verbose:        cfg.Verbose,
	})
	return retrieval.SortByDistanceTruncate(hits, cfg.TopN), nil
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
