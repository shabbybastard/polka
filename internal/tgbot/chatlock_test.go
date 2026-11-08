package tgbot

import (
	"io"
	"log/slog"
	"testing"
)

func TestChatLockRefcountCleanup(t *testing.T) {
	b := New(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	unlock := b.lockChat(42)
	if len(b.chatLocks.locks) != 1 {
		t.Fatalf("locks = %d, want 1", len(b.chatLocks.locks))
	}
	unlock()
	if len(b.chatLocks.locks) != 0 {
		t.Fatalf("idle lock leaked: %d", len(b.chatLocks.locks))
	}
}
