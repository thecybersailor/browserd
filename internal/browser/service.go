package browser

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"browserd/internal/assets"
	browserrt "browserd/internal/runtime"
	"browserd/internal/session"

	cdbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	cdinput "github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	browserdclient "github.com/flaboy/browserd-client-go/pkg/browserd"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

var (
	ErrInvalidRequest          = errors.New("invalid request")
	ErrInvalidKey              = errors.New("invalid key")
	ErrSnapshotFailed          = errors.New("snapshot failed")
	ErrNavigationFailed        = errors.New("navigation failed")
	ErrActionFailed            = errors.New("action failed")
	ErrEvaluateFailed          = errors.New("evaluate failed")
	ErrUploadFilesFailed       = errors.New("upload files failed")
	ErrUploadSourceFetchFailed = errors.New("upload source fetch failed")
	ErrWaitForTimeout          = errors.New("wait for timeout")
	ErrScreenshotFailed        = errors.New("screenshot failed")
	ErrPlaywrightUnavailable   = errors.New("playwright not available")
	ErrProxyHopFailed          = errors.New("proxy hop failed")
	ErrProfileCheckpointFailed = errors.New("profile checkpoint failed")
)

func chromedpErrorLogger(logf func(string, ...any)) func(string, ...any) {
	return func(format string, args ...any) {
		message := fmt.Sprintf(format, args...)
		if strings.Contains(message, "could not unmarshal event:") &&
			strings.Contains(message, "network.IPAddressSpace") &&
			strings.Contains(message, "unknown IPAddressSpace value: Private") {
			return
		}
		logf("%s", message)
	}
}

type NavigateInput struct {
	IncludeSnapshot           bool
	URL                       string
	WaitUntil                 string
	TimeoutMs                 int
	AfterLoadScreenshotS3Path string
}

type NavigateOutput struct {
	Snapshot        *SnapshotOutput `json:"snapshot,omitempty"`
	URL             string          `json:"url"`
	Title           string          `json:"title,omitempty"`
	SnapshotCleared bool            `json:"snapshotCleared"`
}

type SnapshotInput struct {
	Mode string
}

type SnapshotRef struct {
	Ref     string `json:"ref"`
	Role    string `json:"role,omitempty"`
	Name    string `json:"name,omitempty"`
	Text    string `json:"text,omitempty"`
	TagName string `json:"tagName,omitempty"`
}

type PageTable struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

type observedImageRequest struct {
	URL            string `json:"url"`
	PageURL        string `json:"page_url"`
	RefererKnown   bool   `json:"referer_known"`
	RefererPresent bool   `json:"referer_present"`
	Referer        string `json:"referer,omitempty"`
	UserAgent      string `json:"user_agent,omitempty"`
}

type PageSnapshot struct {
	URL           string                 `json:"url,omitempty"`
	Title         string                 `json:"title,omitempty"`
	FormatVersion int                    `json:"formatVersion,omitempty"`
	Tree          json.RawMessage        `json:"tree,omitempty"`
	Capture       json.RawMessage        `json:"capture,omitempty"`
	Encoding      string                 `json:"encoding,omitempty"`
	Attributes    json.RawMessage        `json:"attributes,omitempty"`
	States        json.RawMessage        `json:"states,omitempty"`
	Groups        map[string]PageTable   `json:"groups,omitempty"`
	ImageRequests []observedImageRequest `json:"imageRequests,omitempty"`
}

type SnapshotOutput struct {
	SnapshotID string       `json:"snapshotId"`
	Page       PageSnapshot `json:"page"`
}

type ActInput struct {
	Action        string   `json:"action"`
	Ref           string   `json:"ref,omitempty"`
	X             float64  `json:"x,omitempty"`
	Y             float64  `json:"y,omitempty"`
	DeltaX        float64  `json:"deltaX,omitempty"`
	DeltaY        float64  `json:"deltaY,omitempty"`
	Text          string   `json:"text,omitempty"`
	HTML          string   `json:"html,omitempty"`
	Key           string   `json:"key,omitempty"`
	Value         string   `json:"value,omitempty"`
	Values        []string `json:"values,omitempty"`
	Clear         bool     `json:"clear,omitempty"`
	Submit        bool     `json:"submit,omitempty"`
	Button        string   `json:"button,omitempty"`
	ClickCount    int      `json:"clickCount,omitempty"`
	MotionProfile string   `json:"motionProfile,omitempty"`
	TimeoutMs     int      `json:"timeoutMs,omitempty"`
}

type ActOutput struct {
	OK     bool   `json:"ok"`
	Action string `json:"action"`
	Ref    string `json:"ref,omitempty"`
	URL    string `json:"url,omitempty"`
	Title  string `json:"title,omitempty"`
}

type ScreenshotInput struct {
	Mode               string `json:"mode,omitempty"`
	Selector           string `json:"selector,omitempty"`
	Ref                string `json:"ref,omitempty"`
	FullPage           bool   `json:"fullPage,omitempty"`
	Format             string `json:"format,omitempty"`
	Quality            int    `json:"quality,omitempty"`
	ScreenshotS3Prefix string `json:"screenshotS3Prefix,omitempty"`
}

type ScreenshotOutput struct {
	ScreenshotID string `json:"screenshotId"`
	S3Path       string `json:"s3Path"`
	ContentType  string `json:"contentType"`
	ByteLength   int    `json:"byteLength"`
}

type UploadFileSource struct {
	S3Path    string `json:"s3Path,omitempty"`
	LocalPath string `json:"localPath,omitempty"`
	URL       string `json:"url,omitempty"`
	Filename  string `json:"filename,omitempty"`
}

type UploadFilesInput struct {
	Ref       string             `json:"ref"`
	X         float64            `json:"x,omitempty"`
	Y         float64            `json:"y,omitempty"`
	Files     []UploadFileSource `json:"files"`
	TimeoutMs int                `json:"timeoutMs,omitempty"`
}

type UploadFilesOutput struct {
	OK        bool     `json:"ok"`
	Ref       string   `json:"ref"`
	FileNames []string `json:"fileNames"`
}

type EvaluateInput struct {
	Script         string                               `json:"script"`
	Args           []any                                `json:"args,omitempty"`
	TimeoutMs      int                                  `json:"timeoutMs,omitempty"`
	World          string                               `json:"world,omitempty"`
	PageToolBridge *browserdclient.PageToolBridgeConfig `json:"pageToolBridge,omitempty"`
}

type EvaluateOutput struct {
	Result any    `json:"result"`
	URL    string `json:"url"`
	Title  string `json:"title"`
}

type WaitForCondition struct {
	Type        string   `json:"type"`
	Ref         string   `json:"ref,omitempty"`
	Selector    string   `json:"selector,omitempty"`
	Text        string   `json:"text,omitempty"`
	URLContains string   `json:"urlContains,omitempty"`
	URLMatches  string   `json:"urlMatches,omitempty"`
	Checks      []string `json:"checks,omitempty"`
}

type WaitForInput struct {
	Condition  WaitForCondition `json:"condition"`
	TimeoutMs  int              `json:"timeoutMs,omitempty"`
	IntervalMs int              `json:"intervalMs,omitempty"`
	StableMs   int              `json:"stableMs,omitempty"`
}

type WaitForOutput struct {
	OK            bool     `json:"ok"`
	ConditionType string   `json:"conditionType"`
	Ref           string   `json:"ref,omitempty"`
	Text          string   `json:"text,omitempty"`
	X             float64  `json:"x,omitempty"`
	Y             float64  `json:"y,omitempty"`
	Checks        []string `json:"checks,omitempty"`
	URL           string   `json:"url,omitempty"`
	Title         string   `json:"title,omitempty"`
}

type Service struct {
	cookiesMu       sync.Mutex
	cookieRevisions map[string]int64
	navigatePage    func(context.Context, string) (NavigateOutput, error)
	captureSnapshot func(context.Context) (snapshotRuntimeEnvelope, error)
	sessions        session.Manager
	state           *browserrt.State
	assets          assets.Store
	assetBucket     string
	proxyHop        ProxyHopOptions

	capturePNG         func(context.Context) ([]byte, error)
	setFileInputFiles  func(runtimeSessionID string, selector string, filePaths []string, timeoutMs int) error
	setFilesAtPoint    func(runtimeSessionID string, x float64, y float64, filePaths []string, timeoutMs int) error
	dropFilesOnTarget  func(runtimeSessionID string, selector string, filePaths []string, timeoutMs int) error
	mu                 sync.Mutex
	browsers           map[string]*activeBrowser
	pointers           map[string]pointerState
	imageRequests      map[string][]observedImageRequest
	pointerSubscribers map[string]map[chan VirtualPointerSnapshot]struct{}
}

type pointerState struct {
	Point       pointerPoint
	Viewport    viewportRect
	Initialized bool
	ButtonDown  bool
}

type browserTarget struct {
	Selector string
	Rect     targetRect
	Editable bool
}

type ProxyHopOptions struct {
	Mode        string
	WorkerURL   string
	WorkerToken string
}

type ServiceOptions struct {
	Sessions    session.Manager
	State       *browserrt.State
	Assets      assets.Store
	AssetBucket string
	ProxyHop    ProxyHopOptions
}

type activeBrowser struct {
	cmd          *exec.Cmd
	live         *LiveRuntime
	wsURL        string
	rootCtx      context.Context
	rootCancel   context.CancelFunc
	allocCtx     context.Context
	allocCancel  context.CancelFunc
	pageCtx      context.Context
	pageCancel   context.CancelFunc
	proxyAdapter *localProxyAdapter
}

type snapshotRuntimeEnvelope struct {
	Page PageSnapshot                  `json:"page"`
	Refs map[string]browserrt.RefState `json:"refs"`
}

func NewService(sessions session.Manager, state *browserrt.State, assetStore assets.Store) *Service {
	return NewServiceWithOptions(ServiceOptions{
		Sessions: sessions,
		State:    state,
		Assets:   assetStore,
	})
}

