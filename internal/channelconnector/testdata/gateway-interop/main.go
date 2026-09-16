// gateway-interop drives the real Go client against the TypeScript test receiver.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/channelconnector"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("interop completed")
}

func run() error {
	if len(os.Args) != 2 && (len(os.Args) != 3 || os.Args[2] != "reject-upload") {
		return errors.New("expected test gateway endpoint")
	}
	rejectUpload := len(os.Args) == 3
	capability := channelconnector.Capability{ConnectorKey: "test_connector", Provider: "slack"}
	client, err := channelconnector.NewOperationsClient([]channelconnector.Config{{
		ID: "interop-test", OperationsURL: os.Args[1], Capabilities: []channelconnector.Capability{capability},
		Token: "omnara_connector_v1_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa_3Q2mUc",
	}}, nil)
	if err != nil {
		return err
	}
	for _, kind := range []channelconnector.OperationKind{
		channelconnector.OperationSend, channelconnector.OperationRead, channelconnector.OperationInteraction,
	} {
		request := channelconnector.OperationRequest{
			RequestID: "interop-" + string(kind), Capability: capability, Kind: kind,
			Scope: channelconnector.OperationScope{
				ProjectID: "project", IntegrationAppID: "app", IntegrationInstallID: "install",
				AgentID: "agent", ChannelID: "channel",
			},
			Payload: json.RawMessage(`{"integer":9007199254740993}`),
		}
		if kind == channelconnector.OperationSend {
			request.Artifacts = []channelconnector.OperationArtifact{{
				ID: "artifact-1", Filename: "résumé.txt", ContentType: "text/plain",
				Open: func(ctx context.Context) (io.ReadCloser, error) {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					return io.NopCloser(strings.NewReader(strings.Repeat("x", 12*1024*1024))), nil
				},
			}}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		result, err := client.Execute(ctx, request)
		cancel()
		if rejectUpload {
			var operationError *channelconnector.OperationError
			if !errors.As(err, &operationError) || result.Outcome != channelconnector.OperationFailed ||
				operationError.Code != "gateway_failed" || operationError.StatusCode != 503 {
				return fmt.Errorf("expected definite upload rejection, got %s: %w", result.Outcome, err)
			}
			return nil
		}
		if err != nil {
			return err
		}
		if result.RequestID != request.RequestID || result.Outcome != channelconnector.OperationCompleted ||
			string(result.Payload) != `{"publication":"draft"}` {
			return errors.New("unexpected interop result")
		}
	}
	return nil
}
