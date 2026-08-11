//go:build integration && servicee2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/publicid"
)

const scopedAgentRuntimeLockCountSQL = `
SELECT count(*)
FROM agent_runtime_locks runtime_lock
JOIN agents agent ON agent.id = runtime_lock.agent_id
WHERE agent.project_id = $1 AND runtime_lock.agent_id = $2
`

type serviceWorkerOptions struct {
	ProviderConfig string
	BaseURL        string
	PublicURL      string
	ExtraEnv       []string
	LogLevel       string
}

type serviceProcess struct {
	cmd           *exec.Cmd
	logs          *safeLogBuffer
	containerName string
	lifecycle     *serviceProcessLifecycle
}

type serviceProcessLifecycle struct {
	stopOnce sync.Once
	done     chan struct{}
}

func newServiceProcess(cmd *exec.Cmd, logs *safeLogBuffer) serviceProcess {
	return serviceProcess{
		cmd:  cmd,
		logs: logs,
		lifecycle: &serviceProcessLifecycle{
			done: make(chan struct{}),
		},
	}
}

func serviceProcessEnv(overrides ...string) []string {
	overrideKeys := make(map[string]bool, len(overrides))
	for _, item := range overrides {
		key, _, ok := strings.Cut(item, "=")
		if ok {
			overrideKeys[key] = true
		}
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if ok && overrideKeys[key] {
			continue
		}
		env = append(env, item)
	}
	env = append(env, overrides...)
	return env
}

type safeLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *safeLogBuffer) Excerpt(maxLines int) string {
	if b == nil {
		return ""
	}
	if os.Getenv("OMNARA_E2E_FULL_LOGS") == "1" {
		return b.String()
	}
	raw := b.String()
	if raw == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	filtered := make([]string, 0, len(lines))
	omittedNoise := 0
	for _, line := range lines {
		if serviceE2ENoisyLogLine(line) {
			omittedNoise++
			continue
		}
		filtered = append(filtered, line)
	}
	omittedTail := 0
	if maxLines > 0 && len(filtered) > maxLines {
		omittedTail = len(filtered) - maxLines
		filtered = filtered[omittedTail:]
	}
	if len(filtered) == 0 {
		if omittedNoise == 0 {
			return ""
		}
		return fmt.Sprintf("<only noisy log lines omitted: %d>\n", omittedNoise)
	}
	var out strings.Builder
	if omittedNoise > 0 {
		fmt.Fprintf(&out, "<omitted noisy log lines: %d>\n", omittedNoise)
	}
	if omittedTail > 0 {
		fmt.Fprintf(&out, "<omitted older non-noisy log lines: %d>\n", omittedTail)
	}
	for _, line := range filtered {
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}

func serviceE2ENoisyLogLine(line string) bool {
	fields, ok := serviceE2ELogFields(line)
	if !ok {
		return false
	}
	message := fields["message"]
	if message != "http.request" {
		return message == "worker.loop" && fields["worker.loop.worked"] == false
	}
	if strings.Contains(line, "/healthz") {
		return true
	}
	if strings.Contains(line, "/interactions") && fields["http.status_code"] == float64(200) {
		return true
	}
	return false
}

func serviceE2ELogFields(line string) (map[string]any, bool) {
	var fields map[string]any
	if err := json.Unmarshal([]byte(line), &fields); err != nil {
		return nil, false
	}
	return fields, true
}

func (e *serviceE2EEnvironment) startAPI(t *testing.T, ctx context.Context) serviceProcess {
	return e.startAPIWithWebServing(t, ctx, "disabled")
}

func (e *serviceE2EEnvironment) runMigrations(t *testing.T, ctx context.Context) {
	t.Helper()
	migratePath := filepath.Join(e.root, "migrate")
	build := exec.CommandContext(ctx, goBin(e.repoRoot), "build", "-o", migratePath, "./cmd/migrate")
	build.Dir = e.repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build migrate: %v\n%s", err, output)
	}
	cmd := exec.CommandContext(ctx, migratePath)
	cmd.Dir = e.repoRoot
	cmd.Env = serviceProcessEnv(
		"OMNARA_DATABASE_URL="+e.databaseURL,
		"OMNARA_MIGRATIONS_DIR="+filepath.Join(e.repoRoot, "migrations"),
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run migrations: %v\n%s", err, output)
	}
}

