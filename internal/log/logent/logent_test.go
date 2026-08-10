package logent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func TestLogEntitiesAttachCanonicalFields(t *testing.T) {
	orgID, projectID, agentID := testID(1), testID(2), testID(3)
	turnID, lockID, workerID := testID(4), testID(5), testID(6)
	machineID, daemonRuntimeID := testID(7), testID(8)
	daemonInstanceID := testID(12)

	tests := []struct {
		name   string
		attach func(ctx context.Context)
		want   map[string]any
		banned []string
	}{
		{
			name: "Org",
			attach: func(ctx context.Context) {
				Org(ctx, identitystore.OrgRecord{ID: orgID, Name: "acme"})
			},
			want: map[string]any{"org.id": orgID.String(), "org.name": "acme"},
		},
		{
			name: "Project",
			attach: func(ctx context.Context) {
				Project(ctx, identitystore.ProjectRecord{ID: projectID, OrgID: orgID, Name: "p"})
			},
			want: map[string]any{"org.id": orgID.String(), "project.id": projectID.String(), "project.name": "p"},
		},
		{
			name: "Agent",
			attach: func(ctx context.Context) {
				Agent(ctx, executionstore.AgentRecord{
					ID: agentID, OrgID: orgID, ProjectID: projectID,
					State: "active",
				})
			},
			want: map[string]any{"agent.id": agentID.String(), "agent.state": "active"},
		},
		{
			name: "AgentInput",
			attach: func(ctx context.Context) {
				AgentInput(ctx, executionstore.AgentInputRecord{
					ID:           testID(9),
					AgentID:      agentID,
					DeliveryMode: executionstore.DeliveryModeSteering,
				})
			},
			want: map[string]any{
				"input.id":            testID(9).String(),
				"input.delivery_mode": string(executionstore.DeliveryModeSteering),
			},
		},
		{
			name: "RuntimeLock",
			attach: func(ctx context.Context) {
				RuntimeLock(ctx, executionstore.AgentRuntimeLockRecord{
					ID: lockID, AgentID: agentID, WorkerProcessID: workerID,
				})
			},
			want: map[string]any{
				"runtime_lock.id":   lockID.String(),
				"worker.process_id": workerID.String(),
			},
		},
		{
			name: "Turn",
			attach: func(ctx context.Context) {
				Turn(ctx, executionstore.AgentTurnRecord{
					ID: turnID, AgentID: agentID, TurnSequence: 7,
				})
			},
			want: map[string]any{"turn.id": turnID.String(), "turn.sequence": float64(7)},
		},
		{
			name: "Model request media omission records body limit without content",
			attach: func(ctx context.Context) {
				ModelRequestMediaOmittedForBodyLimit(ctx, 2, 32_100_000, 31_998_976)
			},
			want: map[string]any{
				"model_request.media_omitted.artifact_count": float64(2),
				"model_request.media_omitted.reason":         "provider_body_limit",
				"model_request.body_bytes_before_omission":   float64(32_100_000),
				"model_request.body_byte_limit":              float64(31_998_976),
			},
			banned: []string{"artifact_id", "content", "media_data"},
		},
		{
			name: "Machine skips config blobs",
			attach: func(ctx context.Context) {
				Machine(ctx, executionstore.MachineRecord{
					ID: machineID, OrgID: orgID, Provider: "fly",
					LifecycleState: "active", ConnectionState: "online",
					ProviderOptions: json.RawMessage(`{"secret":1}`),
				})
			},
			want:   map[string]any{"machine.id": machineID.String(), "machine.provider": "fly"},
			banned: []string{"machine.provider_options", "machine.metadata", "provider_options", "metadata"},
		},
		{
			name: "MachineFailureReport includes searchable failure fields",
			attach: func(ctx context.Context) {
				exitStatus := 7
				MachineFailureReport(ctx, executionstore.MachineFailureReportInput{
					OrgID:           orgID,
					MachineID:       machineID,
					Stage:           "startup_script",
					ExitStatus:      &exitStatus,
					OutputTail:      []byte("startup failed"),
					OutputTruncated: true,
				})
			},
			want: map[string]any{
				"level":                                   "warn",
				"org.id":                                  orgID.String(),
				"machine.id":                              machineID.String(),
				"machine.failure_report.stage":            "startup_script",
				"machine.failure_report.exit_status":      float64(7),
				"machine.failure_report.output_tail":      "startup failed",
				"machine.failure_report.output_truncated": true,
			},
		},
		{
			name: "DaemonRuntime includes version and skips state blobs",
			attach: func(ctx context.Context) {
				DaemonRuntime(ctx, executionstore.DaemonRuntimeRecord{
					ID: daemonRuntimeID, OrgID: orgID, MachineID: machineID,
					DaemonInstanceID: daemonInstanceID,
					DaemonVersion:    "1.2.3",
					State:            "active",
					Capacity:         json.RawMessage(`{"y":2}`),
				})
			},
			want: map[string]any{
				"daemon_runtime.id":                 daemonRuntimeID.String(),
				"daemon_runtime.daemon_instance_id": daemonInstanceID.String(),
				"daemon_runtime.daemon_version":     "1.2.3",
				"daemon_runtime.state":              "active",
			},
			banned: []string{"daemon_runtime.capacity", "daemon_runtime.metadata"},
		},
		{
			name: "DaemonRuntimeRegistration includes reconciliation counts",
			attach: func(ctx context.Context) {
				DaemonRuntimeRegistration(ctx, executionstore.DaemonRuntimeRegistrationRecord{
					Runtime: executionstore.DaemonRuntimeRecord{ID: daemonRuntimeID, State: "active"},
					Reconciliation: executionstore.DaemonRuntimeReconciliation{
						Processes: []executionstore.ProcessReconciliationDirective{
							{ProcessID: testID(20), Disposition: "start"},
							{ProcessID: testID(21), Disposition: "retain"},
						},
					},
				})
			},
			want: map[string]any{
				"daemon_runtime.id":                           daemonRuntimeID.String(),
				"daemon_runtime.reconciliation.process_count": float64(2),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			ctx := log.WithLogger(context.Background(), testLogger(&buf))
			event := log.NewEvent(ctx, "test.event")
			ctx = log.WithEvent(ctx, event)
			tc.attach(ctx)
			event.Done(context.Background())

			record := oneRecord(t, &buf)
			for k, want := range tc.want {
				got, ok := record[k]
				if !ok {
					t.Errorf("missing key %q in %+v", k, record)
					continue
				}
				if got != want {
					t.Errorf("record[%q] = %v, want %v", k, got, want)
				}
			}
			for _, k := range tc.banned {
				if _, ok := record[k]; ok {
					t.Errorf("sensitive key %q leaked into record: %+v", k, record)
				}
			}
		})
	}
}

