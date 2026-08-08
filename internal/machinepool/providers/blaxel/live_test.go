package blaxel

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestBlaxelProviderLiveSmoke(t *testing.T) {
	if os.Getenv("OMNARA_BLAXEL_LIVE") != "1" {
		t.Skip("set OMNARA_BLAXEL_LIVE=1 to run live Blaxel smoke test")
	}
	token := strings.TrimSpace(os.Getenv("OMNARA_BLAXEL_API_TOKEN"))
	workspace := strings.TrimSpace(os.Getenv("OMNARA_BLAXEL_WORKSPACE"))
	image := strings.TrimSpace(os.Getenv("OMNARA_BLAXEL_TEST_IMAGE"))
	region := strings.TrimSpace(os.Getenv("OMNARA_BLAXEL_TEST_REGION"))
	if token == "" || workspace == "" || image == "" || region == "" {
		t.Skip(
			"OMNARA_BLAXEL_API_TOKEN, OMNARA_BLAXEL_WORKSPACE, " +
				"OMNARA_BLAXEL_TEST_IMAGE, and OMNARA_BLAXEL_TEST_REGION are required",
		)
	}
	omnaraPublicURL := strings.TrimSpace(os.Getenv("OMNARA_PUBLIC_URL"))
	if omnaraPublicURL == "" {
		omnaraPublicURL = "https://app.omnara.com"
	}
	machineProvisioning := testMachineProvisioning(t, map[string]any{
		"image":          image,
		"region":         region,
		"sleep_after_ms": 30000,
		"startup_script": `set -eu
test "$STARTUP_VALUE" = "startup-value"
test "$MULTILINE" = 'first
second'
test "$RESOLVED_SECRET" = "live-resolved-secret"
test "${OMNARA_MACHINE_TOKEN+x}" != x
printf ready > /tmp/omnara-live-startup
i=0
while [ ! -f /tmp/omnara-live-release-startup ];do
  i=$((i+1))
  [ "$i" -lt 300 ] || exit 31
  sleep 0.1
done
rm -f /tmp/omnara-live-release-startup
`,
	})
	machineProvider, err := (Definition{}).NewProvider(
		mustRawJSON(t, map[string]any{
			"workspace":       workspace,
			"allowed_images":  []string{image},
			"allowed_regions": []string{region},
		}),
		providers.RuntimeConfig{PublicURL: omnaraPublicURL, ProviderAuthToken: token},
	)
	if err != nil {
		t.Fatalf("new blaxel provider: %v", err)
	}
	blaxelProvider, ok := machineProvider.(*provider)
	if !ok {
		t.Fatalf("live blaxel provider has type %T", machineProvider)
	}
	machineID := uuid.New()
	providerResourceID, err := providers.MachineAllocationName(testInstallationID(), machineID)
	if err != nil {
		t.Fatalf("build live blaxel sandbox name: %v", err)
	}
	deleted := false
	t.Cleanup(func() {
		if deleted {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if err := machineProvider.DeleteMachine(
			cleanupCtx,
			testInstallationID(),
			machineID,
			machineProvisioning,
			providerResourceID,
		); err != nil {
			t.Errorf("delete live blaxel sandbox: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	bootstrap := "live-smoke-token"
	firstResourceID, err := machineProvider.ProvisionMachine(
		ctx,
		testInstallationID(),
		machineID,
		machineProvisioning,
		bootstrap,
		map[string]string{
			"STARTUP_VALUE":   "startup-value",
			"MULTILINE":       "first\nsecond",
			"RESOLVED_SECRET": "live-resolved-secret",
		},
	)
	if err != nil {
		t.Fatalf("provision live blaxel sandbox: %v", err)
	}
	if firstResourceID.ProviderResourceID != providerResourceID {
		t.Fatalf("provider resource id = %q, want %q", firstResourceID.ProviderResourceID, providerResourceID)
	}
	target, found, err := blaxelProvider.apiClient().GetSandbox(ctx, providerResourceID)
	if err != nil || !found {
		t.Fatalf("get live blaxel sandbox found=%t err=%v", found, err)
	}
	if err := waitForLiveFile(ctx, blaxelProvider.apiClient(), target, "/tmp/omnara-live-startup", "ready"); err != nil {
		t.Fatalf("verify live startup environment boundary: %v", err)
	}
	daemonProcess, found, err := blaxelProvider.apiClient().GetSandboxProcess(
		ctx,
		target,
		daemonProcessName,
	)
	if err != nil || !found {
		t.Fatalf("get live daemon process found=%t err=%v", found, err)
	}
	if daemonProcess.KeepAlive {
		t.Fatal("sleep-enabled live daemon process unexpectedly keeps the sandbox awake directly")
	}
	if processStatus(daemonProcess.Status) != processStatusRunning {
		t.Fatalf("live daemon process status = %q, want running before startup release", daemonProcess.Status)
	}
	for _, privateValue := range []string{bootstrap, "live-resolved-secret", "startup-value"} {
		if strings.Contains(daemonProcess.Command, privateValue) {
			t.Fatalf("live daemon command exposes private value %q", privateValue)
		}
	}
	daemonPID, err := strconv.Atoi(daemonProcess.PID)
	if err != nil {
		t.Fatalf("parse live daemon pid %q: %v", daemonProcess.PID, err)
	}
	awakeProcessName := daemonprotocol.BlaxelAwakeProcessName(daemonPID)
	awakeProcess, found, err := blaxelProvider.apiClient().GetSandboxProcess(
		ctx,
		target,
		awakeProcessName,
	)
	if err != nil || !found || processStatus(awakeProcess.Status) != processStatusRunning ||
		!awakeProcess.KeepAlive {
		t.Fatalf("get live awake process %q found=%t process=%+v err=%v", awakeProcessName, found, awakeProcess, err)
	}
	attemptID, found := liveBootstrapAttemptID(daemonProcess.Command)
	if !found {
		t.Fatalf("live daemon command omitted bootstrap attempt marker")
	}
	startupEnvironmentDirectory := startupEnvironmentDirectoryPrefix + attemptID
	for _, removedPath := range []string{
		startupEnvironmentDirectory + "/" + startupEnvironmentFileName,
		startupEnvironmentDirectory,
	} {
		if err := requireLivePathMissing(
			ctx,
			blaxelProvider.apiClient(),
			target,
			removedPath,
		); err != nil {
			t.Fatalf("verify live startup environment cleanup for %s: %v", removedPath, err)
		}
	}
	if err := verifyLiveProcessCommandLinesDoNotExpose(
		ctx,
		blaxelProvider.apiClient(),
		target,
		daemonPID,
		[]string{bootstrap, "live-resolved-secret", "startup-value"},
	); err != nil {
		t.Fatalf("verify live process command-line secrecy: %v", err)
	}
	secondResourceID, err := machineProvider.ProvisionMachine(
		ctx,
		testInstallationID(),
		machineID,
		machineProvisioning,
		bootstrap,
		map[string]string{
			"STARTUP_VALUE":   "startup-value",
			"MULTILINE":       "first\nsecond",
			"RESOLVED_SECRET": "live-resolved-secret",
		},
	)
	if err != nil {
		t.Fatalf("reprovision live blaxel sandbox: %v", err)
	}
	if secondResourceID.ProviderResourceID != providerResourceID {
		t.Fatalf(
			"reprovisioned resource id = %q, want %q",
			secondResourceID.ProviderResourceID,
			providerResourceID,
		)
	}
	adoptedDaemonProcess, found, err := blaxelProvider.apiClient().GetSandboxProcess(
		ctx,
		target,
		daemonProcessName,
	)
	if err != nil || !found {
		t.Fatalf("get reprovisioned live daemon process found=%t err=%v", found, err)
	}
	if adoptedDaemonProcess.PID != daemonProcess.PID ||
		adoptedDaemonProcess.Command != daemonProcess.Command ||
		processStatus(adoptedDaemonProcess.Status) != processStatusRunning {
		t.Fatalf(
			"reprovisioned daemon = pid %q status %q, want adopted pid %q running",
			adoptedDaemonProcess.PID,
			adoptedDaemonProcess.Status,
			daemonProcess.PID,
		)
	}
	if err := blaxelProvider.apiClient().UploadSandboxFile(
		ctx,
		target,
		"/tmp/omnara-live-release-startup",
		"release\n",
	); err != nil {
		t.Fatalf("release live startup barrier: %v", err)
	}
	if err := verifyLiveDaemonInstalled(ctx, blaxelProvider.apiClient(), target); err != nil {
		t.Fatalf("verify live daemon installation: %v", err)
	}
	if err := requireLivePathMissing(
		ctx,
		blaxelProvider.apiClient(),
		target,
		"/tmp/omnara-live-release-startup",
	); err != nil {
		t.Fatalf("verify live startup barrier cleanup: %v", err)
	}
	inspectedResourceID, found, err := machineProvider.InspectMachine(
		ctx,
		testInstallationID(),
		machineID,
		machineProvisioning,
		providerResourceID,
	)
	if err != nil {
		t.Fatalf("inspect live blaxel sandbox: %v", err)
	}
	if !found || inspectedResourceID != providerResourceID {
		t.Fatalf("inspect live blaxel sandbox = (%q, %t), want (%q, true)", inspectedResourceID, found, providerResourceID)
	}
	if err := machineProvider.DeleteMachine(
		ctx,
		testInstallationID(),
		machineID,
		machineProvisioning,
		providerResourceID,
	); err != nil {
		t.Fatalf("delete live blaxel sandbox: %v", err)
	}
	deleted = true
}

func verifyLiveDaemonInstalled(ctx context.Context, api apiClient, target sandbox) error {
	const markerPath = "/tmp/omnara-live-daemon-installed"
	_, err := api.StartSandboxProcess(ctx, target, processRequest{
		Name: "omnara-live-daemon-install-check",
		Command: `i=0
while [ "$i" -lt 100 ];do
  if [ -x "${HOME:?}/.omnarad/bin/omnarad" ] &&
    "${HOME:?}/.omnarad/bin/omnarad" --version 2>/dev/null |
      grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$';then
    printf ready > /tmp/omnara-live-daemon-installed
    exit 0
  fi
  i=$((i+1))
  sleep 0.1
done
printf missing > /tmp/omnara-live-daemon-installed
exit 1`,
		KeepAlive:         false,
		Timeout:           15,
		WaitForCompletion: false,
	})
	if err != nil {
		return err
	}
	return waitForLiveFile(ctx, api, target, markerPath, "ready")
}

func verifyLiveProcessCommandLinesDoNotExpose(
	ctx context.Context,
	api apiClient,
	target sandbox,
	daemonPID int,
	privateValues []string,
) error {
	auditID := strings.ReplaceAll(uuid.NewString(), "-", "")
	patternsPath := "/root/.omnara-live-cmdline-patterns-" + auditID
	markerPath := "/tmp/omnara-live-cmdline-audit-" + auditID
	processName := "omnara-live-cmdline-audit-" + auditID[:8]
	if err := api.UploadSandboxFile(
		ctx,
		target,
		patternsPath,
		strings.Join(privateValues, "\n")+"\n",
	); err != nil {
		return err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = api.DeleteSandboxPath(cleanupCtx, target, patternsPath)
	}()
	command := fmt.Sprintf(`set -eu
omnara_cmdlines=/tmp/omnara-live-cmdlines-%s
: > "$omnara_cmdlines"
[ -r /proc/%d/cmdline ] || {
  printf unreadable > %s
  rm -f "$omnara_cmdlines" %s
  exit 1
}
for omnara_cmdline in /proc/[0-9]*/cmdline;do
  [ -r "$omnara_cmdline" ] || continue
  tr '\000' '\n' < "$omnara_cmdline" >> "$omnara_cmdlines" 2>/dev/null || :
done
if grep -F -f %s "$omnara_cmdlines" >/dev/null 2>&1;then
  printf leak > %s
else
  printf clean > %s
fi
rm -f "$omnara_cmdlines" %s`,
		auditID,
		daemonPID,
		strconv.Quote(markerPath),
		strconv.Quote(patternsPath),
		strconv.Quote(patternsPath),
		strconv.Quote(markerPath),
		strconv.Quote(markerPath),
		strconv.Quote(patternsPath),
	)
	if _, err := api.StartSandboxProcess(ctx, target, processRequest{
		Name:              processName,
		Command:           command,
		KeepAlive:         false,
		Timeout:           15,
		WaitForCompletion: false,
	}); err != nil {
		return err
	}
	if err := waitForLiveFile(ctx, api, target, markerPath, "clean"); err != nil {
		return err
	}
	return requireLivePathMissing(ctx, api, target, patternsPath)
}

func liveBootstrapAttemptID(command string) (string, bool) {
	for line := range strings.SplitSeq(command, "\n") {
		attemptID, found := strings.CutPrefix(line, bootstrapAttemptMarkerPrefix)
		if found && len(attemptID) == startupEnvironmentRandomBytes*2 {
			return attemptID, true
		}
	}
	return "", false
}

func requireLivePathMissing(
	ctx context.Context,
	api apiClient,
	target sandbox,
	path string,
) error {
	rest, requestURL, err := liveFilesystemRequest(api, target, path)
	if err != nil {
		return err
	}
	_, err = rest.doDataPlaneRequest(ctx, http.MethodGet, requestURL, nil, nil)
	if isNotFound(err) {
		return nil
	}
	if err == nil {
		return fmt.Errorf("live path still exists")
	}
	return err
}

func waitForLiveFile(
	ctx context.Context,
	api apiClient,
	target sandbox,
	path string,
	want string,
) error {
	rest, requestURL, err := liveFilesystemRequest(api, target, path)
	if err != nil {
		return err
	}
	for {
		var file struct {
			Content string `json:"content"`
		}
		_, err := rest.doDataPlaneRequest(ctx, http.MethodGet, requestURL, nil, &file)
		if err == nil {
			if file.Content != want {
				return fmt.Errorf("live file content = %q, want %q", file.Content, want)
			}
			return nil
		}
		if !isNotFound(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func liveFilesystemRequest(
	api apiClient,
	target sandbox,
	path string,
) (*restClient, string, error) {
	rest, ok := api.(*restClient)
	if !ok {
		return nil, "", fmt.Errorf("live blaxel client has type %T", api)
	}
	sandboxURL, err := sandboxDataPlaneURL(target)
	if err != nil {
		return nil, "", err
	}
	return rest, sandboxURL + "/filesystem/" + url.PathEscape(path), nil
}
