package sessionfork

import (
	"database/sql"
	"encoding/json"
	"testing"
)

func TestRemapMetadataRetainsAttachmentsAndMapsAnnotationReference(t *testing.T) {
	raw := sql.NullString{Valid: true, String: `{"file_annotations":{"source":{"topic_id":"p2p_7_9","message_id":10,"attachment_index":0,"name":"report","url":"/uploads/files/retained.pdf","type":"file","size":12,"version":"old"},"annotations":[{"body":"keep this"}]},"custom":{"value":9007199254740991}}`}
	got, err := RemapMetadata(raw, "p2p_7_9", "grp_20", map[string]int64{"10": 100})
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err = json.Unmarshal([]byte(got.String), &root); err != nil {
		t.Fatal(err)
	}
	var annotation struct {
		Source struct {
			TopicID      string `json:"topic_id"`
			MessageID    int64  `json:"message_id"`
			URL, Version string
		}
		Annotations []struct{ Body string }
	}
	if err = json.Unmarshal(root["file_annotations"], &annotation); err != nil {
		t.Fatal(err)
	}
	if annotation.Source.TopicID != "grp_20" || annotation.Source.MessageID != 100 || annotation.Source.URL != "/uploads/files/retained.pdf" || len(annotation.Source.Version) != 64 || annotation.Annotations[0].Body != "keep this" {
		t.Fatalf("bad clone: %s", got.String)
	}
	if string(root["custom"]) != `{"value":9007199254740991}` {
		t.Fatalf("lost custom data: %s", root["custom"])
	}
	if raw.String == got.String {
		t.Fatal("reference was not rewritten")
	}
}

func TestRemapMetadataStripsDeliveryCredentials(t *testing.T) {
	raw := sql.NullString{Valid: true, String: `{"catsco_identity":{"device_grants":[{"token":"secret"}]},"xiaoba_runtime":{"device_token":"secret"},"catsco_skill_connectors":{"access_token":"secret"},"artifact_ref":{"token":"secret"},"artifact_page_context":{"open_ref":"secret"},"artifact_context_ref":"secret","catsco_gateway_annotation_context":{"token":"secret"},"__catsco_channel_binding_delivery_trust":"secret","catsco_file_annotations_trusted":true,"device_grants":["secret"],"custom":"retain"}`}
	got, err := RemapMetadata(raw, "p2p_7_9", "grp_20", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.String != `{"custom":"retain"}` {
		t.Fatalf("copied runtime authority: %s", got.String)
	}
}
