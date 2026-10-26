package controller

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"browserd/internal/browser"
	"browserd/internal/fingerprint"
	"browserd/internal/live"
	"browserd/internal/liveviewer"
	"browserd/internal/runtime"
	"browserd/internal/session"
	"browserd/internal/types"

	browserdclient "github.com/flaboy/browserd-client-go/pkg/browserd"
)

type SessionController struct {
	operations    sessionOperationGuard
	manager       session.Manager
	browser       browserRuntime
	cdpBaseURL    string
	liveBaseURL   string
	noVNCBasePath string
	liveTokenTTL  time.Duration
	handoffGrace  time.Duration
	tokenStore    *live.TokenStore
	handoffsMu    sync.Mutex
	handoffs      map[string]handoffState
}

type browserRuntime interface {
	ImportCookies(runtimeSessionID string, cookies []browser.CookieInput) (int, error)
	ImportCookieConfig(runtimeSessionID string, config browser.CookieConfig) (int, error)
	ImportProfileCookies(config browser.CookieConfig) (int, error)
	PrepareSession(runtimeSessionID string) error
	Checkpoint(runtimeSessionID string) error
	Close(runtimeSessionID string) error
	Navigate(ctx context.Context, runtimeSessionID string, input browser.NavigateInput) (browser.NavigateOutput, error)
	Snapshot(runtimeSessionID string, input browser.SnapshotInput) (browser.SnapshotOutput, error)
	Act(runtimeSessionID string, input browser.ActInput) (browser.ActOutput, error)
	WaitFor(runtimeSessionID string, input browser.WaitForInput) (browser.WaitForOutput, error)
	Screenshot(runtimeSessionID string, input browser.ScreenshotInput) (browser.ScreenshotOutput, error)
	UploadFiles(runtimeSessionID string, input browser.UploadFilesInput) (browser.UploadFilesOutput, error)
	Evaluate(runtimeSessionID string, input browser.EvaluateInput) (browser.EvaluateOutput, error)
	PageTool(runtimeSessionID string, input browserdclient.PageToolInput) (browserdclient.PageToolResult, error)
}

type browserPointerRuntime interface {
	SubscribePointer(runtimeSessionID string) (browser.PointerSubscription, error)
}

type browserLiveProxyRuntime interface {
	LiveProxyTarget(runtimeSessionID string) (string, error)
}

type SessionControllerOptions struct {
	Manager       session.Manager
	Browser       browserRuntime
	CDPBaseURL    string
	LiveBaseURL   string
	NoVNCBasePath string
	LiveTokenTTL  time.Duration
	// HandoffDisconnectGrace controls how long a control noVNC disconnect may last
	// before the handoff is automatically completed.
	HandoffDisconnectGrace time.Duration
	TokenStore             *live.TokenStore
}

func NewSessionController(manager session.Manager, browserSvc browserRuntime, cdpBaseURL string) *SessionController {
	return NewSessionControllerWithLive(SessionControllerOptions{
		Manager:    manager,
		Browser:    browserSvc,
		CDPBaseURL: cdpBaseURL,
	})
}

func NewSessionControllerWithLive(opts SessionControllerOptions) *SessionController {
	ttl := opts.LiveTokenTTL
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	handoffGrace := opts.HandoffDisconnectGrace
	if handoffGrace <= 0 {
		handoffGrace = 60 * time.Second
	}
	noVNCBasePath := strings.TrimSpace(opts.NoVNCBasePath)
	if noVNCBasePath == "" {
		noVNCBasePath = "/v"
	}
	if !strings.HasPrefix(noVNCBasePath, "/") {
		noVNCBasePath = "/" + noVNCBasePath
	}
	noVNCBasePath = strings.TrimRight(noVNCBasePath, "/")
	if noVNCBasePath == "" {
		noVNCBasePath = "/v"
	}
	tokenStore := opts.TokenStore
	if tokenStore == nil {
		tokenStore = live.NewTokenStore(live.TokenStoreOptions{})
	}
	return &SessionController{
		manager:       opts.Manager,
		browser:       opts.Browser,
		cdpBaseURL:    opts.CDPBaseURL,
		liveBaseURL:   strings.TrimRight(strings.TrimSpace(opts.LiveBaseURL), "/"),
		noVNCBasePath: noVNCBasePath,
		liveTokenTTL:  ttl,
		handoffGrace:  handoffGrace,
		tokenStore:    tokenStore,
		handoffs:      map[string]handoffState{},
	}
}

type createSessionRequest struct {
	ProfilePath string             `json:"profilePath,omitempty"`
	TTLSeconds  int                `json:"ttlSec,omitempty"`
	LeaseID     string             `json:"leaseId,omitempty"`
	Fingerprint fingerprint.Config `json:"fingerprint"`
	ProxyServer string             `json:"proxyServer,omitempty"`
}

type commitSessionRequest struct {
}

type navigateRequest struct {
	IncludeSnapshot           bool   `json:"includeSnapshot"`
	URL                       string `json:"url"`
	WaitUntil                 string `json:"waitUntil,omitempty"`
	TimeoutMs                 int    `json:"timeoutMs,omitempty"`
	AfterLoadScreenshotS3Path string `json:"afterLoadScreenshotS3Path,omitempty"`
}

