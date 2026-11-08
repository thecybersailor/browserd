package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	cdinput "github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	browserdclient "github.com/thecybersailor/browserd-client-go/pkg/browserd"
)

type pageToolTarget struct {
	Ref      string
	Selector string
	Text     string
	XPath    string
	Point    *pointerPoint
}

func (s *Service) PageTool(parent context.Context, runtimeSessionID string, input browserdclient.PageToolInput) (browserdclient.PageToolResult, error) {
	if err := browserdclient.ValidatePageToolInput(input); err != nil {
		return nil, err
	}
	method := strings.TrimSpace(input.Method)
	payload := input.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	switch method {
	case "page.url", "page.title", "page.viewport":
		return s.pageToolPage(runtimeSessionID, method, input.TimeoutMs)
	case "page.waitForTimeout":
		return s.pageToolWaitForTimeout(parent, payload, input.TimeoutMs)
	case "page.waitForLoadState":
		return s.pageToolWaitForLoadState(parent, runtimeSessionID, payload, input.TimeoutMs)
	case "keyboard.type", "keyboard.press", "keyboard.down", "keyboard.up", "keyboard.insertText":
		return s.pageToolKeyboard(runtimeSessionID, method, payload, input.TimeoutMs)
	case "mouse.move", "mouse.click", "mouse.dblclick", "mouse.down", "mouse.up", "mouse.wheel":
		return s.pageToolMouse(runtimeSessionID, method, payload, input.TimeoutMs)
	case "element.click", "element.hover", "element.type", "element.fill", "element.press":
		return s.pageToolElementAction(runtimeSessionID, method, payload, input.TimeoutMs)
	case "element.text", "element.html", "element.box", "element.exists":
		return s.pageToolElementRead(runtimeSessionID, method, payload, input.TimeoutMs)
	default:
		return nil, fmt.Errorf("%w: unsupported pageTool method %s", ErrActionFailed, method)
	}
}

func (s *Service) pageToolPage(runtimeSessionID string, method string, timeoutMs int) (browserdclient.PageToolResult, error) {
	ctx, cancel, err := s.newBrowserContext(runtimeSessionID, timeoutMs)
	if err != nil {
		return nil, err
	}
	defer cancel()
	switch method {
	case "page.url":
		var value string
		if err := chromedp.Run(ctx, chromedp.Location(&value)); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrActionFailed, err)
		}
		return browserdclient.PageToolResult{"value": value}, nil
	case "page.title":
		var value string
		if err := chromedp.Run(ctx, chromedp.Title(&value)); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrActionFailed, err)
		}
		return browserdclient.PageToolResult{"value": value}, nil
	case "page.viewport":
		var value viewportRect
		if err := chromedp.Run(ctx, chromedp.Evaluate(runtimeViewportRectScript(1366, 768), &value)); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrActionFailed, err)
		}
		return browserdclient.PageToolResult{"value": value}, nil
	default:
		return nil, ErrInvalidRequest
	}
}

func (s *Service) pageToolWaitForTimeout(parent context.Context, payload map[string]any, timeoutMs int) (browserdclient.PageToolResult, error) {
	ms := intPayload(payload, "ms", 0)
	if ms < 0 {
		return nil, ErrInvalidRequest
	}
	if timeoutMs > 0 && ms > timeoutMs {
		return nil, fmt.Errorf("%w: waitForTimeout exceeds timeoutMs", ErrInvalidRequest)
	}
	if err := sleepBehavior(parent, time.Duration(ms)*time.Millisecond); err != nil {
		return nil, err
	}
	return browserdclient.PageToolResult{"ok": true}, nil
}

