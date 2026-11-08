package controller_test

import (
	"browserd/internal/browser"
	"browserd/internal/controller"
	"browserd/internal/profile"
	"browserd/internal/session"
	"context"
	"errors"
	sdk "github.com/thecybersailor/browserd-client-go/pkg/browserd"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in real-process validation. No Docker and no external website are used.
func TestBrowserTaskRealChromeBeyondThirtySecondsAndCancellation(t *testing.T) {
	binary := os.Getenv("CHROME_BIN")
	if binary == "" {
		t.Skip("set CHROME_BIN to run real browser task validation")
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatal(err)
	}
	// Use a real file: Chrome's background helpers may retain stderr after the
	// browser exits, which otherwise leaves go test's output pipe open.
	diagnosticsDir := t.TempDir()
	logPath := filepath.Join(diagnosticsDir, "chrome.log")
	launcher := filepath.Join(diagnosticsDir, "chrome")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec "+quote(binary)+" \"$@\" 2>"+quote(logPath)+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHROME_BIN", launcher)
	t.Cleanup(func() {
		if t.Failed() {
			if raw, err := os.ReadFile(logPath); err == nil {
				t.Logf("native Chrome diagnostics: %s", raw)
			}
		}
	})
	t.Setenv("BROWSERD_LIVE_ENABLED", "false")
	manager := session.NewManager(session.ManagerOptions{Store: profile.NewMemoryStore(), Workdir: t.TempDir()})
	runtime := browser.NewService(manager, nil, nil)
	h := controller.NewSessionController(manager, runtime, "")
	id := createTestSession(t, h)
	defer runtime.Close(id)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/navigate"):
			h.Navigate(w, r, id)
		case strings.HasSuffix(r.URL.Path, "/evaluate"):
			h.Evaluate(w, r, id)
		case strings.HasPrefix(r.URL.Path, "/v1/tasks/"):
			parts := strings.Split(r.URL.Path, "/")
			if parts[4] == "watch" {
				h.WatchTask(w, r, parts[3])
			} else {
				h.CancelTask(w, r, parts[3])
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := sdk.NewClient(sdk.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	blocked := make(chan struct{})
	requested := make(chan struct{}, 1)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow.js" {
			requested <- struct{}{}
			<-blocked
			w.Write([]byte(`window.loaded=true`))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<title>Task navigation</title><script src="/slow.js"></script><h1>Ready</h1>`))
	}))
	defer site.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	sub, err := client.Navigate(ctx, id, sdk.NavigateInput{URL: site.URL, WaitUntil: "load", TaskControl: sdk.TaskControl{RequestID: "real-nav", ExecutionTimeoutMs: 60000}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-requested:
	case <-ctx.Done():
		close(blocked)
		t.Fatal(ctx.Err())
	}
	time.Sleep(31 * time.Second)
	task, err := client.WatchTask(ctx, sub.BrowserdTaskID, 0)
	if err != nil || task.State != sdk.TaskRunning {
		close(blocked)
		t.Fatalf("navigation did not survive old timeout: %+v %v", task, err)
	}
	close(blocked)
	task, err = client.WatchTask(ctx, sub.BrowserdTaskID, 10000)
	if err != nil || task.State != sdk.TaskSucceeded {
		t.Fatalf("navigation did not complete: %+v %v", task, err)
	}
	result, err := sdk.DecodeTaskResult[sdk.NavigateResult](task)
	if err != nil || result.Title != "Task navigation" {
		t.Fatalf("navigation result: %+v %v", result, err)
	}
	sub, err = client.Evaluate(ctx, id, sdk.EvaluateInput{Script: `return await window.__botworksPageToolCall({method:"page.title"})`, PageToolBridge: &sdk.PageToolBridgeConfig{Enabled: true, Name: "__botworksPageToolCall"}, TaskControl: sdk.TaskControl{RequestID: "real-bridge", ExecutionTimeoutMs: 10000}})
	if err != nil {
		t.Fatal(err)
	}
	task, err = client.WatchTask(ctx, sub.BrowserdTaskID, 10000)
	if err != nil || task.State != sdk.TaskSucceeded || !strings.Contains(string(task.Info.Result), "Task navigation") {
		t.Fatalf("parent task pageTool bridge: %+v %v", task, err)
	}
	sub, err = client.Evaluate(ctx, id, sdk.EvaluateInput{Script: "await new Promise(() => {}); return true", TaskControl: sdk.TaskControl{RequestID: "real-infinite", ExecutionTimeoutMs: 60000}})
	if err != nil {
		t.Fatal(err)
	}
	task, err = client.WatchTask(ctx, sub.BrowserdTaskID, 100)
	if err != nil || task.State != sdk.TaskRunning {
		t.Fatalf("evaluate should be running: %+v %v", task, err)
	}
	if _, err = client.CancelTask(ctx, sub.BrowserdTaskID); err != nil {
		t.Fatal(err)
	}
	task, err = client.WatchTask(ctx, sub.BrowserdTaskID, 10000)
	if err != nil || task.State != sdk.TaskCancelled || task.Info.SessionUsable {
		t.Fatalf("cancellation not confirmed: %+v %v", task, err)
	}
	if _, err = manager.Get(id); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("cancelled browser session was not invalidated: %v", err)
	}
}
