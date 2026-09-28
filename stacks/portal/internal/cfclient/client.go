package cfclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const apiBase = "https://api.cloudflare.com/client/v4"

type Client struct {
	Token     string
	AccountID string
	ZoneID    string
	TunnelID  string

	http *http.Client
	// ingressMu mengunci GET-modify-PUT konfigurasi tunnel. PUT = REPLACE
	// seluruh config, jadi dua reconcile paralel bisa saling menghapus.
	ingressMu sync.Mutex
}

func New(token, accountID, zoneID, tunnelID string) *Client {
	return &Client{
		Token:     token,
		AccountID: accountID,
		ZoneID:    zoneID,
		TunnelID:  tunnelID,
		http:      &http.Client{Timeout: 20 * time.Second},
	}
}

type envelope struct {
	Result   json.RawMessage `json:"result"`
	Success  bool            `json:"success"`
	Errors   []cfError       `json:"errors"`
	Messages []cfError       `json:"messages"`
}

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *Client) do(ctx context.Context, method, url string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
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
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("respons CF bukan JSON (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	if !env.Success || resp.StatusCode >= 400 {
		return fmt.Errorf("CF %s %s -> HTTP %d: %s", method, path(url), resp.StatusCode, env.errorText())
	}
	if out != nil && len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("decode result: %w", err)
		}
	}
	return nil
}

func (e envelope) errorText() string {
	if len(e.Errors) == 0 {
		return "success=false tanpa pesan"
	}
	parts := make([]string, 0, len(e.Errors))
	for _, er := range e.Errors {
		parts = append(parts, fmt.Sprintf("%d %s", er.Code, er.Message))
	}
	return strings.Join(parts, "; ")
}

func path(u string) string {
	if i := strings.Index(u, "?"); i >= 0 {
		u = u[:i]
	}
	i := strings.Index(u, "/client/v4")
	if i >= 0 {
		return u[i:]
	}
	return u
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// ---- tunnel configuration ----

type IngressRule struct {
	Hostname      string         `json:"hostname,omitempty"`
	Service       string         `json:"service"`
	OriginRequest *OriginRequest `json:"originRequest,omitempty"`
}

type OriginRequest struct {
	HTTPHostHeader string `json:"httpHostHeader,omitempty"`
}

type tunnelConfig struct {
	Config json.RawMessage `json:"config"`
}

func (c *Client) tunnelURL(suffix string) string {
	return c.tunnelURLFor(c.TunnelID, suffix)
}

func (c *Client) tunnelURLFor(tid, suffix string) string {
	return fmt.Sprintf("%s/accounts/%s/cfd_tunnel/%s%s", apiBase, c.AccountID, tid, suffix)
}

// GetIngress mengembalikan ingress saat ini, urutan dipertahankan
// (catch-all harus tetap terakhir).
func (c *Client) GetIngress(ctx context.Context) ([]IngressRule, map[string]any, error) {
	return c.GetIngressTunnel(ctx, c.TunnelID)
}

func (c *Client) GetIngressTunnel(ctx context.Context, tid string) ([]IngressRule, map[string]any, error) {
	var tc tunnelConfig
	if err := c.do(ctx, http.MethodGet, c.tunnelURLFor(tid, "/configurations"), nil, &tc); err != nil {
		return nil, nil, err
	}
	cfg := map[string]any{}
	if len(tc.Config) > 0 && string(tc.Config) != "null" {
		if err := json.Unmarshal(tc.Config, &cfg); err != nil {
			return nil, nil, err
		}
	}
	var rules []IngressRule
	if raw, ok := cfg["ingress"]; ok && raw != nil {
		if err := json.Unmarshal(rawToJSON(raw), &rules); err != nil {
			return nil, nil, fmt.Errorf("decode ingress: %w", err)
		}
	}
	return rules, cfg, nil
}

func rawToJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

func (c *Client) PutIngress(ctx context.Context, cfg map[string]any, rules []IngressRule) error {
	return c.PutIngressTunnel(ctx, c.TunnelID, cfg, rules)
}

func (c *Client) PutIngressTunnel(ctx context.Context, tid string, cfg map[string]any, rules []IngressRule) error {
	raw, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	cfg["ingress"] = json.RawMessage(raw)
	return c.do(ctx, http.MethodPut, c.tunnelURLFor(tid, "/configurations"), map[string]any{"config": cfg}, nil)
}

// MutateIngress jalankan fn di bawah mutex lalu PUT hasilnya.
func (c *Client) MutateIngress(ctx context.Context, fn func([]IngressRule) ([]IngressRule, error)) ([]IngressRule, error) {
	return c.MutateIngressTunnel(ctx, c.TunnelID, fn)
}

// MutateIngressTunnel: sama, untuk tunnel tertentu. Mutex global karena
// PUT = REPLACE seluruh config dan dua operasi paralel bisa saling menghapus.
func (c *Client) MutateIngressTunnel(ctx context.Context, tid string, fn func([]IngressRule) ([]IngressRule, error)) ([]IngressRule, error) {
	c.ingressMu.Lock()
	defer c.ingressMu.Unlock()
	rules, cfg, err := c.GetIngressTunnel(ctx, tid)
	if err != nil {
		return nil, err
	}
	next, err := fn(rules)
	if err != nil {
		return nil, err
	}
	if err := c.PutIngressTunnel(ctx, tid, cfg, next); err != nil {
		return nil, err
	}
	return next, nil
}

func IsCatchAll(r IngressRule) bool { return r.Hostname == "" }

// SetHostname: set/replace rule hostname di posisi semula, catch-all tetap terakhir.
func SetHostname(rules []IngressRule, hostname, serviceURL string) []IngressRule {
	out := make([]IngressRule, 0, len(rules)+1)
	found := false
	for _, r := range rules {
		if IsCatchAll(r) {
			continue
		}
		if strings.EqualFold(r.Hostname, hostname) {
			out = append(out, IngressRule{
				Hostname:      hostname,
				Service:       serviceURL,
				OriginRequest: &OriginRequest{HTTPHostHeader: hostname},
			})
			found = true
			continue
		}
		out = append(out, r)
	}
	if !found {
		out = append(out, IngressRule{
			Hostname:      hostname,
			Service:       serviceURL,
			OriginRequest: &OriginRequest{HTTPHostHeader: hostname},
		})
	}
	out = appendCatchAll(rules, out)
	return out
}

func RemoveHostname(rules []IngressRule, hostname string) []IngressRule {
	out := make([]IngressRule, 0, len(rules))
	for _, r := range rules {
		if IsCatchAll(r) || strings.EqualFold(r.Hostname, hostname) {
			continue
		}
		out = append(out, r)
	}
	return appendCatchAll(rules, out)
}

func appendCatchAll(old, out []IngressRule) []IngressRule {
	for _, r := range old {
		if IsCatchAll(r) {
			out = append(out, r)
		}
	}
	if len(out) == 0 || !IsCatchAll(out[len(out)-1]) {
		out = append(out, IngressRule{Service: "http_status:404"})
	}
	return out
}

// ---- DNS ----

type DNSRecord struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl"`
}

