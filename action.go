package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/mozership/probe/actionrpc"
)

// DefaultTimeout bounds a step whose with does not set timeout.
const DefaultTimeout = 30 * time.Second

// DefaultAPIURL is the Web API of Slack.
const DefaultAPIURL = "https://slack.com/api"

// params are the keys the action takes in with. action.yml declares them,
// for probe check to report a key the action does not take.
var params = []string{"webhook_url", "token", "channel", "text", "notes", "api_url", "timeout"}

// seconds is a Retry-After of a number of seconds.
var seconds = regexp.MustCompile(`^[0-9]{1,6}$`)

// forLog is with without the URL of a webhook and the token, which the
// log of the action would otherwise hold: Probe hides a value it knows to
// be secret, and these it may not, when the workflow does not list them
// under secrets, nor a part of them, when a long value is cut for the log.
func forLog(with map[string]any) map[string]any {
	out := make(map[string]any, len(with))
	for k, v := range with {
		if k == "webhook_url" || k == "token" {
			v = "<redacted>"
		}
		out[k] = v
	}
	return out
}

// slackError is what an error of Slack looks like, such as no_service or
// invalid_blocks.
var slackError = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// keeps are the kinds of guard the action keeps to, as action.yml declares
// them: it posts only when the run is not read-only, and connects only to a
// host the run allows.
var keeps = []string{actionrpc.KindReadOnly, actionrpc.KindAllowHost}

// Action posts a message to a Slack channel.
type Action struct {
	log hclog.Logger
	// client sends the requests. It defaults to http.DefaultTransport.
	client *http.Client
}

// Run posts the message in with, under no guard.
func (a *Action) Run(with map[string]any) (map[string]any, error) {
	ret, _, err := a.RunStep(actionrpc.Call{With: with})
	return ret, err
}

// RunStep posts the message in call.With under the guard of the run.
func (a *Action) RunStep(call actionrpc.Call) (map[string]any, map[string]any, error) {
	if a.log == nil {
		a.log = hclog.NewNullLogger()
	}
	actionrpc.LogParams(a.log, "received request parameters", forLog(call.With))

	start := time.Now()
	req, err := parseRequest(call.With)
	if err != nil {
		return nil, nil, err
	}
	if call.Guard.ReadOnly {
		return nil, nil, actionrpc.Refuse("the run is read-only, and posting to Slack writes")
	}

	ctx, cancel := context.WithTimeout(context.Background(), req.timeout)
	defer cancel()
	res, err := a.post(ctx, req, call.Guard)
	if err != nil {
		actionrpc.LogOutcome(a.log, "posting to Slack", nil, err)
		return nil, nil, err
	}
	ret := map[string]any{
		"req":    req.forResult(),
		"res":    res.forResult(),
		"rt":     time.Since(start).String(),
		"status": 0,
	}
	actionrpc.LogOutcome(a.log, "posting to Slack", ret, nil)
	return ret, nil, nil
}

type request struct {
	// webhook is the Incoming Webhook to post to, or nil when the message
	// is posted with token to channel.
	webhook *url.URL
	token   string
	channel string
	apiURL  *url.URL
	msg     message
	// hasNotes is whether with gave notes, an empty list included.
	hasNotes bool
	timeout  time.Duration
}

// forResult is what the result tells of the request: neither the webhook,
// whose URL is its secret, nor the token.
func (r *request) forResult() map[string]any {
	m := map[string]any{"notes": len(r.msg.notes), "text": r.msg.text}
	if r.webhook != nil {
		m["via"] = "webhook"
	} else {
		m["via"] = "token"
		m["channel"] = r.channel
		m["api_url"] = r.apiURL.String()
	}
	return m
}

// result is what the action did.
type result struct {
	posted  bool
	channel string
	ts      string
	code    int
}

func (r result) forResult() map[string]any {
	return map[string]any{"posted": r.posted, "channel": r.channel, "ts": r.ts, "code": r.code}
}

