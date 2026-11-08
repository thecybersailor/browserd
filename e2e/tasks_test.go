package e2e

import (
	"context"
	"fmt"
	sdk "github.com/thecybersailor/browserd-client-go/pkg/browserd"
	"testing"
	"time"
)

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
