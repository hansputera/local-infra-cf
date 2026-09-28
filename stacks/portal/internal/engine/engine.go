package engine

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"portal/internal/cfclient"
	"portal/internal/config"
	"portal/internal/dockerclient"
	"portal/internal/registry"
	"portal/internal/traefikclient"
)

type Engine struct {
	Cfg config.Config
	Reg *registry.Store
	NS  *registry.NodeStore
	CF  *cfclient.Client
	DK  *dockerclient.Client
	TR  *traefikclient.Client
}

func New(cfg config.Config, reg *registry.Store, ns *registry.NodeStore) *Engine {
	return &Engine{
		Cfg: cfg,
		Reg: reg,
		NS:  ns,
		CF:  cfclient.New(cfg.CFToken, cfg.CFAccountID, cfg.CFZoneID, cfg.CFTunnelID),
		DK:  dockerclient.New(cfg.DockerHost),
		TR:  traefikclient.New(cfg.TraefikAPI, cfg.InfraRoot+"/traefik/dynamic"),
	}
}

type ServiceStatus struct {
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	Kind     string `json:"kind"`
	Target   string `json:"target"`
	Port     int    `json:"port"`
	Source   string `json:"source"`
	Notes    string `json:"notes,omitempty"`

	CFIngress bool   `json:"cf_ingress"`
	DNS       bool   `json:"dns"`
	Traefik   bool   `json:"traefik"`
	Routing   string `json:"routing"`   // file | label | ""
	Container string `json:"container"` // running | stopped | missing | ""

	TunnelID      string   `json:"tunnel_id"`
	TunnelName    string   `json:"tunnel_name,omitempty"`
	NodeID        string   `json:"node_id,omitempty"`
	NodeName      string   `json:"node_name,omitempty"`
	Origin        string   `json:"origin,omitempty"`
	NodeConnected bool     `json:"node_connected,omitempty"`
	ProbeCode     int      `json:"probe_code"`
	ProbeErr      string   `json:"probe_err,omitempty"`
	Drift         []string `json:"drift,omitempty"`
	Hints         []string `json:"hints,omitempty"`
}

type Orphan struct {
	Hostname string `json:"hostname"`
	Where    string `json:"where"` // cf_ingress | traefik
	Target   string `json:"target,omitempty"`
}

type Report struct {
	Services []ServiceStatus `json:"services"`
	Orphans  []Orphan        `json:"orphans"`
	Warnings []string        `json:"warnings,omitempty"`
	Checked  time.Time       `json:"checked_at"`
}

// Reconcile aligns a single service across three places: the traefik file,
// the CF tunnel ingress, DNS CNAME. Order: traefik first (origin ready),
// then CF (hostname starts resolving), DNS last.
func (e *Engine) Reconcile(ctx context.Context, name string) error {
	svc, ok := e.Reg.Get(name)
	if !ok {
		return fmt.Errorf("service %q not in the registry", name)
	}
	return e.apply(ctx, *svc)
}

func (e *Engine) ReconcileAll(ctx context.Context) []error {
	e.purgeDupFiles(ctx)
	var errs []error
	for _, svc := range e.Reg.List() {
		if err := e.apply(ctx, *svc); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", svc.Name, err))
		}
	}
	return errs
}

func (e *Engine) apply(ctx context.Context, svc registry.Service) error {
	// When the hostname is already claimed by a compose label (docker provider),
	// do not write a dynamic file: two routers with the same rule = ambiguity in
	// traefik. Old files (e.g. written while the docker router briefly vanished
	// on restart) are cleaned up so no duplicate remains.
	if routing, _ := e.routingFor(ctx, svc.Hostname); routing == RoutingLabel {
		if err := e.TR.RemoveRouter(svc.Name); err != nil {
			return fmt.Errorf("clean up duplicate traefik file: %w", err)
		}
		return e.syncCF(ctx, svc)
	}
	if err := e.TR.WriteRouter(svc.Name, svc.Hostname, svc.Port, "web", svc.Target); err != nil {
		return fmt.Errorf("write traefik router: %w", err)
	}
	if svc.Target != "" && svc.Target != "traefik" {
		if err := e.DK.ConnectNetwork(ctx, "infra_proxy", svc.Target); err != nil {
			// non-fatal: the container may already be on the network or stopped
			_ = err
		}
	}
	return e.syncCF(ctx, svc)
}

