package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"browserd/internal/browser"
	"browserd/internal/config"
	"browserd/internal/controller"
	"browserd/internal/live"
	"browserd/internal/profile"
	"browserd/internal/router"
	"browserd/internal/session"

	browserdclient "github.com/flaboy/browserd-client-go/pkg/browserd"
)

type fakeBrowserRuntime struct {
	cookieCalls     [][]browser.CookieInput
	cookieErr       error
	prepareErr      error
	prepareCalls    []string
	checkpointErr   error
	checkpointCalls []string
	closeCalls      []string
	navigateCalls   []browser.NavigateInput
	navigateOut     browser.NavigateOutput
	navigateErr     error
	snapshotOut     browser.SnapshotOutput
	snapshotErr     error
	actCalls        []browser.ActInput
	actOut          browser.ActOutput
	actErr          error
	screenshotCalls []browser.ScreenshotInput
	screenshotOut   browser.ScreenshotOutput
	screenshotErr   error
	uploadCalls     []browser.UploadFilesInput
	uploadOut       browser.UploadFilesOutput
	uploadErr       error
	evaluateCalls   []browser.EvaluateInput
	evaluateOut     browser.EvaluateOutput
	evaluateErr     error
	pageToolCalls   []browserdclient.PageToolInput
	pageToolOut     browserdclient.PageToolResult
	pageToolErr     error
	waitForCalls    []browser.WaitForInput
	waitForOut      browser.WaitForOutput
	waitForErr      error
	pointerSub      browser.PointerSubscription
	pointerErr      error
	pointerCalls    []string
}

type fakeLiveProxyBrowserRuntime struct {
	fakeBrowserRuntime
	target      string
	targetErr   error
	targetCalls int
}

func (f *fakeLiveProxyBrowserRuntime) LiveProxyTarget(_ string) (string, error) {
	f.targetCalls++
	if f.targetErr != nil {
		return "", f.targetErr
	}
	return f.target, nil
}

func (f *fakeBrowserRuntime) PrepareSession(runtimeSessionID string) error {
	f.prepareCalls = append(f.prepareCalls, runtimeSessionID)
	return f.prepareErr
}

func (f *fakeBrowserRuntime) Checkpoint(runtimeSessionID string) error {
	f.checkpointCalls = append(f.checkpointCalls, runtimeSessionID)
	return f.checkpointErr
}

func (f *fakeBrowserRuntime) Close(runtimeSessionID string) error {
	f.closeCalls = append(f.closeCalls, runtimeSessionID)
	return nil
}

func (f *fakeBrowserRuntime) Navigate(_ context.Context, _ string, input browser.NavigateInput) (browser.NavigateOutput, error) {
	f.navigateCalls = append(f.navigateCalls, input)
	return f.navigateOut, f.navigateErr
}

func (f *fakeBrowserRuntime) Snapshot(_ string, _ browser.SnapshotInput) (browser.SnapshotOutput, error) {
	return f.snapshotOut, f.snapshotErr
}

func (f *fakeBrowserRuntime) Act(_ string, input browser.ActInput) (browser.ActOutput, error) {
	f.actCalls = append(f.actCalls, input)
	return f.actOut, f.actErr
}

func (f *fakeBrowserRuntime) Screenshot(_ string, input browser.ScreenshotInput) (browser.ScreenshotOutput, error) {
	f.screenshotCalls = append(f.screenshotCalls, input)
	return f.screenshotOut, f.screenshotErr
}

func (f *fakeBrowserRuntime) UploadFiles(_ string, input browser.UploadFilesInput) (browser.UploadFilesOutput, error) {
	f.uploadCalls = append(f.uploadCalls, input)
	return f.uploadOut, f.uploadErr
}

func (f *fakeBrowserRuntime) Evaluate(_ string, input browser.EvaluateInput) (browser.EvaluateOutput, error) {
	f.evaluateCalls = append(f.evaluateCalls, input)
	return f.evaluateOut, f.evaluateErr
}

func (f *fakeBrowserRuntime) PageTool(_ string, input browserdclient.PageToolInput) (browserdclient.PageToolResult, error) {
	f.pageToolCalls = append(f.pageToolCalls, input)
	if f.pageToolOut != nil || f.pageToolErr != nil {
		return f.pageToolOut, f.pageToolErr
	}
	return browserdclient.PageToolResult{"value": "Example"}, nil
}

func (f *fakeBrowserRuntime) WaitFor(_ string, input browser.WaitForInput) (browser.WaitForOutput, error) {
	f.waitForCalls = append(f.waitForCalls, input)
	return f.waitForOut, f.waitForErr
}

func (f *fakeBrowserRuntime) SubscribePointer(runtimeSessionID string) (browser.PointerSubscription, error) {
	f.pointerCalls = append(f.pointerCalls, runtimeSessionID)
	return f.pointerSub, f.pointerErr
}

type fakeSessionManager struct {
	createOut    session.CreateOutput
	createErr    error
	createCalls  []session.CreateInput
	commitCalls  []session.CommitInput
	commitOut    session.CommitOutput
	commitErr    error
	deleteCalls  []string
	deleteErr    error
	getOut       session.SessionInfo
	getErr       error
	touchCalls   []string
	touchErr     error
	claimCalls   []time.Time
	claimExpired []session.SessionInfo
}

func (f *fakeSessionManager) Create(input session.CreateInput) (session.CreateOutput, error) {
	f.createCalls = append(f.createCalls, input)
	return f.createOut, f.createErr
}

func (f *fakeSessionManager) Commit(_ string, input session.CommitInput) (session.CommitOutput, error) {
	f.commitCalls = append(f.commitCalls, input)
	if f.commitErr != nil {
		return session.CommitOutput{}, f.commitErr
	}
	return f.commitOut, nil
}

func (f *fakeSessionManager) Delete(runtimeSessionID string) error {
	f.deleteCalls = append(f.deleteCalls, runtimeSessionID)
	return f.deleteErr
}

func (f *fakeSessionManager) Get(_ string) (session.SessionInfo, error) {
	if f.getErr != nil {
		return session.SessionInfo{}, f.getErr
	}
	return f.getOut, nil
}

func (f *fakeSessionManager) Touch(runtimeSessionID string) error {
	f.touchCalls = append(f.touchCalls, runtimeSessionID)
	if f.touchErr != nil {
		return f.touchErr
	}
	return nil
}

func (f *fakeSessionManager) ClaimExpired(now time.Time, excluded ...string) []session.SessionInfo {
	f.claimCalls = append(f.claimCalls, now)
	var out []session.SessionInfo
	for _, info := range f.claimExpired {
		skip := false
		for _, id := range excluded {
			if id == info.RuntimeSessionID {
				skip = true
			}
		}
		if !skip {
			out = append(out, info)
		}
	}
	return out
}

func TestAct_TouchesSessionBeforeBrowserUse(t *testing.T) {
	manager := &fakeSessionManager{}
	browserRuntime := &fakeBrowserRuntime{actOut: browser.ActOutput{OK: true, Action: "click"}}
	handler := controller.NewSessionController(manager, browserRuntime, "ws://browserd:9222/devtools/browser")

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/rt_1/act", bytes.NewReader([]byte(`{"action":"click","x":10,"y":20}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.Act(rr, req, "rt_1")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(manager.touchCalls) != 1 || manager.touchCalls[0] != "rt_1" {
		t.Fatalf("expected session touch before browser use, got %+v", manager.touchCalls)
	}
	if len(browserRuntime.actCalls) != 1 {
		t.Fatalf("expected browser act call, got %+v", browserRuntime.actCalls)
	}
}

func TestAct_ReturnsNotFoundWhenTouchFails(t *testing.T) {
	manager := &fakeSessionManager{touchErr: session.ErrSessionNotFound}
	browserRuntime := &fakeBrowserRuntime{actOut: browser.ActOutput{OK: true, Action: "click"}}
	handler := controller.NewSessionController(manager, browserRuntime, "ws://browserd:9222/devtools/browser")

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/rt_1/act", bytes.NewReader([]byte(`{"action":"click","x":10,"y":20}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.Act(rr, req, "rt_1")

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(browserRuntime.actCalls) != 0 {
		t.Fatalf("browser runtime must not be called for expired session, got %+v", browserRuntime.actCalls)
	}
}

func TestReapExpiredSessionsClosesRevokesAndDeletes(t *testing.T) {
	store := live.NewTokenStore(live.TokenStoreOptions{})
	token, _, err := store.Issue(live.IssueRequest{
		RuntimeSessionID: "rt_1",
		HandoffID:        "lv_1",
		Permission:       live.PermissionControl,
		TTL:              time.Minute,
	})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	manager := &fakeSessionManager{
		claimExpired: []session.SessionInfo{{RuntimeSessionID: "rt_1"}},
	}
	browserRuntime := &fakeBrowserRuntime{}
	handler := controller.NewSessionControllerWithLive(controller.SessionControllerOptions{
		Manager:     manager,
		Browser:     browserRuntime,
		LiveBaseURL: "https://browser.example",
		TokenStore:  store,
	})

	reaped := handler.ReapExpiredSessions(time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC))

	if reaped != 1 {
		t.Fatalf("expected one reaped session, got %d", reaped)
	}
	if len(browserRuntime.closeCalls) != 1 || browserRuntime.closeCalls[0] != "rt_1" {
		t.Fatalf("expected browser close, got %+v", browserRuntime.closeCalls)
	}
	if len(manager.deleteCalls) != 1 || manager.deleteCalls[0] != "rt_1" {
		t.Fatalf("expected manager delete, got %+v", manager.deleteCalls)
	}
	if _, ok := store.Lookup(token); ok {
		t.Fatal("expected reaper to revoke live tokens for expired session")
	}
}

