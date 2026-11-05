package conversions

import (
	"context"
	"path/filepath"
	"testing"
)

func TestQueueLifecycle(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "conversions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	if err := st.Create(ctx, "job-1", "book.fb2", "job-1.fb2"); err != nil {
		t.Fatal(err)
	}
	job, err := st.ClaimNext(ctx)
	if err != nil || job == nil || job.ID != "job-1" || job.Status != string(StatusProcessing) {
		t.Fatalf("claim: %+v, %v", job, err)
	}
	if next, err := st.ClaimNext(ctx); err != nil || next != nil {
		t.Fatalf("second claim: %+v, %v", next, err)
	}
	if err := st.SetStatus(ctx, "job-1", StatusDone, "", 42); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(ctx, "job-1")
	if err != nil || got.Status != string(StatusDone) || got.BookID != 42 {
		t.Fatalf("done job: %+v, %v", got, err)
	}
}

func TestResetProcessing(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "conversions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	if err := st.Create(ctx, "job-1", "book.fb2", "job-1.fb2"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimNext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.ResetProcessing(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := st.Get(ctx, "job-1")
	if err != nil || job.Status != string(StatusQueued) {
		t.Fatalf("after reset: %+v, %v", job, err)
	}
}