func (s *Service) pageToolWaitForLoadState(parent context.Context, runtimeSessionID string, payload map[string]any, timeoutMs int) (browserdclient.PageToolResult, error) {
	state := strings.TrimSpace(stringPayload(payload, "state", "load"))
	if state == "" {
		state = "load"
	}
	ctx, cancel, err := s.newTaskBrowserContext(parent, runtimeSessionID, timeoutMs)
	if err != nil {
		return nil, err
	}
	defer cancel()
	switch state {
	case "domcontentloaded":
		if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			for {
				var ready string
				if err := chromedp.Evaluate(`document.readyState`, &ready).Do(ctx); err != nil {
					return err
				}
				if ready == "interactive" || ready == "complete" {
					return nil
				}
				if err := sleepBehavior(ctx, 100*time.Millisecond); err != nil {
					return err
				}
			}
		})); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrActionFailed, err)
		}
	case "load":
		if err := chromedp.Run(ctx, page.SetLifecycleEventsEnabled(true), chromedp.ActionFunc(func(ctx context.Context) error {
			for {
				var ready string
				if err := chromedp.Evaluate(`document.readyState`, &ready).Do(ctx); err != nil {
					return err
				}
				if ready == "complete" {
					return nil
				}
				if err := sleepBehavior(ctx, 100*time.Millisecond); err != nil {
					return err
				}
			}
		})); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrActionFailed, err)
		}
	default:
		return nil, ErrInvalidRequest
	}
	return browserdclient.PageToolResult{"ok": true}, nil
}

func (s *Service) pageToolKeyboard(runtimeSessionID string, method string, payload map[string]any, timeoutMs int) (browserdclient.PageToolResult, error) {
	ctx, cancel, err := s.newBrowserContext(runtimeSessionID, timeoutMs)
	if err != nil {
		return nil, err
	}
	defer cancel()
	switch method {
	case "keyboard.type":
		text := stringPayload(payload, "text", "")
		if text == "" {
			return nil, ErrInvalidRequest
		}
		if err := trustedInsertText(ctx, text); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrActionFailed, err)
		}
	case "keyboard.press":
		key := stringPayload(payload, "key", "")
		encoded, err := normalizePressKey(key)
		if err != nil {
			return nil, err
		}
		if err := chromedp.Run(ctx, keyEventAction(textInputKey{Key: encoded})); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrActionFailed, err)
		}
	case "keyboard.down", "keyboard.up":
		key := stringPayload(payload, "key", "")
		if _, err := normalizePressKey(key); err != nil {
			return nil, err
		}
		eventType := cdinput.KeyDown
		if method == "keyboard.up" {
			eventType = cdinput.KeyUp
		}
		if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			return cdinput.DispatchKeyEvent(eventType).WithKey(key).Do(ctx)
		})); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrActionFailed, err)
		}
	case "keyboard.insertText":
		text := stringPayload(payload, "text", "")
		if text == "" {
			return nil, ErrInvalidRequest
		}
		if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			return cdinput.InsertText(text).Do(ctx)
		})); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrActionFailed, err)
		}
	default:
		return nil, ErrInvalidRequest
	}
	return browserdclient.PageToolResult{"ok": true}, nil
}

func (s *Service) pageToolMouse(runtimeSessionID string, method string, payload map[string]any, timeoutMs int) (browserdclient.PageToolResult, error) {
	ctx, cancel, err := s.newBrowserContext(runtimeSessionID, timeoutMs)
	if err != nil {
		return nil, err
	}
	defer cancel()
	x := floatPayload(payload, "x", 0)
	y := floatPayload(payload, "y", 0)
	viewport := viewportRect{Width: 1366, Height: 768}
	_ = chromedp.Run(ctx, chromedp.Evaluate(runtimeViewportRectScript(1366, 768), &viewport))
	point := s.pointerStart(runtimeSessionID, viewport)
	if x > 0 || y > 0 {
		if x <= 0 || y <= 0 {
			return nil, ErrInvalidRequest
		}
		point = pointerPoint{X: x, Y: y}
	}
	target := browserTarget{Rect: targetRect{X: point.X - 0.5, Y: point.Y - 0.5, Width: 1, Height: 1}}
	input := ActInput{Action: "mouse", X: point.X, Y: point.Y, TimeoutMs: timeoutMs, MotionProfile: optionString(payload, "motionProfile", "direct")}
	switch method {
	case "mouse.move":
		end, nextViewport, err := s.trustedMoveTarget(ctx, runtimeSessionID, target, input)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrActionFailed, err)
		}
		return browserdclient.PageToolResult{"ok": true, "x": end.X, "y": end.Y, "viewport": nextViewport}, nil
	case "mouse.click", "mouse.dblclick":
		input.Button = optionString(payload, "button", "left")
		input.ClickCount = intPayload(payload, "clickCount", 1)
		if method == "mouse.dblclick" {
			input.ClickCount = 2
		}
		if err := s.trustedClickTarget(ctx, runtimeSessionID, target, input); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrActionFailed, err)
		}
	case "mouse.down", "mouse.up":
		down := method == "mouse.down"
		eventType := cdinput.MouseReleased
		buttons := int64(0)
		if down {
			eventType = cdinput.MousePressed
			buttons = mouseButtonsBitfield(cdinput.Left)
		}
		if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			return cdinput.DispatchMouseEvent(eventType, point.X, point.Y).WithButton(cdinput.Left).WithButtons(buttons).Do(ctx)
		})); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrActionFailed, err)
		}
		s.setPointerButton(runtimeSessionID, point, viewport, down)
	case "mouse.wheel":
		deltaX := floatPayload(payload, "deltaX", 0)
		deltaY := floatPayload(payload, "deltaY", 0)
		if deltaX == 0 && deltaY == 0 {
			return nil, ErrInvalidRequest
		}
		if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			return cdinput.DispatchMouseEvent(cdinput.MouseWheel, point.X, point.Y).WithDeltaX(deltaX).WithDeltaY(deltaY).Do(ctx)
		})); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrActionFailed, err)
		}
		s.setPointer(runtimeSessionID, point, viewport)
	default:
		return nil, ErrInvalidRequest
	}
	return browserdclient.PageToolResult{"ok": true}, nil
}

