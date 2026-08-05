package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
)

const (
	defaultAPIURL             = "http://localhost:8080"
	personalAccessTokenPrefix = "omnara_pat_"
	maxJSONResponseBytes      = 16 * 1024 * 1024
	maxErrorResponseBytes     = 64 * 1024
)

const (
	ansiReset   = "\033[0m"
	ansiDim     = "\033[2m"
	ansiGray    = "\033[90m"
	ansiCyan    = "\033[36m"
	ansiGreen   = "\033[32m"
	ansiYellow  = "\033[33m"
	ansiBlue    = "\033[34m"
	ansiMagenta = "\033[35m"
	ansiRed     = "\033[31m"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr, http.DefaultClient); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type cliConfig struct {
	APIURL    string
	Token     string
	OrgID     string
	ProjectID string
}

type apiClient struct {
	baseURL    string
	token      string
	orgID      string
	projectID  string
	httpClient *http.Client
}

type command struct {
	name string
	args []string
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, httpClient *http.Client) error {
	cmd, globalArgs := parseCommand(args)
	if cmd.name == "help" || cmd.name == "-h" || cmd.name == "--help" {
		printUsage(stdout)
		return nil
	}
	cfg, err := parseGlobalFlags(globalArgs, stderr)
	if err != nil {
		return err
	}
	client := apiClient{
		baseURL:    strings.TrimRight(cfg.APIURL, "/"),
		token:      cfg.Token,
		orgID:      cfg.OrgID,
		projectID:  cfg.ProjectID,
		httpClient: outboundhttp.CloneWithoutRedirects(httpClient),
	}
	switch cmd.name {
	case "chat":
		return runChat(ctx, client, cmd.args, stdin, stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q", cmd.name)
	}
}

func parseCommand(args []string) (command, []string) {
	if len(args) == 0 {
		return command{name: "help"}, nil
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if isGlobalFlagName(arg) {
			if !strings.Contains(arg, "=") && i+1 < len(args) {
				i++
			}
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		switch arg {
		case "chat", "help":
			return command{name: arg, args: args[i+1:]}, append(args[:i], nil...)
		default:
			return command{name: "help"}, args
		}
	}
	return command{name: "help"}, args
}

func isGlobalFlagName(arg string) bool {
	name := strings.TrimLeft(arg, "-")
	name, _, _ = strings.Cut(name, "=")
	switch name {
	case "api-url", "token", "org-id", "project-id":
		return true
	default:
		return false
	}
}

func parseGlobalFlags(args []string, stderr io.Writer) (cliConfig, error) {
	cfg := cliConfig{
		APIURL:    getenv("OMNARA_API_URL", defaultAPIURL),
		Token:     os.Getenv("OMNARA_TOKEN"),
		OrgID:     os.Getenv("OMNARA_ORG_ID"),
		ProjectID: os.Getenv("OMNARA_PROJECT_ID"),
	}
	fs := flag.NewFlagSet("omnara", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.APIURL, "api-url", cfg.APIURL, "Omnara API base URL")
	fs.StringVar(&cfg.Token, "token", cfg.Token, "personal access token")
	fs.StringVar(&cfg.OrgID, "org-id", cfg.OrgID, "existing organization ID")
	fs.StringVar(&cfg.ProjectID, "project-id", cfg.ProjectID, "existing project ID")
	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}
	if cfg.Token == "" {
		return cliConfig{}, errors.New("token is required; set --token or OMNARA_TOKEN")
	}
	if !strings.HasPrefix(cfg.Token, personalAccessTokenPrefix) {
		return cliConfig{}, fmt.Errorf(
			"personal access token must start with %q; create an Omnara PAT after logging in",
			personalAccessTokenPrefix,
		)
	}
	if _, err := url.ParseRequestURI(cfg.APIURL); err != nil {
		return cliConfig{}, fmt.Errorf("invalid api url: %w", err)
	}
	if (cfg.OrgID == "") != (cfg.ProjectID == "") {
		return cliConfig{}, errors.New("org id and project id must be specified together")
	}
	return cfg, nil
}

func runChat(ctx context.Context, client apiClient, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: testagent chat <agent.yaml>")
	}
	agentID, err := client.createChatAgent(ctx, fs.Arg(0), stderr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	events := make(chan agentEvent, 32)
	errs := make(chan error, 1)
	interactions := make(chan pendingInteraction, 8)
	interactionState := newInteractionState()
	go func() {
		errs <- client.streamEvents(ctx, agentID, events)
	}()
	go client.pollOpenInteractions(ctx, agentID, interactionState, interactions, stderr)

	done := make(chan error, 1)
	go func() {
		done <- sendConsoleInputs(ctx, client, agentID, interactionState, stdin, stdout)
	}()

	for {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if printEvent(stdout, event) && eventShouldPrompt(event) {
				printPrompt(stdout, "")
			}
		case interaction := <-interactions:
			printInteraction(stdout, interaction)
			printPrompt(stdout, promptPrefix(interaction))
		case err := <-errs:
			if err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
			errs = nil
			if done == nil {
				return nil
			}
		case err := <-done:
			cancel()
			if err != nil {
				return err
			}
			done = nil
			if errs == nil {
				return nil
			}
		}
	}
}

