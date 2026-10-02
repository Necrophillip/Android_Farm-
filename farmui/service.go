package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forkbombeu/avdctl/pkg/avdmanager"
)

// Config holds resolved paths and defaults for the farm service.
type Config struct {
	SDKRoot       string
	AVDHome       string
	EmulatorBin   string
	ADBBin        string
	AvdManagerBin string
	SdkManagerBin string
	QemuImgBin    string
	MacroDir      string
	GoldenDir     string
	APKDir        string
	GPU           string
	Windowed      bool
}

// Service wraps the avdctl manager with host metrics and adb controls.
type Service struct {
	cfg      Config
	mu       sync.RWMutex
	mgr      *avdmanager.Manager
	metrics  *metricsStore
	totalMem int64

	macros    *macroStore
	runner    *macroRunner
	apks      *apkStore
	bake      *bakeManager
	sizeCache sync.Map // device name -> [2]int (w,h)

	shotMu    sync.Mutex
	shotCache map[string]cachedShot

	subMu sync.Mutex
	subs  map[chan Snapshot]struct{}
}

// cachedShot is a short-lived framebuffer capture keyed by device name so that
// multiple viewers share a single screencap.
type cachedShot struct {
	png []byte
	at  time.Time
}

// Snapshot is a full UI state pushed to subscribers over SSE.
type Snapshot struct {
	AVDS    []AVD      `json:"avds"`
	System  SystemInfo `json:"system"`
	Macros  []Macro    `json:"macros"`
	APKs    []APK      `json:"apks"`
	Goldens []Golden   `json:"goldens"`
	Jobs    []BakeJob  `json:"jobs"`
	At      int64      `json:"at"`
}

func NewService(cfg Config) *Service {
	s := &Service{
		cfg:       cfg,
		metrics:   newMetricsStore(),
		totalMem:  totalSystemMem(),
		subs:      make(map[chan Snapshot]struct{}),
		shotCache: make(map[string]cachedShot),
	}
	if cfg.MacroDir != "" {
		s.macros = newMacroStore(cfg.MacroDir)
	}
	if cfg.APKDir != "" {
		s.apks = newAPKStore(cfg.APKDir)
	}
	s.bake = newBakeManager(s)
	s.runner = newMacroRunner(s)
	s.mgr = s.buildManager(cfg.GPU, cfg.Windowed)
	return s
}

// subscribe registers a listener for state snapshots.
func (s *Service) subscribe() (chan Snapshot, func()) {
	ch := make(chan Snapshot, 4)
	s.subMu.Lock()
	s.subs[ch] = struct{}{}
	s.subMu.Unlock()
	return ch, func() {
		s.subMu.Lock()
		delete(s.subs, ch)
		s.subMu.Unlock()
	}
}

// broadcast pushes the current state to all subscribers, dropping stale frames.
func (s *Service) broadcast() {
	snap := s.snapshot()
	if snap == nil {
		return
	}
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- *snap:
		default:
		}
	}
}

func (s *Service) snapshot() *Snapshot {
	avds, err := s.ListAVDs()
	if err != nil {
		return nil
	}
	var macros []Macro
	if s.macros != nil {
		macros = s.macros.list()
	}
	var jobs []BakeJob
	if s.bake != nil {
		jobs = s.bake.list()
	}
	var apks []APK
	if s.apks != nil {
		apks = s.apks.list()
	}
	return &Snapshot{AVDS: avds, System: s.SystemInfo(), Macros: macros, APKs: apks, Goldens: s.ListGoldens(), Jobs: jobs, At: nowMillis()}
}

// Macros lists saved macros.
func (s *Service) Macros() []Macro {
	if s.macros == nil {
		return nil
	}
	return s.macros.list()
}

// SaveMacro creates or updates a macro.
func (s *Service) SaveMacro(m Macro) (Macro, error) {
	if s.macros == nil {
		return Macro{}, errors.New("macro store disabled")
	}
	saved, err := s.macros.save(m)
	if err != nil {
		return Macro{}, err
	}
	go s.broadcast()
	return saved, nil
}

