package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"portal/internal/cfclient"
	"portal/internal/registry"
)

// Node status: connected = live and registered; new = live but unregistered
// (claim candidate); rebind = registered but its client_id vanished while a
// look-alike replacement exists (same origin IP); offline = registered, none live.
const (
	NodeConnected = "connected"
	NodeNew       = "new"
	NodeRebind    = "rebind"
	NodeOffline   = "offline"
)

type NodeStatus struct {
	ClientID   string    `json:"client_id"`
	ShortID    string    `json:"short_id"`
	TunnelID   string    `json:"tunnel_id"`
	TunnelName string    `json:"tunnel_name,omitempty"`
	Name       string    `json:"name,omitempty"`
	Origin     string    `json:"origin,omitempty"`
	Notes      string    `json:"notes,omitempty"`
	Version    string    `json:"version,omitempty"`
	Arch       string    `json:"arch,omitempty"`
	Colos      []string  `json:"colos,omitempty"`
	OriginIPs  []string  `json:"origin_ips,omitempty"`
	RunAt      time.Time `json:"run_at,omitempty"`
	LastSeen   time.Time `json:"last_seen,omitempty"`
	Registered bool      `json:"registered"`
	Status     string    `json:"status"`
	// RebindTo: suggested replacement client_id (only for status rebind).
	RebindTo string `json:"rebind_to,omitempty"`
}

type TunnelInfo struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	Infra      bool      `json:"infra"`
	Managed    bool      `json:"managed"`
	Connectors int       `json:"connectors"`
	Origin     string    `json:"origin,omitempty"`
}

