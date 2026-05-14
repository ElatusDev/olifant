package corpus

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// chunkYAML loads a structured YAML catalog (standards/decisions/anti-patterns)
// and emits one Chunk per top-level entry that carries an identifier-bearing key.
//
// Heuristics:
//   - The top-level YAML value is expected to be either a sequence of mapping nodes
//     (e.g., array of rules) or a mapping whose values are sequences (e.g., grouped
//     by category).
//   - An entry is "identified" by the first present key from idKeys (in order).
//   - If no idKey is found, the entry is rendered as a single chunk using its
//     position in the sequence and the source path.
func chunkYAML(absPath, kbRelPath, scope, docType, sourceSHA string) ([]Chunk, error) {
	raw, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("%s: %w", kbRelPath, err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil, nil
	}
	top := root.Content[0]

	var entries []*yaml.Node
	switch top.Kind {
	case yaml.SequenceNode:
		entries = top.Content
	case yaml.MappingNode:
		// collect entries from each mapping value that is a sequence
		for i := 1; i < len(top.Content); i += 2 {
			val := top.Content[i]
			if val.Kind == yaml.SequenceNode {
				entries = append(entries, val.Content...)
			}
		}
	}
	if len(entries) == 0 {
		return nil, nil
	}

	idKeys := []string{"id", "rule_id", "term", "AP", "D", "code"}

	out := make([]Chunk, 0, len(entries))
	for i, e := range entries {
		artifactID := extractMappingString(e, idKeys...)
		title := extractMappingString(e, "title", "name", "term", "definition")
		severity := extractMappingString(e, "severity")
		status := extractMappingString(e, "status")
		bodyBytes, err := yaml.Marshal(nodeToInterface(e))
		if err != nil {
			continue
		}
		body := strings.TrimRight(string(bodyBytes), "\n")

		anchor := kbRelPath
		if artifactID != "" {
			anchor = kbRelPath + "#" + artifactID
		} else {
			anchor = fmt.Sprintf("%s#item-%d", kbRelPath, i)
		}

		c := Chunk{
			ChunkID:      makeChunkID(kbRelPath, anchor, body),
			Source:       kbRelPath,
			SourceSHA:    sourceSHA,
			SourceAnchor: anchor,
			Scope:        scope,
			DocType:      docType,
			ArtifactID:   artifactID,
			Title:        title,
			Body:         body,
			Metadata: ChunkMetadata{
				Severity:      strings.ToUpper(severity),
				Status:        strings.ToUpper(status),
				CitesOutbound: ExtractCites(body),
			},
			EmbeddedAt: nowISO(),
		}
		out = append(out, c)
	}
	return out, nil
}

// extractMappingString returns the string value of the first matching key in a
// mapping node, or "".
func extractMappingString(n *yaml.Node, keys ...string) string {
	if n.Kind != yaml.MappingNode {
		return ""
	}
	for _, want := range keys {
		for i := 0; i < len(n.Content)-1; i += 2 {
			k := n.Content[i]
			v := n.Content[i+1]
			if k.Value == want && v.Kind == yaml.ScalarNode {
				return v.Value
			}
		}
	}
	return ""
}

// nodeToInterface converts a yaml.Node tree into an interface{} suitable for
// re-marshaling. Preserves scalars, sequences, and mappings.
func nodeToInterface(n *yaml.Node) interface{} {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil
		}
		return nodeToInterface(n.Content[0])
	case yaml.SequenceNode:
		out := make([]interface{}, 0, len(n.Content))
		for _, c := range n.Content {
			out = append(out, nodeToInterface(c))
		}
		return out
	case yaml.MappingNode:
		out := make(map[string]interface{}, len(n.Content)/2)
		for i := 0; i < len(n.Content)-1; i += 2 {
			out[n.Content[i].Value] = nodeToInterface(n.Content[i+1])
		}
		return out
	case yaml.ScalarNode:
		return n.Value
	default:
		return nil
	}
}

// makeChunkID is content-stable: same source/anchor/body → same ID across rebuilds.
func makeChunkID(source, anchor, body string) string {
	h := sha1.New()
	h.Write([]byte(filepath.ToSlash(source)))
	h.Write([]byte{0})
	h.Write([]byte(anchor))
	h.Write([]byte{0})
	h.Write([]byte(body))
	return hex.EncodeToString(h.Sum(nil))
}
