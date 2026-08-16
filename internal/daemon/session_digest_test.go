
package daemon

import (
	"testing"
)

// TestParseSessionDigest tolerates markdown fences and prose wrappers around the JSON.
func TestParseSessionDigest(t *testing.T) {
	cases := []struct {
		name, in string
		wantErr  bool
	}{
		{"plain", `{"task":"T","entity":"瑞福莱","facet":"cases","summary":"S","lessons":["L1","L2"]}`, false},
		{"fenced", "```json\n{\"task\":\"T\",\"summary\":\"S\",\"lessons\":[]}\n```", false},
		{"prose wrapper", `好的,以下是分析结果: {"task":"T","summary":"S","lessons":[]} 希望有帮助`, false},
		{"no json", "抱歉我无法分析", true},
		{"empty digest", `{"task":"","summary":"","lessons":[]}`, true},
	}
	for _, c := range cases {
		d, err := parseSessionDigest(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: expected error", c.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if d.Task != "T" || d.Summary != "S" {
			t.Errorf("%s: parsed %+v", c.name, d)
		}
		if d.Lessons == nil {
			t.Errorf("%s: lessons should never be nil", c.name)
		}
	}
}
