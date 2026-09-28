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

	// PublicOrigin adalah service string ingress CF. Default traefik di network
	// compose. Ganti ke IP LAN (mis. http://192.168.1.5:80) saat connector tunnel
	// pindah ke device lain: hostname "traefik" tidak resolve di luar host ini.
	PublicOrigin string

	TraefikAPI  string
	DockerHost  string
	ComposeArgs []string
	AccessEmail string
}

func FromEnv() Config {
	return Config{
		Listen:       env("PORTAL_LISTEN", ":3000"),
		InfraRoot:    env("INFRA_ROOT", "/infra"),
		PublicDomain: env("PUBLIC_DOMAIN", "itukan.my.id"),

		CFToken:     os.Getenv("CF_API_TOKEN"),
		CFZoneID:    env("CF_ZONE_ID", "8cf06808d24156dea817bea46e26a573"),
		CFAccountID: env("CF_ACCOUNT_ID", "704cdc3baf4cd34018a536e0a560f830"),
		CFTunnelID:  env("CF_TUNNEL_ID", "44703f6e-2691-4677-8fe0-57245730f516"),

		PublicOrigin: env("PUBLIC_ORIGIN", "http://traefik:80"),

		TraefikAPI:  env("TRAEFIK_API", "http://traefik:8080"),
		DockerHost:  env("DOCKER_HOST", "unix:///var/run/docker.sock"),
		ComposeArgs: []string{},
		AccessEmail: env("PORTAL_ACCESS_EMAIL", "hanifdwyputrasembiring@gmail.com"),
	}
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
