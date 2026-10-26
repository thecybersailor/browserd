package controller_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"browserd/internal/controller"
)

func TestImportCookiesForwardsValuesAndReturnsCount(t *testing.T) {
	runtime := &fakeBrowserRuntime{}
	handler := controller.NewSessionController(&fakeSessionManager{}, runtime, "")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/rt_1/cookies/import", strings.NewReader(`{"cookies":[{"name":"sid","value":"a=b%3Bc","url":"https://example.com/","httpOnly":true}]}`))
	handler.ImportCookies(rr, req, "rt_1")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(runtime.cookieCalls) != 1 || runtime.cookieCalls[0][0].Value != "a=b%3Bc" || !runtime.cookieCalls[0][0].HTTPOnly {
		t.Fatalf("calls=%+v", runtime.cookieCalls)
	}
	if !strings.Contains(rr.Body.String(), `"importedCount":1`) {
		t.Fatal(rr.Body.String())
	}
}

func TestImportCookiesRejectsMalformedContract(t *testing.T) {
	for _, body := range []string{`{}`, `{"cookies":[],"unknown":true}`, `{"cookies":[]} {}`, `{"cookies":[{"name":"sid","extra":true}]}`} {
		runtime := &fakeBrowserRuntime{}
		handler := controller.NewSessionController(&fakeSessionManager{}, runtime, "")
		rr := httptest.NewRecorder()
		handler.ImportCookies(rr, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), "rt_1")
		if rr.Code != http.StatusBadRequest || len(runtime.cookieCalls) != 0 {
			t.Fatalf("body=%s status=%d calls=%+v", body, rr.Code, runtime.cookieCalls)
		}
	}
}
