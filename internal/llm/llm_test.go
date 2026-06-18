package llm

import "testing"

func TestBuildConceptTagsPrompt(t *testing.T) {
	prompt := buildConceptTagsPrompt([]string{"前端 message 需要做状态管理", "how to debug a goroutine leak"})

	mustContain := []string{
		"前端 message",     // input content included
		"goroutine",      // second input included
		"Chinese",        // instructs same-language / Chinese tags
		"claude-session", // forbidden-provenance example listed
		"cat:knowledge",  // forbidden-provenance example listed
		"subject",        // tags the subject matter
	}
	for _, s := range mustContain {
		if !contains(prompt, s) {
			t.Errorf("prompt missing %q", s)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