type actRequest struct {
	Action        string          `json:"action"`
	Ref           string          `json:"ref,omitempty"`
	Target        json.RawMessage `json:"target,omitempty"`
	X             float64         `json:"x,omitempty"`
	Y             float64         `json:"y,omitempty"`
	DeltaX        float64         `json:"deltaX,omitempty"`
	DeltaY        float64         `json:"deltaY,omitempty"`
	Text          string          `json:"text,omitempty"`
	HTML          string          `json:"html,omitempty"`
	Key           string          `json:"key,omitempty"`
	Value         string          `json:"value,omitempty"`
	Values        []string        `json:"values,omitempty"`
	Clear         bool            `json:"clear,omitempty"`
	Submit        bool            `json:"submit,omitempty"`
	Button        string          `json:"button,omitempty"`
	ClickCount    int             `json:"clickCount,omitempty"`
	MotionProfile string          `json:"motionProfile,omitempty"`
	TimeoutMs     int             `json:"timeoutMs,omitempty"`
}

type waitForRequest struct {
	Condition  browser.WaitForCondition `json:"condition"`
	TimeoutMs  int                      `json:"timeoutMs,omitempty"`
	IntervalMs int                      `json:"intervalMs,omitempty"`
	StableMs   int                      `json:"stableMs,omitempty"`
}

type screenshotRequest struct {
	Mode               string  `json:"mode,omitempty"`
	Selector           string  `json:"selector,omitempty"`
	Ref                *string `json:"ref,omitempty"`
	FullPage           *bool   `json:"fullPage,omitempty"`
	Format             string  `json:"format,omitempty"`
	Quality            int     `json:"quality,omitempty"`
	ScreenshotS3Prefix string  `json:"screenshotS3Prefix,omitempty"`
}

type uploadFileSourceRequest struct {
	S3Path    string `json:"s3Path,omitempty"`
	LocalPath string `json:"localPath,omitempty"`
	URL       string `json:"url,omitempty"`
	Filename  string `json:"filename,omitempty"`
}

type uploadFilesRequest struct {
	Ref       string                    `json:"ref"`
	X         float64                   `json:"x,omitempty"`
	Y         float64                   `json:"y,omitempty"`
	Files     []uploadFileSourceRequest `json:"files"`
	TimeoutMs int                       `json:"timeoutMs,omitempty"`
}

type evaluateRequest struct {
	Script             string                               `json:"script"`
	Args               []any                                `json:"args,omitempty"`
	TimeoutMs          int                                  `json:"timeoutMs,omitempty"`
	World              string                               `json:"world,omitempty"`
	AllowDuringHandoff bool                                 `json:"allowDuringHandoff,omitempty"`
	PageToolBridge     *browserdclient.PageToolBridgeConfig `json:"pageToolBridge,omitempty"`
}

type liveViewRequest struct {
	Permission string `json:"permission,omitempty"`
	TTLSeconds int    `json:"ttlSeconds,omitempty"`
}

type liveViewOutput struct {
	HandoffID  string          `json:"handoffId"`
	ViewerURL  string          `json:"viewerUrl"`
	ExpiresAt  time.Time       `json:"expiresAt"`
	Permission live.Permission `json:"permission"`
}

type handoffState struct {
	HandoffID         string
	Token             string
	ActiveConnections int
	DisconnectTimer   *time.Timer
}

