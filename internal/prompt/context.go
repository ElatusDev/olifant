package prompt

import (
	"context"

	"github.com/ElatusDev/olifant/internal/corpus"
	"github.com/ElatusDev/olifant/internal/retrieval"
)

// ContextConfig parameterises BuildContext — the retrieval-only half of the
// prompt pipeline (charter R2 / D-OP1: no synthesizer anywhere on this path).
type ContextConfig struct {
	Goal      string
	OllamaURL string
	ChromaURL string
	Embedder  string
	Tenant    string
	Database  string
	Scopes    []string
	TopN      int
	// MaxChars caps each chunk body in the output (0 = no cap).
	MaxChars int
	Verbose  bool
	// Families, when non-empty, replaces the default collection-family set
	// {corpus,code,history,code_history} and queries every listed family on
	// every scope (no code-scope gating). Empty (the default) leaves
	// retrieve() behaviour byte-for-byte unchanged. The code-advice path
	// (`retrieve --file`) sets {"corpus","failure_modes"} so it surfaces the
	// RULE families — anti-patterns / patterns / standards + the "use X not Y"
	// corrections — without the raw source-code families crowding them out
	// (D-PP3, olifant#106; refined at P3 live proof).
	Families []string
}

// ContextChunk is one retrieved chunk shaped for skill consumption: the body
// plus the provenance a prompt author needs to cite it.
type ContextChunk struct {
	Source   string   `yaml:"source"`
	Scope    string   `yaml:"scope"`
	DocType  string   `yaml:"doc_type,omitempty"`
	Distance float32  `yaml:"distance"`
	Cites    []string `yaml:"cites,omitempty"`
	Body     string   `yaml:"body"`
}

// ContextResult is BuildContext's output.
type ContextResult struct {
	Chunks     []ContextChunk
	Sources    []string
	EmbedMs    int64
	RetrieveMs int64
}

// BuildContext embeds the goal, retrieves the top-N scope-filtered chunks,
// and returns them cite-tagged. It never calls a synthesizer.
func BuildContext(ctx context.Context, cfg ContextConfig) (*ContextResult, error) {
	hits, embedMs, retrieveMs, err := retrieve(ctx, retrieveConfig{
		Goal:      cfg.Goal,
		OllamaURL: cfg.OllamaURL,
		ChromaURL: cfg.ChromaURL,
		Embedder:  cfg.Embedder,
		Tenant:    cfg.Tenant,
		Database:  cfg.Database,
		Scopes:    cfg.Scopes,
		TopN:      cfg.TopN,
		Verbose:   cfg.Verbose,
		Families:  cfg.Families,
	})
	if err != nil {
		return nil, err
	}

	res := &ContextResult{
		Chunks:     make([]ContextChunk, 0, len(hits)),
		Sources:    sourcePathsFromHits(hits),
		EmbedMs:    embedMs,
		RetrieveMs: retrieveMs,
	}
	for _, h := range hits {
		body := h.Doc
		if cfg.MaxChars > 0 {
			body = retrieval.CapChars(body, cfg.MaxChars)
		}
		src, _ := h.Meta["source"].(string)
		docType, _ := h.Meta["doc_type"].(string)
		res.Chunks = append(res.Chunks, ContextChunk{
			Source:   src,
			Scope:    h.Scope,
			DocType:  docType,
			Distance: h.Distance,
			Cites:    corpus.ExtractCites(h.Doc),
			Body:     body,
		})
	}
	return res, nil
}