// DeleteMacro removes a macro and stops it on any device currently running it.
func (s *Service) DeleteMacro(id string) error {
	if s.macros == nil {
		return errors.New("macro store disabled")
	}
	procs, _ := s.manager().ListRunning()
	for _, p := range procs {
		if s.runner.runningID(p.Name) == id {
			s.runner.stop(p.Name)
		}
	}
	if err := s.macros.delete(id); err != nil {
		return err
	}
	go s.broadcast()
	return nil
}

// StartMacro runs a saved macro on a device, looping when requested.
func (s *Service) StartMacro(device, id string, loop bool) error {
	if s.macros == nil {
		return errors.New("macro store disabled")
	}
	m, ok := s.macros.get(id)
	if !ok {
		return fmt.Errorf("macro %s not found", id)
	}
	if _, err := s.resolveSerial(device); err != nil {
		return err
	}
	return s.runner.start(device, m, loop)
}

// StopMacro stops the macro running on a device.
func (s *Service) StopMacro(device string) {
	s.runner.stop(device)
}

// applyMacroStep translates a normalized step into a device input command.
func (s *Service) applyMacroStep(device string, step MacroStep) error {
	switch step.Type {
	case "tap":
		w, h := s.deviceSize(device)
		return s.Input(device, InputRequest{Type: "tap", X: step.X * float64(w), Y: step.Y * float64(h)})
	case "swipe":
		w, h := s.deviceSize(device)
		return s.Input(device, InputRequest{
			Type:     "swipe",
			X:        step.X * float64(w),
			Y:        step.Y * float64(h),
			X2:       step.X2 * float64(w),
			Y2:       step.Y2 * float64(h),
			Duration: step.Duration,
		})
	case "text":
		return s.Input(device, InputRequest{Type: "text", Text: step.Text})
	case "key":
		return s.Input(device, InputRequest{Type: "key", Key: step.Key})
	case "wait":
		return nil
	default:
		return fmt.Errorf("unknown macro step %q", step.Type)
	}
}

// deviceSize returns the device resolution, cached per device name.
func (s *Service) deviceSize(device string) (int, int) {
	if v, ok := s.sizeCache.Load(device); ok {
		dims := v.([2]int)
		return dims[0], dims[1]
	}
	w, h := 720, 1280
	if serial, err := s.resolveSerial(device); err == nil {
		out, err := exec.Command(s.cfg.ADBBin, "-s", serial, "shell", "wm", "size").Output()
		if err == nil {
			if pw, ph, ok := parseWMSize(string(out)); ok {
				w, h = pw, ph
			}
		}
	}
	s.sizeCache.Store(device, [2]int{w, h})
	return w, h
}

func parseWMSize(out string) (int, int, bool) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Physical size:") {
			continue
		}
		dim := strings.TrimSpace(strings.TrimPrefix(line, "Physical size:"))
		parts := strings.SplitN(dim, "x", 2)
		if len(parts) != 2 {
			continue
		}
		w, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
		h, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err1 == nil && err2 == nil && w > 0 && h > 0 {
			return w, h, true
		}
	}
	return 0, 0, false
}

func (s *Service) buildManager(gpu string, windowed bool) *avdmanager.Manager {
	return avdmanager.NewWithEnv(s.managerEnv(gpu, windowed, false))
}

func (s *Service) buildWritableManager(gpu string, windowed bool) *avdmanager.Manager {
	return avdmanager.NewWithEnv(s.managerEnv(gpu, windowed, true))
}

func (s *Service) managerEnv(gpu string, windowed, writable bool) avdmanager.Environment {
	return avdmanager.Environment{
		SDKRoot:       s.cfg.SDKRoot,
		AVDHome:       s.cfg.AVDHome,
		EmulatorBin:   s.cfg.EmulatorBin,
		ADBBin:        s.cfg.ADBBin,
		AvdManagerBin: s.cfg.AvdManagerBin,
		SdkManagerBin: s.cfg.SdkManagerBin,
		QemuImgBin:    s.cfg.QemuImgBin,
		GPU:           gpu,
		Windowed:      windowed,
		Writable:      writable,
	}
}