func (h *SessionController) CreateSession(w http.ResponseWriter, r *http.Request) {
	var req createSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid json body")
		return
	}
	fp := req.Fingerprint.Normalized()
	if err := fp.Validate(); err != nil {
		types.WriteErr(w, http.StatusBadRequest, "INVALID_FINGERPRINT_CONFIG", err.Error())
		return
	}
	if _, err := browser.ParseProxyServer(req.ProxyServer); err != nil {
		types.WriteErr(w, http.StatusBadRequest, "INVALID_PROXY_SERVER", err.Error())
		return
	}
	out, err := h.manager.Create(session.CreateInput{
		ProfilePath: req.ProfilePath,
		TTLSeconds:  req.TTLSeconds,
		LeaseID:     req.LeaseID,
		Fingerprint: fp,
		ProxyServer: req.ProxyServer,
	})
	if err != nil {
		types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if h.browser != nil {
		if err := h.browser.PrepareSession(out.RuntimeSessionID); err != nil {
			_ = h.browser.Close(out.RuntimeSessionID)
			_ = h.manager.Delete(out.RuntimeSessionID)
			switch {
			case errors.Is(err, browser.ErrProxyHopConfigMissing):
				types.WriteErr(w, http.StatusServiceUnavailable, "PROXY_HOP_CONFIG_MISSING", err.Error())
				return
			case errors.Is(err, browser.ErrProxyHopConnectFailed):
				types.WriteErr(w, http.StatusServiceUnavailable, "PROXY_HOP_CONNECT_FAILED", err.Error())
				return
			case errors.Is(err, browser.ErrUpstreamProxyAuthFailed):
				types.WriteErr(w, http.StatusServiceUnavailable, "UPSTREAM_PROXY_AUTH_FAILED", err.Error())
				return
			case errors.Is(err, browser.ErrUpstreamProxyConnectFail):
				types.WriteErr(w, http.StatusServiceUnavailable, "UPSTREAM_PROXY_CONNECT_FAILED", err.Error())
				return
			case errors.Is(err, browser.ErrFingerprintInitFailed):
				types.WriteErr(w, http.StatusServiceUnavailable, "FINGERPRINT_INIT_FAILED", err.Error())
				return
			}
			types.WriteErr(w, http.StatusServiceUnavailable, "SESSION_INIT_FAILED", err.Error())
			return
		}
	}
	types.WriteOK(w, http.StatusOK, out)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (h *SessionController) CommitSession(w http.ResponseWriter, r *http.Request, runtimeSessionID string) {
	release, acquired := h.beginOperation(w, runtimeSessionID)
	if !acquired {
		return
	}
	defer release()
	var req commitSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid json body")
		return
	}
	if !h.touchSession(w, runtimeSessionID) {
		return
	}
	if h.browser != nil {
		if err := h.browser.Checkpoint(runtimeSessionID); err != nil {
			types.WriteErr(w, http.StatusServiceUnavailable, "PROFILE_CHECKPOINT_FAILED", err.Error())
			return
		}
	}
	h.revokeRuntimeSession(runtimeSessionID)
	out, err := h.manager.Commit(runtimeSessionID, session.CommitInput{})
	if err != nil {
		switch err {
		case session.ErrSessionNotFound:
			types.WriteErr(w, http.StatusNotFound, "SESSION_NOT_FOUND", err.Error())
			return
		default:
			types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
			return
		}
	}
	if err := h.manager.Delete(runtimeSessionID); err != nil && !errors.Is(err, session.ErrSessionNotFound) {
		slog.Warn("delete committed browserd session failed", "runtime_session_id", runtimeSessionID, "error", err)
	}
	types.WriteOK(w, http.StatusOK, out)
}

func (h *SessionController) DeleteSession(w http.ResponseWriter, _ *http.Request, runtimeSessionID string) {
	release, acquired := h.beginOperation(w, runtimeSessionID)
	if !acquired {
		return
	}
	defer release()
	if h.browser != nil {
		_ = h.browser.Close(runtimeSessionID)
	}
	h.revokeRuntimeSession(runtimeSessionID)
	err := h.manager.Delete(runtimeSessionID)
	if err != nil {
		if err == session.ErrSessionNotFound {
			types.WriteErr(w, http.StatusNotFound, "SESSION_NOT_FOUND", err.Error())
			return
		}
		types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	types.WriteOK(w, http.StatusOK, map[string]any{"deleted": true})
}

func (h *SessionController) Navigate(w http.ResponseWriter, r *http.Request, runtimeSessionID string) {
	release, acquired := h.beginOperation(w, runtimeSessionID)
	if !acquired {
		return
	}
	defer release()
	if h.browser == nil {
		types.WriteErr(w, http.StatusNotImplemented, "PLAYWRIGHT_NOT_AVAILABLE", "browser runtime not configured")
		return
	}
	var req navigateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid json body")
		return
	}
	if req.IncludeSnapshot && req.WaitUntil != "" && req.WaitUntil != "load" {
		types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "navigation with snapshot supports waitUntil=load only")
		return
	}
	if !h.touchSession(w, runtimeSessionID) {
		return
	}
	if h.hasActiveHandoff(runtimeSessionID) {
		types.WriteErr(w, http.StatusConflict, "HANDOFF_ACTIVE", "browser session is under human handoff")
		return
	}
	out, err := h.browser.Navigate(r.Context(), runtimeSessionID, browser.NavigateInput{
		IncludeSnapshot:           req.IncludeSnapshot,
		URL:                       req.URL,
		WaitUntil:                 req.WaitUntil,
		TimeoutMs:                 req.TimeoutMs,
		AfterLoadScreenshotS3Path: req.AfterLoadScreenshotS3Path,
	})
	if err != nil {
		writeBrowserErr(w, err)
		return
	}
	types.WriteOK(w, http.StatusOK, out)
}

func (h *SessionController) Snapshot(w http.ResponseWriter, r *http.Request, runtimeSessionID string) {
	release, acquired := h.beginOperation(w, runtimeSessionID)
	if !acquired {
		return
	}
	defer release()
	if h.browser == nil {
		types.WriteErr(w, http.StatusNotImplemented, "PLAYWRIGHT_NOT_AVAILABLE", "browser runtime not configured")
		return
	}
	if !h.touchSession(w, runtimeSessionID) {
		return
	}
	mode := strings.TrimSpace(r.URL.Query().Get("mode"))
	if mode == "" {
		mode = "refs"
	}
	out, err := h.browser.Snapshot(runtimeSessionID, browser.SnapshotInput{Mode: mode})
	if err != nil {
		writeBrowserErr(w, err)
		return
	}
	types.WriteOK(w, http.StatusOK, out)
}

