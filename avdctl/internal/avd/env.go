// Copyright (C) 2025 Forkbomb B.V.
// License: AGPL-3.0-only

package avd

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

type Env struct {
	SDKRoot    string // ANDROID_SDK_ROOT
	AVDHome    string // ANDROID_AVD_HOME (default ~/.android/avd)
	GoldenDir  string // AVDCTL_GOLDEN_DIR (default ~/avd-golden)
	ClonesDir  string // AVDCTL_CLONES_DIR (default ~/avd-clones)
	ConfigTpl  string // AVDCTL_CONFIG_TEMPLATE (optional)
	Emulator   string // emulator
	ADB        string // adb
	AvdMgr     string // avdmanager
	SdkManager string // sdkmanager
	QemuImg    string // qemu-img
	SSHTarget  string // AVDCTL_SSH_TARGET (optional, e.g. user@host)
	SSHArgs    []string
	// GPU selects the emulator rendering backend (`-gpu`). Defaults to the
	// native Metal backend ("host") on macOS and to software rendering
	// ("swiftshader_indirect") elsewhere. Override with AVDCTL_GPU.
	GPU string
	// Windowed shows the emulator window instead of running headless.
	// Enabled with AVDCTL_WINDOW=1.
	Windowed bool
	// Writable keeps the emulator headless but skips the -read-only flag so
	// the userdata (e.g. an APK install) persists. Enabled with AVDCTL_WRITABLE=1.
	Writable bool
	// ABI is the host system-image ABI (e.g. "arm64-v8a", "x86_64").
	// Override with AVDCTL_ABI.
	ABI string
	// CorrelationID is used to tie logs to a specific workflow/activity.
	CorrelationID string
	// Context is used to parent OpenTelemetry spans.
	Context context.Context
}

func Detect() Env {
	usr, _ := user.Current()
	home := ""
	if usr != nil {
		home = usr.HomeDir
	} else if h := os.Getenv("HOME"); h != "" {
		home = h
	}

	sdk := getenv("ANDROID_SDK_ROOT", "")
	avd := getenv("ANDROID_AVD_HOME", filepath.Join(home, ".android", "avd"))
	gold := getenv("AVDCTL_GOLDEN_DIR", filepath.Join(home, "avd-golden"))
	clns := getenv("AVDCTL_CLONES_DIR", filepath.Join(home, "avd-clones"))
	tpl := os.Getenv("AVDCTL_CONFIG_TEMPLATE")
	sshTarget := os.Getenv("AVDCTL_SSH_TARGET")
	sshArgs := strings.Fields(os.Getenv("AVDCTL_SSH_ARGS"))
	correlationID := getenv("AVDCTL_CORRELATION_ID", "")

	return Env{
		SDKRoot:       sdk,
		AVDHome:       avd,
		GoldenDir:     gold,
		ClonesDir:     clns,
		ConfigTpl:     tpl,
		Emulator:      "emulator",
		ADB:           "adb",
		AvdMgr:        "avdmanager",
		SdkManager:    "sdkmanager",
		QemuImg:       "qemu-img",
		SSHTarget:     sshTarget,
		SSHArgs:       sshArgs,
		GPU:           os.Getenv("AVDCTL_GPU"),
		Windowed:      isTruthy(os.Getenv("AVDCTL_WINDOW")),
		Writable:      isTruthy(os.Getenv("AVDCTL_WRITABLE")),
		ABI:           os.Getenv("AVDCTL_ABI"),
		CorrelationID: correlationID,
		Context:       context.Background(),
	}
}

// HostABI returns the Android system-image ABI matching the host CPU, so that
// Apple Silicon Macs use native arm64-v8a images instead of x86_64.
func HostABI() string {
	switch runtime.GOARCH {
	case "arm64":
		return "arm64-v8a"
	case "amd64":
		return "x86_64"
	default:
		return runtime.GOARCH
	}
}

// abi returns the configured ABI or falls back to the host ABI.
func (e Env) abi() string {
	if strings.TrimSpace(e.ABI) != "" {
		return e.ABI
	}
	return HostABI()
}

// gpu returns the configured GPU backend or a sensible platform default.
// On macOS the Android emulator maps "host" to Metal for hardware
// acceleration.
func (e Env) gpu() string {
	if strings.TrimSpace(e.GPU) != "" {
		return e.GPU
	}
	if runtime.GOOS == "darwin" {
		return "host"
	}
	return "swiftshader_indirect"
}

func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func getenv(k, def string) string {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	return v
}

func DefaultGoldenDir() string { return Detect().GoldenDir }
