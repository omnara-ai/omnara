package modelcontext

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func mediaTestArtifact(id storage.ID, contentType string, size int64) artifactstore.ArtifactRecord {
	return artifactstore.ArtifactRecord{
		ID:          id,
		ProjectID:   testProjectID,
		AgentID:     testAgentID,
		ContentType: contentType,
		SizeBytes:   &size,
	}
}

func mediaRefContent(t *testing.T, ids ...storage.ID) json.RawMessage {
	t.Helper()
	parts := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, map[string]any{"type": "media_ref", "artifact_id": id.String()})
	}
	body, err := json.Marshal(parts)
	if err != nil {
		t.Fatalf("marshal media parts: %v", err)
	}
	return body
}

func TestBuildResolvesMediaMetadata(t *testing.T) {
	imageID := testIDN(101)
	documentID := testIDN(102)
	toolOutputID := testIDN(103)
	missingID := testIDN(104)
	svgID := testIDN(107)
	store := &fakeContextStore{
		messages: []executionstore.ContextEventRecord{
			{
				ID:           testIDN(111),
				Role:         modelprotocol.RoleUser,
				Sequence:     1,
				ContentParts: mediaRefContent(t, imageID, toolOutputID, missingID, svgID),
			},
		},
		toolCalls: []executionstore.ToolCallRecord{
			{
				ID:                      testIDN(121),
				TurnID:                  testTurnID,
				Name:                    "custom_tool",
				ProviderCallID:          "call_121",
				Input:                   json.RawMessage(`{}`),
				ModelCallContextID:      testIDN(120),
				ToolCallResultID:        testIDN(122),
				ToolResultEventID:       testIDN(123),
				SourceEventSequence:     2,
				ToolResultEventSequence: 3,
				State:                   executionstore.ToolCallStateCompleted,
				Outcome:                 executionstore.ToolResultOutcomeSucceeded,
				ResultContentParts:      mediaRefContent(t, documentID),
			},
		},
		watermark: 3,
		artifacts: []artifactstore.ArtifactRecord{
			mediaTestArtifact(imageID, "image/png", 2048),
			mediaTestArtifact(documentID, "application/pdf", 4096),
			mediaTestArtifact(toolOutputID, "application/json", 64),
			mediaTestArtifact(svgID, "image/svg+xml", 128),
		},
	}
	bundle, err := Builder{
		Store: store,
	}.Build(
		context.Background(),
		BuildInput{
			ProjectID:       testProjectID,
			AgentID:         testAgentID,
			TurnID:          testTurnID,
			OpeningInputIDs: []storage.ID{testInputID},
			Now:             time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(bundle.ResolvedMedia) != 2 {
		t.Fatalf("resolved media = %+v, want image and document", bundle.ResolvedMedia)
	}
	image := bundle.ResolvedMedia[imageID.String()]
	if image.Kind != "image" || image.MediaType != "image/png" || image.SizeBytes != 2048 {
		t.Fatalf("unexpected image resolution: %+v", image)
	}
	document := bundle.ResolvedMedia[documentID.String()]
	if document.Kind != "document" || document.MediaType != "application/pdf" {
		t.Fatalf("unexpected document resolution: %+v", document)
	}
	if _, ok := bundle.ResolvedMedia[toolOutputID.String()]; ok {
		t.Fatal("tool_output artifacts must not resolve as media")
	}
	if _, ok := bundle.ResolvedMedia[missingID.String()]; ok {
		t.Fatal("missing artifacts must not resolve")
	}
	if _, ok := bundle.ResolvedMedia[svgID.String()]; ok {
		t.Fatal("non-allowlisted media types must not resolve")
	}
}

func TestMediaRefTextContainsOnlyCanonicalReference(t *testing.T) {
	artifactID := testIDN(150)
	publicArtifactID, err := publicid.Encode(publicid.KindArtifact, artifactID)
	if err != nil {
		t.Fatalf("encode artifact id: %v", err)
	}
	text := MediaRefText(map[string]json.RawMessage{
		"type":            json.RawMessage(`"media_ref"`),
		"artifact_id":     json.RawMessage(`"` + artifactID.String() + `"`),
		"provider_replay": json.RawMessage(`{"item":{"opaque":true}}`),
	})
	if text != "A prior attachment with artifact ID "+publicArtifactID+" is not included in the current model context." {
		t.Fatalf("media ref text = %s", text)
	}
}

func TestResolvedMediaOccurrencesUseExactOpeningInputIdentity(t *testing.T) {
	openingInputID := testIDN(130)
	openingArtifactID := testIDN(131)
	historicalArtifactID := testIDN(132)
	bundle := Bundle{
		OpeningInputIDs: []storage.ID{openingInputID},
		Messages: []Message{
			{
				AgentInputID: testIDN(129).String(),
				Content:      mediaRefContent(t, historicalArtifactID),
			},
			{
				AgentInputID: openingInputID.String(),
				Content:      mediaRefContent(t, openingArtifactID),
			},
		},
		ResolvedMedia: map[string]ResolvedMedia{
			openingArtifactID.String(): {
				ArtifactID: openingArtifactID.String(),
				Kind:       AttachmentKindImage,
				MediaType:  "image/png",
			},
			historicalArtifactID.String(): {
				ArtifactID: historicalArtifactID.String(),
				Kind:       AttachmentKindImage,
				MediaType:  "image/png",
			},
		},
	}

	got := ResolvedMediaOccurrences(bundle)
	if len(got) != 2 || got[0].Opening || !got[1].Opening ||
		got[1].Media.ArtifactID != openingArtifactID.String() {
		t.Fatalf("resolved occurrences = %+v, want only the second occurrence opening", got)
	}
}

type imageOnlyMediaProjector struct{}

func (imageOnlyMediaProjector) ProjectRenderedMedia(bundle Bundle) []RenderedMedia {
	var rendered []RenderedMedia
	for _, occurrence := range ResolvedMediaOccurrences(bundle) {
		if occurrence.Media.Kind != AttachmentKindImage {
			continue
		}
		rendered = append(rendered, RenderedMedia{
			Occurrence:     occurrence.Ref,
			Media:          occurrence.Media,
			Representation: MediaRepresentationInline,
		})
	}
	return rendered
}

func TestBuildBudgetsOnlyAdapterRenderedMediaOccurrences(t *testing.T) {
	const officeMediaType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

	t.Run("non-rendered opening documents do not fail the byte budget", func(t *testing.T) {
		firstInputID := testIDN(133)
		secondInputID := testIDN(134)
		firstDocumentID := testIDN(135)
		secondDocumentID := testIDN(136)
		documentSize := MaxResolvedMediaBytes/2 + 1
		firstDocument := mediaTestArtifact(firstDocumentID, officeMediaType, documentSize)
		firstDocument.Filename = "first.docx"
		store := &fakeContextStore{
			messages: []executionstore.ContextEventRecord{
				{
					ID:           testIDN(137),
					AgentInputID: firstInputID,
					Role:         modelprotocol.RoleUser,
					Sequence:     1,
					ContentParts: mediaRefContent(t, firstDocumentID),
				},
				{
					ID:           testIDN(138),
					AgentInputID: secondInputID,
					Role:         modelprotocol.RoleUser,
					Sequence:     2,
					ContentParts: mediaRefContent(t, secondDocumentID),
				},
			},
			watermark: 2,
			artifacts: []artifactstore.ArtifactRecord{
				firstDocument,
				mediaTestArtifact(secondDocumentID, officeMediaType, documentSize),
			},
		}
		bundle, err := (Builder{Store: store}).Build(context.Background(), BuildInput{
			ProjectID:       testProjectID,
			AgentID:         testAgentID,
			TurnID:          testTurnID,
			OpeningInputIDs: []storage.ID{firstInputID, secondInputID},
			MediaProjector:  imageOnlyMediaProjector{},
			Now:             time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("build non-rendered opening documents: %v", err)
		}
		if len(bundle.ResolvedMedia) != 0 || len(store.artifactBlobReads) != 0 {
			t.Fatalf(
				"resolved media = %+v blob reads = %v, want textual projection without blob loading",
				bundle.ResolvedMedia,
				store.artifactBlobReads,
			)
		}
		if !bytes.Contains(bundle.Messages[0].Content, []byte("first.docx")) {
			t.Fatalf("textual projection does not include filename: %s", bundle.Messages[0].Content)
		}
	})

	t.Run("non-rendered history does not evict rendered media", func(t *testing.T) {
		imageID := testIDN(139)
		documentID := testIDN(146)
		store := &fakeContextStore{
			messages: []executionstore.ContextEventRecord{
				{ID: testIDN(147), Role: modelprotocol.RoleUser, Sequence: 1, ContentParts: mediaRefContent(t, imageID)},
				{ID: testIDN(148), Role: modelprotocol.RoleUser, Sequence: 2, ContentParts: mediaRefContent(t, documentID)},
			},
			watermark: 2,
			artifacts: []artifactstore.ArtifactRecord{
				mediaTestArtifact(imageID, "image/png", MaxResolvedMediaBytes),
				mediaTestArtifact(documentID, officeMediaType, MaxResolvedMediaBytes),
			},
		}
		bundle, err := (Builder{Store: store}).Build(context.Background(), BuildInput{
			ProjectID:       testProjectID,
			AgentID:         testAgentID,
			TurnID:          testTurnID,
			OpeningInputIDs: []storage.ID{testInputID},
			MediaProjector:  imageOnlyMediaProjector{},
			Now:             time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("build mixed rendered media: %v", err)
		}
		if _, ok := bundle.ResolvedMedia[imageID.String()]; !ok {
			t.Fatalf("rendered image was evicted: %+v", bundle.ResolvedMedia)
		}
		if len(store.artifactBlobReads) != 1 || store.artifactBlobReads[0] != imageID {
			t.Fatalf("blob reads = %v, want only image %s", store.artifactBlobReads, imageID)
		}
	})
}

func TestBuildDropsOldestMediaPastByteBudget(t *testing.T) {
	oldID := testIDN(105)
	newID := testIDN(106)
	bigSize := MaxResolvedMediaBytes - 1024
	store := &fakeContextStore{
		messages: []executionstore.ContextEventRecord{
			{ID: testIDN(111), Role: modelprotocol.RoleUser, Sequence: 1, ContentParts: mediaRefContent(t, oldID)},
			{ID: testIDN(112), Role: modelprotocol.RoleUser, Sequence: 2, ContentParts: mediaRefContent(t, newID)},
		},
		watermark: 2,
		artifacts: []artifactstore.ArtifactRecord{
			mediaTestArtifact(oldID, "image/png", bigSize),
			mediaTestArtifact(newID, "image/png", bigSize),
		},
	}
	bundle, err := Builder{
		Store: store,
	}.Build(
		context.Background(),
		BuildInput{
			ProjectID:       testProjectID,
			AgentID:         testAgentID,
			TurnID:          testTurnID,
			OpeningInputIDs: []storage.ID{testInputID},
			Now:             time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if _, ok := bundle.ResolvedMedia[newID.String()]; !ok {
		t.Fatalf("newest media must survive the byte budget: %+v", bundle.ResolvedMedia)
	}
	if _, ok := bundle.ResolvedMedia[oldID.String()]; ok {
		t.Fatal("oldest media must drop past the byte budget")
	}
}

func TestBuildRejectsOpeningMediaBatchPastByteBudget(t *testing.T) {
	firstInputID := testIDN(140)
	secondInputID := testIDN(141)
	firstImageID := testIDN(142)
	secondImageID := testIDN(143)
	imageSize := MaxResolvedMediaBytes/2 + 1
	store := &fakeContextStore{
		messages: []executionstore.ContextEventRecord{
			{
				ID:           testIDN(144),
				AgentInputID: firstInputID,
				Role:         modelprotocol.RoleUser,
				Sequence:     1,
				ContentParts: mediaRefContent(t, firstImageID),
			},
			{
				ID:           testIDN(145),
				AgentInputID: secondInputID,
				Role:         modelprotocol.RoleUser,
				Sequence:     2,
				ContentParts: mediaRefContent(t, secondImageID),
			},
		},
		watermark: 2,
		artifacts: []artifactstore.ArtifactRecord{
			mediaTestArtifact(firstImageID, "image/png", imageSize),
			mediaTestArtifact(secondImageID, "image/png", imageSize),
		},
	}
	_, err := (Builder{Store: store}).Build(
		context.Background(),
		BuildInput{
			ProjectID:       testProjectID,
			AgentID:         testAgentID,
			TurnID:          testTurnID,
			OpeningInputIDs: []storage.ID{firstInputID, secondInputID},
			Now:             time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC),
		},
	)
	if !errors.Is(err, ErrOpeningMediaBudgetExceeded) {
		t.Fatalf("build error = %v, want opening-media budget error", err)
	}
}

func TestBuildCountsEveryReferenceToResolvedMediaAgainstByteBudget(t *testing.T) {
	imageID := testIDN(109)
	imageSize := int64(10 * 1024 * 1024)
	store := &fakeContextStore{
		messages: []executionstore.ContextEventRecord{{
			ID:           testIDN(113),
			Role:         modelprotocol.RoleUser,
			Sequence:     1,
			ContentParts: mediaRefContent(t, imageID, imageID, imageID),
		}},
		watermark: 1,
		artifacts: []artifactstore.ArtifactRecord{
			mediaTestArtifact(imageID, "image/png", imageSize),
		},
	}
	bundle, err := Builder{Store: store}.Build(
		context.Background(),
		BuildInput{
			ProjectID:       testProjectID,
			AgentID:         testAgentID,
			TurnID:          testTurnID,
			OpeningInputIDs: []storage.ID{testInputID},
			Now:             time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if _, ok := bundle.ResolvedMedia[imageID.String()]; !ok {
		t.Fatal("two of three 10 MiB references should fit the 24 MiB rendered-media budget")
	}
	var projected []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(bundle.Messages[0].Content, &projected); err != nil {
		t.Fatalf("decode projected media occurrences: %v", err)
	}
	if len(projected) != 3 || projected[0].Type != "text" || projected[0].Text == "" ||
		projected[1].Type != "media_ref" || projected[2].Type != "media_ref" {
		t.Fatalf("projected media occurrences = %+v, want one textual and two rendered refs", projected)
	}
}

func TestBuildProtectsOpeningOccurrenceWhenHistoryReferencesSameArtifact(t *testing.T) {
	historicalInputID := testIDN(150)
	openingInputID := testIDN(151)
	imageID := testIDN(152)
	imageSize := int64(13 * 1024 * 1024)
	store := &fakeContextStore{
		messages: []executionstore.ContextEventRecord{
			{
				ID:           testIDN(153),
				AgentInputID: historicalInputID,
				Role:         modelprotocol.RoleUser,
				Sequence:     1,
				ContentParts: mediaRefContent(t, imageID),
			},
			{
				ID:           testIDN(154),
				AgentInputID: openingInputID,
				Role:         modelprotocol.RoleUser,
				Sequence:     2,
				ContentParts: mediaRefContent(t, imageID),
			},
		},
		watermark: 2,
		artifacts: []artifactstore.ArtifactRecord{
			mediaTestArtifact(imageID, "image/png", imageSize),
		},
	}
	canonicalHistoricalContent := string(store.messages[0].ContentParts)
	bundle, err := (Builder{Store: store}).Build(
		context.Background(),
		BuildInput{
			ProjectID:       testProjectID,
			AgentID:         testAgentID,
			TurnID:          testTurnID,
			OpeningInputIDs: []storage.ID{openingInputID},
			Now:             time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatalf("build with repeated opening artifact: %v", err)
	}
	resolved := bundle.ResolvedMedia[imageID.String()]
	if resolved.ArtifactID != imageID.String() {
		t.Fatalf("resolved opening media = %+v", resolved)
	}
	resolvedOccurrences := ResolvedMediaOccurrences(bundle)
	if len(resolvedOccurrences) != 1 || resolvedOccurrences[0].Media.ArtifactID != imageID.String() {
		t.Fatalf("resolved occurrences = %+v, want only current image", resolvedOccurrences)
	}
	if string(store.messages[0].ContentParts) != canonicalHistoricalContent {
		t.Fatal("media projection mutated canonical stored event content")
	}
}

func TestBuildDropsOldToolResultMediaBeforeNewerMessageMedia(t *testing.T) {
	oldMessageID := testIDN(110)
	oldToolResultID := testIDN(111)
	newMessageID := testIDN(112)
	mediaSize := MaxResolvedMediaBytes/2 + 1
	store := &fakeContextStore{
		messages: []executionstore.ContextEventRecord{
			{
				ID:           testIDN(114),
				Role:         modelprotocol.RoleUser,
				Sequence:     1,
				ContentParts: mediaRefContent(t, oldMessageID),
			},
			{
				ID:           testIDN(115),
				Role:         modelprotocol.RoleUser,
				Sequence:     4,
				ContentParts: mediaRefContent(t, newMessageID),
			},
		},
		toolCalls: []executionstore.ToolCallRecord{{
			ID:                      testIDN(116),
			TurnID:                  testTurnID,
			ModelCallContextID:      testIDN(119),
			ProviderCallID:          "call_116",
			Name:                    "custom_tool",
			Input:                   json.RawMessage(`{}`),
			ToolCallResultID:        testIDN(117),
			ToolResultEventID:       testIDN(118),
			SourceEventSequence:     2,
			ToolResultEventSequence: 3,
			State:                   executionstore.ToolCallStateCompleted,
			Outcome:                 executionstore.ToolResultOutcomeSucceeded,
			ResultContentParts:      mediaRefContent(t, oldToolResultID),
		}},
		watermark: 4,
		artifacts: []artifactstore.ArtifactRecord{
			mediaTestArtifact(oldMessageID, "image/png", mediaSize),
			mediaTestArtifact(oldToolResultID, "image/png", mediaSize),
			mediaTestArtifact(newMessageID, "image/png", mediaSize),
		},
	}
	bundle, err := Builder{Store: store}.Build(
		context.Background(),
		BuildInput{
			ProjectID:       testProjectID,
			AgentID:         testAgentID,
			TurnID:          testTurnID,
			OpeningInputIDs: []storage.ID{testInputID},
			Now:             time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if _, ok := bundle.ResolvedMedia[newMessageID.String()]; !ok {
		t.Fatalf("newer message media must survive: %+v", bundle.ResolvedMedia)
	}
	if _, ok := bundle.ResolvedMedia[oldToolResultID.String()]; ok {
		t.Fatal("older tool-result media must be evicted before newer message media")
	}
	if _, ok := bundle.ResolvedMedia[oldMessageID.String()]; ok {
		t.Fatal("oldest message media must be evicted")
	}
}