func TestLogEntitiesNoopOnEmptyContext(t *testing.T) {
	ctx := context.Background()
	Org(ctx, identitystore.OrgRecord{})
	Project(ctx, identitystore.ProjectRecord{})
	Agent(ctx, executionstore.AgentRecord{})
	RuntimeLock(ctx, executionstore.AgentRuntimeLockRecord{})
	Turn(ctx, executionstore.AgentTurnRecord{})
	AgentInput(ctx, executionstore.AgentInputRecord{})
	Machine(ctx, executionstore.MachineRecord{})
	MachineBootstrap(ctx, executionstore.MachineBootstrapRecord{})
	MachineFailureReport(ctx, executionstore.MachineFailureReportInput{})
	DaemonRuntime(ctx, executionstore.DaemonRuntimeRecord{})
	DaemonRuntimeRegistration(ctx, executionstore.DaemonRuntimeRegistrationRecord{})
}

func TestWorkerLoopCreatesEventAndAttachesWorker(t *testing.T) {
	var buf bytes.Buffer
	ctx := log.WithLogger(context.Background(), testLogger(&buf))
	workerID := testID(12)
	ctx, event := WorkerLoop(ctx, workerID)
	WorkerLoopResult(ctx, true)
	event.Done(context.Background())

	record := oneRecord(t, &buf)
	for key, want := range map[string]any{
		"message":            "worker.loop",
		"event.name":         "worker.loop",
		"worker.process_id":  workerID.String(),
		"worker.loop.worked": true,
	} {
		if got := record[key]; got != want {
			t.Fatalf("%s = %v, want %v in %+v", key, got, want, record)
		}
	}
}

func TestMaintenanceLoopCreatesWideEvent(t *testing.T) {
	var buf bytes.Buffer
	ctx := log.WithLogger(context.Background(), testLogger(&buf))
	pollAt := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)

	ctx, event := MaintenanceLoop(ctx, 5*time.Second, pollAt)
	MaintenanceLoopResult(ctx, 2, nil, 5, nil)
	event.Done(context.Background())

	record := oneRecord(t, &buf)
	for key, want := range map[string]any{
		"message":                                 "maintenance.loop",
		"event.name":                              "maintenance.loop",
		"maintenance.loop.interval":               float64(5_000_000_000),
		"maintenance.reap_runtime_locks.count":    float64(2),
		"maintenance.rebuild_agent_wakeups.count": float64(5),
		"maintenance.loop.worked":                 true,
	} {
		if got := record[key]; got != want {
			t.Fatalf("%s = %v, want %v in %+v", key, got, want, record)
		}
	}
	if record["maintenance.loop.poll_at"] == "" {
		t.Fatalf("maintenance.loop.poll_at missing in %+v", record)
	}
}