// tunnelOf: tunnel this hostname is served on. Empty = the infra tunnel.
func (e *Engine) tunnelOf(svc registry.Service) string {
	if svc.TunnelID != "" {
		return svc.TunnelID
	}
	return e.Cfg.CFTunnelID
}

// originOf: the ingress service string. Priority: service origin > node origin >
// PUBLIC_ORIGIN (default: traefik on this host).
func (e *Engine) originOf(svc registry.Service) string {
	if svc.Origin != "" {
		return svc.Origin
	}
	if svc.NodeID != "" {
		if nd, ok := e.NS.Get(svc.NodeID); ok && nd.Origin != "" {
			return nd.Origin
		}
	}
	return e.Cfg.PublicOrigin
}

func (e *Engine) tunnelName(tid string) string {
	if tid == e.Cfg.CFTunnelID {
		return "infra"
	}
	if mt, ok := e.NS.GetTunnel(tid); ok && mt.Name != "" {
		return mt.Name
	}
	if t, err := e.CF.ListTunnels(context.Background()); err == nil {
		for _, x := range t {
			if x.ID == tid {
				return strings.TrimSpace(x.Name)
			}
		}
	}
	return ""
}

func (e *Engine) syncCF(ctx context.Context, svc registry.Service) error {
	if svc.Kind != registry.KindPublic {
		return nil
	}
	tid := e.tunnelOf(svc)
	origin := e.originOf(svc)
	if _, err := e.CF.MutateIngressTunnel(ctx, tid, func(rules []cfclient.IngressRule) ([]cfclient.IngressRule, error) {
		return cfclient.SetHostname(rules, svc.Hostname, origin), nil
	}); err != nil {
		return fmt.Errorf("ingress tunnel %s: %w", short(tid), err)
	}
	if err := e.CF.EnsureCNAME(ctx, svc.Hostname, tid+".cfargotunnel.com", true); err != nil {
		return fmt.Errorf("dns: %w", err)
	}
	return nil
}

const (
	RoutingFile  = "file"
	RoutingLabel = "label"
	RoutingNone  = ""
)

// routedByLabel: is this hostname claimed by a traefik compose label?
// That source of truth is more stable than the router's presence (a docker
// router can vanish briefly while a container restarts).
func (e *Engine) routedByLabel(ctx context.Context, hostname string) bool {
	want := "host(`" + strings.ToLower(hostname) + "`)"
	for _, c := range e.containers(ctx) {
		for k, v := range c.Labels {
			if !strings.HasPrefix(k, "traefik.http.routers.") || !strings.HasSuffix(k, ".rule") {
				continue
			}
			if strings.Contains(strings.ToLower(v), want) {
				return true
			}
		}
	}
	return false
}

// purgeDupFiles removes dynamic files for hostnames also claimed by compose
// labels (duplicate @docker + @file routers).
func (e *Engine) purgeDupFiles(ctx context.Context) {
	hosts, err := e.TR.Hosts(ctx)
	if err != nil {
		return
	}
	byHost := map[string][]traefikclient.HostInfo{}
	for _, h := range hosts {
		k := strings.ToLower(h.Hostname)
		byHost[k] = append(byHost[k], h)
	}
	for _, hs := range byHost {
		docker, file := false, ""
		for _, h := range hs {
			switch h.Provider {
			case "docker":
				docker = true
			case "file":
				file = h.Router
			}
		}
		if docker && file != "" {
			_ = e.TR.RemoveRouter(strings.TrimSuffix(file, "@file"))
		}
	}
}

// routingFor: file = written by the portal, label = held by the traefik docker provider.
func (e *Engine) routingFor(ctx context.Context, hostname string) (string, string) {
	if e.routedByLabel(ctx, hostname) {
		return RoutingLabel, ""
	}
	hosts, err := e.TR.Hosts(ctx)
	if err != nil {
		return RoutingNone, ""
	}
	for _, h := range hosts {
		if strings.EqualFold(h.Hostname, hostname) {
			if h.Provider == "docker" {
				return RoutingLabel, h.Target
			}
			return RoutingFile, h.Target
		}
	}
	return RoutingNone, ""
}

