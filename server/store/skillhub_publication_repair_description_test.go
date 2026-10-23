package store

import (
	"strings"
	"testing"
)

func TestSkillHubPublicationDescriptionProposalValidation(t *testing.T) {
	index := int64(0)
	valid := SkillHubPublicationDescriptionProposal{RelativePath: "SKILL.md", Field: "description", EntryIndex: &index, Text: "Read a document.\nReturn its contents."}
	for _, tc := range []struct {
		name   string
		change func(*SkillHubPublicationDescriptionProposal)
	}{
		{"empty", func(p *SkillHubPublicationDescriptionProposal) { p.Text = " \n\t" }},
		{"large", func(p *SkillHubPublicationDescriptionProposal) {
			p.Text = strings.Repeat("a", SkillHubPublicationDescriptionDraftMaxBytes+1)
		}},
		{"invalid_utf8", func(p *SkillHubPublicationDescriptionProposal) { p.Text = string([]byte{0xff}) }},
		{"nul", func(p *SkillHubPublicationDescriptionProposal) { p.Text += "\x00" }},
		{"control", func(p *SkillHubPublicationDescriptionProposal) { p.Text += "\x1b" }},
		{"path", func(p *SkillHubPublicationDescriptionProposal) { p.RelativePath = "../SKILL.md" }},
		{"absolute", func(p *SkillHubPublicationDescriptionProposal) { p.RelativePath = "/tmp/SKILL.md" }},
		{"readme", func(p *SkillHubPublicationDescriptionProposal) { p.RelativePath = "README.md" }},
		{"field", func(p *SkillHubPublicationDescriptionProposal) { p.Field = "name" }},
		{"missing_index", func(p *SkillHubPublicationDescriptionProposal) { p.EntryIndex = nil }},
		{"negative_index", func(p *SkillHubPublicationDescriptionProposal) { n := int64(-1); p.EntryIndex = &n }},
		{"manifest_index", func(p *SkillHubPublicationDescriptionProposal) { p.RelativePath = "skill.json" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := valid
			tc.change(&p)
			if p.Validate() == nil {
				t.Fatal("invalid proposal accepted")
			}
		})
	}
	if valid.Validate() != nil {
		t.Fatal("valid entrypoint proposal rejected")
	}
	valid.RelativePath, valid.EntryIndex = "skill.json", nil
	if valid.Validate() != nil {
		t.Fatal("valid manifest proposal rejected")
	}
}

func TestSkillHubPublicationDescriptionExactTextBinding(t *testing.T) {
	index := int64(0)
	in := SkillHubPublicationDescriptionDraftInput{
		TaskID: "pubrepair_" + strings.Repeat("a", 32), TaskIdentityHash: strings.Repeat("b", 64), Round: 1,
		RoundIdentityHash: strings.Repeat("c", 64), Evidence: SkillHubPublicationRepairEvidence{strings.Repeat("d", 64), strings.Repeat("e", 64), strings.Repeat("f", 64)},
		Proposal: SkillHubPublicationDescriptionProposal{"SKILL.md", "description", &index, "Exact draft\r\ntext"},
	}
	key, err := in.IdentityHash()
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*SkillHubPublicationDescriptionDraftInput){
		func(x *SkillHubPublicationDescriptionDraftInput) { x.Proposal.Text += " " },
		func(x *SkillHubPublicationDescriptionDraftInput) {
			x.Proposal.Text = strings.ReplaceAll(x.Proposal.Text, "\r\n", "\n")
		},
		func(x *SkillHubPublicationDescriptionDraftInput) { n := int64(1); x.Proposal.EntryIndex = &n },
		func(x *SkillHubPublicationDescriptionDraftInput) { x.Round = 2 },
		func(x *SkillHubPublicationDescriptionDraftInput) { x.TaskID = "pubrepair_" + strings.Repeat("b", 32) },
		func(x *SkillHubPublicationDescriptionDraftInput) { x.TaskIdentityHash = strings.Repeat("c", 64) },
		func(x *SkillHubPublicationDescriptionDraftInput) { x.RoundIdentityHash = strings.Repeat("d", 64) },
		func(x *SkillHubPublicationDescriptionDraftInput) { x.Evidence.CandidateHash = strings.Repeat("a", 64) },
		func(x *SkillHubPublicationDescriptionDraftInput) { x.Evidence.ReportHash = strings.Repeat("a", 64) },
	} {
		x := in
		change(&x)
		got, err := x.IdentityHash()
		if err != nil || got == key {
			t.Fatal("changed draft retained confirmation key", err)
		}
	}
}