type NodeView struct {
	Nodes      []NodeStatus `json:"nodes"`      // registered in the node store
	Candidates []NodeStatus `json:"candidates"` // live but not registered yet
	Tunnels    []TunnelInfo `json:"tunnels"`
	Warnings   []string     `json:"warnings,omitempty"`
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// NodeView merges live connections (all relevant tunnels) with the node store.
// Source of truth for node names = the local store; CF keeps no names.
func (e *Engine) NodeView(ctx context.Context) (NodeView, error) {
	view := NodeView{Nodes: []NodeStatus{}, Candidates: []NodeStatus{}, Tunnels: []TunnelInfo{}}

	tunnels, err := e.CF.ListTunnels(ctx)
	if err != nil {
		return view, fmt.Errorf("list tunnels: %w", err)
	}

	// live groups per tunnel
	type live struct {
		group      cfclient.ConnGroup
		tunnelID   string
		tunnelName string
	}
	liveByClient := map[string]live{}
	tunnelByID := map[string]cfclient.Tunnel{}
	for _, t := range tunnels {
		tunnelByID[t.ID] = t
	}
	// make sure the infra tunnel is covered even when it is missing from the list
	if _, ok := tunnelByID[e.Cfg.CFTunnelID]; !ok {
		view.Warnings = append(view.Warnings, "infra tunnel "+short(e.Cfg.CFTunnelID)+" is missing from the CF list (deleted?)")
	}
	for id, t := range tunnelByID {
		groups, err := e.CF.ConnectionsForTunnel(ctx, id)
		if err != nil {
			view.Warnings = append(view.Warnings, "failed to read connections for "+t.Name+": "+err.Error())
			continue
		}
		for _, g := range groups {
			if g.ID == "" {
				continue
			}
			liveByClient[g.ID] = live{group: g, tunnelID: id, tunnelName: t.Name}
		}
	}

	// tunnel info
	for _, t := range tunnels {
		info := TunnelInfo{
			ID: t.ID, Name: strings.TrimSpace(t.Name), Status: t.Status, CreatedAt: t.CreatedAt,
			Infra: t.ID == e.Cfg.CFTunnelID,
		}
		if mt, ok := e.NS.GetTunnel(t.ID); ok {
			info.Managed = mt.Managed
			info.Origin = mt.Origin
		}
		for _, lv := range liveByClient {
			if lv.tunnelID == t.ID {
				info.Connectors++
			}
		}
		view.Tunnels = append(view.Tunnels, info)
	}
	sort.Slice(view.Tunnels, func(i, j int) bool {
		if view.Tunnels[i].Infra != view.Tunnels[j].Infra {
			return view.Tunnels[i].Infra
		}
		return view.Tunnels[i].Name < view.Tunnels[j].Name
	})

	// candidates = live connections not registered yet
	claimed := map[string]bool{}
	for _, nd := range e.NS.List() {
		claimed[nd.ClientID] = true
	}
	for id, lv := range liveByClient {
		if !claimed[id] {
			view.Candidates = append(view.Candidates, e.nodeStatusFromLive(lv.group, lv.tunnelID, lv.tunnelName, NodeNew))
		}
	}
	sort.Slice(view.Candidates, func(i, j int) bool { return view.Candidates[i].RunAt.Before(view.Candidates[j].RunAt) })

	// registered nodes: connected / rebind / offline
	for _, nd := range e.NS.List() {
		st := NodeStatus{
			ClientID: nd.ClientID, ShortID: short(nd.ClientID),
			TunnelID: nd.TunnelID, Name: nd.Name, Origin: nd.Origin, Notes: nd.Notes,
			LastSeen: nd.LastSeen, Registered: true, Status: NodeOffline,
		}
		if t, ok := tunnelByID[nd.TunnelID]; ok {
			st.TunnelName = strings.TrimSpace(t.Name)
		}
		if lv, ok := liveByClient[nd.ClientID]; ok {
			st = e.nodeStatusFromLive(lv.group, lv.tunnelID, lv.tunnelName, NodeConnected)
			st.Name, st.Origin, st.Notes, st.LastSeen, st.Registered = nd.Name, nd.Origin, nd.Notes, nd.LastSeen, true
			st.ShortID = short(nd.ClientID)
			// remember last origin IPs: used to detect a rebind after restart
			e.NS.RememberIPs(nd.ClientID, lv.group.OriginIPs())
			e.NS.Touch(nd.ClientID, time.Now().UTC())
		} else {
			for id, lv := range liveByClient {
				if claimed[id] {
					continue
				}
				if shareIP(nd.LastOriginIPs, lv.group.OriginIPs()) {
					st.Status = NodeRebind
					st.RebindTo = id
					st.Colos = lv.group.Colos()
					st.OriginIPs = lv.group.OriginIPs()
					break
				}
			}
		}
		view.Nodes = append(view.Nodes, st)
	}
	sort.Slice(view.Nodes, func(i, j int) bool { return view.Nodes[i].Name < view.Nodes[j].Name })
	return view, nil
}

func shareIP(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x != "" && x == y {
				return true
			}
		}
	}
	return false
}

func (e *Engine) nodeStatusFromLive(g cfclient.ConnGroup, tunnelID, tunnelName, status string) NodeStatus {
	return NodeStatus{
		ClientID: g.ID, ShortID: short(g.ID),
		TunnelID: tunnelID, TunnelName: strings.TrimSpace(tunnelName),
		Version: g.Version, Arch: g.Arch, Colos: g.Colos(), OriginIPs: g.OriginIPs(),
		RunAt: g.RunAt, Status: status,
	}
}

// ClaimNode binds a live client_id to a node name in the store.
func (e *Engine) ClaimNode(clientID, tunnelID, name, origin, notes string) (registry.Node, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	nd := registry.Node{
		ClientID: clientID, TunnelID: tunnelID, Name: name,
		Origin: strings.TrimSpace(origin), Notes: strings.TrimSpace(notes),
	}
	if err := e.NS.Put(nd); err != nil {
		return registry.Node{}, err
	}
	got, _ := e.NS.Get(clientID)
	return got, nil
}

