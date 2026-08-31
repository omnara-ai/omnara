package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/skills"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	skillMaxUploadBytes = skills.MaxArchiveBytes
	skillMaxOwnerBytes  = 16 * 1024
)

func newSkillOwner(record skillstore.SkillRecord) (openapi.SkillOwner, error) {
	var owner openapi.SkillOwner
	switch record.OwnerKind {
	case skillstore.SkillOwnerOrg:
		err := owner.FromOrgSkillOwner(openapi.OrgSkillOwner{Kind: openapi.OrgSkillOwnerKindOrg})
		return owner, err
	case skillstore.SkillOwnerProject:
		projectID, err := publicID(publicid.KindProject, record.OwnerProjectID)
		if err != nil {
			return owner, err
		}
		err = owner.FromProjectSkillOwner(openapi.ProjectSkillOwner{
			Kind: openapi.ProjectSkillOwnerKindProject, ProjectId: projectID,
		})
		return owner, err
	case skillstore.SkillOwnerUser:
		userID, err := publicID(publicid.KindUser, record.OwnerUserID)
		if err != nil {
			return owner, err
		}
		err = owner.FromUserSkillOwner(openapi.UserSkillOwner{
			Kind: openapi.UserSkillOwnerKindUser, UserId: userID,
		})
		return owner, err
	default:
		return owner, errors.New("unsupported skill owner kind")
	}
}

func skillResponseFromRecord(rec skillstore.SkillRecord, includeBody bool) (openapi.Skill, error) {
	id, err := publicID(publicid.KindSkill, rec.ID)
	if err != nil {
		return openapi.Skill{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, rec.OrgID)
	if err != nil {
		return openapi.Skill{}, err
	}
	revisionID, err := publicID(publicid.KindSkillRevision, rec.RevisionID)
	if err != nil {
		return openapi.Skill{}, err
	}
	owner, err := newSkillOwner(rec)
	if err != nil {
		return openapi.Skill{}, err
	}
	resp := openapi.Skill{
		Id: id, OrgId: orgID, Owner: owner, Name: rec.Name,
		RevisionId: revisionID, Revision: rec.Revision, Description: rec.Description,
		CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt,
	}
	if includeBody {
		resp.SkillMd = &rec.SkillMd
	}
	return resp, nil
}

func skillGrantResponseFromRecord(record skillstore.SkillGrantRecord) (openapi.SkillGrant, error) {
	id, err := publicID(publicid.KindSkillGrant, record.ID)
	if err != nil {
		return openapi.SkillGrant{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.SkillGrant{}, err
	}
	skillID, err := publicID(publicid.KindSkill, record.SkillID)
	if err != nil {
		return openapi.SkillGrant{}, err
	}
	projectID, err := publicID(publicid.KindProject, record.TargetProjectID)
	if err != nil {
		return openapi.SkillGrant{}, err
	}
	return openapi.SkillGrant{
		Id: id, OrgId: orgID, SkillId: skillID, TargetProjectId: projectID,
		CreatedAt: record.CreatedAt,
	}, nil
}

func skillAPIError(ctx context.Context, err error) apierror.ResponseError {
	switch {
	case errors.Is(err, storeerr.ErrUnauthorized):
		return apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
	case errors.Is(err, storeerr.ErrConflict):
		return apierror.FromCode(openapi.ErrorCodeConflict, "conflict")
	case errors.Is(err, storeerr.ErrInvalidSkillName):
		return apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	case errors.Is(err, storeerr.ErrInvalidRequest):
		return apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid skill request")
	case storeerr.IsNotFound(err):
		return apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	default:
		logpkg.Error(ctx, err)
		return apierror.FromCode(openapi.ErrorCodeInternalError, "internal server error")
	}
}

func (s strictOpenAPIServer) CreateSkill(
	ctx context.Context,
	request openapi.CreateSkillRequestObject,
) (openapi.CreateSkillResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	principal, principalErr := accountPrincipalFromContext(ctx)
	if principalErr != nil {
		return nil, *principalErr
	}
	upload, err := readSkillUpload(request.Body, principal)
	if err != nil {
		return nil, err
	}
	format, ok := skills.DetectFormat(upload.filename, upload.archive)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "archive must be .zip or .tar.gz")
	}
	meta, err := skills.ExtractMetadata(format, upload.archive)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	record, err := s.server.skills.CreateSkillRevision(ctx, skillstore.CreateSkillInput{
		OrgID: org.ID, OwnerKind: upload.owner.Kind,
		OwnerProjectID: upload.owner.ProjectID, OwnerUserID: upload.owner.UserID,
		Name: meta.Name, Description: meta.Description, SkillMd: meta.SkillMd,
		ArchiveBytes: upload.archive, Actor: principal,
	})
	if err != nil {
		return nil, skillAPIError(ctx, err)
	}
	response, err := skillResponseFromRecord(record, true)
	if err != nil {
		return nil, err
	}
	return openapi.CreateSkill201JSONResponse(response), nil
}

