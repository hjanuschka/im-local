package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

// Config holds the runtime settings. Every field can be set with an
// environment variable, which is what the container image and Kubernetes
// manifests use.
type Config struct {
	Port            string
	CacheDirectory  string
	ModelDirectory  string
	UploadDirectory string
	CacheDuration   time.Duration

	// AllowedHosts restricts which origins may be fetched. An empty list denies
	// all network sources.
	AllowedHosts         []string
	AllowedPathPrefixes  []string
	AllowPrivateNetworks bool
	InsecureTLS          bool

	MaxImageBytes      int64
	MaxImagePixels     int64
	MaxOutputPixels    int64
	MaxDimension       int
	MaxTransformations int
	MaxQueryBytes      int
	MaxConcurrent      int
	QueueTimeout       time.Duration

	UploadsEnabled     bool
	UploadToken        string
	MaxUploadBytes     int64
	UploadStorageBytes int64
	UploadTTL          time.Duration
	CacheStorageBytes  int64
	DemoMode           bool
}

const (
	defaultCacheDuration      = 24 * time.Hour
	defaultMaxImageBytes      = 16 << 20
	defaultMaxImagePixels     = int64(20_000_000)
	defaultMaxOutputPixels    = int64(25_000_000)
	defaultMaxDimension       = 8192
	defaultMaxTransformations = 20
	defaultMaxQueryBytes      = 16 << 10
	defaultMaxConcurrent      = 4
	defaultQueueTimeout       = 5 * time.Second
	defaultMaxUploadBytes     = 10 << 20
	defaultUploadStorageBytes = 512 << 20
	defaultUploadTTL          = time.Hour
	defaultCacheStorageBytes  = 5 << 30
)

// LoadConfig reads the configuration from the environment. Network fetching
// and uploads both default to disabled.
func LoadConfig() (Config, error) {
	config := Config{
		Port:               environment("IM_PORT", "8080"),
		CacheDirectory:     environment("IM_CACHE_DIR", "./cache"),
		ModelDirectory:     environment("IM_MODEL_DIR", "./models"),
		UploadDirectory:    environment("IM_UPLOAD_DIR", "./uploads"),
		CacheDuration:      defaultCacheDuration,
		MaxImageBytes:      defaultMaxImageBytes,
		MaxImagePixels:     defaultMaxImagePixels,
		MaxOutputPixels:    defaultMaxOutputPixels,
		MaxDimension:       defaultMaxDimension,
		MaxTransformations: defaultMaxTransformations,
		MaxQueryBytes:      defaultMaxQueryBytes,
		MaxConcurrent:      defaultMaxConcurrent,
		QueueTimeout:       defaultQueueTimeout,
		MaxUploadBytes:     defaultMaxUploadBytes,
		UploadStorageBytes: defaultUploadStorageBytes,
		UploadTTL:          defaultUploadTTL,
		CacheStorageBytes:  defaultCacheStorageBytes,
	}

	var err error
	if config.CacheDuration, err = durationEnvironment("IM_CACHE_TTL", config.CacheDuration); err != nil {
		return config, err
	}
	if config.QueueTimeout, err = durationEnvironment("IM_QUEUE_TIMEOUT", config.QueueTimeout); err != nil {
		return config, err
	}
	if config.UploadTTL, err = durationEnvironment("IM_UPLOAD_TTL", config.UploadTTL); err != nil {
		return config, err
	}

	config.AllowedHosts = splitList(os.Getenv("IM_ALLOWED_HOSTS"))
	config.AllowedPathPrefixes = splitList(os.Getenv("IM_ALLOWED_PATH_PREFIXES"))
	config.AllowPrivateNetworks = booleanEnvironment("IM_ALLOW_PRIVATE_NETWORKS", false)
	config.InsecureTLS = booleanEnvironment("IM_INSECURE_TLS", false)
	config.UploadsEnabled = booleanEnvironment("IM_UPLOADS_ENABLED", false)
	config.UploadToken = os.Getenv("IM_UPLOAD_TOKEN")
	config.DemoMode = booleanEnvironment("DEMO_MODE", false)

	integers := []struct {
		name   string
		target *int
	}{
		{"IM_MAX_DIMENSION", &config.MaxDimension},
		{"IM_MAX_TRANSFORMATIONS", &config.MaxTransformations},
		{"IM_MAX_QUERY_BYTES", &config.MaxQueryBytes},
		{"IM_MAX_CONCURRENT", &config.MaxConcurrent},
	}
	for _, setting := range integers {
		if *setting.target, err = intEnvironment(setting.name, *setting.target); err != nil {
			return config, err
		}
	}

	int64s := []struct {
		name   string
		target *int64
	}{
		{"IM_MAX_IMAGE_BYTES", &config.MaxImageBytes},
		{"IM_MAX_IMAGE_PIXELS", &config.MaxImagePixels},
		{"IM_MAX_OUTPUT_PIXELS", &config.MaxOutputPixels},
		{"IM_MAX_UPLOAD_BYTES", &config.MaxUploadBytes},
		{"IM_UPLOAD_STORAGE_BYTES", &config.UploadStorageBytes},
		{"IM_CACHE_STORAGE_BYTES", &config.CacheStorageBytes},
	}
	for _, setting := range int64s {
		if *setting.target, err = int64Environment(setting.name, *setting.target); err != nil {
			return config, err
		}
	}
	return config, nil
}