func TestAct_ForwardsTrustedInputFields(t *testing.T) {
	browserRuntime := &fakeBrowserRuntime{actOut: browser.ActOutput{OK: true, Action: "type"}}
	controller := controller.NewSessionController(&fakeSessionManager{}, browserRuntime, "ws://browserd:9222/devtools/browser")

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/rt_1/act", bytes.NewReader([]byte(`{
			"action":"type",
			"ref":"e8",
			"text":"你好",
			"clear":true,
			"submit":true,
			"motionProfile":"humanized",
			"timeoutMs":5000
		}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	controller.Act(rr, req, "rt_1")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(browserRuntime.actCalls) != 1 {
		t.Fatalf("expected one act call, got %+v", browserRuntime.actCalls)
	}
	call := browserRuntime.actCalls[0]
	if call.Action != "type" || call.Text != "你好" || !call.Clear || call.MotionProfile != "humanized" || call.TimeoutMs != 5000 {
		t.Fatalf("unexpected act call: %+v", call)
	}
	if !call.Submit {
		t.Fatalf("expected submit to be forwarded, got %+v", call)
	}
	if call.Ref != "e8" {
		t.Fatalf("expected ref target, got %+v", call)
	}
}

func TestAct_ForwardsViewportCoordinates(t *testing.T) {
	browserRuntime := &fakeBrowserRuntime{actOut: browser.ActOutput{OK: true, Action: "click"}}
	controller := controller.NewSessionController(&fakeSessionManager{}, browserRuntime, "ws://browserd:9222/devtools/browser")

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/rt_1/act", bytes.NewReader([]byte(`{
			"action":"click",
			"x":742.5,
			"y":724,
			"motionProfile":"direct",
			"timeoutMs":5000
		}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	controller.Act(rr, req, "rt_1")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(browserRuntime.actCalls) != 1 {
		t.Fatalf("expected one act call, got %+v", browserRuntime.actCalls)
	}
	call := browserRuntime.actCalls[0]
	if call.Action != "click" || call.Ref != "" || call.X != 742.5 || call.Y != 724 || call.MotionProfile != "direct" || call.TimeoutMs != 5000 {
		t.Fatalf("unexpected act call: %+v", call)
	}
}

func TestAct_ForwardsScrollDelta(t *testing.T) {
	browserRuntime := &fakeBrowserRuntime{actOut: browser.ActOutput{OK: true, Action: "scroll"}}
	controller := controller.NewSessionController(&fakeSessionManager{}, browserRuntime, "ws://browserd:9222/devtools/browser")

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/rt_1/act", bytes.NewReader([]byte(`{
			"action":"scroll",
			"x":760,
			"y":690,
			"deltaY":720,
			"deltaX":12,
			"motionProfile":"direct",
			"timeoutMs":5000
		}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	controller.Act(rr, req, "rt_1")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(browserRuntime.actCalls) != 1 {
		t.Fatalf("expected one act call, got %+v", browserRuntime.actCalls)
	}
	call := browserRuntime.actCalls[0]
	if call.Action != "scroll" || call.Ref != "" || call.X != 760 || call.Y != 690 || call.DeltaY != 720 || call.DeltaX != 12 || call.MotionProfile != "direct" || call.TimeoutMs != 5000 {
		t.Fatalf("unexpected act call: %+v", call)
	}
}

func TestAct_ForwardsPastePayload(t *testing.T) {
	browserRuntime := &fakeBrowserRuntime{actOut: browser.ActOutput{OK: true, Action: "paste"}}
	controller := controller.NewSessionController(&fakeSessionManager{}, browserRuntime, "ws://browserd:9222/devtools/browser")

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/rt_1/act", bytes.NewReader([]byte(`{
			"action":"paste",
			"ref":"e8",
			"text":"正文第一段\n正文第二段",
			"html":"<p>正文第一段</p><p>正文第二段</p>",
			"clear":true,
			"motionProfile":"direct",
			"timeoutMs":5000
		}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	controller.Act(rr, req, "rt_1")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(browserRuntime.actCalls) != 1 {
		t.Fatalf("expected one act call, got %+v", browserRuntime.actCalls)
	}
	call := browserRuntime.actCalls[0]
	if call.Action != "paste" || call.Ref != "e8" || call.Text != "正文第一段\n正文第二段" || call.HTML != "<p>正文第一段</p><p>正文第二段</p>" || !call.Clear || call.MotionProfile != "direct" || call.TimeoutMs != 5000 {
		t.Fatalf("unexpected act call: %+v", call)
	}
}

func TestWaitFor_ForwardsActionabilityCondition(t *testing.T) {
	browserRuntime := &fakeBrowserRuntime{waitForOut: browser.WaitForOutput{OK: true, ConditionType: "ref_actionable"}}
	controller := controller.NewSessionController(&fakeSessionManager{}, browserRuntime, "ws://browserd:9222/devtools/browser")

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/rt_1/wait-for", bytes.NewReader([]byte(`{
		"condition":{
			"type":"ref_actionable",
			"ref":"publish_button",
			"checks":["visible","stable","receivesEvents","enabled"]
		},
		"timeoutMs":30000,
		"intervalMs":250,
		"stableMs":500
	}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	controller.WaitFor(rr, req, "rt_1")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(browserRuntime.waitForCalls) != 1 {
		t.Fatalf("expected one waitFor call, got %+v", browserRuntime.waitForCalls)
	}
	call := browserRuntime.waitForCalls[0]
	if call.Condition.Type != "ref_actionable" || call.Condition.Ref != "publish_button" || call.TimeoutMs != 30000 || call.IntervalMs != 250 || call.StableMs != 500 {
		t.Fatalf("unexpected waitFor call: %+v", call)
	}
	wantChecks := []string{"visible", "stable", "receivesEvents", "enabled"}
	if !reflect.DeepEqual(call.Condition.Checks, wantChecks) {
		t.Fatalf("unexpected checks: got %+v want %+v", call.Condition.Checks, wantChecks)
	}
}

func TestWaitFor_ForwardsTextVisibleCondition(t *testing.T) {
	browserRuntime := &fakeBrowserRuntime{waitForOut: browser.WaitForOutput{OK: true, ConditionType: "text_visible"}}
	controller := controller.NewSessionController(&fakeSessionManager{}, browserRuntime, "ws://browserd:9222/devtools/browser")

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/rt_1/wait-for", bytes.NewReader([]byte(`{
		"condition":{"type":"text_visible","text":"审核中"},
		"timeoutMs":10000
	}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	controller.WaitFor(rr, req, "rt_1")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(browserRuntime.waitForCalls) != 1 || browserRuntime.waitForCalls[0].Condition.Text != "审核中" {
		t.Fatalf("unexpected waitFor calls: %+v", browserRuntime.waitForCalls)
	}
}

func TestAct_RejectsTargetField(t *testing.T) {
	browserRuntime := &fakeBrowserRuntime{actOut: browser.ActOutput{OK: true, Action: "type"}}
	controller := controller.NewSessionController(&fakeSessionManager{}, browserRuntime, "ws://browserd:9222/devtools/browser")

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/rt_1/act", bytes.NewReader([]byte(`{
		"action":"type",
		"ref":"e8",
		"target":{"ref":"e8"},
		"text":"你好"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	controller.Act(rr, req, "rt_1")

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(browserRuntime.actCalls) != 0 {
		t.Fatalf("expected target request to fail before browser runtime, got %+v", browserRuntime.actCalls)
	}
}