func (c *apiClient) createChatAgent(ctx context.Context, path string, stderr io.Writer) (string, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	now := time.Now().UTC().Format("20060102150405.000000000")

	if c.orgID == "" && c.projectID == "" {
		var orgResponse struct {
			Org struct {
				ID string `json:"id"`
			} `json:"org"`
			Project struct {
				ID string `json:"id"`
			} `json:"project"`
		}
		if err := c.postJSON(
			ctx,
			"/api/v1/orgs",
			map[string]any{"name": "Local CLI"},
			"cli-local-org",
			&orgResponse,
		); err != nil {
			return "", err
		}
		c.orgID = orgResponse.Org.ID
		c.projectID = orgResponse.Project.ID
	}
	_, _ = fmt.Fprintf(stderr, "org: %s\nproject: %s\n", c.orgID, c.projectID)

	var configResponse struct {
		ID string `json:"id"`
	}
	configBody := map[string]any{"source_format": "yaml", "source": string(source)}
	if err := c.postJSON(ctx, c.projectPath("agent-configs"), configBody, "", &configResponse); err != nil {
		if !shouldBootstrapModelProvider(err) {
			return "", err
		}
		if bootstrapErr := c.ensureDefaultModelProvider(ctx, source, stderr); bootstrapErr != nil {
			return "", fmt.Errorf("%w; model provider bootstrap failed: %w", err, bootstrapErr)
		}
		if err := c.postJSON(ctx, c.projectPath("agent-configs"), configBody, "", &configResponse); err != nil {
			return "", err
		}
	}
	_, _ = fmt.Fprintf(stderr, "agent config: %s\n", configResponse.ID)

	var agentResponse struct {
		Agent struct {
			ID string `json:"id"`
		} `json:"agent"`
	}
	if err := c.postJSON(
		ctx,
		c.projectPath("agents"),
		map[string]any{"config": configResponse.ID},
		"cli-agent-"+now,
		&agentResponse,
	); err != nil {
		return "", err
	}
	_, _ = fmt.Fprintf(stderr, "agent: %s\n", agentResponse.Agent.ID)
	return agentResponse.Agent.ID, nil
}

type localModelProviderBootstrap struct {
	providerConfig         string
	preset                 string
	apiKeyEnv              string
	secretName             string
	contextWindowTokens    int
	maxOutputTokens        int
	defaultMaxOutputTokens int
}

func (c *apiClient) ensureDefaultModelProvider(ctx context.Context, source []byte, stderr io.Writer) error {
	parsed, err := agentconfig.ParseSource(agentconfig.SourceFormatYAML, source)
	if err != nil {
		return fmt.Errorf("parse agent config for provider bootstrap: %w", err)
	}
	configuredModelName := strings.TrimSpace(parsed.Model.Name)
	if configuredModelName == "" {
		return nil
	}
	bootstrap, ok := localBootstrapForProviderConfig(strings.TrimSpace(parsed.Model.ProviderConfig))
	if !ok {
		return nil
	}
	providerConfigID, ok, err := c.findOrgModelProviderConfig(ctx, bootstrap.providerConfig)
	if err != nil {
		return err
	}
	if !ok {
		apiKey := strings.TrimSpace(os.Getenv(bootstrap.apiKeyEnv))
		if apiKey == "" {
			return fmt.Errorf(
				"agent config selects %s; set %s or create that org model provider config through the API",
				bootstrap.providerConfig,
				bootstrap.apiKeyEnv,
			)
		}
		secretID, err := c.ensureOrgSecret(ctx, bootstrap.secretName, apiKey)
		if err != nil {
			return err
		}
		providerConfigID, err = c.createPresetModelProviderConfig(ctx, bootstrap.providerConfig, bootstrap.preset, secretID)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stderr, "model provider config: %s\n", providerConfigID)
	}
	configuredModelID, err := c.createConfiguredModel(ctx, providerConfigID, configuredModelName, bootstrap)
	if err != nil {
		return err
	}
	if err := c.createProjectModelGrant(ctx, configuredModelID); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "configured model: %s\n", configuredModelID)
	return nil
}

