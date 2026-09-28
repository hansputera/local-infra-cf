package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"portal/internal/cfclient"
	"portal/internal/config"
	"portal/internal/engine"
	"portal/internal/registry"
	"portal/internal/stackgen"
)

type Server struct {
	Eng  *engine.Engine
	Cfg  config.Config
	tmpl *template.Template

	mu         sync.Mutex
	composeOut string
}

func New(cfg config.Config, eng *engine.Engine) (*Server, error) {
	t, err := template.New("").Funcs(template.FuncMap{
		"shortID": shortID,
	}).ParseFS(FS, "templates/*")
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	return &Server{Eng: eng, Cfg: cfg, tmpl: t}, nil
}

type baseData struct {
	Title    string
	Active   string
	Domain   string
	FlashOK  string
	FlashErr string
}

func (s *Server) base(r *http.Request, title, active string) baseData {
	q := r.URL.Query()
	return baseData{
		Title:    title,
		Active:   active,
		Domain:   s.Cfg.PublicDomain,
		FlashOK:  q.Get("ok"),
		FlashErr: q.Get("err"),
	}
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "template failed: "+err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request, to string) {
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func flashOK(r *http.Request, msg string) string  { return r.URL.Path + "?ok=" + url.QueryEscape(msg) }
func flashErr(r *http.Request, msg string) string { return r.URL.Path + "?err=" + url.QueryEscape(msg) }

func writeJSON(w http.ResponseWriter, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", s.dashboard)
	mux.HandleFunc("GET /services", s.servicesList)
	mux.HandleFunc("GET /services/new", s.formNew)
	mux.HandleFunc("POST /services", s.createService)
	mux.HandleFunc("GET /services/{name}/edit", s.formEdit)
	mux.HandleFunc("POST /services/{name}", s.updateService)
	mux.HandleFunc("POST /services/{name}/reconcile", s.reconcileService)
	mux.HandleFunc("POST /services/{name}/delete", s.deleteService)
	mux.HandleFunc("POST /adopt", s.adopt)

	mux.HandleFunc("GET /tunnels", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/nodes", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /nodes", s.nodesPage)
	mux.HandleFunc("POST /nodes/add", s.addNodeWizard)
	mux.HandleFunc("POST /nodes/claim", s.claimNode)
	mux.HandleFunc("POST /nodes/{id}/unbind", s.unbindNode)
	mux.HandleFunc("POST /nodes/{id}/rebind", s.rebindNode)
	mux.HandleFunc("POST /tunnels/create", s.createTunnel)
	mux.HandleFunc("POST /tunnels/{id}/delete", s.deleteTunnel)
	mux.HandleFunc("GET /api/nodes", s.apiNodes)
	mux.HandleFunc("GET /stacks", s.stacks)
	mux.HandleFunc("POST /stacks", s.createStack)
	mux.HandleFunc("POST /stacks/{name}/up", s.stackUp)
	mux.HandleFunc("POST /stacks/{name}/stop", s.stackStop)
	mux.HandleFunc("POST /stacks/{name}/delete", s.stackDelete)

	mux.HandleFunc("GET /api/report", s.apiReport)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	static, _ := fs.Sub(FS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	return mux
}

// ---- dashboard ----

type dashData struct {
	baseData
	Report        engine.Report
	DriftCount    []engine.ServiceStatus
	Counts        struct{ Public, Private, Drift int }
	Conns         int
	TunnelIDShort string
	Origin        string
	PendingSelf   bool
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	d := dashData{baseData: s.base(r, "Dashboard", "dash"), Origin: s.Cfg.PublicOrigin}
	d.TunnelIDShort = shortID(s.Cfg.CFTunnelID)
	selfHost := "portal." + s.Cfg.PublicDomain

	report, err := s.Eng.Status(ctx, true)
	if err != nil {
		d.FlashErr = "Failed to read status: " + err.Error()
	}
	// The portal's own hostname is deliberately not registered yet: waiting for CF Access.
	kept := report.Orphans[:0]
	for _, o := range report.Orphans {
		if strings.EqualFold(o.Hostname, selfHost) {
			d.PendingSelf = true
			continue
		}
		kept = append(kept, o)
	}
	report.Orphans = kept
	d.Report = report
	for _, svc := range report.Services {
		if svc.Kind == registry.KindPublic {
			d.Counts.Public++
		} else {
			d.Counts.Private++
		}
		if len(svc.Drift) > 0 {
			d.Counts.Drift++
			d.DriftCount = append(d.DriftCount, svc)
		}
	}

	if s.Cfg.CFToken != "" {
		conns, err := s.Eng.CF.TunnelConnections(ctx)
		if err == nil {
			d.Conns = len(conns)
		}
	}
	s.render(w, "dashboard.html", d)
}

// ---- services ----

type servicesData struct {
	baseData
	Services []engine.ServiceStatus
}

func (s *Server) servicesList(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	d := servicesData{baseData: s.base(r, "Hostnames", "services")}
	report, err := s.Eng.Status(ctx, false)
	if err != nil {
		d.FlashErr = "Failed to read status: " + err.Error()
	} else {
		d.Services = report.Services
	}
	s.render(w, "services.html", d)
}

type formData struct {
	baseData
	Service       registry.Service
	IsEdit        bool
	Action        string
	Err           string
	Tunnels       []cfclient.Tunnel
	Nodes         []registry.Node
	InfraID       string
	DefaultOrigin string
}

// tunnelOptions fills the tunnel list for the form. On API failure it falls
// back to the infra tunnel alone so the form still works.
func (s *Server) tunnelOptions(ctx context.Context) []cfclient.Tunnel {
	if list, err := s.Eng.CF.ListTunnels(ctx); err == nil && len(list) > 0 {
		return list
	}
	return []cfclient.Tunnel{{ID: s.Cfg.CFTunnelID, Name: "infra (fallback)", Status: "unknown"}}
}

func (s *Server) formNew(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	d := formData{
		baseData:      s.base(r, "Add hostname", "services"),
		IsEdit:        false,
		Action:        "/services",
		Service:       registry.Service{Kind: registry.KindPublic, TunnelID: s.Cfg.CFTunnelID},
		Tunnels:       s.tunnelOptions(ctx),
		Nodes:         s.Eng.NS.List(),
		InfraID:       s.Cfg.CFTunnelID,
		DefaultOrigin: s.Cfg.PublicOrigin,
	}
	s.render(w, "form.html", d)
}

func (s *Server) formEdit(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	svc, ok := s.Eng.Reg.Get(name)
	if !ok {
		s.redirect(w, r, flashErr(r, "Service "+name+" does not exist"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if svc.TunnelID == "" {
		cp := *svc
		cp.TunnelID = s.Cfg.CFTunnelID
		svc = &cp
	}
	d := formData{
		baseData:      s.base(r, "Edit "+name, "services"),
		IsEdit:        true,
		Action:        "/services/" + name,
		Service:       *svc,
		Tunnels:       s.tunnelOptions(ctx),
		Nodes:         s.Eng.NS.List(),
		InfraID:       s.Cfg.CFTunnelID,
		DefaultOrigin: s.Cfg.PublicOrigin,
	}
	s.render(w, "form.html", d)
}

func parseService(r *http.Request) (registry.Service, error) {
	port, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("port")))
	return registry.Service{
		Name:     strings.ToLower(strings.TrimSpace(r.FormValue("name"))),
		Hostname: strings.ToLower(strings.TrimSpace(r.FormValue("hostname"))),
		Kind:     r.FormValue("kind"),
		Target:   strings.TrimSpace(r.FormValue("target")),
		Port:     port,
		Notes:    strings.TrimSpace(r.FormValue("notes")),
		TunnelID: strings.TrimSpace(r.FormValue("tunnel_id")),
		NodeID:   strings.TrimSpace(r.FormValue("node_id")),
		Origin:   strings.TrimSpace(r.FormValue("origin")),
	}, nil
}

func (s *Server) createService(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.redirect(w, r, flashErr(r, "Form could not be read"))
		return
	}
	svc, err := parseService(r)
	if err != nil {
		s.redirect(w, r, flashErr(r, err.Error()))
		return
	}
	svc.Source = registry.SourcePortal
	if err := s.Eng.Reg.Put(svc); err != nil {
		s.redirect(w, r, flashErr(r, err.Error()))
		return
	}
	if err := s.Eng.Reconcile(r.Context(), svc.Name); err != nil {
		s.redirect(w, r, flashErr(r, "Saved but sync failed: "+err.Error()))
		return
	}
	s.redirect(w, r, flashOK(r, svc.Hostname+" synced (traefik + Cloudflare)"))
}

func (s *Server) updateService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := r.ParseForm(); err != nil {
		s.redirect(w, r, flashErr(r, "Form could not be read"))
		return
	}
	svc, err := parseService(r)
	if err != nil {
		s.redirect(w, r, flashErr(r, err.Error()))
		return
	}
	svc.Name = name
	existing, ok := s.Eng.Reg.Get(name)
	if ok {
		svc.Source = existing.Source
	}
	if err := s.Eng.Reg.Put(svc); err != nil {
		s.redirect(w, r, flashErr(r, err.Error()))
		return
	}
	if err := s.Eng.Reconcile(r.Context(), name); err != nil {
		s.redirect(w, r, flashErr(r, "Saved but sync failed: "+err.Error()))
		return
	}
	s.redirect(w, r, flashOK(r, svc.Hostname+" updated and synced"))
}

func (s *Server) reconcileService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.Eng.Reconcile(r.Context(), name); err != nil {
		s.redirect(w, r, flashErr(r, "Sync of "+name+" failed: "+err.Error()))
		return
	}
	s.redirect(w, r, flashOK(r, "Sync of "+name+" finished"))
}

