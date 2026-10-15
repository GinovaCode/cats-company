package server

// Normalize the runtime's controlled request failures into the existing public
// publication contract. Do not persist raw runtime messages or add DB outcomes.
// A bad request/binding needs intervention; retrying the same payload cannot fix
// it. An expired delivery may be retried with a fresh server-issued request.
func normalizePublicationProbeRequestError(code string) string {
	switch code {
	case "INVALID_REQUEST", "OWNER_MISMATCH", "DEVICE_MISMATCH", "BOT_NOT_ACTIVE":
		return "publication.runtime_unsupported"
	case "REQUEST_EXPIRED":
		return "publication.runtime_timeout"
	default:
		return code
	}
}