func localBootstrapForProviderConfig(providerConfig string) (localModelProviderBootstrap, bool) {
	switch providerConfig {
	case "openai-prod":
		return localModelProviderBootstrap{
			providerConfig:         "openai-prod",
			preset:                 "openai",
			apiKeyEnv:              "OPENAI_API_KEY",
			secretName:             "openai-prod-api-key",
			contextWindowTokens:    128000,
			maxOutputTokens:        32768,
			defaultMaxOutputTokens: 4096,
		}, true
	case "openrouter-prod":
		return localModelProviderBootstrap{
			providerConfig:         "openrouter-prod",
			preset:                 "openrouter",
			apiKeyEnv:              "OPENROUTER_API_KEY",
			secretName:             "openrouter-prod-api-key",
			contextWindowTokens:    128000,
			maxOutputTokens:        32768,
			defaultMaxOutputTokens: 4096,
		}, true
	case "anthropic-prod":
		return localModelProviderBootstrap{
			providerConfig:         "anthropic-prod",
			preset:                 "anthropic",
			apiKeyEnv:              "ANTHROPIC_API_KEY",
			secretName:             "anthropic-prod-api-key",
			contextWindowTokens:    200000,
			maxOutputTokens:        8192,
			defaultMaxOutputTokens: 8192,
		}, true
	default:
		return localModelProviderBootstrap{}, false
	}
}

func (c *apiClient) findOrgModelProviderConfig(ctx context.Context, name string) (string, bool, error) {
	var response struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, c.orgPath("model-provider-configs"), &response); err != nil {
		return "", false, err
	}
	for _, config := range response.Data {
		if config.Name == name {
			return config.ID, true, nil
		}
	}
	return "", false, nil
}

func (c *apiClient) ensureOrgSecret(ctx context.Context, name string, apiKey string) (string, error) {
	var listResponse struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, c.orgPath("secrets"), &listResponse); err != nil {
		return "", err
	}
	for _, secret := range listResponse.Data {
		if secret.Name == name {
			return secret.ID, nil
		}
	}
	var response struct {
		ID string `json:"id"`
	}
	body := map[string]any{
		"name":    name,
		"kind":    "generic",
		"payload": map[string]string{"value": apiKey},
	}
	if err := c.postJSON(ctx, c.orgPath("secrets"), body, "cli-secret-"+name, &response); err != nil {
		return "", err
	}
	return response.ID, nil
}

func (c *apiClient) createPresetModelProviderConfig(
	ctx context.Context,
	name, preset, credentialSecretID string,
) (string, error) {
	var response struct {
		Config struct {
			ID string `json:"id"`
		} `json:"config"`
	}
	body := map[string]any{
		"name":                 name,
		"preset":               preset,
		"credential_secret_id": credentialSecretID,
	}
	if err := c.postJSON(
		ctx,
		c.orgPath("model-provider-configs"),
		body,
		"cli-model-provider-"+name,
		&response,
	); err != nil {
		return "", err
	}
	return response.Config.ID, nil
}

func (c *apiClient) createConfiguredModel(
	ctx context.Context,
	providerConfigID string,
	configuredModelName string,
	bootstrap localModelProviderBootstrap,
) (string, error) {
	var response struct {
		ID string `json:"id"`
	}
	body := map[string]any{
		"name":                      configuredModelName,
		"provider_model_slug":       configuredModelName,
		"context_window_tokens":     bootstrap.contextWindowTokens,
		"max_output_tokens":         bootstrap.maxOutputTokens,
		"default_max_output_tokens": bootstrap.defaultMaxOutputTokens,
		"supports_tools":            true,
	}
	if err := c.postJSON(
		ctx,
		c.orgPath("model-provider-configs", providerConfigID, "models"),
		body,
		"cli-model-"+providerConfigID+"-"+configuredModelName,
		&response,
	); err != nil {
		return "", err
	}
	return response.ID, nil
}

func (c *apiClient) createProjectModelGrant(ctx context.Context, configuredModelID string) error {
	body := map[string]any{"configured_model_id": configuredModelID}
	return c.postJSON(ctx, c.projectPath("model-grants"), body, "cli-model-grant-"+configuredModelID, nil)
}

func shouldBootstrapModelProvider(err error) bool {
	var apiErr apiError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		return false
	}
	body := strings.ToLower(apiErr.Body)
	// These phrases mirror the agent config resolver errors returned by the API.
	if !strings.Contains(body, "model.provider_config") && !strings.Contains(body, "configured model name") {
		return false
	}
	return strings.Contains(body, "not found") ||
		strings.Contains(body, "not configured") ||
		strings.Contains(body, "does not have an active project grant")
}

