package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds the runtime settings. Every field can be set with an
// environment variable, which is what the container image and the Kubernetes
// manifests use.
type Config struct {
	Port            string
	CacheDirectory  string
	ModelDirectory  string
	UploadDirectory string
	CacheDuration   time.Duration

	// AllowedHosts restricts which origins may be fetched. Defaults to
	// localhost so a fresh deployment cannot be used as an open proxy.
	AllowedHosts []string
	// InsecureTLS disables certificate verification for source fetches.
	InsecureTLS bool
	// MaxImageBytes bounds how much is read from an origin.
	MaxImageBytes int64
}

const (
	defaultCacheDuration = 24 * time.Hour
	defaultMaxImageBytes = 64 << 20
)

// defaultAllowedHosts keeps a fresh install from fetching arbitrary URLs.
var defaultAllowedHosts = []string{"localhost", "127.0.0.1", "::1"}

// LoadConfig reads the configuration from the environment.
func LoadConfig() (Config, error) {
	config := Config{
		Port:            environment("IM_PORT", "8080"),
		CacheDirectory:  environment("IM_CACHE_DIR", "./cache"),
		ModelDirectory:  environment("IM_MODEL_DIR", "./models"),
		UploadDirectory: environment("IM_UPLOAD_DIR", "./uploads"),
		CacheDuration:   defaultCacheDuration,
		AllowedHosts:    defaultAllowedHosts,
		MaxImageBytes:   defaultMaxImageBytes,
	}

	if value := os.Getenv("IM_CACHE_TTL"); value != "" {
		duration, err := time.ParseDuration(value)
		if err != nil {
			return config, fmt.Errorf("IM_CACHE_TTL: %w", err)
		}
		config.CacheDuration = duration
	}

	if value := os.Getenv("IM_ALLOWED_HOSTS"); value != "" {
		config.AllowedHosts = splitList(value)
	}

	if value := os.Getenv("IM_MAX_IMAGE_BYTES"); value != "" {
		size, err := strconv.ParseInt(value, 10, 64)
		if err != nil || size <= 0 {
			return config, fmt.Errorf("IM_MAX_IMAGE_BYTES must be a positive number of bytes")
		}
		config.MaxImageBytes = size
	}

	config.InsecureTLS = booleanEnvironment("IM_INSECURE_TLS", false)
	return config, nil
}

func environment(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func booleanEnvironment(name string, fallback bool) bool {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func splitList(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// hostAllowed reports whether a host may be fetched. Entries are compared
// case-insensitively; `*` allows everything, and a leading `*.` matches any
// subdomain of the remaining suffix.
func (c Config) hostAllowed(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, pattern := range c.AllowedHosts {
		pattern = strings.ToLower(pattern)
		switch {
		case pattern == "*":
			return true
		case strings.HasPrefix(pattern, "*."):
			suffix := pattern[1:] // ".example.com"
			if strings.HasSuffix(host, suffix) || host == suffix[1:] {
				return true
			}
		case pattern == host:
			return true
		}
	}
	return false
}

// checkSourceURL validates a source URL against the scheme and host policy.
func (c Config) checkSourceURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid image url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("unsupported image url scheme %q", parsed.Scheme)
	}

	host := parsed.Hostname()
	if host == "" {
		return nil, fmt.Errorf("image url has no host")
	}
	if !c.hostAllowed(host) {
		return nil, fmt.Errorf("host %q is not allowed; set IM_ALLOWED_HOSTS to permit it", host)
	}
	return parsed, nil
}

// isLoopback is used by the documentation page to decide whether it can offer
// its own samples as sources.
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
