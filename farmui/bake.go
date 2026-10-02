package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/forkbombeu/avdctl/pkg/avdmanager"
)

// ---------------------------------------------------------------------------
// APK library
// ---------------------------------------------------------------------------

type apkStore struct {
	mu    sync.Mutex
	dir   string
	index map[string]APK
}

func newAPKStore(dir string) *apkStore {
	s := &apkStore{dir: dir, index: make(map[string]APK)}
	_ = os.MkdirAll(dir, 0o755)
	if b, err := os.ReadFile(filepath.Join(dir, "index.json")); err == nil {
		_ = json.Unmarshal(b, &s.index)
	}
	return s
}

func (s *apkStore) save(name string, data []byte) (APK, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	apk := APK{
		ID:         randomID(),
		Name:       filepath.Base(name),
		SizeBytes:  int64(len(data)),
		UploadedAt: nowMillis(),
	}
	if err := os.WriteFile(filepath.Join(s.dir, apk.ID+".apk"), data, 0o644); err != nil {
		return APK{}, err
	}
	s.index[apk.ID] = apk
	return apk, s.persist()
}

func (s *apkStore) list() []APK {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]APK, 0, len(s.index))
	for _, a := range s.index {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UploadedAt < out[j].UploadedAt })
	return out
}

func (s *apkStore) get(id string) (APK, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.index[id]
	if !ok {
		return APK{}, "", false
	}
	return a, filepath.Join(s.dir, a.ID+".apk"), true
}

func (s *apkStore) delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.index[id]; !ok {
		return fmt.Errorf("apk %s not found", id)
	}
	delete(s.index, id)
	_ = os.Remove(filepath.Join(s.dir, id+".apk"))
	return s.persist()
}

func (s *apkStore) persist() error {
	b, err := json.MarshalIndent(s.index, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, "index.json"), b, 0o644)
}

// ---------------------------------------------------------------------------
// Golden images
// ---------------------------------------------------------------------------

type goldenMeta struct {
	Name      string `json:"name"`
	BaseName  string `json:"baseName"`
	Image     string `json:"image,omitempty"`
	Device    string `json:"device,omitempty"`
	CreatedAt int64  `json:"createdAt"`
}

func (s *Service) ListGoldens() []Golden {
	entries, err := os.ReadDir(s.cfg.GoldenDir)
	if err != nil {
		return nil
	}
	var out []Golden
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(s.cfg.GoldenDir, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "userdata-qemu.img")); err != nil {
			continue
		}
		g := Golden{Name: e.Name(), Path: dir}
		if b, err := os.ReadFile(filepath.Join(dir, "golden.json")); err == nil {
			var m goldenMeta
			if json.Unmarshal(b, &m) == nil {
				g.BaseName = m.BaseName
				g.Image = m.Image
				g.Device = m.Device
				g.CreatedAt = m.CreatedAt
			}
		}
		if g.CreatedAt == 0 {
			if info, err := e.Info(); err == nil {
				g.CreatedAt = info.ModTime().UnixMilli()
			}
		}
		g.SizeBytes = dirSize(dir)
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

func dirSize(dir string) int64 {
	var total int64
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// ---------------------------------------------------------------------------
// Bake jobs
// ---------------------------------------------------------------------------

type bakeManager struct {
	svc  *Service
	mu   sync.Mutex
	jobs map[string]*BakeJob
}

func newBakeManager(svc *Service) *bakeManager {
	return &bakeManager{svc: svc, jobs: make(map[string]*BakeJob)}
}

func (b *bakeManager) list() []BakeJob {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]BakeJob, 0, len(b.jobs))
	for _, j := range b.jobs {
		out = append(out, *j)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	return out
}

func (b *bakeManager) start(req BakeRequest) *BakeJob {
	job := &BakeJob{
		ID:        randomID(),
		Name:      req.Name,
		State:     "running",
		Message:   "En cola…",
		StartedAt: nowMillis(),
	}
	b.mu.Lock()
	b.jobs[job.ID] = job
	b.mu.Unlock()

	go b.svc.runBake(job, req)
	return job
}

func (b *bakeManager) set(job *BakeJob, state, msg string) {
	b.mu.Lock()
	job.State = state
	job.Message = msg
	if state == "done" || state == "error" {
		job.FinishedAt = nowMillis()
	}
	b.mu.Unlock()
	b.svc.broadcast()
}