func TestMaintenanceLoopAttachesTaskErrors(t *testing.T) {
	var buf bytes.Buffer
	ctx := log.WithLogger(context.Background(), testLogger(&buf))

	ctx, event := MaintenanceLoop(ctx, time.Second, time.Now().UTC())
	MaintenanceLoopResult(
		ctx,
		1,
		errors.New("reap failed"),
		0,
		errors.New("rebuild failed"),
	)
	event.Done(context.Background())

	record := oneRecord(t, &buf)
	for key, want := range map[string]any{
		"level":                                   "error",
		"error.message":                           "reap failed",
		"maintenance.reap_runtime_locks.count":    float64(1),
		"maintenance.reap_runtime_locks.error":    "reap failed",
		"maintenance.rebuild_agent_wakeups.count": float64(0),
		"maintenance.rebuild_agent_wakeups.error": "rebuild failed",
		"maintenance.loop.worked":                 true,
	} {
		if got := record[key]; got != want {
			t.Fatalf("%s = %v, want %v in %+v", key, got, want, record)
		}
	}
}

func TestAuthenticatedLogsNilPrincipalIDsAsNull(t *testing.T) {
	var buf bytes.Buffer
	ctx := log.WithLogger(context.Background(), testLogger(&buf))
	event := log.NewEvent(ctx, "test.event")
	ctx = log.WithEvent(ctx, event)

	Authenticated(ctx, identitystore.PrincipalRecord{
		Type:             identitystore.PrincipalTypeUser,
		ID:               testID(1),
		BrowserSessionID: testID(2),
	})
	event.Done(context.Background())

	record := oneRecord(t, &buf)
	if record["principal.id"] != testID(1).String() || record["browser_session.id"] != testID(2).String() {
		t.Fatalf("set principal IDs were not logged as UUID strings: %+v", record)
	}
	for _, key := range []string{
		"org.id",
		"project.id",
		"personal_access_token.id",
		"machine_daemon_token.id",
	} {
		if value, ok := record[key]; !ok || value != nil {
			t.Fatalf("%s = %v, want JSON null in %+v", key, value, record)
		}
	}
}

func TestOrgAuthorizationAttachesOutcome(t *testing.T) {
	var buf bytes.Buffer
	ctx := log.WithLogger(context.Background(), testLogger(&buf))
	event := log.NewEvent(ctx, "test.event")
	ctx = log.WithEvent(ctx, event)

	OrgAuthorization(ctx, identitystore.AuthorizeOrgInput{
		Principal: identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: testID(1)},
		OrgID:     testID(2),
		Action:    identitystore.OrgActionManage,
	}, OrgAuthForbidden)
	event.Done(context.Background())

	record := oneRecord(t, &buf)
	for key, want := range map[string]any{
		"level":                "warn",
		"principal.type":       "user",
		"principal.id":         testID(1).String(),
		"org.id":               testID(2).String(),
		"authorization.action": identitystore.OrgActionManage,
		"authorization.result": string(OrgAuthForbidden),
	} {
		if got := record[key]; got != want {
			t.Fatalf("%s = %v, want %v in %+v", key, got, want, record)
		}
	}
}

func TestProjectAuthorizationAttachesOutcome(t *testing.T) {
	var buf bytes.Buffer
	ctx := log.WithLogger(context.Background(), testLogger(&buf))
	event := log.NewEvent(ctx, "test.event")
	ctx = log.WithEvent(ctx, event)

	ProjectAuthorization(ctx, identitystore.AuthorizeProjectInput{
		Principal: identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: testID(1)},
		OrgID:     testID(2),
		ProjectID: testID(3),
		Action:    identitystore.ProjectActionManage,
	}, ProjectAuthNotVisible)
	event.Done(context.Background())

	record := oneRecord(t, &buf)
	for key, want := range map[string]any{
		"level":                "warn",
		"principal.type":       identitystore.PrincipalTypeUser,
		"principal.id":         testID(1).String(),
		"org.id":               testID(2).String(),
		"project.id":           testID(3).String(),
		"authorization.action": identitystore.ProjectActionManage,
		"authorization.result": string(ProjectAuthNotVisible),
	} {
		if got := record[key]; got != want {
			t.Fatalf("%s = %v, want %v in %+v", key, got, want, record)
		}
	}
}

