package logbook_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/jmmshn/peons/internal/logbook"
)

func open(t *testing.T) *logbook.Logbook {
	t.Helper()
	ctx := context.Background()
	lb, err := logbook.Open(ctx, filepath.Join(t.TempDir(), "sub", "logbook.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lb.Close() })
	if err := lb.Init(ctx); err != nil {
		t.Fatal(err)
	}
	return lb
}

func TestSentMessageIsUnreadForRecipientOnly(t *testing.T) {
	ctx := context.Background()
	lb := open(t)
	if _, err := lb.Send(ctx, "chief", "peon-a", "add tests for parser"); err != nil {
		t.Fatal(err)
	}
	got, err := lb.Unread(ctx, "peon-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].From != "chief" || got[0].To != "peon-a" || got[0].Body != "add tests for parser" {
		t.Fatalf("unread for peon-a = %+v", got)
	}
	other, err := lb.Unread(ctx, "peon-b")
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("unread for peon-b = %+v, want none", other)
	}
}

func TestMarkReadRemovesFromUnreadAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	lb := open(t)
	first, _ := lb.Send(ctx, "chief", "peon-a", "one")
	if _, err := lb.Send(ctx, "chief", "peon-a", "two"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := lb.MarkRead(ctx, "peon-a", []logbook.ID{first}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := lb.Unread(ctx, "peon-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Body != "two" {
		t.Fatalf("unread = %+v, want only \"two\"", got)
	}
}

func TestMarkReadByAnotherSessionDoesNotCount(t *testing.T) {
	ctx := context.Background()
	lb := open(t)
	id, _ := lb.Send(ctx, "chief", "peon-a", "one")
	if err := lb.MarkRead(ctx, "peon-b", []logbook.ID{id}); err != nil {
		t.Fatal(err)
	}
	got, _ := lb.Unread(ctx, "peon-a")
	if len(got) != 1 {
		t.Fatalf("unread = %+v, want the message still unread", got)
	}
}

func TestReadingANewerMessageLeavesOlderOnesUnread(t *testing.T) {
	ctx := context.Background()
	lb := open(t)
	if _, err := lb.Send(ctx, "chief", "peon-a", "older"); err != nil {
		t.Fatal(err)
	}
	newer, _ := lb.Send(ctx, "peon-b", "peon-a", "newer")
	if err := lb.MarkRead(ctx, "peon-a", []logbook.ID{newer}); err != nil {
		t.Fatal(err)
	}
	got, _ := lb.Unread(ctx, "peon-a")
	if len(got) != 1 || got[0].Body != "older" {
		t.Fatalf("unread = %+v, want only \"older\"", got)
	}
}

func TestConcurrentSendersFromSeparateProcessesLoseNothing(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "logbook.db")
	setup, err := logbook.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := setup.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer setup.Close()

	const senders = 20
	errs := make(chan error, senders)
	for i := range senders {
		go func() {
			lb, err := logbook.Open(ctx, path) // own handle, like a separate pns process
			if err != nil {
				errs <- err
				return
			}
			defer lb.Close()
			_, err = lb.Send(ctx, fmt.Sprintf("peon-%d", i), "chief", "done")
			if err == nil {
				err = lb.MarkRead(ctx, fmt.Sprintf("peon-%d", i), nil)
			}
			errs <- err
		}()
	}
	for range senders {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	got, _ := setup.Unread(ctx, "chief")
	if len(got) != senders {
		t.Fatalf("chief has %d unread, want %d", len(got), senders)
	}
}