func TestCreateSession_ReturnsCdpWsUrlAndLeaseEcho(t *testing.T) {
	manager := session.NewManager(session.ManagerOptions{
		Store:      profile.NewMemoryStore(),
		Workdir:    t.TempDir(),
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	handler := controller.NewSessionController(manager, &fakeBrowserRuntime{}, "ws://browserd:9222/devtools/browser")

	body := []byte(`{
		"profilePath":"/browser-sessions/t_1/c_1/bs_1/profile.tgz",
		"fingerprint":{"seed":"fp_seed_1","locale":"en-US","languages":["en-US","en"],"acceptLanguage":"en-US,en;q=0.9","timezone":"America/New_York","platform":"Win32","os":"Windows","userAgent":"Mozilla/5.0 test","viewportWidth":1366,"viewportHeight":768,"screenWidth":1366,"screenHeight":768,"deviceScaleFactor":1,"hardwareConcurrency":8,"deviceMemory":8,"webglVendor":"Google Inc.","webglRenderer":"ANGLE Test"},
		"leaseId":"lease_1"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.CreateSession(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	data := out["data"].(map[string]any)
	if data["leaseId"] != "lease_1" {
		t.Fatalf("leaseId mismatch: %+v", data)
	}
	cdp := data["cdpWsUrl"].(string)
	if !strings.HasPrefix(cdp, "ws://browserd:9222/devtools/browser/rt_") {
		t.Fatalf("unexpected cdpWsUrl: %s", cdp)
	}
}

func TestCreateSession_AcceptsLogicalProfilePath(t *testing.T) {
	manager := &fakeSessionManager{
		createOut: session.CreateOutput{
			RuntimeSessionID: "rt_1",
			CDPWsURL:         "ws://browserd:9222/devtools/browser/rt_1",
			LeaseID:          "lease_1",
			ResolvedVersion:  "new",
		},
	}
	controller := controller.NewSessionController(manager, &fakeBrowserRuntime{}, "ws://browserd:9222/devtools/browser")

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader([]byte(`{
		"profilePath":"/browser-sessions/dev/douyin/local/profile.tgz",
		"expectedVersion":"stale-version-must-be-ignored",
		"fingerprint":{"seed":"fp_seed_1","locale":"en-US","languages":["en-US","en"],"acceptLanguage":"en-US,en;q=0.9","timezone":"America/New_York","platform":"Win32","os":"Windows","userAgent":"Mozilla/5.0 test","viewportWidth":1366,"viewportHeight":768,"screenWidth":1366,"screenHeight":768,"deviceScaleFactor":1,"hardwareConcurrency":8,"deviceMemory":8,"webglVendor":"Google Inc.","webglRenderer":"ANGLE Test"}
	}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	controller.CreateSession(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(manager.createCalls) != 1 {
		t.Fatalf("expected one create call, got %+v", manager.createCalls)
	}
	if manager.createCalls[0].ProfilePath != "/browser-sessions/dev/douyin/local/profile.tgz" {
		t.Fatalf("profile path was not forwarded: %+v", manager.createCalls[0])
	}
}

func TestCreateSession_PreparesBrowserBeforeReturning(t *testing.T) {
	manager := &fakeSessionManager{
		createOut: session.CreateOutput{
			RuntimeSessionID: "rt_1",
			CDPWsURL:         "ws://browserd:9222/devtools/browser/rt_1",
			LeaseID:          "lease_1",
			ResolvedVersion:  "new",
		},
	}
	browserRuntime := &fakeBrowserRuntime{}
	controller := controller.NewSessionController(manager, browserRuntime, "ws://browserd:9222/devtools/browser")

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader([]byte(`{
		"profilePath":"/browser-sessions/t_1/c_1/bs_1/profile.tgz",
		"fingerprint":{"seed":"fp_seed_1","locale":"en-US","languages":["en-US","en"],"acceptLanguage":"en-US,en;q=0.9","timezone":"America/New_York","platform":"Win32","os":"Windows","userAgent":"Mozilla/5.0 test","viewportWidth":1366,"viewportHeight":768,"screenWidth":1366,"screenHeight":768,"deviceScaleFactor":1,"hardwareConcurrency":8,"deviceMemory":8,"webglVendor":"Google Inc.","webglRenderer":"ANGLE Test"}
	}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	controller.CreateSession(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(browserRuntime.prepareCalls) != 1 || browserRuntime.prepareCalls[0] != "rt_1" {
		t.Fatalf("expected prepare to run before returning, got %+v", browserRuntime.prepareCalls)
	}
	if manager.deleteCalls != nil {
		t.Fatalf("did not expect delete on success, got %+v", manager.deleteCalls)
	}
}

func TestCreateSession_DeletesSessionWhenBrowserPrepareFails(t *testing.T) {
	manager := &fakeSessionManager{
		createOut: session.CreateOutput{
			RuntimeSessionID: "rt_1",
			CDPWsURL:         "ws://browserd:9222/devtools/browser/rt_1",
		},
	}
	browserRuntime := &fakeBrowserRuntime{
		prepareErr: errors.New("devtools websocket not ready"),
	}
	controller := controller.NewSessionController(manager, browserRuntime, "ws://browserd:9222/devtools/browser")

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader([]byte(`{
		"profilePath":"/browser-sessions/t_1/c_1/bs_1/profile.tgz",
		"fingerprint":{"seed":"fp_seed_1","locale":"en-US","languages":["en-US","en"],"acceptLanguage":"en-US,en;q=0.9","timezone":"America/New_York","platform":"Win32","os":"Windows","userAgent":"Mozilla/5.0 test","viewportWidth":1366,"viewportHeight":768,"screenWidth":1366,"screenHeight":768,"deviceScaleFactor":1,"hardwareConcurrency":8,"deviceMemory":8,"webglVendor":"Google Inc.","webglRenderer":"ANGLE Test"}
	}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	controller.CreateSession(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(manager.deleteCalls) != 1 || manager.deleteCalls[0] != "rt_1" {
		t.Fatalf("expected session cleanup, got %+v", manager.deleteCalls)
	}
	if len(browserRuntime.closeCalls) != 1 || browserRuntime.closeCalls[0] != "rt_1" {
		t.Fatalf("expected browser close on prepare failure, got %+v", browserRuntime.closeCalls)
	}
}

func TestCreateSession_ProxyHopFailureReturnsSpecificError(t *testing.T) {
	manager := &fakeSessionManager{
		createOut: session.CreateOutput{
			RuntimeSessionID: "rt_1",
			CDPWsURL:         "ws://browserd:9222/devtools/browser/rt_1",
		},
	}
	browserRuntime := &fakeBrowserRuntime{
		prepareErr: browser.ErrProxyHopConnectFailed,
	}
	controller := controller.NewSessionController(manager, browserRuntime, "ws://browserd:9222/devtools/browser")

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader([]byte(`{
		"profilePath":"/browser-sessions/t_1/c_1/bs_1/profile.tgz",
		"fingerprint":{"seed":"fp_seed_1","locale":"en-US","languages":["en-US","en"],"acceptLanguage":"en-US,en;q=0.9","timezone":"America/New_York","platform":"Win32","os":"Windows","userAgent":"Mozilla/5.0 test","viewportWidth":1366,"viewportHeight":768,"screenWidth":1366,"screenHeight":768,"deviceScaleFactor":1,"hardwareConcurrency":8,"deviceMemory":8,"webglVendor":"Google Inc.","webglRenderer":"ANGLE Test"},
		"proxyServer":"http://user:pass@proxy.example.com:8080"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	controller.CreateSession(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "PROXY_HOP_CONNECT_FAILED") {
		t.Fatalf("expected proxy hop error, got %s", rr.Body.String())
	}
	if len(manager.deleteCalls) != 1 || manager.deleteCalls[0] != "rt_1" {
		t.Fatalf("expected session cleanup, got %+v", manager.deleteCalls)
	}
}

func TestCreateSession_PrepareFailureRemovesRuntimeSessionFromManager(t *testing.T) {
	manager := session.NewManager(session.ManagerOptions{
		Store:      profile.NewMemoryStore(),
		Workdir:    t.TempDir(),
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	browserRuntime := &fakeBrowserRuntime{prepareErr: errors.New("devtools websocket not ready")}
	controller := controller.NewSessionController(manager, browserRuntime, "ws://browserd:9222/devtools/browser")

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader([]byte(`{
		"profilePath":"/browser-sessions/t_1/c_1/bs_1/profile.tgz",
		"fingerprint":{"seed":"fp_seed_1","locale":"en-US","languages":["en-US","en"],"acceptLanguage":"en-US,en;q=0.9","timezone":"America/New_York","platform":"Win32","os":"Windows","userAgent":"Mozilla/5.0 test","viewportWidth":1366,"viewportHeight":768,"screenWidth":1366,"screenHeight":768,"deviceScaleFactor":1,"hardwareConcurrency":8,"deviceMemory":8,"webglVendor":"Google Inc.","webglRenderer":"ANGLE Test"}
	}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	controller.CreateSession(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", rr.Code, rr.Body.String())
	}
	for _, runtimeSessionID := range browserRuntime.prepareCalls {
		if _, err := manager.Get(runtimeSessionID); err == nil {
			t.Fatalf("expected failed create session to be removed: %s", runtimeSessionID)
		}
	}
}

func TestCreateSession_RequiresFingerprintConfig(t *testing.T) {
	manager := session.NewManager(session.ManagerOptions{
		Store:      profile.NewMemoryStore(),
		Workdir:    t.TempDir(),
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	handler := controller.NewSessionController(manager, nil, "ws://browserd:9222/devtools/browser")

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader([]byte(`{
		"profilePath":"/browser-sessions/t_1/c_1/bs_1/profile.tgz"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.CreateSession(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "INVALID_FINGERPRINT_CONFIG") || !strings.Contains(rr.Body.String(), "seed is required") {
		t.Fatalf("expected actionable fingerprint error, got %s", rr.Body.String())
	}
}

func TestCreateSession_RejectsInvalidProxyServer(t *testing.T) {
	manager := &fakeSessionManager{}
	handler := controller.NewSessionController(manager, nil, "ws://browserd:9222/devtools/browser")

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader([]byte(`{
		"profilePath":"/browser-sessions/t_1/c_1/bs_1/profile.tgz",
		"fingerprint":{"seed":"fp_seed_1","locale":"en-US","languages":["en-US","en"],"acceptLanguage":"en-US,en;q=0.9","timezone":"America/New_York","platform":"Win32","os":"Windows","userAgent":"Mozilla/5.0 test","viewportWidth":1366,"viewportHeight":768,"screenWidth":1366,"screenHeight":768,"deviceScaleFactor":1,"hardwareConcurrency":8,"deviceMemory":8,"webglVendor":"Google Inc.","webglRenderer":"ANGLE Test"},
		"proxyServer":"https://proxy.example.com:443"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.CreateSession(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "INVALID_PROXY_SERVER") {
		t.Fatalf("expected invalid proxy error, got %s", rr.Body.String())
	}
	if len(manager.createCalls) != 0 {
		t.Fatalf("invalid proxy must not create session, got %+v", manager.createCalls)
	}
}

func TestCreateSession_ForwardsFingerprintAndProxyServer(t *testing.T) {
	manager := &fakeSessionManager{
		createOut: session.CreateOutput{
			RuntimeSessionID: "rt_1",
			CDPWsURL:         "ws://browserd:9222/devtools/browser/rt_1",
			LeaseID:          "lease_1",
			ResolvedVersion:  "new",
		},
	}
	handler := controller.NewSessionController(manager, nil, "ws://browserd:9222/devtools/browser")

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader([]byte(`{
		"profilePath":"/browser-sessions/t_1/c_1/bs_1/profile.tgz",
		"fingerprint":{"seed":"fp_seed_1","locale":"en-US","languages":["en-US","en"],"acceptLanguage":"en-US,en;q=0.9","timezone":"America/New_York","platform":"Win32","os":"Windows","userAgent":"Mozilla/5.0 test","viewportWidth":1366,"viewportHeight":768,"screenWidth":1366,"screenHeight":768,"deviceScaleFactor":1,"hardwareConcurrency":8,"deviceMemory":8,"webglVendor":"Google Inc.","webglRenderer":"ANGLE Test"},
		"proxyServer":"http://user:pass@proxy.example.com:8080"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.CreateSession(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(manager.createCalls) != 1 {
		t.Fatalf("expected one create call, got %+v", manager.createCalls)
	}
	got := manager.createCalls[0]
	if got.Fingerprint.Seed != "fp_seed_1" || got.Fingerprint.Timezone != "America/New_York" || got.Fingerprint.ViewportWidth != 1366 {
		t.Fatalf("fingerprint mismatch: %+v", got)
	}
	if got.ProxyServer != "http://user:pass@proxy.example.com:8080" {
		t.Fatalf("proxy server mismatch: %+v", got)
	}
}

func TestCommitSession_DoesNotRequireIfMatchVersion(t *testing.T) {
	manager := session.NewManager(session.ManagerOptions{
		Store:      profile.NewMemoryStore(),
		Workdir:    t.TempDir(),
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	handler := controller.NewSessionController(manager, &fakeBrowserRuntime{}, "ws://browserd:9222/devtools/browser")

	create := []byte(`{
		"profilePath":"/browser-sessions/t_1/c_1/bs_1/profile.tgz",
		"fingerprint":{"seed":"fp_seed_1","locale":"en-US","languages":["en-US","en"],"acceptLanguage":"en-US,en;q=0.9","timezone":"America/New_York","platform":"Win32","os":"Windows","userAgent":"Mozilla/5.0 test","viewportWidth":1366,"viewportHeight":768,"screenWidth":1366,"screenHeight":768,"deviceScaleFactor":1,"hardwareConcurrency":8,"deviceMemory":8,"webglVendor":"Google Inc.","webglRenderer":"ANGLE Test"}
	}`)
	creq := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader(create))
	creq.Header.Set("Content-Type", "application/json")
	crr := httptest.NewRecorder()
	handler.CreateSession(crr, creq)
	if crr.Code != http.StatusOK {
		t.Fatalf("create expected 200, got %d body=%s", crr.Code, crr.Body.String())
	}

	var cbody map[string]any
	_ = json.Unmarshal(crr.Body.Bytes(), &cbody)
	rid := cbody["data"].(map[string]any)["runtimeSessionId"].(string)

	commit := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+rid+"/commit", bytes.NewReader(commit))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.CommitSession(rr, req, rid)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCommitSession_DoesNotCommitWhenCheckpointFails(t *testing.T) {
	manager := &fakeSessionManager{commitOut: session.CommitOutput{NewVersion: "v2"}}
	browserRuntime := &fakeBrowserRuntime{checkpointErr: browser.ErrProfileCheckpointFailed}
	handler := controller.NewSessionController(manager, browserRuntime, "ws://browserd:9222/devtools/browser")

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/rt_1/commit", bytes.NewReader([]byte(`{"ifMatchVersion":"v1"}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.CommitSession(rr, req, "rt_1")

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "PROFILE_CHECKPOINT_FAILED") {
		t.Fatalf("expected checkpoint error response, got %s", rr.Body.String())
	}
	if len(browserRuntime.checkpointCalls) != 1 || browserRuntime.checkpointCalls[0] != "rt_1" {
		t.Fatalf("expected checkpoint call, got %+v", browserRuntime.checkpointCalls)
	}
	if len(manager.commitCalls) != 0 {
		t.Fatalf("manager commit must not run after checkpoint failure, got %+v", manager.commitCalls)
	}
}

func TestCommitSession_DeletesRuntimeSessionAfterSuccessfulCommit(t *testing.T) {
	manager := &fakeSessionManager{commitOut: session.CommitOutput{NewVersion: "v2"}}
	browserRuntime := &fakeBrowserRuntime{}
	handler := controller.NewSessionController(manager, browserRuntime, "ws://browserd:9222/devtools/browser")

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/rt_1/commit", bytes.NewReader([]byte(`{"ifMatchVersion":"v1"}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.CommitSession(rr, req, "rt_1")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(manager.deleteCalls) != 1 || manager.deleteCalls[0] != "rt_1" {
		t.Fatalf("expected runtime session delete after commit, got %+v", manager.deleteCalls)
	}
}

func TestCommitSession_IgnoresIfMatchVersionField(t *testing.T) {
	manager := &fakeSessionManager{commitOut: session.CommitOutput{NewVersion: "v2"}}
	handler := controller.NewSessionController(manager, &fakeBrowserRuntime{}, "ws://browserd:9222/devtools/browser")

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/rt_1/commit", bytes.NewReader([]byte(`{"ifMatchVersion":"stale"}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.CommitSession(rr, req, "rt_1")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(manager.commitCalls) != 1 {
		t.Fatalf("expected one commit call, got %+v", manager.commitCalls)
	}
}

func TestNavigate_ForwardsAfterLoadScreenshotS3Path(t *testing.T) {
	browserRuntime := &fakeBrowserRuntime{
		navigateOut: browser.NavigateOutput{
			URL:             "https://news.163.com/",
			Title:           "News",
			SnapshotCleared: true,
		},
	}
	handler := controller.NewSessionController(&fakeSessionManager{}, browserRuntime, "ws://browserd:9222/devtools/browser")

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/rt_1/navigate", bytes.NewReader([]byte(`{
		"url":"https://news.163.com",
		"waitUntil":"load",
		"afterLoadScreenshotS3Path":"s3://browserd-snapshots/team_1/conv_1/1737373333.png"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.Navigate(rr, req, "rt_1")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(browserRuntime.navigateCalls) != 1 {
		t.Fatalf("expected one navigate call, got %+v", browserRuntime.navigateCalls)
	}
	got := browserRuntime.navigateCalls[0]
	if got.AfterLoadScreenshotS3Path != "s3://browserd-snapshots/team_1/conv_1/1737373333.png" {
		t.Fatalf("unexpected afterLoadScreenshotS3Path: %+v", got)
	}
	if got.URL != "https://news.163.com" || got.WaitUntil != "load" {
		t.Fatalf("unexpected navigate input: %+v", got)
	}
}

func TestScreenshot_ForwardsS3PrefixAndReturnsArtifactMetadata(t *testing.T) {
	browserRuntime := &fakeBrowserRuntime{
		screenshotOut: browser.ScreenshotOutput{
			ScreenshotID: "123e4567-e89b-12d3-a456-426614174000.png",
			S3Path:       "/browser-screenshots/2026-08/team_1/conv_1/123e4567-e89b-12d3-a456-426614174000.png",
			ContentType:  "image/png",
			ByteLength:   9,
		},
	}
	handler := controller.NewSessionController(&fakeSessionManager{}, browserRuntime, "ws://browserd:9222/devtools/browser")

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/rt_1/screenshot", bytes.NewReader([]byte(`{
		"mode":"selector",
		"selector":"form.login-panel",
		"format":"png",
		"screenshotS3Prefix":"/browser-screenshots/2026-08/team_1/conv_1/"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.Screenshot(rr, req, "rt_1")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(browserRuntime.screenshotCalls) != 1 {
		t.Fatalf("expected one screenshot call, got %+v", browserRuntime.screenshotCalls)
	}
	got := browserRuntime.screenshotCalls[0]
	if got.Mode != "selector" || got.Selector != "form.login-panel" || got.Format != "png" || got.ScreenshotS3Prefix != "/browser-screenshots/2026-08/team_1/conv_1/" {
		t.Fatalf("unexpected screenshot input: %+v", got)
	}
	if strings.Contains(rr.Body.String(), "base64") {
		t.Fatalf("screenshot response must not expose base64: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"screenshotId":"123e4567-e89b-12d3-a456-426614174000.png"`) {
		t.Fatalf("missing screenshotId in response: %s", rr.Body.String())
	}
}

func TestScreenshot_DefaultsToViewportMode(t *testing.T) {
	browserRuntime := &fakeBrowserRuntime{
		screenshotOut: browser.ScreenshotOutput{
			ScreenshotID: "shot.png",
			S3Path:       "/browser-screenshots/2026-08/team_1/conv_1/shot.png",
			ContentType:  "image/png",
			ByteLength:   9,
		},
	}
	handler := controller.NewSessionController(&fakeSessionManager{}, browserRuntime, "ws://browserd:9222/devtools/browser")

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/rt_1/screenshot", bytes.NewReader([]byte(`{
		"format":"png",
		"screenshotS3Prefix":"/browser-screenshots/2026-08/team_1/conv_1/"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.Screenshot(rr, req, "rt_1")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(browserRuntime.screenshotCalls) != 1 {
		t.Fatalf("expected one screenshot call, got %+v", browserRuntime.screenshotCalls)
	}
	got := browserRuntime.screenshotCalls[0]
	if got.Mode != "viewport" || got.Selector != "" {
		t.Fatalf("expected viewport default, got %+v", got)
	}
}

func TestScreenshot_RejectsRef(t *testing.T) {
	handler := controller.NewSessionController(&fakeSessionManager{}, &fakeBrowserRuntime{}, "ws://browserd:9222/devtools/browser")
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/rt_1/screenshot", bytes.NewReader([]byte(`{
		"ref":"e104",
		"format":"png",
		"screenshotS3Prefix":"/browser-screenshots/2026-08/team_1/conv_1/"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.Screenshot(rr, req, "rt_1")

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "ref is not supported") {
		t.Fatalf("expected ref rejection, got %s", rr.Body.String())
	}
}

func TestScreenshot_RejectsFullPage(t *testing.T) {
	handler := controller.NewSessionController(&fakeSessionManager{}, &fakeBrowserRuntime{}, "ws://browserd:9222/devtools/browser")
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/rt_1/screenshot", bytes.NewReader([]byte(`{
		"fullPage":true,
		"format":"png",
		"screenshotS3Prefix":"/browser-screenshots/2026-08/team_1/conv_1/"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.Screenshot(rr, req, "rt_1")

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "fullPage is not supported") {
		t.Fatalf("expected fullPage rejection, got %s", rr.Body.String())
	}
}

func TestNavigateRoute_RejectsUnknownSession(t *testing.T) {
	h := router.New(config.Config{Port: 7011, CDPBaseURL: "ws://browserd:9222/devtools/browser"})

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/rt_missing/navigate", bytes.NewReader([]byte(`{"url":"https://example.com"}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestSnapshotRoute_UsesPageAsSingleStructure(t *testing.T) {
	controller := controller.NewSessionController(&fakeSessionManager{}, &fakeBrowserRuntime{
		snapshotOut: browser.SnapshotOutput{
			SnapshotID: "snap_1",
			Page: browser.PageSnapshot{
				URL:   "https://example.com",
				Title: "Example",
				Groups: map[string]browser.PageTable{
					"buttons": {
						Columns: []string{"ref", "tag", "text"},
						Rows:    [][]any{{"e1", "BUTTON", "Submit"}},
					},
				},
			},
		},
	}, "ws://browserd:9222/devtools/browser")

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/rt_1/snapshot?mode=refs", nil)
	rr := httptest.NewRecorder()
	controller.Snapshot(rr, req, "rt_1")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	data := body["data"].(map[string]any)
	if _, ok := data["page"]; !ok {
		t.Fatalf("expected page field, got %+v", data)
	}
	if _, ok := data["refs"]; ok {
		t.Fatalf("expected refs field to be removed: %+v", data)
	}
	if _, ok := data["text"]; ok {
		t.Fatalf("expected text field to be removed: %+v", data)
	}
}

func TestHandoffStart_ReturnsControlViewerURL(t *testing.T) {
	manager := session.NewManager(session.ManagerOptions{
		Store:      profile.NewMemoryStore(),
		Workdir:    t.TempDir(),
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	handler := controller.NewSessionControllerWithLive(controller.SessionControllerOptions{
		Manager:      manager,
		Browser:      &fakeBrowserRuntime{},
		CDPBaseURL:   "ws://browserd:9222/devtools/browser",
		LiveBaseURL:  "https://browser.example",
		LiveTokenTTL: 15 * time.Minute,
		TokenStore:   live.NewTokenStore(live.TokenStoreOptions{}),
	})
	rid := createTestSession(t, handler)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+rid+"/handoff/start", bytes.NewReader([]byte(`{
		"permission":"control",
		"ttlSeconds":900
	}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.StartHandoff(rr, req, rid)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	data := decodeData(t, rr)
	if !strings.HasPrefix(data["handoffId"].(string), "ho_") {
		t.Fatalf("expected handoff id, got %+v", data)
	}
	if !strings.HasPrefix(data["viewerUrl"].(string), "https://browser.example/v/") {
		t.Fatalf("unexpected viewerUrl: %+v", data)
	}
	if data["permission"] != "control" {
		t.Fatalf("unexpected permission: %+v", data)
	}
	if data["expiresAt"] == "" {
		t.Fatalf("expected expiresAt: %+v", data)
	}
}

func TestLiveView_ReturnsViewOnlyViewerURL(t *testing.T) {
	manager := session.NewManager(session.ManagerOptions{
		Store:      profile.NewMemoryStore(),
		Workdir:    t.TempDir(),
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	handler := controller.NewSessionControllerWithLive(controller.SessionControllerOptions{
		Manager:      manager,
		Browser:      &fakeBrowserRuntime{},
		CDPBaseURL:   "ws://browserd:9222/devtools/browser",
		LiveBaseURL:  "https://browser.example/",
		LiveTokenTTL: 15 * time.Minute,
		TokenStore:   live.NewTokenStore(live.TokenStoreOptions{}),
	})
	rid := createTestSession(t, handler)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+rid+"/live-view", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.LiveView(rr, req, rid)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	data := decodeData(t, rr)
	if data["permission"] != "view" {
		t.Fatalf("unexpected permission: %+v", data)
	}
	if !strings.HasPrefix(data["viewerUrl"].(string), "https://browser.example/v/") {
		t.Fatalf("unexpected viewerUrl: %+v", data)
	}
}

func TestServePointerEventsRequiresValidLiveToken(t *testing.T) {
	manager := session.NewManager(session.ManagerOptions{
		Store:      profile.NewMemoryStore(),
		Workdir:    t.TempDir(),
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	handler := controller.NewSessionControllerWithLive(controller.SessionControllerOptions{
		Manager:     manager,
		Browser:     &fakeBrowserRuntime{},
		LiveBaseURL: "https://browser.example",
		TokenStore:  live.NewTokenStore(live.TokenStoreOptions{}),
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v/bad-token/pointer-events", nil)

	handler.ServePointerEvents(rr, req, "bad-token")

	if rr.Code != http.StatusGone {
		t.Fatalf("expected invalid token to fail, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestServePointerEventsStreamsSanitizedSnapshots(t *testing.T) {
	store := live.NewTokenStore(live.TokenStoreOptions{})
	browserRuntime := &fakeBrowserRuntime{
		pointerSub: browser.PointerSubscription{
			C: closedPointerChannel(browser.VirtualPointerSnapshot{
				X: 1, Y: 2, ViewportWidth: 100, ViewportHeight: 80, Visible: true,
			}),
			Cancel: func() {},
		},
	}
	manager := session.NewManager(session.ManagerOptions{
		Store:      profile.NewMemoryStore(),
		Workdir:    t.TempDir(),
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	handler := controller.NewSessionControllerWithLive(controller.SessionControllerOptions{
		Manager:     manager,
		Browser:     browserRuntime,
		LiveBaseURL: "https://browser.example",
		TokenStore:  store,
	})
	rid := createTestSession(t, handler)
	token, _, err := store.Issue(live.IssueRequest{
		RuntimeSessionID: rid,
		HandoffID:        "lv_1",
		Permission:       live.PermissionView,
		TTL:              time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v/"+token+"/pointer-events", nil)

	handler.ServePointerEvents(rr, req, token)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected stream response, got %d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "event: pointer") || !strings.Contains(body, `"visible":true`) {
		t.Fatalf("expected pointer SSE body, got %s", body)
	}
	if strings.Contains(body, rid) {
		t.Fatalf("pointer stream must not expose runtime session id: %s", body)
	}
	if len(browserRuntime.pointerCalls) != 1 || browserRuntime.pointerCalls[0] != rid {
		t.Fatalf("expected pointer subscription for %s, got %+v", rid, browserRuntime.pointerCalls)
	}
}

func TestEvaluate_ReturnsJSONResult(t *testing.T) {
	manager := session.NewManager(session.ManagerOptions{
		Store:      profile.NewMemoryStore(),
		Workdir:    t.TempDir(),
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	browserRuntime := &fakeBrowserRuntime{
		evaluateOut: browser.EvaluateOutput{
			Result: map[string]any{"title": "Example"},
			URL:    "https://example.com/",
			Title:  "Example",
		},
	}
	handler := controller.NewSessionControllerWithLive(controller.SessionControllerOptions{
		Manager:    manager,
		Browser:    browserRuntime,
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	rid := createTestSession(t, handler)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+rid+"/evaluate", bytes.NewReader([]byte(`{
		"script":"return { title: document.title }",
		"args":["ok"],
		"timeoutMs":1000
	}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.Evaluate(rr, req, rid)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	data := decodeData(t, rr)
	result := data["result"].(map[string]any)
	if result["title"] != "Example" {
		t.Fatalf("unexpected result: %+v", data)
	}
	if len(browserRuntime.evaluateCalls) != 1 {
		t.Fatalf("expected one evaluate call, got %d", len(browserRuntime.evaluateCalls))
	}
	if browserRuntime.evaluateCalls[0].Script != "return { title: document.title }" {
		t.Fatalf("unexpected script: %+v", browserRuntime.evaluateCalls[0])
	}
}

func TestPageTool_ForwardsCurrentContract(t *testing.T) {
	manager := session.NewManager(session.ManagerOptions{
		Store:      profile.NewMemoryStore(),
		Workdir:    t.TempDir(),
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	browserRuntime := &fakeBrowserRuntime{}
	handler := controller.NewSessionControllerWithLive(controller.SessionControllerOptions{
		Manager:    manager,
		Browser:    browserRuntime,
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	rid := createTestSession(t, handler)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+rid+"/pageTool", strings.NewReader(`{"method":"page.title","payload":{}}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.PageTool(rr, req, rid)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if len(browserRuntime.pageToolCalls) != 1 || browserRuntime.pageToolCalls[0].Method != "page.title" {
		t.Fatalf("unexpected calls: %#v", browserRuntime.pageToolCalls)
	}
}

func TestEvaluate_ForwardsPageToolBridgeConfig(t *testing.T) {
	manager := session.NewManager(session.ManagerOptions{
		Store:      profile.NewMemoryStore(),
		Workdir:    t.TempDir(),
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	browserRuntime := &fakeBrowserRuntime{
		evaluateOut: browser.EvaluateOutput{
			Result: map[string]any{"ok": true},
			URL:    "https://example.com/",
			Title:  "Example",
		},
	}
	handler := controller.NewSessionControllerWithLive(controller.SessionControllerOptions{
		Manager:    manager,
		Browser:    browserRuntime,
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	rid := createTestSession(t, handler)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+rid+"/evaluate", strings.NewReader(`{"script":"return true","pageToolBridge":{"enabled":true,"name":"__browserdPageToolCall"}}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.Evaluate(rr, req, rid)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if len(browserRuntime.evaluateCalls) != 1 || browserRuntime.evaluateCalls[0].PageToolBridge == nil || !browserRuntime.evaluateCalls[0].PageToolBridge.Enabled {
		t.Fatalf("pageTool bridge was not forwarded: %#v", browserRuntime.evaluateCalls)
	}
}

func TestEvaluate_DuringHandoffRequiresExplicitOptIn(t *testing.T) {
	manager := session.NewManager(session.ManagerOptions{
		Store:      profile.NewMemoryStore(),
		Workdir:    t.TempDir(),
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	browserRuntime := &fakeBrowserRuntime{
		evaluateOut: browser.EvaluateOutput{
			Result: map[string]any{"loggedIn": true},
			URL:    "https://example.com/",
			Title:  "Example",
		},
	}
	handler := controller.NewSessionControllerWithLive(controller.SessionControllerOptions{
		Manager:     manager,
		Browser:     browserRuntime,
		CDPBaseURL:  "ws://browserd:9222/devtools/browser",
		LiveBaseURL: "https://browser.example",
		TokenStore:  live.NewTokenStore(live.TokenStoreOptions{}),
	})
	rid := createTestSession(t, handler)
	startControlHandoff(t, handler, rid)

	blockedReq := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+rid+"/evaluate", bytes.NewReader([]byte(`{
		"script":"return { loggedIn: true }"
	}`)))
	blockedReq.Header.Set("Content-Type", "application/json")
	blockedRR := httptest.NewRecorder()
	handler.Evaluate(blockedRR, blockedReq, rid)
	if blockedRR.Code != http.StatusConflict {
		t.Fatalf("expected active handoff evaluate to be blocked, got %d body=%s", blockedRR.Code, blockedRR.Body.String())
	}
	if len(browserRuntime.evaluateCalls) != 0 {
		t.Fatalf("blocked evaluate should not reach browser runtime")
	}

	allowedReq := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+rid+"/evaluate", bytes.NewReader([]byte(`{
		"script":"return { loggedIn: true }",
		"allowDuringHandoff":true
	}`)))
	allowedReq.Header.Set("Content-Type", "application/json")
	allowedRR := httptest.NewRecorder()
	handler.Evaluate(allowedRR, allowedReq, rid)
	if allowedRR.Code != http.StatusOK {
		t.Fatalf("expected explicit opt-in evaluate to pass, got %d body=%s", allowedRR.Code, allowedRR.Body.String())
	}
	if len(browserRuntime.evaluateCalls) != 1 || browserRuntime.evaluateCalls[0].Script != "return { loggedIn: true }" {
		t.Fatalf("unexpected evaluate calls: %+v", browserRuntime.evaluateCalls)
	}
}

func TestUploadFiles_ForwardsControlledSources(t *testing.T) {
	manager := session.NewManager(session.ManagerOptions{
		Store:      profile.NewMemoryStore(),
		Workdir:    t.TempDir(),
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	browserRuntime := &fakeBrowserRuntime{
		uploadOut: browser.UploadFilesOutput{OK: true, Ref: "file_1", FileNames: []string{"cover.png"}},
	}
	handler := controller.NewSessionControllerWithLive(controller.SessionControllerOptions{
		Manager:    manager,
		Browser:    browserRuntime,
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	rid := createTestSession(t, handler)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+rid+"/upload-files", bytes.NewReader([]byte(`{
		"ref":"file_1",
		"x":321.5,
		"y":222.25,
		"files":[
			{"s3Path":"s3://browserd-assets/team_1/cover.png","filename":"cover.png"},
			{"url":"https://cdn.example.test/team_1/gallery.png","filename":"gallery.png"}
		],
		"timeoutMs":1000
	}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.UploadFiles(rr, req, rid)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(browserRuntime.uploadCalls) != 1 {
		t.Fatalf("expected one upload call, got %d", len(browserRuntime.uploadCalls))
	}
	call := browserRuntime.uploadCalls[0]
	if call.Ref != "file_1" || call.X != 321.5 || call.Y != 222.25 || call.TimeoutMs != 1000 || len(call.Files) != 2 {
		t.Fatalf("unexpected upload call: %+v", call)
	}
	if call.Files[0].S3Path != "s3://browserd-assets/team_1/cover.png" || call.Files[0].Filename != "cover.png" {
		t.Fatalf("unexpected S3 upload source: %+v", call.Files[0])
	}
	if call.Files[1].URL != "https://cdn.example.test/team_1/gallery.png" || call.Files[1].Filename != "gallery.png" {
		t.Fatalf("unexpected upload call: %+v", call)
	}
	data := decodeData(t, rr)
	if data["ref"] != "file_1" {
		t.Fatalf("unexpected response: %+v", data)
	}
	if strings.Contains(rr.Body.String(), "/tmp/") {
		t.Fatalf("upload response must not leak local paths: %s", rr.Body.String())
	}
}

func TestUploadFiles_MapsFailureCode(t *testing.T) {
	manager := session.NewManager(session.ManagerOptions{
		Store:      profile.NewMemoryStore(),
		Workdir:    t.TempDir(),
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	browserRuntime := &fakeBrowserRuntime{
		uploadErr: fmt.Errorf("%w: input rejected", browser.ErrUploadFilesFailed),
	}
	handler := controller.NewSessionControllerWithLive(controller.SessionControllerOptions{
		Manager:    manager,
		Browser:    browserRuntime,
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	rid := createTestSession(t, handler)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+rid+"/upload-files", bytes.NewReader([]byte(`{"ref":"file_1","files":[{"s3Path":"/key.png"}]}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.UploadFiles(rr, req, rid)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	errBody := body["error"].(map[string]any)
	if errBody["code"] != "UPLOAD_FILES_FAILED" {
		t.Fatalf("unexpected error: %+v", body)
	}
}

func TestUploadFiles_MapsUploadSourceFetchFailureCode(t *testing.T) {
	manager := session.NewManager(session.ManagerOptions{
		Store:      profile.NewMemoryStore(),
		Workdir:    t.TempDir(),
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	browserRuntime := &fakeBrowserRuntime{
		uploadErr: fmt.Errorf("%w: GET http://asset.example.test/missing.png returned HTTP 404: missing", browser.ErrUploadSourceFetchFailed),
	}
	handler := controller.NewSessionControllerWithLive(controller.SessionControllerOptions{
		Manager:    manager,
		Browser:    browserRuntime,
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	rid := createTestSession(t, handler)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+rid+"/upload-files", bytes.NewReader([]byte(`{"ref":"file_1","files":[{"url":"http://asset.example.test/missing.png"}]}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.UploadFiles(rr, req, rid)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	errBody := body["error"].(map[string]any)
	if errBody["code"] != "UPLOAD_SOURCE_FETCH_FAILED" {
		t.Fatalf("unexpected error: %+v", body)
	}
	if !strings.Contains(fmt.Sprint(errBody["message"]), "HTTP 404") {
		t.Fatalf("expected actionable error message, got %+v", errBody)
	}
}

func TestEvaluate_MapsNonJSONResultErrorCode(t *testing.T) {
	manager := session.NewManager(session.ManagerOptions{
		Store:      profile.NewMemoryStore(),
		Workdir:    t.TempDir(),
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	browserRuntime := &fakeBrowserRuntime{
		evaluateErr: fmt.Errorf("%w: exception \"Uncaught Error: EVALUATE_RESULT_NOT_JSON: $ is DOM Node\"", browser.ErrEvaluateFailed),
	}
	handler := controller.NewSessionControllerWithLive(controller.SessionControllerOptions{
		Manager:    manager,
		Browser:    browserRuntime,
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	rid := createTestSession(t, handler)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+rid+"/evaluate", bytes.NewReader([]byte(`{"script":"return document.body"}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.Evaluate(rr, req, rid)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	errBody := body["error"].(map[string]any)
	if errBody["code"] != "EVALUATE_RESULT_NOT_JSON" {
		t.Fatalf("unexpected error: %+v", body)
	}
	if !strings.Contains(errBody["message"].(string), "return a JSON-serializable value") {
		t.Fatalf("missing hint: %+v", body)
	}
}

func TestHandoffComplete_RevokesViewerToken(t *testing.T) {
	manager := session.NewManager(session.ManagerOptions{
		Store:      profile.NewMemoryStore(),
		Workdir:    t.TempDir(),
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	handler := controller.NewSessionControllerWithLive(controller.SessionControllerOptions{
		Manager:      manager,
		Browser:      &fakeBrowserRuntime{},
		CDPBaseURL:   "ws://browserd:9222/devtools/browser",
		LiveBaseURL:  "https://browser.example",
		LiveTokenTTL: 15 * time.Minute,
		TokenStore:   live.NewTokenStore(live.TokenStoreOptions{}),
	})
	rid := createTestSession(t, handler)

	startReq := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+rid+"/handoff/start", bytes.NewReader([]byte(`{"permission":"control"}`)))
	startRR := httptest.NewRecorder()
	handler.StartHandoff(startRR, startReq, rid)
	if startRR.Code != http.StatusOK {
		t.Fatalf("expected start 200, got %d body=%s", startRR.Code, startRR.Body.String())
	}
	startData := decodeData(t, startRR)
	handoffID := startData["handoffId"].(string)
	token := liveTokenFromTestViewerURL(t, startData["viewerUrl"].(string))

	liveReq := httptest.NewRequest(http.MethodGet, "/v/"+token+"/", nil)
	liveRR := httptest.NewRecorder()
	handler.ServeLiveView(liveRR, liveReq, token)
	if liveRR.Code != http.StatusOK {
		t.Fatalf("expected live token before complete, got %d body=%s", liveRR.Code, liveRR.Body.String())
	}

	completeReq := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+rid+"/handoff/"+handoffID+"/complete", nil)
	completeRR := httptest.NewRecorder()
	handler.CompleteHandoff(completeRR, completeReq, rid, handoffID)
	if completeRR.Code != http.StatusOK {
		t.Fatalf("expected complete 200, got %d body=%s", completeRR.Code, completeRR.Body.String())
	}

	revokedRR := httptest.NewRecorder()
	handler.ServeLiveView(revokedRR, liveReq, token)
	if revokedRR.Code != http.StatusGone {
		t.Fatalf("expected revoked token to return 410, got %d body=%s", revokedRR.Code, revokedRR.Body.String())
	}
}

func TestServeLiveView_ServesViewerShellWithoutLiveProxyHealth(t *testing.T) {
	manager := session.NewManager(session.ManagerOptions{
		Store:      profile.NewMemoryStore(),
		Workdir:    t.TempDir(),
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	browserRuntime := &fakeLiveProxyBrowserRuntime{targetErr: browser.ErrLiveRuntimeUnhealthy}
	handler := controller.NewSessionControllerWithLive(controller.SessionControllerOptions{
		Manager:      manager,
		Browser:      browserRuntime,
		CDPBaseURL:   "ws://browserd:9222/devtools/browser",
		LiveBaseURL:  "https://browser.example",
		LiveTokenTTL: 15 * time.Minute,
		TokenStore:   live.NewTokenStore(live.TokenStoreOptions{}),
	})
	rid := createTestSession(t, handler)
	token := startControlHandoff(t, handler, rid)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v/"+token+"/vnc.html", nil)
	handler.ServeLiveView(rr, req, token)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected viewer shell to return 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if browserRuntime.targetCalls != 0 {
		t.Fatalf("expected viewer shell to skip live proxy health, got %d calls", browserRuntime.targetCalls)
	}
	if !strings.Contains(rr.Body.String(), "/browser-live/assets/") {
		t.Fatalf("expected hashed browser-live assets in viewer shell, got %s", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "/core/") {
		t.Fatalf("viewer shell must not load noVNC modules under token path, got %s", rr.Body.String())
	}
}

func TestServeLiveView_WebsockifyReturnsUnhealthyWhenLiveProxyTargetFails(t *testing.T) {
	manager := session.NewManager(session.ManagerOptions{
		Store:      profile.NewMemoryStore(),
		Workdir:    t.TempDir(),
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	handler := controller.NewSessionControllerWithLive(controller.SessionControllerOptions{
		Manager:      manager,
		Browser:      &fakeLiveProxyBrowserRuntime{targetErr: browser.ErrLiveRuntimeUnhealthy},
		CDPBaseURL:   "ws://browserd:9222/devtools/browser",
		LiveBaseURL:  "https://browser.example",
		LiveTokenTTL: 15 * time.Minute,
		TokenStore:   live.NewTokenStore(live.TokenStoreOptions{}),
	})
	rid := createTestSession(t, handler)
	token := startControlHandoff(t, handler, rid)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v/"+token+"/websockify", nil)
	handler.ServeLiveView(rr, req, token)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected unhealthy live proxy to return 503, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "LIVE_RUNTIME_UNHEALTHY") {
		t.Fatalf("expected LIVE_RUNTIME_UNHEALTHY body, got %s", rr.Body.String())
	}
}

func TestHandoffControlDisconnectAutoCompletesAfterGrace(t *testing.T) {
	proxy, release := newBlockingLiveProxy(t)
	defer proxy.Close()

	manager := session.NewManager(session.ManagerOptions{
		Store:      profile.NewMemoryStore(),
		Workdir:    t.TempDir(),
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	browserRuntime := &fakeLiveProxyBrowserRuntime{
		target: proxy.URL,
		fakeBrowserRuntime: fakeBrowserRuntime{
			navigateOut: browser.NavigateOutput{URL: "https://example.com/", Title: "Example"},
		},
	}
	handler := controller.NewSessionControllerWithLive(controller.SessionControllerOptions{
		Manager:                manager,
		Browser:                browserRuntime,
		CDPBaseURL:             "ws://browserd:9222/devtools/browser",
		LiveBaseURL:            "https://browser.example",
		LiveTokenTTL:           15 * time.Minute,
		HandoffDisconnectGrace: 25 * time.Millisecond,
		TokenStore:             live.NewTokenStore(live.TokenStoreOptions{}),
	})
	rid := createTestSession(t, handler)
	token := startControlHandoff(t, handler, rid)

	done := serveLiveWebsockify(t, handler, token)
	release()
	<-done

	assertNavigateStatus(t, handler, rid, http.StatusConflict)
	eventually(t, 500*time.Millisecond, func() bool {
		return navigateStatus(handler, rid) == http.StatusOK
	})

	revokedRR := httptest.NewRecorder()
	handler.ServeLiveView(revokedRR, httptest.NewRequest(http.MethodGet, "/v/"+token+"/", nil), token)
	if revokedRR.Code != http.StatusGone {
		t.Fatalf("expected auto-completed token to return 410, got %d body=%s", revokedRR.Code, revokedRR.Body.String())
	}
}

func TestHandoffControlReconnectCancelsAutoComplete(t *testing.T) {
	proxy, releaseFirst := newBlockingLiveProxy(t)
	defer proxy.Close()

	handler, rid := newLiveHandoffTestController(t, proxy.URL, 50*time.Millisecond)
	token := startControlHandoff(t, handler, rid)

	firstDone := serveLiveWebsockify(t, handler, token)
	releaseFirst()
	<-firstDone

	_, releaseSecond := replaceBlockingLiveProxyHandler(t, proxy)
	secondDone := serveLiveWebsockify(t, handler, token)
	time.Sleep(80 * time.Millisecond)
	assertNavigateStatus(t, handler, rid, http.StatusConflict)

	releaseSecond()
	<-secondDone
	eventually(t, 500*time.Millisecond, func() bool {
		return navigateStatus(handler, rid) == http.StatusOK
	})
}

func TestHandoffExplicitCompleteCancelsDisconnectTimer(t *testing.T) {
	proxy, release := newBlockingLiveProxy(t)
	defer proxy.Close()

	handler, rid := newLiveHandoffTestController(t, proxy.URL, 50*time.Millisecond)
	startReq := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+rid+"/handoff/start", bytes.NewReader([]byte(`{"permission":"control"}`)))
	startRR := httptest.NewRecorder()
	handler.StartHandoff(startRR, startReq, rid)
	if startRR.Code != http.StatusOK {
		t.Fatalf("expected start 200, got %d body=%s", startRR.Code, startRR.Body.String())
	}
	startData := decodeData(t, startRR)
	token := liveTokenFromTestViewerURL(t, startData["viewerUrl"].(string))
	handoffID := startData["handoffId"].(string)

	done := serveLiveWebsockify(t, handler, token)
	release()
	<-done

	completeReq := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+rid+"/handoff/"+handoffID+"/complete", nil)
	completeRR := httptest.NewRecorder()
	handler.CompleteHandoff(completeRR, completeReq, rid, handoffID)
	if completeRR.Code != http.StatusOK {
		t.Fatalf("expected complete 200, got %d body=%s", completeRR.Code, completeRR.Body.String())
	}
	time.Sleep(80 * time.Millisecond)
	assertNavigateStatus(t, handler, rid, http.StatusOK)
}

func TestViewOnlyLiveViewDisconnectDoesNotCompleteActiveHandoff(t *testing.T) {
	proxy, release := newBlockingLiveProxy(t)
	defer proxy.Close()

	handler, rid := newLiveHandoffTestController(t, proxy.URL, 25*time.Millisecond)
	_ = startControlHandoff(t, handler, rid)

	liveReq := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+rid+"/live-view", bytes.NewReader([]byte(`{}`)))
	liveRR := httptest.NewRecorder()
	handler.LiveView(liveRR, liveReq, rid)
	if liveRR.Code != http.StatusOK {
		t.Fatalf("expected live view 200, got %d body=%s", liveRR.Code, liveRR.Body.String())
	}
	viewToken := liveTokenFromTestViewerURL(t, decodeData(t, liveRR)["viewerUrl"].(string))

	done := serveLiveWebsockify(t, handler, viewToken)
	release()
	<-done
	time.Sleep(60 * time.Millisecond)
	assertNavigateStatus(t, handler, rid, http.StatusConflict)
}

func newLiveHandoffTestController(t *testing.T, target string, grace time.Duration) (*controller.SessionController, string) {
	t.Helper()
	manager := session.NewManager(session.ManagerOptions{
		Store:      profile.NewMemoryStore(),
		Workdir:    t.TempDir(),
		CDPBaseURL: "ws://browserd:9222/devtools/browser",
	})
	handler := controller.NewSessionControllerWithLive(controller.SessionControllerOptions{
		Manager:                manager,
		Browser:                &fakeLiveProxyBrowserRuntime{target: target, fakeBrowserRuntime: fakeBrowserRuntime{navigateOut: browser.NavigateOutput{URL: "https://example.com/"}}},
		CDPBaseURL:             "ws://browserd:9222/devtools/browser",
		LiveBaseURL:            "https://browser.example",
		LiveTokenTTL:           15 * time.Minute,
		HandoffDisconnectGrace: grace,
		TokenStore:             live.NewTokenStore(live.TokenStoreOptions{}),
	})
	return handler, createTestSession(t, handler)
}

func newBlockingLiveProxy(t *testing.T) (*httptest.Server, func()) {
	t.Helper()
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/websockify" {
			http.NotFound(w, r)
			return
		}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	return server, func() {
		close(release)
	}
}

func replaceBlockingLiveProxyHandler(t *testing.T, server *httptest.Server) (*httptest.Server, func()) {
	t.Helper()
	release := make(chan struct{})
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/websockify" {
			http.NotFound(w, r)
			return
		}
		<-release
		w.WriteHeader(http.StatusOK)
	})
	return server, func() {
		close(release)
	}
}

func startControlHandoff(t *testing.T, handler *controller.SessionController, rid string) string {
	t.Helper()
	startReq := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+rid+"/handoff/start", bytes.NewReader([]byte(`{"permission":"control"}`)))
	startRR := httptest.NewRecorder()
	handler.StartHandoff(startRR, startReq, rid)
	if startRR.Code != http.StatusOK {
		t.Fatalf("expected start 200, got %d body=%s", startRR.Code, startRR.Body.String())
	}
	return liveTokenFromTestViewerURL(t, decodeData(t, startRR)["viewerUrl"].(string))
}

func serveLiveWebsockify(t *testing.T, handler *controller.SessionController, token string) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v/"+token+"/websockify", nil)
		handler.ServeLiveView(rr, req, token)
	}()
	return done
}

func assertNavigateStatus(t *testing.T, handler *controller.SessionController, rid string, want int) {
	t.Helper()
	if got := navigateStatus(handler, rid); got != want {
		t.Fatalf("expected navigate status %d, got %d", want, got)
	}
}

func navigateStatus(handler *controller.SessionController, rid string) int {
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+rid+"/navigate", bytes.NewReader([]byte(`{"url":"https://example.com"}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.Navigate(rr, req, rid)
	return rr.Code
}

func closedPointerChannel(snapshot browser.VirtualPointerSnapshot) <-chan browser.VirtualPointerSnapshot {
	ch := make(chan browser.VirtualPointerSnapshot, 1)
	ch <- snapshot
	close(ch)
	return ch
}

func eventually(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition was not met within %s", timeout)
}

func liveTokenFromTestViewerURL(t *testing.T, viewerURL string) string {
	t.Helper()
	withoutBase := strings.TrimPrefix(viewerURL, "https://browser.example/v/")
	token, _, _ := strings.Cut(withoutBase, "/")
	if token == "" {
		t.Fatalf("expected viewer token in %q", viewerURL)
	}
	return token
}

func createTestSession(t *testing.T, handler *controller.SessionController) string {
	t.Helper()

	body := []byte(`{
		"profilePath":"/browser-sessions/opaque/profile.tgz",
		"fingerprint":{"seed":"fp_seed_1","locale":"en-US","languages":["en-US","en"],"acceptLanguage":"en-US,en;q=0.9","timezone":"America/New_York","platform":"Win32","os":"Windows","userAgent":"Mozilla/5.0 test","viewportWidth":1366,"viewportHeight":768,"screenWidth":1366,"screenHeight":768,"deviceScaleFactor":1,"hardwareConcurrency":8,"deviceMemory":8,"webglVendor":"Google Inc.","webglRenderer":"ANGLE Test"}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.CreateSession(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("create expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	data := decodeData(t, rr)
	return data["runtimeSessionId"].(string)
}

func decodeData(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data: %+v", body)
	}
	return data
}

func TestNavigateIncludeSnapshotContract(t *testing.T) {
	runtime := &fakeBrowserRuntime{navigateOut: browser.NavigateOutput{URL: "https://example.com/", Snapshot: &browser.SnapshotOutput{SnapshotID: "s", Page: browser.PageSnapshot{URL: "https://example.com/", Groups: map[string]browser.PageTable{}}}}}
	handler := controller.NewSessionController(&fakeSessionManager{}, runtime, "")
	for _, wait := range []string{"load", "networkidle"} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/sessions/rt/navigate", strings.NewReader(`{"url":"https://example.com/","includeSnapshot":true,"waitUntil":"`+wait+`"}`))
		handler.Navigate(rr, req, "rt")
		if wait == "load" {
			if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"snapshotId":"s"`) {
				t.Fatalf("missing snapshot: %s", rr.Body.String())
			}
			if !runtime.navigateCalls[0].IncludeSnapshot {
				t.Fatal("flag not forwarded")
			}
		} else if rr.Code != 400 || len(runtime.navigateCalls) != 1 {
			t.Fatal("invalid wait reached browser")
		}
	}
}

type blockedNavigationRuntime struct {
	fakeBrowserRuntime
	started chan struct{}
	release chan struct{}
}

func (b *blockedNavigationRuntime) Navigate(ctx context.Context, id string, in browser.NavigateInput) (browser.NavigateOutput, error) {
	close(b.started)
	select {
	case <-b.release:
		return browser.NavigateOutput{URL: in.URL}, nil
	case <-ctx.Done():
		return browser.NavigateOutput{}, ctx.Err()
	}
}

func TestSessionOperationGuardRejectsInterleaving(t *testing.T) {
	b := &blockedNavigationRuntime{started: make(chan struct{}), release: make(chan struct{})}
	m := &fakeSessionManager{claimExpired: []session.SessionInfo{{RuntimeSessionID: "one"}, {RuntimeSessionID: "expired"}}}
	h := controller.NewSessionController(m, b, "")
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Navigate(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(`{"url":"https://example.com/"}`)), "one")
	}()
	<-b.started
	for _, call := range []func(http.ResponseWriter, *http.Request, string){h.Snapshot, h.Act, h.Evaluate, h.PageTool, h.WaitFor, h.Screenshot, h.UploadFiles, h.CommitSession, h.DeleteSession, h.StartHandoff} {
		rr := httptest.NewRecorder()
		call(rr, httptest.NewRequest("POST", "/", strings.NewReader(`{}`)), "one")
		if rr.Code != 409 || !strings.Contains(rr.Body.String(), "SESSION_BUSY") {
			t.Errorf("interleaving: %d %s", rr.Code, rr.Body.String())
		}
	}
	if n := h.ReapExpiredSessions(time.Now()); n != 1 {
		t.Errorf("reaped %d, want only other expired session", n)
	}
	for _, id := range b.closeCalls {
		if id == "one" {
			t.Error("closed active navigation")
		}
	}
	close(b.release)
	<-done
	if n := h.ReapExpiredSessions(time.Now()); n != 2 {
		t.Errorf("released session was not claimable: %d", n)
	}
	rr := httptest.NewRecorder()
	h.Snapshot(rr, httptest.NewRequest("GET", "/", nil), "one")
	if rr.Code != 200 {
		t.Fatalf("guard leaked: %s", rr.Body.String())
	}
}

func (f *fakeBrowserRuntime) ImportCookies(_ string, cookies []browser.CookieInput) (int, error) {
	f.cookieCalls = append(f.cookieCalls, cookies)
	return len(cookies), f.cookieErr
}

func (f *fakeBrowserRuntime) ImportCookieConfig(id string, config browser.CookieConfig) (int, error) {
	return f.ImportCookies(id, config.Cookies)
}
func (f *fakeBrowserRuntime) ImportProfileCookies(config browser.CookieConfig) (int, error) {
	_, err := f.ImportCookies("", config.Cookies)
	return 0, err
}
