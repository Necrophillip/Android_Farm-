package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Region describes a Mexican state profile used to localize an emulator for
// testing region-dependent behavior of your own app.
type Region struct {
	Code     string  `json:"code"`
	Name     string  `json:"name"`
	Capital  string  `json:"capital"`
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	Timezone string  `json:"timezone"`
}

// mexicanRegions is the catalog of the 32 states with capital coordinates and
// timezone.
var mexicanRegions = []Region{
	{"MX-AGS", "Aguascalientes", "Aguascalientes", 21.8853, -102.2916, "America/Mexico_City"},
	{"MX-BCN", "Baja California", "Mexicali", 32.6245, -115.4523, "America/Tijuana"},
	{"MX-BCS", "Baja California Sur", "La Paz", 24.1426, -110.3128, "America/Mazatlan"},
	{"MX-CAM", "Campeche", "Campeche", 19.8301, -90.5349, "America/Merida"},
	{"MX-CHP", "Chiapas", "Tuxtla Gutiérrez", 16.7569, -93.1291, "America/Mexico_City"},
	{"MX-CHH", "Chihuahua", "Chihuahua", 28.6353, -106.0889, "America/Chihuahua"},
	{"MX-CMX", "Ciudad de México", "Ciudad de México", 19.4326, -99.1332, "America/Mexico_City"},
	{"MX-COA", "Coahuila", "Saltillo", 25.4267, -101.0053, "America/Monterrey"},
	{"MX-COL", "Colima", "Colima", 19.2452, -103.7241, "America/Mexico_City"},
	{"MX-DUR", "Durango", "Victoria de Durango", 24.0277, -104.6532, "America/Monterrey"},
	{"MX-MEX", "Estado de México", "Toluca", 19.2926, -99.6557, "America/Mexico_City"},
	{"MX-GUA", "Guanajuato", "Guanajuato", 21.0190, -101.2574, "America/Mexico_City"},
	{"MX-GRO", "Guerrero", "Chilpancingo", 17.5510, -99.5001, "America/Mexico_City"},
	{"MX-HID", "Hidalgo", "Pachuca", 20.1011, -98.7591, "America/Mexico_City"},
	{"MX-JAL", "Jalisco", "Guadalajara", 20.6597, -103.3496, "America/Mexico_City"},
	{"MX-MIC", "Michoacán", "Morelia", 19.7060, -101.1950, "America/Mexico_City"},
	{"MX-MOR", "Morelos", "Cuernavaca", 18.9242, -99.2216, "America/Mexico_City"},
	{"MX-NAY", "Nayarit", "Tepic", 21.5039, -104.8947, "America/Mazatlan"},
	{"MX-NLE", "Nuevo León", "Monterrey", 25.6866, -100.3161, "America/Monterrey"},
	{"MX-OAX", "Oaxaca", "Oaxaca", 17.0732, -96.7266, "America/Mexico_City"},
	{"MX-PUE", "Puebla", "Puebla", 19.0414, -98.2063, "America/Mexico_City"},
	{"MX-QUE", "Querétaro", "Querétaro", 20.5888, -100.3899, "America/Mexico_City"},
	{"MX-ROO", "Quintana Roo", "Chetumal", 18.5001, -88.2961, "America/Cancun"},
	{"MX-SLP", "San Luis Potosí", "San Luis Potosí", 22.1565, -100.9855, "America/Mexico_City"},
	{"MX-SIN", "Sinaloa", "Culiacán", 24.8091, -107.3940, "America/Mazatlan"},
	{"MX-SON", "Sonora", "Hermosillo", 29.0729, -110.9559, "America/Hermosillo"},
	{"MX-TAB", "Tabasco", "Villahermosa", 17.9869, -92.9303, "America/Mexico_City"},
	{"MX-TAM", "Tamaulipas", "Ciudad Victoria", 23.7369, -99.1411, "America/Monterrey"},
	{"MX-TLA", "Tlaxcala", "Tlaxcala", 19.3182, -98.2375, "America/Mexico_City"},
	{"MX-VER", "Veracruz", "Xalapa", 19.5438, -96.9102, "America/Mexico_City"},
	{"MX-YUC", "Yucatán", "Mérida", 20.9674, -89.5926, "America/Merida"},
	{"MX-ZAC", "Zacatecas", "Zacatecas", 22.7709, -102.5832, "America/Mexico_City"},
}

func regionByCode(code string) (Region, bool) {
	for _, r := range mexicanRegions {
		if strings.EqualFold(r.Code, code) {
			return r, true
		}
	}
	return Region{}, false
}

// NetProfile is the per-device region/proxy assignment, persisted to disk.
type NetProfile struct {
	State string `json:"state,omitempty"`
	Proxy string `json:"proxy,omitempty"`
}

