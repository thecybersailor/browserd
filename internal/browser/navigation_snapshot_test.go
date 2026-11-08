package browser

import (
	browserrt "browserd/internal/runtime"
	"context"
	"errors"
	"testing"
	"time"
)

func TestNavigateSnapshotPipeline(t *testing.T) {
	for _, stage := range []string{"success", "navigation", "snapshot", "cancel"} {
		t.Run(stage, func(t *testing.T) {
			state := browserrt.NewState()
			state.ReplaceSnapshot("rt", browserrt.SnapshotState{SnapshotID: "old", Refs: map[string]browserrt.RefState{"old-ref": {Ref: "old-ref"}}})
			s := NewService(nil, state, nil)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			deadline, _ := ctx.Deadline()
			captures := 0
			s.navigatePage = func(c context.Context, raw string) (NavigateOutput, error) {
				if _, err := state.GetSnapshot("rt"); !errors.Is(err, browserrt.ErrSnapshotNotFound) {
					t.Errorf("old state still present: %v", err)
				}
				if stage == "navigation" {
					return NavigateOutput{}, errors.New("network")
				}
				if stage == "cancel" {
					cancel()
				}
				return NavigateOutput{URL: raw, Title: "before"}, nil
			}
			s.captureSnapshot = func(c context.Context) (snapshotRuntimeEnvelope, error) {
				captures++
				if d, _ := c.Deadline(); !d.Equal(deadline) {
					t.Error("deadline was reset")
				}
				if stage == "snapshot" {
					return snapshotRuntimeEnvelope{}, errors.New("extract")
				}
				if err := c.Err(); err != nil {
					return snapshotRuntimeEnvelope{}, err
				}
				return snapshotRuntimeEnvelope{SnapshotID: "snap_test", Page: PageSnapshot{URL: "https://example.com/final", Title: "after", Groups: map[string]PageTable{}}, Refs: map[string]browserrt.RefState{"new-ref": {Ref: "new-ref"}}}, nil
			}
			out, err := s.navigateWithContext(ctx, "rt", NavigateInput{URL: "https://example.com/", IncludeSnapshot: true})
			if stage == "success" {
				if err != nil {
					t.Fatal(err)
				}
				if out.Snapshot == nil || out.URL != out.Snapshot.Page.URL || out.Title != "after" {
					t.Fatalf("inconsistent observation: %+v", out)
				}
				stored, err := state.GetSnapshot("rt")
				if err != nil || stored.SnapshotID != out.Snapshot.SnapshotID {
					t.Fatal("snapshot not registered")
				}
				ref, err := state.GetRef("rt", "new-ref")
				if err != nil || ref.SnapshotID != stored.SnapshotID {
					t.Fatal("ref not registered")
				}
				if _, err = state.GetRef("rt", "old-ref"); !errors.Is(err, browserrt.ErrStaleRef) {
					t.Fatal("old ref not stale")
				}
			} else {
				if err == nil {
					t.Fatal("failure reported success")
				}
				if _, err = state.GetSnapshot("rt"); !errors.Is(err, browserrt.ErrSnapshotNotFound) {
					t.Fatal("failure retained snapshot")
				}
			}
			want := 1
			if stage == "navigation" {
				want = 0
			}
			if captures != want {
				t.Fatalf("captures=%d want=%d", captures, want)
			}
		})
	}
}

func TestNavigateIncludeSnapshotRejectsUnsupportedWaitBeforeBrowser(t *testing.T) {
	s := NewService(nil, nil, nil)
	_, err := s.Navigate(context.Background(), "missing", NavigateInput{URL: "https://example.com/", IncludeSnapshot: true, WaitUntil: "networkidle"})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("expected preflight error, got %v", err)
	}
}
