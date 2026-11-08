package browser

import (
	browserrt "browserd/internal/runtime"
	"context"
	"encoding/json"
	"testing"
)

func TestTreeSnapshotRoundTrip(t *testing.T) {
	var envelope snapshotRuntimeEnvelope
	raw := `{"snapshotId":"snap_tree","page":{"formatVersion":2,"url":"https://example.com/","tree":{"id":"n1","tag":"html","children":[{"id":"n2","tag":"button","ref":"e1","children":[{"id":"n3","tag":"#text","text":"Save"}]}]},"capture":{"scope":"light-dom","complete":true,"omissions":[]}},"refs":{"e1":{"ref":"e1","kind":"element","selector":"html > :nth-child(1)"}}}`
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatal(err)
	}
	state := browserrt.NewState()
	service := NewService(nil, state, nil)
	service.captureSnapshot = func(context.Context) (snapshotRuntimeEnvelope, error) { return envelope, nil }
	out, err := service.snapshotWithContext(context.Background(), "rt")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(out.Page)
	if err != nil {
		t.Fatal(err)
	}
	var page map[string]any
	if err := json.Unmarshal(encoded, &page); err != nil {
		t.Fatal(err)
	}
	if page["formatVersion"] != float64(2) || page["tree"] == nil || page["capture"] == nil {
		t.Fatalf("tree contract lost: %s", encoded)
	}
	if _, exists := page["groups"]; exists {
		t.Fatalf("two representations emitted: %s", encoded)
	}
	if _, err := state.GetRef("rt", "n2"); err == nil {
		t.Fatal("structural ID became actionable")
	}
	ref, err := state.GetRef("rt", "e1")
	if err != nil || ref.SnapshotID != out.SnapshotID {
		t.Fatal("action ownership lost")
	}
}
