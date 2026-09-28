package stackgen

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type Spec struct {
	Name     string // service key in compose + folder name
	Image    string // e.g. nginx:alpine
	Port     int    // container port served (0 = no port)
	Hostname string // public hostname (optional, writes the traefik label)
	Mem      string // e.g. 128m
	CPUs     string // e.g. 0.25
	Profile  string // lab | lite
	Env      map[string]string
	Volumes  []string // host:container
	Restart  string   // default unless-stopped
	Networks []string // default [proxy]
	Notes    string
}

var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func (s Spec) validate() error {
	if !nameRe.MatchString(s.Name) {
		return errors.New("stack name: lowercase letters, digits, hyphens")
	}
	if strings.TrimSpace(s.Image) == "" {
		return errors.New("image is required")
	}
	if s.Profile != "lab" && s.Profile != "lite" && s.Profile != "" {
		return errors.New("profile must be lab or lite")
	}
	if s.Profile == "" {
		s.Profile = "lab"
	}
	if s.Port < 0 || s.Port > 65535 {
		return errors.New("invalid port")
	}
	return nil
}

func (s Spec) normalized() Spec {
	if s.Profile == "" {
		s.Profile = "lab"
	}
	if s.Restart == "" {
		s.Restart = "unless-stopped"
	}
	if len(s.Networks) == 0 {
		s.Networks = []string{"proxy"}
	}
	if s.Mem == "" {
		s.Mem = "128m"
	}
	if s.CPUs == "" {
		s.CPUs = "0.25"
	}
	return s
}

// YAML is written by hand: only the fields we emit, no external library
// dependency, and a stable order so diffs stay minimal.
func (s Spec) compose() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# written automatically by the portal (Infra Manager)\nx-logging: &default-logging\n")
	fmt.Fprintf(&b, "  driver: json-file\n  options:\n    max-size: \"10m\"\n    max-file: \"3\"\n\n")
	fmt.Fprintf(&b, "services:\n")
	fmt.Fprintf(&b, "  %s:\n", s.Name)
	fmt.Fprintf(&b, "    image: %s\n", s.Image)
	if s.Profile != "lite" {
		fmt.Fprintf(&b, "    profiles: [%q]\n", s.Profile)
	} else {
		// the lite profile always joins in on `./mode lite`
		fmt.Fprintf(&b, "    profiles: [\"lite\", \"lab\"]\n")
	}
	fmt.Fprintf(&b, "    restart: %s\n", s.Restart)
	if len(s.Volumes) > 0 {
		b.WriteString("    volumes:\n")
		for _, v := range s.Volumes {
			fmt.Fprintf(&b, "      - %s\n", yq(v))
		}
	}
	if len(s.Env) > 0 {
		b.WriteString("    environment:\n")
		keys := make([]string, 0, len(s.Env))
		for k := range s.Env {
			keys = append(keys, k)
		}
		sortStrings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "      %s: %q\n", k, s.Env[k])
		}
	}
	b.WriteString("    networks: [" + strings.Join(quoteAll(s.Networks), ", ") + "]\n")
	// No traefik labels: the portal writes routing through
	// traefik/dynamic/svc-*.yml so there is a single source of truth (docker
	// provider labels would create duplicate routers).
	fmt.Fprintf(&b, "    mem_limit: %s\n", s.Mem)
	fmt.Fprintf(&b, "    cpus: %s\n", s.CPUs)
	fmt.Fprintf(&b, "    logging: *default-logging\n")
	return b.String()
}

func yq(s string) string {
	return fmt.Sprintf("%q", s)
}

func quoteAll(in []string) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = fmt.Sprintf("%q", v)
	}
	return out
}

func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

func (s Spec) RelPath() string { return filepath.Join("stacks", s.Name, "compose.yaml") }

