package sessionfork

import (
	"database/sql"
	"encoding/json"
	"strconv"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// RemapMetadata preserves durable metadata and file URLs. Annotation references
// follow their cloned messages; ephemeral delivery capabilities are not history.
func RemapMetadata(raw sql.NullString, source, target string, ids map[string]int64) (sql.NullString, error) {
	if !raw.Valid || raw.String == "" || raw.String == "null" {
		return raw, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw.String), &root); err != nil {
		return raw, err
	}
	changed := false
	for _, key := range []string{
		"artifact_context", "artifact_task", "artifact_task_ref", "artifact_ref", "artifact_page_context", "artifact_context_ref",
		"gateway_annotation_context", "catsco_gateway_annotation_context", "file_annotations_context", "catsco_file_annotations_trusted", "open_ref",
		"catsco_identity", "xiaoba_runtime", "catsco_skill_connectors", "device_grants", "device_access_token", "device_token", "runtime_token",
		"__catsco_channel_binding_delivery_trust", "channel_device_access_enabled",
	} {
		if _, ok := root[key]; ok {
			delete(root, key)
			changed = true
		}
	}
	if value, ok := root["file_annotations"]; ok {
		var document map[string]json.RawMessage
		if err := json.Unmarshal(value, &document); err != nil {
			return raw, err
		}
		var descriptor types.FileAnnotationDescriptor
		if err := json.Unmarshal(document["source"], &descriptor); err != nil {
			return raw, err
		}
		mapped, ok := ids[strconv.FormatInt(descriptor.MessageID, 10)]
		if !ok || descriptor.TopicID != source {
			return raw, store.ErrSessionForkSource
		}
		// Keep undeclared dimensions and all future descriptor extension fields.
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(document["source"], &fields); err != nil {
			return raw, err
		}
		descriptor.TopicID, descriptor.MessageID = target, mapped
		fields["topic_id"], _ = json.Marshal(target)
		fields["message_id"], _ = json.Marshal(mapped)
		fields["version"], _ = json.Marshal(types.FileAnnotationDescriptorDigest(descriptor))
		document["source"], _ = json.Marshal(fields)
		root["file_annotations"], _ = json.Marshal(document)
		changed = true
	}
	if !changed {
		return raw, nil
	}
	encoded, err := json.Marshal(root)
	return sql.NullString{String: string(encoded), Valid: true}, err
}
