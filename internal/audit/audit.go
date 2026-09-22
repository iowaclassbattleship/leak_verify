// Package audit keeps a record of what people did, so a published
// demonstrator can be watched without reading the container's stderr.
//
// The record is a ring buffer in memory: it survives nothing, holds a bounded
// number of entries, and is readable only with the master password.
package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"
)

// Capacity is how many entries are kept before the oldest are dropped.
const Capacity = 5000

// Entry is one thing that happened.
type Entry struct {
	At      time.Time `json:"at"`
	User    string    `json:"user,omitempty"`    // email address, when signed in
	Client  string    `json:"client,omitempty"`  // remote address
	Product string    `json:"product,omitempty"` // custodial | provenance | ""
	Action  string    `json:"action"`            // what was done, in words
	Detail  string    `json:"detail,omitempty"`
	Status  int       `json:"status,omitempty"` // HTTP status, for requests
	Millis  int64     `json:"ms,omitempty"`
	Routine bool      `json:"routine,omitempty"` // polling and reads, hidden by default
}

type Log struct {
	mu    sync.Mutex
	ring  []Entry
	next  int
	count int
	echo  io.Writer // also written here, so docker logs shows it
}

func New(echo io.Writer) *Log {
	return &Log{ring: make([]Entry, Capacity), echo: echo}
}

func (l *Log) Add(e Entry) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	l.mu.Lock()
	l.ring[l.next] = e
	l.next = (l.next + 1) % Capacity
	if l.count < Capacity {
		l.count++
	}
	l.mu.Unlock()

	if l.echo != nil && !e.Routine {
		fmt.Fprintln(l.echo, strings.TrimSpace(e.Line()))
	}
}

// Entries returns the record oldest first.
func (l *Log) Entries(includeRoutine bool) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Entry, 0, l.count)
	start := (l.next - l.count + Capacity) % Capacity
	for i := 0; i < l.count; i++ {
		e := l.ring[(start+i)%Capacity]
		if e.Routine && !includeRoutine {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Line renders one entry as a column of readable text.
func (e Entry) Line() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %-34s %-11s %-28s", e.At.UTC().Format("2006-01-02 15:04:05"),
		orDash(e.User), orDash(e.Product), e.Action)
	if e.Detail != "" {
		fmt.Fprintf(&b, " %s", e.Detail)
	}
	if e.Status != 0 && (e.Status < 200 || e.Status >= 300) {
		fmt.Fprintf(&b, " [%d]", e.Status)
	}
	if e.Millis > 0 {
		fmt.Fprintf(&b, " (%dms)", e.Millis)
	}
	if e.Client != "" {
		fmt.Fprintf(&b, " from %s", e.Client)
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// WriteText renders the whole record for a human.
func (l *Log) WriteText(w io.Writer, includeRoutine bool) {
	entries := l.Entries(includeRoutine)
	fmt.Fprintf(w, "%d entries, newest last, times in UTC. Capacity %d, held in memory only.\n\n", len(entries), Capacity)
	for _, e := range entries {
		fmt.Fprintln(w, e.Line())
	}
}

// WriteJSON renders the whole record for a machine.
func (l *Log) WriteJSON(w io.Writer, includeRoutine bool) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(l.Entries(includeRoutine))
}

// Printf records something that is not a request, and is the replacement for
// scattered log.Printf calls.
func (l *Log) Printf(action, format string, args ...any) {
	l.Add(Entry{Action: action, Detail: fmt.Sprintf(format, args...)})
}

// StdLogger returns a *log.Logger that writes into the record, so packages
// that only know about the standard library can still be captured.
func (l *Log) StdLogger() *log.Logger {
	return log.New(writerFunc(func(p []byte) (int, error) {
		l.Add(Entry{Action: "server", Detail: strings.TrimSpace(string(p))})
		return len(p), nil
	}), "", 0)
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