func NewServiceWithOptions(opts ServiceOptions) *Service {
	state := opts.State
	if state == nil {
		state = browserrt.NewState()
	}
	svc := &Service{
		sessions:           opts.Sessions,
		state:              state,
		assets:             opts.Assets,
		assetBucket:        strings.TrimSpace(opts.AssetBucket),
		proxyHop:           opts.ProxyHop,
		capturePNG:         capturePagePNG,
		navigatePage:       navigatePage,
		captureSnapshot:    captureSnapshotEnvelope,
		browsers:           map[string]*activeBrowser{},
		pointers:           map[string]pointerState{},
		imageRequests:      map[string][]observedImageRequest{},
		pointerSubscribers: map[string]map[chan VirtualPointerSnapshot]struct{}{},
	}
	svc.setFileInputFiles = svc.defaultSetFileInputFiles
	svc.setFilesAtPoint = svc.defaultSetFilesAtPoint
	svc.dropFilesOnTarget = svc.defaultDropFilesOnTarget
	return svc
}

func (s *Service) PrepareSession(runtimeSessionID string) error {
	_, err := s.ensureBrowser(runtimeSessionID)
	return err
}

func (s *Service) Close(runtimeSessionID string) error {
	b, ok := s.removeActiveBrowser(runtimeSessionID)
	if !ok {
		return nil
	}
	if b.cmd != nil && b.cmd.Process != nil {
		_ = b.cmd.Process.Kill()
		_, _ = b.cmd.Process.Wait()
	}
	s.cleanupActiveBrowser(b)
	return nil
}

func (s *Service) Checkpoint(runtimeSessionID string) error {
	s.mu.Lock()
	b, ok := s.browsers[runtimeSessionID]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	if err := s.closeBrowserGracefully(b, 15*time.Second); err != nil {
		return fmt.Errorf("%w: %v", ErrProfileCheckpointFailed, err)
	}
	if removed, ok := s.removeActiveBrowser(runtimeSessionID); ok {
		s.cleanupActiveBrowser(removed)
	}
	return nil
}

func (s *Service) removeActiveBrowser(runtimeSessionID string) (*activeBrowser, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.browsers[runtimeSessionID]
	if !ok {
		return nil, false
	}
	delete(s.browsers, runtimeSessionID)
	delete(s.pointers, runtimeSessionID)
	delete(s.imageRequests, runtimeSessionID)
	for ch := range s.pointerSubscribers[runtimeSessionID] {
		close(ch)
	}
	delete(s.pointerSubscribers, runtimeSessionID)
	return b, true
}

func (s *Service) cleanupActiveBrowser(b *activeBrowser) {
	if b.live != nil {
		_ = b.live.Stop(context.Background())
	}
	if b.pageCancel != nil {
		b.pageCancel()
	}
	if b.allocCancel != nil {
		b.allocCancel()
	}
	if b.rootCancel != nil {
		b.rootCancel()
	}
	if b.proxyAdapter != nil {
		_ = b.proxyAdapter.Close()
	}
}

func (s *Service) closeBrowserGracefully(b *activeBrowser, timeout time.Duration) error {
	if b == nil || strings.TrimSpace(b.wsURL) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, _, _, err := ws.Dial(ctx, b.wsURL)
	if err != nil {
		if b.cmd != nil && b.cmd.Process != nil {
			return waitChromeProcessExit(ctx, b.cmd)
		}
		return err
	}
	if err := wsutil.WriteClientText(conn, []byte(`{"id":1,"method":"Browser.close"}`)); err != nil {
		_ = conn.Close()
		return err
	}
	_ = conn.Close()
	return waitChromeProcessExit(ctx, b.cmd)
}

func waitChromeProcessExit(ctx context.Context, cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	select {
	case err := <-done:
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return nil
			}
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) LiveProxyTarget(runtimeSessionID string) (string, error) {
	b, err := s.ensureBrowser(runtimeSessionID)
	if err != nil {
		return "", err
	}
	if b.live == nil {
		return "", ErrPlaywrightUnavailable
	}
	if err := b.live.Health(context.Background()); err != nil {
		return "", err
	}
	return b.live.ProxyTarget(), nil
}

// Navigate optionally captures a fresh observation under the navigation deadline.
func (s *Service) Navigate(parent context.Context, runtimeSessionID string, input NavigateInput) (NavigateOutput, error) {
	if strings.TrimSpace(input.URL) == "" || (input.IncludeSnapshot && input.WaitUntil != "" && input.WaitUntil != "load") {
		return NavigateOutput{}, ErrInvalidRequest
	}
	if err := parent.Err(); err != nil {
		return NavigateOutput{}, err
	}
	ctx, cancel, err := s.newBrowserContext(runtimeSessionID, input.TimeoutMs)
	if err != nil {
		return NavigateOutput{}, err
	}
	defer cancel()
	if deadline, ok := parent.Deadline(); ok {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithDeadline(ctx, deadline)
		defer deadlineCancel()
	}
	stop := context.AfterFunc(parent, cancel)
	defer stop()
	return s.navigateWithContext(ctx, runtimeSessionID, input)
}

func navigatePage(ctx context.Context, rawURL string) (NavigateOutput, error) {
	var out NavigateOutput
	err := chromedp.Run(ctx, chromedp.Navigate(rawURL), chromedp.Title(&out.Title), chromedp.Location(&out.URL))
	return out, err
}

func (s *Service) navigateWithContext(ctx context.Context, id string, input NavigateInput) (NavigateOutput, error) {
	s.state.ClearSnapshot(id)
	out, err := s.navigatePage(ctx, input.URL)
	if err != nil {
		return NavigateOutput{}, fmt.Errorf("%w: %w", ErrNavigationFailed, err)
	}
	if err = s.uploadAfterNavigate(ctx, input.AfterLoadScreenshotS3Path); err != nil {
		return NavigateOutput{}, fmt.Errorf("%w: %w", ErrScreenshotFailed, err)
	}
	out.SnapshotCleared = true
	if input.IncludeSnapshot {
		snapshot, err := s.snapshotWithContext(ctx, id)
		if err != nil {
			return NavigateOutput{}, fmt.Errorf("%w: navigation may have completed; no valid snapshot: %w", ErrSnapshotFailed, err)
		}
		out.Snapshot = &snapshot
		out.URL = snapshot.Page.URL
		out.Title = snapshot.Page.Title
	}
	return out, nil
}

func (s *Service) uploadAfterNavigate(ctx context.Context, s3Path string) error {
	s3Path = strings.TrimSpace(s3Path)
	if s3Path == "" {
		return nil
	}
	if s.assets == nil {
		return fmt.Errorf("asset store not configured")
	}
	if s.capturePNG == nil {
		return fmt.Errorf("screenshot capture not configured")
	}
	png, err := s.capturePNG(ctx)
	if err != nil {
		return err
	}
	return s.assets.Put(ctx, s3Path, png, "image/png")
}

func capturePagePNG(ctx context.Context) ([]byte, error) {
	var png []byte
	if err := chromedp.Run(ctx, chromedp.FullScreenshot(&png, 100)); err != nil {
		return nil, err
	}
	return png, nil
}

func (s *Service) Snapshot(runtimeSessionID string, input SnapshotInput) (SnapshotOutput, error) {
	ctx, cancel, err := s.newBrowserContext(runtimeSessionID, 20_000)
	if err != nil {
		return SnapshotOutput{}, err
	}
	defer cancel()

	return s.snapshotWithContext(ctx, runtimeSessionID)
}

func captureSnapshotEnvelope(ctx context.Context) (snapshotRuntimeEnvelope, error) {
	var envelope snapshotRuntimeEnvelope
	err := chromedp.Run(ctx, chromedp.Evaluate(browserSnapshotRuntimeScript, &envelope))
	return envelope, err
}

func (s *Service) snapshotWithContext(ctx context.Context, runtimeSessionID string) (SnapshotOutput, error) {
	envelope, err := s.captureSnapshot(ctx)
	if err != nil {
		return SnapshotOutput{}, fmt.Errorf("%w: %w", ErrSnapshotFailed, err)
	}
	if err = ctx.Err(); err != nil {
		return SnapshotOutput{}, err
	}
	parsed, err := url.Parse(envelope.Page.URL)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return SnapshotOutput{}, fmt.Errorf("%w: invalid observed page URL", ErrSnapshotFailed)
	}

	snapshotID := fmt.Sprintf("snap_%d", time.Now().UnixNano())
	page := envelope.Page
	page.ImageRequests = s.observedImageRequestsForPage(runtimeSessionID, page.URL)
	switch page.FormatVersion {
	case 0, 1:
		if len(page.Tree) != 0 || len(page.Capture) != 0 {
			return SnapshotOutput{}, fmt.Errorf("%w: tree requires snapshot formatVersion 2", ErrSnapshotFailed)
		}
	case 2:
		if page.Groups != nil || !jsonObject(page.Tree) || !jsonObject(page.Capture) {
			return SnapshotOutput{}, fmt.Errorf("%w: invalid tree snapshot envelope", ErrSnapshotFailed)
		}
	case 3:
		raw, marshalErr := json.Marshal(page)
		var publicPage browserdclient.PageSnapshot
		if marshalErr != nil || json.Unmarshal(raw, &publicPage) != nil {
			return SnapshotOutput{}, fmt.Errorf("%w: invalid compact page", ErrSnapshotFailed)
		}
		if _, decodeErr := browserdclient.DecodeSnapshotPage(publicPage); decodeErr != nil {
			return SnapshotOutput{}, fmt.Errorf("%w: %v", ErrSnapshotFailed, decodeErr)
		}
	default:
		return SnapshotOutput{}, fmt.Errorf("%w: unsupported snapshot formatVersion %d", ErrSnapshotFailed, page.FormatVersion)
	}
	refs := envelope.Refs
	if refs == nil {
		refs = map[string]browserrt.RefState{}
	}
	for ref, state := range refs {
		if state.Ref == "" {
			state.Ref = ref
		}
		state.SnapshotID = snapshotID
		refs[ref] = state
	}

	s.state.ReplaceSnapshot(runtimeSessionID, browserrt.SnapshotState{
		SnapshotID: snapshotID,
		Page: browserrt.PageState{
			URL:           page.URL,
			Title:         page.Title,
			Groups:        pageGroupsToState(page.Groups),
			FormatVersion: page.FormatVersion,
			Tree:          page.Tree,
			Capture:       page.Capture,
			Encoding:      page.Encoding,
			Attributes:    page.Attributes,
			States:        page.States,
		},
		Refs: refs,
	})

	return SnapshotOutput{
		SnapshotID: snapshotID,
		Page:       page,
	}, nil
}

