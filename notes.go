package main

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Levels of a note, as Probe gives them in the notes context.
const (
	levelInfo     = "info"
	levelWarning  = "warning"
	levelCritical = "critical"
)

// levels are the levels in the order the line counting them names them.
var levels = []string{levelCritical, levelWarning, levelInfo}

// emojis mark a note of each level.
var emojis = map[string]string{
	levelCritical: "🚨",
	levelWarning:  "⚠️",
	levelInfo:     "ℹ️",
}

// Slack takes at most maxBlocks blocks in a message, and maxMarkdown
// characters in all its markdown blocks.
const (
	maxBlocks   = 50
	maxMarkdown = 12000
)

// maxFallback bounds the plain text of a message, which Slack cuts when it
// is longer than 40000 characters. The notes of the blocks hold at most
// 12000, so it cuts only a text that is long by itself.
const maxFallback = 40000

// note is an entry of the notes context of Probe.
type note struct {
	job, jobID, step string
	index            int
	status           string
	level            string
	title, body      string
	// run is the run of a job with repeat the note is of, or 0.
	run int
}

// parseNotes reads with.notes, a list of entries of the notes context.
func parseNotes(v any) ([]note, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf(`with.notes must be a list of notes, such as "{{ notes }}", not %T`, v)
	}
	out := make([]note, 0, len(list))
	for i, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("with.notes[%d] must be an entry of notes, not %T", i, e)
		}
		n, err := parseNote(m)
		if err != nil {
			return nil, fmt.Errorf("with.notes[%d]: %w", i, err)
		}
		out = append(out, n)
	}
	return out, nil
}

func parseNote(m map[string]any) (note, error) {
	var n note
	str := func(key string, required bool) (string, error) {
		v, ok := m[key]
		if !ok || v == nil {
			if required {
				return "", fmt.Errorf("%s is missing", key)
			}
			return "", nil
		}
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("%s must be a string, not %T", key, v)
		}
		return s, nil
	}
	var err error
	if n.level, err = str("level", true); err != nil {
		return n, err
	}
	if _, ok := emojis[n.level]; !ok {
		return n, fmt.Errorf("level %q is not one of info, warning or critical", n.level)
	}
	if n.body, err = str("body", true); err != nil {
		return n, err
	}
	if n.title, err = str("title", false); err != nil {
		return n, err
	}
	if n.job, err = str("job", false); err != nil {
		return n, err
	}
	if n.jobID, err = str("job_id", false); err != nil {
		return n, err
	}
	if n.step, err = str("step", false); err != nil {
		return n, err
	}
	if n.status, err = str("status", false); err != nil {
		return n, err
	}
	if n.index, err = number(m, "index"); err != nil {
		return n, err
	}
	if n.run, err = number(m, "run"); err != nil {
		return n, err
	}
	return n, nil
}

// number reads a whole number of m, 0 when it is absent.
func number(m map[string]any, key string) (int, error) {
	switch v := m[key].(type) {
	case nil:
		return 0, nil
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		if v == float64(int(v)) {
			return int(v), nil
		}
	}
	return 0, fmt.Errorf("%s must be a whole number, not %v", key, m[key])
}

// message is what the action posts: text the workflow wrote, and the notes.
type message struct {
	text  string
	notes []note
}