func sendConsoleInputs(
	ctx context.Context,
	client apiClient,
	agentID string,
	interactions *interactionState,
	stdin io.Reader,
	stdout io.Writer,
) error {
	scanner := bufio.NewScanner(stdin)
	printPrompt(stdout, "")
	for scanner.Scan() {
		_, _ = fmt.Fprintln(stdout)
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			printPrompt(stdout, "")
			continue
		}
		interaction, ok := interactions.next()
		if !ok {
			if open, err := client.listOpenInteractions(ctx, agentID); err == nil {
				interactions.replaceOpen(open)
				interaction, ok = interactions.next()
			}
		}
		if ok {
			form, err := parseInteractionForm(interaction.Request)
			if err != nil {
				return err
			}
			answerInputs := []string{text}
			for len(answerInputs) < len(form.Questions) {
				printPrompt(stdout, "answer question "+strconv.Itoa(len(answerInputs)))
				if !scanner.Scan() {
					if err := scanner.Err(); err != nil {
						return err
					}
					return errors.New("interaction ended before every question was answered")
				}
				_, _ = fmt.Fprintln(stdout)
				answerInputs = append(answerInputs, strings.TrimSpace(scanner.Text()))
			}
			answers, err := interactionFormAnswers(form, answerInputs)
			if err != nil {
				return err
			}
			if err := client.resolveInteraction(ctx, agentID, interaction.ID, answers); err != nil {
				return err
			}
			interactions.markResolved(interaction.ID)
			continue
		}
		if err := client.sendPromptInput(ctx, agentID, text); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (c *apiClient) sendPromptInput(ctx context.Context, agentID, text string) error {
	blocks, err := promptContentBlocks(text)
	if err != nil {
		return err
	}
	body := map[string]any{
		"content_blocks": blocks,
	}
	return c.postJSON(
		ctx,
		c.projectPath("agents", agentID, "inputs"),
		body,
		"cli-input-"+time.Now().UTC().Format("20060102150405.000000000"),
		nil,
	)
}

func promptContentBlocks(text string) ([]map[string]string, error) {
	refs, err := parseAttachmentRefs(text)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return []map[string]string{{"type": "text", "text": text}}, nil
	}
	blocks := make([]map[string]string, 0, len(refs)*2+1)
	cursor := 0
	for _, ref := range refs {
		if before := strings.TrimSpace(text[cursor:ref.start]); before != "" {
			blocks = append(blocks, map[string]string{"type": "text", "text": before})
		}
		block, err := attachmentContentBlock(ref.filename)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
		cursor = ref.end
	}
	if after := strings.TrimSpace(text[cursor:]); after != "" {
		blocks = append(blocks, map[string]string{"type": "text", "text": after})
	}
	if len(blocks) == 0 {
		return nil, errors.New("prompt must include text or at least one attachment")
	}
	return blocks, nil
}

type attachmentRef struct {
	start    int
	end      int
	filename string
}

func parseAttachmentRefs(text string) ([]attachmentRef, error) {
	var refs []attachmentRef
	for cursor := 0; cursor < len(text); {
		startRel := strings.IndexByte(text[cursor:], '@')
		if startRel < 0 {
			break
		}
		start := cursor + startRel
		if start > 0 && !isPromptSpace(text[start-1]) {
			cursor = start + 1
			continue
		}
		if start+1 >= len(text) {
			break
		}
		if text[start+1] == '[' {
			ref, err := parseBracketAttachmentRef(text, start)
			if err != nil {
				return nil, err
			}
			refs = append(refs, ref)
			cursor = ref.end
			continue
		}
		ref := parseBareAttachmentRef(text, start)
		if ref.filename == "" {
			cursor = start + 1
			continue
		}
		refs = append(refs, ref)
		cursor = ref.end
	}
	return refs, nil
}

func parseBracketAttachmentRef(text string, start int) (attachmentRef, error) {
	nameStart := start + len("@[")
	nameEndRel := strings.IndexByte(text[nameStart:], ']')
	if nameEndRel < 0 {
		return attachmentRef{}, fmt.Errorf("unterminated attachment reference starting at %q", text[start:])
	}
	end := nameStart + nameEndRel
	filename := strings.TrimSpace(text[nameStart:end])
	if filename == "" {
		return attachmentRef{}, errors.New("attachment filename must not be empty")
	}
	return attachmentRef{start: start, end: end + 1, filename: filename}, nil
}

func parseBareAttachmentRef(text string, start int) attachmentRef {
	nameStart := start + 1
	end := nameStart
	for end < len(text) && !isPromptSpace(text[end]) {
		end++
	}
	filename := strings.TrimSpace(text[nameStart:end])
	return attachmentRef{start: start, end: end, filename: filename}
}

func isPromptSpace(ch byte) bool {
	switch ch {
	case ' ', '\t', '\n', '\r':
		return true
	default:
		return false
	}
}

func attachmentContentBlock(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read attachment %s: %w", path, err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("attachment %s must not be empty", path)
	}
	mediaType, _, err := attachmentMediaType(path, data)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"type":       "media",
		"media_type": mediaType,
		"filename":   filepath.Base(path),
		"data":       base64.StdEncoding.EncodeToString(data),
	}, nil
}