func (s *Service) installImageRequestObserver(runtimeSessionID string, ctx context.Context) error {
	chromedp.ListenTarget(ctx, func(ev any) {
		event, ok := ev.(*network.EventRequestWillBeSent)
		if !ok || event == nil || event.Type != network.ResourceTypeImage || event.Request == nil {
			return
		}
		request := observedImageRequest{
			URL:          strings.TrimSpace(event.Request.URL),
			PageURL:      strings.TrimSpace(event.DocumentURL),
			RefererKnown: true,
			UserAgent:    headerString(event.Request.Headers, "user-agent"),
		}
		if referer := headerString(event.Request.Headers, "referer"); referer != "" {
			request.RefererPresent = true
			request.Referer = referer
		}
		s.recordObservedImageRequest(runtimeSessionID, request)
	})
	return chromedp.Run(ctx, network.Enable())
}

func headerString(headers network.Headers, name string) string {
	for key, value := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		switch v := value.(type) {
		case string:
			return strings.TrimSpace(v)
		case fmt.Stringer:
			return strings.TrimSpace(v.String())
		default:
			return strings.TrimSpace(fmt.Sprint(v))
		}
	}
	return ""
}

func (s *Service) recordObservedImageRequest(runtimeSessionID string, request observedImageRequest) {
	if strings.TrimSpace(runtimeSessionID) == "" || strings.TrimSpace(request.URL) == "" || strings.TrimSpace(request.PageURL) == "" {
		return
	}
	request.URL = strings.TrimSpace(request.URL)
	request.PageURL = strings.TrimSpace(request.PageURL)
	request.Referer = strings.TrimSpace(request.Referer)
	request.UserAgent = strings.TrimSpace(request.UserAgent)
	s.mu.Lock()
	defer s.mu.Unlock()
	items := append(s.imageRequests[runtimeSessionID], request)
	if len(items) > 1000 {
		items = items[len(items)-1000:]
	}
	s.imageRequests[runtimeSessionID] = items
}

func (s *Service) observedImageRequestsForPage(runtimeSessionID string, pageURL string) []observedImageRequest {
	pageURL = strings.TrimSpace(pageURL)
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.imageRequests[runtimeSessionID]
	if len(items) == 0 || pageURL == "" {
		return nil
	}
	latest := map[string]observedImageRequest{}
	order := []string{}
	for _, item := range items {
		if strings.TrimSpace(item.PageURL) != pageURL || strings.TrimSpace(item.URL) == "" {
			continue
		}
		key := item.PageURL + "\x00" + item.URL
		if _, exists := latest[key]; !exists {
			order = append(order, key)
		}
		latest[key] = item
	}
	out := make([]observedImageRequest, 0, len(order))
	for _, key := range order {
		out = append(out, latest[key])
	}
	return out
}

func jsonObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}

func (s *Service) Act(runtimeSessionID string, input ActInput) (ActOutput, error) {
	if input.Action == "type" && input.Ref == "" {
		return ActOutput{}, ErrInvalidRequest
	}
	ctx, cancel, err := s.newBrowserContext(runtimeSessionID, input.TimeoutMs)
	if err != nil {
		return ActOutput{}, err
	}
	defer cancel()

	switch input.Action {
	case "click":
		if strings.TrimSpace(input.Ref) == "" {
			if input.X <= 0 || input.Y <= 0 {
				return ActOutput{}, ErrInvalidRequest
			}
			err = s.trustedClickPoint(ctx, runtimeSessionID, input)
		} else {
			refState, refErr := s.state.GetRef(runtimeSessionID, input.Ref)
			if refErr != nil {
				return ActOutput{}, refErr
			}
			if err := validateActionRef(input.Action, refState); err != nil {
				return ActOutput{}, err
			}
			err = s.trustedClick(ctx, runtimeSessionID, refState.Selector, input)
		}
	case "doubleClick":
		refState, refErr := s.state.GetRef(runtimeSessionID, input.Ref)
		if refErr != nil {
			return ActOutput{}, refErr
		}
		if err := validateActionRef(input.Action, refState); err != nil {
			return ActOutput{}, err
		}
		selector := refState.Selector
		err = chromedp.Run(ctx, chromedp.DoubleClick(selector, chromedp.ByQuery))
	case "hover":
		target, refErr := s.resolveActRef(ctx, runtimeSessionID, input.Ref)
		if refErr != nil {
			return ActOutput{}, refErr
		}
		_, _, err = s.trustedMoveTarget(ctx, runtimeSessionID, target, input)
	case "scroll":
		err = s.trustedScroll(ctx, runtimeSessionID, input)
	case "paste":
		err = s.trustedPaste(ctx, runtimeSessionID, input)
	case "type":
		err = s.trustedTextInput(ctx, runtimeSessionID, input)
	case "fill":
		err = s.trustedTextInput(ctx, runtimeSessionID, ActInput{
			Action:        "type",
			Ref:           input.Ref,
			Text:          input.Value,
			Clear:         true,
			Submit:        input.Submit,
			Button:        input.Button,
			ClickCount:    input.ClickCount,
			MotionProfile: input.MotionProfile,
			TimeoutMs:     input.TimeoutMs,
		})
	case "press":
		refState, refErr := s.state.GetRef(runtimeSessionID, input.Ref)
		if refErr != nil {
			return ActOutput{}, refErr
		}
		if err := validateActionRef(input.Action, refState); err != nil {
			return ActOutput{}, err
		}
		encodedKey, keyErr := normalizePressKey(input.Key)
		if keyErr != nil {
			return ActOutput{}, keyErr
		}
		selector := refState.Selector
		err = chromedp.Run(ctx, chromedp.SendKeys(selector, encodedKey, chromedp.ByQuery))
	case "scrollIntoView":
		refState, refErr := s.state.GetRef(runtimeSessionID, input.Ref)
		if refErr != nil {
			return ActOutput{}, refErr
		}
		if err := validateActionRef(input.Action, refState); err != nil {
			return ActOutput{}, err
		}
		selector := refState.Selector
		err = chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(`(() => { const el = document.querySelector(%q); if (!el) throw new Error("missing"); el.scrollIntoView({block:"center", inline:"center"}); return true; })()`, selector), nil))
	case "select":
		refState, refErr := s.state.GetRef(runtimeSessionID, input.Ref)
		if refErr != nil {
			return ActOutput{}, refErr
		}
		if err := validateActionRef(input.Action, refState); err != nil {
			return ActOutput{}, err
		}
		selector := refState.Selector
		err = chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(`(() => { const el = document.querySelector(%q); if (!el) throw new Error("missing"); const values = %s; for (const opt of el.options ?? []) { opt.selected = values.includes(opt.value); } el.dispatchEvent(new Event("input", {bubbles:true})); el.dispatchEvent(new Event("change", {bubbles:true})); return true; })()`, selector, jsStringArray(input.Values)), nil))
	case "waitFor":
		refState, refErr := s.state.GetRef(runtimeSessionID, input.Ref)
		if refErr != nil {
			return ActOutput{}, refErr
		}
		if err := validateActionRef(input.Action, refState); err != nil {
			return ActOutput{}, err
		}
		selector := refState.Selector
		err = chromedp.Run(ctx, chromedp.WaitVisible(selector, chromedp.ByQuery))
	default:
		return ActOutput{}, ErrInvalidRequest
	}
	if err != nil {
		return ActOutput{}, fmt.Errorf("%w: %v", ErrActionFailed, err)
	}
	if err := sleepBehavior(ctx, defaultBehaviorProfile().ActionAfterDelay); err != nil {
		return ActOutput{}, fmt.Errorf("%w: %v", ErrActionFailed, err)
	}

	var url string
	var title string
	_ = chromedp.Run(ctx, chromedp.Location(&url), chromedp.Title(&title))
	return ActOutput{
		OK:     true,
		Action: input.Action,
		Ref:    input.Ref,
		URL:    url,
		Title:  title,
	}, nil
}

func (s *Service) WaitFor(runtimeSessionID string, input WaitForInput) (WaitForOutput, error) {
	if strings.TrimSpace(input.Condition.Type) == "" {
		return WaitForOutput{}, ErrInvalidRequest
	}
	timeoutMs := input.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = 30000
	}
	intervalMs := input.IntervalMs
	if intervalMs <= 0 {
		intervalMs = 250
	}
	stableMs := input.StableMs
	if stableMs < 0 {
		return WaitForOutput{}, ErrInvalidRequest
	}
	ctx, cancel, err := s.newBrowserContext(runtimeSessionID, timeoutMs)
	if err != nil {
		return WaitForOutput{}, err
	}
	defer cancel()

	var firstMatchedAt time.Time
	for {
		out, matched, err := s.checkWaitForCondition(ctx, runtimeSessionID, input.Condition)
		if err != nil {
			return WaitForOutput{}, err
		}
		if matched {
			if stableMs == 0 {
				return out, nil
			}
			if firstMatchedAt.IsZero() {
				firstMatchedAt = time.Now()
			}
			if time.Since(firstMatchedAt) >= time.Duration(stableMs)*time.Millisecond {
				return out, nil
			}
		} else {
			firstMatchedAt = time.Time{}
		}
		timer := time.NewTimer(time.Duration(intervalMs) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return WaitForOutput{}, fmt.Errorf("%w: condition %q did not match within %dms", ErrWaitForTimeout, input.Condition.Type, timeoutMs)
		case <-timer.C:
		}
	}
}

