package daemon

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ParseReminderTime parses natural-language time strings (Chinese + English) into a time.Time.
// It handles the most common ways people express "when" in a hurry. It does NOT try to be a
// full NLU parser — just the high-frequency patterns. Returns the computed time and the
// remainder of the input with the time phrase stripped (so "下午3点去查X" → 15:00 + "去查X").
//
// Supported patterns (checked in order, first match wins):
//   - Relative: "2小时后" / "in 2 hours" / "30分钟后" / "2h后" / "3d后"
//   - Time-of-day: "15:30" / "3pm" / "下午3点" / "下午3点半" / "上午10点" / "15点"
//   - Date + time: "明天下午3点" / "后天上午10点"
//   - Date only: "明天" / "后天" / "tomorrow" / "下周一" / "下周三" / "2026-06-20" / "06-20"
//   - Absolute datetime: "2026-06-20 15:30"
func ParseReminderTime(input string, loc *time.Location) (time.Time, string) {
	now := time.Now().In(loc)
	lower := strings.ToLower(input)

	// --- Relative duration: "2小时后" / "in 2 hours" / "30分钟后" / "2h后" / "3d后" ---
	if t, rest, ok := parseRelativeDuration(lower, input, now); ok {
		return t.In(loc), cleanRemainder(rest, input)
	}

	// --- Date + optional time combos ---
	// First try to extract a date component (明天/后天/下周一/specific date).
	dayOffset, datePhrase, dateFound := extractDate(lower, now)
	hour, minute, timePhrase, timeFound := extractTimeOfDay(lower)

	if dateFound || timeFound {
		// Start from today, apply date offset, then set hour/minute.
		t := now
		if dateFound {
			t = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, dayOffset)
		}
		if timeFound {
			t = time.Date(t.Year(), t.Month(), t.Day(), hour, minute, 0, 0, loc)
			// If only a time was given (no date) and it's already passed today, roll to tomorrow.
			if !dateFound && t.Before(now) {
				t = t.Add(24 * time.Hour)
			}
		}
		// Strip the matched phrases from input to get the content remainder.
		stripPhrase := datePhrase
		if stripPhrase == "" {
			stripPhrase = timePhrase
		} else if timePhrase != "" {
			stripPhrase = datePhrase + "|" + timePhrase
		}
		return t, cleanRemainder(stripTimePhrases(input, stripPhrase), input)
	}

	// --- Absolute datetime: "2026-06-20 15:30" or "2026-06-20" ---
	if t, rest, ok := parseAbsoluteDate(input, loc); ok {
		return t, cleanRemainder(rest, input)
	}

	// Fallback: no time phrase found. Default to 1 hour from now (a reminder with no clear
	// time is still useful — better to fire soon than never).
	return now.Add(time.Hour), input
}

// parseRelativeDuration handles "2小时后", "in 2 hours", "30分钟后", "2h后", "3d后".
func parseRelativeDuration(lower, original string, now time.Time) (time.Time, string, bool) {
	// "N小时后" / "N分钟后" / "N天后" / "N秒后"
	re := regexp.MustCompile(`(\d+)\s*(小时|分钟|分|天|秒|周|hours?|hrs?|minutes?|mins?|days?|d|h|m)\s*(后|after|later|后)??`)
	m := re.FindStringSubmatch(lower)
	if m != nil {
		n, _ := strconv.Atoi(m[1])
		unit := m[2]
		var dur time.Duration
		switch {
		case strings.HasPrefix(unit, "小时") || strings.HasPrefix(unit, "hour") || strings.HasPrefix(unit, "hr") || unit == "h":
			dur = time.Duration(n) * time.Hour
		case strings.HasPrefix(unit, "分钟") || unit == "分" || strings.HasPrefix(unit, "minute") || strings.HasPrefix(unit, "min") || unit == "m":
			dur = time.Duration(n) * time.Minute
		case strings.HasPrefix(unit, "天") || strings.HasPrefix(unit, "day") || unit == "d":
			dur = time.Duration(n) * 24 * time.Hour
		case strings.HasPrefix(unit, "周"):
			dur = time.Duration(n) * 7 * 24 * time.Hour
		case unit == "秒":
			dur = time.Duration(n) * time.Second
		}
		rest := strings.Replace(original, m[0], "", 1)
		// Also handle "in N hours" English form
		return now.Add(dur), rest, true
	}

	// "in 2 hours" / "in 30 minutes"
	reIn := regexp.MustCompile(`in\s+(\d+)\s*(hours?|hrs?|minutes?|mins?|days?|seconds?)`)
	m = reIn.FindStringSubmatch(lower)
	if m != nil {
		n, _ := strconv.Atoi(m[1])
		var dur time.Duration
		switch {
		case strings.HasPrefix(m[2], "hour") || strings.HasPrefix(m[2], "hr"):
			dur = time.Duration(n) * time.Hour
		case strings.HasPrefix(m[2], "minute") || strings.HasPrefix(m[2], "min"):
			dur = time.Duration(n) * time.Minute
		case strings.HasPrefix(m[2], "day"):
			dur = time.Duration(n) * 24 * time.Hour
		case strings.HasPrefix(m[2], "second"):
			dur = time.Duration(n) * time.Second
		}
		rest := strings.Replace(original, m[0], "", 1)
		return now.Add(dur), rest, true
	}

	return now, original, false
}

