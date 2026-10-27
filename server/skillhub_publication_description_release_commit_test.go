package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestPublicationDescriptionReleaseCommitClientHTTPS(t *testing.T) {
	app, intent, permit := releaseAuthorizeClientFixture(t, "https://cloud.example")
	var calls int
	want := publicationDescriptionCloudReleaseAdmission{SchemaVersion: 1, ApplicationID: app.ID,
		TaskID: app.Input.Receipt.TaskID, PermitID: permit.PermitID, State: "queued"}
	replayed := false
	want.Replayed = &replayed
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != publicationDescriptionReleaseCommitPath ||
			r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" || r.Header.Get("Accept-Encoding") != "identity" {
			t.Error("invalid service request")
		}
		raw, _ := io.ReadAll(r.Body)
		request, err := publicationExactObject(raw, "schemaVersion", "applicationId", "permitId")
		if err != nil || string(request["applicationId"]) != `"`+app.ID+`"` || string(request["permitId"]) != `"`+permit.PermitID+`"` {
			t.Error("request is not bound to the exact retained permit", err)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(want)
	}))
	defer s.Close()
	cfg := derivationClientConfig(s)
	cfg.PlanEnabled, cfg.ReleaseAuthorizeEnabled, cfg.ReleaseCommitEnabled = true, true, true
	c, err := NewPublicationDescriptionDerivationClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.AdmitRetainedRelease(context.Background(), app, intent, permit)
	if err != nil || got == nil || !reflect.DeepEqual(*got, want) || calls != 1 {
		t.Fatalf("bad admission response: err=%v got=%+v want=%+v calls=%d", err, got, want, calls)
	}
}

func TestPublicationDescriptionReleaseCommitClientFailsClosed(t *testing.T) {
	for _, mode := range []string{"wrong-application", "wrong-task", "wrong-permit", "unknown-state", "null-replayed", "cacheable", "encoding", "redirect", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			app, intent, permit := releaseAuthorizeClientFixture(t, "https://cloud.example")
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect" {
					w.Header().Set("Location", "https://attacker.invalid/")
					w.WriteHeader(http.StatusFound)
					return
				}
				out := publicationDescriptionCloudReleaseAdmission{SchemaVersion: 1, ApplicationID: app.ID,
					TaskID: app.Input.Receipt.TaskID, PermitID: permit.PermitID, State: "queued"}
				if mode == "wrong-application" {
					out.ApplicationID = "pubdesc_" + strings.Repeat("f", 64)
				}
				if mode == "wrong-task" {
					out.TaskID = "pubtask_22345678-1234-4123-8123-123456789abc"
				}
				if mode == "wrong-permit" {
					out.PermitID = "pubdescpermit_" + strings.Repeat("f", 32)
				}
				if mode == "unknown-state" {
					out.State = "legacy_commit"
				}
				replayed := false
				out.Replayed = &replayed
				if mode == "null-replayed" {
					out.Replayed = nil
				}
				if mode == "duplicate" {
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					w.Header().Set("Cache-Control", "no-store")
					_, _ = io.WriteString(w, `{"schemaVersion":1,"applicationId":"`+app.ID+`","applicationId":"`+app.ID+
						`","taskId":"`+out.TaskID+`","permitId":"`+permit.PermitID+`","state":"queued","replayed":false}`)
					return
				}
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("Cache-Control", "no-store")
				if mode == "cacheable" {
					w.Header().Set("Cache-Control", "public, max-age=60")
				}
				if mode == "encoding" {
					w.Header().Set("Content-Encoding", "gzip")
				}
				_ = json.NewEncoder(w).Encode(out)
			}))
			defer s.Close()
			cfg := derivationClientConfig(s)
			cfg.PlanEnabled, cfg.ReleaseAuthorizeEnabled, cfg.ReleaseCommitEnabled = true, true, true
			c, err := NewPublicationDescriptionDerivationClient(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := c.AdmitRetainedRelease(context.Background(), app, intent, permit); got != nil || !errors.Is(err, ErrSkillHubPublicationDescriptionReleaseCommitPeer) {
				t.Fatalf("unsafe response accepted: got=%+v err=%v", got, err)
			}
		})
	}
}

func TestPublicationDescriptionReleaseCommitClientIsDefaultOff(t *testing.T) {
	app, intent, permit := releaseAuthorizeClientFixture(t, "https://cloud.example")
	c := &PublicationDescriptionDerivationClient{}
	if got, err := c.AdmitRetainedRelease(context.Background(), app, intent, permit); got != nil || !errors.Is(err, ErrSkillHubPublicationDescriptionReleaseCommitDisabled) {
		t.Fatalf("disabled client admitted a release: got=%+v err=%v", got, err)
	}
}
