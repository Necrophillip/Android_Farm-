package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// macroStore persists macros as JSON files under a directory.
type macroStore struct {
	mu    sync.Mutex
	dir   string
	items map[string]Macro
}

func newMacroStore(dir string) *macroStore {
	s := &macroStore{dir: dir, items: make(map[string]Macro)}
	_ = os.MkdirAll(dir, 0o755)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var m Macro
		if json.Unmarshal(b, &m) == nil && m.ID != "" {
			s.items[m.ID] = m
		}
	}
	return s
}

func (s *macroStore) list() []Macro {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Macro, 0, len(s.items))
	for _, m := range s.items {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

func (s *macroStore) get(id string) (Macro, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.items[id]
	return m, ok
}

func (s *macroStore) save(m Macro) (Macro, error) {
	if strings.TrimSpace(m.Name) == "" {
		return Macro{}, fmt.Errorf("macro name is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.ID == "" {
		m.ID = randomID()
	}
	if m.CreatedAt == 0 {
		m.CreatedAt = nowMillis()
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return Macro{}, err
	}
	if err := os.WriteFile(filepath.Join(s.dir, m.ID+".json"), b, 0o644); err != nil {
		return Macro{}, err
	}
	s.items[m.ID] = m
	return m, nil
}

func (s *macroStore) delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[id]; !ok {
		return fmt.Errorf("macro %s not found", id)
	}
	delete(s.items, id)
	return os.Remove(filepath.Join(s.dir, id+".json"))
}

func randomID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// macroRunner executes macros against devices, optionally looping until stopped.
type macroRunner struct {
	svc    *Service
	mu     sync.Mutex
	active map[string]*macroSession // device name -> session
}

type macroSession struct {
	macroID string
	name    string
	cancel  chan struct{}
	done    chan struct{}
}

func newMacroRunner(svc *Service) *macroRunner {
	return &macroRunner{svc: svc, active: make(map[string]*macroSession)}
}

func (r *macroRunner) runningID(device string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.active[device]; ok {
		return s.macroID
	}
	return ""
}

func (r *macroRunner) start(device string, m Macro, loop bool) error {
	if len(m.Steps) == 0 {
		return fmt.Errorf("macro %q has no steps", m.Name)
	}
	r.stop(device)

	session := &macroSession{
		macroID: m.ID,
		name:    m.Name,
		cancel:  make(chan struct{}),
		done:    make(chan struct{}),
	}
	r.mu.Lock()
	r.active[device] = session
	r.mu.Unlock()

	go func() {
		defer func() {
			r.mu.Lock()
			if r.active[device] == session {
				delete(r.active, device)
			}
			r.mu.Unlock()
			close(session.done)
			r.svc.broadcast()
		}()
		for {
			for _, step := range m.Steps {
				select {
				case <-session.cancel:
					return
				default:
				}
				_ = r.svc.applyMacroStep(device, step)
				if step.DelayMs > 0 {
					delay := time.Duration(step.DelayMs) * time.Millisecond
					if delay > 5*time.Second {
						delay = 5 * time.Second
					}
					select {
					case <-session.cancel:
						return
					case <-time.After(delay):
					}
				}
			}
			if !loop {
				return
			}
			// brief pause between loops
			select {
			case <-session.cancel:
				return
			case <-time.After(400 * time.Millisecond):
			}
		}
	}()
	r.svc.broadcast()
	return nil
}

func (r *macroRunner) stop(device string) {
	r.mu.Lock()
	session, ok := r.active[device]
	if ok {
		delete(r.active, device)
	}
	r.mu.Unlock()
	if !ok {
		return
	}
	close(session.cancel)
	select {
	case <-session.done:
	case <-time.After(3 * time.Second):
	}
	r.svc.broadcast()
}