func (s *Server) deleteService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	warn, err := s.Eng.Remove(r.Context(), name)
	if err != nil {
		s.redirect(w, r, flashErr(r, "Deleting "+name+" failed: "+err.Error()))
		return
	}
	msg := "Service " + name + " removed from the registry, Cloudflare, DNS and traefik"
	if warn != "" {
		msg += ". Warning: " + warn
	}
	s.redirect(w, r, flashOK(r, msg))
}

func (s *Server) adopt(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.redirect(w, r, flashErr(r, "Form could not be read"))
		return
	}
	host := strings.TrimSpace(r.FormValue("hostname"))
	svc, err := s.Eng.Adopt(r.Context(), host)
	if err != nil {
		s.redirect(w, r, flashErr(r, "Adopt failed: "+err.Error()))
		return
	}
	s.redirect(w, r, flashOK(r, host+" adopted as "+svc.Name))
}

// ---- nodes & tunnels ----

type nodesData struct {
	baseData
	View    engine.NodeView
	Origin  string
	InfraID string
}

func (s *Server) nodesPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	d := nodesData{
		baseData: s.base(r, "Tunnel & Node", "nodes"),
		InfraID:  s.Cfg.CFTunnelID,
		Origin:   s.Cfg.PublicOrigin,
	}
	view, err := s.Eng.NodeView(ctx)
	if err != nil {
		d.FlashErr = "Failed to read tunnels/connections: " + err.Error()
		d.View = engine.NodeView{Nodes: []engine.NodeStatus{}, Candidates: []engine.NodeStatus{}, Tunnels: []engine.TunnelInfo{}}
	} else {
		d.View = view
	}
	s.render(w, "nodes.html", d)
}

