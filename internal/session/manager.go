package session

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"browserd/internal/fingerprint"
	"browserd/internal/profile"
)

var (
	ErrInvalidRequest  = errors.New("invalid request")
	ErrSessionNotFound = errors.New("session not found")
)

type CreateInput struct {
	ProfileTransfer bool
	ProfilePath     string
	LeaseID         string
	TTLSeconds      int
	Fingerprint     fingerprint.Config
	ProxyServer     string
}

type CreateOutput struct {
	RuntimeSessionID string `json:"runtimeSessionId"`
	CDPWsURL         string `json:"cdpWsUrl"`
	LeaseID          string `json:"leaseId"`
	ResolvedVersion  string `json:"resolvedVersion,omitempty"`
}

type CommitInput struct{}

type CommitOutput struct {
	NewVersion string `json:"newVersion"`
	Bytes      int64  `json:"bytes"`
	DurationMs int64  `json:"durationMs"`
}

type SessionInfo struct {
	ProfileTransfer  bool
	RuntimeSessionID string
	ProfilePath      string
	ProfileDir       string
	Version          string
	LeaseID          string
	TTLSeconds       int
	LastActiveAt     time.Time
	ExpiresAt        time.Time
	Fingerprint      fingerprint.Config
	ProxyServer      string
}

type runtimeSession struct {
	ProfileTransfer  bool
	RuntimeSessionID string
	ProfilePath      string
	ProfileDir       string
	Version          string
	LeaseID          string
	TTLSeconds       int
	LastActiveAt     time.Time
	ExpiresAt        time.Time
	Fingerprint      fingerprint.Config
	ProxyServer      string
	Closing          bool
}

type Manager interface {
	Create(input CreateInput) (CreateOutput, error)
	Commit(runtimeSessionID string, input CommitInput) (CommitOutput, error)
	Delete(runtimeSessionID string) error
	Get(runtimeSessionID string) (SessionInfo, error)
	Touch(runtimeSessionID string) error
	ClaimExpired(now time.Time, excluded ...string) []SessionInfo
}

type ManagerOptions struct {
	Store      profile.Store
	Workdir    string
	CDPBaseURL string
	Now        func() time.Time
}

type manager struct {
	profileLocks sync.Map
	mu           sync.Mutex
	store        profile.Store
	workdir      string
	cdpBaseURL   string
	sessions     map[string]runtimeSession
	now          func() time.Time
}

