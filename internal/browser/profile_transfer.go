package browser

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"browserd/internal/fingerprint"
	"browserd/internal/session"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"
)

//go:embed portable/runtime.js
var portableRuntime string

type PortableCookie struct {
	Name           string                      `json:"name"`
	Value          string                      `json:"value"`
	Domain         string                      `json:"domain"`
	Path           string                      `json:"path"`
	Secure         bool                        `json:"secure"`
	HTTPOnly       bool                        `json:"httpOnly"`
	HostOnly       bool                        `json:"hostOnly"`
	SameSite       string                      `json:"sameSite"`
	ExpirationDate *float64                    `json:"expirationDate,omitempty"`
	PartitionKey   *network.CookiePartitionKey `json:"partitionKey,omitempty"`
}

type ProfileBundle struct {
	Format      string `json:"format"`
	Version     int    `json:"version"`
	ExportedAt  string `json:"exportedAt,omitempty"`
	Fingerprint struct {
		Seed   string             `json:"seed"`
		Config fingerprint.Config `json:"config"`
	} `json:"fingerprint"`
	Origins []json.RawMessage `json:"origins"`
	Cookies []PortableCookie  `json:"cookies"`
}

type ProfileTransferInput struct {
	ProfilePath     string             `json:"profilePath"`
	Fingerprint     fingerprint.Config `json:"fingerprint"`
	FingerprintSeed string             `json:"fingerprintSeed"`
	Origins         []string           `json:"origins,omitempty"`
	Bundle          *ProfileBundle     `json:"bundle,omitempty"`
	CookieRevision  int64              `json:"cookieRevision,omitempty"`
}

type exclusiveProfileManager interface {
	WithExclusiveProfile(session.CreateInput, func(string) error) error
}

func validOrigins(origins []string) error {
	if len(origins) == 0 || len(origins) > 50 {
		return fmt.Errorf("supply 1 to 50 HTTP(S) origins")
	}
	seen := map[string]bool{}
	for _, origin := range origins {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || origin != u.Scheme+"://"+u.Host || seen[origin] {
			return fmt.Errorf("supply unique HTTP(S) origins, for example https://example.com")
		}
		seen[origin] = true
	}
	return nil
}

func cookieSelected(domain string, origins []string) bool {
	for _, origin := range origins {
		u, _ := url.Parse(origin)
		host := strings.ToLower(u.Hostname())
		d := strings.TrimPrefix(domain, ".")
		if host == d || (strings.HasPrefix(domain, ".") && strings.HasSuffix(host, "."+d)) {
			return true
		}
	}
	return false
}

func portableCookie(c *network.Cookie) (PortableCookie, error) {
	if c.PartitionKeyOpaque {
		return PortableCookie{}, fmt.Errorf("opaque partitioned Cookie cannot be exported; remove it from the selected profile before retrying")
	}
	same := map[network.CookieSameSite]string{network.CookieSameSiteLax: "lax", network.CookieSameSiteStrict: "strict", network.CookieSameSiteNone: "no_restriction"}[c.SameSite]
	if same == "" {
		same = "unspecified"
	}
	out := PortableCookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Secure: c.Secure, HTTPOnly: c.HTTPOnly, HostOnly: !strings.HasPrefix(c.Domain, "."), SameSite: same, PartitionKey: c.PartitionKey}
	if !c.Session {
		v := c.Expires
		out.ExpirationDate = &v
	}
	return out, nil
}

func cookieParam(c PortableCookie) *network.CookieParam {
	scheme := "http"
	if c.Secure {
		scheme = "https"
	}
	out := &network.CookieParam{Name: c.Name, Value: c.Value, Path: c.Path, Secure: c.Secure, HTTPOnly: c.HTTPOnly, PartitionKey: c.PartitionKey}
	if c.HostOnly {
		out.URL = scheme + "://" + c.Domain + c.Path
	} else {
		out.Domain = c.Domain
	}
	out.SameSite = map[string]network.CookieSameSite{"lax": network.CookieSameSiteLax, "strict": network.CookieSameSiteStrict, "no_restriction": network.CookieSameSiteNone}[c.SameSite]
	if c.ExpirationDate != nil {
		stamp := cdp.TimeSinceEpoch(time.UnixMilli(int64(*c.ExpirationDate * 1000)))
		out.Expires = &stamp
	}
	return out
}

func evalPortable(ctx context.Context, script string, out any) error {
	return chromedp.Run(ctx, chromedp.Evaluate(script, out, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }))
}

// A synthetic document retains the site's origin without executing site code or
// sending requests to the website during transfer.
func syntheticOrigin(ctx context.Context, origin string) error {
	return chromedp.Run(ctx, page.SetBypassCSP(true), page.SetLifecycleEventsEnabled(true), network.SetBypassServiceWorker(true), chromedp.Navigate(origin+"/"))
}