func (s *Service) pageToolElementAction(runtimeSessionID string, method string, payload map[string]any, timeoutMs int) (browserdclient.PageToolResult, error) {
	target, err := parsePageToolTarget(payload["target"])
	if err != nil {
		return nil, err
	}
	ctx, cancel, err := s.newBrowserContext(runtimeSessionID, timeoutMs)
	if err != nil {
		return nil, err
	}
	defer cancel()
	resolved, err := s.resolvePageToolTarget(ctx, runtimeSessionID, target)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrActionFailed, err)
	}
	input := ActInput{
		TimeoutMs:     timeoutMs,
		MotionProfile: optionString(payload, "motionProfile", "direct"),
		Button:        optionString(payload, "button", "left"),
		ClickCount:    intPayload(payload, "clickCount", 1),
		Text:          stringPayload(payload, "text", ""),
		Value:         stringPayload(payload, "value", ""),
		Key:           stringPayload(payload, "key", ""),
	}
	switch method {
	case "element.click":
		if err := s.trustedClickTarget(ctx, runtimeSessionID, resolved, input); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrActionFailed, err)
		}
	case "element.hover":
		if _, _, err := s.trustedMoveTarget(ctx, runtimeSessionID, resolved, input); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrActionFailed, err)
		}
	case "element.type", "element.fill":
		text := input.Text
		if method == "element.fill" {
			text = input.Value
			input.Clear = true
		}
		if text == "" {
			return nil, ErrInvalidRequest
		}
		if err := s.trustedClickTarget(ctx, runtimeSessionID, resolved, input); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrActionFailed, err)
		}
		if input.Clear {
			before, _ := planTextInputKeys(true, false)
			for _, key := range before {
				if err := keyEventAction(key).Do(ctx); err != nil {
					return nil, fmt.Errorf("%w: %v", ErrActionFailed, err)
				}
			}
		}
		if err := trustedInsertText(ctx, text); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrActionFailed, err)
		}
	case "element.press":
		encoded, err := normalizePressKey(input.Key)
		if err != nil {
			return nil, err
		}
		if err := s.trustedClickTarget(ctx, runtimeSessionID, resolved, input); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrActionFailed, err)
		}
		if err := keyEventAction(textInputKey{Key: encoded}).Do(ctx); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrActionFailed, err)
		}
	default:
		return nil, ErrInvalidRequest
	}
	return browserdclient.PageToolResult{"ok": true}, nil
}

