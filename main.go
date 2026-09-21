package main

import (
	"bytes"
	"crypto/md5"
	"flag"
	"fmt"
	"image"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/disintegration/imaging"
	pigo "github.com/esimov/pigo/core"
	"github.com/gin-gonic/gin"
	"github.com/oliamb/cutter"
)

// defaultQuality is the encoder quality used when the request omits quality.
const defaultQuality = 90

// Face represents a detected face
type Face struct {
	X      int
	Y      int
	Width  int
	Height int
	Score  float32
}

// ImageProcessor handles all image processing operations
type ImageProcessor struct {
	config     Config
	classifier *pigo.Pigo
	facefinder []byte
	segmenter  *segmenter
}

func NewImageProcessor(config Config) (*ImageProcessor, error) {
	// An empty policy would deny every source, so fall back to the default.
	if len(config.AllowedHosts) == 0 {
		config.AllowedHosts = defaultAllowedHosts
	}
	if config.MaxImageBytes <= 0 {
		config.MaxImageBytes = defaultMaxImageBytes
	}

	// Create cache directory if it doesn't exist
	if err := os.MkdirAll(config.CacheDirectory, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	// Try to load the facefinder cascade file
	// The cascade is embedded in the binary; a file next to the binary wins so
	// it can be swapped without a rebuild.
	facefinder := embeddedCascade
	if fromDisk, err := os.ReadFile("facefinder"); err == nil && len(fromDisk) > 1000 {
		facefinder = fromDisk
		log.Printf("Loaded cascade file from disk, size: %d bytes", len(facefinder))
	} else {
		log.Printf("Using embedded cascade file, size: %d bytes", len(facefinder))
	}

	processor := &ImageProcessor{
		config:     config,
		facefinder: facefinder,
		segmenter:  newSegmenter(config.ModelDirectory),
	}

	// Initialize the classifier if we have the cascade file
	if len(facefinder) > 0 {
		p := pigo.NewPigo()
		classifier, err := p.Unpack(facefinder)
		if err != nil {
			log.Printf("Error unpacking cascade: %v", err)
		} else {
			processor.classifier = classifier
			log.Printf("Face detection classifier initialized successfully")
		}
	} else {
		log.Printf("Face detection will not be available")
	}

	return processor, nil
}

// RegisterRoutes wires every endpoint of the service.
func (ip *ImageProcessor) RegisterRoutes(router gin.IRouter) {
	router.GET("/process", ip.HandleRequest)

	// Documentation with a live example per transformation.
	router.GET("/doc", ip.HandleDoc)
	router.GET("/sample.jpg", ip.HandleSample)
	router.GET("/sample-studio.jpg", ip.HandleStudioSample)

	// Uploads let the documentation page run against your own image.
	router.POST("/upload", ip.HandleUpload)
	router.GET("/uploads/:name", ip.HandleUploadedImage)

	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
}

// HandleRequest processes the image according to the parameters in the URL
func (ip *ImageProcessor) HandleRequest(c *gin.Context) {
	started := time.Now()

	// Parse URL parameters
	imageURL := queryValue(c.Request, "url")
	if imageURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url parameter is required"})
		return
	}

	quality, err := parseQuality(queryValue(c.Request, "quality"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	img, format, err := ip.fetchImage(imageURL)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	outputFormat, explicitFormat, err := selectOutputFormat(
		queryValue(c.Request, "out"), queryValue(c.Request, "prefer"), format, c.GetHeader("Accept"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// The key is built from the inputs that change the output, so equivalent
	// requests share an entry no matter how the query string is ordered.
	cacheKey := derivativeCacheKey(imageURL, transformationQuery(c.Request), outputFormat, explicitFormat, quality)
	cachePath := filepath.Join(ip.config.CacheDirectory, cacheKey)
	if cachedData, err := readFreshCache(cachePath, ip.config.CacheDuration); err == nil && queryValue(c.Request, "nocache") == "" {
		log.Printf("Serving from cache: %s", cacheKey)
		ip.serveImage(c, cachedData, outputFormat, "HIT", started)
		return
	}

	// Process the image based on parameters.
	processedImg, err := ip.ProcessImage(img, c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to process image: " + err.Error()})
		return
	}

	outputFormat = finalizeFormat(outputFormat, explicitFormat, processedImg)
	encoded, err := encodeImage(processedImg, outputFormat, quality)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to encode image: " + err.Error()})
		return
	}

	if err := writeCache(cachePath, encoded); err != nil {
		log.Printf("Warning: failed to write cache file: %v", err)
	}
	ip.serveImage(c, encoded, outputFormat, "MISS", started)
}

// serveImage writes the derivative together with validators and the timing and
// cache information the documentation page displays.
func (ip *ImageProcessor) serveImage(c *gin.Context, data []byte, format, cacheStatus string, started time.Time) {
	etag := fmt.Sprintf("%q", fmt.Sprintf("%x", md5.Sum(data)))
	c.Header("ETag", etag)
	c.Header("Cache-Control", fmt.Sprintf("public, max-age=%d", int(ip.config.CacheDuration.Seconds())))
	// The selected representation depends on Accept when out=jxl or out=auto.
	c.Header("Vary", "Accept")
	c.Header("X-Cache", cacheStatus)
	c.Header("X-Process-Time-Ms", strconv.FormatInt(time.Since(started).Milliseconds(), 10))
	if config, detected, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		c.Header("X-Image-Width", strconv.Itoa(config.Width))
		c.Header("X-Image-Height", strconv.Itoa(config.Height))
		// A cached derivative may have been stored in a different format than
		// the negotiation suggests, for example PNG to preserve transparency.
		format = detected
	}
	c.Header("Access-Control-Expose-Headers", "X-Cache, X-Process-Time-Ms, X-Image-Width, X-Image-Height")

	if strings.Contains(c.GetHeader("If-None-Match"), etag) {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, contentType(format), data)
}

func parseQuality(value string) (int, error) {
	if value == "" {
		return defaultQuality, nil
	}
	quality, err := strconv.Atoi(value)
	if err != nil || quality < 1 || quality > 100 {
		return 0, fmt.Errorf("quality must be an integer between 1 and 100")
	}
	return quality, nil
}

func readFreshCache(path string, duration time.Duration) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if time.Since(info.ModTime()) >= duration {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(path)
}

func writeCache(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".image-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

// Resize resizes an image to the specified dimensions, maintaining aspect ratio if one dimension is 0
func (ip *ImageProcessor) Resize(img image.Image, width, height int) (image.Image, error) {
	bounds := img.Bounds()
	origWidth := bounds.Dx()
	origHeight := bounds.Dy()

	// If both dimensions are specified
	if width > 0 && height > 0 {
		return imaging.Resize(img, width, height, imaging.Lanczos), nil
	}

	// If only width is specified, maintain aspect ratio
	if width > 0 {
		newHeight := int(float64(origHeight) * float64(width) / float64(origWidth))
		return imaging.Resize(img, width, newHeight, imaging.Lanczos), nil
	}

	// If only height is specified, maintain aspect ratio
	if height > 0 {
		newWidth := int(float64(origWidth) * float64(height) / float64(origHeight))
		return imaging.Resize(img, newWidth, height, imaging.Lanczos), nil
	}

	// If neither dimension is specified, return original
	return img, nil
}

// FeatureCrop crops an image to the specified dimensions, focusing on the center
func (ip *ImageProcessor) FeatureCrop(img image.Image, width, height int) (image.Image, error) {
	bounds := img.Bounds()
	origWidth := bounds.Dx()
	origHeight := bounds.Dy()

	// Resize if necessary to fit the target dimensions while maintaining aspect ratio
	if float64(width)/float64(height) > float64(origWidth)/float64(origHeight) {
		// Target is wider than original, resize to match width
		newHeight := int(float64(origHeight) * float64(width) / float64(origWidth))
		img = imaging.Resize(img, width, newHeight, imaging.Lanczos)
	} else {
		// Target is taller than original, resize to match height
		newWidth := int(float64(origWidth) * float64(height) / float64(origHeight))
		img = imaging.Resize(img, newWidth, height, imaging.Lanczos)
	}

	// Crop from center
	croppedImg, err := cutter.Crop(img, cutter.Config{
		Width:  width,
		Height: height,
		Mode:   cutter.Centered,
	})

	return croppedImg, err
}

// derivativeCacheKey identifies a derivative by everything that influences its
// bytes: the source image, the transformation chain, the negotiated output
// format, and the encoder quality.
func derivativeCacheKey(imageURL, transformations, outputFormat string, explicitFormat bool, quality int) string {
	identity := strings.Join([]string{
		"url=" + imageURL,
		"im=" + transformations,
		"out=" + outputFormat,
		// A negotiated format may still become PNG to keep transparency, so it
		// must not share an entry with an explicitly requested format.
		"explicit=" + strconv.FormatBool(explicitFormat),
		"quality=" + strconv.Itoa(quality),
	}, "\x00")
	return fmt.Sprintf("%x", md5.Sum([]byte(identity)))
}

// healthcheck is used by the container image, which has no curl.
func healthcheck(port string) int {
	response, err := http.Get("http://127.0.0.1:" + port + "/health")
	if err != nil {
		log.Printf("health check failed: %v", err)
		return 1
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		log.Printf("health check returned %s", response.Status)
		return 1
	}
	return 0
}

func main() {
	healthcheckFlag := flag.Bool("healthcheck", false, "probe a running instance and exit")
	flag.Parse()

	config, err := LoadConfig()
	if err != nil {
		log.Fatalf("Invalid configuration: %v", err)
	}

	if *healthcheckFlag {
		os.Exit(healthcheck(config.Port))
	}

	// Create image processor
	processor, err := NewImageProcessor(config)
	if err != nil {
		log.Fatalf("Failed to initialize image processor: %v", err)
	}

	// Setup router
	router := gin.Default()

	processor.RegisterRoutes(router)

	// Start server
	log.Printf("Encoders: %s", encoderBackends())
	log.Printf("Allowed source hosts: %s", strings.Join(config.AllowedHosts, ", "))
	log.Printf("Starting server on port %s", config.Port)
	router.Run(":" + config.Port)
}