type netProfileStore struct {
	mu   sync.Mutex
	file string
	data map[string]NetProfile
}

func newNetProfileStore(file string) *netProfileStore {
	s := &netProfileStore{file: file, data: map[string]NetProfile{}}
	if b, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(b, &s.data)
	}
	return s
}

func (s *netProfileStore) get(device string) NetProfile {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[device]
}

func (s *netProfileStore) set(device string, p NetProfile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[device] = p
	b, _ := json.MarshalIndent(s.data, "", "  ")
	_ = os.MkdirAll(filepath.Dir(s.file), 0o755)
	_ = os.WriteFile(s.file, b, 0o644)
}

func (s *netProfileStore) all() map[string]NetProfile {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]NetProfile, len(s.data))
	for k, v := range s.data {
		out[k] = v
	}
	return out
}

// Regions returns the built-in state catalog.
func (s *Service) Regions() []Region { return mexicanRegions }

// NetProfiles returns the per-device assignments.
func (s *Service) NetProfiles() map[string]NetProfile {
	if s.nets == nil {
		return nil
	}
	return s.nets.all()
}

func (s *Service) adb(serial string, args ...string) error {
	ctx, cancel := context.WithTimeout(s.ctx(), 25*time.Second)
	defer cancel()
	full := append([]string{"-s", serial}, args...)
	out, err := exec.CommandContext(ctx, s.cfg.ADBBin, full...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// SetRegion localizes a device to a Mexican state: GPS, timezone and locale.
// Best-effort for timezone/locale; fails if the device is not running.
func (s *Service) SetRegion(device, stateCode string) (Region, error) {
	serial, err := s.resolveSerial(device)
	if err != nil {
		return Region{}, err
	}
	region, ok := regionByCode(stateCode)
	if !ok {
		return Region{}, fmt.Errorf("estado desconocido: %s", stateCode)
	}

	// setprop persist.* needs root on the userdebug emulator image.
	_ = s.adb(serial, "root")
	time.Sleep(1200 * time.Millisecond)

	// Location services + GPS fix (note: the emulator console takes lon,lat).
	_ = s.adb(serial, "shell", "settings", "put", "secure", "location_mode", "3")
	if err := s.adb(serial, "emu", "geo", "fix",
		strconv.FormatFloat(region.Lon, 'f', 6, 64),
		strconv.FormatFloat(region.Lat, 'f', 6, 64)); err != nil {
		return Region{}, fmt.Errorf("geo fix: %w", err)
	}

	// Timezone.
	_ = s.adb(serial, "shell", "settings", "put", "global", "auto_time_zone", "0")
	_ = s.adb(serial, "shell", "setprop", "persist.sys.timezone", region.Timezone)
	_ = s.adb(serial, "shell", "settings", "put", "global", "time_zone", region.Timezone)
	_ = s.adb(serial, "shell", "am", "broadcast", "-a", "android.intent.action.TIMEZONE_CHANGED")

	// Locale.
	_ = s.adb(serial, "shell", "setprop", "persist.sys.locale", "es-MX")
	_ = s.adb(serial, "shell", "setprop", "persist.sys.language", "es")
	_ = s.adb(serial, "shell", "setprop", "persist.sys.country", "MX")

	if s.nets != nil {
		p := s.nets.get(device)
		p.State = region.Code
		s.nets.set(device, p)
	}
	go s.broadcast()
	return region, nil
}

// SetProxy configures a fixed HTTP proxy for a device (empty clears it). This
// is a single, user-provided proxy for QA — there is no rotation service.
func (s *Service) SetProxy(device, proxy string) error {
	serial, err := s.resolveSerial(device)
	if err != nil {
		return err
	}
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		_ = s.adb(serial, "shell", "settings", "put", "global", "http_proxy", ":0")
		_ = s.adb(serial, "shell", "settings", "delete", "global", "http_proxy")
	} else {
		if !strings.Contains(proxy, ":") {
			return fmt.Errorf("formato esperado host:puerto")
		}
		if err := s.adb(serial, "shell", "settings", "put", "global", "http_proxy", proxy); err != nil {
			return err
		}
	}
	if s.nets != nil {
		p := s.nets.get(device)
		p.Proxy = proxy
		s.nets.set(device, p)
	}
	go s.broadcast()
	return nil
}

// readDeviceTraffic returns cumulative rx/tx bytes summed over non-loopback
// interfaces from the guest's /proc/net/dev.
func readDeviceTraffic(adbBin, serial string) (rx, tx int64, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, adbBin, "-s", serial, "shell", "cat", "/proc/net/dev").Output()
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(out), "\n") {
		name, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" || name == "lo" {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			continue
		}
		if r, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
			rx += r
		}
		if t, err := strconv.ParseInt(fields[8], 10, 64); err == nil {
			tx += t
		}
	}
	return rx, tx, true
}
