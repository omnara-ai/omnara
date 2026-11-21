//go:build integration && servicee2e && live

package e2e

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
)

// Speech synthesized with macOS `say`: "The secret word is pineapple."
//
//go:embed testdata/spoken-codeword.mp3
var liveAttachmentAudio []byte

const (
	liveAttachmentImageColor = "blue"
	liveAttachmentPDFCode    = "TANGERINE-5817"
	liveAttachmentAudioWord  = "pineapple"
)

func TestServiceE2ELiveOpenAIResponsesAttachments(t *testing.T) {
	runLiveServiceAttachments(t, "openai-prod", "", false)
}

func TestServiceE2ELiveOpenAIChatCompletionsAttachments(t *testing.T) {
	runLiveServiceAttachments(t, "openai-chat-prod", "", false)
}

func TestServiceE2ELiveOpenRouterAttachments(t *testing.T) {
	runLiveServiceAttachments(t, "openrouter-prod", modeltest.LiveOpenRouterAudioProviderModelSlug, true)
}

func TestServiceE2ELiveAnthropicAttachments(t *testing.T) {
	runLiveServiceAttachments(t, "anthropic-prod", "", false)
}

// Sends one input carrying an image, a PDF, and an audio file. Every provider
// must read the image and the PDF. Only a model that accepts audio must hear the
// audio; the others receive it as an artifact reference and must still answer.
func runLiveServiceAttachments(t *testing.T, providerConfig, modelSlug string, hearsAudio bool) {
	t.Helper()
	provider := requireLiveServiceProvider(t, providerConfig)
	provider.requireAPIKey(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	opts := provider.journeyOptions("attachments")
	opts.ModelOptions.InputModalities = []string{"text", "image", "file"}
	if modelSlug != "" {
		opts.ConfiguredModelName = modelSlug
		opts.ModelOptions = serviceE2EConfiguredModelOptions{}
	}
	if hearsAudio {
		opts.ModelOptions.InputModalities = []string{"text", "image", "file", "audio"}
	}

	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, opts.Seed)
	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithToolsAndModelOptions(
		t,
		ctx,
		opts.Seed,
		opts.ProviderConfig,
		opts.ConfiguredModelName,
		serviceE2EConfiguredModelOptionsByIdentity{
			{
				ProviderConfigName:  opts.ProviderConfig,
				ConfiguredModelName: opts.ConfiguredModelName,
			}: opts.ModelOptions,
		},
	)
	agentID := project.createAgent(t, ctx)
	prompt := strings.Join([]string{
		"Three files are attached: an image, a PDF, and an audio recording. Do not call any tool.",
		"Reply on one line in exactly this form: IMAGE=<the image's single solid color> " +
			"PDF=<the code printed in the PDF> AUDIO=<the secret word spoken in the recording>.",
		"If you cannot perceive one of the files, write NONE for it.",
	}, "\n")
	created := env.requestJSON(
		t,
		ctx,
		http.MethodPost,
		project.projectPath+"/agents/"+agentID+"/inputs",
		map[string]any{"content_blocks": []map[string]any{
			{"type": "text", "text": prompt},
			liveAttachmentBlock("image/png", "swatch.png", liveAttachmentImage(t)),
			liveAttachmentBlock("application/pdf", "code.pdf", liveAttachmentPDF()),
			liveAttachmentBlock("audio/mpeg", "recording.mp3", liveAttachmentAudio),
		}},
		"live-attachments-"+agentID,
		project.adminToken,
		http.StatusCreated,
	)
	storedBlocks := testutil.RequireType[[]any](
		t, testutil.RequireType[map[string]any](t, created["agent_input"])["content_blocks"],
	)
	if len(storedBlocks) != 4 {
		t.Fatalf("stored content blocks = %+v, want text and three attachments", storedBlocks)
	}
	for _, raw := range storedBlocks[1:] {
		block := testutil.RequireType[map[string]any](t, raw)
		if id, _ := block["artifact_id"].(string); !strings.HasPrefix(id, "art_") {
			t.Fatalf("attachment was not stored as an artifact: %+v", block)
		}
	}

	worker := env.startWorker(t, ctx, project.projectID, serviceWorkerOptions{
		ProviderConfig: opts.ProviderConfig,
		BaseURL:        opts.BaseURL,
	})
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	var output string
	waitForServiceE2EConditionUntil(t, ctx, time.Now().Add(3*time.Minute), func() (bool, string) {
		latest, err := latestModelOutputTextAfterSequence(ctx, env, projectUUID, agentUUID, 0)
		if err != nil {
			return false, err.Error()
		}
		if strings.Contains(latest, "AUDIO=") {
			output = latest
			return true, ""
		}
		failure, err := latestTerminalLiveModelFailure(ctx, env, projectUUID, agentUUID, 0)
		if err != nil {
			return false, err.Error()
		}
		if failure != "" {
			t.Fatalf("terminal live model failure: %s worker_logs=%s", failure, worker.logExcerpt())
		}
		return false, "latest live model output=" + strconv.Quote(latest) +
			" worker_logs=" + worker.logExcerpt()
	})
	waitForLiveAgentIdle(t, ctx, env, project.projectID, agentID, worker)

	answer := strings.ToLower(output)
	for name, want := range map[string]string{
		"image": "image=" + liveAttachmentImageColor,
		"pdf":   "pdf=" + strings.ToLower(liveAttachmentPDFCode),
	} {
		if !strings.Contains(answer, want) {
			t.Fatalf("%s attachment was not read: output=%q want %q", name, output, want)
		}
	}
	heardAudio := strings.Contains(answer, "audio="+liveAttachmentAudioWord)
	if heardAudio != hearsAudio {
		t.Fatalf("audio heard = %t, want %t: output=%q", heardAudio, hearsAudio, output)
	}
}

func liveAttachmentBlock(mediaType, filename string, content []byte) map[string]any {
	return map[string]any{
		"type":       "media",
		"media_type": mediaType,
		"filename":   filename,
		"data":       base64.StdEncoding.EncodeToString(content),
	}
}

func liveAttachmentImage(t *testing.T) []byte {
	t.Helper()
	const side = 64
	swatch := image.NewRGBA(image.Rect(0, 0, side, side))
	for y := range side {
		for x := range side {
			swatch.Set(x, y, color.RGBA{B: 255, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, swatch); err != nil {
		t.Fatalf("encode attachment image: %v", err)
	}
	return encoded.Bytes()
}

// A one-page PDF with a single line of Helvetica text.
func liveAttachmentPDF() []byte {
	stream := "BT /F1 24 Tf 72 720 Td (The code is " + liveAttachmentPDFCode + ") Tj ET"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R " +
			"/Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	offsets := make([]int, 0, len(objects))
	for index, object := range objects {
		offsets = append(offsets, pdf.Len())
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(
		&pdf,
		"trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(objects)+1,
		xref,
	)
	return pdf.Bytes()
}