func (s *Service) checkWaitForCondition(ctx context.Context, runtimeSessionID string, condition WaitForCondition) (WaitForOutput, bool, error) {
	condition.Type = strings.TrimSpace(condition.Type)
	switch condition.Type {
	case "url_contains":
		if strings.TrimSpace(condition.URLContains) == "" {
			return WaitForOutput{}, false, ErrInvalidRequest
		}
		return s.evaluateWaitFor(ctx, waitForURLScript("contains"), condition)
	case "url_matches":
		if strings.TrimSpace(condition.URLMatches) == "" {
			return WaitForOutput{}, false, ErrInvalidRequest
		}
		return s.evaluateWaitFor(ctx, waitForURLScript("matches"), condition)
	case "text_visible", "text_gone":
		if strings.TrimSpace(condition.Text) == "" {
			return WaitForOutput{}, false, ErrInvalidRequest
		}
		return s.evaluateWaitFor(ctx, waitForTextScript(), condition)
	case "element_visible", "element_enabled", "element_editable", "ref_actionable":
		if strings.TrimSpace(condition.Ref) == "" && strings.TrimSpace(condition.Selector) == "" {
			return WaitForOutput{}, false, ErrInvalidRequest
		}
		if strings.TrimSpace(condition.Selector) == "" {
			refState, err := s.state.GetRef(runtimeSessionID, condition.Ref)
			if err != nil {
				return WaitForOutput{}, false, err
			}
			if refState.Kind != "element" {
				return WaitForOutput{}, false, browserrt.ErrInvalidRef
			}
			condition.Selector = refState.Selector
		}
		if len(condition.Checks) == 0 {
			condition.Checks = defaultWaitForChecks(condition.Type)
		}
		return s.evaluateWaitFor(ctx, waitForElementScript(), condition)
	default:
		return WaitForOutput{}, false, ErrInvalidRequest
	}
}

func (s *Service) evaluateWaitFor(ctx context.Context, script string, condition WaitForCondition) (WaitForOutput, bool, error) {
	argsJSON, err := json.Marshal(condition)
	if err != nil {
		return WaitForOutput{}, false, err
	}
	var out struct {
		OK     bool     `json:"ok"`
		Text   string   `json:"text"`
		X      float64  `json:"x"`
		Y      float64  `json:"y"`
		Checks []string `json:"checks"`
		URL    string   `json:"url"`
		Title  string   `json:"title"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(`(async () => {
      const condition = %s;
      %s
    })()`, string(argsJSON), script), &out, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true)
	})); err != nil {
		return WaitForOutput{}, false, err
	}
	if !out.OK {
		return WaitForOutput{}, false, nil
	}
	return WaitForOutput{
		OK:            true,
		ConditionType: condition.Type,
		Ref:           condition.Ref,
		Text:          out.Text,
		X:             out.X,
		Y:             out.Y,
		Checks:        out.Checks,
		URL:           out.URL,
		Title:         out.Title,
	}, true, nil
}

func defaultWaitForChecks(conditionType string) []string {
	switch conditionType {
	case "element_visible":
		return []string{"visible"}
	case "element_enabled":
		return []string{"visible", "enabled"}
	case "element_editable":
		return []string{"visible", "enabled", "editable"}
	default:
		return []string{"visible", "stable", "receivesEvents", "enabled"}
	}
}

func waitForURLScript(mode string) string {
	switch mode {
	case "matches":
		return `
      const url = location.href;
      let ok = false;
      try { ok = new RegExp(condition.urlMatches).test(url); } catch { ok = false; }
      return { ok, url, title: document.title || "" };`
	default:
		return `
      const url = location.href;
      return { ok: url.includes(condition.urlContains || ""), url, title: document.title || "" };`
	}
}

func waitForTextScript() string {
	return `
      const visible = (el) => {
        if (!el) return false;
        const style = window.getComputedStyle(el);
        const r = el.getBoundingClientRect();
        return r.width > 0 && r.height > 0 && style.visibility !== "hidden" && style.display !== "none";
      };
      const text = String(condition.text || "");
      const nodes = Array.from(document.querySelectorAll("body *"));
      const match = nodes.find((el) => visible(el) && (el.innerText || el.textContent || "").includes(text));
      const found = Boolean(match);
      const ok = condition.type === "text_gone" ? !found : found;
      const r = match ? match.getBoundingClientRect() : { left: 0, top: 0, width: 0, height: 0 };
      return { ok, text: found ? text : "", x: r.left + r.width / 2, y: r.top + r.height / 2, url: location.href, title: document.title || "" };`
}

func waitForElementScript() string {
	return `
      const selector = String(condition.selector || "");
      const checks = Array.isArray(condition.checks) ? condition.checks : [];
      const el = selector ? document.querySelector(selector) : null;
      if (!el) return { ok: false, checks, url: location.href, title: document.title || "" };
      const visible = (node) => {
        const style = window.getComputedStyle(node);
        const r = node.getBoundingClientRect();
        return r.width > 0 && r.height > 0 && style.visibility !== "hidden" && style.display !== "none";
      };
      const enabled = (node) => {
        if (node.closest?.("[aria-disabled=true]")) return false;
        if (node.disabled) return false;
        const disabledFieldset = node.closest?.("fieldset[disabled]");
        return !disabledFieldset;
      };
      const editable = (node) => {
        const tag = (node.tagName || "").toLowerCase();
        const type = (node.type || "").toLowerCase();
        const textInput = tag === "textarea" || (tag === "input" && !["button","checkbox","file","hidden","image","radio","range","reset","submit"].includes(type));
        return enabled(node) && !node.readOnly && node.getAttribute("aria-readonly") !== "true" && (node.isContentEditable || textInput);
      };
      const stable = async (node) => {
        const first = node.getBoundingClientRect();
        await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
        const second = node.getBoundingClientRect();
        return first.left === second.left && first.top === second.top && first.width === second.width && first.height === second.height;
      };
      const receivesEvents = (node) => {
        const r = node.getBoundingClientRect();
        const x = r.left + r.width / 2;
        const y = r.top + r.height / 2;
        const hit = document.elementFromPoint(x, y);
        return Boolean(hit && (hit === node || node.contains(hit) || hit.contains(node)));
      };
      const r = el.getBoundingClientRect();
      const results = {
        visible: visible(el),
        enabled: enabled(el),
        editable: editable(el),
        receivesEvents: receivesEvents(el),
        stable: await stable(el)
      };
      const ok = checks.every((check) => results[check] === true);
      return { ok, checks, text: (el.innerText || el.value || el.textContent || "").trim(), x: r.left + r.width / 2, y: r.top + r.height / 2, url: location.href, title: document.title || "" };`
}

func validateActionRef(action string, ref browserrt.RefState) error {
	switch action {
	case "scrollIntoView":
		return nil
	case "click", "doubleClick", "hover", "type", "fill", "press", "select", "waitFor":
		if ref.Kind != "element" {
			return browserrt.ErrInvalidRef
		}
		return nil
	default:
		return ErrInvalidRequest
	}
}

func (s *Service) trustedTextInput(ctx context.Context, runtimeSessionID string, input ActInput) error {
	if strings.TrimSpace(input.Text) == "" && !input.Clear {
		return ErrInvalidRequest
	}
	if input.Ref == "" {
		return ErrInvalidRequest
	}
	target, err := s.resolveActRef(ctx, runtimeSessionID, input.Ref)
	if err != nil {
		return err
	}
	if err := s.trustedClickTarget(ctx, runtimeSessionID, target, input); err != nil {
		return err
	}
	var editable bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
      const el = document.activeElement;
      if (!el) return false;
      const tag = (el.tagName || "").toLowerCase();
      return !!(el.isContentEditable || tag === "textarea" || (tag === "input" && !["button","checkbox","file","hidden","image","radio","range","reset","submit"].includes((el.type || "").toLowerCase())));
    })()`, &editable)); err != nil {
		return err
	}
	if !editable {
		return ErrInvalidRequest
	}
	profile := defaultBehaviorProfile()
	before, after := planTextInputKeys(input.Clear, input.Submit)
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		for _, key := range before {
			if err := keyEventAction(key).Do(ctx); err != nil {
				return err
			}
			if err := sleepBehavior(ctx, profile.KeyAfterDelay); err != nil {
				return err
			}
		}
		parts := splitTextRunes(input.Text)
		for index, part := range parts {
			if err := cdinput.InsertText(part).Do(ctx); err != nil {
				return err
			}
			if index < len(parts)-1 {
				if err := sleepBehavior(ctx, profile.TypeRuneDelay); err != nil {
					return err
				}
			}
		}
		for _, key := range after {
			if err := sleepBehavior(ctx, profile.KeyAfterDelay); err != nil {
				return err
			}
			if err := keyEventAction(key).Do(ctx); err != nil {
				return err
			}
		}
		return nil
	}))
}

func (s *Service) trustedClick(ctx context.Context, runtimeSessionID string, selector string, input ActInput) error {
	target, err := queryTargetBySelector(ctx, selector, false)
	if err != nil {
		return err
	}
	return s.trustedClickTarget(ctx, runtimeSessionID, target, input)
}

func (s *Service) trustedClickPoint(ctx context.Context, runtimeSessionID string, input ActInput) error {
	target := browserTarget{
		Rect: targetRect{
			X:      input.X - 0.5,
			Y:      input.Y - 0.5,
			Width:  1,
			Height: 1,
		},
	}
	return s.trustedClickTarget(ctx, runtimeSessionID, target, input)
}

func (s *Service) trustedScroll(ctx context.Context, runtimeSessionID string, input ActInput) error {
	if input.DeltaX == 0 && input.DeltaY == 0 {
		return ErrInvalidRequest
	}
	viewport := viewportRect{Width: 1366, Height: 768}
	_ = chromedp.Run(ctx, chromedp.Evaluate(runtimeViewportRectScript(1366, 768), &viewport))
	x := input.X
	y := input.Y
	if x <= 0 {
		x = viewport.Width / 2
	}
	if y <= 0 {
		y = viewport.Height / 2
	}
	target := browserTarget{
		Rect: targetRect{
			X:      x - 0.5,
			Y:      y - 0.5,
			Width:  1,
			Height: 1,
		},
	}
	end, viewport, err := s.trustedMoveTarget(ctx, runtimeSessionID, target, input)
	if err != nil {
		return err
	}
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		if err := cdinput.DispatchMouseEvent(cdinput.MouseWheel, end.X, end.Y).
			WithDeltaX(input.DeltaX).
			WithDeltaY(input.DeltaY).
			Do(ctx); err != nil {
			return err
		}
		s.setPointer(runtimeSessionID, end, viewport)
		return nil
	}))
}

