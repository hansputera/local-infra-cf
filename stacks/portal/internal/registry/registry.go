package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	KindPublic  = "public"
	KindPrivate = "private"

	SourcePortal   = "portal"
	SourceAdopted  = "adopted"
)

type Service struct {
	Name      string `json:"name"`
	Hostname  string `json:"hostname"`
	Kind      string `json:"kind"`
	Target    string `json:"target"`
	Port      int    `json:"port"`
	Source    string `json:"source"`
	Notes     string `json:"notes,omitempty"`

	// TunnelID & NodeID: asal dipilih lewat form portal. Kosong = tunnel infra
	// dan origin default. NodeID menunjuk registry node (client_id cloudflared).
	TunnelID string `json:"tunnel_id,omitempty"`
	NodeID   string `json:"node_id,omitempty"`
	// Origin adalah service string ingress CF utk hostname ini. Kosong = ikut
	// node.Origin, lalu PUBLIC_ORIGIN.
	Origin string `json:"origin,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type file struct {
	Version  int                `json:"version"`
	Services map[string]*Service `json:"services"`
}

type Store struct {
	mu   sync.Mutex
	path string
	data file
}

var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func Open(path string) (*Store, error) {
	s := &Store{path: path, data: file{Version: 1, Services: map[string]*Service{}}}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, s.Save()
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return nil, fmt.Errorf("registry rusak: %w", err)
	}
	if s.data.Services == nil {
		s.data.Services = map[string]*Service{}
	}
	return s, nil
}

func (s *Store) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) List() []*Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Service, 0, len(s.data.Services))
	for _, svc := range s.data.Services {
		cp := *svc
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Store) Get(name string) (*Service, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	svc, ok := s.data.Services[name]
	if !ok {
		return nil, false
	}
	cp := *svc
	return &cp, true
}

func (s *Store) FindByHostname(host string) (*Service, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, svc := range s.data.Services {
		if strings.EqualFold(svc.Hostname, host) {
			cp := *svc
			return &cp, true
		}
	}
	return nil, false
}

func (s *Store) Put(svc Service) error {
	if err := Validate(svc); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	svc.Hostname = strings.ToLower(strings.TrimSpace(svc.Hostname))
	svc.Name = strings.ToLower(strings.TrimSpace(svc.Name))
	if existing, ok := s.data.Services[svc.Name]; ok {
		svc.CreatedAt = existing.CreatedAt
	} else if svc.CreatedAt.IsZero() {
		svc.CreatedAt = time.Now().UTC()
	}
	for _, other := range s.data.Services {
		if other.Name != svc.Name && strings.EqualFold(other.Hostname, svc.Hostname) {
			return fmt.Errorf("hostname sudah dipakai service %q", other.Name)
		}
	}
	svc.UpdatedAt = time.Now().UTC()
	cp := svc
	s.data.Services[svc.Name] = &cp
	return s.Save()
}

func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Services[name]; !ok {
		return fmt.Errorf("service %q tidak ada di registry", name)
	}
	delete(s.data.Services, name)
	return s.Save()
}

func Validate(svc Service) error {
	if !nameRe.MatchString(svc.Name) {
		return errors.New("nama service: huruf kecil, angka, tanda hubung saja")
	}
	if !strings.Contains(svc.Hostname, ".") {
		return errors.New("hostname tidak valid")
	}
	if svc.Kind != KindPublic && svc.Kind != KindPrivate {
		return errors.New("kind harus public atau private")
	}
	if svc.Target == "" {
		return errors.New("target (container / nama service) wajib diisi")
	}
	if svc.Port < 1 || svc.Port > 65535 {
		return errors.New("port harus 1-65535")
	}
	return nil
}