func (e *serviceE2EEnvironment) startEmbeddedWebAPI(
	t *testing.T,
	ctx context.Context,
) serviceProcess {
	return e.startAPIWithWebServing(t, ctx, "embedded")
}

func (e *serviceE2EEnvironment) startAPIWithWebServing(
	t *testing.T,
	ctx context.Context,
	webServing string,
) serviceProcess {
	t.Helper()
	apiPath := filepath.Join(e.root, "api")
	build := exec.CommandContext(ctx, goBin(e.repoRoot), "build", "-o", apiPath, "./cmd/api")
	build.Dir = e.repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build api: %v\n%s", err, output)
	}
	cmd := exec.Command(apiPath)
	cmd.Dir = e.repoRoot
	cmd.Env = serviceProcessEnv(
		"OMNARA_ALLOW_INSECURE_DEV_DEFAULTS=1",
		"OMNARA_API_ADDR="+e.apiListenAddr,
		"OMNARA_API_METRICS_ADDR="+e.apiMetricsListenAddr,
		"OMNARA_DATABASE_URL="+e.databaseURL,
		"OMNARA_REDIS_URL="+e.redisURL,
		"OMNARA_PUBLIC_URL="+e.publicURL,
		"OMNARA_WEB_SERVING="+webServing,
		"OMNARA_SECRET_ENCRYPTION_KEYS={\"e2e-local\":\"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=\"}",
		"OMNARA_SECRET_ENCRYPTION_ACTIVE_KEY_ID=e2e-local",
		"OMNARA_LOG_LEVEL="+serviceE2EEnvDefault("OMNARA_E2E_API_LOG_LEVEL", "error"),
	)
	stderr := &safeLogBuffer{}
	cmd.Stdout = stderr
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start api: %v", err)
	}
	proc := newServiceProcess(cmd, stderr)
	registerSubprocessCleanup(t, "api", proc)
	waitForHTTP(t, ctx, e.apiMetricsURL+"/healthz", stderr)
	return proc
}

func (e *serviceE2EEnvironment) startWorker(
	t *testing.T,
	ctx context.Context,
	projectID string,
	opts serviceWorkerOptions,
) serviceProcess {
	t.Helper()
	workerPort := freePort(t)
	e.workerURL = "http://127.0.0.1:" + workerPort
	e.workerListenAddr = "0.0.0.0:" + workerPort
	if opts.LogLevel == "" {
		opts.LogLevel = "error"
	}
	workerPublicURL := e.publicURL
	if opts.PublicURL != "" {
		workerPublicURL = opts.PublicURL
	}
	if opts.ProviderConfig != "" && opts.BaseURL != "" {
		e.updateServiceE2EProviderBaseURL(t, ctx, projectID, opts.ProviderConfig, opts.BaseURL)
	}
	workerPath := filepath.Join(e.root, "worker")
	build := exec.CommandContext(ctx, goBin(e.repoRoot), "build", "-o", workerPath, "./cmd/worker")
	build.Dir = e.repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build worker: %v\n%s", err, output)
	}
	cmd := exec.Command(workerPath)
	cmd.Dir = e.repoRoot
	workerEnv := []string{
		"OMNARA_ALLOW_INSECURE_DEV_DEFAULTS=1",
		"OMNARA_WORKER_METRICS_ADDR=" + e.workerListenAddr,
		"OMNARA_DATABASE_URL=" + e.databaseURL,
		"OMNARA_REDIS_URL=" + e.redisURL,
		"OMNARA_PUBLIC_URL=" + workerPublicURL,
		"OMNARA_SECRET_ENCRYPTION_KEYS={\"e2e-local\":\"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=\"}",
		"OMNARA_SECRET_ENCRYPTION_ACTIVE_KEY_ID=e2e-local",
		"OMNARA_LOG_LEVEL=" + opts.LogLevel,
	}
	workerEnv = append(workerEnv, opts.ExtraEnv...)
	cmd.Env = serviceProcessEnv(workerEnv...)
	stderr := &safeLogBuffer{}
	cmd.Stdout = stderr
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start worker binary: %v", err)
	}
	proc := newServiceProcess(cmd, stderr)
	registerSubprocessCleanup(t, "worker", proc)
	waitForHTTP(t, ctx, e.workerURL+"/healthz", stderr)
	return proc
}