func (s *Service) trustedPaste(ctx context.Context, runtimeSessionID string, input ActInput) error {
	if strings.TrimSpace(input.Text) == "" && strings.TrimSpace(input.HTML) == "" {
		return ErrInvalidRequest
	}
	var target browserTarget
	if strings.TrimSpace(input.Ref) != "" {
		resolved, err := s.resolveActRef(ctx, runtimeSessionID, input.Ref)
		if err != nil {
			return err
		}
		target = resolved
	} else if input.X > 0 && input.Y > 0 {
		target = browserTarget{
			Rect: targetRect{
				X:      input.X - 0.5,
				Y:      input.Y - 0.5,
				Width:  1,
				Height: 1,
			},
		}
	} else {
		return ErrInvalidRequest
	}
	if err := s.trustedClickTarget(ctx, runtimeSessionID, target, input); err != nil {
		return err
	}
	if input.Clear {
		for _, key := range []textInputKey{
			{Key: "a", Modifiers: cdinput.ModifierCtrl},
			{Key: kb.Backspace},
		} {
			if err := keyEventAction(key).Do(ctx); err != nil {
				return err
			}
			if err := sleepBehavior(ctx, defaultBehaviorProfile().KeyAfterDelay); err != nil {
				return err
			}
		}
	}
	if strings.TrimSpace(input.HTML) == "" && strings.TrimSpace(input.Text) != "" {
		return trustedInsertText(ctx, input.Text)
	}
	if err := s.writeClipboard(ctx, input.Text, input.HTML); err != nil {
		return err
	}
	return keyEventAction(textInputKey{Key: "v", Modifiers: cdinput.ModifierCtrl}).Do(ctx)
}

func (s *Service) writeClipboard(ctx context.Context, text string, html string) error {
	var currentURL string
	_ = chromedp.Run(ctx, chromedp.Location(&currentURL))
	if origin := clipboardOrigin(currentURL); origin != "" {
		if chromeCtx := chromedp.FromContext(ctx); chromeCtx != nil && chromeCtx.Browser != nil {
			_ = cdbrowser.GrantPermissions([]cdbrowser.PermissionType{
				cdbrowser.PermissionTypeClipboardReadWrite,
				cdbrowser.PermissionTypeClipboardSanitizedWrite,
			}).WithOrigin(origin).Do(cdp.WithExecutor(ctx, chromeCtx.Browser))
		}
	}
	textJSON, err := json.Marshal(text)
	if err != nil {
		return err
	}
	htmlJSON, err := json.Marshal(html)
	if err != nil {
		return err
	}
	script := fmt.Sprintf(`(async () => {
      const text = %s;
      const html = %s;
      if (!navigator.clipboard) throw new Error("clipboard unavailable");
      if (html && typeof ClipboardItem !== "undefined") {
        const item = new ClipboardItem({
          "text/plain": new Blob([text || ""], {type: "text/plain"}),
          "text/html": new Blob([html], {type: "text/html"})
        });
        await navigator.clipboard.write([item]);
        return true;
      }
      await navigator.clipboard.writeText(text || html);
      return true;
    })()`, string(textJSON), string(htmlJSON))
	var ok bool
	return chromedp.Run(ctx, chromedp.Evaluate(script, &ok, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true)
	}))
}

func trustedInsertText(ctx context.Context, text string) error {
	profile := defaultBehaviorProfile()
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		parts := splitTextRunes(text)
		for index, part := range parts {
			if err := cdinput.InsertText(part).Do(ctx); err != nil {
				return err
			}
			if index < len(parts)-1 {
				if err := sleepBehavior(ctx, profile.TypeRuneDelay); err != nil {
					return err
				}
			}
		}
		return nil
	}))
}

func clipboardOrigin(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func (s *Service) trustedClickTarget(ctx context.Context, runtimeSessionID string, target browserTarget, input ActInput) error {
	profile := defaultBehaviorProfile()
	end, viewport, err := s.trustedMoveTarget(ctx, runtimeSessionID, target, input)
	if err != nil {
		return err
	}
	button := cdinput.Left
	switch input.Button {
	case "right":
		button = cdinput.Right
	case "middle":
		button = cdinput.Middle
	}
	clickCount := int64(input.ClickCount)
	if clickCount <= 0 {
		clickCount = 1
	}
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		if err := sleepBehavior(ctx, profile.MouseBeforeDownDelay); err != nil {
			return err
		}
		if err := cdinput.DispatchMouseEvent(cdinput.MousePressed, end.X, end.Y).WithButton(button).WithButtons(mouseButtonsBitfield(button)).WithClickCount(clickCount).Do(ctx); err != nil {
			return err
		}
		s.setPointerButton(runtimeSessionID, end, viewport, true)
		if err := sleepBehavior(ctx, profile.MouseDownUpDelay); err != nil {
			return err
		}
		if err := cdinput.DispatchMouseEvent(cdinput.MouseReleased, end.X, end.Y).WithButton(button).WithButtons(0).WithClickCount(clickCount).Do(ctx); err != nil {
			return err
		}
		s.setPointerButton(runtimeSessionID, end, viewport, false)
		return nil
	})); err != nil {
		return err
	}
	return nil
}

func mouseButtonsBitfield(button cdinput.MouseButton) int64 {
	switch button {
	case cdinput.Left:
		return 1
	case cdinput.Right:
		return 2
	case cdinput.Middle:
		return 4
	default:
		return 0
	}
}

const trustedMouseMoveDispatchIntervalPX = 24.0

func shouldDispatchTrustedMouseMove(lastDispatched pointerPoint, point pointerPoint, final bool) bool {
	if final {
		return true
	}
	return math.Hypot(point.X-lastDispatched.X, point.Y-lastDispatched.Y) >= trustedMouseMoveDispatchIntervalPX
}

func (s *Service) trustedMoveTarget(ctx context.Context, runtimeSessionID string, target browserTarget, input ActInput) (pointerPoint, viewportRect, error) {
	profile := defaultBehaviorProfile()
	viewport := viewportRect{Width: 1366, Height: 768}
	_ = chromedp.Run(ctx, chromedp.Evaluate(runtimeViewportRectScript(1366, 768), &viewport))
	start := s.pointerStart(runtimeSessionID, viewport)
	path := planPointerPath(start, target.Rect, viewport, input.MotionProfile, profile.MouseMoveStepDelay)
	if len(path) == 0 {
		return pointerPoint{}, viewportRect{}, ErrInvalidRequest
	}
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		lastDispatched := start
		for index, point := range path {
			final := index == len(path)-1
			if shouldDispatchTrustedMouseMove(lastDispatched, point, final) {
				if err := cdinput.DispatchMouseEvent(cdinput.MouseMoved, point.X, point.Y).WithButton(cdinput.None).Do(ctx); err != nil {
					return err
				}
				lastDispatched = point
			}
			s.setPointer(runtimeSessionID, point, viewport)
			if err := sleepBehavior(ctx, profile.MouseMoveStepDelay); err != nil {
				return err
			}
		}
		return nil
	})); err != nil {
		return pointerPoint{}, viewportRect{}, err
	}
	end := path[len(path)-1]
	s.setPointer(runtimeSessionID, end, viewport)
	return end, viewport, nil
}

func runtimeViewportRectScript(fallbackWidth float64, fallbackHeight float64) string {
	return fmt.Sprintf(`(() => {
      const innerWidth = window.innerWidth || %f;
      const innerHeight = window.innerHeight || %f;
      const outerWidth = window.outerWidth || innerWidth;
      const outerHeight = window.outerHeight || innerHeight;
      const screenX = window.screenX || window.screenLeft || 0;
      const screenY = window.screenY || window.screenTop || 0;
      const sideChrome = Math.max(0, (outerWidth - innerWidth) / 2);
      const topChrome = Math.max(0, outerHeight - innerHeight);
      return {
        width: innerWidth,
        height: innerHeight,
        contentOffsetX: Math.max(0, screenX + sideChrome),
        contentOffsetY: Math.max(0, screenY + topChrome)
      };
    })()`, fallbackWidth, fallbackHeight)
}

func (s *Service) resolveActRef(ctx context.Context, runtimeSessionID string, ref string) (browserTarget, error) {
	if ref == "" {
		return browserTarget{}, ErrInvalidRequest
	}
	refState, err := s.state.GetRef(runtimeSessionID, ref)
	if err != nil {
		return browserTarget{}, err
	}
	if refState.Kind != "element" {
		return browserTarget{}, browserrt.ErrInvalidRef
	}
	return queryTargetBySelector(ctx, refState.Selector, false)
}

func queryTargetBySelector(ctx context.Context, selector string, requireUnique bool) (browserTarget, error) {
	var out browserTarget
	script := fmt.Sprintf(`(() => {
      const selector = %q;
      const visible = (el) => {
        const r = el.getBoundingClientRect();
        const style = window.getComputedStyle(el);
        return r.width > 0 && r.height > 0 && style.visibility !== "hidden" && style.display !== "none";
      };
      const matches = Array.from(document.querySelectorAll(selector)).filter(visible);
      if (%t && matches.length !== 1) throw new Error("target ambiguous");
      const el = matches[0] || document.querySelector(selector);
      if (!el) throw new Error("missing");
      el.scrollIntoView({block:"center", inline:"center"});
      const r = el.getBoundingClientRect();
      const tag = (el.tagName || "").toLowerCase();
      return {
        selector,
        rect: { x: r.left, y: r.top, width: r.width, height: r.height },
        editable: !!(el.isContentEditable || tag === "textarea" || (tag === "input" && !["button","checkbox","file","hidden","image","radio","range","reset","submit"].includes((el.type || "").toLowerCase())))
      };
    })()`, selector, requireUnique)
	if err := chromedp.Run(ctx, chromedp.Evaluate(script, &out)); err != nil {
		return browserTarget{}, err
	}
	return out, nil
}

func (s *Service) pointerStart(runtimeSessionID string, viewport viewportRect) pointerPoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.pointers[runtimeSessionID]
	if ok && state.Initialized {
		return clampPoint(state.Point, viewport)
	}
	return pointerPoint{X: viewport.Width * 0.35, Y: viewport.Height * 0.35}
}

func (s *Service) initializePointerCenter(runtimeSessionID string, fp FingerprintConfig) {
	viewport := viewportRect{Width: float64(fp.ViewportWidth), Height: float64(fp.ViewportHeight)}
	s.initializePointerCenterViewport(runtimeSessionID, viewport)
}

func (s *Service) initializePointerCenterViewport(runtimeSessionID string, viewport viewportRect) {
	if viewport.Width <= 0 || viewport.Height <= 0 {
		return
	}
	s.setPointer(runtimeSessionID, pointerPoint{X: viewport.Width / 2, Y: viewport.Height / 2}, viewport)
}

