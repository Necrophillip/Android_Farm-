package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"
)

//go:embed web
var webFS embed.FS

// Server exposes the farm REST API and the embedded single-page UI.
type Server struct {
	svc *Service
	mux *http.ServeMux
}

func NewServer(svc *Service) *Server {
	s := &Server{svc: svc, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	s.mux.Handle("GET /", http.FileServer(http.FS(sub)))

	s.mux.HandleFunc("GET /api/system", s.handleSystem)

	s.mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	s.mux.HandleFunc("PUT /api/settings", s.handlePutSettings)

	s.mux.HandleFunc("GET /api/avds", s.handleListAVDs)
	s.mux.HandleFunc("POST /api/avds", s.handleCreateAVD)
	s.mux.HandleFunc("DELETE /api/avds/{name}", s.handleDeleteAVD)
	s.mux.HandleFunc("POST /api/avds/{name}/start", s.handleStartAVD)
	s.mux.HandleFunc("POST /api/avds/{name}/stop", s.handleStopAVD)
	s.mux.HandleFunc("POST /api/avds/{name}/optimize", s.handleOptimizeAVD)

	s.mux.HandleFunc("GET /api/events", s.handleEvents)
	s.mux.HandleFunc("GET /api/instances", s.handleListAVDs)

	s.mux.HandleFunc("GET /api/macros", s.handleListMacros)
	s.mux.HandleFunc("POST /api/macros", s.handleSaveMacro)
	s.mux.HandleFunc("DELETE /api/macros/{id}", s.handleDeleteMacro)
	s.mux.HandleFunc("POST /api/instances/{name}/macro/start", s.handleStartMacro)
	s.mux.HandleFunc("POST /api/instances/{name}/macro/stop", s.handleStopMacro)

	s.mux.HandleFunc("GET /api/apks", s.handleListAPKs)
	s.mux.HandleFunc("POST /api/apks", s.handleUploadAPK)
	s.mux.HandleFunc("DELETE /api/apks/{id}", s.handleDeleteAPK)
	s.mux.HandleFunc("GET /api/goldens", s.handleListGoldens)
	s.mux.HandleFunc("POST /api/bake", s.handleBake)
	s.mux.HandleFunc("GET /api/jobs", s.handleListJobs)
	s.mux.HandleFunc("POST /api/goldens/{name}/launch", s.handleLaunchGolden)
	s.mux.HandleFunc("DELETE /api/goldens/{name}", s.handleDeleteGolden)
	s.mux.HandleFunc("GET /api/instances/{name}/metrics", s.handleMetrics)
	s.mux.HandleFunc("GET /api/instances/{name}/screenshot", s.handleScreenshot)
	s.mux.HandleFunc("POST /api/instances/{name}/input", s.handleInput)
	s.mux.HandleFunc("POST /api/instances/{name}/shell", s.handleShell)
	s.mux.HandleFunc("GET /api/instances/{name}/packages", s.handleListPackages)
	s.mux.HandleFunc("POST /api/instances/{name}/apks/{id}/install", s.handleInstallAPK)
	s.mux.HandleFunc("DELETE /api/instances/{name}/packages/{pkg}", s.handleUninstallPackage)
	s.mux.HandleFunc("POST /api/instances/{name}/packages/{pkg}/launch", s.handleLaunchPackage)
	s.mux.HandleFunc("POST /api/instances/{name}/region", s.handleSetRegion)
	s.mux.HandleFunc("POST /api/instances/{name}/proxy", s.handleSetProxy)
	s.mux.HandleFunc("GET /api/regions", s.handleRegions)
	s.mux.HandleFunc("GET /api/proxies", s.handleListProxies)
	s.mux.HandleFunc("POST /api/proxies", s.handleAddProxy)
	s.mux.HandleFunc("DELETE /api/proxies/{id}", s.handleDeleteProxy)
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.SystemInfo())
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Settings())
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var in Settings
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, s.svc.UpdateSettings(in))
	go s.svc.broadcast()
}

func (s *Server) handleListAVDs(w http.ResponseWriter, r *http.Request) {
	avds, err := s.svc.ListAVDs()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, avds)
}

type createAVDRequest struct {
	Name   string `json:"name"`
	Image  string `json:"image"`
	Device string `json:"device"`
	Lite   bool   `json:"lite"`
}

func (s *Server) handleCreateAVD(w http.ResponseWriter, r *http.Request) {
	var in createAVDRequest
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	avd, err := s.svc.CreateAVD(in.Name, in.Image, in.Device, in.Lite)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	go s.svc.broadcast()
	writeJSON(w, http.StatusCreated, avd)
}

func (s *Server) handleDeleteAVD(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.svc.DeleteAVD(name); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	go s.svc.broadcast()
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
}

func (s *Server) handleOptimizeAVD(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.svc.OptimizeAVD(name, true); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	go s.svc.broadcast()
	writeJSON(w, http.StatusOK, map[string]string{"status": "optimized", "name": name})
}

type startRequest struct {
	Port     int   `json:"port"`
	Windowed *bool `json:"windowed"`
}

