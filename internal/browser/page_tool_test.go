package browser

import (
	"context"
	"os"
	"strings"
	"testing"

	browserdclient "github.com/thecybersailor/browserd-client-go/pkg/browserd"
)

func TestPageToolRejectsUnsupportedMethods(t *testing.T) {
	svc := &Service{}
	_, err := svc.PageTool(context.Background(), "rt_1", browserdclient.PageToolInput{Method: "browser.unsupported"})
	var browserErr browserdclient.Error
	if !browserdclient.AsError(err, &browserErr) || browserErr.Code != "browserd_pagetool_method_unsupported" {
		t.Fatalf("expected unsupported method error, got %v", err)
	}
}

func TestBuildElementReadScriptUsesSelectorTarget(t *testing.T) {
	script, err := buildPageToolElementReadScript("element.text", pageToolTarget{Selector: "#title"})
	if err != nil {
		t.Fatalf("build script: %v", err)
	}
	if !strings.Contains(script, "document.querySelector") || !strings.Contains(script, "innerText") {
		t.Fatalf("unexpected script: %s", script)
	}
}

func TestPageToolTargetRejectsMissingLocator(t *testing.T) {
	_, err := parsePageToolTarget(map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "target") {
		t.Fatalf("expected target error, got %v", err)
	}
}

func TestPageToolImplementationUsesTrustedInputPrimitives(t *testing.T) {
	source, err := os.ReadFile("page_tool.go")
	if err != nil {
		t.Fatalf("read page_tool.go: %v", err)
	}
	text := string(source)
	for _, marker := range []string{
		"trustedClickTarget(ctx, runtimeSessionID, target, input)",
		"trustedMoveTarget(ctx, runtimeSessionID, target, input)",
		"cdinput.DispatchMouseEvent",
		"keyEventAction",
		"trustedInsertText",
	} {
		if !strings.Contains(text, marker) {
			t.Fatalf("pageTool implementation must use trusted primitive %s", marker)
		}
	}
}