func (s *Service) pageToolElementRead(runtimeSessionID string, method string, payload map[string]any, timeoutMs int) (browserdclient.PageToolResult, error) {
	target, err := parsePageToolTarget(payload["target"])
	if err != nil {
		return nil, err
	}
	if target.Ref != "" {
		refState, err := s.state.GetRef(runtimeSessionID, target.Ref)
		if err != nil {
			return nil, err
		}
		target.Selector = refState.Selector
		target.Ref = ""
	}
	script, err := buildPageToolElementReadScript(method, target)
	if err != nil {
		return nil, err
	}
	ctx, cancel, err := s.newBrowserContext(runtimeSessionID, timeoutMs)
	if err != nil {
		return nil, err
	}
	defer cancel()
	var out map[string]any
	if err := chromedp.Run(ctx, chromedp.Evaluate(script, &out, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true)
	})); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrActionFailed, err)
	}
	return browserdclient.PageToolResult(out), nil
}

func (s *Service) resolvePageToolTarget(ctx context.Context, runtimeSessionID string, target pageToolTarget) (browserTarget, error) {
	if target.Ref != "" {
		return s.resolveActRef(ctx, runtimeSessionID, target.Ref)
	}
	if target.Point != nil {
		return browserTarget{Rect: targetRect{X: target.Point.X - 0.5, Y: target.Point.Y - 0.5, Width: 1, Height: 1}}, nil
	}
	if target.Selector != "" {
		return queryTargetBySelector(ctx, target.Selector, false)
	}
	var out browserTarget
	script, err := buildPageToolTargetScript(target)
	if err != nil {
		return browserTarget{}, err
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(script, &out)); err != nil {
		return browserTarget{}, err
	}
	return out, nil
}

func parsePageToolTarget(raw any) (pageToolTarget, error) {
	obj, ok := raw.(map[string]any)
	if !ok || len(obj) == 0 {
		return pageToolTarget{}, fmt.Errorf("%w: pageTool element method requires target", ErrInvalidRequest)
	}
	target := pageToolTarget{
		Ref:      strings.TrimSpace(stringFromAny(obj["ref"])),
		Selector: strings.TrimSpace(stringFromAny(obj["selector"])),
		Text:     strings.TrimSpace(stringFromAny(obj["text"])),
		XPath:    strings.TrimSpace(stringFromAny(obj["xpath"])),
	}
	if rawPoint, ok := obj["point"].(map[string]any); ok {
		x := floatFromAny(rawPoint["x"])
		y := floatFromAny(rawPoint["y"])
		if x <= 0 || y <= 0 {
			return pageToolTarget{}, fmt.Errorf("%w: point target requires positive x and y", ErrInvalidRequest)
		}
		target.Point = &pointerPoint{X: x, Y: y}
	}
	count := 0
	for _, value := range []bool{target.Ref != "", target.Selector != "", target.Text != "", target.XPath != "", target.Point != nil} {
		if value {
			count++
		}
	}
	if count != 1 {
		return pageToolTarget{}, fmt.Errorf("%w: target must contain exactly one of ref, selector, text, xpath, or point", ErrInvalidRequest)
	}
	return target, nil
}

func buildPageToolTargetScript(target pageToolTarget) (string, error) {
	if target.Selector != "" {
		return fmt.Sprintf(`(() => {
  const el = document.querySelector(%q);
  if (!el) throw new Error("missing");
  el.scrollIntoView({block:"center", inline:"center"});
  const r = el.getBoundingClientRect();
  const tag = (el.tagName || "").toLowerCase();
  return { selector: %q, rect: { x: r.left, y: r.top, width: r.width, height: r.height }, editable: !!(el.isContentEditable || tag === "textarea" || (tag === "input" && !["button","checkbox","file","hidden","image","radio","range","reset","submit"].includes((el.type || "").toLowerCase()))) };
})()`, target.Selector, target.Selector), nil
	}
	if target.Text != "" {
		return fmt.Sprintf(`(() => {
  const text = %q;
  const visible = (el) => {
    const r = el.getBoundingClientRect();
    const style = window.getComputedStyle(el);
    return r.width > 0 && r.height > 0 && style.visibility !== "hidden" && style.display !== "none";
  };
  const el = Array.from(document.querySelectorAll("body *")).find((node) => visible(node) && (node.innerText || node.textContent || "").trim() === text);
  if (!el) throw new Error("missing");
  el.scrollIntoView({block:"center", inline:"center"});
  const r = el.getBoundingClientRect();
  const tag = (el.tagName || "").toLowerCase();
  return { rect: { x: r.left, y: r.top, width: r.width, height: r.height }, editable: !!(el.isContentEditable || tag === "textarea" || (tag === "input" && !["button","checkbox","file","hidden","image","radio","range","reset","submit"].includes((el.type || "").toLowerCase()))) };
})()`, target.Text), nil
	}
	if target.XPath != "" {
		return fmt.Sprintf(`(() => {
  const result = document.evaluate(%q, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null);
  const el = result.singleNodeValue;
  if (!el) throw new Error("missing");
  el.scrollIntoView({block:"center", inline:"center"});
  const r = el.getBoundingClientRect();
  const tag = (el.tagName || "").toLowerCase();
  return { rect: { x: r.left, y: r.top, width: r.width, height: r.height }, editable: !!(el.isContentEditable || tag === "textarea" || (tag === "input" && !["button","checkbox","file","hidden","image","radio","range","reset","submit"].includes((el.type || "").toLowerCase()))) };
})()`, target.XPath), nil
	}
	return "", ErrInvalidRequest
}