func (s *Service) manager() *avdmanager.Manager {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mgr
}

func (s *Service) managerFor(gpu string, windowed bool) *avdmanager.Manager {
	if gpu == s.cfg.GPU && windowed == s.cfg.Windowed {
		return s.manager()
	}
	return s.buildManager(gpu, windowed)
}

func (s *Service) ctx() context.Context { return context.Background() }

// Settings returns the current launch settings.
func (s *Service) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Settings{GPU: s.cfg.GPU, Windowed: s.cfg.Windowed}
}

// UpdateSettings rebuilds the manager with new launch settings.
func (s *Service) UpdateSettings(in Settings) Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.GPU = strings.TrimSpace(in.GPU)
	s.cfg.Windowed = in.Windowed
	s.mgr = s.buildManager(s.cfg.GPU, s.cfg.Windowed)
	return Settings{GPU: s.cfg.GPU, Windowed: s.cfg.Windowed}
}

func (s *Service) effectiveGPU() string {
	s.mu.RLock()
	gpu := s.cfg.GPU
	s.mu.RUnlock()
	if strings.TrimSpace(gpu) != "" {
		return gpu
	}
	if runtime.GOOS == "darwin" {
		return "host"
	}
	return "swiftshader_indirect"
}

// ListAVDs merges known AVDs with running instances and metrics.
func (s *Service) ListAVDs() ([]AVD, error) {
	mgr := s.manager()
	infos, err := mgr.List()
	if err != nil {
		return nil, err
	}
	procs, _ := mgr.ListRunning()
	byName := make(map[string]avdmanager.ProcessInfo, len(procs))
	for _, p := range procs {
		byName[p.Name] = p
	}

	result := make([]AVD, 0, len(infos))
	seen := make(map[string]bool, len(infos))
	for _, info := range infos {
		avd := AVD{
			Name:      info.Name,
			Path:      info.Path,
			Userdata:  info.Userdata,
			SizeBytes: info.SizeBytes,
		}
		avd.Image, avd.ABI = readAVDImage(info.Path)
		if p, ok := byName[info.Name]; ok {
			avd.Running = true
			avd.Serial = p.Serial
			avd.Port = p.Port
			avd.PID = p.PID
			avd.Booted = p.Booted
			avd.MacroID = s.runner.runningID(info.Name)
			if m, ok := s.metrics.get(info.Name); ok {
				if m.UptimeSec > 0 {
					avd.UpSince = nowMillis() - m.UptimeSec*1000
				}
				metricCopy := m
				avd.Metrics = &metricCopy
			}
		}
		result = append(result, avd)
		seen[info.Name] = true
	}

	// Include emulators started outside avdctl.
	for _, p := range procs {
		if seen[p.Name] {
			continue
		}
		avd := AVD{
			Name:    p.Name,
			Running: true,
			Serial:  p.Serial,
			Port:    p.Port,
			PID:     p.PID,
			Booted:  p.Booted,
		}
		result = append(result, avd)
	}
	return result, nil
}

// CreateAVD creates a base AVD. When lite is true, the image defaults to an
// AOSP (no Play Services) build and the AVD config is tuned for low CPU/RAM.
func (s *Service) CreateAVD(name, image, device string, lite bool) (AVD, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return AVD{}, errors.New("name is required")
	}
	if strings.TrimSpace(image) == "" {
		tag := "google_apis_playstore"
		if lite {
			tag = "default"
		}
		image = fmt.Sprintf("system-images;android-35;%s;%s", tag, hostABI())
	}
	if strings.TrimSpace(device) == "" {
		device = "pixel_6"
	}
	info, err := s.manager().InitBase(avdmanager.InitBaseOptions{
		Name:        name,
		SystemImage: image,
		Device:      device,
	})
	if err != nil {
		return AVD{}, err
	}
	if lite {
		if err := patchAVDConfig(info.Path, liteConfig); err != nil {
			return AVD{}, fmt.Errorf("apply lite config: %w", err)
		}
	}
	avd := AVD{Name: info.Name, Path: info.Path, Userdata: info.Userdata, SizeBytes: info.SizeBytes}
	avd.Image, avd.ABI = readAVDImage(info.Path)
	return avd, nil
}

