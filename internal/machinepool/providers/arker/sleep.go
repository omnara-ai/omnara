package arker

import (
	_ "embed"
	"strconv"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
)

const daemonPIDPath = "/tmp/omnara-daemon.pid"

//go:embed keep_awake.sh
var keepAwakeScript string

var (
	sleepBootCommand = keepAwakeCommand("boot")
	wakeCommand      = keepAwakeCommand("wake")
)

func keepAwakeCommand(mode string) string {
	return "omnara_mode=" + mode + "\n" +
		"omnara_awake_file=" + daemonprotocol.ArkerAwakeControlFilePath + "\n" +
		"omnara_daemon_pid_file=" + daemonPIDPath + "\n" +
		"omnara_wake_port=" + strconv.Itoa(daemonprotocol.WakeListenerPort) + "\n" +
		"omnara_daemon_launcher='" + daemonLauncherCommand + "'\n" +
		keepAwakeScript
}