// blocks writes the message as Block Kit markdown blocks: the text, a line
// counting the notes, and a block for each note. When they do not all fit
// in what Slack takes, the last notes are left out, and a block says how
// many. It fails when the text does not fit even without the notes.
func (m message) blocks() ([]map[string]any, int, error) {
	var head []string
	if t := strings.TrimSpace(m.text); t != "" {
		head = append(head, escapeMasks(t))
	}
	if len(m.notes) > 0 {
		head = append(head, "**"+countLine(m.notes)+"**")
	}
	leftOut := func(n int) string {
		if n == 0 {
			return ""
		}
		return fmt.Sprintf("_%s left out, as Slack takes at most %d blocks and %d characters in a message. The run has them all._", plural(n, "more note is", "more notes are"), maxBlocks, maxMarkdown)
	}
	size := utf8.RuneCountInString
	used := 0
	for _, h := range head {
		used += size(h)
	}
	chunks := make([]string, len(m.notes))
	sizes := make([]int, len(m.notes))
	total := 0
	for i, n := range m.notes {
		chunks[i] = noteBlock(n)
		sizes[i] = size(chunks[i])
		total += sizes[i]
	}
	fits := func(k, chars int) bool {
		blocks, extra := len(head)+k, 0
		if k < len(chunks) {
			blocks++
			extra = size(leftOut(len(chunks) - k))
		}
		return blocks <= maxBlocks && used+chars+extra <= maxMarkdown
	}
	shown := len(chunks)
	if !fits(shown, total) {
		shown = -1
		chars := 0
		for k := 0; k < len(chunks); k++ {
			if !fits(k, chars) {
				break
			}
			shown = k
			chars += sizes[k]
		}
	}
	if shown < 0 {
		return nil, 0, fmt.Errorf("with.text is too long: the message is more than the %d characters Slack takes even without the notes", maxMarkdown)
	}

	var out []map[string]any
	add := func(s string) { out = append(out, map[string]any{"type": "markdown", "text": s}) }
	for _, h := range head {
		add(h)
	}
	for _, c := range chunks[:shown] {
		add(c)
	}
	if l := leftOut(len(chunks) - shown); l != "" {
		add(l)
	}
	return out, shown, nil
}

// fallback is the plain text of the message, of the first shown notes as
// the blocks show them. Slack shows it in a notification, and a screen
// reader reads it in place of the blocks, so it holds what they do. Slack
// reads &, < and > in it as the start of an entity or a link, so they are
// escaped.
func (m message) fallback(shown int) string {
	var parts []string
	if t := strings.TrimSpace(m.text); t != "" {
		parts = append(parts, t)
	}
	if len(m.notes) > 0 {
		parts = append(parts, countLine(m.notes))
	}
	for _, n := range m.notes[:shown] {
		line := n.level
		if t := strings.TrimSpace(n.title); t != "" {
			line += ": " + strings.Join(strings.Fields(t), " ")
		}
		line += fmt.Sprintf(" (%s › %s · %s)\n%s", orDash(n.job), orDash(n.step), stepState(n), strings.TrimSpace(n.body))
		parts = append(parts, line)
	}
	if left := len(m.notes) - shown; left > 0 {
		parts = append(parts, plural(left, "more note is", "more notes are")+" left out.")
	}
	s := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(strings.Join(parts, "\n\n"))
	// It is cut after the escaping, which lengthens it, and not inside an
	// entity.
	if r := []rune(s); len(r) > maxFallback {
		s = string(r[:maxFallback-1])
		if i := strings.LastIndexByte(s, '&'); i >= 0 && !strings.Contains(s[i:], ";") {
			s = s[:i]
		}
		s += "…"
	}
	return s
}

// noteBlock writes n: its emoji and title, its body, and a line naming its
// job, its step, and the status of the step.
func noteBlock(n note) string {
	var b strings.Builder
	b.WriteString(emojis[n.level] + " **" + n.level)
	if t := strings.TrimSpace(n.title); t != "" {
		b.WriteString(" · " + escapeMasks(strings.Join(strings.Fields(t), " ")))
	}
	b.WriteString("**\n\n")
	b.WriteString(escapeMasks(strings.TrimRight(n.body, "\n")))
	fmt.Fprintf(&b, "\n\n_%s › %s · %s_", escapeMasks(orDash(n.job)), escapeMasks(orDash(n.step)), stepState(n))
	return b.String()
}

// countLine says how many notes there are of each level.
func countLine(notes []note) string {
	count := map[string]int{}
	for _, n := range notes {
		count[n.level]++
	}
	var parts []string
	for _, l := range levels {
		if c := count[l]; c > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c, l))
		}
	}
	return fmt.Sprintf("The run left %s: %s.", plural(len(notes), "note", "notes"), strings.Join(parts, ", "))
}

// stepState is the status of the step of n, with its run.
func stepState(n note) string {
	s := orDash(n.status)
	if n.run > 0 {
		s += fmt.Sprintf(", run %d", n.run)
	}
	return s
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// masks are what Probe writes in place of a value it hides: <secret:NAME>
// and <redacted>.
var masks = regexp.MustCompile(`<(secret:[A-Za-z_][A-Za-z0-9_]*|redacted)>`)

// fence opens or closes a fenced code block: three or more backticks or
// tildes, indented by at most three spaces, and what follows them.
var fence = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})(.*)$")