func (e *serviceE2EEnvironment) updateServiceE2EProviderBaseURL(
	t *testing.T,
	ctx context.Context,
	projectID, providerConfig, baseURL string,
) {
	t.Helper()
	projectUUID, err := publicid.Decode(publicid.KindProject, projectID)
	if err != nil {
		t.Fatalf("decode project id for provider config update: %v", err)
	}
	tag, err := e.db.Exec(ctx, `
UPDATE model_provider_configs
SET base_url = $3, updated_at = now()
WHERE org_id = (SELECT org_id FROM projects WHERE id = $1)
  AND name = $2
  AND deleted_at IS NULL
`, projectUUID, providerConfig, baseURL)
	if err != nil {
		t.Fatalf("update service e2e provider base url: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("updated %d provider configs named %q, want 1", tag.RowsAffected(), providerConfig)
	}
}

func (e *serviceE2EEnvironment) startMaintenance(t *testing.T, ctx context.Context) serviceProcess {
	t.Helper()
	maintenancePath := filepath.Join(e.root, "maintenance")
	build := exec.CommandContext(ctx, goBin(e.repoRoot), "build", "-o", maintenancePath, "./cmd/maintenance")
	build.Dir = e.repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build maintenance: %v\n%s", err, output)
	}
	cmd := exec.Command(maintenancePath)
	cmd.Dir = e.repoRoot
	cmd.Env = serviceProcessEnv(
		"OMNARA_ALLOW_INSECURE_DEV_DEFAULTS=1",
		"OMNARA_MAINTENANCE_METRICS_ADDR="+e.maintenanceListenAddr,
		"OMNARA_DATABASE_URL="+e.databaseURL,
		"OMNARA_REDIS_URL="+e.redisURL,
		"OMNARA_MAINTENANCE_INTERVAL=10s",
		"OMNARA_LOG_LEVEL=error",
	)
	stderr := &safeLogBuffer{}
	cmd.Stdout = stderr
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start maintenance binary: %v", err)
	}
	proc := newServiceProcess(cmd, stderr)
	registerSubprocessCleanup(t, "maintenance", proc)
	waitForHTTP(t, ctx, e.maintenanceURL+"/healthz", stderr)
	return proc
}

func (e *serviceE2EEnvironment) startDaemonContainer(
	t *testing.T,
	ctx context.Context,
	token, workdir string,
) serviceProcess {
	t.Helper()
	return e.startDaemonContainerWithStateVolume(
		t,
		ctx,
		token,
		workdir,
		"",
	)
}