func (c *Client) ListDNS(ctx context.Context) ([]DNSRecord, error) {
	var recs []DNSRecord
	url := fmt.Sprintf("%s/zones/%s/dns_records?per_page=100&type=CNAME", apiBase, c.ZoneID)
	for page := 1; ; page++ {
		var batch []DNSRecord
		u := fmt.Sprintf("%s&page=%d", url, page)
		if err := c.do(ctx, http.MethodGet, u, nil, &batch); err != nil {
			return nil, err
		}
		recs = append(recs, batch...)
		if len(batch) < 100 {
			return recs, nil
		}
	}
}

func (c *Client) EnsureCNAME(ctx context.Context, hostname, content string, proxied bool) error {
	recs, err := c.ListDNS(ctx)
	if err != nil {
		return err
	}
	for _, r := range recs {
		if !strings.EqualFold(r.Name, hostname) {
			continue
		}
		wantTTL := 1
		if !proxied {
			wantTTL = 300
		}
		if r.Type == "CNAME" && strings.EqualFold(r.Content, content) && r.Proxied == proxied && r.TTL == wantTTL {
			return nil
		}
		return c.do(ctx, http.MethodPatch,
			fmt.Sprintf("%s/zones/%s/dns_records/%s", apiBase, c.ZoneID, r.ID),
			map[string]any{"type": "CNAME", "name": hostname, "content": content, "proxied": proxied, "ttl": wantTTL},
			nil)
	}
	body := map[string]any{"type": "CNAME", "name": hostname, "content": content, "proxied": proxied, "ttl": 1}
	if !proxied {
		body["ttl"] = 300
	}
	return c.do(ctx, http.MethodPost, fmt.Sprintf("%s/zones/%s/dns_records", apiBase, c.ZoneID), body, nil)
}

