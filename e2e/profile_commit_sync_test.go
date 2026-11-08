package e2e

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	browserdclient "github.com/thecybersailor/browserd-client-go/pkg/browserd"
)

func TestProfileCommitSyncSafety(t *testing.T) {
	baseURL := strings.TrimRight(os.Getenv("BROWSERD_SYNC_TEST_BASE_URL"), "/")
	workdir := strings.TrimSpace(os.Getenv("BROWSERD_SYNC_TEST_WORKDIR"))
	if baseURL == "" || workdir == "" {
		t.Skip("set BROWSERD_SYNC_TEST_BASE_URL and BROWSERD_SYNC_TEST_WORKDIR to run profile commit sync safety test")
	}
	pageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>sync safety</title></head><body>sync safety</body></html>`))
	}))
	defer pageServer.Close()
	rounds := 8
	if raw := strings.TrimSpace(os.Getenv("BROWSERD_SYNC_TEST_ROUNDS")); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &rounds); err != nil || rounds <= 0 {
			t.Fatalf("invalid BROWSERD_SYNC_TEST_ROUNDS=%q", raw)
		}
	}

	directPass := 0
	for i := 0; i < rounds; i++ {
		ok := runProfileSyncRound(t, baseURL, workdir, pageServer.URL, "direct-commit", i)
		if ok {
			directPass++
		}
	}

	cdpPass := 0
	for i := 0; i < rounds; i++ {
		ok := runProfileSyncRound(t, baseURL, workdir, pageServer.URL, "cdp-close", i)
		if ok {
			cdpPass++
		}
	}

	t.Logf("profile commit restore result: direct-commit=%d/%d cdp-close=%d/%d", directPass, rounds, cdpPass, rounds)
	if cdpPass != rounds {
		t.Fatalf("CDP Browser.close profile checkpoint is not reliable: %d/%d rounds restored persistent cookie and localStorage", cdpPass, rounds)
	}
	if directPass != rounds {
		t.Fatalf("browserd commit endpoint is not reliable: %d/%d rounds restored persistent cookie and localStorage", directPass, rounds)
	}
}

func runProfileSyncRound(t *testing.T, baseURL string, workdir string, pageURL string, mode string, round int) bool {
	t.Helper()
	client := syncClient(t, baseURL)
	profilePath := fmt.Sprintf("/browser-sessions/sync-test/%s/round-%02d/profile.tgz", mode, round)
	session := syncCreateSession(t, client, profilePath)
	runtimeID := session.RuntimeSessionID
	defer syncDeleteSession(client, runtimeID)

	if _, err := navigateAndWatch(t, client, context.Background(), runtimeID, browserdclient.NavigateInput{
		URL:       pageURL,
		WaitUntil: "load",
	}); err != nil {
		t.Fatalf("navigate failed: %v", err)
	}

	key := fmt.Sprintf("sync_%s_%02d", strings.ReplaceAll(mode, "-", "_"), round)
	value := fmt.Sprintf("value_%d", time.Now().UnixNano())
	setScript := fmt.Sprintf(`return (() => {
  document.cookie = %q + "=" + %q + "; expires=Tue, 19 Jan 2038 03:14:07 GMT; path=/; SameSite=Lax";
  localStorage.setItem(%q, %q);
  return {cookie: document.cookie, localStorage: localStorage.getItem(%q)};
})()`, key, value, key, value, key)
	before := syncEvaluate(t, client, runtimeID, setScript)
	if !strings.Contains(stringValue(before["cookie"]), key+"="+value) || stringValue(before["localStorage"]) != value {
		t.Fatalf("%s round %d did not set initial browser state: %#v", mode, round, before)
	}

	if mode == "cdp-close" {
		wsURL := syncDevToolsWSURL(t, workdir, runtimeID)
		syncCloseBrowserWithCDP(t, wsURL)
		syncWaitDevToolsDown(t, wsURL, 10*time.Second)
	}

	commit, err := client.Commit(context.Background(), runtimeID, browserdclient.CommitInput{})
	if err != nil {
		t.Fatalf("%s round %d commit failed: %v", mode, round, err)
	}
	if strings.TrimSpace(commit.NewVersion) == "" {
		t.Fatalf("%s round %d commit returned empty version", mode, round)
	}

	restored := syncCreateSession(t, client, profilePath)
	defer syncDeleteSession(client, restored.RuntimeSessionID)
	if _, err := navigateAndWatch(t, client, context.Background(), restored.RuntimeSessionID, browserdclient.NavigateInput{
		URL:       pageURL,
		WaitUntil: "load",
	}); err != nil {
		t.Fatalf("restored navigate failed: %v", err)
	}
	got := syncEvaluate(t, client, restored.RuntimeSessionID, fmt.Sprintf(`return (() => ({cookie: document.cookie, localStorage: localStorage.getItem(%q)}))()`, key))
	cookieOK := strings.Contains(stringValue(got["cookie"]), key+"="+value)
	localOK := stringValue(got["localStorage"]) == value
	t.Logf("%s round %d restored cookie=%t localStorage=%t", mode, round, cookieOK, localOK)
	return cookieOK && localOK
}

func syncClient(t *testing.T, baseURL string) *browserdclient.Client {
	t.Helper()
	client, err := browserdclient.NewClient(browserdclient.Config{BaseURL: baseURL})
	if err != nil {
		t.Fatalf("new browserd client: %v", err)
	}
	return client
}

func syncCreateSession(t *testing.T, client *browserdclient.Client, profilePath string) browserdclient.Session {
	t.Helper()
	session, err := client.CreateSession(context.Background(), browserdclient.CreateSessionInput{
		ProfilePath: profilePath,
		TTLSeconds:  300,
		Fingerprint: browserdclient.FingerprintConfig{
			Seed:                "sync-safe-test",
			Locale:              "zh-CN",
			Languages:           []string{"zh-CN", "zh"},
			AcceptLanguage:      "zh-CN,zh;q=0.9",
			Timezone:            "Asia/Shanghai",
			Platform:            "Win32",
			OS:                  "Windows",
			UserAgent:           "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
			ViewportWidth:       1366,
			ViewportHeight:      768,
			ScreenWidth:         1366,
			ScreenHeight:        768,
			DeviceScaleFactor:   1,
			HardwareConcurrency: 8,
			DeviceMemory:        8,
			WebGLVendor:         "Google Inc.",
			WebGLRenderer:       "ANGLE (Intel, Intel(R) UHD Graphics Direct3D11 vs_5_0 ps_5_0, D3D11)",
		},
	})
	if err != nil {
		t.Fatalf("create session failed: %v", err)
	}
	return session
}

func syncEvaluate(t *testing.T, client *browserdclient.Client, runtimeID string, script string) map[string]any {
	t.Helper()
	out, err := evaluateAndWatch(t, client, context.Background(), runtimeID, browserdclient.EvaluateInput{
		Script: script,
	})
	if err != nil {
		t.Fatalf("evaluate failed: %v", err)
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("evaluate result should be object, got %#v", out.Result)
	}
	return result
}

func syncDeleteSession(client *browserdclient.Client, runtimeID string) {
	if strings.TrimSpace(runtimeID) == "" {
		return
	}
	_ = client.Close(context.Background(), runtimeID)
}

func syncDevToolsWSURL(t *testing.T, workdir string, runtimeID string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(workdir, "sessions", runtimeID, "profile", "DevToolsActivePort"))
	if err != nil {
		t.Fatalf("read DevToolsActivePort: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) < 2 {
		t.Fatalf("invalid DevToolsActivePort: %q", string(raw))
	}
	return "ws://127.0.0.1:" + strings.TrimSpace(lines[0]) + strings.TrimSpace(lines[1])
}

func syncCloseBrowserWithCDP(t *testing.T, wsURL string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, _, err := ws.Dial(ctx, wsURL)
	if err != nil {
		t.Fatalf("dial cdp websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := wsutil.WriteClientText(conn, []byte(`{"id":1,"method":"Browser.close"}`)); err != nil {
		t.Fatalf("send Browser.close: %v", err)
	}
}

func syncWaitDevToolsDown(t *testing.T, wsURL string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		conn, _, _, err := ws.Dial(ctx, wsURL)
		cancel()
		if err != nil {
			return
		}
		_ = conn.Close()
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("devtools websocket still accepts connections after Browser.close")
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
