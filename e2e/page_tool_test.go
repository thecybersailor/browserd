package e2e

import (
	"context"
	"os"
	"strings"
	"testing"

	browserdclient "github.com/thecybersailor/browserd-client-go/pkg/browserd"
)

func TestEvaluateWithPageToolBridgeReadsPageTitle(t *testing.T) {
	baseURL := strings.TrimRight(os.Getenv("BROWSERD_BASE_URL"), "/")
	if baseURL == "" {
		t.Skip("BROWSERD_BASE_URL not set")
	}
	client, err := browserdclient.NewClient(browserdclient.Config{BaseURL: baseURL})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	ctx := context.Background()
	session, err := client.CreateSession(ctx, browserdclient.CreateSessionInput{
		Fingerprint: browserdclient.FingerprintConfig{Seed: "pagetool-e2e"},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer func() { _ = client.Close(ctx, session.RuntimeSessionID) }()

	if _, err := navigateAndWatch(t, client, ctx, session.RuntimeSessionID, browserdclient.NavigateInput{
		URL:       "data:text/html,<title>PageTool E2E</title><h1 id='title'>Hello</h1>",
		WaitUntil: "load",
	}); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	pageToolScript, err := browserdclient.BuildPageToolRuntimeScript(browserdclient.DefaultPageToolBridgeName)
	if err != nil {
		t.Fatalf("runtime script: %v", err)
	}
	out, err := evaluateAndWatch(t, client, ctx, session.RuntimeSessionID, browserdclient.EvaluateInput{
		PageToolBridge: &browserdclient.PageToolBridgeConfig{Enabled: true},
		Script: pageToolScript + `
const title = await pageTool.page.title();
const text = await pageTool.element.text({ selector: "#title" });
return { title: title.value, text: text.value };
`,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	result, ok := out.Result.(map[string]any)
	if !ok || result["title"] != "PageTool E2E" || result["text"] != "Hello" {
		t.Fatalf("unexpected result: %#v", out.Result)
	}
}