func attachmentMediaType(path string, data []byte) (string, string, error) {
	mediaType := mediaTypeByExtension(filepath.Ext(path))
	if mediaType == "" {
		if typed := mime.TypeByExtension(filepath.Ext(path)); typed != "" {
			mediaType = strings.TrimSpace(strings.Split(typed, ";")[0])
		}
	}
	if mediaType == "" {
		mediaType = http.DetectContentType(data)
	}
	mediaType = strings.TrimSpace(strings.Split(mediaType, ";")[0])
	kind, ok := modelcontext.AttachmentKindForMediaType(mediaType)
	if !ok {
		return "", "", fmt.Errorf("attachment %s has unsupported media type %q", path, mediaType)
	}
	return mediaType, kind, nil
}

func mediaTypeByExtension(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".pdf":
		return "application/pdf"
	case ".txt":
		return "text/plain"
	case ".yaml", ".yml":
		return "text/plain"
	case ".md", ".markdown":
		return "text/markdown"
	case ".csv":
		return "text/csv"
	case ".tsv":
		return "text/tab-separated-values"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	default:
		return ""
	}
}

func (c *apiClient) resolveInteraction(
	ctx context.Context,
	agentID string,
	interactionID string,
	answers []interactionAnswer,
) error {
	body := interactionResolution{Answers: answers}
	return c.postJSON(ctx, c.projectPath("agents", agentID, "interactions", interactionID, "resolve"), body, "", nil)
}

func (c *apiClient) pollOpenInteractions(
	ctx context.Context,
	agentID string,
	state *interactionState,
	out chan<- pendingInteraction,
	stderr io.Writer,
) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastErr := ""
	for {
		interactions, err := c.listOpenInteractions(ctx, agentID)
		if err == nil {
			lastErr = ""
			for _, interaction := range state.replaceOpen(interactions) {
				select {
				case out <- interaction:
				case <-ctx.Done():
					return
				}
			}
		} else if message := err.Error(); message != lastErr {
			lastErr = message
			_, _ = fmt.Fprintf(stderr, "interaction poll error: %s\n", message)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *apiClient) listOpenInteractions(ctx context.Context, agentID string) ([]pendingInteraction, error) {
	var response struct {
		Data []pendingInteraction `json:"data"`
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		c.url(c.projectPath("agents", agentID, "interactions")+"?state=open"),
		nil,
	)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if err := c.doJSON(req, &response); err != nil {
		return nil, err
	}
	out := make([]pendingInteraction, 0, len(response.Data))
	for _, interaction := range response.Data {
		if (interaction.InteractionKind == "question" || interaction.InteractionKind == "permission") &&
			interaction.State == "open" {
			out = append(out, interaction)
		}
	}
	return out, nil
}

func (c *apiClient) postJSON(ctx context.Context, path string, body any, idempotencyKey string, out any) error {
	var payload bytes.Buffer
	if err := json.NewEncoder(&payload).Encode(body); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(path), &payload)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	return c.doJSON(req, out)
}

func (c *apiClient) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(path), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	return c.doJSON(req, out)
}

func (c *apiClient) doJSON(req *http.Request, out any) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := readResponseBody(resp.Body, maxJSONResponseBytes)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiError{
			Method:     req.Method,
			Path:       req.URL.Path,
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(data)),
		}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

type apiError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e apiError) Error() string {
	return fmt.Sprintf("%s %s: %s", e.Method, e.Path, e.Body)
}

func (c *apiClient) streamEvents(ctx context.Context, agentID string, out chan<- agentEvent) error {
	defer close(out)
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		c.url(c.projectPath("agents", agentID, "events", "stream")+"?after_sequence=0"),
		nil,
	)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := readResponseBody(resp.Body, maxErrorResponseBytes)
		return fmt.Errorf("GET %s: %s", req.URL.Path, strings.TrimSpace(string(data)))
	}
	return readSSE(ctx, resp.Body, out)
}

func readResponseBody(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response exceeds the %d-byte limit", limit)
	}
	return data, nil
}