// liteConfig is the tuned config.ini for resource-constrained fleets.
var liteConfig = map[string]string{
	"hw.cpu.ncore":           "2",
	"hw.ramSize":             "2048",
	"hw.lcd.width":           "720",
	"hw.lcd.height":          "1280",
	"hw.lcd.density":         "320",
	"hw.gpu.enabled":         "yes",
	"hw.gpu.mode":            "host",
	"hw.camera.back":         "none",
	"hw.camera.front":        "none",
	"hw.audioInput":          "no",
	"hw.audioOutput":         "no",
	"hw.gps":                 "no",
	"hw.sensors.orientation": "no",
}

// OptimizeAVD applies the lite config to an existing AVD and, when it is
// running, disables the OS animations to cut idle CPU.
func (s *Service) OptimizeAVD(name string, lite bool) error {
	avds, err := s.ListAVDs()
	if err != nil {
		return err
	}
	var target *AVD
	for i := range avds {
		if avds[i].Name == name {
			target = &avds[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("AVD %s not found", name)
	}
	if target.Path != "" {
		if err := patchAVDConfig(target.Path, liteConfig); err != nil {
			return err
		}
	}
	if target.Running && target.Booted {
		for _, cmd := range []string{"settings put global window_animation_scale 0",
			"settings put global transition_animation_scale 0",
			"settings put global animator_duration_scale 0"} {
			_, _ = s.Shell(name, cmd)
		}
	}
	return nil
}

// patchAVDConfig upserts key=value pairs into an AVD's config.ini.
func patchAVDConfig(avdPath string, values map[string]string) error {
	cfgPath := filepath.Join(avdPath, "config.ini")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(raw), "\n")
	for key, val := range values {
		prefix := key + "="
		found := false
		for i, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), prefix) {
				lines[i] = prefix + val
				found = true
				break
			}
		}
		if !found {
			lines = append(lines, prefix+val)
		}
	}
	return os.WriteFile(cfgPath, []byte(strings.Join(lines, "\n")), 0o644)
}

// DeleteAVD stops (if needed) and removes an AVD.
func (s *Service) DeleteAVD(name string) error {
	if serial, err := s.resolveSerial(name); err == nil {
		_ = s.manager().Stop(serial)
	}
	return s.manager().Delete(name)
}

// StartAVD boots an AVD and returns its serial.
func (s *Service) StartAVD(name string, port int, windowed *bool) (string, error) {
	mgr := s.manager()
	if windowed != nil {
		mgr = s.managerFor(s.cfg.GPU, *windowed)
	}
	opts := avdmanager.RunOptions{Name: name, Port: port}
	if port > 0 {
		serial, _, err := mgr.RunOnPort(opts)
		return serial, err
	}
	return mgr.Run(opts)
}

// StopAVD stops a running AVD by name.
func (s *Service) StopAVD(name string) error {
	serial, err := s.resolveSerial(name)
	if err != nil {
		return err
	}
	err = s.manager().Stop(serial)
	if err == nil {
		s.pruneShot(name)
	}
	return err
}

func (s *Service) resolveSerial(name string) (string, error) {
	procs, err := s.manager().ListRunning()
	if err != nil {
		return "", err
	}
	for _, p := range procs {
		if p.Name == name {
			return p.Serial, nil
		}
	}
	return "", fmt.Errorf("no running emulator named %s", name)
}

// Screenshot captures the device framebuffer as PNG bytes. Results are cached
// for a short TTL so concurrent viewers share one capture; pass force=true to
// bypass the cache for an on-demand fresh frame.
func (s *Service) Screenshot(name string, force bool) ([]byte, error) {
	if !force {
		s.shotMu.Lock()
		if c, ok := s.shotCache[name]; ok && time.Since(c.at) < screenshotTTL {
			png := c.png
			s.shotMu.Unlock()
			return png, nil
		}
		s.shotMu.Unlock()
	}

	serial, err := s.resolveSerial(name)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(s.ctx(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.cfg.ADBBin, "-s", serial, "exec-out", "screencap", "-p")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("screencap: %w", err)
	}
	if len(out) == 0 {
		return nil, errors.New("empty screencap")
	}
	s.shotMu.Lock()
	s.shotCache[name] = cachedShot{png: out, at: time.Now()}
	s.shotMu.Unlock()
	return out, nil
}

