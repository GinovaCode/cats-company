package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"time"
)

const SkillHubPublicationRepairMaxRounds = 3
const SkillHubPublicationRepairLeaseTTL = 2 * time.Minute

var publicationRepairTopic = regexp.MustCompile(`^grp[A-Za-z0-9_-]{1,128}$`)
var publicationRepairID = regexp.MustCompile(`^pubrepair_[a-f0-9]{32}$`)
var publicationRepairToken = regexp.MustCompile(`^[a-f0-9]{32}$`)

func ValidSkillHubPublicationRepairWorker(id string) bool {
	return publicationReference.MatchString(id)
}

// These digests are derived by the trusted report adapter, NEVER the model or
// browser. ReportHash covers the exact verified report; IssueSetHash covers its
// normalized issue identities (excluding fresh report IDs and display text).
// They are integrity references, not proof that a change is safe or authorized.
type SkillHubPublicationRepairEvidence struct {
	CandidateHash string `json:"candidateHash"`
	ReportHash    string `json:"reportHash"`
	IssueSetHash  string `json:"issueSetHash"`
}

func (e SkillHubPublicationRepairEvidence) Validate() error {
	for _, h := range []string{e.CandidateHash, e.ReportHash, e.IssueSetHash} {
		if !publicationSHA256.MatchString(h) {
			return ErrSkillHubPublicationInvalid
		}
	}
	return nil
}

// Internal admission facts. A future human adapter must independently verify
// the rejected Cloud report, private agent_task topic (owner and sole Bot),
// explicit repair action and exact receipt before calling this store. A topic
// string or digest supplied by a browser is not such verification.
type SkillHubPublicationRepairInput struct {
	Receipt  SkillHubPublicationCloudReceipt   `json:"receipt"`
	TopicID  string                            `json:"topicId"`
	Evidence SkillHubPublicationRepairEvidence `json:"evidence"`
}

func (in SkillHubPublicationRepairInput) Validate() error {
	if in.Receipt.Validate() != nil || !publicationRepairTopic.MatchString(in.TopicID) ||
		in.Evidence.Validate() != nil || in.Evidence.CandidateHash != in.Receipt.Query.SourceSnapshotHash {
		return ErrSkillHubPublicationInvalid
	}
	return nil
}

func repairIdentity(domain string, in any) (string, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(append([]byte(domain+"\x00"), b...))
	return hex.EncodeToString(h[:]), nil
}

func (in SkillHubPublicationRepairInput) IdentityHash() (string, error) {
	if err := in.Validate(); err != nil {
		return "", err
	}
	return repairIdentity("catsco-publication-repair-v1", in)
}

type SkillHubPublicationRepairRoundInput struct {
	RequestID string                            `json:"requestId"`
	Evidence  SkillHubPublicationRepairEvidence `json:"evidence"`
}

func (in SkillHubPublicationRepairRoundInput) Validate() error {
	if !ValidSkillHubPublicationRequestID(in.RequestID) || in.Evidence.Validate() != nil {
		return ErrSkillHubPublicationInvalid
	}
	return nil
}

func (in SkillHubPublicationRepairRoundInput) IdentityHash() (string, error) {
	if err := in.Validate(); err != nil {
		return "", err
	}
	return repairIdentity("catsco-publication-repair-round-v1", in)
}

// Only a trusted executor/revalidation adapter may record this checkpoint.
// validated means validation passed, NOT that a Skill was published. Waiting
// confirmation cannot be resumed by this store; exact human draft approval is
// separate work. Neither checkpoint nor lease authorizes editing or model use.
type SkillHubPublicationRepairResult struct {
	Outcome  string                            `json:"outcome"`
	Evidence SkillHubPublicationRepairEvidence `json:"evidence"`
	Code     string                            `json:"code"`
}

func (r SkillHubPublicationRepairResult) Validate() error {
	if r.Evidence.Validate() != nil {
		return ErrSkillHubPublicationInvalid
	}
	switch r.Outcome {
	case "rejected", "validated", "waiting_confirmation":
		if r.Code == "" {
			return nil
		}
	case "stopped":
		switch r.Code {
		case "semantic_change", "identity_changed", "safety", "runtime_unavailable", "model_result_uncertain":
			return nil
		}
	}
	return ErrSkillHubPublicationInvalid
}

type SkillHubPublicationRepairLease struct {
	EntryID    string
	TaskID     string
	Round      int
	Input      SkillHubPublicationRepairRoundInput
	WorkerID   string
	Token      string
	Generation int64
	Until      time.Time
}

func (l SkillHubPublicationRepairLease) Validate() error {
	if !ValidSkillHubPublicationDeliveryEntryID(l.EntryID) || !publicationRepairID.MatchString(l.TaskID) ||
		l.Round < 1 || l.Round > SkillHubPublicationRepairMaxRounds || l.Input.Validate() != nil ||
		!publicationReference.MatchString(l.WorkerID) || !publicationRepairToken.MatchString(l.Token) ||
		l.Generation < 1 || l.Until.IsZero() {
		return ErrSkillHubPublicationInvalid
	}
	return nil
}

type SkillHubPublicationRepairRound struct {
	Lease  SkillHubPublicationRepairLease
	Result *SkillHubPublicationRepairResult
}

type SkillHubPublicationRepairTask struct {
	ID           string
	Input        SkillHubPublicationRepairInput
	IdentityHash string
	CreatedAt    time.Time
	Rounds       []SkillHubPublicationRepairRound
}

// State derives progress from retained checkpoints. Even a newly issued report
// cannot disguise no progress when candidate bytes and the issue set are equal.
func (t SkillHubPublicationRepairTask) State() string {
	if len(t.Rounds) == 0 {
		return "queued"
	}
	r := t.Rounds[len(t.Rounds)-1]
	if r.Result == nil {
		return "running"
	}
	if r.Result.Outcome != "rejected" {
		return r.Result.Outcome
	}
	if r.Result.Evidence.CandidateHash == r.Lease.Input.Evidence.CandidateHash &&
		r.Result.Evidence.IssueSetHash == r.Lease.Input.Evidence.IssueSetHash {
		return "stopped_no_progress"
	}
	if len(t.Rounds) == SkillHubPublicationRepairMaxRounds {
		return "stopped_budget"
	}
	return "queued"
}

// Dormant, optional persistent accounting only: no handler, worker, task event,
// model call or file edit is registered. All mutations serialize with entry
// cancellation and current human/owner/body checks. No in-memory fallback.
// Reclaiming an expired lease resumes the SAME round and fences the old worker;
// it must not be interpreted as permission to repeat an uncertain paid call.
type SkillHubPublicationRepairStore interface {
	CreateSkillHubPublicationRepairTask(context.Context, int64, SkillHubPublicationRepairInput) (*SkillHubPublicationRepairTask, error)
	GetSkillHubPublicationRepairTask(context.Context, int64, string) (*SkillHubPublicationRepairTask, error)
	ClaimSkillHubPublicationRepairRound(context.Context, int64, string, SkillHubPublicationRepairRoundInput, string) (*SkillHubPublicationRepairRound, error)
	FinishSkillHubPublicationRepairRound(context.Context, int64, SkillHubPublicationRepairLease, SkillHubPublicationRepairResult) error
}