func (h *SessionController) Act(w http.ResponseWriter, r *http.Request, runtimeSessionID string) {
	release, acquired := h.beginOperation(w, runtimeSessionID)
	if !acquired {
		return
	}
	defer release()
	if h.browser == nil {
		types.WriteErr(w, http.StatusNotImplemented, "PLAYWRIGHT_NOT_AVAILABLE", "browser runtime not configured")
		return
	}
	var req actRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid json body")
		return
	}
	if !h.touchSession(w, runtimeSessionID) {
		return
	}
	if h.hasActiveHandoff(runtimeSessionID) {
		types.WriteErr(w, http.StatusConflict, "HANDOFF_ACTIVE", "browser session is under human handoff")
		return
	}
	if len(req.Target) > 0 {
		types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "browser act only supports top-level ref; call snapshot first and pass ref")
		return
	}
	out, err := h.browser.Act(runtimeSessionID, browser.ActInput{
		Action:        req.Action,
		Ref:           req.Ref,
		X:             req.X,
		Y:             req.Y,
		DeltaX:        req.DeltaX,
		DeltaY:        req.DeltaY,
		Text:          req.Text,
		HTML:          req.HTML,
		Key:           req.Key,
		Value:         req.Value,
		Values:        req.Values,
		Clear:         req.Clear,
		Submit:        req.Submit,
		Button:        req.Button,
		ClickCount:    req.ClickCount,
		MotionProfile: req.MotionProfile,
		TimeoutMs:     req.TimeoutMs,
	})
	if err != nil {
		writeBrowserErr(w, err)
		return
	}
	types.WriteOK(w, http.StatusOK, out)
}

func (h *SessionController) WaitFor(w http.ResponseWriter, r *http.Request, runtimeSessionID string) {
	release, acquired := h.beginOperation(w, runtimeSessionID)
	if !acquired {
		return
	}
	defer release()
	if h.browser == nil {
		types.WriteErr(w, http.StatusNotImplemented, "PLAYWRIGHT_NOT_AVAILABLE", "browser runtime not configured")
		return
	}
	var req waitForRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid json body")
		return
	}
	if !h.touchSession(w, runtimeSessionID) {
		return
	}
	if h.hasActiveHandoff(runtimeSessionID) {
		types.WriteErr(w, http.StatusConflict, "HANDOFF_ACTIVE", "browser session is under human handoff")
		return
	}
	out, err := h.browser.WaitFor(runtimeSessionID, browser.WaitForInput{
		Condition:  req.Condition,
		TimeoutMs:  req.TimeoutMs,
		IntervalMs: req.IntervalMs,
		StableMs:   req.StableMs,
	})
	if err != nil {
		writeBrowserErr(w, err)
		return
	}
	types.WriteOK(w, http.StatusOK, out)
}

func (h *SessionController) Screenshot(w http.ResponseWriter, r *http.Request, runtimeSessionID string) {
	release, acquired := h.beginOperation(w, runtimeSessionID)
	if !acquired {
		return
	}
	defer release()
	if h.browser == nil {
		types.WriteErr(w, http.StatusNotImplemented, "PLAYWRIGHT_NOT_AVAILABLE", "browser runtime not configured")
		return
	}
	var req screenshotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid json body")
		return
	}
	if !h.touchSession(w, runtimeSessionID) {
		return
	}
	if req.Ref != nil {
		writeBrowserErr(w, fmt.Errorf("%w: ref is not supported for screenshot; use mode selector with selector", browser.ErrInvalidRequest))
		return
	}
	if req.FullPage != nil {
		writeBrowserErr(w, fmt.Errorf("%w: fullPage is not supported for screenshot; use mode fullpage", browser.ErrInvalidRequest))
		return
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "viewport"
	}
	out, err := h.browser.Screenshot(runtimeSessionID, browser.ScreenshotInput{
		Mode:               mode,
		Selector:           req.Selector,
		Format:             req.Format,
		Quality:            req.Quality,
		ScreenshotS3Prefix: req.ScreenshotS3Prefix,
	})
	if err != nil {
		writeBrowserErr(w, err)
		return
	}
	types.WriteOK(w, http.StatusOK, out)
}

func (h *SessionController) UploadFiles(w http.ResponseWriter, r *http.Request, runtimeSessionID string) {
	release, acquired := h.beginOperation(w, runtimeSessionID)
	if !acquired {
		return
	}
	defer release()
	if h.browser == nil {
		types.WriteErr(w, http.StatusNotImplemented, "PLAYWRIGHT_NOT_AVAILABLE", "browser runtime not configured")
		return
	}
	var req uploadFilesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid json body")
		return
	}
	if !h.touchSession(w, runtimeSessionID) {
		return
	}
	if h.hasActiveHandoff(runtimeSessionID) {
		types.WriteErr(w, http.StatusConflict, "HANDOFF_ACTIVE", "browser session is under human handoff")
		return
	}
	files := make([]browser.UploadFileSource, 0, len(req.Files))
	for _, file := range req.Files {
		files = append(files, browser.UploadFileSource{
			S3Path:    file.S3Path,
			LocalPath: file.LocalPath,
			URL:       file.URL,
			Filename:  file.Filename,
		})
	}
	out, err := h.browser.UploadFiles(runtimeSessionID, browser.UploadFilesInput{
		Ref:       req.Ref,
		X:         req.X,
		Y:         req.Y,
		Files:     files,
		TimeoutMs: req.TimeoutMs,
	})
	if err != nil {
		writeBrowserErr(w, err)
		return
	}
	types.WriteOK(w, http.StatusOK, out)
}

