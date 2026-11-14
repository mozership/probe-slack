package main

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/mozership/probe/actionrpc"
	"github.com/mozership/probe/mask/markdown"
)

// levels are the levels in the order the line counting them names them.
var levels = []string{actionrpc.NoteCritical, actionrpc.NoteWarning, actionrpc.NoteInfo}

// emojis mark a note of each level.
var emojis = map[string]string{
	actionrpc.NoteCritical: "🚨",
	actionrpc.NoteWarning:  "⚠️",
	actionrpc.NoteInfo:     "ℹ️",
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

// parseNotes reads with.notes, a list of entries of the notes context.
func parseNotes(v any) ([]actionrpc.Note, error) {
	notes, err := actionrpc.ParseNotes(v)
	if err != nil {
		// The error starts with notes, as notes[1]: body is missing.
		return nil, fmt.Errorf("with.%w", err)
	}
	return notes, nil
}

// message is what the action posts: text the workflow wrote, and the notes.
type message struct {
	text  string
	notes []actionrpc.Note
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
		line := n.Level
		if t := strings.TrimSpace(n.Title); t != "" {
			line += ": " + strings.Join(strings.Fields(t), " ")
		}
		line += fmt.Sprintf(" (%s › %s · %s)\n%s", orDash(n.Job), orDash(n.Step), stepState(n), strings.TrimSpace(n.Body))
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
func noteBlock(n actionrpc.Note) string {
	var b strings.Builder
	b.WriteString(emojis[n.Level] + " **" + n.Level)
	if t := strings.TrimSpace(n.Title); t != "" {
		b.WriteString(" · " + escapeMasks(strings.Join(strings.Fields(t), " ")))
	}
	b.WriteString("**\n\n")
	b.WriteString(escapeMasks(strings.TrimRight(n.Body, "\n")))
	fmt.Fprintf(&b, "\n\n_%s › %s · %s_", escapeMasks(orDash(n.Job)), escapeMasks(orDash(n.Step)), stepState(n))
	return b.String()
}

// countLine says how many notes there are of each level.
func countLine(notes []actionrpc.Note) string {
	count := map[string]int{}
	for _, n := range notes {
		count[n.Level]++
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
func stepState(n actionrpc.Note) string {
	s := orDash(n.Status)
	if n.Run > 0 {
		s += fmt.Sprintf(", run %d", n.Run)
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

// escapeMasks puts the placeholders of the values Probe hides in s,
// <secret:NAME> and <redacted>, in code spans, as Slack would take them for
// links or mentions, so that a reader sees where a value was hidden.
func escapeMasks(s string) string {
	return markdown.Escape(s, markdown.AsCode)
}
