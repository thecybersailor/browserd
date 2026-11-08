package e2e

import (
	"encoding/json"
	"fmt"
	sdk "github.com/thecybersailor/browserd-client-go/pkg/browserd"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNavigateSnapshotE2E(t *testing.T) {
	base := strings.TrimRight(os.Getenv("BROWSERD_BASE_URL"), "/")
	if base == "" {
		t.Skip("NOT EXECUTED: BROWSERD_BASE_URL is required")
	}
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/network":
			fmt.Fprint(w, `<body>Live catalog $25<script>fetch('/stream')</script></body>`)
		case "/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: connected\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case "/broken":
			fmt.Fprint(w, `<body>Extraction failure<script>document.createRange = () => {throw new Error('forced extraction failure')}</script></body>`)
		case "/redirect":
			http.Redirect(w, r, "/catalog", http.StatusFound)
		case "/slow":
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second * 2):
			}
			fmt.Fprint(w, "<body>slow</body>")
		case "/async":
			fmt.Fprint(w, `<body>Loading<script>setTimeout(()=>{document.body.innerHTML='<button data-test-id="buy">Buy delayed item $25</button>'},700)</script></body>`)
		default:
			fmt.Fprint(w, `<title>Catalog</title><body><a href="/item">Real item $25</a><button onclick="this.textContent='Purchased'">Buy item</button></body>`)
		}
	}))
	defer site.Close()
	status, created := mustDoJSON(t, "POST", base+"/v1/sessions", map[string]any{"profilePath": fmt.Sprintf("/accounts/nav-e2e/%d/profile.tgz", time.Now().UnixNano()), "fingerprint": smokeFingerprint()})
	if status != 200 {
		t.Fatalf("create: %d %+v", status, created)
	}
	id := fmt.Sprint(created.Data["runtimeSessionId"])
	session := base + "/v1/sessions/" + id
	defer mustDoJSON(t, "DELETE", session, nil)
	var snapshotID string
	for _, path := range []string{"/catalog", "/redirect"} {
		out := successfulTaskObject(t, submitHTTPTaskAndWatch(t, session, "navigate", map[string]any{"url": site.URL + path, "includeSnapshot": true, "waitUntil": "load", "executionTimeoutMs": 5000}))
		snap, ok := out["snapshot"].(map[string]any)
		if !ok {
			t.Fatalf("navigation did not return snapshot: %+v", out)
		}
		if snap["snapshotId"] == "" || snap["snapshotId"] == snapshotID {
			t.Fatal("snapshot ID not renewed")
		}
		snapshotID = fmt.Sprint(snap["snapshotId"])
		page := snap["page"].(map[string]any)
		if page["url"] != site.URL+"/catalog" || out["url"] != page["url"] {
			t.Fatalf("redirect observation inconsistent: %+v", out)
		}
		raw, _ := json.Marshal(page)
		if !strings.Contains(string(raw), "Real item $25") {
			t.Fatal("missing real page facts")
		}
		ref := firstTreeRef(t, page, "button")
		status, act := mustDoJSON(t, "POST", session+"/act", map[string]any{"action": "click", "ref": ref})
		if status != 200 {
			t.Fatalf("returned ref cannot act: %d %+v", status, act)
		}
	}
	out := successfulTaskObject(t, submitHTTPTaskAndWatch(t, session, "navigate", map[string]any{"url": site.URL + "/network", "includeSnapshot": true, "executionTimeoutMs": 3000}))
	if out["snapshot"] == nil {
		t.Fatalf("continuous background network blocked load observation: %+v", out)
	}
	broken := submitHTTPTaskAndWatch(t, session, "navigate", map[string]any{"url": site.URL + "/broken", "includeSnapshot": true, "executionTimeoutMs": 3000})
	if broken.State != sdk.TaskFailed || len(broken.Info.Result) != 0 || broken.Info.Error == nil || broken.Info.Error.Code != "SNAPSHOT_FAILED" {
		t.Fatalf("extraction failure not distinguished: %+v", broken)
	}
	status, response := mustDoJSON(t, "POST", session+"/act", map[string]any{"action": "click", "ref": "e1"})
	if status == 200 {
		t.Fatal("failed observation retained actionable stale refs")
	}
	out = successfulTaskObject(t, submitHTTPTaskAndWatch(t, session, "navigate", map[string]any{"url": site.URL + "/async", "includeSnapshot": true}))
	if out["snapshot"] == nil {
		t.Fatalf("async navigation failed: %+v", out)
	}
	successfulTaskObject(t, submitHTTPTaskAndWatch(t, session, "wait-for", map[string]any{"condition": map[string]any{"type": "element_visible", "selector": "[data-test-id=buy]"}, "executionTimeoutMs": 5000}))
	status, response = mustDoJSON(t, "GET", session+"/snapshot", nil)
	raw, _ := json.Marshal(response.Data)
	if status != 200 || !strings.Contains(string(raw), "Buy delayed item $25") {
		t.Fatalf("fresh observation missing async facts: %s", raw)
	}
	// Evaluate -> native PageTool must not recursively acquire the request guard.
	successfulTaskObject(t, submitHTTPTaskAndWatch(t, session, "evaluate", map[string]any{"script": "return await window.__browserdPageToolCall({method:'page.title',payload:{}});", "pageToolBridge": map[string]any{"name": "__browserdPageToolCall", "enabled": true}, "executionTimeoutMs": 3000}))
	slow := submitHTTPTaskAndWatch(t, session, "navigate", map[string]any{"url": site.URL + "/slow", "includeSnapshot": true, "executionTimeoutMs": 100})
	if slow.State != sdk.TaskTimedOut || len(slow.Info.Result) != 0 {
		t.Fatalf("timeout returned success: %+v", slow)
	}
}
