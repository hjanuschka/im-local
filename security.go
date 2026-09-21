package main

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"fmt"
	"image"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func (ip *ImageProcessor) newSourceHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: ip.config.InsecureTLS,
		},
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("invalid source address: %w", err)
		}

		var addresses []net.IP
		if literal := net.ParseIP(host); literal != nil {
			addresses = []net.IP{literal}
		} else {
			resolved, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("resolve source host: %w", err)
			}
			for _, item := range resolved {
				addresses = append(addresses, item.IP)
			}
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("source host resolved to no addresses")
		}

		var lastErr error
		for _, address := range addresses {
			if isPrivateAddress(address) && !ip.config.AllowPrivateNetworks {
				lastErr = fmt.Errorf("source address %s is private or non-routable", address)
				continue
			}
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(address.String(), port))
			if err == nil {
				return connection, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("no permitted source address")
		}
		return nil, lastErr
	}

	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many source redirects")
			}
			if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && request.URL.Scheme != "https" {
				return fmt.Errorf("source redirect cannot downgrade HTTPS")
			}
			if _, err := ip.config.checkSourceURL(request.URL.String()); err != nil {
				return fmt.Errorf("source redirect rejected: %w", err)
			}
			return nil
		},
	}
}

func validateDimensions(width, height int, maxDimension int, maxPixels int64) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("image dimensions must be positive")
	}
	if width > maxDimension || height > maxDimension {
		return fmt.Errorf("image dimensions %dx%d exceed maximum dimension %d", width, height, maxDimension)
	}
	if int64(width) > maxPixels/int64(height) {
		return fmt.Errorf("image dimensions %dx%d exceed maximum of %d pixels", width, height, maxPixels)
	}
	return nil
}

func (ip *ImageProcessor) validateInputConfig(config image.Config) error {
	limits := ip.config.withDefaults()
	return validateDimensions(config.Width, config.Height, limits.MaxDimension, limits.MaxImagePixels)
}

func (ip *ImageProcessor) validateOutputImage(img image.Image) error {
	limits := ip.config.withDefaults()
	bounds := img.Bounds()
	return validateDimensions(bounds.Dx(), bounds.Dy(), limits.MaxDimension, limits.MaxOutputPixels)
}

func (ip *ImageProcessor) acquireProcessing(c *gin.Context) bool {
	timer := time.NewTimer(ip.config.QueueTimeout)
	defer timer.Stop()
	select {
	case ip.processSlots <- struct{}{}:
		return true
	case <-c.Request.Context().Done():
		return false
	case <-timer.C:
		c.Header("Retry-After", "5")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "server is busy; retry later"})
		return false
	}
}

func (ip *ImageProcessor) releaseProcessing() { <-ip.processSlots }

func (ip *ImageProcessor) uploadAuthorized(c *gin.Context) bool {
	if !ip.config.UploadsEnabled {
		return false
	}
	if ip.config.UploadToken == "" {
		return true
	}
	provided := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	if provided == "" {
		provided = c.GetHeader("X-Upload-Token")
	}
	return len(provided) == len(ip.config.UploadToken) &&
		subtle.ConstantTimeCompare([]byte(provided), []byte(ip.config.UploadToken)) == 1
}

type storedFile struct {
	path     string
	size     int64
	modified time.Time
}

// pruneDirectory removes expired files first, then oldest files until adding
// incoming bytes would keep the directory within its configured quota.
func pruneDirectory(directory string, ttl time.Duration, quota, incoming int64) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	now := time.Now()
	var files []storedFile
	var total int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		if ttl > 0 && now.Sub(info.ModTime()) >= ttl {
			_ = os.Remove(path)
			continue
		}
		files = append(files, storedFile{path: path, size: info.Size(), modified: info.ModTime()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modified.Before(files[j].modified) })
	for _, file := range files {
		if total+incoming <= quota {
			break
		}
		if err := os.Remove(file.path); err == nil {
			total -= file.size
		}
	}
	if incoming > quota || total+incoming > quota {
		return fmt.Errorf("storage quota exceeded")
	}
	return nil
}

const demoArtifactRetention = 90 * time.Minute

func removeExpiredFiles(directory string, retention time.Duration) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-retention)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(directory, entry.Name()))
		}
	}
}

