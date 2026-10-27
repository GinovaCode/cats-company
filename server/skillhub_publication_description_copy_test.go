package server

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

type descriptionCopyTestStore struct {
	app   *store.SkillHubPublicationDescriptionApplication
	get   int
	onGet func(int) error
}

func (s *descriptionCopyTestStore) GetSkillHubPublicationDescriptionApplication(_ context.Context, actor int64, id string) (*store.SkillHubPublicationDescriptionApplication, error) {
	s.get++
	if s.onGet != nil {
		if err := s.onGet(s.get); err != nil {
			return nil, err
		}
	}
	if s.app != nil && (actor != 1 || id != s.app.Input.Receipt.Query.EntryID) {
		return nil, store.ErrSkillHubPublicationDenied
	}
	return s.app, nil
}

func TestReadConfirmedPublicationDescriptionReadOnlyRecovery(t *testing.T) {
	raw := []byte("private-derived-bytes")
	app := completedDescriptionApplication(t, "https://hub.example", raw)
	db := &descriptionCopyTestStore{app: &app}
	baseline := snapshotPublicationDescriptionApplication(app)
	var events []string
	var peerBuffer []byte
	db.onGet = func(n int) error { events = append(events, "authority"); return nil }
	ports := publicationDescriptionCopyPorts{
		VerifySource: func(_ context.Context, a store.SkillHubPublicationDescriptionApplication) error {
			events = append(events, "source")
			if !reflect.DeepEqual(a, baseline) {
				t.Fatal("source verification changed its binding")
			}
			// Ports get isolated snapshots, even for nested pointers.
			*a.Input.Draft.Proposal.EntryIndex = 99
			a.Result.RequestBytes++
			*a.AppliedAt = time.Time{}
			if len(peerBuffer) > 0 {
				peerBuffer[0] = 'Z' // The coordinator must already own its copy.
			}
			return nil
		},
		ReadRetainedCopy: func(_ context.Context, a store.SkillHubPublicationDescriptionApplication) ([]byte, error) {
			events = append(events, "copy")
			if !reflect.DeepEqual(a, baseline) {
				t.Fatal("copy read changed its binding")
			}
			peerBuffer = append([]byte(nil), raw...)
			return peerBuffer, nil
		},
	}
	for i := 0; i < 2; i++ {
		got, err := readConfirmedPublicationDescription(context.Background(), db, 1, app.Input.Receipt.Query.EntryID, ports)
		if err != nil || got == nil || string(got.bytes) != string(raw) || !reflect.DeepEqual(got.application, baseline) || !reflect.DeepEqual(app, baseline) {
			t.Fatalf("recovered copy mismatch: %v %v", got, err)
		}
		got.bytes[0] = 'X'
		*got.application.Input.Draft.Proposal.EntryIndex = 88
		if !reflect.DeepEqual(app, baseline) || string(raw) != "private-derived-bytes" {
			t.Fatal("returned object aliases port/store")
		}
	}
	want := []string{"authority", "source", "authority", "copy", "source", "authority", "authority", "source", "authority", "copy", "source", "authority"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("wrong check order: %v", events)
	}
}

func TestReadConfirmedPublicationDescriptionRejectsIncompleteAndChangedCheckpoints(t *testing.T) {
	raw := []byte("private-derived-bytes")
	for _, step := range []int{1, 2, 3} {
		for _, kind := range []string{"denied", "missing", "incomplete", "binding", "receipt", "size", "applied"} {
			t.Run(string(rune('0'+step))+"/"+kind, func(t *testing.T) {
				app := completedDescriptionApplication(t, "https://hub.example", raw)
				db := &descriptionCopyTestStore{app: &app}
				id := app.Input.Receipt.Query.EntryID
				reads, verifies := 0, 0
				db.onGet = func(n int) error {
					if n != step {
						return nil
					}
					switch kind {
					case "denied":
						return store.ErrSkillHubPublicationDenied
					case "missing":
						db.app = nil
					case "incomplete":
						app.Result = nil
						app.AppliedAt = nil
					case "binding":
						app.Input.Draft.Proposal.Text += " changed"
						app.Input.ConfirmationKey, _ = app.Input.Draft.IdentityHash()
						app.IdentityHash, _ = app.Input.IdentityHash()
						app.ID = "pubdesc_" + app.IdentityHash
						app.Result.ApplicationID = app.ID
						app.Result.Derivation.ProposalDigest = publicationDescriptionProposalDigest(app.Input.Draft.Proposal)
					case "receipt":
						app.Result.Derivation.ValidationDigest = strings.Repeat("f", 64)
					case "size":
						app.Result.RequestBytes++
					case "applied":
						*app.AppliedAt = app.AppliedAt.Add(time.Second)
					}
					return nil
				}
				ports := publicationDescriptionCopyPorts{
					VerifySource: func(context.Context, store.SkillHubPublicationDescriptionApplication) error { verifies++; return nil },
					ReadRetainedCopy: func(context.Context, store.SkillHubPublicationDescriptionApplication) ([]byte, error) {
						reads++
						return raw, nil
					},
				}
				got, err := readConfirmedPublicationDescription(context.Background(), db, 1, id, ports)
				// A self-consistent different completed application at the FIRST
				// current-authority read is valid. Later reads must be identical.
				validInitial := step == 1 && (kind == "binding" || kind == "receipt" || kind == "applied")
				if validInitial {
					if err != nil || got == nil {
						t.Fatalf("valid current application rejected: %v", err)
					}
					return
				}
				if err == nil || got != nil {
					t.Fatalf("changed checkpoint returned bytes: %v %v", got, err)
				}
				if step < 3 && !(step == 1 && kind == "size") && reads != 0 {
					t.Fatal("read after failed early checkpoint")
				}
				if verifies > 2 {
					t.Fatal("unexpected source retry")
				}
			})
		}
	}
}

