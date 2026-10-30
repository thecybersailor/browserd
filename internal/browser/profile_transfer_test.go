package browser

import (
	"context"
	_ "embed"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"browserd/internal/fingerprint"
	"browserd/internal/profile"
	"browserd/internal/session"
)

//go:embed portable/profile-v1.json
var portableFixture []byte

func TestProfileTransferNativeRoundTrip(t *testing.T) {
	if _, err := os.Stat("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"); err != nil {
		t.Skip("local Chrome is unavailable")
	}
	t.Setenv("CHROME_BIN", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome")
	fp := fingerprint.FromSeed("profile-transfer-test")
	manager := session.NewManager(session.ManagerOptions{Store: profile.NewMemoryStore(), Workdir: t.TempDir()})
	svc := NewServiceWithOptions(ServiceOptions{Sessions: manager})
	var bundle ProfileBundle
	fixture:=portableFixture
 if path:=os.Getenv("PROFILE_TEST_INPUT");path!="" { var err error;fixture,err=os.ReadFile(path);if err!=nil {t.Fatal(err)} }
 if err := json.Unmarshal(fixture, &bundle); err != nil {
		t.Fatal(err)
	}
	fp = bundle.Fingerprint.Config
	in := ProfileTransferInput{ProfilePath: "/test/profile.tgz", Fingerprint: fp, FingerprintSeed: fp.Seed, Bundle: &bundle, CookieRevision: 2}
	if _, err := svc.TransferProfile(context.Background(), in, true); err != nil {
		t.Fatal(err)
	}
	in.Bundle = nil
	in.Origins = []string{"https://example.com"}
	out, err := svc.TransferProfile(context.Background(), in, false)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(*ProfileBundle)
	if path := os.Getenv("PROFILE_TEST_EXPORT_PATH"); path != "" {
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if len(got.Cookies) != 3 {
		t.Fatalf("cookies: %+v", got.Cookies)
	}
	var site struct {
		LocalStorage map[string]string `json:"localStorage"`
	}
	if err := json.Unmarshal(got.Origins[0], &site); err != nil {
		t.Fatal(err)
	}
	if site.LocalStorage["login"] != "value==" {
		t.Fatalf("storage: %s", got.Origins[0])
	}
	if !strings.Contains(string(got.Origins[0]), "account-db") || !strings.Contains(string(got.Origins[0]), "buffer") {
		t.Fatalf("IDB lost: %s", got.Origins[0])
	}
	// A failed import must leave the stored archive untouched.
	before, _ := json.Marshal(got.Origins)
	invalid := bundle
	invalid.Origins = []json.RawMessage{json.RawMessage(`{"origin":"https://example.com","localStorage":{"login":"bad"},"indexedDB":[{"name":"bad","version":1,"stores":[{"name":"s","keyPath":null,"autoIncrement":false,"indexes":[],"records":{"root":{"ref":99},"nodes":[]}}]}]}`)}
	in.Bundle = &invalid
	if _, err := svc.TransferProfile(context.Background(), in, true); err == nil {
		t.Fatal("invalid graph accepted")
	}
	in.Bundle = nil
	check, err := svc.TransferProfile(context.Background(), in, false)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(check.(*ProfileBundle).Origins)
	if string(before) != string(after) {
		t.Fatal("failed import modified stored archive")
	}
	live, err := manager.Create(session.CreateInput{ProfilePath: in.ProfilePath, Fingerprint: fp})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Delete(live.RuntimeSessionID)
	if _, err := svc.TransferProfile(context.Background(), in, false); err == nil {
		t.Fatal("active profile accepted")
	}
}
