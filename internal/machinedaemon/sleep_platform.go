package machinedaemon

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
)

const createOSSelfPauseTimeout = 3 * time.Second

type sleepPlatform interface {
	allowSleep() error
	preventSleep() error
}

func newSleepPlatform(name string) (sleepPlatform, error) {
	switch name {
	case "":
		return noopSleepPlatform{}, nil
	case daemonprotocol.SleepPlatformUnikraft:
		return controlFileSleepPlatform{
			controlPath: daemonprotocol.UnikraftScaleToZeroControlFilePath,
		}, nil
	case daemonprotocol.SleepPlatformArker:
		return controlFileSleepPlatform{
			controlPath: daemonprotocol.ArkerAwakeControlFilePath,
		}, nil
	case daemonprotocol.SleepPlatformBlaxel:
		return newBlaxelSleepPlatform()
	case daemonprotocol.SleepPlatformCreateOS:
		return createOSSleepPlatform{
			pauseURL:   daemonprotocol.CreateOSSelfPauseURL,
			httpClient: &http.Client{Timeout: createOSSelfPauseTimeout},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported daemon sleep platform %q", name)
	}
}

type noopSleepPlatform struct{}

func (noopSleepPlatform) allowSleep() error   { return nil }
func (noopSleepPlatform) preventSleep() error { return nil }

type controlFileSleepPlatform struct {
	controlPath string
}

func (p controlFileSleepPlatform) allowSleep() error   { return p.write("=0") }
func (p controlFileSleepPlatform) preventSleep() error { return p.write("=1") }

func (p controlFileSleepPlatform) write(value string) error {
	if err := os.WriteFile(p.controlPath, []byte(value), 0); err != nil {
		return fmt.Errorf("write sleep control file %s: %w", p.controlPath, err)
	}
	return nil
}

type createOSSleepPlatform struct {
	pauseURL   string
	httpClient *http.Client
}

func (p createOSSleepPlatform) allowSleep() error {
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, p.pauseURL, nil)
	if err != nil {
		return fmt.Errorf("build createos self-pause request: %w", err)
	}
	response, err := p.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("createos self-pause request failed: %w", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("createos self-pause returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (createOSSleepPlatform) preventSleep() error { return nil }