func (s *Server) handleStartAVD(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var in startRequest
	if r.Body != nil {
		_ = decode(r, &in)
	}
	serial, err := s.svc.StartAVD(name, in.Port, in.Windowed)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	go s.svc.broadcast()
	writeJSON(w, http.StatusOK, map[string]any{"status": "started", "name": name, "serial": serial})
}

func (s *Server) handleStopAVD(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.svc.StopAVD(name); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	go s.svc.broadcast()
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped", "name": name})
}

// handleEvents streams full state snapshots over Server-Sent Events so the UI
// renders on demand instead of re-rendering on a fixed poll.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsub := s.svc.subscribe()
	defer unsub()

	if snap := s.svc.snapshot(); snap != nil {
		writeSSE(w, snap)
	}
	flusher.Flush()

	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case snap := <-ch:
			if !writeSSE(w, &snap) {
				return
			}
			flusher.Flush()
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeSSE(w io.Writer, snap *Snapshot) bool {
	b, err := json.Marshal(snap)
	if err != nil {
		return true
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
		return false
	}
	return true
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if m, ok := s.svc.metrics.get(name); ok {
		writeJSON(w, http.StatusOK, m)
		return
	}
	writeJSON(w, http.StatusOK, Metrics{Name: name, UpdatedAt: nowMillis()})
}

func (s *Server) handleListMacros(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Macros())
}

func (s *Server) handleSaveMacro(w http.ResponseWriter, r *http.Request) {
	var in Macro
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	saved, err := s.svc.SaveMacro(in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeleteMacro(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.svc.DeleteMacro(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": id})
}

func (s *Server) handleStartMacro(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var in MacroRunRequest
	if r.Body != nil {
		_ = decode(r, &in)
	}
	if strings.TrimSpace(in.ID) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("macro id is required"))
		return
	}
	if err := s.svc.StartMacro(name, in.ID, in.Loop); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "running", "name": name, "id": in.ID, "loop": in.Loop})
}

func (s *Server) handleStopMacro(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.svc.StopMacro(name)
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped", "name": name})
}

func (s *Server) handleListAPKs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.apks.list())
}

func (s *Server) handleUploadAPK(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(512 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	file, header, err := r.FormFile("apk")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("field 'apk' is required"))
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 512<<20))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if len(data) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("empty file"))
		return
	}
	apk, err := s.svc.apks.save(header.Filename, data)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, apk)
}

func (s *Server) handleDeleteAPK(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.svc.apks.delete(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": id})
}

func (s *Server) handleListGoldens(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.ListGoldens())
}

func (s *Server) handleBake(w http.ResponseWriter, r *http.Request) {
	var in BakeRequest
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	job := s.svc.bake.start(in)
	go s.svc.broadcast()
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.bake.list())
}

func (s *Server) handleLaunchGolden(w http.ResponseWriter, r *http.Request) {
	golden := r.PathValue("name")
	var in LaunchRequest
	if r.Body != nil {
		_ = decode(r, &in)
	}
	serial, err := s.svc.LaunchGolden(golden, in.CloneName, in.Writable)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "launched", "golden": golden, "serial": serial})
}

func (s *Server) handleDeleteGolden(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.svc.DeleteGolden(name); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
}

func (s *Server) handleScreenshot(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	force := r.URL.Query().Get("force") == "1"
	png, err := s.svc.Screenshot(name, force)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
}

func (s *Server) handleInput(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var in InputRequest
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.svc.Input(name, in); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleShell(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var in ShellRequest
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	out, err := s.svc.Shell(name, in.Command)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"output": out, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": out})
}

func (s *Server) handleListPackages(w http.ResponseWriter, r *http.Request) {
	pkgs, err := s.svc.ListPackages(r.PathValue("name"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, pkgs)
}

func (s *Server) handleInstallAPK(w http.ResponseWriter, r *http.Request) {
	out, err := s.svc.InstallAPK(r.PathValue("name"), r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"output": out, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "installed", "output": out})
}

func (s *Server) handleUninstallPackage(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.UninstallPackage(r.PathValue("name"), r.PathValue("pkg")); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "uninstalled", "package": r.PathValue("pkg")})
}

func (s *Server) handleLaunchPackage(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.LaunchPackage(r.PathValue("name"), r.PathValue("pkg")); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "launched", "package": r.PathValue("pkg")})
}

func (s *Server) handleRegions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Regions())
}

func (s *Server) handleListProxies(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.ListProxies())
}

func (s *Server) handleAddProxy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Label string `json:"label"`
		Proxy string `json:"proxy"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	p, err := s.svc.AddProxy(in.Label, in.Proxy)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleDeleteProxy(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.DeleteProxy(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": r.PathValue("id")})
}

func (s *Server) handleSetRegion(w http.ResponseWriter, r *http.Request) {
	var in struct {
		State string `json:"state"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	region, err := s.svc.SetRegion(r.PathValue("name"), in.State)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "region": region})
}

func (s *Server) handleSetProxy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Proxy string `json:"proxy"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.svc.SetProxy(r.PathValue("name"), in.Proxy); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "proxy": in.Proxy})
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/api") {
			log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}
	})
}

func decode(r *http.Request, dst any) error {
	if r.Body == nil {
		return errors.New("missing body")
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