// ---- add-connector wizard ----

type wizardData struct {
	baseData
	Name       string
	TunnelID   string
	TunnelName string
	Origin     string
	Notes      string
	Token      string
	TokenErr   string
	DockerCmd  string
	BinaryCmd  string
}

func dockerRunCmd(name, token string) string {
	return fmt.Sprintf(`# single container (VPS, Raspberry Pi OS + Docker, mini PC)
docker run -d \
  --name cloudflared-%s \
  --restart unless-stopped \
  cloudflare/cloudflared:latest tunnel --no-autoupdate run \
  --token %s`, name, token)
}

func binaryCmd(token string) string {
	return fmt.Sprintf(`# 1) download the binary (swap arch: amd64 / arm64)
curl -L -o cloudflared \
  https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64
chmod +x cloudflared

# 2) install as a systemd service (Linux) - token = this tunnel's token
sudo ./cloudflared service install %s
sudo systemctl enable --now cloudflared

# alternative without a service (quick test):
./cloudflared tunnel run --token %s`, token, token)
}

// addNodeWizard: step 1 -> instructions page with polling for new connections.
func (s *Server) addNodeWizard(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.redirect(w, r, flashErr(r, "Form could not be read"))
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.FormValue("name")))
	origin := strings.TrimSpace(r.FormValue("origin"))
	notes := strings.TrimSpace(r.FormValue("notes"))
	if name == "" {
		s.redirect(w, r, flashErr(r, "Node name is required"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	tunnelID := strings.TrimSpace(r.FormValue("tunnel_id"))
	tunnelName := ""
	if nt := strings.TrimSpace(r.FormValue("new_tunnel")); nt != "" {
		ti, err := s.Eng.CreateManagedTunnel(ctx, nt, origin)
		if err != nil {
			s.redirect(w, r, flashErr(r, "Creating the tunnel failed: "+err.Error()))
			return
		}
		tunnelID, tunnelName = ti.ID, ti.Name
	} else if tunnelID == "" || tunnelID == "new" {
		s.redirect(w, r, flashErr(r, "Pick a tunnel first"))
		return
	} else {
		if list, err := s.Eng.CF.ListTunnels(ctx); err == nil {
			for _, t := range list {
				if t.ID == tunnelID {
					tunnelName = strings.TrimSpace(t.Name)
				}
			}
		}
		if tunnelName == "" && tunnelID != s.Cfg.CFTunnelID {
			s.redirect(w, r, flashErr(r, "Tunnel "+tunnelID+" not found on the CF account"))
			return
		}
		if tunnelName == "" {
			tunnelName = "infra"
		}
	}

	token, terr := s.Eng.CF.TunnelTokenFor(ctx, tunnelID)
	d := wizardData{
		baseData:   s.base(r, "Connect node", "nodes"),
		Name:       name,
		TunnelID:   tunnelID,
		TunnelName: tunnelName,
		Origin:     origin,
		Notes:      notes,
		Token:      token,
	}
	if terr != nil {
		d.TokenErr = terr.Error()
	}
	d.DockerCmd = dockerRunCmd(name, token)
	d.BinaryCmd = binaryCmd(token)
	s.render(w, "node-instructions.html", d)
}

func wantsJSON(r *http.Request) bool {
	return r.Header.Get("X-Requested-With") == "fetch" || strings.Contains(r.Header.Get("Accept"), "application/json")
}

// claimNode binds a connection candidate (client_id) to a node name.
func (s *Server) claimNode(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.claimFail(w, r, "Form could not be read")
		return
	}
	nd, err := s.Eng.ClaimNode(
		strings.TrimSpace(r.FormValue("client_id")),
		strings.TrimSpace(r.FormValue("tunnel_id")),
		r.FormValue("name"),
		r.FormValue("origin"),
		r.FormValue("notes"),
	)
	if err != nil {
		s.claimFail(w, r, err.Error())
		return
	}
	if wantsJSON(r) {
		writeJSON(w, map[string]any{"ok": true, "name": nd.Name, "client_id": nd.ClientID})
		return
	}
	s.redirect(w, r, flashOK(r, "Node "+nd.Name+" connected ("+shortID(nd.ClientID)+")"))
}

func (s *Server) claimFail(w http.ResponseWriter, r *http.Request, msg string) {
	if wantsJSON(r) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		writeJSON(w, map[string]any{"ok": false, "error": msg})
		return
	}
	s.redirect(w, r, flashErr(r, msg))
}