func readSSE(ctx context.Context, body io.Reader, out chan<- agentEvent) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var data strings.Builder
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := scanner.Text()
		if line == "" {
			if data.Len() > 0 {
				var event agentEvent
				if err := json.Unmarshal([]byte(data.String()), &event); err == nil {
					out <- event
				}
				data.Reset()
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func (c *apiClient) projectPath(parts ...string) string {
	all := []string{"api", "v1", "orgs", c.orgID, "projects", c.projectID}
	all = append(all, parts...)
	for i, part := range all {
		all[i] = url.PathEscape(part)
	}
	return "/" + strings.Join(all, "/")
}

func (c *apiClient) orgPath(parts ...string) string {
	all := []string{"api", "v1", "orgs", c.orgID}
	all = append(all, parts...)
	for i, part := range all {
		all[i] = url.PathEscape(part)
	}
	return "/" + strings.Join(all, "/")
}

func (c *apiClient) url(path string) string {
	return c.baseURL + path
}

type agentEvent struct {
	Sequence      int64          `json:"sequence"`
	EventKind     string         `json:"event_kind"`
	ToolCallID    string         `json:"tool_call_id"`
	Outcome       string         `json:"outcome"`
	ContentBlocks []contentBlock `json:"content_blocks"`
	CreatedAt     time.Time      `json:"created_at"`
}

type contentBlock struct {
	Type       string          `json:"type"`
	Text       string          `json:"text"`
	Name       string          `json:"name"`
	ToolName   string          `json:"tool_name"`
	ToolCallID string          `json:"tool_call_id"`
	Input      json.RawMessage `json:"input"`
	Value      json.RawMessage `json:"value"`
	Raw        map[string]any  `json:"-"`
}

type pendingInteraction struct {
	ID              string          `json:"id"`
	InteractionKind string          `json:"interaction_kind"`
	State           string          `json:"state"`
	Request         json.RawMessage `json:"request"`
}

type interactionForm struct {
	Title     string                   `json:"title"`
	Context   []interactionContextItem `json:"context"`
	Questions []interactionQuestion    `json:"questions"`
}

type interactionContextItem struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type interactionQuestion struct {
	Prompt   string                      `json:"prompt"`
	Multiple bool                        `json:"multiple"`
	Options  []interactionQuestionOption `json:"options"`
}

type interactionQuestionOption struct {
	Label      string `json:"label"`
	AllowsText bool   `json:"allows_text"`
}

type interactionResolution struct {
	Answers []interactionAnswer `json:"answers"`
}

type interactionAnswer struct {
	OptionIndices []int  `json:"option_indices"`
	Text          string `json:"text,omitempty"`
}

func parseInteractionForm(raw json.RawMessage) (interactionForm, error) {
	var value interactionForm
	if err := json.Unmarshal(raw, &value); err != nil {
		return interactionForm{}, fmt.Errorf("decode interaction form: %w", err)
	}
	if strings.TrimSpace(value.Title) == "" || len(value.Questions) == 0 {
		return interactionForm{}, errors.New("interaction form is incomplete")
	}
	return value, nil
}

func interactionFormAnswers(
	value interactionForm,
	inputs []string,
) ([]interactionAnswer, error) {
	if len(inputs) != len(value.Questions) {
		return nil, fmt.Errorf("expected %d question answers", len(value.Questions))
	}
	answers := make([]interactionAnswer, 0, len(value.Questions))
	for index, question := range value.Questions {
		answer, err := interactionFormAnswer(question, inputs[index])
		if err != nil {
			return nil, fmt.Errorf("question %d: %w", index, err)
		}
		answers = append(answers, answer)
	}
	return answers, nil
}

func interactionFormOptionIndices(question interactionQuestion, input string) ([]int, error) {
	selections := []string{input}
	if question.Multiple {
		selections = strings.Split(input, "+")
	}
	optionIndices := make([]int, 0, len(selections))
	for _, selection := range selections {
		selection = strings.TrimSpace(selection)
		if index, err := strconv.Atoi(selection); err == nil {
			if index >= 0 && index < len(question.Options) {
				optionIndices = append(optionIndices, index)
				continue
			}
		}
		matchedIndex := -1
		for index, option := range question.Options {
			if strings.EqualFold(selection, option.Label) {
				matchedIndex = index
				break
			}
		}
		if matchedIndex == -1 {
			return nil, fmt.Errorf("no option matches %q", selection)
		}
		optionIndices = append(optionIndices, matchedIndex)
	}
	return optionIndices, nil
}

func interactionFormAnswer(
	question interactionQuestion,
	input string,
) (interactionAnswer, error) {
	optionIndices, err := interactionFormOptionIndices(question, input)
	if err == nil {
		return interactionAnswer{OptionIndices: optionIndices}, nil
	}
	selection, text, hasText := strings.Cut(input, ":")
	if !hasText {
		return interactionAnswer{}, err
	}
	optionIndices, selectionErr := interactionFormOptionIndices(question, selection)
	if selectionErr != nil {
		return interactionAnswer{}, selectionErr
	}
	allowsText := false
	for _, optionIndex := range optionIndices {
		allowsText = allowsText || question.Options[optionIndex].AllowsText
	}
	if !allowsText {
		return interactionAnswer{}, errors.New("selected options do not accept text")
	}
	return interactionAnswer{
		OptionIndices: optionIndices,
		Text:          strings.TrimSpace(text),
	}, nil
}

type interactionState struct {
	mu        sync.Mutex
	pending   []pendingInteraction
	answering map[string]bool
	seen      map[string]bool
}

func newInteractionState() *interactionState {
	return &interactionState{answering: map[string]bool{}, seen: map[string]bool{}}
}

func (s *interactionState) replaceOpen(open []pendingInteraction) []pendingInteraction {
	s.mu.Lock()
	defer s.mu.Unlock()
	openIDs := map[string]bool{}
	nextPending := make([]pendingInteraction, 0, len(open))
	var newlySeen []pendingInteraction
	for _, interaction := range open {
		openIDs[interaction.ID] = true
		if s.answering[interaction.ID] {
			continue
		}
		nextPending = append(nextPending, interaction)
		if !s.seen[interaction.ID] {
			s.seen[interaction.ID] = true
			newlySeen = append(newlySeen, interaction)
		}
	}
	for id := range s.answering {
		if !openIDs[id] {
			delete(s.answering, id)
		}
	}
	s.pending = nextPending
	return newlySeen
}

func (s *interactionState) next() (pendingInteraction, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, interaction := range s.pending {
		if !s.answering[interaction.ID] {
			s.answering[interaction.ID] = true
			return interaction, true
		}
	}
	return pendingInteraction{}, false
}

func (s *interactionState) markResolved(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.answering, id)
	for i, interaction := range s.pending {
		if interaction.ID == id {
			s.pending = append(s.pending[:i], s.pending[i+1:]...)
			return
		}
	}
}

func (b *contentBlock) UnmarshalJSON(data []byte) error {
	type alias contentBlock
	var block alias
	if err := json.Unmarshal(data, &block); err != nil {
		return err
	}
	var raw map[string]any
	_ = json.Unmarshal(data, &raw)
	*b = contentBlock(block)
	b.Raw = raw
	return nil
}

func printPrompt(w io.Writer, prefix string) {
	if prefix == "" {
		_, _ = fmt.Fprintf(w, "\n%s>%s ", ansiCyan, ansiReset)
		return
	}
	_, _ = fmt.Fprintf(w, "\n%s %s>%s ", prefix, ansiCyan, ansiReset)
}

func printInteraction(w io.Writer, interaction pendingInteraction) {
	switch interaction.InteractionKind {
	case "permission":
		_, _ = fmt.Fprintf(w, "%s %s\n", eventLabel("approval"), interactionFormPrompt(interaction))
	default:
		_, _ = fmt.Fprintf(w, "%s %s\n", eventLabel("question"), interactionFormPrompt(interaction))
	}
}

func promptPrefix(interaction pendingInteraction) string {
	switch interaction.InteractionKind {
	case "permission":
		return "approve tool [allow/deny]"
	case "question":
		return "choose option numbers"
	default:
		return ""
	}
}

func interactionFormPrompt(interaction pendingInteraction) string {
	value, err := parseInteractionForm(interaction.Request)
	if err != nil {
		return compactJSON(interaction.Request)
	}
	lines := []string{value.Title}
	for _, item := range value.Context {
		lines = append(lines, item.Label+": "+item.Value)
	}
	for questionIndex, question := range value.Questions {
		options := make([]string, 0, len(question.Options))
		for optionIndex, option := range question.Options {
			label := strconv.Itoa(optionIndex) + "=" + option.Label
			if option.AllowsText {
				label += " (: text)"
			}
			options = append(options, label)
		}
		lines = append(
			lines,
			strconv.Itoa(questionIndex)+": "+question.Prompt+" ["+strings.Join(options, ", ")+"]",
		)
	}
	return strings.Join(lines, "\n  ")
}

func printEvent(w io.Writer, event agentEvent) bool {
	if event.EventKind == "" {
		return false
	}
	if event.EventKind == "agent_input" {
		return false
	}
	printed := false
	prefix := "system"
	switch event.EventKind {
	case "model_output":
		prefix = "agent"
	case "agent_input":
		prefix = "user"
	case "tool_result":
		prefix = "tool result"
	}
	if event.EventKind == "tool_result" && len(event.ContentBlocks) == 0 && event.Outcome != "" {
		_, _ = fmt.Fprintln(w, eventTextLabel(event, prefix))
		return true
	}
	for _, block := range event.ContentBlocks {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				_, _ = fmt.Fprintf(w, "%s %s\n", eventTextLabel(event, prefix), block.Text)
				printed = true
			}
		case "tool_call":
			printToolCall(w, block)
			printed = true
		case "structured_data":
			if len(block.Value) != 0 {
				_, _ = fmt.Fprintf(w, "%s", eventTextLabel(event, prefix))
				for _, line := range toolResultLines(block.Value) {
					_, _ = fmt.Fprintln(w)
					printDetailLine(w, line)
				}
				printed = true
			}
		case "status", "error":
			_, _ = fmt.Fprintf(w, "%s %s\n", eventLabel(block.Type), compactJSONFromMap(block.Raw))
			printed = true
		default:
			_, _ = fmt.Fprintf(w, "%s %s\n", eventLabel(block.Type), compactJSONFromMap(block.Raw))
			printed = true
		}
	}
	return printed
}

