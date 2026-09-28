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

// Node = satu proses cloudflared yang diakui portal. Cloudflare tidak punya
// konsep "nama connector", jadi nama & asal-usulnya hidup di file ini saja.
// Kunci = client_id (UUID grup koneksi), bisa berubah saat cloudflared
// restart => ada mekanisme rebind di engine.
type Node struct {
	ClientID  string    `json:"client_id"`
	TunnelID  string    `json:"tunnel_id"`
	Name      string    `json:"name"`
	Origin    string    `json:"origin,omitempty"`
	Notes     string    `json:"notes,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
	// LastOriginIPs: egress ip saat terakhir terhubung, dipakai deteksi rebind
	// bila client_id berubah setelah cloudflared restart.
	LastOriginIPs []string `json:"last_origin_ips,omitempty"`
}

// ManagedTunnel menandai tunnel yang dibuat/diakui portal. Tunnel infra dan
// tunnel yang tidak dikenal portal tidak pernah boleh dihapus lewat portal.
type ManagedTunnel struct {
	ID      string    `json:"id"`
	Name    string    `json:"name,omitempty"`
	Managed bool      `json:"managed"` // true = dibuat portal => boleh dihapus
	AddedAt time.Time `json:"added_at"`
	Origin  string    `json:"origin,omitempty"` // default origin utk tunnel ini
}

type nodesFile struct {
	Version int                       `json:"version"`
	Nodes   map[string]*Node          `json:"nodes"`
	Tunnels map[string]*ManagedTunnel `json:"tunnels"`
}

// NodeStore menyimpan node + tunnel managed dalam satu file terpisah dari
// registry service supaya format registry.json lama tidak berubah.
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
		return nil, fmt.Errorf("file node rusak: %w", err)
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
		return errors.New("nama node: huruf kecil, angka, tanda hubung saja")
	}
	if nd.ClientID == "" {
		return errors.New("client_id wajib diisi")
	}
	if nd.TunnelID == "" {
		return errors.New("node harus menempel pada satu tunnel")
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
			return fmt.Errorf("nama node %q sudah dipakai", nd.Name)
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

// Touch memperbarui last_seen; diskip bila perubahan < 60 dtk supaya polling
// halaman tidak menulis file terus-menerus.
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
		return fmt.Errorf("node %q tidak ada di registry", clientID)
	}
	delete(n.data.Nodes, clientID)
	return n.Save()
}

// ---- tunnel managed ----

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
		return errors.New("id tunnel wajib diisi")
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
		return fmt.Errorf("tunnel %q tidak dikelola portal", id)
	}
	delete(n.data.Tunnels, id)
	return n.Save()
}
