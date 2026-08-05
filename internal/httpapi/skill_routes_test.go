package httpapi

import (
	"bytes"
	"mime/multipart"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/skills"

	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
)

func TestParseSkillOwner(t *testing.T) {
	principal := identitystore.PrincipalRecord{ID: uuid.New(), Type: identitystore.PrincipalTypeUser}
	projectUUID := uuid.New()
	projectID, err := publicid.Encode(publicid.KindProject, projectUUID)
	if err != nil {
		t.Fatalf("encode project id: %v", err)
	}
	tests := []struct {
		name string
		raw  string
		want skillstore.SkillOwner
	}{
		{name: "org", raw: `{"kind":"org"}`, want: skillstore.SkillOwner{Kind: skillstore.SkillOwnerOrg}},
		{
			name: "project",
			raw:  `{"kind":"project","project_id":"` + projectID + `"}`,
			want: skillstore.SkillOwner{Kind: skillstore.SkillOwnerProject, ProjectID: projectUUID},
		},
		{
			name: "user resolves to caller",
			raw:  `{"kind":"user"}`,
			want: skillstore.SkillOwner{Kind: skillstore.SkillOwnerUser, UserID: principal.ID},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSkillOwner([]byte(tc.raw), principal)
			if err != nil {
				t.Fatalf("parse owner: %v", err)
			}
			if got != tc.want {
				t.Fatalf("owner = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestReadSkillUploadIncludesExplicitOwner(t *testing.T) {
	principal := identitystore.PrincipalRecord{ID: uuid.New(), Type: identitystore.PrincipalTypeUser}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	owner, err := writer.CreateFormField("owner")
	if err != nil {
		t.Fatalf("create owner field: %v", err)
	}
	if _, err := owner.Write([]byte(`{"kind":"user"}`)); err != nil {
		t.Fatalf("write owner: %v", err)
	}
	archive, err := writer.CreateFormFile("archive", "skill.tar.gz")
	if err != nil {
		t.Fatalf("create archive field: %v", err)
	}
	if _, err := archive.Write([]byte("archive bytes")); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	reader := multipart.NewReader(bytes.NewReader(body.Bytes()), writer.Boundary())
	upload, err := readSkillUpload(reader, principal)
	if err != nil {
		t.Fatalf("read skill upload: %v", err)
	}
	if upload.owner.Kind != skillstore.SkillOwnerUser || upload.owner.UserID != principal.ID {
		t.Fatalf("owner = %+v", upload.owner)
	}
	if upload.filename != "skill.tar.gz" || string(upload.archive) != "archive bytes" {
		t.Fatalf("upload = %+v", upload)
	}
}

func TestSkillDownloadCapabilityIsBoundToAuthenticatedMachine(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	machineA := uuid.New()
	machineB := uuid.New()
	machineAPublicID, err := publicid.Encode(publicid.KindMachine, machineA)
	if err != nil {
		t.Fatalf("encode machine id: %v", err)
	}
	token, expiresAt, err := skills.MintDownloadToken(
		key,
		"skl_test",
		"skr_test",
		machineAPublicID,
		now,
	)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	principal := identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeMachineDaemon, ID: machineA}
	if err := verifySkillDownloadCapability(
		key,
		principal,
		"skl_test",
		"skr_test",
		token,
		expiresAt,
		now,
	); err != nil {
		t.Fatalf("verify matching machine: %v", err)
	}
	principal.ID = machineB
	if err := verifySkillDownloadCapability(
		key,
		principal,
		"skl_test",
		"skr_test",
		token,
		expiresAt,
		now,
	); err == nil {
		t.Fatal("capability minted for another machine was accepted")
	}
}
