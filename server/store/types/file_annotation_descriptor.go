package types

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// FileAnnotationDescriptor is the frozen descriptor hash contract shared by
// file-open authorization and transactional session-fork reference remapping.
type FileAnnotationDescriptor struct {
	TopicID         string `json:"topic_id"`
	MessageID       int64  `json:"message_id"`
	AttachmentIndex int64  `json:"attachment_index"`
	Name            string `json:"name"`
	URL             string `json:"url"`
	FileKey         string `json:"file_key,omitempty"`
	MimeType        string `json:"mime_type,omitempty"`
	Type            string `json:"type"`
	Size            int64  `json:"size"`
}

func FileAnnotationDescriptorDigest(source FileAnnotationDescriptor) string {
	raw, err := CompactAnnotationsJSON(source)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// CompactAnnotationsJSON preserves the existing browser-compatible annotation
// serialization, including literal separators but not escaped backslashes.
func CompactAnnotationsJSON(value interface{}) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	raw := bytes.TrimRight(buffer.Bytes(), "\n")
	result := make([]byte, 0, len(raw))
	for index := 0; index < len(raw); {
		if raw[index] != '\\' || index+1 >= len(raw) {
			result = append(result, raw[index])
			index++
			continue
		}
		if index+6 <= len(raw) && raw[index+1] == 'u' {
			escape := string(raw[index : index+6])
			if escape == `\u2028` || escape == `\u2029` {
				if escape == `\u2028` {
					result = append(result, []byte("\u2028")...)
				} else {
					result = append(result, []byte("\u2029")...)
				}
				index += 6
				continue
			}
		}
		result = append(result, raw[index], raw[index+1])
		index += 2
	}
	return result, nil
}
