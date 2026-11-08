package browser

import (
	"context"
	cdinput "github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// textInputKey is a single key event dispatched around the insertText call of a
// type action.
type textInputKey struct {
	Key       string
	Command   string
	Modifiers cdinput.Modifier
}

// planTextInputKeys returns the key events dispatched before and after the text
// is inserted by a type action.
//
// clear selects the existing value and deletes it. submit presses Enter inside
// the same CDP round trip as the insertText, so the page cannot re-render and
// invalidate the ref between typing and submitting; that is the reason to use
// type with submit instead of a follow-up press action.
func planTextInputKeys(clear bool, submit bool) (before []textInputKey, after []textInputKey) {
	if clear {
		before = []textInputKey{
			{Key: "a", Command: "selectAll"},
			{Key: kb.Backspace},
		}
	}
	if submit {
		after = []textInputKey{{Key: kb.Enter}}
	}
	return before, after
}

func keyEventAction(key textInputKey) chromedp.Action {
	if key.Command != "" {
		return chromedp.ActionFunc(func(ctx context.Context) error {
			if err := cdinput.DispatchKeyEvent(cdinput.KeyRawDown).WithKey(key.Key).WithCommands([]string{key.Command}).Do(ctx); err != nil {
				return err
			}
			return cdinput.DispatchKeyEvent(cdinput.KeyUp).WithKey(key.Key).Do(ctx)
		})
	}
	if key.Modifiers != cdinput.ModifierNone {
		return chromedp.KeyEvent(key.Key, chromedp.KeyModifiers(key.Modifiers))
	}
	return chromedp.KeyEvent(key.Key)
}
