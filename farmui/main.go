package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	var (
		addr     = flag.String("addr", ":8080", "HTTP listen address")
		sdk      = flag.String("sdk", envOr("ANDROID_SDK_ROOT", "/opt/homebrew/share/android-commandlinetools"), "Android SDK root")
		avdHome  = flag.String("avd-home", os.Getenv("ANDROID_AVD_HOME"), "AVD home directory (default ~/.android/avd)")
		gpu      = flag.String("gpu", os.Getenv("AVDCTL_GPU"), "Emulator GPU backend (host=Metal on macOS, auto, swiftshader_indirect)")
		windowed = flag.Bool("windowed", false, "Launch emulators with a visible window")
	)
	flag.Parse()

	sdkRoot := expandTilde(*sdk)
	home := *avdHome
	if home == "" {
		home = filepath.Join(homeDir(), ".android", "avd")
	} else {
		home = expandTilde(home)
	}

	setupJava()
	setupPath(sdkRoot)

	cfg := Config{
		SDKRoot:       sdkRoot,
		AVDHome:       home,
		EmulatorBin:   resolveBin(filepath.Join(sdkRoot, "emulator", "emulator"), "emulator"),
		ADBBin:        resolveBin(filepath.Join(sdkRoot, "platform-tools", "adb"), "adb"),
		AvdManagerBin: resolveBin(filepath.Join(sdkRoot, "cmdline-tools", "latest", "bin", "avdmanager"), "avdmanager"),
		SdkManagerBin: resolveBin(filepath.Join(sdkRoot, "cmdline-tools", "latest", "bin", "sdkmanager"), "sdkmanager"),
		QemuImgBin:    resolveBin("", "qemu-img"),
		MacroDir:      filepath.Join(homeDir(), ".androidfarm", "macros"),
		GoldenDir:     filepath.Join(homeDir(), ".androidfarm", "goldens"),
		APKDir:        filepath.Join(homeDir(), ".androidfarm", "apks"),
		GPU:           *gpu,
		Windowed:      *windowed,
	}

	for name, bin := range map[string]string{
		"emulator": cfg.EmulatorBin, "adb": cfg.ADBBin,
		"avdmanager": cfg.AvdManagerBin, "sdkmanager": cfg.SdkManagerBin, "qemu-img": cfg.QemuImgBin,
	} {
		if bin == "" {
			log.Printf("warning: %s not found in SDK root %q or PATH", name, sdkRoot)
		}
	}

	svc := NewService(cfg)
	stopSampler := make(chan struct{})
	go svc.sampleLoop(stopSampler)

	app := NewServer(svc)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           app.withLogging(app),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	go func() {
		log.Printf("Android Farm UI  ->  http://localhost%s", normalizeAddr(*addr))
		log.Printf("SDK=%s  AVD_HOME=%s  GPU=%s  windowed=%v", cfg.SDKRoot, cfg.AVDHome, svc.effectiveGPU(), cfg.Windowed)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	<-ctx.Done()
	close(stopSampler)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
	log.Println("bye")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return os.Getenv("HOME")
}

func expandTilde(p string) string {
	if strings.HasPrefix(p, "~") {
		return filepath.Join(homeDir(), strings.TrimPrefix(p, "~"))
	}
	return p
}

func resolveBin(path, fallback string) string {
	if path != "" {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	if p, err := exec.LookPath(fallback); err == nil {
		return p
	}
	return ""
}

func normalizeAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return addr
	}
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return addr
}

// setupPath prepends the SDK tool directories so child processes can find
// siblings (adb, emulator, avdmanager, sdkmanager).
func setupPath(sdkRoot string) {
	dirs := []string{
		filepath.Join(sdkRoot, "cmdline-tools", "latest", "bin"),
		filepath.Join(sdkRoot, "platform-tools"),
		filepath.Join(sdkRoot, "emulator"),
	}
	current := os.Getenv("PATH")
	_ = os.Setenv("PATH", strings.Join(append(dirs, current), ":"))
}

// setupJava ensures JAVA_HOME points at a JDK 17+ for avdmanager/sdkmanager.
func setupJava() {
	if jh := os.Getenv("JAVA_HOME"); jh != "" {
		if _, err := os.Stat(filepath.Join(jh, "bin", "java")); err == nil {
			return
		}
	}
	candidates := []string{
		"/opt/homebrew/opt/openjdk@21/libexec/openjdk.jdk/Contents/Home",
		"/opt/homebrew/opt/openjdk/libexec/openjdk.jdk/Contents/Home",
		"/usr/local/opt/openjdk/libexec/openjdk.jdk/Contents/Home",
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, "bin", "java")); err == nil {
			_ = os.Setenv("JAVA_HOME", c)
			return
		}
	}
	if out, err := exec.Command("/usr/libexec/java_home", "-v", "17+").Output(); err == nil {
		_ = os.Setenv("JAVA_HOME", strings.TrimSpace(string(out)))
	}
}