// Remove reverses reconcile: traefik file, CF ingress, then DNS, registry last.
// Returns a warning when routing is still held by a compose label.
func (e *Engine) Remove(ctx context.Context, name string) (string, error) {
	svc, ok := e.Reg.Get(name)
	if !ok {
		return "", fmt.Errorf("service %q not in the registry", name)
	}
	warn := ""
	if routing, _ := e.routingFor(ctx, svc.Hostname); routing == RoutingLabel {
		warn = "the compose-label router is still active in traefik; remove the traefik label on that compose service if you really want it off"
	} else if err := e.TR.RemoveRouter(svc.Name); err != nil {
		return "", fmt.Errorf("remove traefik file: %w", err)
	}
	if svc.Kind == registry.KindPublic {
		if _, err := e.CF.MutateIngressTunnel(ctx, e.tunnelOf(*svc), func(rules []cfclient.IngressRule) ([]cfclient.IngressRule, error) {
			return cfclient.RemoveHostname(rules, svc.Hostname), nil
		}); err != nil {
			return "", fmt.Errorf("remove ingress: %w", err)
		}
		if err := e.CF.DeleteDNSByName(ctx, svc.Hostname); err != nil {
			return "", fmt.Errorf("remove dns: %w", err)
		}
	}
	return warn, e.Reg.Delete(name)
}

