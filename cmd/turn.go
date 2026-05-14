package cmd

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Turn dispatches `olifant turn <list|show|stats>`.
func Turn(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "olifant turn: missing action (list|show|stats)")
		return 2
	}
	action, rest := args[0], args[1:]
	switch action {
	case "list":
		return turnList(rest)
	case "show":
		return turnShow(rest)
	case "stats":
		return turnStats(rest)
	default:
		fmt.Fprintf(os.Stderr, "olifant turn: unknown action %q\n", action)
		return 2
	}
}

func turnsDir() (string, error) {
	found, ok := findUp("knowledge-base/README.md")
	if !ok {
		return "", fmt.Errorf("knowledge-base not found via cwd ancestors")
	}
	return filepath.Join(filepath.Dir(found), "short-term", "turns"), nil
}

func listTurnFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out) // chronological by name
	return out, nil
}

func turnList(args []string) int {
	fs := flag.NewFlagSet("turn list", flag.ExitOnError)
	limit := fs.Int("n", 20, "show last N (most-recent) turns; 0 for all")
	_ = fs.Parse(args)

	dir, err := turnsDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "turn list:", err)
		return 1
	}
	files, err := listTurnFiles(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "turn list:", err)
		return 1
	}
	start := 0
	if *limit > 0 && len(files) > *limit {
		start = len(files) - *limit
	}
	for _, f := range files[start:] {
		fmt.Println(strings.TrimSuffix(f, ".yaml"))
	}
	fmt.Fprintf(os.Stderr, "# %d turns (%d shown)\n", len(files), len(files)-start)
	return 0
}

func turnShow(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "turn show: missing turn_id")
		return 2
	}
	dir, err := turnsDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "turn show:", err)
		return 1
	}
	id := strings.TrimSuffix(args[0], ".yaml")
	path := filepath.Join(dir, id+".yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "turn show:", err)
		return 1
	}
	os.Stdout.Write(body)
	return 0
}

func turnStats(args []string) int {
	_ = args
	dir, err := turnsDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "turn stats:", err)
		return 1
	}
	files, err := listTurnFiles(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "turn stats:", err)
		return 1
	}
	fmt.Printf("turns dir: %s\n", dir)
	fmt.Printf("total turns: %d\n", len(files))
	if len(files) > 0 {
		fmt.Printf("first: %s\n", strings.TrimSuffix(files[0], ".yaml"))
		fmt.Printf("last:  %s\n", strings.TrimSuffix(files[len(files)-1], ".yaml"))
	}
	return 0
}
