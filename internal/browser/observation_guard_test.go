package browser

import (
	"context"
	"errors"
	"github.com/chromedp/chromedp"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestObservationGuardRealChromeRejectsReplacedAndBlockedTargets(t *testing.T) {
	binary := os.Getenv("CHROME_BIN")
	if binary == "" {
		t.Skip("set CHROME_BIN for real observation validation")
	}
	alloc, cancel := chromedp.NewExecAllocator(context.Background(), append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(binary), chromedp.UserDataDir(t.TempDir()))...)
	defer cancel()
	page, closePage := chromedp.NewContext(alloc)
	defer closePage()
	ctx, stop := context.WithTimeout(page, 30*time.Second)
	defer stop()
	defer func() {
		shutdown, cancel := context.WithTimeout(page, 5*time.Second)
		defer cancel()
		if err := chromedp.Cancel(shutdown); err != nil {
			t.Errorf("close observation test browser: %v", err)
		}
	}()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body><input aria-label="Order" value="A123"><button id="save">Save</button></body></html>`))
	}))
	defer site.Close()
	for _, tc := range []struct {
		name, mutation string
		want           error
	}{
		{"replaced", `document.querySelector('button').outerHTML = '<button id="save">Save</button>'`, ErrStaleObservation},
		{"field_changed", `document.querySelector('input').value = 'B456'`, ErrStaleObservation},
		{"target_meaning_changed", `document.querySelector('button').setAttribute('aria-label','Delete order')`, ErrStaleObservation},
		{"overlay", `const div = document.createElement('div');div.style='position:fixed;inset:0;z-index:999;background:white';document.body.append(div)`, ErrTargetNotActionable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := chromedp.Run(ctx, chromedp.Navigate(site.URL)); err != nil {
				t.Fatal(err)
			}
			s := NewService(nil, nil, nil)
			observed, err := s.snapshotWithContext(ctx, "rt")
			if err != nil {
				t.Fatal(err)
			}
			state, err := s.state.GetSnapshot("rt")
			if err != nil {
				t.Fatal(err)
			}
			ref := ""
			for id, target := range state.Refs {
				if target.TagName == "BUTTON" {
					ref = id
				}
			}
			if ref == "" {
				t.Fatal("button missing from observation")
			}
			input := ActInput{Action: "click", Ref: ref, ExpectedSnapshotID: observed.SnapshotID}
			if err = s.validateObservation(ctx, "rt", input); err != nil {
				t.Fatalf("fresh target rejected: %v", err)
			}
			if err = chromedp.Run(ctx, chromedp.Evaluate(`(()=>{`+tc.mutation+`;return true})()`, nil)); err != nil {
				t.Fatal(err)
			}
			if err = s.validateObservation(ctx, "rt", input); !errors.Is(err, tc.want) {
				t.Fatalf("changed target not rejected: %v, want %v", err, tc.want)
			}
		})
	}
}