func (s *Service) PointerSnapshot(runtimeSessionID string) (VirtualPointerSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.pointers[runtimeSessionID]
	if !ok || !state.Initialized {
		return VirtualPointerSnapshot{Visible: false}, false
	}
	return newVirtualPointerSnapshot(runtimeSessionID, state), true
}

func (s *Service) SubscribePointer(runtimeSessionID string) (PointerSubscription, error) {
	if strings.TrimSpace(runtimeSessionID) == "" {
		return PointerSubscription{}, ErrInvalidRequest
	}
	ch := make(chan VirtualPointerSnapshot, 16)
	s.mu.Lock()
	if s.pointerSubscribers[runtimeSessionID] == nil {
		s.pointerSubscribers[runtimeSessionID] = map[chan VirtualPointerSnapshot]struct{}{}
	}
	s.pointerSubscribers[runtimeSessionID][ch] = struct{}{}
	if state, ok := s.pointers[runtimeSessionID]; ok && state.Initialized {
		ch <- newVirtualPointerSnapshot(runtimeSessionID, state)
	}
	s.mu.Unlock()
	return PointerSubscription{
		C: ch,
		Cancel: func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if _, ok := s.pointerSubscribers[runtimeSessionID][ch]; ok {
				delete(s.pointerSubscribers[runtimeSessionID], ch)
				close(ch)
			}
		},
	}, nil
}

func (s *Service) setPointer(runtimeSessionID string, point pointerPoint, viewport viewportRect) {
	s.setPointerButton(runtimeSessionID, point, viewport, false)
}

func (s *Service) setPointerButton(runtimeSessionID string, point pointerPoint, viewport viewportRect, buttonDown bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := pointerState{
		Point:       point,
		Viewport:    viewport,
		Initialized: true,
		ButtonDown:  buttonDown,
	}
	s.pointers[runtimeSessionID] = state
	snapshot := newVirtualPointerSnapshot(runtimeSessionID, state)
	for ch := range s.pointerSubscribers[runtimeSessionID] {
		select {
		case ch <- snapshot:
		default:
		}
	}
}

func (s *Service) UploadFiles(runtimeSessionID string, input UploadFilesInput) (UploadFilesOutput, error) {
	ref := strings.TrimSpace(input.Ref)
	hasPoint := input.X > 0 && input.Y > 0
	if (ref == "" && !hasPoint) || len(input.Files) == 0 {
		return UploadFilesOutput{}, ErrInvalidRequest
	}
	if s.sessions == nil || s.state == nil {
		return UploadFilesOutput{}, ErrInvalidRequest
	}
	var refState browserrt.RefState
	isFileInput := false
	if ref != "" {
		var err error
		refState, err = s.state.GetRef(runtimeSessionID, ref)
		if err != nil {
			return UploadFilesOutput{}, err
		}
		isFileInput = refState.Kind == "element" && strings.ToLower(strings.TrimSpace(refState.TagName)) == "input"
		if !isFileInput && refState.Kind != "text" && refState.Kind != "element" {
			return UploadFilesOutput{}, browserrt.ErrInvalidRef
		}
	}
	info, err := s.sessions.Get(runtimeSessionID)
	if err != nil {
		return UploadFilesOutput{}, err
	}
	uploadDir := filepath.Join(filepath.Dir(info.ProfileDir), "uploads")
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		return UploadFilesOutput{}, err
	}
	filePaths := make([]string, 0, len(input.Files))
	fileNames := make([]string, 0, len(input.Files))
	for index, source := range input.Files {
		path, name, err := s.materializeUploadSource(context.Background(), uploadDir, index, source)
		if err != nil {
			return UploadFilesOutput{}, err
		}
		filePaths = append(filePaths, path)
		fileNames = append(fileNames, name)
	}
	if ref == "" {
		setter := s.setFilesAtPoint
		if setter == nil {
			setter = s.defaultSetFilesAtPoint
		}
		if err := setter(runtimeSessionID, input.X, input.Y, filePaths, input.TimeoutMs); err != nil {
			return UploadFilesOutput{}, fmt.Errorf("%w: %v", ErrUploadFilesFailed, err)
		}
	} else if isFileInput {
		setter := s.setFileInputFiles
		if setter == nil {
			setter = s.defaultSetFileInputFiles
		}
		if err := setter(runtimeSessionID, refState.Selector, filePaths, input.TimeoutMs); err != nil {
			return UploadFilesOutput{}, fmt.Errorf("%w: %v", ErrUploadFilesFailed, err)
		}
	} else {
		dropper := s.dropFilesOnTarget
		if dropper == nil {
			dropper = s.defaultDropFilesOnTarget
		}
		if err := dropper(runtimeSessionID, refState.Selector, filePaths, input.TimeoutMs); err != nil {
			return UploadFilesOutput{}, fmt.Errorf("%w: %v", ErrUploadFilesFailed, err)
		}
	}
	return UploadFilesOutput{OK: true, Ref: ref, FileNames: fileNames}, nil
}

func (s *Service) materializeUploadSource(ctx context.Context, uploadDir string, index int, source UploadFileSource) (string, string, error) {
	s3Path := strings.TrimSpace(source.S3Path)
	localPath := strings.TrimSpace(source.LocalPath)
	sourceURL := strings.TrimSpace(source.URL)
	sourceCount := 0
	for _, value := range []string{s3Path, localPath, sourceURL} {
		if value != "" {
			sourceCount++
		}
	}
	if sourceCount != 1 {
		return "", "", ErrInvalidRequest
	}
	name := sanitizeUploadFilename(source.Filename)
	if name == "" {
		switch {
		case s3Path != "":
			name = sanitizeUploadFilename(filepath.Base(strings.TrimPrefix(s3Path, "s3://")))
		case localPath != "":
			name = sanitizeUploadFilename(filepath.Base(localPath))
		case sourceURL != "":
			parsed, err := url.Parse(sourceURL)
			if err != nil {
				return "", "", ErrInvalidRequest
			}
			name = sanitizeUploadFilename(filepath.Base(parsed.Path))
		}
	}
	if name == "" || name == "." {
		name = fmt.Sprintf("upload-%d.bin", index+1)
	}
	if s3Path != "" {
		if s.assets == nil {
			return "", "", ErrInvalidRequest
		}
		body, _, err := s.assets.Get(ctx, s3Path)
		if err != nil {
			return "", "", err
		}
		outPath := filepath.Join(uploadDir, name)
		if err := os.WriteFile(outPath, body, 0o600); err != nil {
			return "", "", err
		}
		return outPath, name, nil
	}
	if sourceURL != "" {
		parsed, err := url.Parse(sourceURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return "", "", ErrInvalidRequest
		}
		client := newUploadHTTPClient()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
		if err != nil {
			return "", "", ErrInvalidRequest
		}
		res, err := client.Do(req)
		if err != nil {
			return "", "", fmt.Errorf("%w: GET %s failed: %v", ErrUploadSourceFetchFailed, uploadSourceURLForError(sourceURL), err)
		}
		defer func() {
			_ = res.Body.Close()
		}()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
			detail := strings.TrimSpace(string(body))
			if detail == "" {
				detail = res.Status
			}
			return "", "", fmt.Errorf("%w: GET %s returned HTTP %d: %s", ErrUploadSourceFetchFailed, uploadSourceURLForError(sourceURL), res.StatusCode, detail)
		}
		outPath := filepath.Join(uploadDir, name)
		out, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return "", "", err
		}
		defer func() {
			_ = out.Close()
		}()
		if _, err := io.Copy(out, res.Body); err != nil {
			return "", "", err
		}
		return outPath, name, nil
	}
	cleanLocal, err := filepath.Abs(localPath)
	if err != nil {
		return "", "", ErrInvalidRequest
	}
	allowedRoot, err := filepath.Abs(uploadDir)
	if err != nil {
		return "", "", ErrInvalidRequest
	}
	rel, err := filepath.Rel(allowedRoot, cleanLocal)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", "", ErrInvalidRequest
	}
	return cleanLocal, name, nil
}

func uploadSourceURLForError(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func sanitizeUploadFilename(value string) string {
	name := filepath.Base(strings.TrimSpace(value))
	name = strings.ReplaceAll(name, string(os.PathSeparator), "_")
	name = strings.TrimSpace(name)
	if name == "." || name == string(os.PathSeparator) {
		return ""
	}
	return name
}

func newUploadHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = func(req *http.Request) (*url.URL, error) {
		if shouldBypassUploadURLProxy(req.URL) {
			return nil, nil
		}
		return http.ProxyFromEnvironment(req)
	}
	return &http.Client{Timeout: 2 * time.Minute, Transport: transport}
}

func shouldBypassUploadURLProxy(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host == "localhost" || host == "host.docker.internal" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Service) defaultSetFileInputFiles(runtimeSessionID string, selector string, filePaths []string, timeoutMs int) error {
	ctx, cancel, err := s.newBrowserContext(runtimeSessionID, timeoutMs)
	if err != nil {
		return err
	}
	defer cancel()

	chooserCh := make(chan *page.EventFileChooserOpened, 1)
	listenCtx, stopListening := context.WithCancel(ctx)
	defer stopListening()
	chromedp.ListenTarget(listenCtx, func(ev any) {
		chooser, ok := ev.(*page.EventFileChooserOpened)
		if !ok {
			return
		}
		select {
		case chooserCh <- chooser:
		default:
		}
	})

	if err := chromedp.Run(ctx, page.SetInterceptFileChooserDialog(true)); err != nil {
		return err
	}
	defer func() {
		_ = chromedp.Run(ctx, page.SetInterceptFileChooserDialog(false))
	}()

	if err := s.trustedClick(ctx, runtimeSessionID, selector, ActInput{TimeoutMs: timeoutMs}); err != nil {
		return fmt.Errorf("open file chooser: %w", err)
	}
	select {
	case chooser := <-chooserCh:
		if chooser.BackendNodeID == 0 {
			return errors.New("file chooser opened without backend node")
		}
		return chromedp.Run(ctx, dom.SetFileInputFiles(filePaths).WithBackendNodeID(chooser.BackendNodeID))
	case <-ctx.Done():
		return fmt.Errorf("file chooser not opened: %w", ctx.Err())
	}
}

