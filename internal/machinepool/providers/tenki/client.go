package tenki

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	sdk "github.com/LuxorLabs/tenki-sdk-go/sandbox"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
)

type sdkClient struct {
	baseURL string
	token   string
}

func (c *sdkClient) newClient() (*sdk.Client, error) {
	client := outboundhttp.NewPublicClient(outboundhttp.PublicClientOptions{DisableResponseHeaderTimeout: true})
	return sdk.New(
		sdk.WithBaseURL(c.baseURL),
		sdk.WithAuthToken(c.token),
		sdk.WithHTTPClient(client),
		sdk.WithWarningHandler(func(sdk.SandboxWarning) {}),
	)
}

func fromSession(s *sdk.Session) sandbox {
	if s == nil {
		return sandbox{}
	}
	return sandbox{
		ID:         s.ID,
		State:      string(s.State),
		Metadata:   s.Metadata,
		CPU:        int(s.CPUCores),
		MemoryMB:   int(s.MemoryMB),
		DiskSizeGB: s.DiskSizeGB,
		Sticky:     s.Sticky,
	}
}

func (c *sdkClient) Create(ctx context.Context, request createRequest) (sandbox, error) {
	client, err := c.newClient()
	if err != nil {
		return sandbox{}, err
	}
	defer func() { _ = client.Close() }()
	opts := []sdk.CreateOption{
		sdk.WithName(request.Name),
		sdk.WithCPUCores(int32(request.CPU)),
		sdk.WithMemoryMB(int32(request.MemoryMB)),
		sdk.WithDiskSizeGB(int(request.Options.DiskSizeGB)),
		sdk.WithMetadata(request.Metadata),
		sdk.WithTags(managedTag),
		sdk.WithSticky(),
		sdk.WithAllowInbound(false),
		sdk.WithAllowOutbound(true),
		sdk.WithWaitReady(false),
	}
	if request.Options.Image != "" {
		opts = append(opts, sdk.WithImage(request.Options.Image))
	}
	session, err := client.Create(ctx, opts...)
	return fromSession(session), err
}

func (c *sdkClient) List(ctx context.Context) ([]sandbox, error) {
	client, err := c.newClient()
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()
	sessions, err := client.List(ctx, sdk.WithTagFilter(managedTag), sdk.WithIncludeTerminated(true))
	if err != nil {
		return nil, err
	}
	result := make([]sandbox, len(sessions))
	for i, session := range sessions {
		result[i] = fromSession(session)
	}
	return result, nil
}

func (c *sdkClient) Get(ctx context.Context, id string) (sandbox, bool, error) {
	client, err := c.newClient()
	if err != nil {
		return sandbox{}, false, err
	}
	defer func() { _ = client.Close() }()
	session, err := client.Get(ctx, id)
	if errors.Is(err, sdk.ErrSessionNotFound) {
		return sandbox{}, false, nil
	}
	if err != nil {
		return sandbox{}, false, err
	}
	return fromSession(session), true, nil
}

func (c *sdkClient) WaitReady(ctx context.Context, id string) (sandbox, error) {
	client, err := c.newClient()
	if err != nil {
		return sandbox{}, err
	}
	defer func() { _ = client.Close() }()
	session, err := client.Get(ctx, id)
	if err != nil {
		return sandbox{}, err
	}
	if err := session.WaitReady(ctx, provisioningTimeout); err != nil {
		return fromSession(session), err
	}
	return fromSession(session), nil
}

func (c *sdkClient) Delete(ctx context.Context, id string) error {
	client, err := c.newClient()
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	session, err := client.Get(ctx, id)
	if errors.Is(err, sdk.ErrSessionNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if session.State == sdk.SessionStateTerminated {
		return nil
	}
	err = session.CloseIfOpen(ctx)
	if errors.Is(err, sdk.ErrSessionNotFound) {
		return nil
	}
	return err
}

func (c *sdkClient) Bootstrap(ctx context.Context, id string, env map[string]string) error {
	script, err := bootstrapScript(env)
	if err != nil {
		return err
	}
	client, err := c.newClient()
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	session, err := client.Get(ctx, id)
	if err != nil {
		return err
	}
	result, err := session.Command([]string{"/bin/sh", "-s"}, sdk.RunOptions{Stdin: strings.NewReader(script)}).
		Exec(ctx)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("tenki daemon launcher exited with status %d", result.ExitCode)
	}
	return nil
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func environmentScript(env map[string]string) (string, error) {
	keys := make([]string, 0, len(env))
	for key := range env {
		if key == "" {
			return "", errors.New("empty environment key")
		}
		for i, r := range key {
			if r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (i == 0 || r < '0' || r > '9') {
				return "", fmt.Errorf("invalid environment key %q", key)
			}
		}
		if strings.ContainsRune(env[key], 0) {
			return "", fmt.Errorf("environment value for %s contains NUL", key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var script strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&script, "export %s=%s\n", key, shellQuote(env[key]))
	}
	return script.String(), nil
}