func (e *serviceE2EEnvironment) startDaemonContainerWithStateVolume(
	t *testing.T,
	ctx context.Context,
	token, workdir, stateVolume string,
) serviceProcess {
	t.Helper()
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Fatalf("unsupported Docker daemon E2E GOARCH %q", runtime.GOARCH)
	}
	platform := dockerLinuxPlatformForGOARCH(t, runtime.GOARCH)
	daemonPath := filepath.Join(e.root, "daemon-linux-"+runtime.GOARCH)
	build := exec.CommandContext(ctx, goBin(e.repoRoot), "build", "-o", daemonPath, "./cmd/daemon")
	build.Dir = e.repoRoot
	build.Env = serviceProcessEnv("CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build linux daemon: %v\n%s", err, output)
	}
	if err := os.Chmod(daemonPath, 0o755); err != nil {
		t.Fatalf("chmod linux daemon: %v", err)
	}
	containerName := "omnara-service-e2e-daemon-" + sanitizeDockerName(e.seed) + "-" + fmt.Sprint(time.Now().UnixNano())
	daemonCommand := fmt.Sprintf(
		"if [ -x /root/.omnarad/bin/omnarad ]; then "+
			"exec /root/.omnarad/bin/omnarad start --no-service; fi; "+
			"/usr/local/bin/omnarad install --release-manifest-url "+
			"https://releases.omnara.test/omnarad/latest/linux-%s.txt --no-start && "+
			"exec /root/.omnarad/bin/omnarad start --no-service",
		runtime.GOARCH,
	)
	args := []string{
		"run",
		"--rm",
		"--name", containerName,
		"--platform", platform,
		"--add-host=host.docker.internal:host-gateway",
		"-v", daemonPath + ":/usr/local/bin/omnarad:ro",
		"-v", workdir + ":/work",
	}
	if stateVolume != "" {
		args = append(args, "-v", stateVolume+":/root/.omnarad")
	}
	args = append(args,
		"-w", "/work",
		"-e", "OMNARA_API_URL="+e.containerAPIURL,
		"-e", "OMNARA_MACHINE_TOKEN="+token,
		"-e", "OMNARA_DAEMON_RETRY_INTERVAL_MS=25",
		"-e", "OMNARA_LOG_LEVEL=error",
		"alpine:3.20",
		"sh", "-c", daemonCommand,
	)
	cmd := exec.Command("docker", args...)
	stderr := &safeLogBuffer{}
	cmd.Stdout = stderr
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon container: %v", err)
	}
	proc := newServiceProcess(cmd, stderr)
	proc.containerName = containerName
	registerSubprocessCleanup(t, "daemon", proc, func() {
		_ = exec.Command("docker", "rm", "-f", containerName).Run()
	})
	return proc
}

func (e *serviceE2EEnvironment) createDockerVolume(
	t *testing.T,
	label string,
) string {
	t.Helper()
	name := "omnara-service-e2e-" +
		sanitizeDockerName(e.seed+"-"+label) +
		"-" +
		fmt.Sprint(time.Now().UnixNano())
	output, err := exec.Command("docker", "volume", "create", name).CombinedOutput()
	if err != nil {
		t.Fatalf("create Docker volume %s: %v\n%s", name, err, output)
	}
	t.Cleanup(func() {
		if cleanupOutput, cleanupErr := exec.Command(
			"docker",
			"volume",
			"rm",
			"-f",
			name,
		).CombinedOutput(); cleanupErr != nil {
			t.Errorf(
				"remove Docker volume %s: %v\n%s",
				name,
				cleanupErr,
				cleanupOutput,
			)
		}
	})
	return name
}

func (e *serviceE2EEnvironment) recreateDockerVolume(t *testing.T, name string) {
	t.Helper()
	prefix := "omnara-service-e2e-" + sanitizeDockerName(e.seed)
	if !strings.HasPrefix(name, prefix) {
		t.Fatalf("refusing to recreate Docker volume %q outside %q", name, prefix)
	}
	if output, err := exec.Command("docker", "volume", "rm", name).CombinedOutput(); err != nil {
		t.Fatalf("remove Docker volume %s: %v\n%s", name, err, output)
	}
	if output, err := exec.Command("docker", "volume", "create", name).CombinedOutput(); err != nil {
		t.Fatalf("recreate Docker volume %s: %v\n%s", name, err, output)
	}
}

func (p serviceProcess) crashContainer(
	t *testing.T,
	ctx context.Context,
) {
	t.Helper()
	if p.containerName == "" {
		t.Fatal("service process is not a Docker container")
	}
	output, err := exec.CommandContext(
		ctx,
		"docker",
		"kill",
		"--signal",
		"KILL",
		p.containerName,
	).CombinedOutput()
	if err != nil {
		t.Fatalf(
			"crash daemon container %s: %v\n%s",
			p.containerName,
			err,
			output,
		)
	}
	p.stop()
}