func (h *SessionController) Evaluate(w http.ResponseWriter, r *http.Request, runtimeSessionID string) {
	release, acquired := h.beginOperation(w, runtimeSessionID)
	if !acquired {
		return
	}
	defer release()
	if h.browser == nil {
		types.WriteErr(w, http.StatusNotImplemented, "PLAYWRIGHT_NOT_AVAILABLE", "browser runtime not configured")
		return
	}
	var req evaluateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid json body")
		return
	}
	if !h.touchSession(w, runtimeSessionID) {
		return
	}
	if h.hasActiveHandoff(runtimeSessionID) && !req.AllowDuringHandoff {
		types.WriteErr(w, http.StatusConflict, "HANDOFF_ACTIVE", "browser session is under human handoff")
		return
	}
	out, err := h.browser.Evaluate(runtimeSessionID, browser.EvaluateInput{
		Script:         req.Script,
		Args:           req.Args,
		TimeoutMs:      req.TimeoutMs,
		World:          req.World,
		PageToolBridge: req.PageToolBridge,
	})
	if err != nil {
		writeBrowserErr(w, err)
		return
	}
	types.WriteOK(w, http.StatusOK, out)
}

func (h *SessionController) PageTool(w http.ResponseWriter, r *http.Request, runtimeSessionID string) {
	release, acquired := h.beginOperation(w, runtimeSessionID)
	if !acquired {
		return
	}
	defer release()
	if h.browser == nil {
		types.WriteErr(w, http.StatusNotImplemented, "PLAYWRIGHT_NOT_AVAILABLE", "browser runtime not configured")
		return
	}
	var req browserdclient.PageToolInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid json body")
		return
	}
	if err := browserdclient.ValidatePageToolInput(req); err != nil {
		types.WriteErr(w, http.StatusBadRequest, "PAGETOOL_REQUEST_INVALID", err.Error())
		return
	}
	if !h.touchSession(w, runtimeSessionID) {
		return
	}
	if h.hasActiveHandoff(runtimeSessionID) {
		types.WriteErr(w, http.StatusConflict, "HANDOFF_ACTIVE", "browser session is under human handoff")
		return
	}
	out, err := h.browser.PageTool(runtimeSessionID, req)
	if err != nil {
		writeBrowserErr(w, err)
		return
	}
	types.WriteOK(w, http.StatusOK, out)
}

func (h *SessionController) LiveView(w http.ResponseWriter, r *http.Request, runtimeSessionID string) {
	release, acquired := h.beginOperation(w, runtimeSessionID)
	if !acquired {
		return
	}
	defer release()
	req, ok := h.decodeLiveViewRequest(w, r)
	if !ok {
		return
	}
	out, err := h.issueViewer(runtimeSessionID, "", req, live.PermissionView)
	if err != nil {
		h.writeLiveErr(w, err)
		return
	}
	types.WriteOK(w, http.StatusOK, out)
}

