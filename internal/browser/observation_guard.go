package browser

import (
	"context"
	"fmt"
	"github.com/chromedp/chromedp"
)

func (s *Service) validateObservation(ctx context.Context, id string, in ActInput) error {
	return s.checkObservation(ctx, id, in, "validate")
}

func (s *Service) checkObservation(ctx context.Context, id string, in ActInput, method string) error {
	state, err := s.state.GetSnapshot(id)
	if err != nil {
		return err
	}
	if state.SnapshotID != in.ExpectedSnapshotID {
		return ErrStaleObservation
	}
	var result struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
	}
	script := fmt.Sprintf(`(() => {const guard=globalThis.__browserActionGuard;if(!guard || typeof guard[%q]!=="function")return {ok:false,code:"STALE_OBSERVATION"};return guard[%q](%q,%q);})()`, method, method, in.ExpectedSnapshotID, in.Ref)
	if err = chromedp.Run(ctx, chromedp.Evaluate(script, &result)); err != nil {
		return err
	}
	if result.OK {
		return nil
	}
	if result.Code == "TARGET_NOT_ACTIONABLE" {
		return ErrTargetNotActionable
	}
	return ErrStaleObservation
}
