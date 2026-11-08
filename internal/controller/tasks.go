package controller

import (
	"browserd/internal/browser"
	"browserd/internal/session"
	"browserd/internal/tasks"
	"browserd/internal/types"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/thecybersailor/browserd-client-go/pkg/browserd"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func decodeStrict(r *http.Request, out any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 16<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("supply exactly one JSON object")
	}
	return nil
}
func writeTaskErr(w http.ResponseWriter, err error) {
	var e sdk.Error
	if errors.As(err, &e) {
		status := 400
		switch e.Code {
		case "SESSION_BUSY", "REQUEST_ID_CONFLICT", "HANDOFF_ACTIVE":
			status = 409
		case "TASK_NOT_FOUND":
			status = 404
		case "TASK_INSTANCE_LOST":
			status = 410
		case "TASK_CAPACITY_EXCEEDED":
			status = 503
		}
		types.WriteErr(w, status, e.Code, e.Message)
		return
	}
	writeBrowserErr(w, err)
}
func (h *SessionController) submitTask(w http.ResponseWriter, id, operation string, control sdk.TaskControl, payload any, allowHandoff bool, run func(context.Context) (any, error)) {
	if h.browser == nil {
		types.WriteErr(w, 503, "BROWSER_NOT_CONFIGURED", "browser runtime is not configured")
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		writeTaskErr(w, err)
		return
	}
	out, err := h.tasks.Submit(tasks.Input{SessionID: id, Operation: operation, Control: control, Payload: raw, Run: func(ctx context.Context) (any, error) { out, err := run(ctx); return out, taskRuntimeError(err) },
		Acquire: func() (func(), error) {
			release, ok := h.operations.tryAcquire(id)
			if !ok {
				return nil, sdk.Error{Code: "SESSION_BUSY", Message: "another browser operation is executing; this request did not execute"}
			}
			if err := h.manager.Touch(id); err != nil {
				release()
				return nil, err
			}
			if h.hasActiveHandoff(id) && !allowHandoff {
				release()
				return nil, sdk.Error{Code: "HANDOFF_ACTIVE", Message: "browser session is under human handoff"}
			}
			return release, nil
		}, Stop: func() error {
			if err := h.browser.Close(id); err != nil {
				return err
			}
			h.revokeRuntimeSession(id)
			if err := h.manager.Delete(id); err != nil && !errors.Is(err, session.ErrSessionNotFound) {
				return err
			}
			return nil
		}})
	if err != nil {
		writeTaskErr(w, err)
		return
	}
	types.WriteOK(w, http.StatusAccepted, out)
}
func executionMs(c sdk.TaskControl) int {
	if c.ExecutionTimeoutMs == 0 {
		return sdk.DefaultExecutionTimeoutMs
	}
	return c.ExecutionTimeoutMs
}
func (h *SessionController) Navigate(w http.ResponseWriter, r *http.Request, id string) {
	var req sdk.NavigateInput
	if err := decodeStrict(r, &req); err != nil {
		types.WriteErr(w, 400, "INVALID_REQUEST", err.Error())
		return
	}
	u, err := url.Parse(req.URL)
	if err != nil || strings.TrimSpace(req.URL) == "" || (u.Scheme != "http" && u.Scheme != "https" && req.URL != "about:blank") || ((u.Scheme == "http" || u.Scheme == "https") && u.Host == "") || (req.WaitUntil != "" && req.WaitUntil != "load") {
		types.WriteErr(w, 400, "INVALID_REQUEST", "navigate requires an HTTP(S) URL or about:blank and waitUntil=load")
		return
	}
	h.submitTask(w, id, "navigate", req.TaskControl, req, false, func(ctx context.Context) (any, error) {
		return h.browser.Navigate(ctx, id, browser.NavigateInput{URL: req.URL, WaitUntil: req.WaitUntil, TimeoutMs: executionMs(req.TaskControl), IncludeSnapshot: req.IncludeSnapshot, AfterLoadScreenshotS3Path: req.AfterLoadScreenshotS3Path})
	})
}
func (h *SessionController) Evaluate(w http.ResponseWriter, r *http.Request, id string) {
	var req sdk.EvaluateInput
	if err := decodeStrict(r, &req); err != nil {
		types.WriteErr(w, 400, "INVALID_REQUEST", err.Error())
		return
	}
	if strings.TrimSpace(req.Script) == "" || (req.World != "" && req.World != "MAIN") {
		types.WriteErr(w, 400, "INVALID_REQUEST", "evaluate requires script in the MAIN world")
		return
	}
	h.submitTask(w, id, "evaluate", req.TaskControl, req, req.AllowDuringHandoff, func(ctx context.Context) (any, error) {
		return h.browser.Evaluate(ctx, id, browser.EvaluateInput{Script: req.Script, Args: req.Args, TimeoutMs: executionMs(req.TaskControl), World: req.World, PageToolBridge: req.PageToolBridge})
	})
}
func (h *SessionController) WaitFor(w http.ResponseWriter, r *http.Request, id string) {
	var req sdk.WaitForInput
	if err := decodeStrict(r, &req); err != nil {
		types.WriteErr(w, 400, "INVALID_REQUEST", err.Error())
		return
	}
	if err := sdk.ValidateWaitForCondition(req.Condition); err != nil {
		writeTaskErr(w, err)
		return
	}
	if req.IntervalMs < 0 || req.StableMs < 0 {
		types.WriteErr(w, 400, "INVALID_REQUEST", "intervalMs and stableMs must be nonnegative")
		return
	}
	h.submitTask(w, id, "wait_for", req.TaskControl, req, false, func(ctx context.Context) (any, error) {
		return h.browser.WaitFor(ctx, id, browser.WaitForInput{Condition: browser.WaitForCondition{Type: req.Condition.Type, Ref: req.Condition.Ref, Selector: req.Condition.Selector, Text: req.Condition.Text, URLContains: req.Condition.URLContains, URLMatches: req.Condition.URLMatches, Checks: req.Condition.Checks}, TimeoutMs: executionMs(req.TaskControl), IntervalMs: req.IntervalMs, StableMs: req.StableMs})
	})
}
func (h *SessionController) submitPageToolTask(w http.ResponseWriter, r *http.Request, id string, req sdk.PageToolInput) {
	state, _ := req.Payload["state"].(string)
	if req.TimeoutMs != 0 || (state != "" && state != "load" && state != "domcontentloaded") {
		types.WriteErr(w, 400, "INVALID_REQUEST", "page.waitForLoadState supports load/domcontentloaded and executionTimeoutMs; networkidle is not implemented")
		return
	}
	h.submitTask(w, id, "page.waitForLoadState", req.TaskControl, req, false, func(ctx context.Context) (any, error) {
		req.TimeoutMs = executionMs(req.TaskControl)
		return h.browser.PageTool(ctx, id, req)
	})
}
func (h *SessionController) WatchTask(w http.ResponseWriter, r *http.Request, id string) {
	var req sdk.WatchTaskInput
	if err := decodeStrict(r, &req); err != nil {
		types.WriteErr(w, 400, "INVALID_REQUEST", err.Error())
		return
	}
	out, err := h.tasks.Watch(r.Context(), id, req.TimeoutMs)
	if err != nil {
		writeTaskErr(w, err)
		return
	}
	types.WriteOK(w, 200, out)
}
func (h *SessionController) CancelTask(w http.ResponseWriter, r *http.Request, id string) {
	var req struct{}
	if err := decodeStrict(r, &req); err != nil {
		types.WriteErr(w, 400, "INVALID_REQUEST", err.Error())
		return
	}
	out, err := h.tasks.Cancel(id)
	if err != nil {
		writeTaskErr(w, err)
		return
	}
	types.WriteOK(w, 200, out)
}

func taskRuntimeError(err error) error {
	if err == nil {
		return nil
	}
	code := "BROWSER_TASK_FAILED"
	switch {
	case strings.Contains(err.Error(), "EVALUATE_RESULT_NOT_JSON"):
		code = "EVALUATE_RESULT_NOT_JSON"
	case errors.Is(err, browser.ErrNavigationFailed):
		code = "NAVIGATION_FAILED"
	case errors.Is(err, browser.ErrEvaluateFailed):
		code = "EVALUATE_FAILED"
	case errors.Is(err, browser.ErrWaitForTimeout):
		code = "WAIT_FOR_TIMEOUT"
	case errors.Is(err, browser.ErrInvalidRequest):
		code = "INVALID_REQUEST"
	}
	return sdk.Error{Code: code, Message: taskErrorMessage(code, err), Cause: err}
}

func taskErrorMessage(code string, err error) string {
	if code == "EVALUATE_RESULT_NOT_JSON" {
		return normalizeEvaluateResultNotJSONMessage(err.Error())
	}
	return err.Error()
}
