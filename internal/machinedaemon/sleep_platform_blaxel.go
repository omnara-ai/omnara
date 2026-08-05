package machinedaemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
)

const (
	blaxelLocalAPITimeout   = 3 * time.Second
	blaxelMaxResponseBytes  = 64 * 1024
	blaxelProcessStatusLive = "running"
)

type blaxelSleepPlatform struct {
	apiURL              string
	httpClient          *http.Client
	supervisorPID       int
	supervisorStartTime string
	awakeProcessName    string
	awakeProcessCommand string
	processStartTime    func(int) (string, error)
}

type blaxelProcessRequest struct {
	Name              string `json:"name"`
	Command           string `json:"command"`
	KeepAlive         bool   `json:"keepAlive"`
	Timeout           int    `json:"timeout"`
	WaitForCompletion bool   `json:"waitForCompletion"`
}

type blaxelProcessResponse struct {
	Status    string `json:"status"`
	KeepAlive bool   `json:"keepAlive"`
}

func newBlaxelSleepPlatform() (sleepPlatform, error) {
	supervisorPID := os.Getppid()
	supervisorStartTime, err := blaxelProcessStartTime(supervisorPID)
	if err != nil {
		return nil, err
	}
	awakeProcessCommand, err := blaxelAwakeProcessCommand(supervisorPID, supervisorStartTime)
	if err != nil {
		return nil, err
	}
	return &blaxelSleepPlatform{
		apiURL:              daemonprotocol.BlaxelLocalAPIURL,
		supervisorPID:       supervisorPID,
		supervisorStartTime: supervisorStartTime,
		awakeProcessName:    daemonprotocol.BlaxelAwakeProcessName(supervisorPID),
		awakeProcessCommand: awakeProcessCommand,
		processStartTime:    blaxelProcessStartTime,
		httpClient: &http.Client{
			Timeout: blaxelLocalAPITimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (p *blaxelSleepPlatform) preventSleep() error {
	startTime, err := p.processStartTime(p.supervisorPID)
	if err != nil {
		return fmt.Errorf("read blaxel awake process supervisor start time: %w", err)
	}
	if startTime != p.supervisorStartTime {
		return errors.New("blaxel awake process supervisor is no longer running")
	}
	process, found, err := p.getAwakeProcess()
	if err != nil {
		return err
	}
	if found && validBlaxelAwakeProcess(process) {
		return nil
	}
	if found && normalizedBlaxelProcessStatus(process.Status) == blaxelProcessStatusLive {
		if err := p.killAwakeProcess(); err != nil {
			return err
		}
	}
	return p.startAwakeProcess()
}

func (p *blaxelSleepPlatform) allowSleep() error {
	return p.killAwakeProcess()
}

func (p *blaxelSleepPlatform) startAwakeProcess() error {
	var process blaxelProcessResponse
	_, err := p.request(http.MethodPost, "/process", blaxelProcessRequest{
		Name:              p.awakeProcessName,
		Command:           p.awakeProcessCommand,
		KeepAlive:         true,
		Timeout:           0,
		WaitForCompletion: false,
	}, &process)
	if err == nil {
		if !validBlaxelAwakeProcess(process) {
			return fmt.Errorf(
				"blaxel awake process %q is not running with keep-alive enabled",
				p.awakeProcessName,
			)
		}
		return nil
	}
	existing, found, getErr := p.getAwakeProcess()
	if getErr == nil && found && validBlaxelAwakeProcess(existing) {
		return nil
	}
	if getErr != nil {
		return errors.Join(err, getErr)
	}
	return err
}

func (p *blaxelSleepPlatform) getAwakeProcess() (blaxelProcessResponse, bool, error) {
	var process blaxelProcessResponse
	status, err := p.request(
		http.MethodGet,
		"/process/"+url.PathEscape(p.awakeProcessName),
		nil,
		&process,
	)
	if status == http.StatusNotFound {
		return blaxelProcessResponse{}, false, nil
	}
	if err != nil {
		return blaxelProcessResponse{}, false, err
	}
	return process, true, nil
}

func (p *blaxelSleepPlatform) killAwakeProcess() error {
	status, err := p.request(
		http.MethodDelete,
		"/process/"+url.PathEscape(p.awakeProcessName)+"/kill",
		nil,
		nil,
	)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}

func (p *blaxelSleepPlatform) request(method, path string, body, out any) (int, error) {
	var requestBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("marshal blaxel sleep request: %w", err)
		}
		requestBody = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(
		context.Background(),
		method,
		strings.TrimRight(p.apiURL, "/")+path,
		requestBody,
	)
	if err != nil {
		return 0, fmt.Errorf("build blaxel sleep request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := p.httpClient.Do(request)
	if err != nil {
		return 0, fmt.Errorf("blaxel sleep request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, blaxelMaxResponseBytes))
	if err != nil {
		return response.StatusCode, fmt.Errorf("read blaxel sleep response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, fmt.Errorf("blaxel sleep API returned HTTP %d", response.StatusCode)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return response.StatusCode, fmt.Errorf("decode blaxel sleep response: %w", err)
		}
	}
	return response.StatusCode, nil
}

func validBlaxelAwakeProcess(process blaxelProcessResponse) bool {
	return normalizedBlaxelProcessStatus(process.Status) == blaxelProcessStatusLive && process.KeepAlive
}

func normalizedBlaxelProcessStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

func blaxelAwakeProcessCommand(supervisorPID int, supervisorStartTime string) (string, error) {
	if supervisorPID <= 0 {
		return "", errors.New("blaxel awake process supervisor pid must be positive")
	}
	if !blaxelDigitsOnly(supervisorStartTime) {
		return "", errors.New("blaxel awake process supervisor start time must be numeric")
	}
	return fmt.Sprintf(
		"supervisor_pid=%d;supervisor_start=%s;while [ -r /proc/$supervisor_pid/stat ] && "+
			"[ x$(sed 's/^.*) //' /proc/$supervisor_pid/stat 2>/dev/null | cut -d ' ' -f 20) "+
			"= x$supervisor_start ]; do sleep 1; done",
		supervisorPID,
		supervisorStartTime,
	), nil
}

func blaxelProcessStartTime(pid int) (string, error) {
	if pid <= 0 {
		return "", errors.New("process pid must be positive")
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", fmt.Errorf("read process %d start time: %w", pid, err)
	}
	return parseBlaxelProcessStartTime(string(raw))
}

func parseBlaxelProcessStartTime(stat string) (string, error) {
	end := strings.LastIndex(stat, ") ")
	if end < 0 {
		return "", errors.New("process stat is malformed")
	}
	fields := strings.Fields(stat[end+2:])
	if len(fields) < 20 || !blaxelDigitsOnly(fields[19]) {
		return "", errors.New("process stat is missing start time")
	}
	return fields[19], nil
}

func blaxelDigitsOnly(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}
