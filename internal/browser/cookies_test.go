package browser

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"browserd/internal/session"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

func TestCookieParams(t *testing.T) {
	params, err := cookieParams([]CookieInput{{Name: "sid", Value: "a=b%3Bc", URL: "https://example.com/", HTTPOnly: true}, {Name: "empty", Value: "", URL: "https://example.com/"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(params) != 2 || params[0].Value != "a=b%3Bc" || !params[0].HTTPOnly || !params[0].Secure || params[0].Path != "/" {
		t.Fatalf("unexpected params: %+v", params)
	}
	for _, cookies := range [][]CookieInput{
		nil,
		{{Name: "sid", Value: "x", URL: "file:///tmp"}},
		{{Name: "sid", Value: "x", URL: "https://user:pass@example.com"}},
		{{Name: "sid", Value: "x", URL: "https://example.com/page"}},
		{{Name: "a;b", Value: "x", URL: "https://example.com/"}},
		{{Name: "sid", Value: "x\ny", URL: "https://example.com/"}},
		{{Name: "sid", Value: "x", URL: "https://example.com/"}, {Name: "sid", Value: "y", URL: "https://example.com/"}},
		{{Name: "__Host-sid", Value: "x", URL: "http://example.com/"}},
	} {
		if _, err := cookieParams(cookies); err == nil {
			t.Fatalf("accepted invalid cookies: %+v", cookies)
		}
	}
}

func TestImportCookiesInChrome(t *testing.T) {
	chrome := os.Getenv("CHROME_BIN")
	if chrome == "" {
		chrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	}
	if _, err := os.Stat(chrome); err != nil {
		t.Skip("local Chrome is not installed")
	}
	alloc, cancel := chromedp.NewExecAllocator(context.Background(), append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chrome), chromedp.UserDataDir(t.TempDir()))...)
	defer cancel()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "<html>cookie test</html>") }))
	defer server.Close()
	target := server.URL + "/"
	params, err := cookieParams([]CookieInput{{Name: "sid", Value: "opaque=a%3Ab==", URL: target, HTTPOnly: true}, {Name: "empty", Value: "", URL: target}})
	if err != nil {
		t.Fatal(err)
	}
	if count, err := importCookiesIntoContext(ctx, params); err != nil || count != 2 {
		t.Fatalf("import: count=%d err=%v", count, err)
	}
	var visible, header string
	if err := chromedp.Run(ctx, chromedp.Navigate(target), chromedp.Evaluate(`document.cookie`, &visible)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(visible, "sid=") || !strings.Contains(visible, "empty=") {
		t.Fatalf("unexpected visible cookies %q", visible)
	}
	// Updating one imported cookie keeps unrelated cookie values.
	params, err = cookieParams([]CookieInput{{Name: "sid", Value: "new==", URL: target, HTTPOnly: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := importCookiesIntoContext(ctx, params); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		cs, err := network.GetCookies().WithURLs([]string{target}).Do(ctx)
		for _, c := range cs {
			header += c.Name + "=" + c.Value + ";"
		}
		return err
	})); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(header, "sid=new==;") || !strings.Contains(header, "empty=;") {
		t.Fatalf("stored cookies %q", header)
	}
}

func TestProfileCookieRevisionRejectsStaleImports(t *testing.T) {
	svc := &Service{}
	newer := CookieConfig{ProfilePath: "/browser-instances/team/browser/profile.tgz", Revision: 200, Cookies: []CookieInput{}}
	if count, err := svc.ImportProfileCookies(newer); err != nil || count != 0 {
		t.Fatalf("save empty config: count=%d err=%v", count, err)
	}
	stale := newer
	stale.Revision = 100
	if _, err := svc.ImportProfileCookies(stale); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("expected stale rejection: %v", err)
	}
	newer.Revision = 300
	if _, err := svc.ImportProfileCookies(newer); err != nil {
		t.Fatal(err)
	}
}

func TestProfileCookieUpdatesSelectMatchingActiveBrowsers(t *testing.T) {
	svc := &Service{sessions: fakeUploadSessionManager{info: session.SessionInfo{ProfilePath: "/expected/profile.tgz"}}, browsers: map[string]*activeBrowser{"one": {}, "two": {}}}
	config := CookieConfig{ProfilePath: "/expected/profile.tgz", Revision: 1, Cookies: []CookieInput{}}
	count, err := svc.ImportProfileCookies(config)
	if err != nil || count != 2 {
		t.Fatalf("expected two matching sessions, got %d, %v", count, err)
	}
	config.ProfilePath = "/different/profile.tgz"
	count, err = svc.ImportProfileCookies(config)
	if err != nil || count != 0 {
		t.Fatalf("unrelated sessions must stay unchanged: %d, %v", count, err)
	}
	if _, err := svc.ImportCookieConfig("one", config); err == nil {
		t.Fatal("must reject a session with a different profile")
	}
}
