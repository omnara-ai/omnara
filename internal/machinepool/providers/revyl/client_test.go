package revyl

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type recordedRequest struct {
	Method        string
	Path          string
	Authorization string
	Prefer        string
	Body          json.RawMessage
}

type recordedOperation struct {
	RequestID uuid.UUID `json:"request_id"`
	Command   struct {
		Kind    string   `json:"kind"`
		ArgvB64 []string `json:"argv_b64"`
		PathB64 string   `json:"path_b64"`
	} `json:"command"`
}

func decodeOperation(t *testing.T, raw json.RawMessage) recordedOperation {
	t.Helper()
	var operation recordedOperation
	if err := json.Unmarshal(raw, &operation); err != nil {
		t.Errorf("decode operation request: %v", err)
	}
	return operation
}

func operationIDFor(t *testing.T, computerID uuid.UUID, raw json.RawMessage) uuid.UUID {
	t.Helper()
	return uuid.NewSHA1(computerID, []byte(decodeOperation(t, raw).RequestID.String()))
}

func testClient(t *testing.T, handler func(recordedRequest) (int, string)) (*restClient, *[]recordedRequest) {
	t.Helper()
	previous := operationPollInterval
	operationPollInterval = time.Millisecond
	t.Cleanup(func() { operationPollInterval = previous })
	var mu sync.Mutex
	var requests []recordedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		request := recordedRequest{
			Method:        r.Method,
			Path:          r.URL.RequestURI(),
			Authorization: r.Header.Get("Authorization"),
			Prefer:        r.Header.Get("Prefer"),
			Body:          raw,
		}
		mu.Lock()
		requests = append(requests, request)
		mu.Unlock()
		status, body := handler(request)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return newRESTClient(server.URL+"/", "revyl-key", server.Client()), &requests
}

func TestCreateComputerSendsDeterministicRequest(t *testing.T) {
	id := uuid.New()
	client, requests := testClient(t, func(recordedRequest) (int, string) {
		return http.StatusOK, fmt.Sprintf(
			`{"computer_session_id":%q,"os":"macos","state":"starting","release":"pending"}`,
			id,
		)
	})

	got, err := client.CreateComputer(
		context.Background(),
		createComputerRequest{ID: id, OS: computerOSMacOS, TimeoutSeconds: 7200},
	)

	if err != nil || got.ID != id || got.State != computerStateStarting {
		t.Fatalf("CreateComputer = %+v, %v", got, err)
	}
	request := (*requests)[0]
	if request.Method != http.MethodPost || request.Path != "/api/v1/computer-runs" ||
		request.Authorization != "Bearer revyl-key" {
		t.Fatalf("request = %+v", request)
	}
	var body createComputerRequest
	if err := json.Unmarshal(request.Body, &body); err != nil {
		t.Fatalf("decode create body: %v", err)
	}
	if want := (createComputerRequest{ID: id, OS: computerOSMacOS, TimeoutSeconds: 7200}); body != want {
		t.Fatalf("create body = %+v, want %+v", body, want)
	}
}

func TestClientReadsErrorDetailAndNotFound(t *testing.T) {
	client, _ := testClient(t, func(request recordedRequest) (int, string) {
		if strings.HasSuffix(request.Path, "/stop") {
			return http.StatusNotFound, `{"detail":"inaccessible"}`
		}
		return http.StatusForbidden, `{"detail":"computer_not_enabled","offer":null}`
	})

	_, err := client.CreateComputer(context.Background(), createComputerRequest{ID: uuid.New(), OS: computerOSMacOS})
	apiErr, ok := apiErrorOf(err)
	if !ok || apiErr.StatusCode != http.StatusForbidden || apiErr.Code != "computer_not_enabled" {
		t.Fatalf("CreateComputer error = %v, want 403 computer_not_enabled", err)
	}
	if _, found, err := client.StopComputer(context.Background(), uuid.New()); found || err != nil {
		t.Fatalf("StopComputer on missing computer = found %v error %v, want absent", found, err)
	}
}

