package bus

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/noetive/samspel-bench/internal/trace"
)

func spoolBus(t *testing.T, cfg Config, agents ...string) (*Bus, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "run.jsonl")
	s, err := OpenSpool(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	cfg.Enabled, cfg.Carrier = true, s
	return New(cfg, agents, 1, trace.New()), path
}

// With the spool as transport, an agent must receive exactly what was sent,
// in order, whatever the text contains: agents send newlines, quotes and
// non-ASCII, and a transport that mangles them changes the task.
func TestSpool_DeliversTextIntactAndInOrder(t *testing.T) {
	b, path := spoolBus(t, Config{}, "a", "b", "c")
	texts := []string{"line one\nline two", `say "hi" \ bye`, "förhandling 🤝", "{\"to\":\"c\"}"}
	for _, x := range texts {
		if _, ok := b.Send("a", "b", x); !ok {
			t.Fatalf("send %q rejected", x)
		}
	}
	if _, ok := b.Broadcast("c", "to all"); !ok {
		t.Fatal("broadcast rejected")
	}
	got := b.Drain("b")
	if len(got) != len(texts)+1 {
		t.Fatalf("b got %d messages, want %d", len(got), len(texts)+1)
	}
	for i, x := range texts {
		if got[i].Text != x || got[i].From != "a" {
			t.Errorf("message %d = %+v, want text %q from a", i, got[i], x)
		}
	}
	if got[len(texts)].Text != "to all" {
		t.Errorf("broadcast arrived as %q", got[len(texts)].Text)
	}
	if again := b.Drain("b"); len(again) != 0 {
		t.Errorf("drained messages came back: %+v", again)
	}
	if other := b.Drain("a"); len(other) != 1 || other[0].Text != "to all" {
		t.Errorf("a should hold only the broadcast, got %+v", other)
	}
	file, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(file, []byte("\n")); n != len(texts)+2 {
		t.Errorf("spool holds %d lines, want one per delivery (%d)", n, len(texts)+2)
	}
	if err := b.Err(); err != nil {
		t.Fatalf("transport error: %v", err)
	}
}

// Agents detect a stalled team through quiescence, so it must work the same
// when messages sit in a file instead of memory.
func TestSpool_QuiescenceSeesUncollectedMessages(t *testing.T) {
	b, _ := spoolBus(t, Config{}, "a", "b")
	b.Send("a", "b", "pending")
	_, q := b.Wait(context.Background(), "a", 50*time.Millisecond)
	if q {
		t.Fatal("quiet reported while b has an uncollected message")
	}
	if msgs, _ := b.Wait(context.Background(), "b", time.Second); len(msgs) != 1 {
		t.Fatalf("b got %d messages, want 1", len(msgs))
	}
	done := make(chan bool)
	go func() { _, q := b.Wait(context.Background(), "a", 5*time.Second); done <- q }()
	time.Sleep(20 * time.Millisecond)
	if _, q := b.Wait(context.Background(), "b", 5*time.Second); !q || !<-done {
		t.Fatal("both waiting with nothing held should be quiescent")
	}
}

// If the file loses messages, the run must be marked broken rather than
// scored as if the agents had simply not been told.
func TestSpool_LostMessagesFailTheRun(t *testing.T) {
	b, path := spoolBus(t, Config{}, "a", "b")
	b.Send("a", "b", "first")
	b.Send("a", "b", "second")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, info.Size()/2); err != nil {
		t.Fatal(err)
	}
	b.Drain("b")
	if b.Err() == nil {
		t.Fatal("a truncated spool went unnoticed")
	}
}

// Message text comes from models, so the spool must carry any of it without
// loss: whatever an agent sends is exactly what its recipient reads back.
// Model text reaches the bus through JSON decoding and is always valid UTF-8,
// so inputs that are not are out of scope.
func FuzzSpool_RoundTripsAnyText(f *testing.F) {
	for _, s := range []string{"", "plain", "a\nb\r\n", `{"to":"x","text":"y"}`, "\x00  ", "förhandling 🤝"} {
		f.Add(s, s+"!")
	}
	f.Fuzz(func(t *testing.T, first, second string) {
		if !utf8.ValidString(first) || !utf8.ValidString(second) {
			t.Skip()
		}
		s, err := OpenSpool(filepath.Join(t.TempDir(), "run.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		for i, x := range []string{first, second} {
			if err := s.Deliver(Message{ID: int64(i), From: "a", To: "b", Text: x}); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Deliver(Message{ID: 2, From: "b", To: "a", Text: first}); err != nil {
			t.Fatal(err)
		}
		got, err := s.Collect("b")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].Text != first || got[1].Text != second || got[0].ID != 0 || got[1].ID != 1 {
			t.Fatalf("round trip lost text: got %+v", got)
		}
		if s.Holds("b") || !s.Holds("a") {
			t.Fatal("collecting b's messages changed what is held")
		}
	})
}
