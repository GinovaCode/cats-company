package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func htmlTestTarget() map[string]interface{} {
	target := fileTestRect()
	target["viewport"] = map[string]interface{}{"width": 800, "height": 600, "scroll_x": 0, "scroll_y": 500, "revision": 0}
	return target
}

func TestHTMLFileAnnotationPersistsRenderedViewportAndAgentContext(t *testing.T) {
	f := newFileBindingFixture(t, "p2p_7_9")
	f.source.MsgType = "file"
	f.source.ContentBlocks[0].Type = "file"
	f.source.ContentBlocks[0].Payload = map[string]interface{}{"name": "report.html", "url": "/uploads/files/report.html", "mime_type": "text/html", "size": 120}
	ref, source := f.openFile(t)
	f.request(t, "POST", "/api/files/annotations", f.token, fileTestSubmit(ref, fileTestDocument(source, "html", htmlTestTarget())), 200)
	saved := f.db.durable[len(f.db.durable)-1]
	raw, err := json.Marshal(saved.Metadata[fileAnnotationsMetadataKey])
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	ann := doc["annotations"].([]interface{})[0].(map[string]interface{})
	view := ann["target"].(map[string]interface{})["viewport"].(map[string]interface{})
	if ann["kind"] != "html" || view["scroll_y"] != float64(500) || view["scroll_x"] != float64(0) || view["revision"] != float64(0) {
		t.Fatalf("lost HTML viewport fields: %s", raw)
	}
	context := f.fileHub.fileAnnotationAgentContext(7, 9, saved.TopicID, saved.Metadata)
	if context == nil || !strings.Contains(context["summary"].(string), "scroll_y=500") {
		t.Fatalf("lost HTML model context: %+v", context)
	}
	normalized, err := normalizeFileAnnotations(doc)
	if err != nil || normalized == nil {
		t.Fatalf("history cannot read canonical HTML annotation: %v", err)
	}
}

func TestHTMLFileAnnotationRejectsInvalidViewport(t *testing.T) {
	for _, scenario := range []string{"missing", "zero-width", "huge-height", "negative-revision", "huge-scroll", "missing-scroll"} {
		t.Run(scenario, func(t *testing.T) {
			target := htmlTestTarget()
			view := target["viewport"].(map[string]interface{})
			switch scenario {
			case "missing":
				delete(target, "viewport")
			case "zero-width":
				view["width"] = 0
			case "huge-height":
				view["height"] = 32769
			case "negative-revision":
				view["revision"] = -1
			case "huge-scroll":
				view["scroll_y"] = 1e8
			case "missing-scroll":
				delete(view, "scroll_x")
			}
			if _, err := normalizeFileAnnotationTarget("html", target); err == nil {
				t.Fatal("accepted invalid HTML viewport")
			}
		})
	}
}