type skillUpload struct {
	owner    skillstore.SkillOwner
	archive  []byte
	filename string
}

func readSkillUpload(body *multipart.Reader, principal identitystore.PrincipalRecord) (skillUpload, error) {
	if body == nil {
		return skillUpload{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "multipart body is required")
	}
	var upload skillUpload
	var ownerSet, archiveSet bool
	for {
		part, err := body.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return skillUpload{}, apierror.FromCode(
				openapi.ErrorCodeInvalidRequest,
				fmt.Sprintf("parse multipart form: %v", err),
			)
		}
		switch part.FormName() {
		case "owner":
			if ownerSet {
				_ = part.Close()
				return skillUpload{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "owner must be provided once")
			}
			raw, readErr := io.ReadAll(io.LimitReader(part, skillMaxOwnerBytes+1))
			_ = part.Close()
			if readErr != nil {
				return skillUpload{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, fmt.Sprintf("read owner: %v", readErr))
			}
			if len(raw) > skillMaxOwnerBytes {
				return skillUpload{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "owner is too large")
			}
			owner, parseErr := parseSkillOwner(raw, principal)
			if parseErr != nil {
				return skillUpload{}, parseErr
			}
			upload.owner, ownerSet = owner, true
		case "archive":
			if archiveSet {
				_ = part.Close()
				return skillUpload{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "archive must be provided once")
			}
			upload.archive, upload.filename, err = readSkillArchivePart(part)
			if err != nil {
				return skillUpload{}, err
			}
			archiveSet = true
		default:
			_ = part.Close()
			return skillUpload{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "unsupported multipart field")
		}
	}
	if !ownerSet {
		return skillUpload{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "owner is required")
	}
	if !archiveSet || len(upload.archive) == 0 {
		return skillUpload{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "archive file is required")
	}
	return upload, nil
}

func readSkillArchivePart(part *multipart.Part) ([]byte, string, error) {
	filename := part.FileName()
	archive, err := io.ReadAll(io.LimitReader(part, skillMaxUploadBytes+1))
	_ = part.Close()
	if err != nil {
		return nil, "", apierror.FromCode(openapi.ErrorCodeInvalidRequest, fmt.Sprintf("read archive: %v", err))
	}
	if len(archive) > skillMaxUploadBytes {
		return nil, "", apierror.FromCode(openapi.ErrorCodeRequestTooLarge,
			fmt.Sprintf("archive exceeds %d bytes", skillMaxUploadBytes))
	}
	return archive, filename, nil
}

type skillUpdateUpload struct {
	archive    []byte
	filename   string
	skillMd    string
	hasArchive bool
	hasSkillMd bool
}

