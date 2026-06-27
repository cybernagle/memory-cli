package ingest

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func findFiles(root, suffix string) ([]string, error) {
	var results []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("warning: walk error at %s: %v", path, err)
			return nil
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), suffix) {
			results = append(results, path)
		}
		return nil
	})
	return results, err
}

func findDirs(root, name string) ([]string, error) {
	var results []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("warning: walk error at %s: %v", path, err)
			return nil
		}
		if info.IsDir() && info.Name() == name {
			results = append(results, path)
		}
		return nil
	})
	return results, err
}

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// ProjectFromCwd derives a short, lowercased project name from a working-directory path.
// e.g. "/Users/naglezhang/Desktop/Code/makro" -> "makro". Returns "" for empty/root paths.
// This is the concrete project anchor (stable, merges case variants) — the full path is
// never stored; only this basename.
func ProjectFromCwd(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" || cwd == "/" {
		return ""
	}
	base := strings.ToLower(strings.TrimSpace(filepath.Base(cwd)))
	if base == "" || base == "." || base == "/" {
		return ""
	}
	return base
}

// ProjectFromClaudeDir derives a project name from a Claude session-storage directory name.
// Claude encodes the cwd as the directory name with dashes, e.g.
// "-Users-naglezhang-Desktop-Code-makro". We trim the home prefix (derived from $HOME so it
// works on any machine, not just this user's) plus the common code-dir parents, then take the
// final segment as the project name. We deliberately do NOT replace "-" with "/" (that lossy
// step produced garbage like "car/agent"), so "-...-car-agent" -> "car-agent".
func ProjectFromClaudeDir(dir string) string {
	base := strings.ToLower(filepath.Base(dir))
	// Build the encoded prefixes to trim, derived from $HOME so it works on any machine instead
	// of hardcoding "-users-naglezhang-". Claude encodes "/Users/naglezhang" as
	// "-users-naglezhang" (drop leading slash, "/" → "-"). Trailing dirs under home that hold
	// code (Desktop/Code, projects, code, dev, src) are stripped too so the project name is the
	// last segment. Order matters: longest/most-specific first.
	prefixes := []string{}
	if home := os.Getenv("HOME"); home != "" {
		encoded := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(home, "/"), "/", "-"))
		prefixes = append(prefixes,
			"-"+encoded+"-desktop-code-",  // ~/Desktop/Code/<project>
			"-"+encoded+"-projects-",      // ~/projects/<project>
			"-"+encoded+"-code-",          // ~/code/<project>
			"-"+encoded+"-",               // ~/<anything>
			"-"+encoded,
		)
	}
	// Broad fallbacks for when $HOME is unset or the transcript came from another machine.
	prefixes = append(prefixes,
		"-users-", "-home-",
		"-desktop-code-", "-projects-", "-code-",
	)
	for _, prefix := range prefixes {
		base = strings.TrimPrefix(base, prefix)
	}
	base = strings.Trim(base, "-")
	return base
}

// CurrentProject returns the project name derived from the process's current working
// directory, or "" if it can't be determined. Used by write entry points (CLI/agent)
// so a memory written from within a project dir is automatically anchored to it.
func CurrentProject() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return ProjectFromCwd(cwd)
}

// CurrentTmuxSession returns the tmux session name the process is running in, or "" if not in tmux.
func CurrentTmuxSession() string {
	if os.Getenv("TMUX") == "" {
		return ""
	}
	out, err := exec.Command("tmux", "display-message", "-p", "#S").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
