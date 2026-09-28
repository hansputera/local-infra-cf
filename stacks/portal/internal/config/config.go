package config

import (
	"os"
	"strings"
)

type Config struct {
	Listen       string
	InfraRoot    string
	PublicDomain string

	CFToken     string
	CFZoneID    string
	CFAccountID string
	CFTunnelID  string

	// PublicOrigin is the CF ingress service string. Defaults to traefik on the
	// compose network. Switch it to a LAN IP (e.g. http://192.168.1.5:80) when
	// the tunnel connector moves to another device: the hostname "traefik" does
	// not resolve outside this host.
	PublicOrigin string

	TraefikAPI  string
	DockerHost  string
	ComposeArgs []string
	AccessEmail string
}

func FromEnv() Config {
	return Config{
		Listen:    env("PORTAL_LISTEN", ":3000"),
		InfraRoot: env("INFRA_ROOT", "/infra"),
		// No baked-in identity: PUBLIC_DOMAIN / CF_ZONE_ID / CF_ACCOUNT_ID /
		// CF_TUNNEL_ID / PORTAL_ACCESS_EMAIL must come from the environment
		// (compose interpolates them from .env — see .env.example).
		PublicDomain: os.Getenv("PUBLIC_DOMAIN"),

		CFToken:     os.Getenv("CF_API_TOKEN"),
		CFZoneID:    os.Getenv("CF_ZONE_ID"),
		CFAccountID: os.Getenv("CF_ACCOUNT_ID"),
		CFTunnelID:  os.Getenv("CF_TUNNEL_ID"),

		PublicOrigin: env("PUBLIC_ORIGIN", "http://traefik:80"),

		TraefikAPI:  env("TRAEFIK_API", "http://traefik:8080"),
		DockerHost:  env("DOCKER_HOST", "unix:///var/run/docker.sock"),
		ComposeArgs: []string{},
		AccessEmail: os.Getenv("PORTAL_ACCESS_EMAIL"),
	}
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
