package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

// TestZcodePromptDedup is the regression test for the "10 duplicate messages" bug. zcode writes
// the FULL sliding message window on every turn, so the same user prompt reappears in every
// subsequent turn's window. Without dedup in parseZcodeRollout, a single prompt gets prepended
// ("Q: ...") to every following assistant turn → N memories with the same Q but different A →
// distinct content_hashes → the store's content_hash dedup can't catch them.
//
// The fix: track the last-paired prompt and pair a prompt with ONLY the first turn after it
// appears; continuation turns (tool calls / multi-part answers) get just the answer, no Q.
func TestZcodePromptDedup(t *testing.T) {
	dir := t.TempDir()

	// Build a rollout where one user prompt is followed by 5 assistant turns. All 5 turns carry
	// the SAME sliding window (so the prompt appears in all of them). Only the FIRST turn should
	// be prefixed "Q: ..."; the other 4 must be plain answers.
	prompt := `{"type":"model_io","sessionId":"s1","startedAt":"2026-06-28T04:14:00Z","model":{"modelId":"glm"},"request":{"messages":[{"role":"user","content":"帮我修复 bug"}]},"response":{"text":"这是第一个回答，包含足够长度的正文内容。","finishReason":"stop"}}`
	// Turns 2-5: window still contains the same "帮我修复 bug" (sliding window), but the
	// assistant keeps producing new text (tool-call follow-ups).
	for i := 0; i < 4; i++ {
		prompt += "\n" + `{"type":"model_io","sessionId":"s1","startedAt":"2026-06-28T04:14:0` + string(rune('1'+i)) + `Z","model":{"modelId":"glm"},"request":{"messages":[{"role":"user","content":"帮我修复 bug"}]},"response":{"text":"后续回答第` + string(rune('①'+i)) + `部分，内容足够长可以入库。","finishReason":"stop"}}`
	}
	os.WriteFile(filepath.Join(dir, "model-io-sess_test1.jsonl"), []byte(prompt), 0644)

	a := &ZcodeAdapter{Path: dir}
	memories, err := a.Ingest()
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(memories) == 0 {
		t.Fatal("expected memories, got 0")
	}

	// Count how many are Q-prefixed with the repeated prompt.
	qPrefixed := 0
	for _, m := range memories {
		if len(m.Content) > 3 && m.Content[:3] == "Q: " && containsStr(m.Content, "帮我修复 bug") {
			qPrefixed++
		}
	}
	if qPrefixed != 1 {
		t.Errorf("prompt should be paired (Q-prefixed) exactly ONCE, got %d times (duplicate-Q bug). memories: %d", qPrefixed, len(memories))
	}

	// The remaining turns must NOT carry the Q prefix (plain answers).
	if len(memories) > 1 {
		for _, m := range memories[1:] {
			if len(m.Content) > 3 && m.Content[:3] == "Q: " {
				t.Errorf("continuation turn should be a plain answer, got Q-prefixed: %q", m.Content[:40])
			}
		}
	}
}

// TestZcodeNewPromptResets verifies that when a GENUINELY new prompt appears after a chain, it
// DOES get paired again (the dedup shouldn't over-suppress).
func TestZcodeNewPromptResets(t *testing.T) {
	dir := t.TempDir()
	rollout := `{"type":"model_io","sessionId":"s2","startedAt":"2026-06-28T05:00:00Z","model":{"modelId":"glm"},"request":{"messages":[{"role":"user","content":"第一个问题"}]},"response":{"text":"第一个回答，足够长的正文内容。","finishReason":"stop"}}
{"type":"model_io","sessionId":"s2","startedAt":"2026-06-28T05:00:10Z","model":{"modelId":"glm"},"request":{"messages":[{"role":"user","content":"第一个问题"}]},"response":{"text":"第一个问题的后续回答，足够长。","finishReason":"stop"}}
{"type":"model_io","sessionId":"s2","startedAt":"2026-06-28T05:01:00Z","model":{"modelId":"glm"},"request":{"messages":[{"role":"user","content":"第二个完全不同的问题"}]},"response":{"text":"第二个回答，足够长的正文内容。","finishReason":"stop"}}`
	os.WriteFile(filepath.Join(dir, "model-io-sess_test2.jsonl"), []byte(rollout), 0644)

	a := &ZcodeAdapter{Path: dir}
	memories, _ := a.Ingest()

	// Two distinct prompts → two Q-prefixed memories (one per prompt).
	qFirst := 0
	qSecond := 0
	for _, m := range memories {
		if len(m.Content) > 3 && m.Content[:3] == "Q: " {
			if containsStr(m.Content, "第一个问题") {
				qFirst++
			}
			if containsStr(m.Content, "第二个完全不同的问题") {
				qSecond++
			}
		}
	}
	if qFirst != 1 {
		t.Errorf("first prompt should be paired once, got %d", qFirst)
	}
	if qSecond != 1 {
		t.Errorf("second (new) prompt should be paired once, got %d", qSecond)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