// Status compares the registry against reality (CF + traefik + docker), then
// probes HTTPS when enabled. Ingress is checked on the tunnel each service
// uses (multi-tunnel); DNS is checked for CNAME direction.
func (e *Engine) Status(ctx context.Context, probe bool) (Report, error) {
	rep := Report{Checked: time.Now().UTC(), Services: []ServiceStatus{}, Orphans: []Orphan{}}

	dnsRecs, dnsErr := e.CF.ListDNS(ctx)
	if dnsErr != nil {
		return rep, dnsErr
	}
	hosts, hostErr := e.TR.Hosts(ctx)
	if hostErr != nil {
		return rep, hostErr
	}
	containers, contErr := e.DK.Containers(ctx, true)
	if contErr != nil {
		containers = nil
	}

	// tunnels to check: infra + every tunnel referenced by a public service
	tunnelIDs := []string{e.Cfg.CFTunnelID}
	seenT := map[string]bool{e.Cfg.CFTunnelID: true}
	for _, svc := range e.Reg.List() {
		if svc.Kind != registry.KindPublic {
			continue
		}
		tid := e.tunnelOf(*svc)
		if !seenT[tid] {
			seenT[tid] = true
			tunnelIDs = append(tunnelIDs, tid)
		}
	}

	ingressByTunnel := map[string]map[string]string{}
	for _, tid := range tunnelIDs {
		rules, _, err := e.CF.GetIngressTunnel(ctx, tid)
		if err != nil {
			if tid == e.Cfg.CFTunnelID {
				return rep, err
			}
			rep.Warnings = append(rep.Warnings, "failed to read ingress of tunnel "+short(tid)+": "+err.Error())
			continue
		}
		m := map[string]string{}
		for _, r := range rules {
			if !cfclient.IsCatchAll(r) {
				m[strings.ToLower(r.Hostname)] = r.Service
			}
		}
		ingressByTunnel[tid] = m
	}

	// live connections per tunnel: used for node status checks
	liveClient := map[string]bool{}
	for _, tid := range tunnelIDs {
		groups, err := e.CF.ConnectionsForTunnel(ctx, tid)
		if err != nil {
			rep.Warnings = append(rep.Warnings, "failed to read connections of tunnel "+short(tid)+": "+err.Error())
			continue
		}
		for _, g := range groups {
			if g.ID != "" {
				liveClient[g.ID] = true
			}
		}
	}

	dnsSet := map[string]bool{}
	dnsContent := map[string]string{}
	for _, d := range dnsRecs {
		dnsSet[strings.ToLower(d.Name)] = true
		dnsContent[strings.ToLower(d.Name)] = d.Content
	}
	hostSet := map[string]traefikclient.HostInfo{}
	for _, h := range hosts {
		hostSet[h.Hostname] = h
	}

	// cache tunnel names (avoid repeated tunnel-list queries)
	tunnelNames := map[string]string{e.Cfg.CFTunnelID: "infra"}
	if list, err := e.CF.ListTunnels(ctx); err == nil {
		for _, t := range list {
			tunnelNames[t.ID] = strings.TrimSpace(t.Name)
		}
	}
	for _, mt := range e.NS.Tunnels() {
		if _, ok := tunnelNames[mt.ID]; !ok || tunnelNames[mt.ID] == "" {
			tunnelNames[mt.ID] = mt.Name
		}
	}

	seen := map[string]bool{}
	for _, svc := range e.Reg.List() {
		seen[stringToLower(svc.Hostname)] = true
		tid := e.tunnelOf(*svc)
		origin := e.originOf(*svc)
		st := ServiceStatus{
			Name: svc.Name, Hostname: svc.Hostname, Kind: svc.Kind,
			Target: svc.Target, Port: svc.Port, Source: svc.Source, Notes: svc.Notes,
			TunnelID: tid, TunnelName: tunnelNames[tid], NodeID: svc.NodeID, Origin: origin,
			Drift: []string{},
		}
		if svc.NodeID != "" {
			if nd, ok := e.NS.Get(svc.NodeID); ok {
				st.NodeName = nd.Name
				st.NodeConnected = liveClient[nd.ClientID]
				if !st.NodeConnected {
					st.Drift = append(st.Drift, "node "+nd.Name+" ("+short(nd.ClientID)+") is not connected to tunnel "+short(tid))
				}
			} else {
				st.Drift = append(st.Drift, "node_id "+short(svc.NodeID)+" is not in the node registry")
			}
		}

		ingressMap := ingressByTunnel[tid]
		st.CFIngress = hasKey(ingressMap, svc.Hostname)
		st.DNS = hasKey(dnsSet, svc.Hostname)
		st.Traefik = hasKey(hostSet, svc.Hostname)
		st.Container = e.containerState(containers, svc.Target)
		if h, ok := hostSet[svc.Hostname]; ok {
			if h.Provider == "docker" {
				st.Routing = RoutingLabel
				st.Hints = append(st.Hints, "routing is held by compose labels; change the port via labels, not the portal form")
			} else {
				st.Routing = RoutingFile
			}
		}

		if !st.Traefik {
			st.Drift = append(st.Drift, "traefik router missing (file svc-"+svc.Name+".yml not readable)")
		}
		if svc.Kind == registry.KindPublic {
			if !st.CFIngress {
				st.Drift = append(st.Drift, "hostname missing from ingress of tunnel "+short(tid))
			} else if got := ingressMap[stringToLower(svc.Hostname)]; !strings.EqualFold(got, origin) {
				st.Drift = append(st.Drift, "ingress origin ("+got+") differs from the portal choice ("+origin+")")
			}
			if !st.DNS {
				st.Drift = append(st.Drift, "DNS CNAME missing")
			} else if want := tid + ".cfargotunnel.com"; !strings.EqualFold(dnsContent[stringToLower(svc.Hostname)], want) {
				st.Drift = append(st.Drift, "CNAME points to "+dnsContent[stringToLower(svc.Hostname)]+", should be "+want)
			}
		}
		if st.Container == "stopped" {
			st.Drift = append(st.Drift, "container "+svc.Target+" is stopped")
		}
		if st.Container == "missing" {
			st.Drift = append(st.Drift, "container "+svc.Target+" not found")
		}
		rep.Services = append(rep.Services, st)
	}

	// HTTPS probes in parallel: N hosts x one timeout could take forever.
	if probe {
		type res struct {
			idx  int
			code int
			err  string
		}
		ch := make(chan res, len(rep.Services))
		running := 0
		for i := range rep.Services {
			st := &rep.Services[i]
			if st.Kind != registry.KindPublic || !st.CFIngress || !st.DNS {
				continue
			}
			running++
			go func(idx int, host string) {
				code, msg := probeHTTPS(ctx, host)
				ch <- res{idx: idx, code: code, err: msg}
			}(i, st.Hostname)
		}
		for ; running > 0; running-- {
			r := <-ch
			st := &rep.Services[r.idx]
			st.ProbeCode = r.code
			st.ProbeErr = r.err
			if r.code >= 500 {
				st.Drift = append(st.Drift, fmt.Sprintf("origin answered HTTP %d", r.code))
			} else if r.code == 0 {
				st.Drift = append(st.Drift, "probe failed: "+r.err)
			}
		}
	}

	for tid, ingressMap := range ingressByTunnel {
		for host, svcURL := range ingressMap {
			if !seen[host] {
				rep.Orphans = append(rep.Orphans, Orphan{Hostname: host, Where: "cf_ingress " + short(tid), Target: svcURL})
			}
		}
	}
	for host, h := range hostSet {
		if !seen[host] {
			rep.Orphans = append(rep.Orphans, Orphan{Hostname: host, Where: "traefik", Target: h.Target})
		}
	}
	sort.Slice(rep.Services, func(i, j int) bool { return len(rep.Services[i].Drift) > len(rep.Services[j].Drift) })
	sort.Slice(rep.Orphans, func(i, j int) bool { return rep.Orphans[i].Hostname < rep.Orphans[j].Hostname })
	return rep, nil
}