func (e *Engine) UnbindNode(clientID string) (registry.Node, error) {
	nd, ok := e.NS.Get(clientID)
	if !ok {
		return registry.Node{}, fmt.Errorf("client_id %s not in the registry", short(clientID))
	}
	return nd, e.NS.Delete(clientID)
}

// RebindNode moves the old node name/origin to a new client_id (a cloudflared
// restart usually changes the client_id).
func (e *Engine) RebindNode(oldClientID, newClientID string) (registry.Node, error) {
	nd, ok := e.NS.Get(oldClientID)
	if !ok {
		return registry.Node{}, fmt.Errorf("old node %s not found", short(oldClientID))
	}
	if _, ok := e.NS.Get(newClientID); ok {
		return registry.Node{}, fmt.Errorf("client_id %s is already registered", short(newClientID))
	}
	if err := e.NS.Delete(oldClientID); err != nil {
		return registry.Node{}, err
	}
	nd.ClientID = newClientID
	nd.LastSeen = time.Time{}
	if err := e.NS.Put(nd); err != nil {
		return registry.Node{}, err
	}
	// move any services that point at the old node so they do not become drift
	for _, svc := range e.Reg.List() {
		if svc.NodeID != oldClientID {
			continue
		}
		cp := *svc
		cp.NodeID = newClientID
		if err := e.Reg.Put(cp); err != nil {
			return nd, fmt.Errorf("service %s could not be updated: %w", svc.Name, err)
		}
	}
	got, _ := e.NS.Get(newClientID)
	return got, nil
}

// ---- tunnel managed (5C) ----

// CreateManagedTunnel creates a new remote-config tunnel and marks it as
// portal-owned (deletable from the portal).
func (e *Engine) CreateManagedTunnel(ctx context.Context, name, origin string) (TunnelInfo, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return TunnelInfo{}, fmt.Errorf("tunnel name is empty")
	}
	t, err := e.CF.CreateTunnel(ctx, name)
	if err != nil {
		return TunnelInfo{}, fmt.Errorf("create tunnel: %w", err)
	}
	if t.ID == "" {
		return TunnelInfo{}, fmt.Errorf("CF response carries no tunnel id")
	}
	mt := registry.ManagedTunnel{ID: t.ID, Name: name, Managed: true, Origin: strings.TrimSpace(origin)}
	if err := e.NS.PutTunnel(mt); err != nil {
		return TunnelInfo{}, err
	}
	return TunnelInfo{ID: t.ID, Name: name, Status: t.Status, CreatedAt: t.CreatedAt, Managed: true, Infra: false}, nil
}

// DeleteManagedTunnel deletes a tunnel. Guard: the infra tunnel and tunnels
// not created by the portal are never deletable; services pointing at the
// tunnel must be moved or deleted first.
func (e *Engine) DeleteManagedTunnel(ctx context.Context, tid string) error {
	if tid == e.Cfg.CFTunnelID {
		return fmt.Errorf("infra tunnel (%s) cannot be deleted from the portal", short(tid))
	}
	mt, ok := e.NS.GetTunnel(tid)
	if !ok || !mt.Managed {
		return fmt.Errorf("tunnel %s is not marked portal-managed", short(tid))
	}
	var users []string
	for _, svc := range e.Reg.List() {
		if svc.Kind == registry.KindPublic && e.tunnelOf(*svc) == tid {
			users = append(users, svc.Hostname)
		}
	}
	if len(users) > 0 {
		return fmt.Errorf("tunnel still in use by: %s — move or delete those hostnames first", strings.Join(users, ", "))
	}
	if err := e.CF.DeleteTunnel(ctx, tid); err != nil {
		return fmt.Errorf("delete tunnel at CF: %w", err)
	}
	if err := e.NS.DeleteTunnel(tid); err != nil {
		return err
	}
	for _, nd := range e.NS.List() {
		if nd.TunnelID == tid {
			_ = e.NS.Delete(nd.ClientID)
		}
	}
	return nil
}