func (c *Client) DeleteDNSByName(ctx context.Context, hostname string) error {
	recs, err := c.ListDNS(ctx)
	if err != nil {
		return err
	}
	for _, r := range recs {
		if strings.EqualFold(r.Name, hostname) {
			if err := c.do(ctx, http.MethodDelete,
				fmt.Sprintf("%s/zones/%s/dns_records/%s", apiBase, c.ZoneID, r.ID), nil, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---- tunnels ----

type Tunnel struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Status    string     `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
	DeletedAt *time.Time `json:"deleted_at"`
}

func (c *Client) ListTunnels(ctx context.Context) ([]Tunnel, error) {
	// result = array langsung (bukan objek pembungkus)
	var out []Tunnel
	url := fmt.Sprintf("%s/accounts/%s/cfd_tunnel?is_deleted=false", apiBase, c.AccountID)
	if err := c.do(ctx, http.MethodGet, url, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

type Conn struct {
	ColoName      string    `json:"colo_name"`
	ClientVersion string    `json:"client_version"`
	OriginIP      string    `json:"origin_ip"`
	OpenedAt      time.Time `json:"opened_at"`
	ClientID      string    `json:"client_id"`
	IsConnected   bool      `json:"is_connected"`
	ConnectionID  string    `json:"connection_id"`
}

type connectionsResult struct {
	Conns []Conn `json:"conns"`
}

// ConnGroup = satu proses cloudflared (client_id) yang terhubung ke tunnel.
type ConnGroup struct {
	ID      string    `json:"id"`
	Version string    `json:"version"`
	Arch    string    `json:"arch"`
	RunAt   time.Time `json:"run_at"`
	Conns   []Conn    `json:"conns"`
}

func (g ConnGroup) Colos() []string {
	var out []string
	for _, c := range g.Conns {
		out = append(out, c.ColoName)
	}
	return out
}

func (g ConnGroup) OriginIPs() []string {
	var out []string
	for _, c := range g.Conns {
		if c.OriginIP != "" {
			out = append(out, c.OriginIP)
		}
	}
	return out
}

func (c *Client) ConnectionsForTunnel(ctx context.Context, tid string) ([]ConnGroup, error) {
	// result = array grup connector; tiap grup punya "conns"
	var groups []ConnGroup
	if err := c.do(ctx, http.MethodGet, c.tunnelURLFor(tid, "/connections"), nil, &groups); err != nil {
		return nil, err
	}
	return groups, nil
}

func (c *Client) TunnelConnections(ctx context.Context) ([]Conn, error) {
	groups, err := c.ConnectionsForTunnel(ctx, c.TunnelID)
	if err != nil {
		return nil, err
	}
	var all []Conn
	for _, g := range groups {
		all = append(all, g.Conns...)
	}
	return all, nil
}

func (c *Client) TunnelToken(ctx context.Context) (string, error) {
	return c.TunnelTokenFor(ctx, c.TunnelID)
}

func (c *Client) TunnelTokenFor(ctx context.Context, tid string) (string, error) {
	// Respons CF kadang berupa string langsung, kadang objek {token}.
	var raw json.RawMessage
	if err := c.do(ctx, http.MethodGet, c.tunnelURLFor(tid, "/token"), nil, &raw); err != nil {
		return "", err
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil && asString != "" {
		return asString, nil
	}
	var obj struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil || obj.Token == "" {
		return "", fmt.Errorf("token tunnel tidak terbaca: %s", snippet(raw))
	}
	return obj.Token, nil
}

// CreateTunnel membuat tunnel remote-config baru di account.
func (c *Client) CreateTunnel(ctx context.Context, name string) (Tunnel, error) {
	var out Tunnel
	url := fmt.Sprintf("%s/accounts/%s/cfd_tunnel", apiBase, c.AccountID)
	body := map[string]any{"name": name, "config_src": "cloudflare"}
	if err := c.do(ctx, http.MethodPost, url, body, &out); err != nil {
		return Tunnel{}, err
	}
	return out, nil
}

// DeleteTunnel menghapus tunnel remote-config. Guard anti salah hapus ada di
// engine (tunnel infra & tunnel non-managed tidak boleh lewat sini).
func (c *Client) DeleteTunnel(ctx context.Context, tid string) error {
	return c.do(ctx, http.MethodDelete, c.tunnelURLFor(tid, ""), nil, nil)
}

// ---- Access (bootstrap portal di balik Cloudflare Access) ----

type AccessApp struct {
	ID              string         `json:"id,omitempty"`
	Name            string         `json:"name"`
	Domain          string         `json:"domain"`
	Type            string         `json:"type"`
	SessionDuration string         `json:"session_duration,omitempty"`
	Policies        []AccessPolicy `json:"policies,omitempty"`
}

type AccessPolicy struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name"`
	Decision string `json:"decision"`
	Include  []any  `json:"include"`
}

func (c *Client) ListAccessApps(ctx context.Context) ([]AccessApp, error) {
	var apps []AccessApp
	url := fmt.Sprintf("%s/accounts/%s/access/apps?per_page=100", apiBase, c.AccountID)
	if err := c.do(ctx, http.MethodGet, url, nil, &apps); err != nil {
		return nil, err
	}
	return apps, nil
}

// EnsureSelfHostedApp bikin app self-hosted di domain kalau belum ada,
// dengan policy allow email tertentu.
func (c *Client) EnsureSelfHostedApp(ctx context.Context, name, hostname string, emails []string) (string, error) {
	apps, err := c.ListAccessApps(ctx)
	if err != nil {
		return "", err
	}
	domain := hostname
	for _, a := range apps {
		if a.Domain == domain || strings.EqualFold(a.Domain, "https://"+domain) {
			return a.ID, nil
		}
	}
	include := make([]any, 0, len(emails))
	for _, e := range emails {
		include = append(include, map[string]any{"email": map[string]string{"email": e}})
	}
	app := AccessApp{
		Name:            name,
		Domain:          domain,
		Type:            "self_hosted",
		SessionDuration: "24h",
		Policies: []AccessPolicy{{
			Name:     "admin",
			Decision: "allow",
			Include:  include,
		}},
	}
	var created AccessApp
	if err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("%s/accounts/%s/access/apps", apiBase, c.AccountID), app, &created); err != nil {
		return "", err
	}
	return created.ID, nil
}