func (p serviceProcess) crashManagedDaemon(
	t *testing.T,
	ctx context.Context,
) {
	t.Helper()
	// Splitting the token prevents docker exec from matching its own command line.
	const script = `
supervisor_pid=$(tr -d '[:space:]' < /root/.omnarad/daemon.lock)
case "$supervisor_pid" in
	''|*[!0-9]*)
		echo "daemon.lock did not contain a valid supervisor PID" >&2
		exit 1
		;;
esac
managed_token='run-service ''--supervised'
target=
for cmdline in /proc/[0-9]*/cmdline; do
	[ -r "$cmdline" ] || continue
	args=$(tr '\000' ' ' < "$cmdline" 2>/dev/null) || continue
	case "$args" in
		*"$managed_token"*)
			child=${cmdline#/proc/}
			child=${child%/cmdline}
			parent=$(awk '$1 == "PPid:" { print $2 }' "/proc/$child/status" 2>/dev/null) || continue
			[ "$parent" = "$supervisor_pid" ] || continue
			if [ -n "$target" ]; then
				echo "foreground supervisor has multiple managed daemon children" >&2
				exit 1
			fi
			target=$child
			;;
	esac
done
[ -n "$target" ] || {
	echo "foreground supervisor has no managed daemon child" >&2
	echo "supervisor pid: $supervisor_pid" >&2
	for status in /proc/[0-9]*/status; do
		pid=${status#/proc/}
		pid=${pid%/status}
		cmdline="/proc/$pid/cmdline"
		[ -r "$cmdline" ] || continue
		parent=$(awk '$1 == "PPid:" { print $2 }' "$status" 2>/dev/null) || continue
		args=$(tr '\000' ' ' < "$cmdline" 2>/dev/null) || continue
		printf 'pid=%s ppid=%s args=%s\n' "$pid" "$parent" "$args" >&2
	done
	exit 1
}
kill -KILL "$target"
printf '%s\n' "$target"
`
	pid := p.runContainerScript(t, ctx, script)
	t.Logf("killed managed daemon process %s in container %s", pid, p.containerName)
}

func (p serviceProcess) crashProcessSupervisor(
	t *testing.T,
	ctx context.Context,
	processID string,
) {
	t.Helper()
	if processID == "" {
		t.Fatal("process ID is required")
	}
	// Splitting the token prevents docker exec from matching its own command line.
	const script = `
runner_token='__omnara_''process_runner'
target=
for cmdline in /proc/[0-9]*/cmdline; do
	[ -r "$cmdline" ] || continue
	args=$(tr '\000' ' ' < "$cmdline" 2>/dev/null) || continue
	case "$args" in
		*"$runner_token"*"$1-"*)
			pid=${cmdline#/proc/}
			pid=${pid%/cmdline}
			if [ -n "$target" ]; then
				echo "multiple process supervisors matched $1" >&2
				exit 1
			fi
			target=$pid
			;;
	esac
done
[ -n "$target" ] || {
	echo "no process supervisor matched $1" >&2
	exit 1
}
kill -KILL "$target"
printf '%s\n' "$target"
`
	pid := p.runContainerScript(t, ctx, script, processID)
	t.Logf(
		"killed process supervisor %s for %s in container %s",
		pid,
		processID,
		p.containerName,
	)
}

func (p serviceProcess) runContainerScript(
	t *testing.T,
	ctx context.Context,
	script string,
	args ...string,
) string {
	t.Helper()
	if p.containerName == "" {
		t.Fatal("service process is not a Docker container")
	}
	commandArgs := []string{
		"exec",
		p.containerName,
		"sh",
		"-ceu",
		script,
		"omnara-service-e2e",
	}
	commandArgs = append(commandArgs, args...)
	output, err := exec.CommandContext(
		ctx,
		"docker",
		commandArgs...,
	).CombinedOutput()
	if err != nil {
		t.Fatalf(
			"run script in daemon container %s: %v\n%s",
			p.containerName,
			err,
			output,
		)
	}
	return strings.TrimSpace(string(output))
}

