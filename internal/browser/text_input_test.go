package browser

import (
	"testing"

	"github.com/chromedp/chromedp/kb"
)

func TestPlanTextInputKeys_PlainTypeDispatchesNoSurroundingKeys(t *testing.T) {
	before, after := planTextInputKeys(false, false)
	if len(before) != 0 {
		t.Fatalf("before = %v, want none", before)
	}
	if len(after) != 0 {
		t.Fatalf("after = %v, want none", after)
	}
}

func TestPlanTextInputKeys_ClearSelectsAllThenDeletes(t *testing.T) {
	before, after := planTextInputKeys(true, false)
	want := []textInputKey{
		{Key: "a", Command: "selectAll"},
		{Key: kb.Backspace},
	}
	if len(before) != len(want) {
		t.Fatalf("before = %v, want %v", before, want)
	}
	for i := range want {
		if before[i] != want[i] {
			t.Fatalf("before[%d] = %+v, want %+v", i, before[i], want[i])
		}
	}
	if len(after) != 0 {
		t.Fatalf("after = %v, want none", after)
	}
}

func TestPlanTextInputKeys_SubmitPressesEnterAfterInsert(t *testing.T) {
	before, after := planTextInputKeys(false, true)
	if len(before) != 0 {
		t.Fatalf("before = %v, want none", before)
	}
	if len(after) != 1 || after[0] != (textInputKey{Key: kb.Enter}) {
		t.Fatalf("after = %v, want a single Enter key event", after)
	}
	if after[0].Key == "Enter" {
		t.Fatal("submit dispatched the literal name Enter, which chromedp would type as text")
	}
}

func TestPlanTextInputKeys_ClearAndSubmitCombine(t *testing.T) {
	before, after := planTextInputKeys(true, true)
	if len(before) != 2 {
		t.Fatalf("before = %v, want clear keys", before)
	}
	if len(after) != 1 || after[0].Key != kb.Enter {
		t.Fatalf("after = %v, want a single Enter key event", after)
	}
}