func (h *SessionController) StartHandoff(w http.ResponseWriter, r *http.Request, runtimeSessionID string) {
	release, acquired := h.beginOperation(w, runtimeSessionID)
	if !acquired {
		return
	}
	defer release()
	req, ok := h.decodeLiveViewRequest(w, r)
	if !ok {
		return
	}

	h.handoffsMu.Lock()
	if _, exists := h.handoffs[runtimeSessionID]; exists {
		h.handoffsMu.Unlock()
		types.WriteErr(w, http.StatusConflict, "HANDOFF_ACTIVE", "browser session already has an active handoff")
		return
	}
	h.handoffsMu.Unlock()

	handoffID, err := newOpaqueID("ho")
	if err != nil {
		types.WriteErr(w, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", err.Error())
		return
	}
	out, err := h.issueViewer(runtimeSessionID, handoffID, req, live.PermissionControl)
	if err != nil {
		h.writeLiveErr(w, err)
		return
	}

	token := tokenFromViewerURL(out.ViewerURL, h.liveBaseURL, h.noVNCBasePath)
	h.handoffsMu.Lock()
	h.handoffs[runtimeSessionID] = handoffState{
		HandoffID: out.HandoffID,
		Token:     token,
	}
	h.handoffsMu.Unlock()

	types.WriteOK(w, http.StatusOK, out)
}

func (h *SessionController) CompleteHandoff(w http.ResponseWriter, _ *http.Request, runtimeSessionID string, handoffID string) {
	release, acquired := h.beginOperation(w, runtimeSessionID)
	if !acquired {
		return
	}
	defer release()
	if ok := h.completeHandoff(runtimeSessionID, handoffID); !ok {
		types.WriteErr(w, http.StatusNotFound, "HANDOFF_NOT_FOUND", "handoff not found")
		return
	}
	types.WriteOK(w, http.StatusOK, map[string]any{"completed": true})
}

func (h *SessionController) ServeLiveView(w http.ResponseWriter, r *http.Request, token string) {
	state, ok := h.tokenStore.Lookup(token)
	if !ok {
		types.WriteErr(w, http.StatusGone, "LIVE_TOKEN_EXPIRED", "live view token is expired or revoked")
		return
	}
	if !h.touchSession(w, state.RuntimeSessionID) {
		return
	}
	if !h.isWebsockifyRequest(r, token) {
		html, err := liveviewer.IndexHTML()
		if err != nil {
			types.WriteErr(w, http.StatusInternalServerError, "LIVE_VIEWER_ASSET_MISSING", err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(html)
		return
	}
	if liveRuntime, ok := h.browser.(browserLiveProxyRuntime); ok {
		target, err := liveRuntime.LiveProxyTarget(state.RuntimeSessionID)
		if err != nil {
			writeBrowserErr(w, err)
			return
		}
		if strings.TrimSpace(target) == "" {
			types.WriteErr(w, http.StatusServiceUnavailable, "LIVE_RUNTIME_UNHEALTHY", "live proxy target is empty")
			return
		}
		if h.proxyLiveView(w, r, target, token, state) {
			return
		}
		types.WriteErr(w, http.StatusServiceUnavailable, "LIVE_RUNTIME_UNHEALTHY", "live proxy target is invalid")
		return
	}
	types.WriteErr(w, http.StatusServiceUnavailable, "LIVE_RUNTIME_UNHEALTHY", "live proxy runtime is not configured")
}

func (h *SessionController) ServePointerEvents(w http.ResponseWriter, r *http.Request, token string) {
	state, ok := h.tokenStore.Lookup(token)
	if !ok {
		types.WriteErr(w, http.StatusGone, "LIVE_TOKEN_EXPIRED", "live view token is expired or revoked")
		return
	}
	if !h.touchSession(w, state.RuntimeSessionID) {
		return
	}
	runtime, ok := h.browser.(browserPointerRuntime)
	if !ok {
		types.WriteErr(w, http.StatusServiceUnavailable, "POINTER_STREAM_NOT_AVAILABLE", "browser runtime does not expose pointer events")
		return
	}
	sub, err := runtime.SubscribePointer(state.RuntimeSessionID)
	if err != nil {
		writeBrowserErr(w, err)
		return
	}
	defer sub.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	for {
		select {
		case <-r.Context().Done():
			return
		case snapshot, ok := <-sub.C:
			if !ok {
				return
			}
			payload, err := json.Marshal(snapshot)
			if err != nil {
				return
			}
			_, _ = fmt.Fprintf(w, "event: pointer\ndata: %s\n\n", payload)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

func (h *SessionController) isWebsockifyRequest(r *http.Request, token string) bool {
	path := strings.TrimRight(h.noVNCBasePath, "/") + "/" + token + "/websockify"
	return r.URL.Path == path
}

func (h *SessionController) proxyLiveView(w http.ResponseWriter, r *http.Request, target string, token string, state live.TokenState) bool {
	targetURL, err := url.Parse(target)
	if err != nil {
		return false
	}
	tracked := h.shouldTrackHandoffConnection(r, token, state)
	if tracked {
		h.beginHandoffConnection(state.RuntimeSessionID, state.HandoffID)
		defer h.endHandoffConnection(state.RuntimeSessionID, state.HandoffID)
	}
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Path = h.liveProxyPath(r.URL.Path, token)
		req.URL.RawPath = ""
		req.Host = targetURL.Host
	}
	proxy.ServeHTTP(w, r)
	return true
}

func (h *SessionController) shouldTrackHandoffConnection(r *http.Request, token string, state live.TokenState) bool {
	if state.Permission != live.PermissionControl {
		return false
	}
	return h.isWebsockifyRequest(r, token)
}

func (h *SessionController) beginHandoffConnection(runtimeSessionID string, handoffID string) {
	if err := h.manager.Touch(runtimeSessionID); err != nil {
		slog.Debug("touch browserd session on handoff connection", "runtime_session_id", runtimeSessionID, "error", err)
	}
	h.handoffsMu.Lock()
	defer h.handoffsMu.Unlock()

	state, ok := h.handoffs[runtimeSessionID]
	if !ok || state.HandoffID != handoffID {
		return
	}
	if state.DisconnectTimer != nil {
		state.DisconnectTimer.Stop()
		state.DisconnectTimer = nil
	}
	state.ActiveConnections++
	h.handoffs[runtimeSessionID] = state
}

func (h *SessionController) endHandoffConnection(runtimeSessionID string, handoffID string) {
	h.handoffsMu.Lock()
	defer h.handoffsMu.Unlock()

	state, ok := h.handoffs[runtimeSessionID]
	if !ok || state.HandoffID != handoffID {
		return
	}
	if state.ActiveConnections > 0 {
		state.ActiveConnections--
	}
	if state.ActiveConnections == 0 {
		state.DisconnectTimer = time.AfterFunc(h.handoffGrace, func() {
			h.completeHandoff(runtimeSessionID, handoffID)
		})
	}
	h.handoffs[runtimeSessionID] = state
}

func (h *SessionController) liveProxyPath(path string, token string) string {
	prefix := strings.TrimRight(h.noVNCBasePath, "/") + "/" + token
	rest := strings.TrimPrefix(path, prefix)
	if rest == "" || rest == "/" {
		return "/"
	}
	if !strings.HasPrefix(rest, "/") {
		rest = "/" + rest
	}
	return rest
}

func writeBrowserErr(w http.ResponseWriter, err error) {
	switch err {
	case nil:
		return
	}
	switch {
	case errors.Is(err, session.ErrSessionNotFound):
		types.WriteErr(w, http.StatusNotFound, "SESSION_NOT_FOUND", err.Error())
	case errors.Is(err, runtime.ErrSnapshotNotFound):
		types.WriteErr(w, http.StatusBadRequest, "SNAPSHOT_NOT_FOUND", err.Error())
	case errors.Is(err, runtime.ErrStaleRef):
		types.WriteErr(w, http.StatusConflict, "STALE_REF", err.Error())
	case errors.Is(err, runtime.ErrInvalidRef):
		types.WriteErr(w, http.StatusBadRequest, "INVALID_REF", err.Error())
	case errors.Is(err, browser.ErrInvalidKey):
		types.WriteErr(w, http.StatusBadRequest, "INVALID_KEY", err.Error())
	case errors.Is(err, browser.ErrInvalidRequest):
		types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
	case errors.Is(err, browser.ErrPlaywrightUnavailable):
		types.WriteErr(w, http.StatusNotImplemented, "PLAYWRIGHT_NOT_AVAILABLE", err.Error())
	case errors.Is(err, browser.ErrLiveRuntimeUnhealthy):
		types.WriteErr(w, http.StatusServiceUnavailable, "LIVE_RUNTIME_UNHEALTHY", err.Error())
	case errors.Is(err, browser.ErrSnapshotFailed):
		types.WriteErr(w, http.StatusBadGateway, "SNAPSHOT_FAILED", err.Error())
	case errors.Is(err, browser.ErrNavigationFailed):
		types.WriteErr(w, http.StatusBadGateway, "NAVIGATION_FAILED", err.Error())
	case errors.Is(err, browser.ErrActionFailed):
		types.WriteErr(w, http.StatusBadGateway, "ACTION_FAILED", err.Error())
	case errors.Is(err, browser.ErrWaitForTimeout):
		types.WriteErr(w, http.StatusGatewayTimeout, "WAIT_FOR_TIMEOUT", err.Error())
	case errors.Is(err, browser.ErrUploadFilesFailed):
		types.WriteErr(w, http.StatusBadGateway, "UPLOAD_FILES_FAILED", err.Error())
	case errors.Is(err, browser.ErrUploadSourceFetchFailed):
		types.WriteErr(w, http.StatusBadGateway, "UPLOAD_SOURCE_FETCH_FAILED", err.Error())
	case strings.Contains(err.Error(), "EVALUATE_RESULT_NOT_JSON:"):
		types.WriteErr(w, http.StatusBadRequest, "EVALUATE_RESULT_NOT_JSON", normalizeEvaluateResultNotJSONMessage(err.Error()))
	case errors.Is(err, browser.ErrEvaluateFailed):
		types.WriteErr(w, http.StatusBadGateway, "EVALUATE_FAILED", err.Error())
	case errors.Is(err, browser.ErrScreenshotFailed):
		types.WriteErr(w, http.StatusBadGateway, "SCREENSHOT_FAILED", err.Error())
	default:
		types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
	}
}

func normalizeEvaluateResultNotJSONMessage(message string) string {
	const marker = "EVALUATE_RESULT_NOT_JSON:"
	idx := strings.Index(message, marker)
	if idx < 0 {
		return "evaluate result is not JSON serializable; return a JSON-serializable value such as textContent, outerHTML, or a plain object"
	}
	detail := strings.TrimSpace(message[idx+len(marker):])
	if detail == "" {
		detail = "evaluate result is not JSON serializable"
	}
	return detail + "; return a JSON-serializable value such as textContent, outerHTML, or a plain object"
}

func (h *SessionController) decodeLiveViewRequest(w http.ResponseWriter, r *http.Request) (liveViewRequest, bool) {
	var req liveViewRequest
	if r.Body == nil {
		return req, true
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid json body")
		return liveViewRequest{}, false
	}
	return req, true
}

func (h *SessionController) issueViewer(runtimeSessionID string, handoffID string, req liveViewRequest, defaultPermission live.Permission) (liveViewOutput, error) {
	if strings.TrimSpace(h.liveBaseURL) == "" {
		return liveViewOutput{}, errLiveBaseURLMissing
	}
	if err := h.manager.Touch(runtimeSessionID); err != nil {
		return liveViewOutput{}, err
	}
	permission := defaultPermission
	if strings.TrimSpace(req.Permission) != "" {
		permission = live.Permission(strings.TrimSpace(req.Permission))
	}
	ttl := h.liveTokenTTL
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	if strings.TrimSpace(handoffID) == "" {
		var err error
		handoffID, err = newOpaqueID("lv")
		if err != nil {
			return liveViewOutput{}, err
		}
	}
	token, state, err := h.tokenStore.Issue(live.IssueRequest{
		RuntimeSessionID: runtimeSessionID,
		HandoffID:        handoffID,
		Permission:       permission,
		TTL:              ttl,
	})
	if err != nil {
		return liveViewOutput{}, err
	}
	return liveViewOutput{
		HandoffID:  handoffID,
		ViewerURL:  h.viewerURL(token),
		ExpiresAt:  state.ExpiresAt,
		Permission: permission,
	}, nil
}

var errLiveBaseURLMissing = errors.New("live base url is not configured")

func (h *SessionController) writeLiveErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, session.ErrSessionNotFound):
		types.WriteErr(w, http.StatusNotFound, "SESSION_NOT_FOUND", err.Error())
	case errors.Is(err, live.ErrInvalidTokenRequest):
		types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
	case errors.Is(err, errLiveBaseURLMissing):
		types.WriteErr(w, http.StatusServiceUnavailable, "LIVE_VIEW_NOT_CONFIGURED", err.Error())
	default:
		types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
	}
}

func (h *SessionController) viewerURL(token string) string {
	base := h.liveBaseURL + h.noVNCBasePath + "/" + token
	path := strings.TrimLeft(h.noVNCBasePath+"/"+token+"/websockify", "/")
	return base + "/vnc.html?autoconnect=true&resize=remote&path=" + url.QueryEscape(path)
}

func (h *SessionController) hasActiveHandoff(runtimeSessionID string) bool {
	h.handoffsMu.Lock()
	defer h.handoffsMu.Unlock()
	_, ok := h.handoffs[runtimeSessionID]
	return ok
}

func (h *SessionController) revokeRuntimeSession(runtimeSessionID string) {
	h.handoffsMu.Lock()
	if state, ok := h.handoffs[runtimeSessionID]; ok {
		if state.DisconnectTimer != nil {
			state.DisconnectTimer.Stop()
		}
		h.tokenStore.Revoke(state.Token)
		delete(h.handoffs, runtimeSessionID)
	}
	h.handoffsMu.Unlock()
	h.tokenStore.RevokeSession(runtimeSessionID)
}

func (h *SessionController) touchSession(w http.ResponseWriter, runtimeSessionID string) bool {
	if err := h.manager.Touch(runtimeSessionID); err != nil {
		writeBrowserErr(w, err)
		return false
	}
	return true
}

func (h *SessionController) StartExpiredSessionReaper(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				h.ReapExpiredSessions(now)
			}
		}
	}()
}