func (s *Server) unbindNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	nd, err := s.Eng.UnbindNode(id)
	if err != nil {
		s.redirect(w, r, flashErr(r, err.Error()))
		return
	}
	s.redirect(w, r, flashOK(r, "Node "+nd.Name+" ("+shortID(id)+") unbound from the registry"))
}

func (s *Server) rebindNode(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.redirect(w, r, flashErr(r, "Form could not be read"))
		return
	}
	nd, err := s.Eng.RebindNode(r.PathValue("id"), strings.TrimSpace(r.FormValue("client_id")))
	if err != nil {
		s.redirect(w, r, flashErr(r, err.Error()))
		return
	}
	s.redirect(w, r, flashOK(r, "Node "+nd.Name+" now bound to "+shortID(nd.ClientID)))
}

func (s *Server) createTunnel(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.redirect(w, r, flashErr(r, "Form could not be read"))
		return
	}
	ti, err := s.Eng.CreateManagedTunnel(r.Context(), r.FormValue("name"), strings.TrimSpace(r.FormValue("origin")))
	if err != nil {
		s.redirect(w, r, flashErr(r, err.Error()))
		return
	}
	s.redirect(w, r, flashOK(r, "Tunnel "+ti.Name+" created ("+shortID(ti.ID)+") — pick it in the Add connector form"))
}

