package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/thecybersailor/browserd-client-go/pkg/browserd"
	"strings"
	"testing"
	"time"
)

func submitHTTPTaskAndWatch(t *testing.T, session, operation string, input map[string]any) sdk.Task {
	t.Helper()
	input["request_id"] = fmt.Sprintf("%s-%d", operation, time.Now().UnixNano())
	status, out := mustDoJSON(t, "POST", session+"/"+operation, input)
	if status != 202 {
		t.Fatalf("submit %s: %d %+v", operation, status, out)
	}
	id, _ := out.Data["browserd_task_id"].(string)
	base := strings.SplitN(session, "/v1/sessions/", 2)[0]
	client, err := sdk.NewClient(sdk.Config{BaseURL: base})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		task, err := client.WatchTask(ctx, id, 10000)
		if err != nil {
			t.Fatal(err)
		}
		if task.State.Terminal() {
			return task
		}
	}
}

func successfulTaskObject(t *testing.T, task sdk.Task) map[string]any {
	t.Helper()
	if task.State != sdk.TaskSucceeded {
		t.Fatalf("task %s: state=%s error=%+v", task.BrowserdTaskID, task.State, task.Info.Error)
	}
	var result map[string]any
	if err := json.Unmarshal(task.Info.Result, &result); err != nil || result == nil {
		t.Fatalf("task result must be an object: %s (%v)", task.Info.Result, err)
	}
	return result
}

func waitBrowserTestTask(ctx context.Context, c *sdk.Client, sub sdk.TaskSubmission) (sdk.Task, error) {
	for {
		task, err := c.WatchTask(ctx, sub.BrowserdTaskID, 10000)
		if err != nil {
			return task, err
		}
		if task.State.Terminal() {
			if task.State != sdk.TaskSucceeded {
				return task, fmt.Errorf("task failed: %+v", task.Info.Error)
			}
			return task, nil
		}
	}
}
func navigateAndWatch(t *testing.T, c *sdk.Client, ctx context.Context, id string, in sdk.NavigateInput) (sdk.NavigateResult, error) {
	t.Helper()
	in.TaskControl = sdk.TaskControl{RequestID: fmt.Sprintf("nav-%d", time.Now().UnixNano()), ExecutionTimeoutMs: 15000}
	sub, err := c.Navigate(ctx, id, in)
	if err != nil {
		return sdk.NavigateResult{}, err
	}
	task, err := waitBrowserTestTask(ctx, c, sub)
	if err != nil {
		return sdk.NavigateResult{}, err
	}
	return sdk.DecodeNavigationResult(task, in.IncludeSnapshot)
}
func evaluateAndWatch(t *testing.T, c *sdk.Client, ctx context.Context, id string, in sdk.EvaluateInput) (sdk.EvaluateResult, error) {
	t.Helper()
	in.TaskControl = sdk.TaskControl{RequestID: fmt.Sprintf("eval-%d", time.Now().UnixNano()), ExecutionTimeoutMs: 15000}
	sub, err := c.Evaluate(ctx, id, in)
	if err != nil {
		return sdk.EvaluateResult{}, err
	}
	task, err := waitBrowserTestTask(ctx, c, sub)
	if err != nil {
		return sdk.EvaluateResult{}, err
	}
	return sdk.DecodeTaskResult[sdk.EvaluateResult](task)
}