const screenshotTTL = time.Second

// pruneShots drops cached frames for devices that are no longer running.
func (s *Service) pruneShots(alive map[string]bool) {
	s.shotMu.Lock()
	for name := range s.shotCache {
		if !alive[name] {
			delete(s.shotCache, name)
		}
	}
	s.shotMu.Unlock()
}

func (s *Service) pruneShot(name string) {
	s.shotMu.Lock()
	delete(s.shotCache, name)
	s.shotMu.Unlock()
}

// Input forwards a remote control action to the device.
func (s *Service) Input(name string, req InputRequest) error {
	serial, err := s.resolveSerial(name)
	if err != nil {
		return err
	}
	var args []string
	switch req.Type {
	case "tap":
		args = []string{"input", "tap", itoa(int(req.X)), itoa(int(req.Y))}
	case "swipe":
		d := req.Duration
		if d <= 0 {
			d = 300
		}
		args = []string{"input", "swipe", itoa(int(req.X)), itoa(int(req.Y)), itoa(int(req.X2)), itoa(int(req.Y2)), itoa(d)}
	case "text":
		args = []string{"input", "text", strings.ReplaceAll(req.Text, " ", "%s")}
	case "key":
		args = []string{"input", "keyevent", itoa(req.Key)}
	default:
		return fmt.Errorf("unknown input type %q", req.Type)
	}
	return s.adbShell(serial, args...)
}

// Shell runs a command inside the device and returns its output.
func (s *Service) Shell(name, command string) (string, error) {
	serial, err := s.resolveSerial(name)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(command) == "" {
		return "", errors.New("empty command")
	}
	ctx, cancel := context.WithTimeout(s.ctx(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.cfg.ADBBin, "-s", serial, "shell", command)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (s *Service) adbShell(serial string, args ...string) error {
	ctx, cancel := context.WithTimeout(s.ctx(), 20*time.Second)
	defer cancel()
	full := append([]string{"-s", serial, "shell"}, args...)
	cmd := exec.CommandContext(ctx, s.cfg.ADBBin, full...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// InstallAPK installs an uploaded APK on a running device (adb install -r -t).
func (s *Service) InstallAPK(device, apkID string) (string, error) {
	apk, path, ok := s.apks.get(apkID)
	if !ok {
		return "", fmt.Errorf("apk %s not found", apkID)
	}
	serial, err := s.resolveSerial(device)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(s.ctx(), 5*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, s.cfg.ADBBin, "-s", serial, "install", "-r", "-t", path).CombinedOutput()
	res := strings.TrimSpace(string(out))
	if err != nil {
		return res, fmt.Errorf("instalar %s: %s", apk.Name, lastNonEmptyLine(res))
	}
	return res, nil
}

// PackageInfo describes a third-party app installed on a device.
type PackageInfo struct {
	Package string `json:"package"`
	Version string `json:"version,omitempty"`
}

// ListPackages returns the third-party packages installed on a device.
func (s *Service) ListPackages(device string) ([]PackageInfo, error) {
	serial, err := s.resolveSerial(device)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(s.ctx(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, s.cfg.ADBBin, "-s", serial, "shell", "pm", "list", "packages", "-3").Output()
	if err != nil {
		return nil, fmt.Errorf("list packages: %w", err)
	}
	var pkgs []PackageInfo
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "package:") {
			continue
		}
		name := strings.TrimPrefix(line, "package:")
		if name == "" {
			continue
		}
		pkgs = append(pkgs, PackageInfo{Package: name})
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Package < pkgs[j].Package })
	return pkgs, nil
}

// UninstallPackage removes a package from a device (adb uninstall).
func (s *Service) UninstallPackage(device, pkg string) error {
	pkg = strings.TrimSpace(pkg)
	if pkg == "" {
		return errors.New("package is required")
	}
	serial, err := s.resolveSerial(device)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(s.ctx(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, s.cfg.ADBBin, "-s", serial, "uninstall", pkg).CombinedOutput()
	if err != nil {
		return fmt.Errorf("desinstalar %s: %s", pkg, lastNonEmptyLine(string(out)))
	}
	return nil
}

// LaunchPackage starts a package's launcher activity.
func (s *Service) LaunchPackage(device, pkg string) error {
	pkg = strings.TrimSpace(pkg)
	if pkg == "" {
		return errors.New("package is required")
	}
	serial, err := s.resolveSerial(device)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(s.ctx(), 30*time.Second)
	defer cancel()

	// Resolve the launcher activity, then start it explicitly.
	resolve, _ := exec.CommandContext(ctx, s.cfg.ADBBin, "-s", serial, "shell",
		"cmd", "package", "resolve-activity", "--brief",
		"-c", "android.intent.category.LAUNCHER", pkg).Output()
	component := ""
	for _, line := range strings.Split(string(resolve), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "/") && !strings.Contains(line, " ") {
			component = line
		}
	}
	if component == "" {
		component = pkg
	}
	out, err := exec.CommandContext(ctx, s.cfg.ADBBin, "-s", serial, "shell",
		"am", "start", "-n", component).CombinedOutput()
	if err != nil {
		return fmt.Errorf("lanzar %s: %s", pkg, lastNonEmptyLine(string(out)))
	}
	return nil
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return strings.TrimSpace(lines[i])
		}
	}
	return ""
}