func (s *Server) deleteTunnel(w http.ResponseWriter, r *http.Request) {
	if err := s.Eng.DeleteManagedTunnel(r.Context(), r.PathValue("id")); err != nil {
		s.redirect(w, r, flashErr(r, err.Error()))
		return
	}
	s.redirect(w, r, flashOK(r, "Tunnel "+shortID(r.PathValue("id"))+" deleted"))
}

func (s *Server) apiNodes(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	view, err := s.Eng.NodeView(ctx)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}
	writeJSON(w, view)
}

// ---- stacks ----

type stackInfo struct {
	Name     string
	Image    string
	Included bool
	Present  bool
}

type stacksData struct {
	baseData
	Stacks     []stackInfo
	Err        string
	ComposeOut string
}

var imageRe = regexp.MustCompile(`(?m)^\s+image:\s*(\S+)`)

func (s *Server) stacks(w http.ResponseWriter, r *http.Request) {
	d := stacksData{baseData: s.base(r, "Stack", "stacks")}
	includes, err := stackgen.Includes(s.Cfg.InfraRoot)
	if err != nil {
		d.Err = "Failed to read the root compose.yaml: " + err.Error()
		s.render(w, "stacks.html", d)
		return
	}
	inSet := map[string]bool{}
	for _, inc := range includes {
		inSet[inc] = true
	}
	entries, _ := os.ReadDir(filepath.Join(s.Cfg.InfraRoot, "stacks"))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rel := "stacks/" + e.Name() + "/compose.yaml"
		info := stackInfo{Name: e.Name(), Present: false}
		if raw, err := os.ReadFile(filepath.Join(s.Cfg.InfraRoot, rel)); err == nil {
			info.Present = true
			if m := imageRe.FindSubmatch(raw); m != nil {
				info.Image = string(m[1])
			}
		}
		if inSet[rel] {
			info.Included = true
		}
		// the root compose may include this file under another name
		for _, inc := range includes {
			if strings.HasPrefix(inc, "stacks/"+e.Name()+"/") {
				info.Included = true
			}
		}
		d.Stacks = append(d.Stacks, info)
	}
	s.mu.Lock()
	d.ComposeOut = s.composeOut
	s.mu.Unlock()
	s.render(w, "stacks.html", d)
}