func TestReadConfirmedPublicationDescriptionRejectsPortFailuresAndBytes(t *testing.T) {
	raw := []byte("private-derived-bytes")
	for _, kind := range []string{"first source", "second source", "cloud", "empty", "truncated", "extra", "hash", "cancel source", "cancel cloud", "cancel checkpoint"} {
		t.Run(kind, func(t *testing.T) {
			app := completedDescriptionApplication(t, "https://hub.example", raw)
			db := &descriptionCopyTestStore{app: &app}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reads, verifies := 0, 0
			failure := errors.New("private source no longer matches")
			ports := publicationDescriptionCopyPorts{
				VerifySource: func(context.Context, store.SkillHubPublicationDescriptionApplication) error {
					verifies++
					if kind == "first source" || (kind == "second source" && verifies == 2) {
						return failure
					}
					if kind == "cancel source" {
						cancel()
					}
					return nil
				},
				ReadRetainedCopy: func(context.Context, store.SkillHubPublicationDescriptionApplication) ([]byte, error) {
					reads++
					switch kind {
					case "cloud":
						return raw, failure
					case "empty":
						return nil, nil
					case "truncated":
						return raw[:len(raw)-1], nil
					case "extra":
						return append(append([]byte(nil), raw...), '!'), nil
					case "hash":
						return []byte(strings.Repeat("X", len(raw))), nil
					case "cancel cloud":
						cancel()
					}
					return raw, nil
				},
			}
			if kind == "cancel checkpoint" {
				db.onGet = func(n int) error {
					if n == 3 {
						cancel()
					}
					return nil
				}
			}
			got, err := readConfirmedPublicationDescription(ctx, db, 1, app.Input.Receipt.Query.EntryID, ports)
			if got != nil || err == nil {
				t.Fatalf("bad read returned copy: %v %v", got, err)
			}
			if strings.HasPrefix(kind, "cancel") && !errors.Is(err, context.Canceled) {
				t.Fatalf("wrong cancellation: %v", err)
			}
			if (kind == "first source" || kind == "cancel source") && reads != 0 {
				t.Fatal("read after failed source")
			}
		})
	}
}

func TestReadConfirmedPublicationDescriptionRejectsInvalidRequest(t *testing.T) {
	app := completedDescriptionApplication(t, "https://hub.example", []byte("private"))
	db := &descriptionCopyTestStore{app: &app}
	ports := publicationDescriptionCopyPorts{
		VerifySource: func(context.Context, store.SkillHubPublicationDescriptionApplication) error {
			t.Fatal("invalid request called source")
			return nil
		},
		ReadRetainedCopy: func(context.Context, store.SkillHubPublicationDescriptionApplication) ([]byte, error) {
			t.Fatal("invalid request called Cloud")
			return nil, nil
		},
	}
	for _, tc := range []struct {
		actor int64
		id    string
		db    publicationDescriptionCopyStore
		ports publicationDescriptionCopyPorts
	}{
		{0, app.Input.Receipt.Query.EntryID, db, ports}, {1, "invalid", db, ports}, {1, app.Input.Receipt.Query.EntryID, nil, ports},
		{1, app.Input.Receipt.Query.EntryID, db, publicationDescriptionCopyPorts{}},
	} {
		if got, err := readConfirmedPublicationDescription(context.Background(), tc.db, tc.actor, tc.id, tc.ports); got != nil || err != store.ErrSkillHubPublicationInvalid {
			t.Fatal("invalid request accepted", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := readConfirmedPublicationDescription(ctx, db, 1, app.Input.Receipt.Query.EntryID, ports); got != nil || err != context.Canceled {
		t.Fatal(err)
	}
	if db.get != 0 {
		t.Fatal("invalid request reached store")
	}
}
