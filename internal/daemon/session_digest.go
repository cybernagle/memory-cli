package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/cybernagle/memory-cli/internal/llm"
	"github.com/cybernagle/memory-cli/internal/store"
)

// SessionDigestTask builds the per-session projection (session_views): for each session
// with new (unconsumed) memories, it feeds the session's memories to the LLM and derives
// {task, entity, facet, summary, lessons} — the "what did I do here and what did I learn"
// view over the event log. This is CQRS read model #2: memories are the fact view,
// session_views are the task/experience view.
//
// Idempotent per memory (ConsumerSessionDigest bit): a session is re-digested only when
// it gains new memories; the digest then replaces the previous row wholesale.
type SessionDigestTask struct {
	LLM   *llm.Client
	Store *store.SqliteStore
	// Limit overrides sessionDigestPerTick. The one-off `memory sessions --build N` sets
	// this high to drain backlog; the daemon leaves it 0 for the per-tick cap.
	Limit int
}

const (
	sessionDigestPerTick  = 3  // sessions per daemon tick (each is one LLM call)
	sessionDigestMemories = 60 // max memories fed into one digest prompt
)

func (t *SessionDigestTask) Name() string { return "session-digest" }

func (t *SessionDigestTask) Run(s store.Store) (int, error) {
	if t.LLM == nil || t.Store == nil {
		return 0, nil
	}
	limit := t.Limit
	if limit == 0 {
		limit = sessionDigestPerTick
	}

	sessions, err := t.Store.SessionsWithUnconsumed("session-digest", limit)
	if err != nil {
		return 0, err
	}
	if len(sessions) == 0 {
		return 0, nil
	}
	log.Printf("[session-digest] %d sessions to digest", len(sessions))

	digested := 0
	for _, ref := range sessions {
		ok, err := t.digestSession(ref)
		if err != nil {
			log.Printf("[session-digest] session %s: %v (will retry next tick)", ref.SessionID, err)
			continue // not marked consumed → retried next tick
		}
		if ok {
			digested++
		}
	}
	return digested, nil
}

func (t *SessionDigestTask) digestSession(ref store.SessionRef) (bool, error) {
	memories, err := t.Store.ListMemoriesBySession(ref.SessionID, sessionDigestMemories)
	if err != nil {
		return false, err
	}
	if len(memories) == 0 {
		// No digestable material (e.g. everything deduped away) — mark consumed to avoid
		// an infinite retry loop on an empty group.
		t.markConsumed(ref.SessionID)
		return false, nil
	}

	// Prompt assembly: oldest-first, one line per memory, time + phase + content (truncated).
	var sb strings.Builder
	sb.WriteString(sessionDigestPrompt)
	for i, m := range memories {
		content := m.Content
		runes := []rune(content)
		if len(runes) > 300 {
			content = string(runes[:300]) + "…"
		}
		sb.WriteString(fmt.Sprintf("%d. [%s|%s] %s\n", i+1,
			m.CreatedAt.Format("01-02 15:04"), m.Phase, content))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := t.LLM.ChatWithModel(ctx, "glm-4.7-flash", sb.String(), 1000)
	if err != nil {
		return false, fmt.Errorf("llm: %w", err)
	}

	d, err := parseSessionDigest(resp)
	if err != nil {
		return false, fmt.Errorf("parse digest: %w", err)
	}

	lessons, _ := json.Marshal(d.Lessons)
	view := &store.SessionView{
		SessionID:   ref.SessionID,
		Project:     ref.Project,
		TmuxSession: ref.TmuxSession,
		FirstSeen:   memories[0].CreatedAt.Format(time.RFC3339),
		LastSeen:    memories[len(memories)-1].CreatedAt.Format(time.RFC3339),
		MemoryCount: len(memories),
		Task:        d.Task,
		Entity:      d.Entity,
		Facet:       d.Facet,
		Summary:     d.Summary,
		Lessons:     string(lessons),
		Model:       "glm-4.7-flash",
	}
	if err := t.Store.UpsertSessionView(view); err != nil {
		return false, err
	}
	t.markConsumed(ref.SessionID)
	return true, nil
}

// markConsumed sets the session-digest bit on every memory of the session.
func (t *SessionDigestTask) markConsumed(sessionID string) {
	memories, err := t.Store.ListMemoriesBySession(sessionID, 0)
	if err != nil {
		return
	}
	for _, m := range memories {
		t.Store.MarkConsumed(m.ID, "session-digest")
	}
}

const sessionDigestPrompt = `你是工作日志分析器。以下是从一个编码会话(session)中提取的记忆条目,按时间排序。
请分析这个会话,输出严格的 JSON(不要 markdown 代码块,不要解释):
{"task": "这次会话在做什么任务(一句话,动词开头)", "entity": "工作围绕的主要主体,如公司/项目名(瑞福莱, marco, juli, memory-cli 等;没有明确主体则空字符串)", "facet": "主体的哪个侧面(如 product/about/cases/company/infra/数据流 等;无则空)", "summary": "这个会话具体做了什么、结果如何(3-5句话)", "lessons": ["可复用的工作经验、踩过的坑或方法教训,每条一句话;只写真正有复用价值的,没有则空数组"]}

注意:
- lessons 是给别人(未来的你和 AI 助手)看的,要具体可执行,不要空话
- entity/facet 填英文或简短中文,便于按此过滤
- 记忆条目可能杂乱(包含工具输出片段),聚焦人的实际工作和决策

记忆条目:
`

// sessionDigest is the LLM's parsed response.
type sessionDigest struct {
	Task    string   `json:"task"`
	Entity  string   `json:"entity"`
	Facet   string   `json:"facet"`
	Summary string   `json:"summary"`
	Lessons []string `json:"lessons"`
}

// parseSessionDigest extracts the first JSON object from an LLM response, tolerating
// markdown fences and prose wrappers.
func parseSessionDigest(resp string) (*sessionDigest, error) {
	start := strings.Index(resp, "{")
	end := strings.LastIndex(resp, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object in response")
	}
	var d sessionDigest
	if err := json.Unmarshal([]byte(resp[start:end+1]), &d); err != nil {
		return nil, err
	}
	if strings.TrimSpace(d.Task) == "" && strings.TrimSpace(d.Summary) == "" {
		return nil, fmt.Errorf("empty digest")
	}
	if d.Lessons == nil {
		d.Lessons = []string{}
	}
	return &d, nil
}
