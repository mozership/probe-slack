// Command server is a stand-in for Slack, for the e2e workflow: an Incoming
// Webhook at /services/T000/B000/e2e, which posts to C0NOTES, and
// chat.postMessage under /api, which takes the token FAKE_SLACK_TOKEN
// names, xoxb-e2e when it is not set, for C0NOTES and C0ALERTS. GET
// /e2e/messages lists what was posted. It listens on FAKE_SLACK_ADDR,
// 127.0.0.1:18083 when it is not set, and keeps the messages in memory.
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/mozership/probe-slack/internal/fakeslack"
)

func main() {
	token := os.Getenv("FAKE_SLACK_TOKEN")
	if token == "" {
		token = "xoxb-e2e"
	}
	addr := os.Getenv("FAKE_SLACK_ADDR")
	if addr == "" {
		addr = "127.0.0.1:18083"
	}
	fake := fakeslack.New(token, "C0NOTES", "C0ALERTS")
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		fake.ServeHTTP(w, r)
	})
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, handler))
}
