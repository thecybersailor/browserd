package session

import (
	"browserd/internal/fingerprint"
	"browserd/internal/profile"
	"errors"
	"testing"
	"time"
)

func TestProfileTransferExclusionAndIndependentProfiles(t *testing.T) {
	m := NewManager(ManagerOptions{Store: profile.NewMemoryStore(), Workdir: t.TempDir()}).(*manager)
	in := CreateInput{ProfilePath: "/one/profile.tgz", Fingerprint: fingerprint.FromSeed("test")}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- m.WithExclusiveProfile(in, func(string) error { close(entered); <-release; return errors.New("abort") })
	}()
	<-entered
	created := make(chan CreateOutput, 1)
	go func() {
		out, err := m.Create(in)
		if err != nil {
			t.Error(err)
		}
		created <- out
	}()
	select {
	case <-created:
		t.Fatal("same-profile create raced transfer")
	case <-time.After(20 * time.Millisecond):
	}
	other := in
	other.ProfilePath = "/two/profile.tgz"
	independent, err := m.Create(other)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Delete(independent.RuntimeSessionID)
	close(release)
	if err := <-done; err == nil {
		t.Fatal("transfer callback failure lost")
	}
	active := <-created
	defer m.Delete(active.RuntimeSessionID)
	if err := m.WithExclusiveProfile(in, func(string) error { t.Fatal("active profile entered transfer"); return nil }); err == nil {
		t.Fatal("active profile accepted")
	}
}