func (h *SessionController) ReapExpiredSessions(now time.Time) int {
	// Atomically exclude active operations and reserve claimed sessions before
	// releasing admission control, so neither navigation nor deletion can race.
	h.operations.mu.Lock()
	excluded := make([]string, 0, len(h.operations.busy))
	for id := range h.operations.busy {
		excluded = append(excluded, id)
	}
	expired := h.manager.ClaimExpired(now, excluded...)
	if h.operations.busy == nil {
		h.operations.busy = make(map[string]bool)
	}
	for _, info := range expired {
		h.operations.busy[info.RuntimeSessionID] = true
	}
	h.operations.mu.Unlock()
	for _, info := range expired {
		runtimeSessionID := info.RuntimeSessionID
		if h.browser != nil {
			if err := h.browser.Close(runtimeSessionID); err != nil {
				slog.Warn("close expired browserd session failed", "runtime_session_id", runtimeSessionID, "error", err)
			}
		}
		h.revokeRuntimeSession(runtimeSessionID)
		if err := h.manager.Delete(runtimeSessionID); err != nil && !errors.Is(err, session.ErrSessionNotFound) {
			slog.Warn("delete expired browserd session failed", "runtime_session_id", runtimeSessionID, "error", err)
		}
	}
	for _, info := range expired {
		h.operations.releaseFunc(info.RuntimeSessionID)()
	}
	return len(expired)
}