// runBake builds a golden image: base AVD -> boot writable -> install APKs ->
// stop -> save-golden.
func (s *Service) runBake(job *BakeJob, req BakeRequest) {
	set := func(state, msg string) { s.bake.set(job, state, msg) }
	mgr := s.buildWritableManager(s.cfg.GPU, s.cfg.Windowed)

	name := strings.TrimSpace(req.Name)
	if name == "" {
		set("error", "El nombre del golden es obligatorio")
		return
	}
	baseName := strings.TrimSpace(req.BaseName)
	if baseName == "" {
		baseName = name + "-base"
	}

	// 1. Ensure the base AVD exists.
	baseExists := false
	if infos, err := mgr.List(); err == nil {
		for _, i := range infos {
			if i.Name == baseName {
				baseExists = true
			}
		}
	}
	if !baseExists {
		set("running", "Creando AVD base "+baseName+"…")
		if _, err := mgr.InitBase(avdmanager.InitBaseOptions{Name: baseName, SystemImage: resolvedImage(req.Image), Device: resolvedDevice(req.Device)}); err != nil {
			set("error", "InitBase: "+err.Error())
			return
		}
	}

	// 2. Stop the base if already running.
	if serial, err := s.runningSerial(mgr, baseName); err == nil && serial != "" {
		_ = mgr.Stop(serial)
		time.Sleep(2 * time.Second)
	}

	// 3. Boot writable.
	set("running", "Arrancando "+baseName+" (writable)…")
	serial, err := mgr.Run(avdmanager.RunOptions{Name: baseName})
	if err != nil {
		set("error", "Run: "+err.Error())
		return
	}

	// 4. Wait for boot.
	set("running", "Esperando a que Android arranque…")
	if err := mgr.WaitForBoot(serial, 3*time.Minute); err != nil {
		_ = mgr.Stop(serial)
		set("error", "Boot: "+err.Error())
		return
	}

	// 5. Install APKs.
	for i, id := range req.APKIDs {
		apk, path, ok := s.apks.get(id)
		if !ok {
			_ = mgr.Stop(serial)
			set("error", "APK no encontrado: "+id)
			return
		}
		set("running", fmt.Sprintf("Instalando APK %d/%d: %s", i+1, len(req.APKIDs), apk.Name))
		ctx, cancel := context.WithTimeout(s.ctx(), 2*time.Minute)
		out, err := exec.CommandContext(ctx, s.cfg.ADBBin, "-s", serial, "install", "-r", "-t", path).CombinedOutput()
		cancel()
		if err != nil {
			_ = mgr.Stop(serial)
			set("error", "install "+apk.Name+": "+strings.TrimSpace(string(out)))
			return
		}
	}

	// 6. Stop cleanly.
	set("running", "Deteniendo emulador…")
	_ = mgr.Stop(serial)
	time.Sleep(2 * time.Second)

	// 7. Save golden.
	set("running", "Guardando imagen golden "+name+"…")
	goldenDir := filepath.Join(s.cfg.GoldenDir, name)
	if _, _, err := mgr.SaveGolden(avdmanager.SaveGoldenOptions{Name: baseName, Destination: goldenDir}); err != nil {
		set("error", "SaveGolden: "+err.Error())
		return
	}
	meta, _ := json.Marshal(goldenMeta{Name: name, BaseName: baseName, Image: resolvedImage(req.Image), Device: resolvedDevice(req.Device), CreatedAt: nowMillis()})
	_ = os.WriteFile(filepath.Join(goldenDir, "golden.json"), meta, 0o644)

	job.Golden = name
	set("done", "Golden "+name+" listo")
}

// DeleteGolden removes a golden image directory from disk.
func (s *Service) DeleteGolden(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("golden name is required")
	}
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return fmt.Errorf("invalid golden name %q", name)
	}
	dir := filepath.Join(s.cfg.GoldenDir, name)
	if _, err := os.Stat(filepath.Join(dir, "userdata-qemu.img")); err != nil {
		return fmt.Errorf("golden %s not found", name)
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	go s.broadcast()
	return nil
}

func resolvedImage(image string) string {
	if strings.TrimSpace(image) != "" {
		return strings.TrimSpace(image)
	}
	return fmt.Sprintf("system-images;android-35;default;%s", hostABI())
}

func resolvedDevice(device string) string {
	if strings.TrimSpace(device) != "" {
		return strings.TrimSpace(device)
	}
	return "pixel_6"
}

func (s *Service) runningSerial(mgr *avdmanager.Manager, name string) (string, error) {
	procs, err := mgr.ListRunning()
	if err != nil {
		return "", err
	}
	for _, p := range procs {
		if p.Name == name {
			return p.Serial, nil
		}
	}
	return "", nil
}

// LaunchGolden creates a clone from a golden and boots it.
func (s *Service) LaunchGolden(goldenName, cloneName string, writable bool) (string, error) {
	golden := Golden{}
	found := false
	for _, g := range s.ListGoldens() {
		if g.Name == goldenName {
			golden = g
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("golden %s not found", goldenName)
	}
	if golden.BaseName == "" {
		return "", fmt.Errorf("golden %s no registra base AVD", goldenName)
	}
	// Self-heal: if the base AVD was removed, recreate it from the metadata so
	// the golden stays usable (its read-only system files are symlinked from it).
	mgr := s.manager()
	if writable {
		mgr = s.buildWritableManager(s.cfg.GPU, s.cfg.Windowed)
	}
	baseExists := false
	if infos, err := mgr.List(); err == nil {
		for _, i := range infos {
			if i.Name == golden.BaseName {
				baseExists = true
			}
		}
	}
	if !baseExists {
		if _, err := mgr.InitBase(avdmanager.InitBaseOptions{
			Name:        golden.BaseName,
			SystemImage: resolvedImage(golden.Image),
			Device:      resolvedDevice(golden.Device),
		}); err != nil {
			return "", fmt.Errorf("recrear base %s: %w", golden.BaseName, err)
		}
	}
	if strings.TrimSpace(cloneName) == "" {
		cloneName = goldenName + "-" + randomID()
	}
	if _, err := mgr.Clone(avdmanager.CloneOptions{BaseName: golden.BaseName, CloneName: cloneName, GoldenPath: golden.Path}); err != nil {
		return "", err
	}
	serial, err := mgr.Run(avdmanager.RunOptions{Name: cloneName})
	if err != nil {
		return "", err
	}
	go s.broadcast()
	return serial, nil
}