func (s *Service) TransferProfile(parent context.Context, in ProfileTransferInput, importing bool) (any, error) {
	mgr, ok := s.sessions.(exclusiveProfileManager)
	if !ok {
		return nil, fmt.Errorf("profile transfer is unavailable; deploy browserd with profile transfer support")
	}
	origins := in.Origins
	if importing {
		if in.Bundle == nil || in.Bundle.Format != "syngy-browser-profile" || in.Bundle.Version != 1 {
			return nil, fmt.Errorf("unsupported profile format/version; supply syngy-browser-profile v1")
		}
		origins = nil
		for _, raw := range in.Bundle.Origins {
			var site struct {
				Origin string `json:"origin"`
			}
			if err := json.Unmarshal(raw, &site); err != nil {
				return nil, err
			}
			origins = append(origins, site.Origin)
		}
	}
	if err := validOrigins(origins); err != nil {
		return nil, err
	}
	var result any
	// The same lock orders legacy Cookie updates before transfer and advances its revision.
	s.cookiesMu.Lock()
	defer s.cookiesMu.Unlock()
	err := mgr.WithExclusiveProfile(session.CreateInput{ProfilePath: in.ProfilePath, Fingerprint: in.Fingerprint, TTLSeconds: 300, ProxyServer: "http://127.0.0.1:9"}, func(id string) error {
		defer s.Close(id)
		info, err := s.sessions.Get(id)
		if err != nil {
			return err
		}
		// Only modify our temporary copy, never the persisted source archive.
		// Keep session-cookie restoration, but do not restore old website tabs.
		for _, name := range []string{"Sessions", "Current Session", "Current Tabs", "Last Session", "Last Tabs"} {
			if err := os.RemoveAll(filepath.Join(info.ProfileDir, "Default", name)); err != nil {
				return err
			}
		}
		ctx, cancel, err := s.newBrowserContext(id, 120000)
		if err != nil {
			return err
		}
		defer cancel()
		ctx, cancelParent := context.WithCancel(ctx)
		defer cancelParent()
		stop := context.AfterFunc(parent, cancelParent)
		defer stop()
		chromedp.ListenTarget(ctx, func(ev any) {
			if e, ok := ev.(*fetch.EventRequestPaused); ok {
				go func() {
					_ = chromedp.Run(ctx, chromedp.ActionFunc(func(c context.Context) error {
						return fetch.FulfillRequest(e.RequestID, 200).WithResponseHeaders([]*fetch.HeaderEntry{{Name: "Content-Type", Value: "text/html; charset=utf-8"}, {Name: "Cache-Control", Value: "no-store"}}).WithBody(base64.StdEncoding.EncodeToString([]byte("<!doctype html><title>Profile transfer</title>"))).Do(c)
					}))
				}()
			}
		})
		if err := chromedp.Run(ctx, fetch.Enable()); err != nil {
			return err
		}
		bundle := in.Bundle
		if importing {
			raw, _ := json.Marshal(bundle)
			if err := evalPortable(ctx, "("+portableRuntime+").validateProfile("+string(raw)+") && true", new(bool)); err != nil {
				return err
			}
			params := make([]*network.CookieParam, 0, len(bundle.Cookies))
			for _, c := range bundle.Cookies {
				params = append(params, cookieParam(c))
			}
			if len(params) > 0 {
				if err := chromedp.Run(ctx, chromedp.ActionFunc(func(c context.Context) error { return network.SetCookies(params).Do(c) })); err != nil {
					return err
				}
				// CDP may silently reject a cookie. Verify every imported scope/value.
				var actual []*network.Cookie
				if err := chromedp.Run(ctx, chromedp.ActionFunc(func(c context.Context) error { var e error; actual, e = storage.GetCookies().Do(c); return e })); err != nil {
					return err
				}
				for _, expected := range bundle.Cookies {
					found := false
					for _, stored := range actual {
						got, e := portableCookie(stored)
						if e != nil {
							continue
						}
						a, _ := json.Marshal(got)
						b, _ := json.Marshal(expected)
						if string(a) == string(b) {
							found = true
							break
						}
					}
					if !found {
						return fmt.Errorf("Chrome rejected a Cookie scope or attribute; correct the profile Cookie settings and retry")
					}
				}
			}
		} else {
			bundle = &ProfileBundle{Format: "syngy-browser-profile", Version: 1, ExportedAt: time.Now().UTC().Format(time.RFC3339), Origins: []json.RawMessage{}, Cookies: []PortableCookie{}}
			bundle.Fingerprint.Seed = in.FingerprintSeed
			bundle.Fingerprint.Config = in.Fingerprint
			var cookies []*network.Cookie
			if err := chromedp.Run(ctx, chromedp.ActionFunc(func(c context.Context) error { var e error; cookies, e = storage.GetCookies().Do(c); return e })); err != nil {
				return err
			}
			for _, c := range cookies {
				if cookieSelected(c.Domain, origins) {
					item, err := portableCookie(c)
					if err != nil {
						return err
					}
					bundle.Cookies = append(bundle.Cookies, item)
				}
			}
		}
		for i, origin := range origins {
			if err := syntheticOrigin(ctx, origin); err != nil {
				return err
			}
			if importing {
				var out any
				if err := evalPortable(ctx, "("+portableRuntime+").profilePageState('import',"+string(bundle.Origins[i])+")", &out); err != nil {
					return err
				}
			} else {
				var raw json.RawMessage
				if err := evalPortable(ctx, "("+portableRuntime+").profilePageState('export')", &raw); err != nil {
					return err
				}
				bundle.Origins = append(bundle.Origins, raw)
			}
		}
		if importing {
			if err := s.Checkpoint(id); err != nil {
				return err
			}
			commit, err := s.sessions.Commit(id, session.CommitInput{})
			if err != nil {
				return err
			}
			if in.CookieRevision > 0 {
				s.rememberCookieRevisionLocked(CookieConfig{ProfilePath: in.ProfilePath, Revision: in.CookieRevision})
			}
			result = map[string]any{"newProfileVersion": commit.NewVersion, "origins": len(origins), "cookies": len(bundle.Cookies)}
		} else {
			result = bundle
		}
		return nil
	})
	return result, err
}
