package bus

import (
	"context"
	"testing"
	"time"

	"github.com/noetive/samspel-bench/internal/trace"
)

func TestQuiescence(t *testing.T) {
	b := New(Config{Enabled: true}, []string{"a", "b"}, 1, trace.New())
	done := make(chan bool)
	go func() { _, q := b.Wait(context.Background(), "a", 5*time.Second); done <- q }()
	time.Sleep(20 * time.Millisecond)
	_, q := b.Wait(context.Background(), "b", 5*time.Second)
	if !q || !<-done {
		t.Fatal("both waiting with empty inboxes should be quiescent")
	}
}

func TestBudgetCapAndTopology(t *testing.T) {
	b := New(Config{Enabled: true, BudgetTokens: 10, MaxMessageTokens: 6, Allowed: func(f, to string) bool { return to != "c" }}, []string{"a", "b", "c"}, 1, trace.New())
	if _, ok := b.Send("a", "b", "0123456789012345678901234567890"); ok {
		t.Fatal("over per-message cap should be rejected")
	}
	if _, ok := b.Send("a", "c", "hi"); ok {
		t.Fatal("topology should block a->c")
	}
	if _, ok := b.Send("a", "b", "0123456789012345678"); !ok {
		t.Fatal("5 tokens should pass")
	}
	if _, ok := b.Send("a", "b", "0123456789012345678901"); ok {
		t.Fatal("budget of 10 should be exhausted")
	}
	if got := b.Drain("b"); len(got) != 1 {
		t.Fatalf("want 1 delivered, got %d", len(got))
	}
}

func TestDuplicateAndAblate(t *testing.T) {
	b := New(Config{Enabled: true, Duplicate: 1, Ablate: true}, []string{"a", "b"}, 1, trace.New())
	b.Send("a", "b", "the secret value is 42")
	got := b.Drain("b")
	if len(got) != 2 {
		t.Fatalf("want duplicate delivery, got %d", len(got))
	}
	if got[0].Text == "the secret value is 42" {
		t.Fatal("ablation should replace content")
	}
}
