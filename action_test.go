package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/mozership/probe-slack/internal/fakeslack"
	"github.com/mozership/probe/actionref"
	"github.com/mozership/probe/actionrpc"
)

const token = "xoxb-test"

func slack(t *testing.T) (*fakeslack.Server, *httptest.Server) {
	t.Helper()
	fake := fakeslack.New(token, "C0NOTES", "C0OTHER")
	ts := httptest.NewServer(fake)
	t.Cleanup(ts.Close)
	return fake, ts
}

func someNotes() []any {
	return []any{
		entry("Sign-up", "A new user signs up", "passed", "critical", "No + in an address", "The form refuses `qa+1@example.com`."),
		entry("Checkout", "Pay", "failed", "warning", "", "The total updates late."),
	}
}

func res(t *testing.T, ret map[string]any) map[string]any {
	t.Helper()
	r, ok := ret["res"].(map[string]any)
	if !ok {
		t.Fatalf("ret = %v, want res in it", ret)
	}
	return r
}

func TestWebhook(t *testing.T) {
	fake, ts := slack(t)
	ret, err := (&Action{}).Run(map[string]any{
		"webhook_url": ts.URL + fakeslack.WebhookPath,
		"text":        "Notes of the nightly run",
		"notes":       someNotes(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := res(t, ret); r["posted"] != true || r["code"] != 200 || r["ts"] != "" {
		t.Errorf("res = %v, want posted, with no ts from a webhook", r)
	}
	msgs := fake.Messages()
	if len(msgs) != 1 || msgs[0].Via != "webhook" || msgs[0].Channel != "C0NOTES" {
		t.Fatalf("messages = %v, want one by the webhook", msgs)
	}
	want := []string{
		"Notes of the nightly run",
		"**The run left 2 notes: 1 critical, 1 warning.**",
		"🚨 **critical · No + in an address**\n\nThe form refuses `qa+1@example.com`.\n\n_Sign-up › A new user signs up · passed_",
		"⚠️ **warning**\n\nThe total updates late.\n\n_Checkout › Pay · failed_",
	}
	var got []string
	for _, b := range msgs[0].Blocks {
		got = append(got, b["text"].(string))
	}
	if !slices.Equal(got, want) {
		t.Errorf("blocks =\n%q\nwant\n%q", got, want)
	}
	if msgs[0].Text != "Notes of the nightly run\nThe run left 2 notes: 1 critical, 1 warning." {
		t.Errorf("text = %q", msgs[0].Text)
	}
	if req := ret["req"].(map[string]any); req["via"] != "webhook" || strings.Contains(fmt.Sprint(req), fakeslack.WebhookPath) {
		t.Errorf("req = %v, want the webhook named without its URL", req)
	}
}

func TestToken(t *testing.T) {
	fake, ts := slack(t)
	ret, err := (&Action{}).Run(map[string]any{
		"token":   token,
		"channel": "#C0OTHER",
		"api_url": ts.URL + "/api",
		"notes":   someNotes()[:1],
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := res(t, ret); r["posted"] != true || r["channel"] != "C0OTHER" || r["ts"] != "1700000000.000001" {
		t.Errorf("res = %v, want the channel and the ts of the message", r)
	}
	if msgs := fake.Messages(); len(msgs) != 1 || msgs[0].Via != "token" || !strings.Contains(msgs[0].Markdown(), "No + in an address") {
		t.Errorf("messages = %v", msgs)
	}
	if req := ret["req"].(map[string]any); req["via"] != "token" || req["channel"] != "#C0OTHER" {
		t.Errorf("req = %v", req)
	}
	for _, v := range ret["req"].(map[string]any) {
		if v == token {
			t.Errorf("req = %v, want no token in it", ret["req"])
		}
	}
}

func TestNothingToPost(t *testing.T) {
	fake, ts := slack(t)
	ret, err := (&Action{}).Run(map[string]any{"webhook_url": ts.URL + fakeslack.WebhookPath, "notes": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	if r := res(t, ret); r["posted"] != false || fake.Requests != 0 {
		t.Errorf("res = %v, %d requests; want nothing posted", r, fake.Requests)
	}

	// A text is posted, with no notes or with none left.
	for _, with := range []map[string]any{
		{"text": "The nightly run failed"},
		{"text": "The nightly run failed", "notes": []any{}},
	} {
		with["webhook_url"] = ts.URL + fakeslack.WebhookPath
		if _, err := (&Action{}).Run(with); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range fake.Messages() {
		if len(m.Blocks) != 1 || m.Markdown() != "The nightly run failed" {
			t.Errorf("message = %v, want the text alone", m)
		}
	}
}

func TestSlackErrorsFailTheStep(t *testing.T) {
	_, ts := slack(t)
	for _, tc := range []struct {
		with map[string]any
		want string
	}{
		{map[string]any{"token": token, "channel": "C0GONE", "api_url": ts.URL + "/api", "notes": someNotes()}, "chat.postMessage: Slack answered channel_not_found"},
		{map[string]any{"token": "xoxb-wrong", "channel": "C0NOTES", "api_url": ts.URL + "/api", "notes": someNotes()}, "invalid_auth"},
		{map[string]any{"webhook_url": ts.URL + "/services/T000/B000/removed", "notes": someNotes()}, "the webhook answered 404 Not Found: no_service"},
	} {
		if _, err := (&Action{}).Run(tc.with); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("error = %v, want one with %q", err, tc.want)
		}
	}
}

func TestTooManyRequests(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(ts.Close)
	_, err := (&Action{}).Run(map[string]any{"webhook_url": ts.URL + "/services/x", "text": "hi"})
	if err == nil || !strings.Contains(err.Error(), "429 Too Many Requests: it takes messages again in 30 seconds") {
		t.Errorf("error = %v", err)
	}
}

// TestManyNotesFitWhatSlackTakes checks that a message of more notes than
// Slack takes leaves out the last, which the stand-in would refuse
// otherwise, as Slack does.
func TestManyNotesFitWhatSlackTakes(t *testing.T) {
	fake, ts := slack(t)
	var many, long []any
	for range 80 {
		many = append(many, entry("J", "S", "passed", "info", "", "short"))
	}
	for range 5 {
		long = append(long, entry("J", "S", "passed", "info", "", strings.Repeat("x", 4000)))
	}
	for _, notes := range [][]any{many, long} {
		if _, err := (&Action{}).Run(map[string]any{"webhook_url": ts.URL + fakeslack.WebhookPath, "notes": notes}); err != nil {
			t.Fatal(err)
		}
	}
	msgs := fake.Messages()
	if len(msgs[0].Blocks) != 50 || !strings.Contains(msgs[0].Markdown(), "32 more notes are left out") {
		t.Errorf("message of 80 notes has %d blocks, and says %q", len(msgs[0].Blocks), msgs[0].Blocks[len(msgs[0].Blocks)-1]["text"])
	}
	if !strings.Contains(msgs[1].Markdown(), "3 more notes are left out") {
		t.Errorf("message of long notes does not leave out 3:\n%s", msgs[1].Markdown()[len(msgs[1].Markdown())-200:])
	}
}

func TestReadOnlyRefuses(t *testing.T) {
	fake, ts := slack(t)
	_, _, err := (&Action{}).RunStep(actionrpc.Call{With: map[string]any{"webhook_url": ts.URL + fakeslack.WebhookPath, "notes": someNotes()}, Guard: actionrpc.Guard{ReadOnly: true}})
	if !actionrpc.IsRefused(err) || fake.Requests != 0 {
		t.Errorf("error = %v, %d requests; want it refused before any", err, fake.Requests)
	}
}

func TestAllowHost(t *testing.T) {
	fake, ts := slack(t)
	u, _ := url.Parse(ts.URL)
	with := map[string]any{"webhook_url": ts.URL + fakeslack.WebhookPath, "notes": someNotes()}
	_, _, err := (&Action{}).RunStep(actionrpc.Call{With: with, Guard: actionrpc.Guard{AllowHosts: []string{"hooks.slack.com"}}})
	if !actionrpc.IsRefused(err) || fake.Requests != 0 {
		t.Errorf("error = %v, %d requests; want it refused before any", err, fake.Requests)
	}
	if strings.Contains(err.Error(), fakeslack.WebhookPath) {
		t.Errorf("error = %v, want the webhook not told", err)
	}
	if _, _, err := (&Action{}).RunStep(actionrpc.Call{With: with, Guard: actionrpc.Guard{AllowHosts: []string{u.Host}}}); err != nil {
		t.Errorf("error = %v, want the host of the webhook allowed", err)
	}
}

// TestRedirectOfThePostFails checks that a POST redirected by a 301 is not
// sent again as a GET, which would post nothing.
func TestRedirectOfThePostFails(t *testing.T) {
	_, ts := slack(t)
	moved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, ts.URL+r.URL.Path, http.StatusMovedPermanently)
	}))
	t.Cleanup(moved.Close)
	_, err := (&Action{}).Run(map[string]any{"webhook_url": moved.URL + fakeslack.WebhookPath, "text": "hi"})
	if err == nil || !strings.Contains(err.Error(), "redirected the POST") || strings.Contains(err.Error(), fakeslack.WebhookPath) {
		t.Errorf("error = %v, want one saying the POST was redirected, without the webhook", err)
	}
}

func TestDoesNotReturnNotes(t *testing.T) {
	_, ts := slack(t)
	ret, err := (&Action{}).Run(map[string]any{"webhook_url": ts.URL + fakeslack.WebhookPath, "notes": someNotes()})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res(t, ret)["notes"]; ok {
		t.Errorf("res = %v, want no notes in it, which Probe would leave again", ret["res"])
	}
}

func TestRequestRefuses(t *testing.T) {
	const hook = "https://hooks.slack.com/services/T/B/secret"
	for _, tc := range []struct {
		with map[string]any
		want string
	}{
		{map[string]any{"text": "hi"}, "requires with.webhook_url, or with.token and with.channel"},
		{map[string]any{"token": token, "text": "hi"}, "requires with.webhook_url, or with.token and with.channel"},
		{map[string]any{"webhook_url": hook, "token": token, "channel": "C", "text": "hi"}, "not both"},
		{map[string]any{"webhook_url": "hooks.slack.com/secret", "text": "hi"}, "with.webhook_url must be an http or https URL"},
		{map[string]any{"webhook_url": hook, "api_url": "https://slack.com/api", "text": "hi"}, "with.api_url is for with.token"},
		{map[string]any{"token": token, "channel": "C", "api_url": "ftp://x", "text": "hi"}, "with.api_url must be"},
		{map[string]any{"webhook_url": hook}, "requires with.text or with.notes"},
		{map[string]any{"webhook_url": hook, "text": " "}, "requires with.text or with.notes"},
		{map[string]any{"webhook_url": hook, "notes": "{{ notes }}"}, "must be a list of notes"},
		{map[string]any{"webhook_url": hook, "text": 1}, "with.text must be a string"},
		{map[string]any{"webhook_url": hook, "text": "hi", "timeout": "soon"}, "with.timeout"},
		{map[string]any{"webhook_url": hook, "text": "hi", "channle": "C"}, "not channle"},
	} {
		_, err := (&Action{}).Run(tc.with)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Run(%v) error = %v, want one with %q", tc.with, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Errorf("error = %v, want the webhook not told", err)
		}
	}
}

// TestManifestsDeclare checks that action.yml, and the one the e2e workflows
// run the action by, declare the params and the guard the action has, so
// that probe check and the guard of a run take it as it is. probe manifest
// keeps what action.yml declares in the one of a release.
func TestManifestsDeclare(t *testing.T) {
	for _, name := range []string{"action.yml", "e2e/action/action.yml"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		m, err := actionref.ParseManifest(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !slices.Equal(m.Params, params) || !slices.Equal(m.Guard, keeps) {
			t.Errorf("%s declares params %v and guard %v, want %v and %v", name, m.Params, m.Guard, params, keeps)
		}
	}
}