// startDemoCleanup bounds the lifetime of user-created artifacts in a public
// demonstration. Models are excluded because they are immutable dependencies.
func (ip *ImageProcessor) startDemoCleanup() {
	cleanup := func() {
		ip.storageMutex.Lock()
		defer ip.storageMutex.Unlock()
		removeExpiredFiles(ip.config.CacheDirectory, demoArtifactRetention)
		removeExpiredFiles(ip.config.UploadDirectory, demoArtifactRetention)
	}
	cleanup()
	go func() {
		ticker := time.NewTicker(demoArtifactRetention)
		defer ticker.Stop()
		for range ticker.C {
			cleanup()
		}
	}()
}

func (ip *ImageProcessor) validateTransformationGeometry(img image.Image, item transformation) error {
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	targetWidth, targetHeight := width, height
	requestedWidth, requestedHeight := dimensions(item)

	switch item.Name {
	case "resize":
		if requestedWidth > 0 && requestedHeight > 0 {
			targetWidth, targetHeight = requestedWidth, requestedHeight
		} else if requestedWidth > 0 {
			targetWidth = requestedWidth
			targetHeight = max(1, int(float64(height)*float64(requestedWidth)/float64(width)))
		} else if requestedHeight > 0 {
			targetHeight = requestedHeight
			targetWidth = max(1, int(float64(width)*float64(requestedHeight)/float64(height)))
		}
	case "featurecrop", "facecrop", "smartcrop", "fitandfill", "regionofinterestcrop":
		if requestedWidth > 0 {
			targetWidth = requestedWidth
		}
		if requestedHeight > 0 {
			targetHeight = requestedHeight
		}
	case "crop":
		if item.Flags["allowexpansion"] {
			targetWidth, targetHeight = requestedWidth, requestedHeight
		}
	case "aspectcrop":
		if item.Flags["allowexpansion"] {
			ratioWidth, ratioHeight := item.number(1, "width"), item.number(1, "height")
			if values := parseNumbers(item.Value); len(values) >= 2 {
				ratioWidth, ratioHeight = values[0], values[1]
			}
			if ratioWidth <= 0 || ratioHeight <= 0 {
				return fmt.Errorf("aspect width and height must be positive")
			}
			ratio := ratioWidth / ratioHeight
			if float64(width)/float64(height) > ratio {
				targetHeight = int(float64(width)/ratio + 0.999999)
			} else {
				targetWidth = int(float64(height)*ratio + 0.999999)
			}
		}
	case "relativecrop":
		targetWidth = max(1, width-int(item.number(0, "west"))-int(item.number(0, "east")))
		targetHeight = max(1, height-int(item.number(0, "north"))-int(item.number(0, "south")))
	case "scale":
		targetWidth = max(1, int(float64(width)*item.number(1, "width")+0.5))
		targetHeight = max(1, int(float64(height)*item.number(1, "height")+0.5))
	case "shear":
		xFactor := item.number(0, "x", "horizontal", "width")
		yFactor := item.number(0, "y", "vertical", "height")
		if xFactor < -10 || xFactor > 10 || yFactor < -10 || yFactor > 10 {
			return fmt.Errorf("shear factor exceeds 10")
		}
		targetWidth = width + int(absFloat(xFactor)*float64(height)+0.999999)
		targetHeight = height + int(absFloat(yFactor)*float64(width)+0.999999)
	case "rotate":
		degrees := item.number(0, "degrees")
		radians := degrees * 3.141592653589793 / 180
		sine, cosine := math.Abs(math.Sin(radians)), math.Abs(math.Cos(radians))
		targetWidth = int(float64(width)*cosine + float64(height)*sine + 0.999999)
		targetHeight = int(float64(width)*sine + float64(height)*cosine + 0.999999)
	case "blur":
		if sigma := item.number(1, "sigma", "strength", "blur"); sigma < 0 || sigma > 100 {
			return fmt.Errorf("blur strength must be between 0 and 100")
		}
	case "unsharpmask":
		if gain := item.number(1, "gain"); gain < 0 || gain > 100 {
			return fmt.Errorf("sharpen gain must be between 0 and 100")
		}
	case "maxcolors":
		colors := int(item.number(256, "colors"))
		if colors < 2 || colors > 256 {
			return fmt.Errorf("colors must be between 2 and 256")
		}
	}
	return validateDimensions(targetWidth, targetHeight, ip.config.withDefaults().MaxDimension, ip.config.withDefaults().MaxOutputPixels)
}

func absFloat(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}