// Write creates stacks/<name>/compose.yaml and makes sure the root includes it.
func Write(root string, spec Spec) (string, error) {
	if err := spec.validate(); err != nil {
		return "", err
	}
	spec = spec.normalized()
	dir := filepath.Join(root, "stacks", spec.Name)
	if _, err := os.Stat(dir); err == nil {
		if _, err := os.Stat(filepath.Join(dir, "compose.yaml")); err == nil {
			return "", fmt.Errorf("stack %q already exists, delete it first or use another name", spec.Name)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	rel := spec.RelPath()
	path := filepath.Join(root, rel)
	if err := os.WriteFile(path, []byte(spec.compose()), 0o644); err != nil {
		return "", err
	}
	if err := EnsureInclude(root, rel); err != nil {
		return "", fmt.Errorf("write include: %w", err)
	}
	return rel, nil
}

func Delete(root, name string) error {
	rel := filepath.Join("stacks", name, "compose.yaml")
	if err := RemoveInclude(root, rel); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(root, rel)); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = os.Remove(filepath.Join(root, "stacks", name))
	return nil
}

func EnsureInclude(root, rel string) error {
	rootCompose := filepath.Join(root, "compose.yaml")
	raw, err := os.ReadFile(rootCompose)
	if err != nil {
		return err
	}
	norm := filepath.ToSlash(rel)
	if strings.Contains(string(raw), "- "+norm) {
		return nil
	}
	lines := strings.Split(string(raw), "\n")
	entry := "  - " + norm

	idx := -1
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "include:" && !strings.HasPrefix(ln, " ") && !strings.HasPrefix(ln, "\t") {
			idx = i
			break
		}
	}
	if idx >= 0 {
		// insertion point = after the last `- ` entry, before the blank line
		// that closes the block, so the original formatting stays intact.
		last := idx
		for j := idx + 1; j < len(lines); j++ {
			ln := lines[j]
			t := strings.TrimSpace(ln)
			if t == "" || strings.HasPrefix(t, "#") {
				continue
			}
			indented := strings.HasPrefix(ln, " ") || strings.HasPrefix(ln, "\t")
			if !indented {
				break
			}
			if strings.HasPrefix(t, "- ") {
				last = j
			}
		}
		out := make([]string, 0, len(lines)+1)
		out = append(out, lines[:last+1]...)
		out = append(out, entry)
		out = append(out, lines[last+1:]...)
		return writeLines(rootCompose, out)
	}

	// no include: create a new block after the `name:` line
	out := make([]string, 0, len(lines)+4)
	added := false
	for _, ln := range lines {
		out = append(out, ln)
		if !added && strings.HasPrefix(strings.TrimSpace(ln), "name:") {
			out = append(out, "", "include:", entry)
			added = true
		}
	}
	if !added {
		out = append(out, "", "include:", entry)
	}
	return writeLines(rootCompose, out)
}

func writeLines(path string, lines []string) error {
	joined := strings.Join(lines, "\n")
	if !strings.HasSuffix(joined, "\n") {
		joined += "\n"
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(joined), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func RemoveInclude(root, rel string) error {
	rootCompose := filepath.Join(root, "compose.yaml")
	raw, err := os.ReadFile(rootCompose)
	if err != nil {
		return err
	}
	norm := filepath.ToSlash(rel)
	lines := strings.Split(string(raw), "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "- "+norm {
			continue
		}
		out = append(out, ln)
	}
	joined := strings.Join(out, "\n")
	if joined == string(raw) {
		return nil
	}
	tmp := rootCompose + ".tmp"
	if err := os.WriteFile(tmp, []byte(joined), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, rootCompose)
}

// Includes reads the root compose and returns its include entries.
func Includes(root string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(root, "compose.yaml"))
	if err != nil {
		return nil, err
	}
	var out []string
	inInclude := false
	for _, ln := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "include:") {
			inInclude = true
			continue
		}
		if inInclude {
			if strings.HasPrefix(t, "- ") {
				out = append(out, strings.TrimPrefix(t, "- "))
				continue
			}
			if t != "" && !strings.HasPrefix(t, "#") {
				inInclude = false
			}
		}
	}
	return out, nil
}

// RunCompose runs `docker compose <args...>` from the project root.
func RunCompose(root string, args ...string) (string, error) {
	full := append([]string{"compose"}, args...)
	cmd := exec.Command("docker", full...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "COMPOSE_PROJECT_NAME=infra")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("docker %s: %w: %s", strings.Join(full, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
