// Package fakeslack is a stand-in for the part of Slack the action calls:
// an Incoming Webhook, and chat.postMessage of the Web API. It keeps the
// messages it is sent in memory, and refuses what Slack refuses of their
// blocks, for the tests and the e2e workflows.
package fakeslack

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

// WebhookPath is the path of the Incoming Webhook the server answers.
const WebhookPath = "/services/T000/B000/e2e"

// Message is a message posted to a channel.
type Message struct {
	Channel string           `json:"channel"`
	TS      string           `json:"ts"`
	Text    string           `json:"text"`
	Blocks  []map[string]any `json:"blocks"`
	// Via is webhook or token, by which the message was posted.
	Via string `json:"via"`
}

// Markdown is the text of the markdown blocks of the message, one to a
// line.
func (m Message) Markdown() string {
	var parts []string
	for _, b := range m.Blocks {
		if t, _ := b["text"].(string); b["type"] == "markdown" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}

// Server is a workspace with the channels it knows.
type Server struct {
	// Token is the bot token chat.postMessage takes.
	Token string
	// Channels are the ids of the channels a message can be posted to; the
	// webhook posts to the first.
	Channels []string

	mu       sync.Mutex
	messages []Message
	// Requests counts the requests that reached the server.
	Requests int
}

// New returns a server that takes token, with the channels named.
func New(token string, channels ...string) *Server {
	return &Server{Token: token, Channels: channels}
}

// Messages returns the messages posted, oldest first.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.messages)
}

// ServeHTTP answers the webhook, chat.postMessage, and, for the e2e
// workflows to read what was posted, GET /e2e/messages.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Requests++
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/e2e/messages":
		reply(w, http.StatusOK, s.messages)
	case r.Method == http.MethodPost && r.URL.Path == WebhookPath:
		s.webhook(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/chat.postMessage":
		s.postMessage(w, r)
	case strings.HasPrefix(r.URL.Path, "/services/"):
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("no_service"))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type payload struct {
	Channel string           `json:"channel"`
	Text    string           `json:"text"`
	Blocks  []map[string]any `json:"blocks"`
}

func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	var p payload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid_payload"))
		return
	}
	if msg := checkBlocks(p); msg != "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid_blocks"))
		return
	}
	s.add(Message{Channel: s.Channels[0], Text: p.Text, Blocks: p.Blocks, Via: "webhook"})
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) postMessage(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+s.Token {
		reply(w, http.StatusOK, map[string]any{"ok": false, "error": "invalid_auth"})
		return
	}
	var p payload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		reply(w, http.StatusOK, map[string]any{"ok": false, "error": "invalid_json"})
		return
	}
	channel := strings.TrimPrefix(p.Channel, "#")
	if !slices.Contains(s.Channels, channel) {
		reply(w, http.StatusOK, map[string]any{"ok": false, "error": "channel_not_found"})
		return
	}
	if msg := checkBlocks(p); msg != "" {
		reply(w, http.StatusOK, map[string]any{"ok": false, "error": "invalid_blocks", "response_metadata": map[string]any{"messages": []string{msg}}})
		return
	}
	m := s.add(Message{Channel: channel, Text: p.Text, Blocks: p.Blocks, Via: "token"})
	reply(w, http.StatusOK, map[string]any{"ok": true, "channel": m.Channel, "ts": m.TS})
}

func (s *Server) add(m Message) Message {
	m.TS = fmt.Sprintf("1700000000.%06d", len(s.messages)+1)
	s.messages = append(s.messages, m)
	return m
}

// checkBlocks says what Slack would refuse of the blocks of p: more than 50
// of them, or more than 12000 characters in their markdown blocks.
func checkBlocks(p payload) string {
	if p.Text == "" && len(p.Blocks) == 0 {
		return "no text"
	}
	if len(p.Blocks) > 50 {
		return fmt.Sprintf("[ERROR] no more than 50 items allowed [json-pointer:/blocks], got %d", len(p.Blocks))
	}
	n := 0
	for _, b := range p.Blocks {
		if b["type"] != "markdown" {
			return fmt.Sprintf("[ERROR] unexpected block type %v", b["type"])
		}
		t, _ := b["text"].(string)
		n += utf8.RuneCountInString(t)
	}
	if n > 12000 {
		return fmt.Sprintf("[ERROR] markdown blocks hold %d characters, more than 12000", n)
	}
	return ""
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