func parseRequest(with map[string]any) (*request, error) {
	r := &request{timeout: DefaultTimeout}

	for k := range with {
		if !slices.Contains(params, k) {
			return nil, fmt.Errorf("slack action takes %s, not %s", strings.Join(params, ", "), k)
		}
	}

	webhook, err := optString(with, "webhook_url")
	if err != nil {
		return nil, err
	}
	if r.token, err = optString(with, "token"); err != nil {
		return nil, err
	}
	if r.channel, err = optString(with, "channel"); err != nil {
		return nil, err
	}
	switch {
	case webhook != "" && (r.token != "" || r.channel != ""):
		return nil, errors.New("slack action takes either with.webhook_url, or with.token and with.channel, not both")
	case webhook != "":
		u, err := url.Parse(webhook)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			// The URL is the secret of the webhook, so it is not told.
			return nil, errors.New("with.webhook_url must be an http or https URL")
		}
		if _, ok := with["api_url"]; ok {
			return nil, errors.New("with.api_url is for with.token, not with.webhook_url")
		}
		r.webhook = u
	case r.token == "" || r.channel == "":
		return nil, errors.New("slack action requires with.webhook_url, or with.token and with.channel")
	default:
		api, err := optString(with, "api_url")
		if err != nil {
			return nil, err
		}
		if api == "" {
			api = DefaultAPIURL
		}
		u, err := url.Parse(api)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("with.api_url must be an http or https URL, not %q", api)
		}
		if !strings.HasSuffix(u.Path, "/") {
			u.Path += "/"
		}
		r.apiURL = u
	}

	if r.msg.text, err = optString(with, "text"); err != nil {
		return nil, err
	}
	if v, ok := with["notes"]; ok {
		if r.msg.notes, err = parseNotes(v); err != nil {
			return nil, err
		}
		r.hasNotes = true
	}
	if !r.hasNotes && strings.TrimSpace(r.msg.text) == "" {
		return nil, errors.New(`slack action requires with.text or with.notes, such as "{{ notes }}"`)
	}

	if v, ok := with["timeout"]; ok {
		if r.timeout, err = actionrpc.ParseTimeout(v); err != nil {
			return nil, fmt.Errorf("with.timeout: %w", err)
		}
	}
	return r, nil
}

func (a *Action) post(ctx context.Context, r *request, guard actionrpc.Guard) (result, error) {
	if len(r.msg.notes) == 0 && strings.TrimSpace(r.msg.text) == "" {
		a.log.Info("no notes and no text, so nothing is posted")
		return result{}, nil
	}
	blocks, shown, err := r.msg.blocks()
	if err != nil {
		return result{}, err
	}
	payload := map[string]any{"text": r.msg.fallback(shown), "blocks": blocks}
	c := a.httpClient(guard)
	if r.webhook != nil {
		return a.postWebhook(ctx, c, r.webhook, payload, guard)
	}
	payload["channel"] = r.channel
	return a.postMessage(ctx, c, r, payload, guard)
}

// postWebhook posts payload to an Incoming Webhook, which answers ok, or an
// error as plain text with a status other than 200.
func (a *Action) postWebhook(ctx context.Context, c *http.Client, u *url.URL, payload map[string]any, guard actionrpc.Guard) (result, error) {
	code, body, err := send(ctx, c, u, "", payload, guard)
	res := result{code: code}
	if err != nil {
		return res, err
	}
	if code != http.StatusOK {
		// A server can answer with the path it was sent, which is the
		// secret of the webhook, so only an error of Slack, such as
		// no_service, is told.
		if id := strings.TrimSpace(string(body)); slackError.MatchString(id) {
			return res, fmt.Errorf("the webhook answered %d %s: %s", code, http.StatusText(code), id)
		}
		return res, fmt.Errorf("the webhook answered %d %s", code, http.StatusText(code))
	}
	res.posted = true
	return res, nil
}