func (h *SessionController) completeHandoff(runtimeSessionID string, handoffID string) bool {
	h.handoffsMu.Lock()
	state, exists := h.handoffs[runtimeSessionID]
	if !exists || state.HandoffID != strings.TrimSpace(handoffID) {
		h.handoffsMu.Unlock()
		return false
	}
	if state.DisconnectTimer != nil {
		state.DisconnectTimer.Stop()
	}
	delete(h.handoffs, runtimeSessionID)
	h.handoffsMu.Unlock()

	h.tokenStore.Revoke(state.Token)
	return true
}

func newOpaqueID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func tokenFromViewerURL(viewerURL string, liveBaseURL string, basePath string) string {
	parsed, err := url.Parse(viewerURL)
	if err == nil && parsed.Path != "" {
		prefix := strings.TrimRight(basePath, "/") + "/"
		rest := strings.TrimPrefix(parsed.Path, prefix)
		rest = strings.TrimLeft(rest, "/")
		token, _, _ := strings.Cut(rest, "/")
		return strings.TrimSpace(token)
	}
	token := strings.TrimPrefix(viewerURL, strings.TrimRight(liveBaseURL, "/")+strings.TrimRight(basePath, "/")+"/")
	token, _, _ = strings.Cut(strings.Trim(token, "/"), "/")
	return strings.TrimSpace(token)
}

func ExtractRuntimeSessionID(path string) (string, bool) {
	// expected:
	// /v1/sessions/{runtimeSessionId}/commit
	// /v1/sessions/{runtimeSessionId}
	if !strings.HasPrefix(path, "/v1/sessions/") {
		return "", false
	}
	rest := strings.TrimPrefix(path, "/v1/sessions/")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return "", false
	}
	parts := strings.Split(rest, "/")
	return parts[0], true
}