func buildPageToolElementReadScript(method string, target pageToolTarget) (string, error) {
	resolve, err := pageToolElementResolverScript(target)
	if err != nil {
		return "", err
	}
	switch method {
	case "element.text":
		return fmt.Sprintf(`(() => {
  const el = %s;
  return { value: el.innerText || el.textContent || "" };
})()`, resolve), nil
	case "element.html":
		return fmt.Sprintf(`(() => {
  const el = %s;
  return { value: el.innerHTML || "" };
})()`, resolve), nil
	case "element.box":
		return fmt.Sprintf(`(() => {
  const el = %s;
  const r = el.getBoundingClientRect();
  return { value: { x: r.left, y: r.top, width: r.width, height: r.height } };
})()`, resolve), nil
	case "element.exists":
		return fmt.Sprintf(`(() => {
  try {
    %s;
    return { value: true };
  } catch {
    return { value: false };
  }
})()`, resolve), nil
	default:
		return "", ErrInvalidRequest
	}
}

func pageToolElementResolverScript(target pageToolTarget) (string, error) {
	switch {
	case target.Selector != "":
		return fmt.Sprintf(`(() => {
    const el = document.querySelector(%q);
    if (!el) throw new Error("missing");
    return el;
  })()`, target.Selector), nil
	case target.Text != "":
		return fmt.Sprintf(`(() => {
    const text = %q;
    const el = Array.from(document.querySelectorAll("body *")).find((node) => (node.innerText || node.textContent || "").trim() === text);
    if (!el) throw new Error("missing");
    return el;
  })()`, target.Text), nil
	case target.XPath != "":
		return fmt.Sprintf(`(() => {
    const result = document.evaluate(%q, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null);
    const el = result.singleNodeValue;
    if (!el) throw new Error("missing");
    return el;
  })()`, target.XPath), nil
	default:
		return "", ErrInvalidRequest
	}
}

func stringPayload(payload map[string]any, key string, fallback string) string {
	value := strings.TrimSpace(stringFromAny(payload[key]))
	if value == "" {
		return fallback
	}
	return value
}

func optionString(payload map[string]any, key string, fallback string) string {
	if options, ok := payload["options"].(map[string]any); ok {
		if value := strings.TrimSpace(stringFromAny(options[key])); value != "" {
			return value
		}
	}
	return stringPayload(payload, key, fallback)
}

func intPayload(payload map[string]any, key string, fallback int) int {
	if options, ok := payload["options"].(map[string]any); ok {
		if value := intFromAny(options[key]); value != 0 {
			return value
		}
	}
	if value := intFromAny(payload[key]); value != 0 {
		return value
	}
	return fallback
}

func floatPayload(payload map[string]any, key string, fallback float64) float64 {
	if options, ok := payload["options"].(map[string]any); ok {
		if value := floatFromAny(options[key]); value != 0 {
			return value
		}
	}
	if value := floatFromAny(payload[key]); value != 0 {
		return value
	}
	return fallback
}

func stringFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		out, _ := typed.Int64()
		return int(out)
	default:
		return 0
	}
}

func floatFromAny(value any) float64 {
	switch typed := value.(type) {
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case float64:
		return typed
	case json.Number:
		out, _ := typed.Float64()
		return out
	default:
		return 0
	}
}
