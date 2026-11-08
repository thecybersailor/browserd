package browser

import (
	"strings"
	"testing"

	browserdclient "github.com/thecybersailor/browserd-client-go/pkg/browserd"
)

func TestBuildPageToolBridgeInstallScript(t *testing.T) {
	script, err := buildPageToolBridgeInstallScript("__browserdPageToolCall", "__browserdNativePageToolBinding")
	if err != nil {
		t.Fatalf("build bridge script: %v", err)
	}
	for _, want := range []string{"window.__browserdPageToolCall", "Promise", "requestId", "__browserdNativePageToolBinding"} {
		if !strings.Contains(script, want) {
			t.Fatalf("missing %s in %s", want, script)
		}
	}
}

func TestPageToolBridgeNameDefaultsToPublicContract(t *testing.T) {
	input := &browserdclient.PageToolBridgeConfig{Enabled: true}
	if pageToolBridgeName(input) != browserdclient.DefaultPageToolBridgeName {
		t.Fatalf("unexpected bridge name")
	}
}
