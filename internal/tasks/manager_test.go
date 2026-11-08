package tasks

import (
	"context"
	"errors"
	"fmt"
	sdk "github.com/thecybersailor/browserd-client-go/pkg/browserd"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTaskWatchDoesNotCancelAndDeduplicates(t *testing.T) {
	m := New()
	gate := make(chan struct{})
	var runs atomic.Int32
	in := Input{SessionID: "rt", Operation: "navigate", Control: sdk.TaskControl{RequestID: "req"}, Payload: []byte(`{"url":"a"}`), Run: func(ctx context.Context) (any, error) { runs.Add(1); <-gate; return map[string]any{"url": "a"}, nil }}
	a, err := m.Submit(in)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Submit(in)
	if err != nil || a != b {
		t.Fatalf("duplicate %v %v", b, err)
	}
	task, err := m.Watch(context.Background(), a.BrowserdTaskID, 1)
	if err != nil || task.State != sdk.TaskRunning {
		t.Fatalf("watch %v %v", task, err)
	}
	in.Payload = []byte(`{"url":"b"}`)
	if _, err = m.Submit(in); err == nil {
		t.Fatal("accepted conflicting request")
	}
	close(gate)
	task, err = m.Watch(context.Background(), a.BrowserdTaskID, 1000)
	if err != nil || task.State != sdk.TaskSucceeded || runs.Load() != 1 {
		t.Fatalf("task %+v %v runs=%d", task, err, runs.Load())
	}
	again, _ := m.Watch(context.Background(), a.BrowserdTaskID, 0)
	if string(again.Info.Result) != string(task.Info.Result) {
		t.Fatal("result changed")
	}
}

func TestTaskCancelKeepsAdmissionUntilExecutionStopped(t *testing.T) {
	m := New()
	started := make(chan struct{})
	stopped := make(chan struct{})
	var released atomic.Bool
	a, err := m.Submit(Input{SessionID: "rt", Operation: "evaluate", Control: sdk.TaskControl{RequestID: "r"}, Acquire: func() (func(), error) { return func() { released.Store(true) }, nil }, Stop: func() error { close(stopped); return nil }, Run: func(ctx context.Context) (any, error) { close(started); <-ctx.Done(); <-stopped; return nil, ctx.Err() }})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	_, err = m.Cancel(a.BrowserdTaskID)
	if err != nil {
		t.Fatal(err)
	}
	task, err := m.Watch(context.Background(), a.BrowserdTaskID, 1000)
	if err != nil || task.State != sdk.TaskCancelled || !released.Load() {
		t.Fatalf("cancel %+v %v released=%v", task, err, released.Load())
	}
}

func TestTaskAdmissionAndLookupFailFast(t *testing.T) {
	m := New()
	_, err := m.Submit(Input{SessionID: "rt", Control: sdk.TaskControl{RequestID: "r"}, Acquire: func() (func(), error) { return nil, errors.New("SESSION_BUSY") }, Run: func(context.Context) (any, error) { t.Error("ran rejected task"); return nil, nil }})
	if err == nil {
		t.Fatal("accepted busy session")
	}
	if _, err = m.Watch(context.Background(), "bdt_other_generation_id", 0); err == nil {
		t.Fatal("accepted unknown task")
	}
	if _, err = m.Watch(context.Background(), "", 30001); err == nil {
		t.Fatal("accepted invalid watch")
	}
}

func TestTaskExecutionDeadline(t *testing.T) {
	m := New()
	a, err := m.Submit(Input{SessionID: "rt", Operation: "navigate", Control: sdk.TaskControl{RequestID: "r", ExecutionTimeoutMs: 10}, Run: func(ctx context.Context) (any, error) { <-ctx.Done(); return nil, ctx.Err() }})
	if err != nil {
		t.Fatal(err)
	}
	task, err := m.Watch(context.Background(), a.BrowserdTaskID, 1000)
	if err != nil || task.State != sdk.TaskTimedOut {
		t.Fatalf("deadline %+v %v", task, err)
	}
}

func TestTaskWatchContextOnlyCancelsWatch(t *testing.T) {
	m := New()
	gate := make(chan struct{})
	a, _ := m.Submit(Input{SessionID: "rt", Operation: "evaluate", Control: sdk.TaskControl{RequestID: "r"}, Run: func(context.Context) (any, error) { <-gate; return true, nil }})
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := m.Watch(ctx, a.BrowserdTaskID, 1000); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("watch error %v", err)
	}
	close(gate)
	task, err := m.Watch(context.Background(), a.BrowserdTaskID, 1000)
	if err != nil || task.State != sdk.TaskSucceeded {
		t.Fatalf("task %+v %v", task, err)
	}
}

