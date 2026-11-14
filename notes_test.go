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
	want := "ℹ️ **info**\n\nTook `<secret:TOKEN>` and ``<redacted>``\n\n_Load › Get `<secret:HOST>` · failed, run 3_"
	if got := noteBlock(notes[0]); got != want {
		t.Errorf("noteBlock() =\n%q\nwant\n%q", got, want)
	}
}

// TestNoteBlockEscapesMasksInIndentedNames checks that a name escaped by
// itself would be code, as one of four spaces before it, or a title that
// opens a fence, is escaped as the line it is shown in.
func TestNoteBlockEscapesMasksInIndentedNames(t *testing.T) {
	notes, err := parseNotes([]any{entry("    Load <secret:HOST>", "    Get <redacted>", "passed", "info", "``` Token <secret:TOKEN>", "b")})
	if err != nil {
		t.Fatal(err)
	}
	want := "ℹ️ **info · ``` Token `<secret:TOKEN>`**\n\nb\n\n_    Load `<secret:HOST>` ›     Get `<redacted>` · passed_"
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
	amps := message{text: strings.Repeat("&", maxFallback)}.fallback(0)
	if n := len([]rune(amps)); n > maxFallback || !strings.HasSuffix(amps, "&amp;…") {
		t.Errorf("fallback() of a text of & is %d characters ending %q, want at most %d with whole entities", n, amps[len(amps)-12:], maxFallback)
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
	notes[0].Body += strings.Repeat("x", maxMarkdown-used)
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

// TestNoteBlockEscapesMasks checks that the placeholders of a body are put
// in code spans outside code, and left alone in it, a code span that goes
// on over the lines of a paragraph among it.
func TestNoteBlockEscapesMasks(t *testing.T) {
	notes, err := parseNotes([]any{entry("J", "S", "passed", "info", "", "a `span\nwith <redacted>` and <redacted>\n\n```\ntoken=<redacted>\n```")})
	if err != nil {
		t.Fatal(err)
	}
	want := "ℹ️ **info**\n\na `span\nwith <redacted>` and ``<redacted>``\n\n```\ntoken=<redacted>\n```\n\n_J › S · passed_"
	if got := noteBlock(notes[0]); got != want {
		t.Errorf("noteBlock() =\n%q\nwant\n%q", got, want)
	}
}

// TestNoteBlockEscapesMasksInTables checks that a placeholder between the
// backticks of two cells of a table is put in code, as a table cuts its row
// into cells before reading code spans, and that one in a code span of a
// cell is left alone.
func TestNoteBlockEscapesMasksInTables(t *testing.T) {
	notes, err := parseNotes([]any{entry("J", "S", "passed", "info", "", "| Step | Value |\n|---|---|\n| login | `<secret:TOKEN>` |\n| `x | <redacted> | y` | z |")})
	if err != nil {
		t.Fatal(err)
	}
	want := "ℹ️ **info**\n\n| Step | Value |\n|---|---|\n| login | `<secret:TOKEN>` |\n| `x | ``<redacted>`` | y` | z |\n\n_J › S · passed_"
	if got := noteBlock(notes[0]); got != want {
		t.Errorf("noteBlock() =\n%q\nwant\n%q", got, want)
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
