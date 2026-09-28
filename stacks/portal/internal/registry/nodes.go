package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// A Node = one cloudflared process acknowledged by the portal. Cloudflare has
// no concept of "connector name", so the name lives only in this file.
// Key = client_id (the connection group UUID), which can change whenever
// cloudflared restarts => the engine keeps a rebind mechanism.
type Node struct {
	ClientID  string    `json:"client_id"`
	TunnelID  string    `json:"tunnel_id"`
	Name      string    `json:"name"`
	Origin    string    `json:"origin,omitempty"`
	Notes     string    `json:"notes,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
	// LastOriginIPs: egress IPs at last connect, used to detect a rebind
	// when the client_id changes after a cloudflared restart.
	LastOriginIPs []string `json:"last_origin_ips,omitempty"`
}

// ManagedTunnel marks a tunnel created/acknowledged by the portal. The infra
// tunnel and any tunnel the portal does not know must never be deletable from
// the portal.
type ManagedTunnel struct {
	ID      string    `json:"id"`
	Name    string    `json:"name,omitempty"`
	Managed bool      `json:"managed"` // true = created by the portal => deletable
	AddedAt time.Time `json:"added_at"`
	Origin  string    `json:"origin,omitempty"` // default origin for this tunnel
}

type nodesFile struct {
	Version int                       `json:"version"`
	Nodes   map[string]*Node          `json:"nodes"`
	Tunnels map[string]*ManagedTunnel `json:"tunnels"`
}

// NodeStore holds nodes + managed tunnels in one file, separate from the
// service registry so the old registry.json format stays untouched.
type NodeStore struct {
	mu   sync.Mutex
	path string
	data nodesFile
}

var nodeNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func OpenNodes(path string) (*NodeStore, error) {
	ns := &NodeStore{path: path, data: nodesFile{
		Version: 1,
		Nodes:   map[string]*Node{},
		Tunnels: map[string]*ManagedTunnel{},
	}}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ns, ns.Save()
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, &ns.data); err != nil {
		return nil, fmt.Errorf("nodes file corrupted: %w", err)
	}
	if ns.data.Nodes == nil {
		ns.data.Nodes = map[string]*Node{}
	}
	if ns.data.Tunnels == nil {
		ns.data.Tunnels = map[string]*ManagedTunnel{}
	}
	return ns, nil
}

func (n *NodeStore) Save() error {
	if err := os.MkdirAll(filepath.Dir(n.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(n.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := n.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, n.path)
}

func ValidateNode(nd Node) error {
	if !nodeNameRe.MatchString(nd.Name) {
		return errors.New("node name: lowercase letters, digits and hyphens only")
	}
	if nd.ClientID == "" {
		return errors.New("client_id is required")
	}
	if nd.TunnelID == "" {
		return errors.New("node must belong to a tunnel")
	}
	return nil
}

func (n *NodeStore) List() []Node {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]Node, 0, len(n.data.Nodes))
	for _, nd := range n.data.Nodes {
		out = append(out, *nd)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (n *NodeStore) Get(clientID string) (Node, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	nd, ok := n.data.Nodes[clientID]
	if !ok {
		return Node{}, false
	}
	return *nd, true
}

func (n *NodeStore) FindByName(name string) (Node, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, nd := range n.data.Nodes {
		if nd.Name == name {
			return *nd, true
		}
	}
	return Node{}, false
}

func (n *NodeStore) Put(nd Node) error {
	if err := ValidateNode(nd); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if ex, ok := n.data.Nodes[nd.ClientID]; ok {
		nd.CreatedAt = ex.CreatedAt
	} else if nd.CreatedAt.IsZero() {
		nd.CreatedAt = time.Now().UTC()
	}
	for id, other := range n.data.Nodes {
		if id != nd.ClientID && other.Name == nd.Name {
			return fmt.Errorf("node name %q is already taken", nd.Name)
		}
	}
	nd.LastSeen = time.Now().UTC()
	cp := nd
	n.data.Nodes[nd.ClientID] = &cp
	return n.Save()
}

func (n *NodeStore) RememberIPs(clientID string, ips []string) {
	n.mu.Lock()
	nd, ok := n.data.Nodes[clientID]
	changed := false
	if ok && !sameStrings(nd.LastOriginIPs, ips) {
		nd.LastOriginIPs = ips
		changed = true
	}
	n.mu.Unlock()
	if changed {
		_ = n.Save()
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Touch updates last_seen; skipped when the delta is under 60s so page
// polling does not keep writing the file.
func (n *NodeStore) Touch(clientID string, at time.Time) {
	n.mu.Lock()
	nd, ok := n.data.Nodes[clientID]
	changed := false
	if ok && at.Sub(nd.LastSeen) > time.Minute {
		nd.LastSeen = at
		changed = true
	}
	n.mu.Unlock()
	if changed {
		_ = n.Save()
	}
}

func (n *NodeStore) Delete(clientID string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.data.Nodes[clientID]; !ok {
		return fmt.Errorf("node %q not in the registry", clientID)
	}
	delete(n.data.Nodes, clientID)
	return n.Save()
}

// ---- managed tunnels ----

func (n *NodeStore) Tunnels() []ManagedTunnel {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]ManagedTunnel, 0, len(n.data.Tunnels))
	for _, t := range n.data.Tunnels {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AddedAt.Before(out[j].AddedAt) })
	return out
}

func (n *NodeStore) GetTunnel(id string) (ManagedTunnel, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	t, ok := n.data.Tunnels[id]
	if !ok {
		return ManagedTunnel{}, false
	}
	return *t, true
}

func (n *NodeStore) PutTunnel(t ManagedTunnel) error {
	if t.ID == "" {
		return errors.New("tunnel id is required")
	}
	if t.AddedAt.IsZero() {
		t.AddedAt = time.Now().UTC()
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	cp := t
	n.data.Tunnels[t.ID] = &cp
	return n.Save()
}

func (n *NodeStore) DeleteTunnel(id string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.data.Tunnels[id]; !ok {
		return fmt.Errorf("tunnel %q is not portal-managed", id)
	}
	delete(n.data.Tunnels, id)
	return n.Save()
}
