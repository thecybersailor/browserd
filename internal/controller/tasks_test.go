package controller_test

import (
	"browserd/internal/browser"
	"browserd/internal/controller"
	"context"
	sdk "github.com/thecybersailor/browserd-client-go/pkg/browserd"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAsyncNavigatePreservesSnapshotFailureCode(t *testing.T) {
	h := controller.NewSessionController(&fakeSessionManager{}, &fakeBrowserRuntime{navigateErr: browser.ErrSnapshotFailed}, "")
	rr := httptest.NewRecorder()
	h.Navigate(rr, httptest.NewRequest("POST", "/", strings.NewReader(`{"request_id":"snapshot-error","url":"https://example.com/","includeSnapshot":true}`)), "rt")
	if rr.Code != 202 {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body)
	}
	id := decodeData(t, rr)["browserd_task_id"].(string)
	watch := httptest.NewRecorder()
	h.WatchTask(watch, httptest.NewRequest("POST", "/", strings.NewReader(`{"timeoutMs":1000}`)), id)
	data := decodeData(t, watch)
	failure := data["info"].(map[string]any)["error"].(map[string]any)
	if data["state"] != "failed" || failure["code"] != "SNAPSHOT_FAILED" {
		t.Fatalf("snapshot failure lost: %s", watch.Body)
	}
}

func TestAsyncNavigateSurvivesSubmittingRequestCancellation(t *testing.T) {
	b := &blockedNavigationRuntime{started: make(chan struct{}), release: make(chan struct{})}
	h := controller.NewSessionController(&fakeSessionManager{}, b, "")
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"request_id":"r","url":"https://example.com/","executionTimeoutMs":1000}`)).WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Navigate(rr, req, "rt")
	if rr.Code != 202 {
		t.Fatalf("not submitted: %d %s", rr.Code, rr.Body)
	}
	id := decodeData(t, rr)["browserd_task_id"].(string)
	<-b.started
	cancel()
	watch := httptest.NewRecorder()
	h.WatchTask(watch, httptest.NewRequest("POST", "/", strings.NewReader(`{"timeoutMs":1}`)), id)
	if watch.Code != 200 || decodeData(t, watch)["state"] != "running" {
		t.Fatalf("watch cancelled task: %s", watch.Body)
	}
	busy := httptest.NewRecorder()
	h.Snapshot(busy, httptest.NewRequest("GET", "/", nil), "rt")
	if busy.Code != 409 {
		t.Fatal("released busy session")
	}
	close(b.release)
	watch = httptest.NewRecorder()
	h.WatchTask(watch, httptest.NewRequest("POST", "/", strings.NewReader(`{"timeoutMs":1000}`)), id)
	if decodeData(t, watch)["state"] != string(sdk.TaskSucceeded) {
		t.Fatalf("task did not succeed: %s", watch.Body)
	}
}

func TestAsyncNavigateRejectsLegacyAndUnknownFields(t *testing.T) {
	h := controller.NewSessionController(&fakeSessionManager{}, &fakeBrowserRuntime{}, "")
	for _, body := range []string{`{"url":"https://example.com/"}`, `{"request_id":"r","url":"https://example.com/","timeoutMs":30000}`, `{"request_id":"r","url":"https://example.com/","async":true}`, `{"request_id":"r","url":"https://example.com/","executionTimeoutMs":1800001}`} {
		rr := httptest.NewRecorder()
		h.Navigate(rr, httptest.NewRequest("POST", "/", strings.NewReader(body)), "rt")
		if rr.Code != 400 {
			t.Fatalf("accepted old request %s: %d", body, rr.Code)
		}
	}
}

func TestAsyncWatchMissingTaskFailsFast(t *testing.T) {
	h := controller.NewSessionController(&fakeSessionManager{}, &fakeBrowserRuntime{}, "")
	rr := httptest.NewRecorder()
	start := time.Now()
	h.WatchTask(rr, httptest.NewRequest("POST", "/", strings.NewReader(`{"timeoutMs":30000}`)), "bdt_lost")
	if rr.Code < 400 || time.Since(start) > time.Second {
		t.Fatalf("invalid task did not fail fast: %s", rr.Body)
	}
}
