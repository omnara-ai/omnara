package machinedaemon

import (
	"fmt"
	"os"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
)

type sleepPlatform interface {
	allowSleep() error
	preventSleep() error
}

func newSleepPlatform(name string) (sleepPlatform, error) {
	switch name {
	case "":
		return noopSleepPlatform{}, nil
	case daemonprotocol.SleepPlatformUnikraft:
		return unikraftSleepPlatform{
			controlPath: daemonprotocol.UnikraftScaleToZeroControlFilePath,
		}, nil
	case daemonprotocol.SleepPlatformBlaxel:
		return newBlaxelSleepPlatform()
	default:
		return nil, fmt.Errorf("unsupported daemon sleep platform %q", name)
	}
}

type noopSleepPlatform struct{}

func (noopSleepPlatform) allowSleep() error   { return nil }
func (noopSleepPlatform) preventSleep() error { return nil }

type unikraftSleepPlatform struct {
	controlPath string
}

func (p unikraftSleepPlatform) allowSleep() error   { return p.write("=0") }
func (p unikraftSleepPlatform) preventSleep() error { return p.write("=1") }

func (p unikraftSleepPlatform) write(value string) error {
	if err := os.WriteFile(p.controlPath, []byte(value), 0); err != nil {
		return fmt.Errorf("write unikraft scale-to-zero control %s: %w", p.controlPath, err)
	}
	return nil
}