func dockerLinuxPlatformForGOARCH(t *testing.T, arch string) string {
	t.Helper()
	switch arch {
	case "amd64":
		return "linux/amd64"
	case "arm64":
		return "linux/arm64"
	default:
		t.Fatalf("unsupported Docker daemon E2E GOARCH %q", arch)
		return ""
	}
}

func sanitizeDockerName(value string) string {
	replacer := strings.NewReplacer("_", "-", ".", "-", "/", "-", ":", "-")
	clean := strings.ToLower(replacer.Replace(value))
	var b strings.Builder
	for _, r := range clean {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "test"
	}
	if len(out) > 48 {
		return out[:48]
	}
	return out
}

func serviceE2EEnvDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func (p serviceProcess) logExcerpt() string {
	if p.logs == nil {
		return ""
	}
	return p.logs.Excerpt(80)
}

func (p serviceProcess) fullLogString() string {
	if p.logs == nil {
		return ""
	}
	return p.logs.String()
}

// registerSubprocessCleanup stops the process at test end and dumps captured
// output on failure. preStop hooks (e.g. `docker rm`) run before stop, in
// registration order.
func registerSubprocessCleanup(t *testing.T, label string, proc serviceProcess, preStop ...func()) {
	t.Helper()
	t.Cleanup(func() {
		for _, fn := range preStop {
			fn()
		}
		proc.stop()
		if !t.Failed() {
			return
		}
		logs := proc.logExcerpt()
		if logs == "" {
			logs = "<no captured output>\n"
		}
		t.Logf("\n=== %s subprocess log excerpt ===\n%s=== end %s log excerpt ===", label, logs, label)
	})
}

func (p serviceProcess) stop() {
	if p.lifecycle == nil {
		return
	}
	p.lifecycle.stopOnce.Do(func() {
		if p.cmd == nil || p.cmd.Process == nil {
			close(p.lifecycle.done)
			return
		}
		_ = p.cmd.Process.Signal(os.Interrupt)
		go func() {
			_ = p.cmd.Wait()
			close(p.lifecycle.done)
		}()
	})
	select {
	case <-p.lifecycle.done:
	case <-time.After(3 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.lifecycle.done
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	return freePorts(t, 1)[0]
}

var (
	serviceE2EPortMu         sync.Mutex
	serviceE2EAllocatedPorts = map[string]struct{}{}
	serviceE2ENextPort       = serviceE2EPortStart
)

const (
	serviceE2EPortStart = 20000
	serviceE2EPortEnd   = 29999
)

func freePorts(t *testing.T, count int) []string {
	t.Helper()
	serviceE2EPortMu.Lock()
	defer serviceE2EPortMu.Unlock()

	listeners := make([]net.Listener, 0, count)
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()

	ports := make([]string, 0, count)
	for attempts := 0; len(ports) < count && attempts <= serviceE2EPortEnd-serviceE2EPortStart; attempts++ {
		if serviceE2ENextPort > serviceE2EPortEnd {
			break
		}
		port := fmt.Sprint(serviceE2ENextPort)
		serviceE2ENextPort++
		if _, exists := serviceE2EAllocatedPorts[port]; exists {
			continue
		}
		listener, err := net.Listen("tcp", "0.0.0.0:"+port)
		if err != nil {
			continue
		}
		serviceE2EAllocatedPorts[port] = struct{}{}
		listeners = append(listeners, listener)
		ports = append(ports, port)
	}
	if len(ports) != count {
		t.Fatalf(
			"allocate %d distinct service e2e ports in %d-%d: found %d",
			count,
			serviceE2EPortStart,
			serviceE2EPortEnd,
			len(ports),
		)
	}
	return ports
}

func waitForHTTP(t *testing.T, ctx context.Context, url string, logs *safeLogBuffer) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond) //nolint:omnaralint // The subprocess offers no readiness handshake.
	}
	t.Fatalf("timed out waiting for %s logs=%s", url, logs.Excerpt(80))
}

func goBin(repoRoot string) string {
	candidate := filepath.Join(repoRoot, ".tools", "go", "bin", "go")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return "go"
}
