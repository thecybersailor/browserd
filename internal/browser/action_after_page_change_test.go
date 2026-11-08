package browser

import (
	"browserd/internal/fingerprint"
	"browserd/internal/profile"
	"browserd/internal/session"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClickExecutesAfterPageAndTargetChanges(t *testing.T) {
	if os.Getenv("CHROME_BIN") == "" {
		t.Skip("set CHROME_BIN for native link execution acceptance")
	}
	t.Setenv("BROWSERD_LIVE_ENABLED", "false")
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/article" {
			_, _ = w.Write([]byte(`<html><body><h1 data-test-id="article">Article opened</h1></body></html>`))
			return
		}
		_, _ = w.Write([]byte(`<html><body><input data-test-id="order" value="A123"><aside data-test-id="ticker">Ad 1</aside><a data-test-id="story" href="/article" target="_blank">Second news</a></body></html>`))
	}))
	defer site.Close()
	manager := session.NewManager(session.ManagerOptions{Store: profile.NewMemoryStore(), Workdir: t.TempDir()})
	created, err := manager.Create(session.CreateInput{ProfilePath: "/observed-link/profile.tgz", Fingerprint: fingerprint.FromSeed("guarded-link")})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewServiceWithOptions(ServiceOptions{Sessions: manager})
	defer svc.Close(created.RuntimeSessionID)
	if err = svc.PrepareSession(created.RuntimeSessionID); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Navigate(context.Background(), created.RuntimeSessionID, NavigateInput{URL: site.URL, WaitUntil: "load", IncludeSnapshot: true, TimeoutMs: 10000})
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.state.GetSnapshot(created.RuntimeSessionID)
	if err != nil {
		t.Fatal(err)
	}
	ref := ""
	for id, node := range state.Refs {
		if node.TagName == "A" {
			ref = id
		}
	}
	if ref == "" {
		t.Fatal("the observed article link is missing")
	}
	_, err = svc.Evaluate(context.Background(), created.RuntimeSessionID, EvaluateInput{Script: `document.querySelector('[data-test-id="ticker"]').textContent='Ad 2';document.querySelector('[data-test-id="order"]').value='B456';document.querySelector('[data-test-id="story"]').outerHTML='<a data-test-id="story" href="/article" target="_blank">Changed story</a>';return true;`})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Act(created.RuntimeSessionID, ActInput{Action: "click", Ref: ref, MotionProfile: "direct", TimeoutMs: 10000})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		observed, err := svc.Snapshot(created.RuntimeSessionID, SnapshotInput{})
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(observed.Page.URL, "/article") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("click left the owned session on %s instead of the selected article", observed.Page.URL)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
