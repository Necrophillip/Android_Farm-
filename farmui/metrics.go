package main

import (
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

type metricsStore struct {
	mu      sync.RWMutex
	history map[string][]MetricPoint
	latest  map[string]Metrics
	max     int
}

func newMetricsStore() *metricsStore {
	return &metricsStore{
		history: make(map[string][]MetricPoint),
		latest:  make(map[string]Metrics),
		max:     90,
	}
}

func (s *metricsStore) record(name string, pid int, cpu float64, memBytes, uptime int64, totalMem int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	point := MetricPoint{T: nowMillis(), CPU: cpu, Mem: memBytes}
	hist := append(s.history[name], point)
	if len(hist) > s.max {
		hist = hist[len(hist)-s.max:]
	}
	s.history[name] = hist

	m := Metrics{
		Name:      name,
		PID:       pid,
		CPU:       cpu,
		MemBytes:  memBytes,
		UptimeSec: uptime,
		History:   append([]MetricPoint(nil), hist...),
		UpdatedAt: nowMillis(),
	}
	if totalMem > 0 {
		m.MemPct = float64(memBytes) / float64(totalMem) * 100
	}
	s.latest[name] = m
}

func (s *metricsStore) get(name string) (Metrics, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.latest[name]
	return m, ok
}

// prune drops samples for devices that are no longer running.
func (s *metricsStore) prune(alive map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name := range s.latest {
		if !alive[name] {
			delete(s.latest, name)
			delete(s.history, name)
		}
	}
}

// readProcessStats returns CPU%, RSS bytes and uptime seconds for a PID using
// the host `ps` tool. Works on macOS and Linux.
func readProcessStats(pid int) (cpu float64, rssBytes, uptime int64, ok bool) {
	if pid <= 0 {
		return 0, 0, 0, false
	}
	out, err := exec.Command("ps", "-o", "pcpu=,rss=,etime=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, 0, 0, false
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) < 3 {
		return 0, 0, 0, false
	}
	cpu, _ = strconv.ParseFloat(fields[0], 64)
	rssKB, _ := strconv.ParseInt(fields[1], 10, 64)
	return cpu, rssKB * 1024, parseEtime(fields[2]), true
}

// parseEtime understands the ps etime formats MM:SS, HH:MM:SS and D-HH:MM:SS.
func parseEtime(v string) int64 {
	var days int64
	if i := strings.IndexByte(v, '-'); i >= 0 {
		d, _ := strconv.ParseInt(v[:i], 10, 64)
		days = d
		v = v[i+1:]
	}
	parts := strings.Split(v, ":")
	var h, m, s int64
	switch len(parts) {
	case 3:
		h, _ = strconv.ParseInt(parts[0], 10, 64)
		m, _ = strconv.ParseInt(parts[1], 10, 64)
		s, _ = strconv.ParseInt(parts[2], 10, 64)
	case 2:
		m, _ = strconv.ParseInt(parts[0], 10, 64)
		s, _ = strconv.ParseInt(parts[1], 10, 64)
	}
	return days*86400 + h*3600 + m*60 + s
}

func totalSystemMem() int64 {
	for _, key := range []string{"hw.memsize", "hw.physmem"} {
		out, err := exec.Command("sysctl", "-n", key).Output()
		if err == nil {
			if n, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err == nil && n > 0 {
				return n
			}
		}
	}
	// Linux fallback: /proc/meminfo MemTotal (kB)
	out, err := exec.Command("sh", "-c", "awk '/MemTotal/{print $2*1024}' /proc/meminfo").Output()
	if err == nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err == nil {
			return n
		}
	}
	return 0
}

func (s *Service) sampleLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	s.sample()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			s.sample()
		}
	}
}

func (s *Service) sample() {
	procs, err := s.manager().ListRunning()
	if err != nil {
		return
	}
	alive := make(map[string]bool, len(procs))
	total := s.totalMem
	for _, p := range procs {
		alive[p.Name] = true
		if cpu, mem, up, ok := readProcessStats(p.PID); ok {
			s.metrics.record(p.Name, p.PID, cpu, mem, up, total)
		}
	}
	s.metrics.prune(alive)
	s.pruneShots(alive)
	s.broadcast()
}