func (c Config) withDefaults() Config {
	if c.Port == "" {
		c.Port = "8080"
	}
	if c.CacheDirectory == "" {
		c.CacheDirectory = "./cache"
	}
	if c.ModelDirectory == "" {
		c.ModelDirectory = "./models"
	}
	if c.UploadDirectory == "" {
		c.UploadDirectory = "./uploads"
	}
	if c.CacheDuration <= 0 {
		c.CacheDuration = defaultCacheDuration
	}
	if c.MaxImageBytes <= 0 {
		c.MaxImageBytes = defaultMaxImageBytes
	}
	if c.MaxImagePixels <= 0 {
		c.MaxImagePixels = defaultMaxImagePixels
	}
	if c.MaxOutputPixels <= 0 {
		c.MaxOutputPixels = defaultMaxOutputPixels
	}
	if c.MaxDimension <= 0 {
		c.MaxDimension = defaultMaxDimension
	}
	if c.MaxTransformations <= 0 {
		c.MaxTransformations = defaultMaxTransformations
	}
	if c.MaxQueryBytes <= 0 {
		c.MaxQueryBytes = defaultMaxQueryBytes
	}
	if c.MaxConcurrent <= 0 {
		c.MaxConcurrent = defaultMaxConcurrent
	}
	if c.QueueTimeout <= 0 {
		c.QueueTimeout = defaultQueueTimeout
	}
	if c.MaxUploadBytes <= 0 {
		c.MaxUploadBytes = defaultMaxUploadBytes
	}
	if c.UploadStorageBytes <= 0 {
		c.UploadStorageBytes = defaultUploadStorageBytes
	}
	if c.UploadTTL <= 0 {
		c.UploadTTL = defaultUploadTTL
	}
	if c.CacheStorageBytes <= 0 {
		c.CacheStorageBytes = defaultCacheStorageBytes
	}
	return c
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

func durationEnvironment(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return parsed, nil
}

func intEnvironment(name string, fallback int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return parsed, nil
}

func int64Environment(name string, fallback int64) (int64, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return parsed, nil
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
// case-insensitively; `*` allows everything, and `*.example.com` matches the
// domain and its subdomains. Private addresses remain blocked separately.
func (c Config) hostAllowed(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, pattern := range c.AllowedHosts {
		pattern = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(pattern), "."))
		switch {
		case pattern == "*":
			return true
		case strings.HasPrefix(pattern, "*."):
			suffix := pattern[1:]
			if strings.HasSuffix(host, suffix) || host == suffix[1:] {
				return true
			}
		case pattern == host:
			return true
		}
	}
	return false
}

func (c Config) checkSourceURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid image url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("unsupported image url scheme %q", parsed.Scheme)
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("image url credentials are not allowed")
	}
	host := parsed.Hostname()
	if host == "" {
		return nil, fmt.Errorf("image url has no host")
	}
	if !c.hostAllowed(host) {
		return nil, fmt.Errorf("host %q is not allowed", host)
	}
	sourcePath := parsed.Path
	if sourcePath == "" {
		sourcePath = "/"
	}
	if path.Clean(sourcePath) != sourcePath {
		return nil, fmt.Errorf("source path contains unsafe segments")
	}
	if len(c.AllowedPathPrefixes) > 0 {
		allowed := false
		for _, prefix := range c.AllowedPathPrefixes {
			if sourcePath == prefix || (strings.HasSuffix(prefix, "/") && strings.HasPrefix(sourcePath, prefix)) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("source path %q is not allowed", parsed.Path)
		}
	}
	port := parsed.Port()
	if port != "" && port != "80" && port != "443" && !c.AllowPrivateNetworks {
		return nil, fmt.Errorf("source port %q is not allowed", port)
	}
	return parsed, nil
}

func isPrivateAddress(address net.IP) bool {
	return address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() || address.IsUnspecified() || address.IsMulticast()
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
