package e2e

import (
	"encoding/json"
	"fmt"
	publicbrowserd "github.com/thecybersailor/browserd-client-go/pkg/browserd"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func decodedTree(t *testing.T, page map[string]any) map[string]any {
	t.Helper()
	doc, err := publicbrowserd.DecodeSnapshotPage(publicbrowserd.PageSnapshot(page))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(doc.Tree)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	return root
}

func treeNodes(node map[string]any) []map[string]any {
	out := []map[string]any{node}
	for _, child := range childrenOf(node) {
		out = append(out, treeNodes(child)...)
	}
	return out
}

func childrenOf(node map[string]any) []map[string]any {
	var out []map[string]any
	children, _ := node["children"].([]any)
	for _, child := range children {
		out = append(out, child.(map[string]any))
	}
	return out
}

func treeText(node map[string]any) string {
	out, _ := node["text"].(string)
	for _, child := range childrenOf(node) {
		out += treeText(child)
	}
	return out
}

func firstTreeRef(t *testing.T, page map[string]any, tag string) string {
	t.Helper()
	for _, node := range treeNodes(decodedTree(t, page)) {
		if node["tag"] == tag {
			if ref, ok := node["ref"].(string); ok {
				return ref
			}
		}
	}
	t.Fatalf("missing actionable %s", tag)
	return ""
}

func TestSnapshotDOMTreeE2E(t *testing.T) {
	base := strings.TrimRight(os.Getenv("BROWSERD_BASE_URL"), "/")
	if base == "" {
		t.Skip("NOT EXECUTED: BROWSERD_BASE_URL is required")
	}
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/frame" {
			fmt.Fprint(w, "<body>Frame text</body>")
			return
		}
		fmt.Fprint(w, `<title>DOM association</title><body><main>
<section id="a"><a href="/a">Entry A</a><div><span>$<b>11.75</b></span></div><img src="/a.png" width="20" height="20" alt="A"></section>
<section id="b"><a href="/b">Entry B</a><span>$9.50</span><img src="/b.png" width="20" height="20" alt="B"></section>
<p>Hello <strong>world</strong>!</p><span style="display:none">$1.00</span>
<div style="visibility:hidden">Hidden parent <span>Hidden child</span><button style="visibility:visible">Visible action</button></div>
<button id="save" onclick="this.textContent='Done'">Save</button><button disabled>Disabled</button>
<label for="name">Name</label><input id="name" placeholder="Name" value="Alice"><input type="checkbox" checked>
<select><option value="a">First</option><option value="b" selected>Second</option></select>
<div style="display:none"><input type="file" accept="image/*"></div>
<div contenteditable="true" data-placeholder="Compose">Draft</div>
<iframe src="/frame"></iframe><div id="shadow"></div>
<script>document.querySelector('#shadow').attachShadow({mode:'open'}).innerHTML='<button>Shadow action</button>'</script>
</main></body>`)
	}))
	defer site.Close()
	status, created := mustDoJSON(t, "POST", base+"/v1/sessions", map[string]any{"profilePath": fmt.Sprintf("/accounts/tree-e2e/%d/profile.tgz", time.Now().UnixNano()), "fingerprint": smokeFingerprint()})
	if status != 200 {
		t.Fatalf("create: %d %+v", status, created)
	}
	session := base + "/v1/sessions/" + fmt.Sprint(created.Data["runtimeSessionId"])
	defer mustDoJSON(t, "DELETE", session, nil)
	status, out := mustDoJSON(t, "POST", session+"/navigate", map[string]any{"url": site.URL, "includeSnapshot": true, "waitUntil": "load"})
	if status != 200 {
		t.Fatalf("navigate: %d %+v", status, out)
	}
	page := out.Data["snapshot"].(map[string]any)["page"].(map[string]any)
	if _, ok := page["groups"]; ok {
		t.Fatal("legacy groups emitted alongside tree")
	}
	if page["formatVersion"] != float64(3) {
		t.Fatal("tree is not versioned")
	}
	root := decodedTree(t, page)
	nodes := treeNodes(root)
	byID := map[string]map[string]any{}
	images, disabled, uploads, editors, checked, selected := 0, 0, 0, 0, 0, 0
	for _, node := range nodes {
		attrs, _ := node["attrs"].(map[string]any)
		state, _ := node["state"].(map[string]any)
		if id, ok := attrs["id"].(string); ok {
			byID[id] = node
		}
		if node["tag"] == "img" {
			images++
			if !strings.HasPrefix(fmt.Sprint(attrs["src"]), site.URL) {
				t.Fatal("image URL lost")
			}
		}
		if state["disabled"] == true {
			disabled++
			if node["ref"] != nil {
				t.Fatal("disabled node became actionable")
			}
		}
		if attrs["type"] == "file" {
			uploads++
			if node["ref"] == nil || attrs["accept"] != "image/*" {
				t.Fatal("hidden file input lost")
			}
		}
		if state["editable"] == true {
			editors++
			if node["ref"] == nil || attrs["placeholder"] != "Compose" {
				t.Fatal("editor lost")
			}
		}
		if state["checked"] == true {
			checked++
		}
		if node["tag"] == "select" && state["value"] == "b" {
			selected++
		}
	}
	if images != 2 || disabled != 1 || uploads != 1 || editors != 1 || checked != 1 || selected != 1 {
		t.Fatalf("generic capabilities lost: images=%d disabled=%d uploads=%d editors=%d checked=%d selected=%d", images, disabled, uploads, editors, checked, selected)
	}
	if treeText(byID["a"]) != "Entry A$11.75" || treeText(byID["b"]) != "Entry B$9.50" {
		t.Fatalf("container/text order lost: a=%q b=%q", treeText(byID["a"]), treeText(byID["b"]))
	}
	text := treeText(root)
	if !strings.Contains(text, "Hello world!") || strings.Contains(text, "$1.00") || strings.Count(text, "11.75") != 1 {
		t.Fatalf("text visibility/order/duplication: %q", text)
	}
	if !strings.Contains(text, "Visible action") || strings.Contains(text, "Hidden parent") || strings.Contains(text, "Hidden child") {
		t.Fatalf("visibility override lost: %q", text)
	}
	capture := page["capture"].(map[string]any)
	if capture["complete"] != false || len(capture["omissions"].([]any)) != 2 {
		t.Fatalf("frame/shadow boundaries not disclosed: %+v", capture)
	}
	status, acted := mustDoJSON(t, "POST", session+"/act", map[string]any{"action": "click", "ref": byID["save"]["id"]})
	if status == 200 {
		t.Fatal("reading ID accepted as action ref")
	}
	status, acted = mustDoJSON(t, "POST", session+"/act", map[string]any{"action": "click", "ref": byID["save"]["ref"]})
	if status != 200 {
		t.Fatalf("action ref regressed: %d %+v", status, acted)
	}
	status, acted = mustDoJSON(t, "POST", session+"/act", map[string]any{"action": "fill", "ref": byID["name"]["ref"], "value": "Bob"})
	if status != 200 {
		t.Fatalf("input ref regressed: %d %+v", status, acted)
	}
	status, after := mustDoJSON(t, "GET", session+"/snapshot", nil)
	encoded, _ := json.Marshal(after.Data)
	if status != 200 || !strings.Contains(string(encoded), `"Done"`) || !strings.Contains(string(encoded), `"value":"Bob"`) {
		t.Fatalf("actions did not affect the real DOM: %s", encoded)
	}
}