// extractDate finds a date phrase and returns the day offset from today + the matched phrase.
func extractDate(lower string, now time.Time) (offset int, phrase string, found bool) {
	switch {
	case strings.Contains(lower, "后天"):
		return 2, "后天", true
	case strings.Contains(lower, "明天") || strings.Contains(lower, "tomorrow") || strings.Contains(lower, "tmr"):
		return 1, "明天", true
	case strings.Contains(lower, "后天"):
		return 2, "后天", true
	case strings.Contains(lower, "大后天"):
		return 3, "大后天", true
	case strings.Contains(lower, "今天") || strings.Contains(lower, "today"):
		return 0, "今天", true
	}

	// "下周一" ~ "下周日"
	reNextWeek := regexp.MustCompile(`下周([一二三四五六日天])`)
	m := reNextWeek.FindStringSubmatch(lower)
	if m != nil {
		target := chineseWeekday(m[1])
		if target >= 0 {
			daysUntil := (target - int(now.Weekday()) + 7) % 7
			if daysUntil == 0 {
				daysUntil = 7 // next week's same day
			}
			return daysUntil, m[0], true
		}
	}

	return 0, "", false
}

// extractTimeOfDay finds a time-of-day phrase and returns hour, minute, phrase.
func extractTimeOfDay(lower string) (hour, minute int, phrase string, found bool) {
	// "下午3点半" / "下午3点30" / "上午10点" / "下午3点"
	reCN := regexp.MustCompile(`(上午|下午|傍晚|晚上|中午)?\s*(\d{1,2})\s*点\s*(\d{1,2})?\s*半?`)
	if m := reCN.FindStringSubmatch(lower); m != nil {
		h, _ := strconv.Atoi(m[2])
		ampm := m[1]
		if ampm == "下午" || ampm == "傍晚" || ampm == "晚上" {
			if h < 12 {
				h += 12
			}
		} else if ampm == "上午" && h == 12 {
			h = 0
		}
		min := 0
		if m[3] != "" {
			min, _ = strconv.Atoi(m[3])
		} else if strings.Contains(m[0], "半") {
			min = 30
		}
		return h, min, m[0], true
	}

	// "15点" (24h without 点 suffix time, already handled above). Fallback pure "15:30"
	reColon := regexp.MustCompile(`\b(\d{1,2}):(\d{2})\b`)
	if m := reColon.FindStringSubmatch(lower); m != nil {
		h, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		return h, min, m[0], true
	}

	// "3pm" / "3am" / "3 p.m."
	reAMPM := regexp.MustCompile(`\b(\d{1,2})\s*(am|pm|a\.m\.|p\.m\.)`)
	if m := reAMPM.FindStringSubmatch(lower); m != nil {
		h, _ := strconv.Atoi(m[1])
		if strings.HasPrefix(m[2], "p") && h < 12 {
			h += 12
		}
		if strings.HasPrefix(m[2], "a") && h == 12 {
			h = 0
		}
		return h, 0, m[0], true
	}

	// "15点" pure (no AM/PM marker)
	re24h := regexp.MustCompile(`\b(\d{1,2})\s*点\b`)
	if m := re24h.FindStringSubmatch(lower); m != nil {
		h, _ := strconv.Atoi(m[1])
		if h >= 0 && h <= 23 {
			return h, 0, m[0], true
		}
	}

	return 0, 0, "", false
}

// parseAbsoluteDate handles "2026-06-20 15:30" or "2026-06-20" or "06-20".
func parseAbsoluteDate(input string, loc *time.Location) (time.Time, string, bool) {
	now := time.Now().In(loc)

	// "2026-06-20 15:30"
	reFull := regexp.MustCompile(`(\d{4}-\d{2}-\d{2})\s+(\d{1,2}):(\d{2})`)
	if m := reFull.FindStringSubmatch(input); m != nil {
		t, err := time.ParseInLocation("2006-01-02 15:04", m[1]+" "+m[2]+":"+m[3], loc)
		if err == nil {
			return t, strings.Replace(input, m[0], "", 1), true
		}
	}

	// "2026-06-20"
	reDate := regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})\b`)
	if m := reDate.FindStringSubmatch(input); m != nil {
		t, err := time.ParseInLocation("2006-01-02", m[0], loc)
		if err == nil {
			return t, strings.Replace(input, m[0], "", 1), true
		}
	}

	// "06-20" (this year)
	reShort := regexp.MustCompile(`\b(\d{2})-(\d{2})\b`)
	if m := reShort.FindStringSubmatch(input); m != nil {
		t, err := time.ParseInLocation("2006-01-02", strconv.Itoa(now.Year())+"-"+m[0], loc)
		if err == nil {
			return t, strings.Replace(input, m[0], "", 1), true
		}
	}

	return now, input, false
}

func chineseWeekday(s string) int {
	switch s {
	case "日", "天":
		return 0
	case "一":
		return 1
	case "二":
		return 2
	case "三":
		return 3
	case "四":
		return 4
	case "五":
		return 5
	case "六":
		return 6
	}
	return -1
}

// stripTimePhrases removes the date/time phrases from the original input, leaving the content.
func stripTimePhrases(input, phrases string) string {
	out := input
	for _, p := range strings.Split(phrases, "|") {
		if p == "" {
			continue
		}
		out = strings.ReplaceAll(out, p, "")
	}
	return out
}

func cleanRemainder(processed, original string) string {
	r := strings.TrimSpace(processed)
	// Clean up leftover particles/connectors after stripping time phrases.
	r = strings.TrimLeft(r, "去、，, 。.的 ")
	r = strings.TrimRight(r, " ，,。.")
	if r == "" {
		return original
	}
	return r
}