// Adopt registers an existing hostname (once portal-owned / manual) into the
// registry so it gets managed, without changing CF.
func (e *Engine) Adopt(ctx context.Context, hostname string) (*registry.Service, error) {
	hostname = strings.ToLower(strings.TrimSpace(hostname))
	if _, ok := e.Reg.FindByHostname(hostname); ok {
		return nil, fmt.Errorf("%s is already registered", hostname)
	}
	hosts, err := e.TR.Hosts(ctx)
	if err != nil {
		return nil, err
	}
	var found *traefikclient.HostInfo
	for i := range hosts {
		if hosts[i].Hostname == hostname {
			found = &hosts[i]
			break
		}
	}
	if found == nil {
		return nil, fmt.Errorf("no traefik router for %s", hostname)
	}
	kind := registry.KindPublic
	if rules, _, err := e.CF.GetIngress(ctx); err == nil {
		hit := false
		for _, r := range rules {
			if strings.EqualFold(r.Hostname, hostname) {
				hit = true
			}
		}
		if !hit {
			kind = registry.KindPrivate
		}
	}

	name := hostname[:strings.Index(hostname, ".")]
	if _, exists := e.Reg.Get(name); exists {
		name = strings.ReplaceAll(hostname, ".", "-")
	}
	target := hostname
	if found.IP != "" {
		if c, ok := dockerclient.FindContainerByIP(e.containers(ctx), found.IP); ok {
			target = c.Name()
		}
	}
	port := found.Port
	if port == 0 {
		port = 80
	}
	svc := registry.Service{
		Name: name, Hostname: hostname, Kind: kind,
		Target: target, Port: port, Source: registry.SourceAdopted,
		TunnelID: e.Cfg.CFTunnelID,
	}
	if err := e.Reg.Put(svc); err != nil {
		return nil, err
	}
	if kind == registry.KindPublic {
		if err := e.apply(ctx, svc); err != nil {
			return nil, err
		}
	}
	got, _ := e.Reg.Get(name)
	return got, nil
}

func (e *Engine) containers(ctx context.Context) []dockerclient.Container {
	cs, err := e.DK.Containers(ctx, true)
	if err != nil {
		return nil
	}
	return cs
}

func (e *Engine) containerState(cs []dockerclient.Container, target string) string {
	if target == "" {
		return ""
	}
	anyMatch := false
	running := false
	for _, c := range cs {
		match := c.Name() == target ||
			(len(c.ID) >= 12 && c.ID[:12] == target) ||
			c.Labels["com.docker.compose.service"] == target
		if !match {
			continue
		}
		anyMatch = true
		if c.State == "running" {
			running = true
			break
		}
	}
	if running {
		return "running"
	}
	if anyMatch {
		return "stopped"
	}
	return "missing"
}

func probeHTTPS(ctx context.Context, hostname string) (int, string) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+hostname+"/", nil)
	if err != nil {
		return 0, err.Error()
	}
	client := &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, shortErr(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, ""
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.Index(s, ": "); i > 0 {
		s = s[i+2:]
	}
	if len(s) > 160 {
		return s[:160] + "..."
	}
	return s
}

func hasKey[T any](m map[string]T, k string) bool {
	_, ok := m[strings.ToLower(k)]
	return ok
}

func stringToLower(s string) string { return strings.ToLower(s) }
