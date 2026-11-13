package main

import (
	"strings"
	"testing"
)

func entry(job, step, status, level, title, body string) map[string]any {
	return map[string]any{
		"job": job, "job_id": strings.ToLower(job), "step": step, "index": 0,
		"status": status, "level": level, "title": title, "body": body,
	}
}

func TestNoteBlockOfARun(t *testing.T) {
	e := entry("Load", "Get <secret:HOST>", "failed", "info", "", "Took `<secret:TOKEN>` and <redacted>")
	e["run"] = 3.0
	notes, err := parseNotes([]any{e})
	if err != nil {
		t.Fatal(err)
	}
	want := "ℹ️ **info**\n\nTook `<secret:TOKEN>` and `<redacted>`\n\n_Load › Get `<secret:HOST>` · failed, run 3_"
	if got := noteBlock(notes[0]); got != want {
		t.Errorf("noteBlock() =\n%q\nwant\n%q", got, want)
	}
}

func TestFallbackHoldsTheNotesShown(t *testing.T) {
	notes, err := parseNotes([]any{
		entry("J", "S", "passed", "critical", "Token <secret:TOKEN>", "a & b"),
		entry("J", "T", "failed", "info", "", "c"),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := message{text: "Run <secret:RUN>", notes: notes}.fallback(1)
	want := "Run &lt;secret:RUN&gt;\n\nThe run left 2 notes: 1 critical, 1 info.\n\ncritical: Token &lt;secret:TOKEN&gt; (J › S · passed)\na &amp; b\n\n1 more note is left out."
	if got != want {
		t.Errorf("fallback() =\n%q\nwant\n%q", got, want)
	}
	long := message{text: strings.Repeat("あ", maxFallback+10)}.fallback(0)
	if n := len([]rune(long)); n != maxFallback || !strings.HasSuffix(long, "…") {
		t.Errorf("fallback() of a long text is %d characters, want %d ending with …", n, maxFallback)
	}
}

func TestBlocksKeepEveryNoteThatFits(t *testing.T) {
	// One note that fills what is left after the count line, and so leaves
	// no room for the line saying another is left out, but all fit.
	notes, err := parseNotes([]any{entry("J", "S", "passed", "info", "", "a")})
	if err != nil {
		t.Fatal(err)
	}
	blocks, _, err := message{notes: notes}.blocks()
	if err != nil {
		t.Fatal(err)
	}
	used := 0
	for _, b := range blocks {
		used += len([]rune(b["text"].(string)))
	}
	notes[0].body += strings.Repeat("x", maxMarkdown-used)
	blocks, shown, err := message{notes: notes}.blocks()
	if err != nil || len(blocks) != 2 || shown != 1 {
		t.Errorf("blocks() = %d blocks, error %v; want the count and the note", len(blocks), err)
	}
}

func TestBlocksRefuseATextThatDoesNotFit(t *testing.T) {
	notes, err := parseNotes([]any{entry("J", "S", "passed", "info", "", "b")})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := (message{text: strings.Repeat("x", maxMarkdown), notes: notes}).blocks(); err == nil || !strings.Contains(err.Error(), "with.text is too long") {
		t.Errorf("blocks() error = %v, want one saying with.text is too long", err)
	}
}

func TestEscapeMasks(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"the password <secret:SHOP_PASSWORD> fails", "the password `<secret:SHOP_PASSWORD>` fails"},
		{"sent `<secret:TOKEN>` and <redacted>", "sent `<secret:TOKEN>` and `<redacted>`"},
		{"```\ntoken=<secret:TOKEN>\n```\n<secret:TOKEN>", "```\ntoken=<secret:TOKEN>\n```\n`<secret:TOKEN>`"},
		{"\\` <redacted> x", "\\` `<redacted>` x"},
		{"<https://example.com|a link>", "<https://example.com|a link>"},
	} {
		if got := escapeMasks(tc.in); got != tc.want {
			t.Errorf("escapeMasks(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseNotesRefuses(t *testing.T) {
	for _, tc := range []struct {
		v    any
		want string
	}{
		{"a note", "must be a list of notes"},
		{[]any{"x"}, "with.notes[0] must be an entry"},
		{[]any{entry("J", "S", "passed", "high", "", "b")}, `level "high" is not one of`},
		{[]any{map[string]any{"level": "info"}}, "body is missing"},
		{[]any{map[string]any{"level": "info", "body": "b", "run": 1.5}}, "run must be a whole number"},
	} {
		if _, err := parseNotes(tc.v); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("parseNotes(%v) error = %v, want one with %q", tc.v, err, tc.want)
		}
	}
}
