package dockerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	http *http.Client
	base string
}

func New(dockerHost string) *Client {
	base := "http://localhost"
	var dial func(ctx context.Context, _, _ string) (net.Conn, error)
	switch {
	case strings.HasPrefix(dockerHost, "unix://"):
		sock := strings.TrimPrefix(dockerHost, "unix://")
		base = "http://docker"
		dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}
	default:
		base = strings.TrimRight(dockerHost, "/")
	}
	tr := &http.Transport{DisableKeepAlives: true}
	if dial != nil {
		tr.DialContext = dial
	}
	return &Client{http: &http.Client{Timeout: 30 * time.Second, Transport: tr}, base: base}
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("docker %s %s -> HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

type Endpoint struct {
	IPAddress string `json:"IPAddress"`
}

type Container struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Labels  map[string]string `json:"Labels"`
	// Networks ada di bawah NetworkSettings di respons /containers/json
	NetworkSettings struct {
		Networks map[string]Endpoint `json:"Networks"`
	} `json:"NetworkSettings"`
}

func (c Container) IPs() []string {
	out := make([]string, 0, len(c.NetworkSettings.Networks))
	for _, ep := range c.NetworkSettings.Networks {
		if ep.IPAddress != "" {
			out = append(out, ep.IPAddress)
		}
	}
	return out
}

func (c Container) Name() string {
	if len(c.Names) == 0 {
		return c.ID[:12]
	}
	return strings.TrimPrefix(c.Names[0], "/")
}

func (c *Client) Ping(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/_ping", nil, nil)
}

func (c *Client) Containers(ctx context.Context, all bool) ([]Container, error) {
	q := "/containers/json?all="
	if all {
		q += "true"
	} else {
		q += "false"
	}
	var out []Container
	if err := c.do(ctx, http.MethodGet, q, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

type Network struct {
	Name   string
	ID     string
	Driver string
	Internal bool
}

func (c *Client) Networks(ctx context.Context) ([]Network, error) {
	var out []Network
	if err := c.do(ctx, http.MethodGet, "/networks", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Connect ke jaringan compose bila container belum ada di sana (idempoten).
// Traefik hanya bisa reach container yang berada di network `proxy`.
func (c *Client) ConnectNetwork(ctx context.Context, network, container string) error {
	body := map[string]any{
		"Container":   container,
		"EndpointConfig": map[string]any{},
	}
	err := c.do(ctx, http.MethodPost, "/networks/"+network+"/connect", body, nil)
	if err != nil && strings.Contains(err.Error(), "already exists") {
		return nil
	}
	return err
}

// FindContainerByIP cari container berdasarkan IP di jaringan mana pun
// (dipakai saat Adopt: traefik loadbalancer memberi URL IP:port).
func FindContainerByIP(containers []Container, ip string) (Container, bool) {
	for _, c := range containers {
		for _, ep := range c.NetworkSettings.Networks {
			if ep.IPAddress == ip {
				return c, true
			}
		}
	}
	return Container{}, false
}