func parseStack(r *http.Request) (stackgen.Spec, error) {
	if err := r.ParseForm(); err != nil {
		return stackgen.Spec{}, err
	}
	port, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("port")))
	var vols []string
	for _, line := range strings.Split(r.FormValue("volumes"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			vols = append(vols, line)
		}
	}
	return stackgen.Spec{
		Name:     strings.ToLower(strings.TrimSpace(r.FormValue("name"))),
		Image:    strings.TrimSpace(r.FormValue("image")),
		Port:     port,
		Hostname: strings.ToLower(strings.TrimSpace(r.FormValue("hostname"))),
		Profile:  r.FormValue("profile"),
		Mem:      strings.TrimSpace(r.FormValue("mem")),
		CPUs:     strings.TrimSpace(r.FormValue("cpus")),
		Volumes:  vols,
	}, nil
}

func (s *Server) createStack(w http.ResponseWriter, r *http.Request) {
	spec, err := parseStack(r)
	if err != nil {
		s.redirect(w, r, flashErr(r, "Form could not be read"))
		return
	}
	rel, err := stackgen.Write(s.Cfg.InfraRoot, spec)
	if err != nil {
		s.redirect(w, r, flashErr(r, err.Error()))
		return
	}
	msg := "File " + rel + " created"
	if spec.Hostname != "" {
		svc := registry.Service{
			Name: spec.Name, Hostname: spec.Hostname, Kind: registry.KindPublic,
			Target: spec.Name, Port: spec.Port, Source: registry.SourcePortal,
			Notes: "created by the stack generator",
		}
		if err := s.Eng.Reg.Put(svc); err == nil {
			if err := s.Eng.Reconcile(r.Context(), spec.Name); err != nil {
				msg += ", but hostname sync failed: " + err.Error()
			} else {
				msg += ", hostname " + spec.Hostname + " synced"
			}
		} else {
			msg += ", hostname registration failed: " + err.Error()
		}
	}
	s.redirect(w, r, flashOK(r, msg))
}

func (s *Server) composeAction(w http.ResponseWriter, r *http.Request, name string, args ...string) {
	full := append([]string{"--profile", "lite", "--profile", "lab"}, args...)
	out, err := stackgen.RunCompose(s.Cfg.InfraRoot, full...)
	s.mu.Lock()
	if out != "" {
		s.composeOut = strings.TrimSpace(out)
	}
	s.mu.Unlock()
	if err != nil {
		s.redirect(w, r, flashErr(r, err.Error()))
		return
	}
	s.redirect(w, r, flashOK(r, "docker compose "+strings.Join(args, " ")+" finished"))
}

func (s *Server) stackUp(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.composeAction(w, r, name, "up", "-d", name)
}

func (s *Server) stackStop(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.composeAction(w, r, name, "stop", name)
}

func (s *Server) stackDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if out, err := stackgen.RunCompose(s.Cfg.InfraRoot, "--profile", "lite", "--profile", "lab", "rm", "-sf", name); err != nil {
		s.redirect(w, r, flashErr(r, err.Error()))
		return
	} else if out != "" {
		s.mu.Lock()
		s.composeOut = strings.TrimSpace(out)
		s.mu.Unlock()
	}
	if err := stackgen.Delete(s.Cfg.InfraRoot, name); err != nil {
		s.redirect(w, r, flashErr(r, "Deleting the file failed: "+err.Error()))
		return
	}
	msg := "Stack " + name + " stopped and its file deleted"
	if _, ok := s.Eng.Reg.Get(name); ok {
		if _, err := s.Eng.Remove(r.Context(), name); err != nil {
			msg += ". Service registry entry not removed: " + err.Error()
		} else {
			msg += ", the related hostname was released from Cloudflare too"
		}
	}
	s.redirect(w, r, flashOK(r, msg))
}

// ---- api ----

func (s *Server) apiReport(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	report, err := s.Eng.Status(ctx, true)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}
	writeJSON(w, report)
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