func TestExecWaitsForAsyncOperationAndDecodesOutput(t *testing.T) {
	id := uuid.New()
	var operationPath string
	polls := 0
	client, requests := testClient(t, func(request recordedRequest) (int, string) {
		if request.Method == http.MethodPost {
			operationPath = fmt.Sprintf(
				"/api/v1/computer-runs/%s/operations/%s",
				id,
				operationIDFor(t, id, request.Body),
			)
			return http.StatusAccepted, "null"
		}
		if request.Path != operationPath {
			return http.StatusNotFound, `{"detail":"operation_not_found"}`
		}
		polls++
		evidence := "pending"
		if polls > 1 {
			evidence = "recorded"
		}
		return http.StatusOK, fmt.Sprintf(
			`{"computer_session_id":%q,"operation_id":%q,"evidence":%q,"execution":{"outcome":"failed",`+
				`"error_code":"exec_nonzero","result":{"kind":"exec","exit_code":3,"stdout":%q,"stderr":%q}}}`,
			id,
			strings.TrimPrefix(operationPath, fmt.Sprintf("/api/v1/computer-runs/%s/operations/", id)),
			evidence,
			base64.URLEncoding.EncodeToString([]byte("out")),
			base64.URLEncoding.EncodeToString([]byte("err")),
		)
	})

	result, err := client.Exec(
		context.Background(),
		id,
		execRequest{Argv: []string{"/bin/sh", "-c", "exit 3"}, TimeoutMS: 1000},
	)

	if err != nil || result.ExitCode != 3 || string(result.Stdout) != "out" || string(result.Stderr) != "err" {
		t.Fatalf("Exec = %+v, %v", result, err)
	}
	submit := (*requests)[0]
	command := decodeOperation(t, submit.Body).Command
	decoded, _ := base64.URLEncoding.DecodeString(command.ArgvB64[2])
	if submit.Prefer != "respond-async" || command.Kind != "exec" || string(decoded) != "exit 3" {
		t.Fatalf("submit = %+v", submit)
	}
	if polls != 2 {
		t.Fatalf("operation polls = %d, want 2", polls)
	}
}

func TestExecReportsUnfinishedCommand(t *testing.T) {
	id := uuid.New()
	client, _ := testClient(t, func(request recordedRequest) (int, string) {
		return http.StatusOK, fmt.Sprintf(
			`{"computer_session_id":%q,"operation_id":%q,"evidence":"recorded",`+
				`"execution":{"outcome":"timed_out","error_code":"timeout","termination":"confirmed"}}`,
			id,
			operationIDFor(t, id, request.Body),
		)
	})

	_, err := client.Exec(context.Background(), id, execRequest{Argv: []string{"true"}, TimeoutMS: 1})

	if err == nil || !strings.Contains(err.Error(), "timed_out") {
		t.Fatalf("Exec error = %v, want timed_out", err)
	}
}

func TestWriteFileChecksByteCount(t *testing.T) {
	id := uuid.New()
	written := 5
	client, requests := testClient(t, func(request recordedRequest) (int, string) {
		return http.StatusOK, fmt.Sprintf(
			`{"computer_session_id":%q,"operation_id":%q,"evidence":"recorded",`+
				`"execution":{"outcome":"succeeded","result":{"kind":"file_write","byte_count":%d}}}`,
			id,
			operationIDFor(t, id, request.Body),
			written,
		)
	})

	if err := client.WriteFile(context.Background(), id, ".upload-000", []byte("hello")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	command := decodeOperation(t, (*requests)[0].Body).Command
	path, _ := base64.URLEncoding.DecodeString(command.PathB64)
	if command.Kind != "file_write" || string(path) != ".upload-000" {
		t.Fatalf("file write command = %+v", command)
	}
	written = 4
	if err := client.WriteFile(context.Background(), id, ".upload-000", []byte("hello")); err == nil {
		t.Fatal("WriteFile accepted a short write")
	}
}
