package store

import (
	"strings"
	"testing"
)

func repairEvidenceTest() SkillHubPublicationRepairEvidence {
	return SkillHubPublicationRepairEvidence{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)}
}

func TestSkillHubPublicationRepairAdmissionValidation(t *testing.T) {
	in := SkillHubPublicationRepairInput{Receipt: commitTestInput().Receipt, TopicID: "grp_123", Evidence: repairEvidenceTest()}
	in.Evidence.CandidateHash = in.Receipt.Query.SourceSnapshotHash
	if _, err := in.IdentityHash(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*SkillHubPublicationRepairInput){
		func(x *SkillHubPublicationRepairInput) { x.TopicID = "p2pAnotherHuman" },
		func(x *SkillHubPublicationRepairInput) { x.Receipt.Query.Epoch = 0 },
		func(x *SkillHubPublicationRepairInput) { x.Evidence.CandidateHash = strings.Repeat("f", 64) },
		func(x *SkillHubPublicationRepairInput) { x.Evidence.IssueSetHash = "" },
	} {
		x := in
		change(&x)
		if _, err := x.IdentityHash(); err == nil {
			t.Fatal("invalid admission facts accepted")
		}
	}
	for _, topic := range []string{"grpRepair123", "grp_0", "grp_01", "grp_-1", "grp_9223372036854775808", "grp_1/other"} {
		if _, err := ParseSkillHubPublicationRepairTopic(topic); err == nil {
			t.Fatal("invalid topic", topic)
		}
	}
}

func TestSkillHubPublicationRepairRoundValidation(t *testing.T) {
	in := SkillHubPublicationRepairRoundInput{"round-0001", repairEvidenceTest()}
	h, err := in.IdentityHash()
	if err != nil || len(h) != 64 {
		t.Fatal("valid round", err)
	}
	for _, change := range []func(*SkillHubPublicationRepairRoundInput){
		func(x *SkillHubPublicationRepairRoundInput) { x.RequestID = "bad" },
		func(x *SkillHubPublicationRepairRoundInput) { x.Evidence.CandidateHash = strings.Repeat("A", 64) },
		func(x *SkillHubPublicationRepairRoundInput) { x.Evidence.ReportHash = "missing" },
		func(x *SkillHubPublicationRepairRoundInput) { x.Evidence.IssueSetHash = "" },
	} {
		x := in
		change(&x)
		if _, err := x.IdentityHash(); err == nil {
			t.Fatal("invalid facts accepted")
		}
	}
	x := in
	x.Evidence.ReportHash = strings.Repeat("d", 64)
	if got, _ := x.IdentityHash(); got == h {
		t.Fatal("report not bound")
	}
	for _, worker := range []string{"", "path/to/worker", "worker\nsecret", strings.Repeat("w", 129)} {
		if ValidSkillHubPublicationRepairWorker(worker) {
			t.Fatal("invalid worker")
		}
	}
}

func TestSkillHubPublicationRepairCheckpointsAndStops(t *testing.T) {
	e := repairEvidenceTest()
	task := SkillHubPublicationRepairTask{}
	if task.State() != "queued" {
		t.Fatal("initial state")
	}
	task.Rounds = []SkillHubPublicationRepairRound{{Lease: SkillHubPublicationRepairLease{Input: SkillHubPublicationRepairRoundInput{Evidence: e}}}}
	if task.State() != "running" {
		t.Fatal("reserved state")
	}
	for _, outcome := range []string{"validated", "waiting_confirmation", "rejected"} {
		r := SkillHubPublicationRepairResult{Outcome: outcome, Evidence: e}
		if r.Validate() != nil {
			t.Fatal(outcome)
		}
		task.Rounds[0].Result = &r
		expected := outcome
		if outcome == "rejected" {
			expected = "stopped_no_progress"
		}
		if task.State() != expected {
			t.Fatal("wrong checkpoint state", task.State())
		}
	}
	// Changing only report identity does not make unchanged bytes/issues progress.
	task.Rounds[0].Result.Evidence.ReportHash = strings.Repeat("d", 64)
	if task.State() != "stopped_no_progress" {
		t.Fatal("fresh report reset no-progress")
	}
	task.Rounds[0].Result.Evidence.CandidateHash = strings.Repeat("e", 64)
	if task.State() != "queued" {
		t.Fatal("progress not resumable")
	}
	task.Rounds = append(task.Rounds, task.Rounds[0], task.Rounds[0])
	if task.State() != "stopped_budget" {
		t.Fatal("budget not exhausted")
	}
	for _, r := range []SkillHubPublicationRepairResult{
		{Outcome: "published", Evidence: e},
		{Outcome: "rejected", Evidence: e, Code: "secret/path"},
		{Outcome: "stopped", Evidence: e, Code: "unbounded text"},
		{Outcome: "validated"},
	} {
		if r.Validate() == nil {
			t.Fatal("invalid result accepted")
		}
	}
	for _, code := range []string{"semantic_change", "identity_changed", "safety", "runtime_unavailable", "model_result_uncertain"} {
		r := SkillHubPublicationRepairResult{Outcome: "stopped", Evidence: e, Code: code}
		if r.Validate() != nil {
			t.Fatal("valid stop rejected", code)
		}
	}
}
