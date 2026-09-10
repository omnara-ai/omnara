package modal

import (
	"context"
	"errors"
	"time"

	modalsdk "github.com/modal-labs/modal-client/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type createSandboxRequest struct {
	Name     string
	Image    string
	CPU      float64
	MemoryMB int
	Timeout  time.Duration
	Command  []string
	Env      map[string]string
	Region   string
	Tags     map[string]string
}

type sandbox struct {
	ID      string
	Tags    map[string]string
	Running bool
}

type apiClient interface {
	CreateSandbox(context.Context, createSandboxRequest) (sandbox, error)
	GetSandboxByName(context.Context, string) (sandbox, bool, error)
	GetSandboxByID(context.Context, string) (sandbox, bool, error)
	DeleteSandbox(context.Context, string) error
	Close()
}

type modalAPI struct {
	client      *modalsdk.Client
	app         string
	environment string
}

func newModalAPI(
	app string,
	environment string,
	credential providerCredential,
) (*modalAPI, error) {
	client, err := modalsdk.NewClientWithOptions(&modalsdk.ClientParams{
		TokenID:     credential.TokenID,
		TokenSecret: credential.TokenSecret,
		Environment: environment,
	})
	if err != nil {
		return nil, err
	}
	return &modalAPI{client: client, app: app, environment: environment}, nil
}

func (c *modalAPI) Close() {
	c.client.Close()
}

func (c *modalAPI) CreateSandbox(
	ctx context.Context,
	request createSandboxRequest,
) (sandbox, error) {
	app, err := c.client.Apps.FromName(ctx, c.app, &modalsdk.AppFromNameParams{
		Environment:     c.environment,
		CreateIfMissing: true,
	})
	if err != nil {
		return sandbox{}, err
	}
	regions := []string(nil)
	if request.Region != "" {
		regions = []string{request.Region}
	}
	created, err := c.client.Sandboxes.Create(
		ctx,
		app,
		c.client.Images.FromRegistry(request.Image, nil),
		&modalsdk.SandboxCreateParams{
			CPU:            request.CPU,
			CPULimit:       request.CPU,
			MemoryMiB:      request.MemoryMB,
			MemoryLimitMiB: request.MemoryMB,
			Timeout:        request.Timeout,
			Command:        request.Command,
			Env:            request.Env,
			Regions:        regions,
			Name:           request.Name,
			Tags:           request.Tags,
		},
	)
	if err != nil {
		return sandbox{}, err
	}
	_ = created.Detach()
	return sandbox{ID: created.SandboxID, Tags: request.Tags, Running: true}, nil
}

func (c *modalAPI) GetSandboxByName(
	ctx context.Context,
	name string,
) (sandbox, bool, error) {
	target, err := c.client.Sandboxes.FromName(
		ctx,
		c.app,
		name,
		&modalsdk.SandboxFromNameParams{Environment: c.environment},
	)
	if isNotFound(err) {
		return sandbox{}, false, nil
	}
	if err != nil {
		return sandbox{}, false, err
	}
	return describeSandbox(ctx, target)
}

func (c *modalAPI) GetSandboxByID(
	ctx context.Context,
	id string,
) (sandbox, bool, error) {
	target, err := c.client.Sandboxes.FromID(ctx, id, nil)
	if err != nil {
		return sandbox{}, false, err
	}
	return describeSandbox(ctx, target)
}

func (c *modalAPI) DeleteSandbox(ctx context.Context, id string) error {
	target, err := c.client.Sandboxes.FromID(ctx, id, nil)
	if err != nil {
		return err
	}
	_, err = target.Terminate(ctx, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

func describeSandbox(ctx context.Context, target *modalsdk.Sandbox) (sandbox, bool, error) {
	defer func() { _ = target.Detach() }()
	tags, err := target.GetTags(ctx, nil)
	if isNotFound(err) {
		return sandbox{}, false, nil
	}
	if err != nil {
		return sandbox{}, false, err
	}
	exitCode, err := target.Poll(ctx, nil)
	if isNotFound(err) {
		return sandbox{}, false, nil
	}
	if err != nil {
		return sandbox{}, false, err
	}
	return sandbox{ID: target.SandboxID, Tags: tags, Running: exitCode == nil}, true, nil
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var notFound modalsdk.NotFoundError
	return errors.As(err, &notFound) || status.Code(err) == codes.NotFound
}