func eventShouldPrompt(event agentEvent) bool {
	if event.EventKind != "model_output" {
		return false
	}
	for _, block := range event.ContentBlocks {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			return true
		}
	}
	return false
}

func printToolCall(w io.Writer, block contentBlock) {
	name := firstNonEmpty(block.Name, block.ToolName, "tool")
	_, _ = fmt.Fprintf(w, "%s %s%s%s", eventLabel("tool call"), ansiMagenta, name, ansiReset)
	if block.ToolCallID != "" {
		_, _ = fmt.Fprintf(w, " %s%s%s", ansiGray, block.ToolCallID, ansiReset)
	}
	_, _ = fmt.Fprintln(w)
	for _, line := range toolCallLines(name, block.Input) {
		printDetailLine(w, line)
	}
}

func toolCallLines(name string, input json.RawMessage) []string {
	if len(input) == 0 || string(input) == "null" {
		return nil
	}
	if name == "ask_question" {
		if questions := questionLines(input); len(questions) > 0 {
			return questions
		}
	}
	return []string{detailLine("input", compactJSON(input))}
}

func questionLines(input json.RawMessage) []string {
	var request struct {
		Questions []interactionQuestion `json:"questions"`
	}
	if json.Unmarshal(input, &request) != nil {
		return nil
	}
	var lines []string
	for _, question := range request.Questions {
		if question.Prompt != "" {
			lines = append(lines, detailLine("question", question.Prompt))
		}
	}
	return lines
}

