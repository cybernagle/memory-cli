package ingest

import "testing"

func TestProjectFromCwd(t *testing.T) {
	cases := map[string]string{
		"/Users/naglezhang/Desktop/Code/makro": "makro",
		"/home/x/projects/car-agent":           "car-agent",
		"~/Desktop/Code/memory-cli":            "memory-cli",
		"":                                     "",
		"/":                                    "",
		"/Users/naglezhang":                    "naglezhang",
	}
	for cwd, want := range cases {
		if got := ProjectFromCwd(cwd); got != want {
			t.Errorf("ProjectFromCwd(%q) = %q, want %q", cwd, got, want)
		}
	}
}

func TestProjectFromClaudeDir(t *testing.T) {
	// Claude encodes cwd as the directory name with dashes. Must decode cleanly,
	// WITHOUT the lossy "-"->"/" step that previously turned "car-agent" into "car/agent".
	cases := map[string]string{
		"-Users-naglezhang-Desktop-Code-makro":      "makro",
		"-Users-naglezhang-Desktop-Code-car-agent":  "car-agent",
		"-Users-naglezhang-Desktop-Code-memory-cli": "memory-cli",
	}
	for dir, want := range cases {
		if got := ProjectFromClaudeDir(dir); got != want {
			t.Errorf("ProjectFromClaudeDir(%q) = %q, want %q", dir, got, want)
		}
	}
}
