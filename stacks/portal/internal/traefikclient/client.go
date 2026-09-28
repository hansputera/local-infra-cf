package traefikclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	API        string // http://traefik:8080
	DynamicDir string // /infra/traefik/dynamic
	http       *http.Client
}

func New(api, dynamicDir string) *Client {
	return &Client{
		API:        api,
		DynamicDir: dynamicDir,
		http:       &http.Client{Timeout: 10 * time.Second},
	}
}

type Router struct {
	Name        string   `json:"name"`
	Rule        string   `json:"rule"`
	Service     string   `json:"service"`
	Provider    string   `json:"provider"`
	Status      string   `json:"status"`
	EntryPoints []string `json:"entryPoints"`
}

type traefikService struct {
	Name         string `json:"name"`
	LoadBalancer struct {
		Servers []struct {
			URL string `json:"url"`
		} `json:"servers"`
	} `json:"loadBalancer"`
}

type HostInfo struct {
	Hostname string
	Router   string
	Provider string
	Target   string // host:port from the load balancer
	Port     int
	IP       string
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.API+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("traefik %s -> HTTP %d", path, resp.StatusCode)
	}
	return json.Unmarshal(raw, out)
}

func (c *Client) Routers(ctx context.Context) ([]Router, error) {
	var out []Router
	if err := c.get(ctx, "/api/http/routers", &out); err != nil {
		return nil, err
	}
	return out, nil
}

type svcIndex struct {
	byName map[string]traefikService
	byBase map[string][]traefikService
}

func baseName(n string) string {
	if i := strings.LastIndex(n, "@"); i > 0 {
		return n[:i]
	}
	return n
}

func newSvcIndex(list []traefikService) svcIndex {
	idx := svcIndex{byName: map[string]traefikService{}, byBase: map[string][]traefikService{}}
	for _, s := range list {
		idx.byName[s.Name] = s
		b := baseName(s.Name)
		idx.byBase[b] = append(idx.byBase[b], s)
	}
	return idx
}

// get looks up the service owned by the router. Docker routers use the name
// without the provider suffix ("whoami") while /api/http/services uses
// "whoami@docker".
func (idx svcIndex) get(name, provider string) (traefikService, bool) {
	if s, ok := idx.byName[name]; ok {
		return s, true
	}
	if s, ok := idx.byName[name+"@"+provider]; ok {
		return s, true
	}
	for _, s := range idx.byBase[baseName(name)] {
		if strings.HasSuffix(s.Name, "@"+provider) {
			return s, true
		}
	}
	return traefikService{}, false
}

func (c *Client) services(ctx context.Context) (svcIndex, error) {
	var out []traefikService
	if err := c.get(ctx, "/api/http/services", &out); err != nil {
		return svcIndex{}, err
	}
	return newSvcIndex(out), nil
}

var hostRe = regexp.MustCompile("Host\\(`([^`]+)`\\)")

// Hosts maps every traefik router hostname back to its origin target.
func (c *Client) Hosts(ctx context.Context) ([]HostInfo, error) {
	routers, err := c.Routers(ctx)
	if err != nil {
		return nil, err
	}
	svcs, err := c.services(ctx)
	if err != nil {
		return nil, err
	}
	var out []HostInfo
	for _, r := range routers {
		if r.Name == "api@internal" {
			continue
		}
		m := hostRe.FindAllStringSubmatch(r.Rule, -1)
		if len(m) == 0 {
			continue
		}
		svc, ok := svcs.get(r.Service, r.Provider)
		if !ok {
			// router exists but the service was not found: still report the
			// hostname so its presence is not treated as drift by the engine.
			for _, hm := range m {
				out = append(out, HostInfo{Hostname: strings.ToLower(hm[1]), Router: r.Name, Provider: r.Provider})
			}
			continue
		}
		target := ""
		port := 0
		ip := ""
		if len(svc.LoadBalancer.Servers) > 0 {
			target = svc.LoadBalancer.Servers[0].URL
			if u, err := url.Parse(target); err == nil {
				ip = u.Hostname()
				port, _ = strconv.Atoi(u.Port())
			}
		}
		for _, hm := range m {
			out = append(out, HostInfo{
				Hostname: strings.ToLower(hm[1]),
				Router:   r.Name,
				Provider: r.Provider,
				Target:   target,
				Port:     port,
				IP:       ip,
			})
		}
	}
	return out, nil
}

func fileName(name string) string {
	return "svc-" + name + ".yml"
}

// WriteRouter writes the dynamic config for a single service. Watch=true in
// traefik.yml makes changes be picked up automatically. `target` = container
// name on the proxy network (not 127.0.0.1: from inside the traefik container,
// which would refer to itself).
func (c *Client) WriteRouter(name, hostname string, port int, entrypoint, target string) error {
	if entrypoint == "" {
		entrypoint = "web"
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid port: %d", port)
	}
	if strings.ContainsAny(target, " \t\n:/") {
		return fmt.Errorf("invalid target (must be a container name): %q", target)
	}
	rule := fmt.Sprintf("Host(`%s`)", hostname)
	body := fmt.Sprintf(`# written automatically by the portal (Infra Manager)
http:
  routers:
    %s:
      rule: %q
      entryPoints: [%q]
      service: %s
  services:
    %s:
      loadBalancer:
        servers:
          - url: "http://%s:%d"
`, name, rule, entrypoint, name, name, target, port)

	if err := os.MkdirAll(c.DynamicDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(c.DynamicDir, fileName(name))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c *Client) RemoveRouter(name string) error {
	path := filepath.Join(c.DynamicDir, fileName(name))
	err := os.Remove(path)
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

func (c *Client) WrittenFiles() (map[string]bool, error) {
	entries, err := os.ReadDir(c.DynamicDir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	out := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "svc-") {
			continue
		}
		out[strings.TrimSuffix(strings.TrimPrefix(e.Name(), "svc-"), ".yml")] = true
	}
	return out, nil
}