func (s *Service) defaultSetFilesAtPoint(runtimeSessionID string, x float64, y float64, filePaths []string, timeoutMs int) error {
	ctx, cancel, err := s.newBrowserContext(runtimeSessionID, timeoutMs)
	if err != nil {
		return err
	}
	defer cancel()

	chooserCh := make(chan *page.EventFileChooserOpened, 1)
	listenCtx, stopListening := context.WithCancel(ctx)
	defer stopListening()
	chromedp.ListenTarget(listenCtx, func(ev any) {
		chooser, ok := ev.(*page.EventFileChooserOpened)
		if !ok {
			return
		}
		select {
		case chooserCh <- chooser:
		default:
		}
	})

	if err := chromedp.Run(ctx, page.SetInterceptFileChooserDialog(true)); err != nil {
		return err
	}
	defer func() {
		_ = chromedp.Run(ctx, page.SetInterceptFileChooserDialog(false))
	}()

	if err := s.trustedClickPoint(ctx, runtimeSessionID, ActInput{X: x, Y: y, TimeoutMs: timeoutMs}); err != nil {
		return fmt.Errorf("open file chooser: %w", err)
	}
	select {
	case chooser := <-chooserCh:
		if chooser.BackendNodeID == 0 {
			return errors.New("file chooser opened without backend node")
		}
		return chromedp.Run(ctx, dom.SetFileInputFiles(filePaths).WithBackendNodeID(chooser.BackendNodeID))
	case <-ctx.Done():
		return fmt.Errorf("file chooser not opened: %w", ctx.Err())
	}
}

func (s *Service) defaultDropFilesOnTarget(runtimeSessionID string, selector string, filePaths []string, timeoutMs int) error {
	ctx, cancel, err := s.newBrowserContext(runtimeSessionID, timeoutMs)
	if err != nil {
		return err
	}
	defer cancel()

	target, err := queryTargetBySelector(ctx, selector, false)
	if err != nil {
		return err
	}
	point, _, err := s.trustedMoveTarget(ctx, runtimeSessionID, target, ActInput{TimeoutMs: timeoutMs})
	if err != nil {
		return err
	}
	data := &cdinput.DragData{
		Items:              []*cdinput.DragDataItem{},
		Files:              filePaths,
		DragOperationsMask: 1,
	}
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		if err := cdinput.DispatchDragEvent(cdinput.DragEnter, point.X, point.Y, data).Do(ctx); err != nil {
			return err
		}
		if err := cdinput.DispatchDragEvent(cdinput.DragOver, point.X, point.Y, data).Do(ctx); err != nil {
			return err
		}
		return cdinput.DispatchDragEvent(cdinput.Drop, point.X, point.Y, data).Do(ctx)
	}))
}

func pageGroupsToState(groups map[string]PageTable) map[string]any {
	out := make(map[string]any, len(groups))
	for key, table := range groups {
		out[key] = table
	}
	return out
}

const (
	screenshotModeViewport = "viewport"
	screenshotModeFullPage = "fullpage"
	screenshotModeSelector = "selector"
)

func (s *Service) Screenshot(runtimeSessionID string, input ScreenshotInput) (ScreenshotOutput, error) {
	ctx, cancel, err := s.newBrowserContext(runtimeSessionID, 20_000)
	if err != nil {
		return ScreenshotOutput{}, err
	}
	defer cancel()

	format := strings.ToLower(strings.TrimSpace(input.Format))
	if format == "" {
		format = "png"
	}
	ext, contentType, err := screenshotExt(format)
	if err != nil {
		return ScreenshotOutput{}, ErrInvalidRequest
	}

	mode, err := normalizeScreenshotMode(input)
	if err != nil {
		return ScreenshotOutput{}, err
	}

	var buf []byte
	switch mode {
	case screenshotModeSelector:
		selector := strings.TrimSpace(input.Selector)
		if _, err := queryTargetBySelector(ctx, selector, true); err != nil {
			return ScreenshotOutput{}, fmt.Errorf("%w: selector must match exactly one visible element: %v", ErrInvalidRequest, err)
		}
		if err := chromedp.Run(ctx, chromedp.Screenshot(selector, &buf, chromedp.ByQuery)); err != nil {
			return ScreenshotOutput{}, fmt.Errorf("%w: %v", ErrScreenshotFailed, err)
		}
	case screenshotModeFullPage:
		if err := chromedp.Run(ctx, chromedp.FullScreenshot(&buf, 100)); err != nil {
			return ScreenshotOutput{}, fmt.Errorf("%w: %v", ErrScreenshotFailed, err)
		}
	case screenshotModeViewport:
		if err := chromedp.Run(ctx, chromedp.CaptureScreenshot(&buf)); err != nil {
			return ScreenshotOutput{}, fmt.Errorf("%w: %v", ErrScreenshotFailed, err)
		}
	}

	return s.persistScreenshot(ctx, input, buf, ext, contentType)
}

func normalizeScreenshotMode(input ScreenshotInput) (string, error) {
	if strings.TrimSpace(input.Ref) != "" {
		return "", fmt.Errorf("%w: ref is not supported for screenshot; use mode selector with selector", ErrInvalidRequest)
	}
	if input.FullPage {
		return "", fmt.Errorf("%w: fullPage is not supported for screenshot; use mode fullpage", ErrInvalidRequest)
	}
	mode := strings.TrimSpace(input.Mode)
	if mode == "" {
		mode = screenshotModeViewport
	}
	selector := strings.TrimSpace(input.Selector)
	switch mode {
	case screenshotModeViewport, screenshotModeFullPage:
		if selector != "" {
			return "", fmt.Errorf("%w: selector is only supported when mode is selector", ErrInvalidRequest)
		}
	case screenshotModeSelector:
		if selector == "" {
			return "", fmt.Errorf("%w: selector is required when mode is selector", ErrInvalidRequest)
		}
	default:
		return "", fmt.Errorf("%w: unsupported screenshot mode %q; use viewport, fullpage, or selector", ErrInvalidRequest, mode)
	}
	return mode, nil
}

func screenshotExt(format string) (string, string, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "png":
		return "png", "image/png", nil
	default:
		return "", "", ErrInvalidRequest
	}
}

func newScreenshotID(ext string) (string, error) {
	ext = strings.TrimPrefix(strings.TrimSpace(ext), ".")
	if ext == "" {
		return "", ErrInvalidRequest
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x.%s", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16], ext), nil
}

func (s *Service) persistScreenshot(ctx context.Context, input ScreenshotInput, body []byte, ext string, contentType string) (ScreenshotOutput, error) {
	prefix := strings.TrimSpace(input.ScreenshotS3Prefix)
	if prefix == "" || strings.Contains(prefix, "://") || !strings.HasPrefix(prefix, "/") || !strings.HasSuffix(prefix, "/") {
		return ScreenshotOutput{}, ErrInvalidRequest
	}
	if s.assets == nil {
		return ScreenshotOutput{}, fmt.Errorf("asset store not configured")
	}
	bucket := strings.TrimSpace(s.assetBucket)
	if bucket == "" {
		return ScreenshotOutput{}, fmt.Errorf("asset bucket not configured")
	}
	if len(body) == 0 {
		return ScreenshotOutput{}, ErrScreenshotFailed
	}
	if !validScreenshotBytes(body, contentType) {
		return ScreenshotOutput{}, fmt.Errorf("%w: screenshot bytes do not match %s", ErrScreenshotFailed, contentType)
	}
	screenshotID, err := newScreenshotID(ext)
	if err != nil {
		return ScreenshotOutput{}, err
	}
	s3Path := prefix + screenshotID
	uploadURI := "s3://" + bucket + strings.TrimRight(s3Path, "/")
	if err := s.assets.Put(ctx, uploadURI, body, contentType); err != nil {
		return ScreenshotOutput{}, err
	}
	return ScreenshotOutput{
		ScreenshotID: screenshotID,
		S3Path:       s3Path,
		ContentType:  contentType,
		ByteLength:   len(body),
	}, nil
}

func validScreenshotBytes(body []byte, contentType string) bool {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/png":
		return bytes.HasPrefix(body, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	default:
		return false
	}
}

func (s *Service) Evaluate(runtimeSessionID string, input EvaluateInput) (EvaluateOutput, error) {
	if input.PageToolBridge != nil && input.PageToolBridge.Enabled {
		return s.evaluateWithPageToolBridge(runtimeSessionID, input)
	}
	if strings.TrimSpace(input.Script) == "" {
		return EvaluateOutput{}, ErrInvalidRequest
	}
	world := strings.TrimSpace(input.World)
	if world != "" && world != "MAIN" {
		return EvaluateOutput{}, fmt.Errorf("%w: browserd evaluate only supports MAIN world; omit world or use MAIN", ErrInvalidRequest)
	}
	argsJSON, err := json.Marshal(input.Args)
	if err != nil {
		return EvaluateOutput{}, fmt.Errorf("%w: args must be JSON serializable", ErrInvalidRequest)
	}
	ctx, cancel, err := s.newBrowserContext(runtimeSessionID, input.TimeoutMs)
	if err != nil {
		return EvaluateOutput{}, err
	}
	defer cancel()

	var out EvaluateOutput
	if err := chromedp.Run(ctx, chromedp.Evaluate(browserEvaluateRuntimeScript(input.Script, string(argsJSON)), &out, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true)
	})); err != nil {
		return EvaluateOutput{}, fmt.Errorf("%w: %v", ErrEvaluateFailed, err)
	}
	return out, nil
}

func browserEvaluateRuntimeScript(script string, argsJSON string) string {
	return fmt.Sprintf(`(async () => {
  const args = %s;
  const raw = await (async () => { %s })();
  const result = raw === undefined ? null : raw;
  const guard = findNonJsonValue(result, "$", new Set());
  if (guard) throw new Error("EVALUATE_RESULT_NOT_JSON: " + guard);
  return {
    result: JSON.parse(JSON.stringify(result)),
    url: document.location.href,
    title: document.title
  };

  function findNonJsonValue(value, path, seen) {
    if (value === null) return null;
    const valueType = typeof value;
    if (valueType === "function" || valueType === "symbol" || valueType === "bigint" || valueType === "undefined") return path + " is " + valueType;
    if (valueType !== "object") return null;
    if (value instanceof Node) return path + " is DOM Node";
    if (seen.has(value)) return path + " is circular";
    seen.add(value);
    if (Array.isArray(value)) {
      for (let i = 0; i < value.length; i += 1) {
        const nested = findNonJsonValue(value[i], path + "[" + i + "]", seen);
        if (nested) return nested;
      }
      return null;
    }
    for (const key of Object.keys(value)) {
      const nested = findNonJsonValue(value[key], path + "." + key, seen);
      if (nested) return nested;
    }
    return null;
  }
})()`, argsJSON, script)
}