func toolResultLines(output json.RawMessage) []string {
	var response struct {
		Answers []struct {
			Question        string   `json:"question"`
			SelectedOptions []string `json:"selected_options"`
		} `json:"answers"`
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	if json.Unmarshal(output, &response) == nil {
		var lines []string
		for _, answer := range response.Answers {
			if len(answer.SelectedOptions) > 0 {
				lines = append(
					lines,
					detailLine("answer", answer.Question+": "+strings.Join(answer.SelectedOptions, ", ")),
				)
			}
		}
		if response.Output != "" {
			lines = append(lines, detailLine("output", response.Output))
		}
		if response.Error != "" {
			lines = append(lines, detailLine("error", response.Error))
		}
		if len(lines) > 0 {
			return lines
		}
	}
	return []string{detailLine("result", compactJSON(output))}
}

func detailLine(key, value string) string {
	return key + "\t" + value
}

func printDetailLine(w io.Writer, line string) {
	key, value, ok := strings.Cut(line, "\t")
	if !ok {
		_, _ = fmt.Fprintf(w, "  %s\n", line)
		return
	}
	_, _ = fmt.Fprintf(w, "  %s%-8s%s %s\n", ansiDim, key, ansiReset, value)
}

func eventTextLabel(event agentEvent, fallback string) string {
	if event.EventKind == "tool_result" && event.ToolCallID != "" {
		label := eventLabel("tool result") + " " + event.ToolCallID
		if event.Outcome != "" {
			label += " (" + event.Outcome + ")"
		}
		return label
	}
	return eventLabel(fallback)
}

func eventLabel(label string) string {
	if label == "" {
		label = "event"
	}
	color := ansiGray
	switch label {
	case "agent":
		color = ansiGreen
	case "tool call":
		color = ansiMagenta
	case "tool result":
		color = ansiBlue
	case "question":
		color = ansiCyan
	case "approval":
		color = ansiYellow
	case "error":
		color = ansiRed
	case "status":
		color = ansiGray
	}
	if color != "" {
		return color + "[" + label + "]" + ansiReset
	}
	if label == "agent" {
		return ansiGreen + "[agent]" + ansiReset
	}
	return "[" + label + "]"
}

func compactJSON(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(out)
}

func compactJSONFromMap(raw map[string]any) string {
	out, err := json.Marshal(raw)
	if err != nil {
		return fmt.Sprint(raw)
	}
	return string(out)
}

func printUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, `Usage:
  testagent [flags] chat <agent.yaml>

Flags:
  --api-url  API URL, defaults to OMNARA_API_URL or http://localhost:8080
  --token    Personal access token, defaults to OMNARA_TOKEN
  --org-id   Existing organization ID, defaults to OMNARA_ORG_ID
  --project-id
             Existing project ID, defaults to OMNARA_PROJECT_ID`)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