func readSkillUpdateUpload(body *multipart.Reader) (skillUpdateUpload, error) {
	if body == nil {
		return skillUpdateUpload{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "multipart body is required")
	}
	var upload skillUpdateUpload
	for {
		part, err := body.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return skillUpdateUpload{}, apierror.FromCode(
				openapi.ErrorCodeInvalidRequest,
				fmt.Sprintf("parse multipart form: %v", err),
			)
		}
		switch part.FormName() {
		case "archive":
			if upload.hasArchive {
				_ = part.Close()
				return skillUpdateUpload{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "archive must be provided once")
			}
			upload.archive, upload.filename, err = readSkillArchivePart(part)
			if err != nil {
				return skillUpdateUpload{}, err
			}
			upload.hasArchive = true
		case "skill_md":
			if upload.hasSkillMd {
				_ = part.Close()
				return skillUpdateUpload{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "skill_md must be provided once")
			}
			raw, readErr := io.ReadAll(io.LimitReader(part, skills.MaxSkillMdBytes+1))
			_ = part.Close()
			if readErr != nil {
				return skillUpdateUpload{}, apierror.FromCode(
					openapi.ErrorCodeInvalidRequest,
					fmt.Sprintf("read skill_md: %v", readErr),
				)
			}
			if len(raw) > skills.MaxSkillMdBytes {
				return skillUpdateUpload{}, apierror.FromCode(openapi.ErrorCodeRequestTooLarge,
					fmt.Sprintf("skill_md exceeds %d bytes", skills.MaxSkillMdBytes))
			}
			upload.skillMd = string(raw)
			upload.hasSkillMd = true
		default:
			_ = part.Close()
			return skillUpdateUpload{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "unsupported multipart field")
		}
	}
	if upload.hasArchive == upload.hasSkillMd {
		return skillUpdateUpload{}, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"provide exactly one of archive or skill_md",
		)
	}
	if upload.hasArchive && len(upload.archive) == 0 {
		return skillUpdateUpload{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "archive file is required")
	}
	return upload, nil
}

func (s strictOpenAPIServer) UpdateSkill(
	ctx context.Context,
	request openapi.UpdateSkillRequestObject,
) (openapi.UpdateSkillResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	principal, principalErr := accountPrincipalFromContext(ctx)
	if principalErr != nil {
		return nil, *principalErr
	}
	skillID, ok := parseOpenAPIPublicID(publicid.KindSkill, request.SkillID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	upload, err := readSkillUpdateUpload(request.Body)
	if err != nil {
		return nil, err
	}
	var archive []byte
	var format skills.ArchiveFormat
	if upload.hasArchive {
		archive = upload.archive
		var formatOK bool
		format, formatOK = skills.DetectFormat(upload.filename, archive)
		if !formatOK {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "archive must be .zip or .tar.gz")
		}
	} else {
		record, recordErr := s.server.skills.GetVisibleSkill(ctx, org.ID, request.SkillID, principal)
		if recordErr != nil {
			return nil, skillAPIError(ctx, recordErr)
		}
		revisionID, revisionErr := publicID(publicid.KindSkillRevision, record.RevisionID)
		if revisionErr != nil {
			return nil, revisionErr
		}
		current, _, loadErr := s.server.skills.LoadSkillArchive(ctx, request.SkillID, revisionID)
		if loadErr != nil {
			return nil, skillAPIError(ctx, loadErr)
		}
		currentFormat, formatOK := skills.DetectFormat("", current)
		if !formatOK {
			return nil, skillAPIError(
				ctx,
				fmt.Errorf("skill %s revision %s archive format is unrecognized", request.SkillID, revisionID),
			)
		}
		archive, err = skills.ReplaceSkillMd(currentFormat, current, upload.skillMd)
		if err != nil {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
		}
		format = skills.FormatZip
	}
	meta, err := skills.ExtractMetadata(format, archive)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	record, err := s.server.skills.CreateSkillRevisionForSkill(ctx, skillstore.CreateSkillRevisionForSkillInput{
		OrgID: org.ID, SkillID: skillID,
		Name: meta.Name, Description: meta.Description, SkillMd: meta.SkillMd,
		ArchiveBytes: archive, Actor: principal,
	})
	if err != nil {
		return nil, skillAPIError(ctx, err)
	}
	response, err := skillResponseFromRecord(record, true)
	if err != nil {
		return nil, err
	}
	entries, err := skills.ListFiles(format, archive)
	if err != nil {
		return nil, skillAPIError(ctx, fmt.Errorf("list skill %s files: %w", request.SkillID, err))
	}
	files := make([]openapi.SkillFile, 0, len(entries))
	for _, entry := range entries {
		files = append(files, openapi.SkillFile{Path: entry.Path, Size: entry.Size})
	}
	response.Files = &files
	return openapi.UpdateSkill200JSONResponse(response), nil
}