// SystemInfo reports host and toolchain details.
func (s *Service) SystemInfo() SystemInfo {
	s.mu.RLock()
	windowed := s.cfg.Windowed
	s.mu.RUnlock()
	return SystemInfo{
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		SDKRoot:    s.cfg.SDKRoot,
		HostABI:    hostABI(),
		GPU:        s.effectiveGPU(),
		Windowed:   windowed,
		TotalMem:   s.totalMem,
		CPUCount:   runtime.NumCPU(),
		Emulator:   s.emulatorVersion(),
		Images:     s.availableImages(),
		ServerTime: nowMillis(),
	}
}

func (s *Service) emulatorVersion() string {
	out, err := exec.Command(s.cfg.EmulatorBin, "-version").CombinedOutput()
	if err != nil {
		return ""
	}
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	return strings.TrimSpace(line)
}

func (s *Service) availableImages() []string {
	root := filepath.Join(s.cfg.SDKRoot, "system-images")
	byAPI, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var images []string
	for _, api := range byAPI {
		if !api.IsDir() {
			continue
		}
		tags, err := os.ReadDir(filepath.Join(root, api.Name()))
		if err != nil {
			continue
		}
		for _, tag := range tags {
			if !tag.IsDir() {
				continue
			}
			abis, err := os.ReadDir(filepath.Join(root, api.Name(), tag.Name()))
			if err != nil {
				continue
			}
			for _, abi := range abis {
				if !abi.IsDir() {
					continue
				}
				images = append(images, fmt.Sprintf("system-images;%s;%s;%s", api.Name(), tag.Name(), abi.Name()))
			}
		}
	}
	return images
}

func readAVDImage(avdPath string) (image, abi string) {
	b, err := os.ReadFile(filepath.Join(avdPath, "config.ini"))
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "image.sysdir.1="):
			val := strings.Trim(strings.TrimPrefix(line, "image.sysdir.1="), "/")
			parts := strings.Split(val, "/")
			if len(parts) >= 4 {
				image = fmt.Sprintf("%s;%s;%s;%s", parts[0], parts[1], parts[2], parts[3])
			}
		case strings.HasPrefix(line, "abi.type="):
			abi = strings.TrimSpace(strings.TrimPrefix(line, "abi.type="))
		}
	}
	return image, abi
}

func hostABI() string {
	switch runtime.GOARCH {
	case "arm64":
		return "arm64-v8a"
	case "amd64":
		return "x86_64"
	default:
		return runtime.GOARCH
	}
}

func itoa(v int) string { return strconv.Itoa(v) }
