package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// CookieInput imports host-only cookies at path /. Values are never decoded.
type CookieInput struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	URL      string `json:"url"`
	HTTPOnly bool   `json:"httpOnly,omitempty"`
}

func cookieParams(cookies []CookieInput) ([]*network.CookieParam, error) {
	if len(cookies) == 0 || len(cookies) > 1000 {
		return nil, fmt.Errorf("%w: supply 1 to 1000 cookies", ErrInvalidRequest)
	}
	params := make([]*network.CookieParam, 0, len(cookies))
	seen := map[string]bool{}
	total := 0
	for _, c := range cookies {
		total += len(c.Name) + len(c.Value) + len(c.URL)
		u, err := url.Parse(c.URL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return nil, fmt.Errorf("%w: supply an HTTP(S) origin for each cookie URL", ErrInvalidRequest)
		}
		if err := (&http.Cookie{Name: c.Name, Value: c.Value}).Valid(); err != nil || total > 131072 || len(c.Name)+len(c.Value) > 4096 {
			return nil, fmt.Errorf("%w: invalid cookie name/value or payload too large", ErrInvalidRequest)
		}
		key := strings.ToLower(u.Hostname()) + "\x00" + c.Name
		if seen[key] {
			return nil, fmt.Errorf("%w: duplicate cookie name for origin", ErrInvalidRequest)
		}
		seen[key] = true
		if (strings.HasPrefix(c.Name, "__Host-") || strings.HasPrefix(c.Name, "__Secure-")) && u.Scheme != "https" {
			return nil, fmt.Errorf("%w: prefixed cookie requires HTTPS", ErrInvalidRequest)
		}
		params = append(params, &network.CookieParam{Name: c.Name, Value: c.Value, URL: c.URL, Path: "/", Secure: u.Scheme == "https", HTTPOnly: c.HTTPOnly})
	}
	return params, nil
}

func (s *Service) ImportCookies(runtimeSessionID string, cookies []CookieInput) (int, error) {
	params, err := cookieParams(cookies)
	if err != nil {
		return 0, err
	}
	s.cookiesMu.Lock()
	defer s.cookiesMu.Unlock()
	ctx, cancel, err := s.newBrowserContext(runtimeSessionID, 15000)
	if err != nil {
		return 0, err
	}
	defer cancel()
	return importCookiesIntoContext(ctx, params)
}

func importCookiesIntoContext(ctx context.Context, params []*network.CookieParam) (int, error) {
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		if err := network.SetCookies(params).Do(ctx); err != nil {
			return fmt.Errorf("cookie import failed; retry with valid cookie values: %w", err)
		}
		// Chrome can reject a cookie without failing the whole CDP request. Verify each value.
		for _, p := range params {
			stored, err := network.GetCookies().WithURLs([]string{p.URL}).Do(ctx)
			if err != nil {
				return err
			}
			u, _ := url.Parse(p.URL)
			found := false
			for _, c := range stored {
				if c.Name == p.Name && c.Domain == strings.ToLower(u.Hostname()) && c.Path == "/" && c.Value == p.Value && c.HTTPOnly == p.HTTPOnly {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("cookie import rejected by Chrome; check cookie format and target website, then retry")
			}
		}
		return nil
	}))
	if err != nil {
		return 0, err
	}
	return len(params), nil
}

// CookieConfig uses a monotonically increasing Host revision to reject stale
// configs fetched by agents before a simultaneous user save.
type CookieConfig struct {
	ProfilePath string        `json:"profilePath,omitempty"`
	Revision    int64         `json:"revision,omitempty"`
	Cookies     []CookieInput `json:"cookies"`
}

func (s *Service) validateCookieConfigLocked(config CookieConfig) ([]*network.CookieParam, error) {
	if !strings.HasPrefix(config.ProfilePath, "/") || strings.Contains(config.ProfilePath, "..") || config.Revision <= 0 {
		return nil, fmt.Errorf("%w: supply a logical profilePath and positive revision", ErrInvalidRequest)
	}
	if s.cookieRevisions[config.ProfilePath] > config.Revision {
		return nil, fmt.Errorf("%w: cookie configuration is stale; refresh the browser configuration and retry create_session", ErrInvalidRequest)
	}
	if len(config.Cookies) == 0 {
		return nil, nil
	}
	return cookieParams(config.Cookies)
}

func (s *Service) rememberCookieRevisionLocked(config CookieConfig) {
	if s.cookieRevisions == nil {
		s.cookieRevisions = map[string]int64{}
	}
	s.cookieRevisions[config.ProfilePath] = config.Revision
}

func (s *Service) ImportCookieConfig(id string, config CookieConfig) (int, error) {
	s.cookiesMu.Lock()
	defer s.cookiesMu.Unlock()
	params, err := s.validateCookieConfigLocked(config)
	if err != nil {
		return 0, err
	}
	info, err := s.sessions.Get(id)
	if err != nil {
		return 0, err
	}
	if info.ProfilePath != config.ProfilePath {
		return 0, fmt.Errorf("%w: cookie configuration does not match session profile", ErrInvalidRequest)
	}
	if len(params) > 0 {
		ctx, cancel, err := s.newBrowserContext(id, 15000)
		if err != nil {
			return 0, err
		}
		defer cancel()
		if _, err := importCookiesIntoContext(ctx, params); err != nil {
			return 0, err
		}
	}
	s.rememberCookieRevisionLocked(config)
	return len(params), nil
}

// Apply to every active browser using the profile, including agent-created
// sessions that are intentionally not recorded in Host's remote-session table.
func (s *Service) ImportProfileCookies(config CookieConfig) (int, error) {
	s.cookiesMu.Lock()
	defer s.cookiesMu.Unlock()
	params, err := s.validateCookieConfigLocked(config)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	ids := make([]string, 0, len(s.browsers))
	for id := range s.browsers {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	count := 0
	for _, id := range ids {
		info, err := s.sessions.Get(id)
		if err != nil {
			return 0, err
		}
		if info.ProfilePath != config.ProfilePath {
			continue
		}
		if len(params) > 0 {
			ctx, cancel, err := s.newBrowserContext(id, 15000)
			if err != nil {
				return 0, err
			}
			_, err = importCookiesIntoContext(ctx, params)
			cancel()
			if err != nil {
				return 0, err
			}
		}
		count++
	}
	s.rememberCookieRevisionLocked(config)
	return count, nil
}