func NewManager(opts ManagerOptions) Manager {
	workdir := strings.TrimSpace(opts.Workdir)
	if workdir == "" {
		workdir = filepath.Join(os.TempDir(), "browserd")
	}
	cdpBase := strings.TrimRight(strings.TrimSpace(opts.CDPBaseURL), "/")
	if cdpBase == "" {
		cdpBase = "ws://browserd:9222/devtools/browser"
	}
	st := opts.Store
	if st == nil {
		st = profile.NewMemoryStore()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &manager{
		store:      st,
		workdir:    workdir,
		cdpBaseURL: cdpBase,
		sessions:   map[string]runtimeSession{},
		now:        now,
	}
}

func (m *manager) Create(input CreateInput) (CreateOutput, error) {
	canonical, err := normalizeProfilePath(input.ProfilePath)
	if err != nil {
		return CreateOutput{}, err
	}
	unlock := m.lockProfile(canonical)
	defer unlock()
	return m.create(input)
}

func (m *manager) create(input CreateInput) (CreateOutput, error) {
	profilePath, err := normalizeProfilePath(input.ProfilePath)
	if err != nil {
		return CreateOutput{}, ErrInvalidRequest
	}
	fp := input.Fingerprint.Normalized()
	if err := fp.Validate(); err != nil {
		return CreateOutput{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}

	ttl := normalizeTTLSeconds(input.TTLSeconds)
	leaseID := strings.TrimSpace(input.LeaseID)
	if leaseID == "" {
		leaseID = fmt.Sprintf("lease_%d", m.now().UnixNano())
	}
	rid := fmt.Sprintf("rt_%d_%d", m.now().UnixNano(), rand.Intn(1000))
	sessionRoot := filepath.Join(m.workdir, "sessions", rid)
	profileDir := filepath.Join(sessionRoot, "profile")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return CreateOutput{}, err
	}

	data, version, found, err := m.store.Get(context.Background(), profilePath)
	if err != nil {
		return CreateOutput{}, err
	}
	resolvedVersion := "new"
	if found {
		tmpTGZ := filepath.Join(sessionRoot, "profile.tgz")
		if err := os.WriteFile(tmpTGZ, data, 0o644); err != nil {
			return CreateOutput{}, err
		}
		if err := profile.UnpackTGZToDir(tmpTGZ, profileDir); err != nil {
			return CreateOutput{}, err
		}
		if strings.TrimSpace(version) != "" {
			resolvedVersion = strings.TrimSpace(version)
		}
	}

	out := CreateOutput{
		RuntimeSessionID: rid,
		CDPWsURL:         m.cdpBaseURL + "/" + rid,
		LeaseID:          leaseID,
		ResolvedVersion:  resolvedVersion,
	}

	now := m.now().UTC()
	m.mu.Lock()
	m.sessions[rid] = runtimeSession{
		ProfileTransfer:  input.ProfileTransfer,
		RuntimeSessionID: rid,
		ProfilePath:      profilePath,
		ProfileDir:       profileDir,
		Version:          resolvedVersion,
		LeaseID:          leaseID,
		TTLSeconds:       ttl,
		LastActiveAt:     now,
		ExpiresAt:        now.Add(time.Duration(ttl) * time.Second),
		Fingerprint:      fp,
		ProxyServer:      strings.TrimSpace(input.ProxyServer),
	}
	m.mu.Unlock()
	return out, nil
}

func (m *manager) Commit(runtimeSessionID string, input CommitInput) (CommitOutput, error) {
	_ = input
	if strings.TrimSpace(runtimeSessionID) == "" {
		return CommitOutput{}, ErrInvalidRequest
	}

	m.mu.Lock()
	s, ok := m.sessions[runtimeSessionID]
	m.mu.Unlock()
	if !ok || s.Closing {
		return CommitOutput{}, ErrSessionNotFound
	}

	start := time.Now()
	tmpTGZ := filepath.Join(filepath.Dir(s.ProfileDir), "upload.tgz")
	if err := profile.PackDirToTGZ(s.ProfileDir, tmpTGZ); err != nil {
		return CommitOutput{}, err
	}
	buf, err := os.ReadFile(tmpTGZ)
	if err != nil {
		return CommitOutput{}, err
	}

	newVersion, err := m.store.Put(context.Background(), s.ProfilePath, buf)
	if err != nil {
		return CommitOutput{}, err
	}

	m.mu.Lock()
	if current, ok := m.sessions[runtimeSessionID]; ok && !current.Closing {
		current.Version = newVersion
		m.sessions[runtimeSessionID] = current
	}
	m.mu.Unlock()

	return CommitOutput{
		NewVersion: newVersion,
		Bytes:      int64(len(buf)),
		DurationMs: time.Since(start).Milliseconds(),
	}, nil
}

func normalizeProfilePath(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", ErrInvalidRequest
	}
	if strings.Contains(trimmed, "://") {
		return "", ErrInvalidRequest
	}
	if !strings.HasPrefix(trimmed, "/") {
		return "", ErrInvalidRequest
	}
	key := strings.Trim(trimmed, "/")
	if key == "" {
		return "", ErrInvalidRequest
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", ErrInvalidRequest
		}
	}
	cleanKey := path.Clean(key)
	if cleanKey == "." || cleanKey == ".." || strings.HasPrefix(cleanKey, "../") || path.IsAbs(cleanKey) {
		return "", ErrInvalidRequest
	}
	if !strings.HasSuffix(cleanKey, "profile.tgz") {
		return "", ErrInvalidRequest
	}
	return "/" + cleanKey, nil
}