// escapeMasks puts the masks in s in code spans, as Slack would take them
// for links or mentions, so that a reader sees where a value was hidden. In
// a code block or a code span a mask is shown as it is, so it is left alone
// there.
func escapeMasks(s string) string {
	lines := strings.Split(s, "\n")
	// open is the fence of the code block the line is in, or empty.
	var open string
	// para is where the paragraph the line is in starts, or -1. A code
	// span can go on over lines of a paragraph, so a paragraph is escaped
	// as one text.
	para := -1
	flush := func(end int) {
		if para >= 0 {
			text := escapeOutsideSpans(strings.Join(lines[para:end], "\n"))
			copy(lines[para:end], strings.Split(text, "\n"))
			para = -1
		}
	}
	for i, line := range lines {
		if m := fence.FindStringSubmatch(line); m != nil {
			switch {
			case open == "" && !(m[1][0] == '`' && strings.Contains(m[2], "`")):
				flush(i)
				open = m[1]
				continue
			case open != "" && m[1][0] == open[0] && len(m[1]) >= len(open) && strings.TrimSpace(m[2]) == "":
				open = ""
				continue
			}
		}
		if open != "" {
			continue
		}
		if strings.TrimSpace(line) == "" {
			flush(i)
			continue
		}
		if para < 0 {
			para = i
		}
	}
	flush(len(lines))
	return strings.Join(lines, "\n")
}

// escapeOutsideSpans escapes the masks of a paragraph outside its code
// spans. A
// code span opens with a run of backticks that is not escaped by a
// backslash, and closes with the next run of the same length; a run with no
// such closer is text.
func escapeOutsideSpans(line string) string {
	// The span a mask is put in is of more backticks than any run of the
	// paragraph, so that no run left unmatched before it can close it.
	delim := strings.Repeat("`", longestRun(line)+1)
	var b strings.Builder
	text := 0
	// escape escapes the text from text to end, which a code span may close
	// right before it and open right after it.
	escape := func(end int) string {
		return escapeIn(line[text:end], delim, text > 0 && line[text-1] == '`', end < len(line) && line[end] == '`')
	}
	for i := 0; i < len(line); {
		if line[i] == '\\' {
			// A backslash makes the character after it text, so an escaped
			// backtick opens no code span.
			i += 2
			continue
		}
		if line[i] != '`' {
			i++
			continue
		}
		n := ticks(line, i)
		end := closer(line, i+n, n)
		if end < 0 {
			i += n
			continue
		}
		b.WriteString(escape(i))
		b.WriteString(line[i : end+n])
		i, text = end+n, end+n
	}
	b.WriteString(escape(len(line)))
	return b.String()
}

// ticks is the length of the run of backticks at i.
func ticks(s string, i int) int {
	n := 0
	for i+n < len(s) && s[i+n] == '`' {
		n++
	}
	return n
}

// closer is where the first run of exactly n backticks from i starts, or -1.
func closer(s string, i, n int) int {
	for i < len(s) {
		if s[i] != '`' {
			i++
			continue
		}
		run := ticks(s, i)
		if run == n {
			return i
		}
		i += run
	}
	return -1
}

// escapeIn puts each mask of s in a code span delimited by delim, where
// Slack shows it as it is rather than take it for a link or a mention. A
// backslash right before a mask would escape the opening backtick, so it is
// escaped itself, and shown. A mask at the start of s after a code span,
// or at its end before one, is kept apart from it by a space, as the
// backticks of the two would make one run.
func escapeIn(s, delim string, tickBefore, tickAfter bool) string {
	var b strings.Builder
	last := 0
	for _, m := range masks.FindAllStringIndex(s, -1) {
		before := s[last:m[0]]
		if trailingBackslashes(before)%2 == 1 {
			before += "\\"
		}
		if m[0] == 0 && tickBefore {
			before = " "
		}
		b.WriteString(before)
		b.WriteString(delim + s[m[0]:m[1]] + delim)
		if m[1] == len(s) && tickAfter {
			b.WriteString(" ")
		}
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

func trailingBackslashes(s string) int {
	n := 0
	for n < len(s) && s[len(s)-1-n] == '\\' {
		n++
	}
	return n
}

// longestRun is the length of the longest run of backticks in s.
func longestRun(s string) int {
	longest := 0
	for i := 0; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		n := ticks(s, i)
		longest = max(longest, n)
		i += n
	}
	return longest
}