func TestMachineAuthorizationAttachesOutcome(t *testing.T) {
	var buf bytes.Buffer
	ctx := log.WithLogger(context.Background(), testLogger(&buf))
	event := log.NewEvent(ctx, "test.event")
	ctx = log.WithEvent(ctx, event)

	MachineAuthorization(ctx, executionstore.AuthorizeMachineInput{
		Principal: identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: testID(1)},
		OrgID:     testID(2),
		MachineID: testID(3),
		Action:    executionstore.MachineActionManage,
	}, MachineAuthForbidden)
	event.Done(context.Background())

	record := oneRecord(t, &buf)
	for key, want := range map[string]any{
		"level":                "warn",
		"principal.type":       identitystore.PrincipalTypeUser,
		"principal.id":         testID(1).String(),
		"org.id":               testID(2).String(),
		"machine.id":           testID(3).String(),
		"authorization.action": executionstore.MachineActionManage,
		"authorization.result": string(MachineAuthForbidden),
	} {
		if got := record[key]; got != want {
			t.Fatalf("%s = %v, want %v in %+v", key, got, want, record)
		}
	}
}

func TestMCPLogEntitiesAttachIndexedFields(t *testing.T) {
	var buf bytes.Buffer
	ctx := log.WithLogger(context.Background(), testLogger(&buf))
	event := log.NewEvent(ctx, "test.event")
	ctx = log.WithEvent(ctx, event)

	connections := []executionstore.MCPConnectionRecord{
		{
			ID:           testID(30),
			ServerKey:    "docs",
			EndpointURL:  "https://example.com/mcp",
			State:        executionstore.MCPConnectionStateInitializing,
			Generation:   1,
			MCPSessionID: "secret-session-id",
		},
	}
	MCPConnections(ctx, connections)
	MCPInitialization(ctx, 0, executionstore.MCPConnectionRecord{
		ID:              connections[0].ID,
		ServerKey:       "docs",
		EndpointURL:     "https://example.com/mcp",
		State:           executionstore.MCPConnectionStateReady,
		ProtocolVersion: "2025-11-25",
		ToolsSnapshot:   json.RawMessage(`[{"name":"search"},{"name":"fetch"}]`),
		Generation:      1,
		MCPSessionID:    "secret-session-id",
	}, "succeeded", nil)
	event.Done(context.Background())

	record := oneRecord(t, &buf)
	for key, want := range map[string]any{
		"mcp.count":                        float64(1),
		"mcp.0.id":                         testID(30).String(),
		"mcp.0.server_key":                 "docs",
		"mcp.0.endpoint_url":               "https://example.com/mcp",
		"mcp.0.state":                      string(executionstore.MCPConnectionStateReady),
		"mcp.0.protocol_version":           "2025-11-25",
		"mcp.0.generation":                 float64(1),
		"mcp.0.initialization.result":      "succeeded",
		"mcp.0.initialization.tools_count": float64(2),
	} {
		if got := record[key]; got != want {
			t.Fatalf("%s = %v, want %v in %+v", key, got, want, record)
		}
	}
	for _, banned := range []string{"mcp.0.mcp_session_id", "mcp_session_id", "secret-session-id"} {
		if _, ok := record[banned]; ok {
			t.Fatalf("unexpected secret/auth-token field %q in %+v", banned, record)
		}
		if strings.Contains(buf.String(), banned) {
			t.Fatalf("unexpected secret/auth-token value %q in log %s", banned, buf.String())
		}
	}
}

func TestMCPInitializationFailureWarns(t *testing.T) {
	var buf bytes.Buffer
	ctx := log.WithLogger(context.Background(), testLogger(&buf))
	event := log.NewEvent(ctx, "test.event")
	ctx = log.WithEvent(ctx, event)

	MCPInitialization(
		ctx,
		1,
		executionstore.MCPConnectionRecord{ID: testID(40), ServerKey: "docs", State: executionstore.MCPConnectionStateFailed},
		"failed",
		errors.New("initialize failed"),
	)
	event.Done(context.Background())

	record := oneRecord(t, &buf)
	for key, want := range map[string]any{
		"level":                            "warn",
		"mcp.1.id":                         testID(40).String(),
		"mcp.1.server_key":                 "docs",
		"mcp.1.state":                      string(executionstore.MCPConnectionStateFailed),
		"mcp.1.initialization.result":      "failed",
		"mcp.1.initialization.error":       "initialize failed",
		"mcp.1.initialization.tools_count": float64(0),
	} {
		if got := record[key]; got != want {
			t.Fatalf("%s = %v, want %v in %+v", key, got, want, record)
		}
	}
}

func testLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			switch attr.Key {
			case slog.TimeKey:
				return slog.Attr{}
			case slog.LevelKey:
				if level, ok := attr.Value.Any().(slog.Level); ok {
					attr.Value = slog.StringValue(strings.ToLower(level.String()))
				}
			case slog.MessageKey:
				attr.Key = "message"
			}
			return attr
		},
	}))
}

func oneRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	records := logRecords(t, buf)
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1: %s", len(records), buf.String())
	}
	return records[0]
}

func logRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("unmarshal log record %q: %v", scanner.Text(), err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return records
}

func testID(seed byte) storage.ID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte{seed})
}
