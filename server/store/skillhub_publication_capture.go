package store

import (
	"context"
	"time"
)

// Metadata authority ceiling, shared with grants so no valid upload size is
// stranded between receipt validation and issuance. A runtime/Cloud transport
// may impose a smaller implementation limit; this does not raise that limit.
const SkillHubPublicationCaptureMaxBytes int64 = SkillHubPublicationUploadMaxBytes

// Internal observation only. An adapter MUST correlate a capture reply with its
// outstanding request and recheck the authenticated socket/body lease before
// calling Record. Never decode a browser/model payload directly into this type.
// Source probes do not contain these digests and cannot stand in for a capture.
type SkillHubPublicationCaptureInput struct {
	EntryID            string
	Binding            SkillHubPublicationDeliveryBinding
	Revision           int
	RequestHash        string
	SourceSnapshotHash string
	RequestBytes       int64
}

func (in SkillHubPublicationCaptureInput) Validate() error {
	if !ValidSkillHubPublicationDeliveryEntryID(in.EntryID) || in.Binding.Validate() != nil ||
		in.Revision != 1 || !publicationSHA256.MatchString(in.RequestHash) ||
		!publicationSHA256.MatchString(in.SourceSnapshotHash) ||
		in.RequestBytes < 1 || in.RequestBytes > SkillHubPublicationCaptureMaxBytes {
		return ErrSkillHubPublicationInvalid
	}
	return nil
}

type SkillHubPublicationCaptureRecord struct {
	Input      SkillHubPublicationCaptureInput
	CapturedAt time.Time
}

// Record keeps the first receipt immutable. Exact candidate replay from a
// currently authenticated reconnect returns that original receipt, never
// replaces it. Cancelled entries/changed owners cannot record or replay.
// No source bytes, local paths or credentials are stored here.
type SkillHubPublicationCaptureStore interface {
	RecordSkillHubPublicationCapture(context.Context, SkillHubPublicationCaptureInput) (*SkillHubPublicationCaptureRecord, error)
}

// Internal live-authority read for upload delivery, not publisher history.
// Binding must come from the current authenticated runtime, never an HTTP body.
type SkillHubPublicationCaptureReader interface {
	ReadSkillHubPublicationCapture(context.Context, string, SkillHubPublicationDeliveryBinding) (*SkillHubPublicationCaptureRecord, error)
}

// Safe historical status only; never serialize capture records, digests or
// lease credentials to the browser. Cancellation wins over retained evidence.
type SkillHubPublicationCaptureStatus struct {
	EntryID, State, LastCode string
	ActorUID                 int64
	Attempts                 int
	NextAttemptAt            *time.Time
	LeaseUntil               *time.Time
	CapturedAt               *time.Time
	ObservedAt               time.Time
}

type SkillHubPublicationCaptureStatusReader interface {
	GetSkillHubPublicationCaptureStatus(context.Context, int64, string) (*SkillHubPublicationCaptureStatus, error)
}