// postMessage posts payload with chat.postMessage, which answers 200 with
// ok false and an error for most of what goes wrong.
func (a *Action) postMessage(ctx context.Context, c *http.Client, r *request, payload map[string]any, guard actionrpc.Guard) (result, error) {
	u, err := r.apiURL.Parse("chat.postMessage")
	if err != nil {
		return result{}, err
	}
	code, body, err := send(ctx, c, u, r.token, payload, guard)
	res := result{code: code}
	if err != nil {
		return res, err
	}
	var out struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
		Channel  string `json:"channel"`
		TS       string `json:"ts"`
		Metadata struct {
			Messages []string `json:"messages"`
		} `json:"response_metadata"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return res, fmt.Errorf("chat.postMessage: Slack answered %d %s, which is not the JSON expected", code, http.StatusText(code))
	}
	if !out.OK {
		msg := out.Error
		if len(out.Metadata.Messages) > 0 {
			msg += ": " + strings.Join(out.Metadata.Messages, "; ")
		}
		return res, fmt.Errorf("chat.postMessage: Slack answered %s", msg)
	}
	res.posted, res.channel, res.ts = true, out.Channel, out.TS
	return res, nil
}

// send posts payload as JSON to u, with token as a bearer token when it is
// given, and returns the status code and the body of the response.
func send(ctx context.Context, c *http.Client, u *url.URL, token string, payload any, guard actionrpc.Guard) (int, []byte, error) {
	if err := checkHost(guard, u); err != nil {
		return 0, nil, err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(data))
	if err != nil {
		return 0, nil, errors.New("the request cannot be made")
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("User-Agent", "probe-slack/"+version)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, sendError(u, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, sendError(u, err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		// Retry-After is told only as the number of seconds it should be.
		after := resp.Header.Get("Retry-After")
		if !seconds.MatchString(after) {
			after = "a few"
		}
		return resp.StatusCode, body, fmt.Errorf("Slack answered 429 Too Many Requests: it takes messages again in %s seconds", after)
	}
	return resp.StatusCode, body, err
}

// sendError is err of a request to u, told without what a URL holds past
// its host. The URL of a webhook is its secret, and an error of net/http
// can name it, or a path a redirect gave, at any depth, so only the kinds
// of error known to name no more than a host are told.
func sendError(u *url.URL, err error) error {
	var refused *actionrpc.Refused
	var op *net.OpError
	var dns *net.DNSError
	switch {
	case errors.As(err, &refused):
		// A refusal names the host alone, and is kept as one for the run
		// to tell it from other errors.
		return refused
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("POST %s: no answer within the timeout", u.Host)
	case errors.As(err, &dns):
		return fmt.Errorf("POST %s: the host cannot be found", u.Host)
	case errors.As(err, &op):
		return fmt.Errorf("POST %s: %s: %v", u.Host, op.Op, op.Err)
	}
	var redirect *redirectError
	if errors.As(err, &redirect) {
		return fmt.Errorf("POST %s: %s", u.Host, redirect.msg)
	}
	return fmt.Errorf("POST %s: the request failed", u.Host)
}

// redirectError is the error of a redirect the action does not follow,
// which names no more than a host.
type redirectError struct{ msg string }

func (e *redirectError) Error() string { return e.msg }

// httpClient is the client of the action, which follows a redirect only to
// a host the guard allows, and only when the POST stays a POST.
func (a *Action) httpClient(guard actionrpc.Guard) *http.Client {
	c := http.Client{Transport: http.DefaultTransport}
	if a.client != nil {
		c = *a.client
	}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return &redirectError{"stopped after 10 redirects"}
		}
		// net/http sends a POST again as a GET after a 301 or a 302, which
		// would post nothing.
		if req.Method != via[0].Method {
			return &redirectError{fmt.Sprintf("Slack redirected the POST to %s, which would be sent as a %s", req.URL.Host, req.Method)}
		}
		return checkHost(guard, req.URL)
	}
	return &c
}

// checkHost returns a Refused error when the guard does not allow the host
// of u. A host without a port is taken at the port of its scheme.
func checkHost(guard actionrpc.Guard, u *url.URL) error {
	host := u.Host
	if u.Port() == "" {
		port := "80"
		if u.Scheme == "https" {
			port = "443"
		}
		host = net.JoinHostPort(u.Hostname(), port)
	}
	return guard.CheckHost(host)
}

func optString(with map[string]any, key string) (string, error) {
	v, ok := with[key]
	if !ok || v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("with.%s must be a string, not %T", key, v)
	}
	return s, nil
}
