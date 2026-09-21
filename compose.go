package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"io"
	"net/http"
	"strings"

	"github.com/disintegration/imaging"
)

// fetchImage retrieves and decodes a pristine image, enforcing the source URL
// policy of the configuration.
func (ip *ImageProcessor) fetchImage(rawURL string) (image.Image, string, error) {
	parsed, err := ip.config.checkSourceURL(rawURL)
	if err != nil {
		return nil, "", err
	}

	response, err := ip.sourceClient.Get(parsed.String())
	if err != nil {
		return nil, "", fmt.Errorf("failed to fetch image: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("failed to fetch image: %s", response.Status)
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, ip.config.MaxImageBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("failed to read image data: %w", err)
	}
	if int64(len(data)) > ip.config.MaxImageBytes {
		return nil, "", fmt.Errorf("source image exceeds %d bytes", ip.config.MaxImageBytes)
	}
	decodedConfig, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("failed to inspect image: %w", err)
	}
	if err := ip.validateInputConfig(decodedConfig); err != nil {
		return nil, "", err
	}

	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("failed to decode image: %w", err)
	}
	return img, format, nil
}

// composeRemote implements Composite (overlay/underlay) and Append (side by
// side placement) for remote images.
func (ip *ImageProcessor) composeRemote(img image.Image, item transformation) (image.Image, error) {
	source := item.arg("image")
	overlayURL := item.arg("url")
	if overlayURL == "" && source != "" {
		for _, part := range splitTopLevel(source, ',') {
			if key, value, ok := splitAssignment(part); ok && normalizeName(key) == "url" {
				overlayURL = value
			}
		}
	}
	if overlayURL == "" {
		return nil, fmt.Errorf("image=(url=...) is required")
	}

	overlay, _, err := ip.fetchImage(overlayURL)
	if err != nil {
		return nil, err
	}

	if scale := item.number(1, "scale"); scale != 1 && scale > 0 {
		width := max(1, int(float64(overlay.Bounds().Dx())*scale))
		height := max(1, int(float64(overlay.Bounds().Dy())*scale))
		if err := validateDimensions(width, height, ip.config.MaxDimension, ip.config.MaxOutputPixels); err != nil {
			return nil, err
		}
		overlay = imaging.Resize(overlay, width, height, imaging.Lanczos)
	}

	if item.Name == "append" {
		vertical := strings.Contains(normalizeName(item.arg("gravity")), "north") || strings.Contains(normalizeName(item.arg("gravity")), "south") ||
			normalizeName(item.arg("gravity")) == "vertical" || normalizeName(item.arg("gravity")) == "top" || normalizeName(item.arg("gravity")) == "bottom"
		width, height := img.Bounds().Dx()+overlay.Bounds().Dx(), max(img.Bounds().Dy(), overlay.Bounds().Dy())
		if vertical {
			width, height = max(img.Bounds().Dx(), overlay.Bounds().Dx()), img.Bounds().Dy()+overlay.Bounds().Dy()
		}
		if err := validateDimensions(width, height, ip.config.MaxDimension, ip.config.MaxOutputPixels); err != nil {
			return nil, err
		}
		return appendImages(img, overlay, normalizeName(item.arg("gravity"))), nil
	}
	return compositeImages(img, overlay, item), nil
}

func appendImages(base, addition image.Image, gravity string) image.Image {
	vertical := strings.Contains(gravity, "north") || strings.Contains(gravity, "south") ||
		gravity == "vertical" || gravity == "top" || gravity == "bottom"

	baseBounds, additionBounds := base.Bounds(), addition.Bounds()
	if vertical {
		canvas := image.NewNRGBA(image.Rect(0, 0, max(baseBounds.Dx(), additionBounds.Dx()), baseBounds.Dy()+additionBounds.Dy()))
		canvas = imaging.Paste(canvas, base, image.Pt(0, 0))
		return imaging.Paste(canvas, addition, image.Pt(0, baseBounds.Dy()))
	}
	canvas := image.NewNRGBA(image.Rect(0, 0, baseBounds.Dx()+additionBounds.Dx(), max(baseBounds.Dy(), additionBounds.Dy())))
	canvas = imaging.Paste(canvas, base, image.Pt(0, 0))
	return imaging.Paste(canvas, addition, image.Pt(baseBounds.Dx(), 0))
}

func compositeImages(base, overlay image.Image, item transformation) image.Image {
	baseBounds, overlayBounds := base.Bounds(), overlay.Bounds()
	x := int(item.number(0, "xposition", "x"))
	y := int(item.number(0, "yposition", "y"))

	switch normalizeName(item.arg("gravity", "placement")) {
	case "center":
		x, y = (baseBounds.Dx()-overlayBounds.Dx())/2, (baseBounds.Dy()-overlayBounds.Dy())/2
	case "northeast", "topright":
		x, y = baseBounds.Dx()-overlayBounds.Dx(), 0
	case "southwest", "bottomleft":
		x, y = 0, baseBounds.Dy()-overlayBounds.Dy()
	case "southeast", "bottomright":
		x, y = baseBounds.Dx()-overlayBounds.Dx(), baseBounds.Dy()-overlayBounds.Dy()
	}

	alpha := clamp(item.number(1, "opacity"), 0, 1)
	if normalizeName(item.arg("placement")) == "under" || item.Flags["under"] {
		canvas := imaging.New(baseBounds.Dx(), baseBounds.Dy(), color.NRGBA{})
		canvas = imaging.Paste(canvas, overlay, image.Pt(x, y))
		return imaging.Overlay(canvas, base, image.Pt(0, 0), 1)
	}
	return imaging.Overlay(base, overlay, image.Pt(x, y), alpha)
}