func TestTaskCancellationAlwaysStopsBrowserEvenIfWorkerReturnsFirst(t *testing.T) {
	for i := 0; i < 50; i++ {
		m := New()
		var stopped atomic.Bool
		a, err := m.Submit(Input{SessionID: "rt", Operation: "evaluate", Control: sdk.TaskControl{RequestID: "r"}, Run: func(ctx context.Context) (any, error) { <-ctx.Done(); return nil, ctx.Err() }, Stop: func() error { stopped.Store(true); return nil }})
		if err != nil {
			t.Fatal(err)
		}
		m.Cancel(a.BrowserdTaskID)
		task, err := m.Watch(context.Background(), a.BrowserdTaskID, 1000)
		if err != nil || task.State != sdk.TaskCancelled || !stopped.Load() {
			t.Fatalf("cancel did not stop browser: %+v %v stopped=%v", task, err, stopped.Load())
		}
	}
}

type gatedJSON struct{ started, release chan struct{} }

func (v gatedJSON) MarshalJSON() ([]byte, error) {
	close(v.started)
	<-v.release
	return []byte(`{"ok":true}`), nil
}
func TestTaskCancelAfterExecutionDoesNotReverseCompletion(t *testing.T) {
	m := New()
	v := gatedJSON{make(chan struct{}), make(chan struct{})}
	sub, err := m.Submit(Input{SessionID: "rt", Operation: "evaluate", Control: sdk.TaskControl{RequestID: "r"}, Run: func(context.Context) (any, error) { return v, nil }})
	if err != nil {
		t.Fatal(err)
	}
	<-v.started
	cancelled, err := m.Cancel(sub.BrowserdTaskID)
	close(v.release)
	if err != nil || cancelled.State != sdk.TaskRunning || cancelled.Info.Stage != "finalizing" {
		t.Fatalf("completed execution accepted cancellation: %+v %v", cancelled, err)
	}
	final, err := m.Watch(context.Background(), sub.BrowserdTaskID, 1000)
	if err != nil || final.State != sdk.TaskSucceeded {
		t.Fatalf("completion %+v %v", final, err)
	}
}

func TestTaskRetentionCapacityAndResultBounds(t *testing.T) {
	t.Run("expired terminal only", func(t *testing.T) {
		m := New()
		gate := make(chan struct{})
		sub, _ := m.Submit(Input{SessionID: "rt", Control: sdk.TaskControl{RequestID: "r"}, Run: func(context.Context) (any, error) { <-gate; return true, nil }})
		m.mu.Lock()
		m.pruneLocked(time.Now().Add(48 * time.Hour))
		_, active := m.entries[sub.BrowserdTaskID]
		m.mu.Unlock()
		if !active {
			t.Fatal("active task expired")
		}
		close(gate)
		m.Watch(context.Background(), sub.BrowserdTaskID, 1000)
		m.mu.Lock()
		m.pruneLocked(time.Now().Add(48 * time.Hour))
		_, retained := m.entries[sub.BrowserdTaskID]
		_, request := m.requests["r"]
		m.mu.Unlock()
		if retained || request {
			t.Fatal("expired result or dedup key retained")
		}
	})
	t.Run("capacity", func(t *testing.T) {
		m := New()
		for i := 0; i < maxTasks; i++ {
			m.entries[fmt.Sprint(i)] = &entry{}
		}
		_, err := m.Submit(Input{SessionID: "rt", Control: sdk.TaskControl{RequestID: "r"}, Run: func(context.Context) (any, error) { t.Error("ran over capacity"); return true, nil }})
		if err == nil {
			t.Fatal("accepted over capacity")
		}
	})
	for name, value := range map[string]any{"null": nil, "non-json": make(chan int), "too-large": strings.Repeat("x", maxResultBytes+1)} {
		t.Run(name, func(t *testing.T) {
			m := New()
			sub, _ := m.Submit(Input{SessionID: "rt", Control: sdk.TaskControl{RequestID: "r"}, Run: func(context.Context) (any, error) { return value, nil }})
			task, err := m.Watch(context.Background(), sub.BrowserdTaskID, 1000)
			if err != nil || task.State != sdk.TaskFailed || task.Info.Error.Code != "TASK_RESULT_INVALID" {
				t.Fatalf("invalid result %+v %v", task, err)
			}
		})
	}
}
func TestTaskStopFailureNeverReleasesAdmission(t *testing.T) {
	m := New()
	var released atomic.Bool
	sub, _ := m.Submit(Input{SessionID: "rt", Control: sdk.TaskControl{RequestID: "r"}, Acquire: func() (func(), error) { return func() { released.Store(true) }, nil }, Run: func(ctx context.Context) (any, error) { <-ctx.Done(); return nil, ctx.Err() }, Stop: func() error { return errors.New("cannot confirm termination") }})
	m.Cancel(sub.BrowserdTaskID)
	task, err := m.Watch(context.Background(), sub.BrowserdTaskID, 1000)
	if err != nil || task.State != sdk.TaskFailed || task.Info.Error.Code != "TASK_STOP_FAILED" || task.Info.SessionUsable || released.Load() {
		t.Fatalf("unsafe stop %+v %v released=%v", task, err, released.Load())
	}
}
