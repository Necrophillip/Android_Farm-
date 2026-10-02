package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Proxy is a user-registered proxy (self-hosted or commercial) that can be
// assigned to a device for region testing. FarmUI stores addresses only; it
// does not generate, discover or rotate proxies.
type Proxy struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Region    string `json:"region,omitempty"`
	CreatedAt int64  `json:"createdAt"`
}

func (p Proxy) Addr() string { return fmt.Sprintf("%s:%d", p.Host, p.Port) }

type proxyStore struct {
	mu   sync.Mutex
	file string
	data map[string]Proxy
}

func newProxyStore(file string) *proxyStore {
	s := &proxyStore{file: file, data: map[string]Proxy{}}
	if b, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(b, &s.data)
	}
	return s
}

func (s *proxyStore) list() []Proxy {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Proxy, 0, len(s.data))
	for _, p := range s.data {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

func (s *proxyStore) save(p Proxy) (Proxy, error) {
	host := strings.TrimSpace(p.Host)
	if host == "" || p.Port <= 0 || p.Port > 65535 {
		return Proxy{}, fmt.Errorf("host y puerto válidos son obligatorios")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ID == "" {
		p.ID = randomID()
	}
	if p.CreatedAt == 0 {
		p.CreatedAt = nowMillis()
	}
	if strings.TrimSpace(p.Label) == "" {
		p.Label = host
	}
	p.Host = host
	s.data[p.ID] = p
	return p, s.persistLocked()
}

func (s *proxyStore) delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[id]; !ok {
		return fmt.Errorf("proxy %s not found", id)
	}
	delete(s.data, id)
	return s.persistLocked()
}

func (s *proxyStore) persistLocked() error {
	b, _ := json.MarshalIndent(s.data, "", "  ")
	_ = os.MkdirAll(filepath.Dir(s.file), 0o755)
	return os.WriteFile(s.file, b, 0o644)
}

// ParseProxy accepts "host:port", "user:pass@host:port" (userinfo ignored) and
// returns a Proxy entry.
func ParseProxy(raw string) (Proxy, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Proxy{}, fmt.Errorf("vacío")
	}
	if at := strings.LastIndex(raw, "@"); at >= 0 {
		raw = raw[at+1:]
	}
	host, portStr, ok := strings.Cut(raw, ":")
	if !ok {
		return Proxy{}, fmt.Errorf("formato esperado host:puerto")
	}
	port, err := strconv.Atoi(strings.TrimSpace(portStr))
	if err != nil {
		return Proxy{}, fmt.Errorf("puerto inválido: %s", portStr)
	}
	return Proxy{Host: strings.TrimSpace(host), Port: port}, nil
}

// ListProxies returns the saved proxy address book.
func (s *Service) ListProxies() []Proxy {
	if s.proxies == nil {
		return nil
	}
	return s.proxies.list()
}

// AddProxy registers a proxy from a raw "host:port" string.
func (s *Service) AddProxy(label, raw string) (Proxy, error) {
	if s.proxies == nil {
		return Proxy{}, fmt.Errorf("proxy store disabled")
	}
	p, err := ParseProxy(raw)
	if err != nil {
		return Proxy{}, err
	}
	p.Label = label
	saved, err := s.proxies.save(p)
	if err != nil {
		return Proxy{}, err
	}
	go s.broadcast()
	return saved, nil
}

// DeleteProxy removes a proxy from the address book.
func (s *Service) DeleteProxy(id string) error {
	if s.proxies == nil {
		return fmt.Errorf("proxy store disabled")
	}
	if err := s.proxies.delete(id); err != nil {
		return err
	}
	go s.broadcast()
	return nil
}
