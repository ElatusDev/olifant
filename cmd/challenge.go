package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ElatusDev/olifant/internal/challenge"
	"github.com/ElatusDev/olifant/internal/config"
)

// languageHintForPath maps a file extension to a short lang tag for the
// review-prompt frame. Falls back to "" for unknown extensions.
func languageHintForPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".java":
		return "java"
	case ".kt", ".kts":
		return "kotlin"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "tsx"
	case ".js":
		return "javascript"
	case ".jsx":
		return "jsx"
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".rb":
		return "ruby"
	case ".swift":
		return "swift"
	case ".rs":
		return "rust"
	case ".tf", ".tfvars", ".hcl":
		return "terraform"
	case ".sql":
		return "sql"
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	case ".xml":
		return "xml"
	case ".sh", ".bash", ".zsh":
		return "shell"
	default:
		return ""
	}
}

// Challenge dispatches `olifant challenge "<user request>"`.
//
// Two input modes:
//
//	olifant challenge "<NL request>"
//	olifant challenge --file path/to/Foo.java   [optional NL suffix]
//
// When --file is supplied, the file content is wrapped in a "Review the
// following code for platform compliance:" frame and used as the request +
// retrieval query.
func Challenge(args []string) int {
	fs := flag.NewFlagSet("challenge", flag.ExitOnError)
	scopes := fs.String("scopes", "", "comma-separated scope filter (default: all)")
	topN := fs.Int("top", 8, "chunks to retrieve per scope")
	temp := fs.Float64("temperature", 0.1, "synthesizer temperature")
	maxTokens := fs.Int("max-tokens", 1024, "synthesizer num_predict")
	timeoutSec := fs.Int("timeout", 300, "overall timeout in seconds")
	verbose := fs.Bool("v", false, "verbose retrieval log")
	synth := fs.String("synth", "", "synthesizer model override")
	codeFile := fs.String("file", "", "code file to review (frames input as review-request)")
	_ = fs.Parse(args)

	var request string
	if *codeFile != "" {
		body, err := os.ReadFile(*codeFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "olifant challenge: read %s: %v\n", *codeFile, err)
			return 2
		}
		lang := languageHintForPath(*codeFile)
		suffix := ""
		if rest := fs.Args(); len(rest) > 0 {
			suffix = "\n\nAdditional context: " + strings.Join(rest, " ")
		}
		// Note: the synthesizer sees the full body. The embedder sees a
		// 3500-char head (single Embed call inside Run() is capped already).
		request = fmt.Sprintf(
			"Review the following %s code for ElatusDev/AkademiaPlus platform compliance.\n"+
				"File: %s\n\n"+
				"```%s\n%s\n```%s",
			lang, *codeFile, lang, strings.TrimRight(string(body), "\n"), suffix)
	} else {
		rest := fs.Args()
		if len(rest) == 0 {
			fmt.Fprintln(os.Stderr, "olifant challenge: missing request — usage: olifant challenge \"<request>\" OR olifant challenge --file <path>")
			return 2
		}
		request = strings.TrimSpace(strings.Join(rest, " "))
	}
	if request == "" {
		fmt.Fprintln(os.Stderr, "olifant challenge: empty request")
		return 2
	}

	rt := config.Resolve()
	synthesizer := rt.Synthesizer
	if *synth != "" {
		synthesizer = *synth
	}

	// Cite validator: load dictionary terms + repo prefixes.
	// Autodetect platform root from kb-root.
	var validator *challenge.CiteValidator
	if found, ok := findUp("knowledge-base/README.md"); ok {
		kbRoot := filepath.Dir(found)
		platformRoot := filepath.Dir(kbRoot)
		v, verr := challenge.NewCiteValidator(platformRoot, filepath.Join(kbRoot, "dictionary"))
		if verr == nil {
			validator = v
			if *verbose {
				fmt.Printf("validator: %d dictionary terms loaded from %s\n",
					v.KnownCount(), filepath.Join(kbRoot, "dictionary"))
			}
		} else {
			fmt.Fprintf(os.Stderr, "challenge: validator init failed (%v) — proceeding without\n", verr)
		}
	}

	var scopeList []string
	if *scopes != "" {
		for _, s := range strings.Split(*scopes, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				scopeList = append(scopeList, s)
			}
		}
	}

	if *verbose {
		fmt.Println("config:", rt.String())
		fmt.Println("synth :", synthesizer)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSec)*time.Second)
	defer cancel()

	res, err := challenge.Run(ctx, challenge.Config{
		Request:            request,
		OllamaURL:          rt.OllamaURL,
		ChromaURL:          rt.ChromaURL,
		Embedder:           rt.Embedder,
		Synthesizer:        synthesizer,
		Tenant:             rt.ChromaTenant,
		Database:           rt.ChromaDatabase,
		Scopes:             scopeList,
		TopN:               *topN,
		Temperature:        *temp,
		MaxTokens:          *maxTokens,
		Verbose:            *verbose,
		Validator:          validator,
		MaxValidateRetries: 1,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "olifant challenge:", err)
		return 1
	}

	// Count remaining violations by severity.
	var blockers, warnings, infos int
	for _, v := range res.RemainingCiteViolations {
		switch v.Severity {
		case challenge.SeverityBlocker:
			blockers++
		case challenge.SeverityWarning:
			warnings++
		case challenge.SeverityInfo:
			infos++
		}
	}
	// Print metrics to stderr so stdout stays clean YAML
	fmt.Fprintf(os.Stderr,
		"# elapsed=%s embed=%dms retrieve=%dms synth=%dms eval_tokens=%d tokens/sec=%.1f retrieved=%d attempts=%d remaining=%d (B=%d W=%d I=%d)\n",
		res.Elapsed.Round(time.Millisecond), res.EmbedMs, res.RetrieveMs, res.SynthMs,
		res.SynthEvalCount, res.SynthTokensSec, res.RetrievedCount,
		res.CiteAttempts, len(res.RemainingCiteViolations),
		blockers, warnings, infos)
	for _, v := range res.RemainingCiteViolations {
		fmt.Fprintf(os.Stderr, "# %s [%s] %s @ %s", v.Severity, v.Code, v.Note, v.Location)
		if v.Value != "" {
			fmt.Fprintf(os.Stderr, "  (%q)", v.Value)
		}
		fmt.Fprintln(os.Stderr)
	}
	// YAML goes to stdout
	fmt.Println(res.YAMLOutput)
	return 0
}
