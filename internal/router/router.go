package router

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"browserd/internal/assets"
	"browserd/internal/browser"
	"browserd/internal/config"
	"browserd/internal/controller"
	"browserd/internal/live"
	"browserd/internal/liveviewer"
	"browserd/internal/profile"
	"browserd/internal/runtime"
	"browserd/internal/session"
	"browserd/internal/types"
)

func New(cfg config.Config) http.Handler {
	var store profile.Store
	switch strings.ToLower(strings.TrimSpace(cfg.ProfileStore)) {
	case "s3", "oss":
		s3Store, err := profile.NewS3Store(profile.S3StoreConfig{
			Endpoint:        cfg.S3Endpoint,
			Region:          cfg.S3Region,
			AccessKeyID:     cfg.S3AccessKeyID,
			SecretAccessKey: cfg.S3SecretAccessKey,
			ForcePathStyle:  cfg.S3ForcePathStyle,
			Bucket:          cfg.ProfileBucket,
		})
		if err != nil {
			log.Fatalf("browserd: init %s profile store failed: %v", cfg.ProfileStore, err)
		} else {
			store = s3Store
		}
	case "file":
		fileStore, err := profile.NewFileStore(profile.FileStoreConfig{Root: cfg.ProfileFileRoot})
		if err != nil {
			log.Fatalf("browserd: init file profile store failed: %v", err)
		}
		store = fileStore
	case "memory":
		store = profile.NewMemoryStore()
	default:
		store = profile.NewMemoryStore()
	}
	manager := session.NewManager(session.ManagerOptions{
		Store:      store,
		Workdir:    cfg.Workdir,
		CDPBaseURL: cfg.CDPBaseURL,
	})
	var assetStore assets.Store
	assetS3Store, err := assets.NewS3Store(assets.S3StoreConfig{
		Endpoint:        cfg.S3Endpoint,
		Region:          cfg.S3Region,
		AccessKeyID:     cfg.S3AccessKeyID,
		SecretAccessKey: cfg.S3SecretAccessKey,
		ForcePathStyle:  cfg.S3ForcePathStyle,
	})
	if err != nil {
		log.Printf("browserd: init asset s3 store failed, screenshot uploads disabled: %v", err)
	} else {
		assetStore = assetS3Store
	}
	browserSvc := browser.NewServiceWithOptions(browser.ServiceOptions{
		Sessions:    manager,
		State:       runtime.NewState(),
		Assets:      assetStore,
		AssetBucket: cfg.ProfileBucket,
		ProxyHop: browser.ProxyHopOptions{
			Mode:        cfg.ProxyHop,
			WorkerURL:   cfg.ProxyWorkerURL,
			WorkerToken: cfg.ProxyWorkerToken,
		},
	})
	handler := controller.NewSessionControllerWithLive(controller.SessionControllerOptions{
		Manager:       manager,
		Browser:       browserSvc,
		CDPBaseURL:    cfg.CDPBaseURL,
		LiveBaseURL:   cfg.LiveBaseURL,
		NoVNCBasePath: cfg.NoVNCBasePath,
		LiveTokenTTL:  cfg.LiveTokenTTL,
		TokenStore:    live.NewTokenStore(live.TokenStoreOptions{}),
	})
	handler.StartExpiredSessionReaper(context.Background(), time.Minute)
	noVNCBasePath := strings.TrimRight(strings.TrimSpace(cfg.NoVNCBasePath), "/")
	if noVNCBasePath == "" {
		noVNCBasePath = "/v"
	}
	liveViewerAssets := http.StripPrefix("/browser-live/", liveviewer.StaticHandler())

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/healthz":
			types.WriteOK(w, http.StatusOK, map[string]any{"ok": true})
			return
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/browser-live/"):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			liveViewerAssets.ServeHTTP(w, r)
			return
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, noVNCBasePath+"/") && strings.HasSuffix(r.URL.Path, "/pointer-events"):
			token := extractLiveViewToken(r.URL.Path, noVNCBasePath)
			if token == "" {
				types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "missing live view token")
				return
			}
			handler.ServePointerEvents(w, r, token)
			return
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, noVNCBasePath+"/"):
			token := extractLiveViewToken(r.URL.Path, noVNCBasePath)
			if token == "" {
				types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "missing live view token")
				return
			}
			handler.ServeLiveView(w, r, token)
			return
		case r.Method == http.MethodPost && r.URL.Path == "/v1/profiles/export":
			handler.TransferProfile(w, r, false)
			return
		case r.Method == http.MethodPost && r.URL.Path == "/v1/profiles/import":
			handler.TransferProfile(w, r, true)
			return
		case r.Method == http.MethodPost && r.URL.Path == "/v1/profiles/cookies":
			handler.ImportProfileCookies(w, r)
			return
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions":
			handler.CreateSession(w, r)
			return
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/sessions/") && strings.HasSuffix(r.URL.Path, "/live-view"):
			id, ok := controller.ExtractRuntimeSessionID(strings.TrimSuffix(r.URL.Path, "/live-view"))
			if !ok {
				types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid runtimeSessionId")
				return
			}
			handler.LiveView(w, r, id)
			return
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/sessions/") && strings.HasSuffix(r.URL.Path, "/handoff/start"):
			id, ok := controller.ExtractRuntimeSessionID(strings.TrimSuffix(r.URL.Path, "/handoff/start"))
			if !ok {
				types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid runtimeSessionId")
				return
			}
			handler.StartHandoff(w, r, id)
			return
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/sessions/") && strings.Contains(r.URL.Path, "/handoff/") && strings.HasSuffix(r.URL.Path, "/complete"):
			id, handoffID, ok := extractHandoffCompletePath(r.URL.Path)
			if !ok {
				types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid handoff path")
				return
			}
			handler.CompleteHandoff(w, r, id, handoffID)
			return
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/sessions/") && strings.HasSuffix(r.URL.Path, "/commit"):
			id, ok := controller.ExtractRuntimeSessionID(strings.TrimSuffix(r.URL.Path, "/commit"))
			if !ok {
				types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid runtimeSessionId")
				return
			}
			handler.CommitSession(w, r, id)
			return
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/sessions/"):
			id, ok := controller.ExtractRuntimeSessionID(r.URL.Path)
			if !ok {
				types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid runtimeSessionId")
				return
			}
			handler.DeleteSession(w, r, id)
			return
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/sessions/") && strings.HasSuffix(r.URL.Path, "/navigate"):
			id, ok := controller.ExtractRuntimeSessionID(strings.TrimSuffix(r.URL.Path, "/navigate"))
			if !ok {
				types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid runtimeSessionId")
				return
			}
			handler.Navigate(w, r, id)
			return
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/sessions/") && strings.HasSuffix(r.URL.Path, "/snapshot"):
			id, ok := controller.ExtractRuntimeSessionID(strings.TrimSuffix(r.URL.Path, "/snapshot"))
			if !ok {
				types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid runtimeSessionId")
				return
			}
			handler.Snapshot(w, r, id)
			return
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/sessions/") && strings.HasSuffix(r.URL.Path, "/act"):
			id, ok := controller.ExtractRuntimeSessionID(strings.TrimSuffix(r.URL.Path, "/act"))
			if !ok {
				types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid runtimeSessionId")
				return
			}
			handler.Act(w, r, id)
			return
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/sessions/") && strings.HasSuffix(r.URL.Path, "/wait-for"):
			id, ok := controller.ExtractRuntimeSessionID(strings.TrimSuffix(r.URL.Path, "/wait-for"))
			if !ok {
				types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid runtimeSessionId")
				return
			}
			handler.WaitFor(w, r, id)
			return
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/sessions/") && strings.HasSuffix(r.URL.Path, "/pageTool"):
			id, ok := controller.ExtractRuntimeSessionID(strings.TrimSuffix(r.URL.Path, "/pageTool"))
			if !ok {
				types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid runtimeSessionId")
				return
			}
			handler.PageTool(w, r, id)
			return
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/sessions/") && strings.HasSuffix(r.URL.Path, "/cookies/import"):
			id, ok := controller.ExtractRuntimeSessionID(strings.TrimSuffix(r.URL.Path, "/cookies/import"))
			if !ok {
				types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid runtimeSessionId")
				return
			}
			handler.ImportCookies(w, r, id)
			return
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/sessions/") && strings.HasSuffix(r.URL.Path, "/evaluate"):
			id, ok := controller.ExtractRuntimeSessionID(strings.TrimSuffix(r.URL.Path, "/evaluate"))
			if !ok {
				types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid runtimeSessionId")
				return
			}
			handler.Evaluate(w, r, id)
			return
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/sessions/") && strings.HasSuffix(r.URL.Path, "/screenshot"):
			id, ok := controller.ExtractRuntimeSessionID(strings.TrimSuffix(r.URL.Path, "/screenshot"))
			if !ok {
				types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid runtimeSessionId")
				return
			}
			handler.Screenshot(w, r, id)
			return
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/sessions/") && strings.HasSuffix(r.URL.Path, "/upload-files"):
			id, ok := controller.ExtractRuntimeSessionID(strings.TrimSuffix(r.URL.Path, "/upload-files"))
			if !ok {
				types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid runtimeSessionId")
				return
			}
			handler.UploadFiles(w, r, id)
			return
		default:
			types.WriteErr(w, http.StatusNotFound, "NOT_FOUND", "route not found")
			return
		}
	})
}

func extractLiveViewToken(path string, basePath string) string {
	basePath = strings.TrimRight(strings.TrimSpace(basePath), "/")
	if basePath == "" {
		basePath = "/v"
	}
	rest := strings.TrimPrefix(path, basePath+"/")
	rest = strings.TrimLeft(rest, "/")
	token, _, _ := strings.Cut(rest, "/")
	return strings.TrimSpace(token)
}

func extractHandoffCompletePath(path string) (string, string, bool) {
	if !strings.HasPrefix(path, "/v1/sessions/") || !strings.HasSuffix(path, "/complete") {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, "/v1/sessions/")
	rest = strings.TrimSuffix(rest, "/complete")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 3 || parts[1] != "handoff" || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[2]) == "" {
		return "", "", false
	}
	return parts[0], parts[2], true
}