func parseSkillOwner(raw []byte, principal identitystore.PrincipalRecord) (skillstore.SkillOwner, error) {
	var input openapi.SkillOwnerInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return skillstore.SkillOwner{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid owner")
	}
	kind, err := input.Discriminator()
	if err != nil {
		return skillstore.SkillOwner{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid owner")
	}
	switch kind {
	case skillstore.SkillOwnerOrg:
		if _, err := input.AsOrgSkillOwnerInput(); err != nil {
			return skillstore.SkillOwner{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid owner")
		}
		return skillstore.SkillOwner{Kind: skillstore.SkillOwnerOrg}, nil
	case skillstore.SkillOwnerProject:
		owner, err := input.AsProjectSkillOwnerInput()
		if err != nil {
			return skillstore.SkillOwner{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid owner")
		}
		projectID, ok := parseOpenAPIPublicID(publicid.KindProject, owner.ProjectId)
		if !ok {
			return skillstore.SkillOwner{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid owner project_id")
		}
		return skillstore.SkillOwner{Kind: skillstore.SkillOwnerProject, ProjectID: projectID}, nil
	case skillstore.SkillOwnerUser:
		if _, err := input.AsUserSkillOwnerInput(); err != nil {
			return skillstore.SkillOwner{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid owner")
		}
		if principal.Type != identitystore.PrincipalTypeUser {
			return skillstore.SkillOwner{}, apierror.FromCode(
				openapi.ErrorCodeInvalidRequest,
				"user-owned skills require a user principal",
			)
		}
		return skillstore.SkillOwner{Kind: skillstore.SkillOwnerUser, UserID: principal.ID}, nil
	default:
		return skillstore.SkillOwner{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid owner kind")
	}
}

func (s strictOpenAPIServer) ListSkills(
	ctx context.Context,
	request openapi.ListSkillsRequestObject,
) (openapi.ListSkillsResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	principal, principalErr := accountPrincipalFromContext(ctx)
	if principalErr != nil {
		return nil, *principalErr
	}
	limit, err := parseOpenAPIPageLimit(request.Params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	filters := skillstore.SkillListFilters{}
	if request.Params.OwnerKind != nil {
		filters.OwnerKind = string(*request.Params.OwnerKind)
	}
	if request.Params.OwnerProjectId != nil {
		projectID, ok := parseOpenAPIPublicID(publicid.KindProject, *request.Params.OwnerProjectId)
		if !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid owner_project_id")
		}
		filters.OwnerProjectID = projectID
	}
	extra := struct {
		OwnerKind      string `json:"owner_kind,omitempty"`
		OwnerProjectID string `json:"owner_project_id,omitempty"`
	}{OwnerKind: filters.OwnerKind}
	if filters.OwnerProjectID != storage.NilID {
		extra.OwnerProjectID = filters.OwnerProjectID.String()
	}
	list, err := parseResourceListQuery(resourceListQueryInput{
		Name: request.Params.Name, Sort: optionalString(request.Params.Sort),
		Cursor: request.Params.Cursor, ListKind: "skills",
		Scope: org.ID.String(), IDKind: publicid.KindSkill,
		AllowedSorts: defaultResourceSorts, Extra: extra,
	})
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.skills.ListSkills(ctx, skillstore.ListSkillsInput{
		OrgID: org.ID, Actor: principal, Filters: filters, Limit: limit, List: list,
	})
	if err != nil {
		return nil, skillAPIError(ctx, err)
	}
	data, err := skillResponses(page.Skills, false)
	if err != nil {
		return nil, err
	}
	next, err := encodeResourceListNextCursor(
		page.HasMore, page.Next, list, "skills", org.ID.String(), publicid.KindSkill, extra,
	)
	if err != nil {
		return nil, err
	}
	return openapi.ListSkills200JSONResponse{Data: data, NextCursor: nullableFromPtr(next)}, nil
}

func skillResponses(records []skillstore.SkillRecord, includeBody bool) ([]openapi.Skill, error) {
	out := make([]openapi.Skill, 0, len(records))
	for _, record := range records {
		response, err := skillResponseFromRecord(record, includeBody)
		if err != nil {
			return nil, err
		}
		out = append(out, response)
	}
	return out, nil
}

func projectSkillAccessResponse(
	record skillstore.ProjectSkillAccessRecord,
) (openapi.ProjectSkillAccess, error) {
	skill, err := skillResponseFromRecord(record.Skill, false)
	if err != nil {
		return openapi.ProjectSkillAccess{}, err
	}
	projectID, err := publicID(publicid.KindProject, record.ProjectID)
	if err != nil {
		return openapi.ProjectSkillAccess{}, err
	}
	var availability openapi.SkillAvailability
	if record.Availability == skillstore.SkillAvailabilityGrant {
		grantID, err := publicID(publicid.KindSkillGrant, record.GrantID)
		if err != nil {
			return openapi.ProjectSkillAccess{}, err
		}
		err = availability.FromGrantedSkillAvailability(openapi.GrantedSkillAvailability{
			Source:  openapi.GrantedSkillAvailabilitySourceGrant,
			GrantId: grantID,
		})
		if err != nil {
			return openapi.ProjectSkillAccess{}, err
		}
	} else {
		if err := availability.FromDirectSkillAvailability(openapi.DirectSkillAvailability{
			Source: openapi.DirectSkillAvailabilitySourceDirect,
		}); err != nil {
			return openapi.ProjectSkillAccess{}, err
		}
	}
	return openapi.ProjectSkillAccess{
		Skill: skill, ProjectId: projectID, Availability: availability,
	}, nil
}

func (s strictOpenAPIServer) ListProjectAvailableSkills(
	ctx context.Context,
	request openapi.ListProjectAvailableSkillsRequestObject,
) (openapi.ListProjectAvailableSkillsResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	limit, err := parseOpenAPIPageLimit(request.Params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	filters := skillstore.ProjectAvailableSkillFilters{}
	if request.Params.OwnerKind != nil {
		filters.OwnerKinds = []string{string(*request.Params.OwnerKind)}
	}
	if request.Params.AvailabilitySource != nil {
		filters.AvailabilitySources = []string{string(*request.Params.AvailabilitySource)}
	}
	extra := struct{ OwnerKinds, AvailabilitySources []string }{filters.OwnerKinds, filters.AvailabilitySources}
	scopeKey := scope.project.OrgID.String() + "/" + scope.project.ID.String()
	list, err := parseResourceListQuery(resourceListQueryInput{
		Name: request.Params.Name, Sort: optionalString(request.Params.Sort),
		Cursor: request.Params.Cursor, ListKind: "project_available_skills",
		Scope: scopeKey, IDKind: publicid.KindSkill,
		AllowedSorts: defaultResourceSorts, Extra: extra,
	})
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.skills.ListProjectAvailableSkills(ctx, skillstore.ListProjectAvailableSkillsInput{
		OrgID: scope.project.OrgID, ProjectID: scope.project.ID, Filters: filters, Limit: limit,
		List: list,
	})
	if err != nil {
		return nil, skillAPIError(ctx, err)
	}
	data := make([]openapi.ProjectSkillAccess, 0, len(page.Accesses))
	for _, access := range page.Accesses {
		response, err := projectSkillAccessResponse(access)
		if err != nil {
			return nil, err
		}
		data = append(data, response)
	}
	next, err := encodeResourceListNextCursor(
		page.HasMore, page.Next, list,
		"project_available_skills", scopeKey, publicid.KindSkill, extra,
	)
	if err != nil {
		return nil, err
	}
	return openapi.ListProjectAvailableSkills200JSONResponse{
		Data: data, NextCursor: nullableFromPtr(next),
	}, nil
}

func (s strictOpenAPIServer) GetSkill(
	ctx context.Context,
	request openapi.GetSkillRequestObject,
) (openapi.GetSkillResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	principal, principalErr := accountPrincipalFromContext(ctx)
	if principalErr != nil {
		return nil, *principalErr
	}
	record, err := s.server.skills.GetVisibleSkill(ctx, org.ID, request.SkillID, principal)
	if err != nil {
		return nil, skillAPIError(ctx, err)
	}
	response, err := skillResponseFromRecord(record, true)
	if err != nil {
		return nil, err
	}
	files, err := s.loadSkillFiles(ctx, record)
	if err != nil {
		return nil, err
	}
	response.Files = &files
	return openapi.GetSkill200JSONResponse(response), nil
}

func (s strictOpenAPIServer) loadSkillFiles(
	ctx context.Context,
	record skillstore.SkillRecord,
) ([]openapi.SkillFile, error) {
	skillID, err := publicID(publicid.KindSkill, record.ID)
	if err != nil {
		return nil, err
	}
	revisionID, err := publicID(publicid.KindSkillRevision, record.RevisionID)
	if err != nil {
		return nil, err
	}
	archive, _, err := s.server.skills.LoadSkillArchive(ctx, skillID, revisionID)
	if err != nil {
		return nil, skillAPIError(ctx, err)
	}
	format, ok := skills.DetectFormat("", archive)
	if !ok {
		return nil, skillAPIError(ctx, fmt.Errorf("skill %s revision %s archive format is unrecognized", skillID, revisionID))
	}
	entries, err := skills.ListFiles(format, archive)
	if err != nil {
		return nil, skillAPIError(ctx, fmt.Errorf("list skill %s files: %w", skillID, err))
	}
	files := make([]openapi.SkillFile, 0, len(entries))
	for _, entry := range entries {
		files = append(files, openapi.SkillFile{Path: entry.Path, Size: entry.Size})
	}
	return files, nil
}

func (s strictOpenAPIServer) DeleteSkill(
	ctx context.Context,
	request openapi.DeleteSkillRequestObject,
) (openapi.DeleteSkillResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	principal, principalErr := accountPrincipalFromContext(ctx)
	if principalErr != nil {
		return nil, *principalErr
	}
	skillID, ok := parseOpenAPIPublicID(publicid.KindSkill, request.SkillID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if err := s.server.skills.DeleteSkill(ctx, skillstore.DeleteSkillInput{
		OrgID: org.ID, SkillID: skillID, Actor: principal,
	}); err != nil {
		return nil, skillAPIError(ctx, err)
	}
	return openapi.DeleteSkill204Response{}, nil
}

func (s strictOpenAPIServer) CreateSkillGrant(
	ctx context.Context,
	request openapi.CreateSkillGrantRequestObject,
) (openapi.CreateSkillGrantResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	principal, principalErr := accountPrincipalFromContext(ctx)
	if principalErr != nil {
		return nil, *principalErr
	}
	skillID, ok := parseOpenAPIPublicID(publicid.KindSkill, request.SkillID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	projectID, ok := parseOpenAPIPublicID(publicid.KindProject, request.Body.TargetProjectId)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid target_project_id")
	}
	grant, err := s.server.skills.CreateSkillGrant(ctx, skillstore.CreateSkillGrantInput{
		OrgID: org.ID, SkillID: skillID, TargetProjectID: projectID,
		Actor: principal,
	})
	if err != nil {
		return nil, skillAPIError(ctx, err)
	}
	response, err := skillGrantResponseFromRecord(grant)
	if err != nil {
		return nil, err
	}
	return openapi.CreateSkillGrant201JSONResponse(response), nil
}

func (s strictOpenAPIServer) ListSkillGrants(
	ctx context.Context,
	request openapi.ListSkillGrantsRequestObject,
) (openapi.ListSkillGrantsResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	principal, principalErr := accountPrincipalFromContext(ctx)
	if principalErr != nil {
		return nil, *principalErr
	}
	skillID, ok := parseOpenAPIPublicID(publicid.KindSkill, request.SkillID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	limit, err := parseOpenAPIPageLimit(request.Params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	scopeKey := org.ID.String() + "/" + skillID.String()
	list, err := parseResourceListQuery(resourceListQueryInput{
		Name: request.Params.Name,
		Sort: optionalString(request.Params.Sort), Cursor: request.Params.Cursor, ListKind: "skill_grants", Scope: scopeKey,
		IDKind: publicid.KindSkillGrant, AllowedSorts: sortSet("name", "created_at"),
	})
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.skills.ListSkillGrants(ctx, skillstore.ListSkillGrantsInput{
		OrgID: org.ID, SkillID: skillID, Actor: principal, Limit: limit, List: list,
	})
	if err != nil {
		return nil, skillAPIError(ctx, err)
	}
	data := make([]openapi.SkillGrantListItem, 0, len(page.Grants))
	for _, record := range page.Grants {
		response, err := skillGrantResponseFromRecord(record.Grant)
		if err != nil {
			return nil, err
		}
		project, err := projectResponse(identitystore.ProjectRecord{
			ID: record.TargetProject.ID, OrgID: record.TargetProject.OrgID,
			Name: record.TargetProject.Name, CreatedAt: record.TargetProject.CreatedAt,
			UpdatedAt: record.TargetProject.UpdatedAt,
		})
		if err != nil {
			return nil, err
		}
		data = append(data, openapi.SkillGrantListItem{Grant: response, TargetProject: project})
	}
	next, err := encodeResourceListNextCursor(
		page.HasMore, page.Next, list,
		"skill_grants", scopeKey, publicid.KindSkillGrant, nil,
	)
	if err != nil {
		return nil, err
	}
	return openapi.ListSkillGrants200JSONResponse{Data: data, NextCursor: nullableFromPtr(next)}, nil
}

func (s strictOpenAPIServer) DeleteSkillGrant(
	ctx context.Context,
	request openapi.DeleteSkillGrantRequestObject,
) (openapi.DeleteSkillGrantResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	principal, principalErr := accountPrincipalFromContext(ctx)
	if principalErr != nil {
		return nil, *principalErr
	}
	skillID, ok := parseOpenAPIPublicID(publicid.KindSkill, request.SkillID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	grantID, ok := parseOpenAPIPublicID(publicid.KindSkillGrant, request.GrantID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if _, err := s.server.skills.DeleteSkillGrant(ctx, skillstore.DeleteSkillGrantInput{
		OrgID: org.ID, SkillID: skillID, GrantID: grantID, Actor: principal,
	}); err != nil {
		return nil, skillAPIError(ctx, err)
	}
	return openapi.DeleteSkillGrant204Response{}, nil
}

func (s strictOpenAPIServer) GetDaemonSkillArchive(
	ctx context.Context,
	request openapi.GetDaemonSkillArchiveRequestObject,
) (openapi.GetDaemonSkillArchiveResponseObject, error) {
	if len(s.server.skillDownloadSigningKey) == 0 {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "skill download signing is not configured")
	}
	skillID := request.SkillID
	if skillID == "" {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	revisionID := request.Params.RevisionId
	if _, err := publicid.Decode(publicid.KindSkill, skillID); err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if _, err := publicid.Decode(publicid.KindSkillRevision, revisionID); err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid revision_id")
	}
	principal, ok := principalFromContext(ctx)
	if !ok || principal.Type != identitystore.PrincipalTypeMachineDaemon || principal.OrgID == storage.NilID {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
	}
	err := verifySkillDownloadCapability(
		s.server.skillDownloadSigningKey,
		principal,
		skillID,
		revisionID,
		request.Params.DownloadToken,
		request.Params.ExpiresAt,
		time.Now(),
	)
	if errors.Is(err, skills.ErrDownloadTokenExpired) {
		return nil, apierror.FromCode(openapi.ErrorCodeGone, "skill download expired")
	}
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
	}
	if _, err := s.server.skills.GetSkillByPublicID(ctx, principal.OrgID, skillID); err != nil {
		return nil, skillAPIError(ctx, err)
	}
	content, meta, err := s.server.skills.LoadSkillArchive(ctx, skillID, revisionID)
	if err != nil {
		return nil, skillAPIError(ctx, err)
	}
	return daemonSkillArchiveResponse{content: content, digest: meta.Digest}, nil
}

func verifySkillDownloadCapability(
	signingKey []byte,
	principal identitystore.PrincipalRecord,
	skillID, revisionID, token string,
	expiresAt int64,
	now time.Time,
) error {
	if principal.Type != identitystore.PrincipalTypeMachineDaemon || principal.ID == storage.NilID {
		return skills.ErrInvalidDownloadToken
	}
	machineID, err := publicID(publicid.KindMachine, principal.ID)
	if err != nil {
		return skills.ErrInvalidDownloadToken
	}
	return skills.VerifyDownloadToken(
		signingKey,
		token,
		skillID,
		revisionID,
		machineID,
		expiresAt,
		now,
	)
}

type daemonSkillArchiveResponse struct {
	content []byte
	digest  string
}

func (r daemonSkillArchiveResponse) VisitGetDaemonSkillArchiveResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(r.content)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.digest != "" {
		w.Header().Set("ETag", `"`+r.digest+`"`)
	}
	w.WriteHeader(http.StatusOK)
	_, err := io.Copy(w, bytes.NewReader(r.content))
	return err
}
