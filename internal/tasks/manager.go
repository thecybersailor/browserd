// Package tasks owns in-process browser executions. Observation never owns execution.
package tasks

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/thecybersailor/browserd-client-go/pkg/browserd"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const maxTasks = 4096
const maxResultBytes = 8 << 20
const retention = 24 * time.Hour

type Input struct {
	SessionID, Operation string
	Control              sdk.TaskControl
	Payload              []byte
	Acquire              func() (func(), error)
	Run                  func(context.Context) (any, error)
	Stop                 func() error
}
type entry struct {
	task              sdk.Task
	fingerprint       [32]byte
	started, finished time.Time
	cancel            context.CancelFunc
	done              chan struct{}
	requestKey        string
	sealed            bool
}
type Manager struct {
	mu         sync.Mutex
	generation string
	entries    map[string]*entry
	requests   map[string]string
}

func New() *Manager {
	return &Manager{generation: randomID(), entries: map[string]*entry{}, requests: map[string]string{}}
}
func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("task ID entropy unavailable: %v", err))
	}
	return hex.EncodeToString(b[:])
}
func taskErr(code, message string) error { return sdk.Error{Code: code, Message: message} }
func (m *Manager) Submit(in Input) (sdk.TaskSubmission, error) {
	if err := sdk.ValidateTaskControl(in.Control); err != nil {
		return sdk.TaskSubmission{}, err
	}
	if in.SessionID == "" || in.Run == nil {
		return sdk.TaskSubmission{}, taskErr("INVALID_REQUEST", "session and task execution are required")
	}
	timeout := in.Control.ExecutionTimeoutMs
	if timeout == 0 {
		timeout = sdk.DefaultExecutionTimeoutMs
	}
	// The session and operation are part of the content fingerprint, not the key:
	// reusing a request ID for a different operation must fail, never execute twice.
	key := in.Control.RequestID
	fingerprint := sha256.Sum256(append([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00", in.SessionID, in.Operation, timeout)), in.Payload...))
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(time.Now())
	if id, ok := m.requests[key]; ok {
		e := m.entries[id]
		if e.fingerprint != fingerprint {
			return sdk.TaskSubmission{}, taskErr("REQUEST_ID_CONFLICT", "request_id was already used with different task input")
		}
		return sdk.TaskSubmission{BrowserdTaskID: id}, nil
	}
	if len(m.entries) >= maxTasks {
		return sdk.TaskSubmission{}, taskErr("TASK_CAPACITY_EXCEEDED", "browser task capacity reached; finish active tasks and wait for retained results to expire")
	}
	release := func() {}
	if in.Acquire != nil {
		var err error
		release, err = in.Acquire()
		if err != nil {
			return sdk.TaskSubmission{}, err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Millisecond)
	id := "bdt_" + m.generation + "_" + randomID()
	e := &entry{task: sdk.Task{BrowserdTaskID: id, State: sdk.TaskRunning, Info: sdk.TaskInfo{RuntimeSessionID: in.SessionID, Operation: in.Operation, Stage: "executing", SessionUsable: true}}, fingerprint: fingerprint, started: time.Now(), cancel: cancel, done: make(chan struct{}), requestKey: key}
	m.entries[id] = e
	m.requests[key] = id
	slog.Info("browser task submitted", "browserd_task_id", id, "runtime_session_id", in.SessionID, "operation", in.Operation)
	go m.execute(ctx, e, in, release)
	return sdk.TaskSubmission{BrowserdTaskID: id}, nil
}
func (m *Manager) execute(ctx context.Context, e *entry, in Input, release func()) {
	type result struct {
		value any
		err   error
	}
	done := make(chan result, 1)
	go func() {
		var r result
		defer func() {
			if v := recover(); v != nil {
				r.err = fmt.Errorf("browser execution panicked: %v", v)
			}
			done <- r
		}()
		r.value, r.err = in.Run(ctx)
	}()
	var r result
	state := sdk.TaskSucceeded
	usable := true
	workerPending := false
	select {
	case r = <-done:
	case <-ctx.Done():
		workerPending = true
	}
	m.mu.Lock()
	cancelErr := ctx.Err()
	if cancelErr == nil {
		e.sealed = true
		e.task.Info.Stage = "finalizing"
	}
	m.mu.Unlock()
	if cancelErr != nil {
		m.mu.Lock()
		e.task.State = sdk.TaskCancelling
		e.task.Info.Stage = "stopping"
		m.mu.Unlock()
		if in.Stop != nil {
			if err := in.Stop(); err != nil {
				// Keep admission reserved if termination could not be confirmed. A broken
				// session must never be offered to the next browser command.
				m.finish(e, sdk.TaskFailed, nil, &sdk.TaskError{Code: "TASK_STOP_FAILED", Message: err.Error()}, false)
				e.cancel()
				return
			}
			usable = false
		}
		if workerPending {
			r = <-done
		}
	}
	if cancelErr != nil {
		state = sdk.TaskCancelled
		if errors.Is(cancelErr, context.DeadlineExceeded) {
			state = sdk.TaskTimedOut
		}
		r.err = cancelErr
	} else if r.err != nil {
		state = sdk.TaskFailed
	}
	var data []byte
	var failure *sdk.TaskError
	if r.err != nil {
		code := "BROWSER_TASK_FAILED"
		var se sdk.Error
		if errors.As(r.err, &se) {
			code = se.Code
		}
		if state == sdk.TaskTimedOut {
			code = "TASK_EXECUTION_TIMEOUT"
		}
		if state == sdk.TaskCancelled {
			code = "TASK_CANCELLED"
		}
		failure = &sdk.TaskError{Code: code, Message: r.err.Error()}
	}
	if state == sdk.TaskSucceeded {
		var err error
		data, err = json.Marshal(r.value)
		if err != nil || len(data) > maxResultBytes || string(data) == "null" {
			state = sdk.TaskFailed
			failure = &sdk.TaskError{Code: "TASK_RESULT_INVALID", Message: "task result must be JSON, non-null, and at most 8 MiB"}
			data = nil
		}
	}
	// Release admission only after the worker and stop operation have completed.
	release()
	m.finish(e, state, data, failure, usable)
	e.cancel()
}
func (m *Manager) finish(e *entry, state sdk.TaskState, data []byte, failure *sdk.TaskError, usable bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e.task.State = state
	e.task.Info.Stage = "completed"
	e.task.Info.Result = data
	e.task.Info.Error = failure
	e.task.Info.SessionUsable = usable
	e.finished = time.Now()
	e.task.Info.ElapsedMs = e.finished.Sub(e.started).Milliseconds()
	close(e.done)
	slog.Info("browser task completed", "browserd_task_id", e.task.BrowserdTaskID, "state", state, "elapsed_ms", e.task.Info.ElapsedMs)
}
func (m *Manager) lookupLocked(id string) (*entry, error) {
	if err := sdk.ValidateTaskID(id); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(id, "bdt_"+m.generation+"_") {
		return nil, taskErr("TASK_INSTANCE_LOST", "task belongs to a different browser process and cannot be resumed or replayed")
	}
	e := m.entries[id]
	if e == nil {
		return nil, taskErr("TASK_NOT_FOUND", "task was not found or its result expired")
	}
	return e, nil
}
func (m *Manager) snapshotLocked(e *entry) sdk.Task {
	t := e.task
	t.Info.Result = append(json.RawMessage(nil), t.Info.Result...)
	if t.Info.Error != nil {
		copy := *t.Info.Error
		t.Info.Error = &copy
	}
	if !t.State.Terminal() {
		t.Info.ElapsedMs = time.Since(e.started).Milliseconds()
	}
	return t
}
func (m *Manager) Watch(ctx context.Context, id string, timeoutMs int) (sdk.Task, error) {
	if timeoutMs < 0 || timeoutMs > sdk.MaxWatchTimeoutMs {
		return sdk.Task{}, taskErr("INVALID_REQUEST", "watch timeoutMs must be 0..30000")
	}
	m.mu.Lock()
	m.pruneLocked(time.Now())
	e, err := m.lookupLocked(id)
	if err != nil {
		m.mu.Unlock()
		return sdk.Task{}, err
	}
	terminal := e.task.State.Terminal()
	m.mu.Unlock()
	if !terminal && timeoutMs > 0 {
		timer := time.NewTimer(time.Duration(timeoutMs) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-e.done:
		case <-timer.C:
		case <-ctx.Done():
			return sdk.Task{}, ctx.Err()
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked(e), nil
}
func (m *Manager) Cancel(id string) (sdk.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, err := m.lookupLocked(id)
	if err != nil {
		return sdk.Task{}, err
	}
	if !e.task.State.Terminal() && !e.sealed {
		e.task.State = sdk.TaskCancelling
		e.task.Info.Stage = "stopping"
		e.cancel()
	}
	return m.snapshotLocked(e), nil
}
func (m *Manager) pruneLocked(now time.Time) {
	for id, e := range m.entries {
		if !e.finished.IsZero() && now.Sub(e.finished) > retention {
			delete(m.entries, id)
			delete(m.requests, e.requestKey)
		}
	}
}