func (s *Service) newBrowserContext(runtimeSessionID string, timeoutMs int) (context.Context, context.CancelFunc, error) {
	b, err := s.ensureBrowser(runtimeSessionID)
	if err != nil {
		return nil, nil, err
	}
	if timeoutMs <= 0 {
		timeoutMs = 20_000
	}
	taskCtx, taskCancel := context.WithTimeout(b.pageCtx, time.Duration(timeoutMs)*time.Millisecond)
	cancel := func() {
		taskCancel()
	}
	return taskCtx, cancel, nil
}

func (s *Service) ensureBrowser(runtimeSessionID string) (*activeBrowser, error) {
	s.mu.Lock()
	b, ok := s.browsers[runtimeSessionID]
	s.mu.Unlock()
	if ok && b != nil && b.wsURL != "" && b.cmd != nil && b.cmd.Process != nil && b.pageCtx != nil {
		return b, nil
	}

	info, err := s.sessions.Get(runtimeSessionID)
	if err != nil {
		return nil, err
	}
	fp := info.Fingerprint
	proxy, err := ParseProxyServer(info.ProxyServer)
	if err != nil {
		return nil, err
	}
	chromeBin := strings.TrimSpace(os.Getenv("CHROME_BIN"))
	if chromeBin == "" {
		chromeBin = "/usr/bin/chromium-browser"
	}
	if _, err := os.Stat(chromeBin); err != nil {
		return nil, ErrPlaywrightUnavailable
	}

	liveEnabled := liveModeEnabled()
	var liveRuntime *LiveRuntime
	var chromeEnv []string
	if liveEnabled {
		var err error
		screenWidth, screenHeight, err := liveRuntimeDimensionsFromFingerprint(fp)
		if err != nil {
			return nil, err
		}
		liveRuntime, err = NewLiveRuntime(sessionRootFromProfileDir(info.ProfileDir), screenWidth, screenHeight)
		if err != nil {
			return nil, err
		}
		if err := liveRuntime.Start(context.Background()); err != nil {
			return nil, err
		}
		chromeEnv = liveRuntime.ChromeEnv()
	}

	var proxyAdapter *localProxyAdapter
	proxyOverride := ""
	useProxyHop, err := s.proxyHopMode(proxy)
	if err != nil {
		if liveRuntime != nil {
			_ = liveRuntime.Stop(context.Background())
		}
		return nil, err
	}
	if useProxyHop {
		proxyAdapter = newLocalProxyAdapter(localProxyAdapterOptions{
			RuntimeSessionID: runtimeSessionID,
			UpstreamProxy:    proxy,
			WorkerURL:        s.proxyHop.WorkerURL,
			WorkerToken:      s.proxyHop.WorkerToken,
		})
		if err := proxyAdapter.Start(context.Background()); err != nil {
			if liveRuntime != nil {
				_ = liveRuntime.Stop(context.Background())
			}
			return nil, fmt.Errorf("%w: proxy hop adapter start failed: %v", ErrProxyHopFailed, err)
		}
		proxyOverride = proxyAdapter.ChromeProxyServer()
	}

	cmd := exec.Command(chromeBin, buildChromeArgs(BrowserOptions{
		ProfileTransfer:     info.ProfileTransfer,
		UserDataDir:         info.ProfileDir,
		Headless:            !liveEnabled,
		KioskMode:           liveEnabled,
		Fingerprint:         fp,
		Proxy:               proxy,
		ProxyOverrideServer: proxyOverride,
	})...)
	cmd.Env = append(cmd.Environ(), chromeEnv...)
	if err := cmd.Start(); err != nil {
		if liveRuntime != nil {
			_ = liveRuntime.Stop(context.Background())
		}
		if proxyAdapter != nil {
			_ = proxyAdapter.Close()
		}
		return nil, err
	}

	wsURL, err := waitForDevToolsWS(info.ProfileDir, 5*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		if liveRuntime != nil {
			_ = liveRuntime.Stop(context.Background())
		}
		if proxyAdapter != nil {
			_ = proxyAdapter.Close()
		}
		return nil, err
	}

	rootCtx, rootCancel := context.WithCancel(context.Background())
	allocCtx, allocCancel := chromedp.NewRemoteAllocator(rootCtx, wsURL)
	pageCtx, pageCancel := chromedp.NewContext(allocCtx, chromedp.WithErrorf(chromedpErrorLogger(func(format string, args ...any) {
		log.Printf("ERROR: "+format, args...)
	})))
	if err := chromedp.Run(pageCtx); err != nil {
		pageCancel()
		allocCancel()
		rootCancel()
		_ = cmd.Process.Kill()
		if liveRuntime != nil {
			_ = liveRuntime.Stop(context.Background())
		}
		if proxyAdapter != nil {
			_ = proxyAdapter.Close()
		}
		return nil, err
	}
	if err := applyRuntimeOptions(pageCtx, fp, proxy); err != nil {
		pageCancel()
		allocCancel()
		rootCancel()
		_ = cmd.Process.Kill()
		if liveRuntime != nil {
			_ = liveRuntime.Stop(context.Background())
		}
		if proxyAdapter != nil {
			_ = proxyAdapter.Close()
		}
		return nil, err
	}
	if err := s.installImageRequestObserver(runtimeSessionID, pageCtx); err != nil {
		pageCancel()
		allocCancel()
		rootCancel()
		_ = cmd.Process.Kill()
		if liveRuntime != nil {
			_ = liveRuntime.Stop(context.Background())
		}
		if proxyAdapter != nil {
			_ = proxyAdapter.Close()
		}
		return nil, err
	}

	s.mu.Lock()
	ab := &activeBrowser{
		cmd:          cmd,
		live:         liveRuntime,
		wsURL:        wsURL,
		rootCtx:      rootCtx,
		rootCancel:   rootCancel,
		allocCtx:     allocCtx,
		allocCancel:  allocCancel,
		pageCtx:      pageCtx,
		pageCancel:   pageCancel,
		proxyAdapter: proxyAdapter,
	}
	s.browsers[runtimeSessionID] = ab
	s.mu.Unlock()
	initialViewport := viewportRect{Width: float64(fp.ViewportWidth), Height: float64(fp.ViewportHeight)}
	_ = chromedp.Run(pageCtx, chromedp.Evaluate(runtimeViewportRectScript(initialViewport.Width, initialViewport.Height), &initialViewport))
	s.initializePointerCenterViewport(runtimeSessionID, initialViewport)
	return ab, nil
}

func waitForDevToolsWS(profileDir string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	devtoolsFile := filepath.Join(profileDir, "DevToolsActivePort")
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(devtoolsFile)
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if len(lines) >= 2 {
				port := strings.TrimSpace(lines[0])
				path := strings.TrimSpace(lines[1])
				if port != "" && path != "" {
					return "ws://127.0.0.1:" + port + path, nil
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", errors.New("devtools websocket not ready")
}

func jsStringArray(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, fmt.Sprintf("%q", v))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

type BrowserOptions struct {
	ProfileTransfer     bool
	UserDataDir         string
	Headless            bool
	KioskMode           bool
	Fingerprint         FingerprintConfig
	Proxy               ProxyConfig
	ProxyOverrideServer string
}

func buildChromeArgs(opts BrowserOptions) []string {
	args := []string{
		"--disable-gpu",
		"--no-sandbox",
		"--no-first-run",
		"--restore-last-session",
		"--no-default-browser-check",
		"--disable-dev-shm-usage",
		"--remote-debugging-port=0",
		"--force-color-profile=srgb",
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
	}
	if opts.ProfileTransfer {
		args = append(args, "--disable-background-networking", "--disable-extensions", "--proxy-bypass-list=<-loopback>")
	}
	proxyServer := opts.Proxy.ChromeServer
	if opts.ProxyOverrideServer != "" {
		proxyServer = opts.ProxyOverrideServer
	}
	if proxyServer != "" {
		args = append(args, "--proxy-server="+proxyServer)
	}
	if opts.Fingerprint.UserAgent != "" {
		args = append(args, "--user-agent="+opts.Fingerprint.UserAgent)
	}
	if opts.Fingerprint.Locale != "" {
		args = append(args, "--lang="+opts.Fingerprint.Locale)
	}
	if opts.Fingerprint.ScreenWidth > 0 && opts.Fingerprint.ScreenHeight > 0 {
		args = append(args, fmt.Sprintf("--window-size=%d,%d", opts.Fingerprint.ScreenWidth, opts.Fingerprint.ScreenHeight))
	}
	if opts.Headless {
		args = append(args, "--headless=new")
	}
	args = append(args, "--user-data-dir="+opts.UserDataDir)
	if opts.KioskMode && !opts.Headless {
		args = append(args, "--kiosk", "--start-fullscreen")
	}
	args = append(args, "about:blank")
	return args
}

func (s *Service) proxyHopMode(proxy ProxyConfig) (bool, error) {
	if proxy.Raw == "" {
		return false, nil
	}
	if !strings.EqualFold(strings.TrimSpace(s.proxyHop.Mode), "cloudflare-worker") {
		return false, nil
	}
	if strings.TrimSpace(s.proxyHop.WorkerURL) == "" || strings.TrimSpace(s.proxyHop.WorkerToken) == "" {
		return false, ErrProxyHopConfigMissing
	}
	return true, nil
}

func liveModeEnabled() bool {
	value := strings.TrimSpace(os.Getenv("BROWSERD_LIVE_ENABLED"))
	return strings.EqualFold(value, "true") || value == "1"
}

func liveRuntimeDimensionsFromFingerprint(fp FingerprintConfig) (int, int, error) {
	if fp.ScreenWidth <= 0 || fp.ScreenHeight <= 0 {
		return 0, 0, fmt.Errorf("%w: fingerprint screenWidth/screenHeight are required for live runtime, got %dx%d", ErrInvalidRequest, fp.ScreenWidth, fp.ScreenHeight)
	}
	return int(fp.ScreenWidth), int(fp.ScreenHeight), nil
}
