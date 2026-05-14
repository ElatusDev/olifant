package corpus

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

const (
	mdMaxChunkChars = 1500
	mdMinChunkChars = 200
)

// chunkMarkdown reads a markdown file and splits it into chunks per the rules
// in CORPUS-V1.md:
//   - Split on H2 (##) boundaries first.
//   - If a section is too long, split on H3 (###).
//   - If still too long, split on blank lines, respecting code fences.
//   - If a section is too short, merge with the next section.
//   - Each chunk's body is prefixed with a breadcrumb header so it self-identifies.
func chunkMarkdown(absPath, kbRelPath, scope, docType, sourceSHA string) ([]Chunk, error) {
	raw, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	docTitle, sections := splitSectionsByHeading(string(raw), 2)
	if len(sections) == 0 {
		// no H2 sections — treat the whole doc as one chunk
		sections = []mdSection{{Title: docTitle, Body: string(raw)}}
	}

	// Subdivide oversize sections by H3, then by paragraph
	var working []mdSection
	for _, s := range sections {
		if len(s.Body) <= mdMaxChunkChars {
			working = append(working, s)
			continue
		}
		_, subs := splitSectionsByHeading(s.Body, 3)
		if len(subs) <= 1 {
			// no H3 boundaries — split by paragraph
			for _, p := range splitByParagraph(s.Body, mdMaxChunkChars) {
				working = append(working, mdSection{Title: s.Title, Body: p, Heading: s.Heading})
			}
			continue
		}
		for _, sub := range subs {
			if len(sub.Body) <= mdMaxChunkChars {
				working = append(working, mdSection{Title: s.Title + " > " + sub.Title, Body: sub.Body, Heading: s.Heading + " > " + sub.Heading})
				continue
			}
			for _, p := range splitByParagraph(sub.Body, mdMaxChunkChars) {
				working = append(working, mdSection{Title: s.Title + " > " + sub.Title, Body: p, Heading: s.Heading + " > " + sub.Heading})
			}
		}
	}

	// Merge sliver sections forward
	merged := mergeSlivers(working, mdMinChunkChars)

	// Emit chunks
	out := make([]Chunk, 0, len(merged))
	for i, s := range merged {
		anchor := fmt.Sprintf("%s#%s", kbRelPath, slugify(s.Heading, i))
		breadcrumb := fmt.Sprintf("[%s] %s%s\n\n", docType, docTitle, ifNonEmpty(" > "+s.Title, s.Title))
		body := breadcrumb + strings.TrimSpace(s.Body)
		c := Chunk{
			ChunkID:      makeChunkID(kbRelPath, anchor, body),
			Source:       kbRelPath,
			SourceSHA:    sourceSHA,
			SourceAnchor: anchor,
			Scope:        scope,
			DocType:      docType,
			Title:        s.Title,
			Body:         body,
			Metadata: ChunkMetadata{
				Section:       s.Heading,
				CitesOutbound: ExtractCites(body),
			},
			EmbeddedAt: nowISO(),
		}
		out = append(out, c)
	}
	return out, nil
}

type mdSection struct {
	Title   string // human-readable, no leading #
	Heading string // canonical chain like "Goals > Determinism"
	Body    string // section body, not including the heading line
}

// splitSectionsByHeading splits a markdown body on headings of the given level
// (1-based; level=2 → ## ). Returns the doc-level title (first H1 line if any)
// and the sections discovered. Body for each section excludes the heading line.
func splitSectionsByHeading(body string, level int) (docTitle string, sections []mdSection) {
	prefix := strings.Repeat("#", level) + " "
	scan := bufio.NewScanner(strings.NewReader(body))
	scan.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var (
		current *mdSection
		inFence bool
	)
	for scan.Scan() {
		line := scan.Text()
		trim := strings.TrimSpace(line)
		// fence detection — never split inside ```
		if strings.HasPrefix(trim, "```") {
			inFence = !inFence
			if current != nil {
				current.Body += line + "\n"
			}
			continue
		}
		if !inFence {
			if docTitle == "" && strings.HasPrefix(trim, "# ") && !strings.HasPrefix(trim, "## ") {
				docTitle = strings.TrimSpace(strings.TrimPrefix(trim, "# "))
				continue
			}
			if strings.HasPrefix(trim, prefix) {
				if current != nil {
					sections = append(sections, *current)
				}
				heading := strings.TrimSpace(strings.TrimPrefix(trim, prefix))
				current = &mdSection{Title: heading, Heading: heading}
				continue
			}
		}
		if current != nil {
			current.Body += line + "\n"
		}
	}
	if current != nil {
		sections = append(sections, *current)
	}
	return docTitle, sections
}

// splitByParagraph greedily packs paragraphs into chunks up to maxChars.
// Respects code fences (a fence block stays intact even if it exceeds maxChars).
func splitByParagraph(body string, maxChars int) []string {
	var (
		out     []string
		buf     strings.Builder
		para    strings.Builder
		inFence bool
	)
	flushPara := func() {
		p := strings.TrimRight(para.String(), "\n") + "\n\n"
		if buf.Len()+len(p) > maxChars && buf.Len() > 0 {
			out = append(out, strings.TrimRight(buf.String(), "\n"))
			buf.Reset()
		}
		buf.WriteString(p)
		para.Reset()
	}
	scan := bufio.NewScanner(strings.NewReader(body))
	scan.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scan.Scan() {
		line := scan.Text()
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") {
			inFence = !inFence
			para.WriteString(line + "\n")
			continue
		}
		if inFence {
			para.WriteString(line + "\n")
			continue
		}
		if trim == "" {
			if para.Len() > 0 {
				flushPara()
			}
			continue
		}
		para.WriteString(line + "\n")
	}
	if para.Len() > 0 {
		flushPara()
	}
	if buf.Len() > 0 {
		out = append(out, strings.TrimRight(buf.String(), "\n"))
	}
	return out
}

// mergeSlivers walks sections and merges short ones forward.
func mergeSlivers(sections []mdSection, minChars int) []mdSection {
	if len(sections) <= 1 {
		return sections
	}
	out := make([]mdSection, 0, len(sections))
	for i := 0; i < len(sections); i++ {
		s := sections[i]
		for i+1 < len(sections) && len(s.Body) < minChars {
			next := sections[i+1]
			s.Body = strings.TrimRight(s.Body, "\n") + "\n\n" + next.Body
			s.Title = s.Title + " + " + next.Title
			s.Heading = s.Heading + " + " + next.Heading
			i++
		}
		out = append(out, s)
	}
	return out
}

// slugify produces a stable anchor fragment from a heading.
func slugify(heading string, fallbackIdx int) string {
	if heading == "" {
		return fmt.Sprintf("section-%d", fallbackIdx)
	}
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ', r == '-', r == '_':
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return fmt.Sprintf("section-%d", fallbackIdx)
	}
	// avoid runaway slugs
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

func ifNonEmpty(prefixed, raw string) string {
	if raw == "" {
		return ""
	}
	return prefixed
}