func (m *manager) Delete(runtimeSessionID string) error {
	if strings.TrimSpace(runtimeSessionID) == "" {
		return ErrInvalidRequest
	}

	m.mu.Lock()
	s, ok := m.sessions[runtimeSessionID]
	if !ok {
		m.mu.Unlock()
		return ErrSessionNotFound
	}
	delete(m.sessions, runtimeSessionID)
	m.mu.Unlock()

	_ = os.RemoveAll(filepath.Dir(s.ProfileDir))
	return nil
}

func (m *manager) Get(runtimeSessionID string) (SessionInfo, error) {
	if strings.TrimSpace(runtimeSessionID) == "" {
		return SessionInfo{}, ErrInvalidRequest
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[runtimeSessionID]
	if !ok || s.Closing {
		return SessionInfo{}, ErrSessionNotFound
	}
	return sessionInfoFromRuntime(s), nil
}

func (m *manager) Touch(runtimeSessionID string) error {
	if strings.TrimSpace(runtimeSessionID) == "" {
		return ErrInvalidRequest
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[runtimeSessionID]
	if !ok || s.Closing {
		return ErrSessionNotFound
	}
	now := m.now().UTC()
	s.LastActiveAt = now
	s.ExpiresAt = now.Add(time.Duration(s.TTLSeconds) * time.Second)
	m.sessions[runtimeSessionID] = s
	return nil
}

func (m *manager) ClaimExpired(now time.Time, excluded ...string) []SessionInfo {
	now = now.UTC()
	m.mu.Lock()
	defer m.mu.Unlock()

	expired := []SessionInfo{}
	for id, s := range m.sessions {
		if slices.Contains(excluded, id) || s.Closing || now.Before(s.ExpiresAt) {
			continue
		}
		s.Closing = true
		m.sessions[id] = s
		expired = append(expired, sessionInfoFromRuntime(s))
	}
	return expired
}

func normalizeTTLSeconds(ttl int) int {
	if ttl <= 0 {
		return 900
	}
	return ttl
}

func sessionInfoFromRuntime(s runtimeSession) SessionInfo {
	return SessionInfo{
		ProfileTransfer:  s.ProfileTransfer,
		RuntimeSessionID: s.RuntimeSessionID,
		ProfilePath:      s.ProfilePath,
		ProfileDir:       s.ProfileDir,
		Version:          s.Version,
		LeaseID:          s.LeaseID,
		TTLSeconds:       s.TTLSeconds,
		LastActiveAt:     s.LastActiveAt,
		ExpiresAt:        s.ExpiresAt,
		Fingerprint:      s.Fingerprint,
		ProxyServer:      s.ProxyServer,
	}
}

// WithExclusiveProfile prevents new sessions while a temporary profile copy is
// inspected or imported. Existing sessions (including agent sessions) reject it.
func (m *manager) WithExclusiveProfile(input CreateInput, fn func(string) error) error {
	path, err := normalizeProfilePath(input.ProfilePath)
	if err != nil {
		return err
	}
	unlock := m.lockProfile(path)
	defer unlock()
	m.mu.Lock()
	for _, s := range m.sessions {
		if s.ProfilePath == path {
			m.mu.Unlock()
			return fmt.Errorf("profile is in use; close all browser sessions and retry profile transfer")
		}
	}
	m.mu.Unlock()
	input.ProfileTransfer = true
	out, err := m.create(input)
	if err != nil {
		return err
	}
	defer m.Delete(out.RuntimeSessionID)
	return fn(out.RuntimeSessionID)
}

func (m *manager) lockProfile(path string) func() {
	value, _ := m.profileLocks.LoadOrStore(strings.TrimSpace(path), new(sync.Mutex))
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}
