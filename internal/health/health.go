package health

import (
	"context"
	"os"
	"time"
)

type Checker struct {
	heartbeatPath string
	interval      time.Duration
	startTime     time.Time
}

func NewChecker(heartbeatPath string) *Checker {
	return &Checker{
		heartbeatPath: heartbeatPath,
		interval:      30 * time.Second,
		startTime:     time.Now(),
	}
}

func (c *Checker) HeartbeatLoop(ctx context.Context) {
	c.writeHeartbeat()
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.writeHeartbeat()
		}
	}
}

func (c *Checker) writeHeartbeat() {
	os.WriteFile(c.heartbeatPath, []byte(time.Now().Format(time.RFC3339)+"\n"), 0644)
}

func (c *Checker) Uptime() time.Duration {
	return time.Since(c.startTime)
}

func ReadHeartbeat(path string) (time.Time, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, trimNewline(string(data)))
}

func IsStale(path string, threshold time.Duration) bool {
	t, err := ReadHeartbeat(path)
	if err != nil {
		return true
	}
	return time.Since(t) > threshold
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
