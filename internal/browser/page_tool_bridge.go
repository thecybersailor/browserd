package browser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	cdruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	browserdclient "github.com/thecybersailor/browserd-client-go/pkg/browserd"
)

var pageToolBridgeIdentifierPattern = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

type pageToolBridgeRequest struct {
	RequestID string                       `json:"requestId"`
	Input     browserdclient.PageToolInput `json:"input"`
}

func (s *Service) evaluateWithPageToolBridge(parent context.Context, runtimeSessionID string, input EvaluateInput) (EvaluateOutput, error) {
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
	ctx, cancel, err := s.newTaskBrowserContext(parent, runtimeSessionID, input.TimeoutMs)
	if err != nil {
		return EvaluateOutput{}, err
	}
	defer cancel()

	bridgeName := pageToolBridgeName(input.PageToolBridge)
	nativeBindingName := "browserdNativePageToolBinding_" + randomBridgeSuffix()
	installScript, err := buildPageToolBridgeInstallScript(bridgeName, nativeBindingName)
	if err != nil {
		return EvaluateOutput{}, err
	}

	chromedp.ListenTarget(ctx, func(ev any) {
		event, ok := ev.(*cdruntime.EventBindingCalled)
		if !ok || event.Name != nativeBindingName {
			return
		}
		go s.handlePageToolBridgeCall(ctx, runtimeSessionID, bridgeName, event.Payload)
	})

	var out EvaluateOutput
	if err := chromedp.Run(ctx,
		cdruntime.AddBinding(nativeBindingName),
		chromedp.Evaluate(installScript, nil, func(p *cdruntime.EvaluateParams) *cdruntime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}),
		chromedp.Evaluate(browserEvaluateRuntimeScript(input.Script, string(argsJSON)), &out, func(p *cdruntime.EvaluateParams) *cdruntime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}),
	); err != nil {
		return EvaluateOutput{}, fmt.Errorf("%w: %v", ErrEvaluateFailed, err)
	}
	return out, nil
}

func (s *Service) handlePageToolBridgeCall(parent context.Context, runtimeSessionID string, bridgeName string, payload string) {
	var request pageToolBridgeRequest
	err := json.Unmarshal([]byte(payload), &request)
	var result browserdclient.PageToolResult
	if err == nil {
		result, err = s.PageTool(parent, runtimeSessionID, request.Input)
	}
	if strings.TrimSpace(request.RequestID) == "" {
		return
	}
	script := buildPageToolBridgeResolveScript(bridgeName, request.RequestID, result, err)
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	_ = chromedp.Run(ctx, chromedp.Evaluate(script, nil))
}

func pageToolBridgeName(config *browserdclient.PageToolBridgeConfig) string {
	if config == nil || strings.TrimSpace(config.Name) == "" {
		return browserdclient.DefaultPageToolBridgeName
	}
	return strings.TrimSpace(config.Name)
}

func buildPageToolBridgeInstallScript(bridgeName string, nativeBindingName string) (string, error) {
	bridgeName = strings.TrimSpace(bridgeName)
	nativeBindingName = strings.TrimSpace(nativeBindingName)
	if bridgeName == "" {
		bridgeName = browserdclient.DefaultPageToolBridgeName
	}
	if !pageToolBridgeIdentifierPattern.MatchString(bridgeName) || !pageToolBridgeIdentifierPattern.MatchString(nativeBindingName) {
		return "", ErrInvalidRequest
	}
	return fmt.Sprintf(`(() => {
  const pending = new Map(); let sequence = 0;
  window.__browserdResolvePageToolCall = (requestId, result, error) => {
    const entry = pending.get(requestId);
    if (!entry) return;
    pending.delete(requestId);
    if (error) entry.reject(new Error(error.message || String(error)));
    else entry.resolve(result);
  };
  window.%s = (input) => new Promise((resolve, reject) => {
    const requestId = String(++sequence);
    pending.set(requestId, { resolve, reject });
    try {
      window.%s(JSON.stringify({ requestId, input }));
    } catch (err) {
      pending.delete(requestId);
      reject(err);
    }
  });
})()`, bridgeName, nativeBindingName), nil
}

func buildPageToolBridgeResolveScript(bridgeName string, requestID string, result browserdclient.PageToolResult, callErr error) string {
	requestJSON, _ := json.Marshal(requestID)
	resultJSON, _ := json.Marshal(result)
	if resultJSON == nil {
		resultJSON = []byte("null")
	}
	var errorJSON []byte
	if callErr != nil {
		errorJSON, _ = json.Marshal(map[string]any{"message": callErr.Error()})
	} else {
		errorJSON = []byte("null")
	}
	nameJSON, _ := json.Marshal(strings.TrimSpace(bridgeName))
	return fmt.Sprintf(`(() => {
  const bridgeName = %s;
  const requestId = %s;
  const result = %s;
  const error = %s;
  if (typeof window.__browserdResolvePageToolCall === "function") {
    window.__browserdResolvePageToolCall(requestId, result, error);
    return true;
  }
  return false;
})()`, string(nameJSON), string(requestJSON), string(resultJSON), string(errorJSON))
}

func randomBridgeSuffix() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}
