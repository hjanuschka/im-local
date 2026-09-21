package main

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"strconv"
	"strings"

	"github.com/gen2brain/avif"
	"github.com/gen2brain/jpegxl"
	"github.com/gen2brain/webp"
)

// formatMediaTypes maps the format names used by the out parameter to their
// media types.
var formatMediaTypes = map[string]string{
	"jpeg": "image/jpeg",
	"png":  "image/png",
	"gif":  "image/gif",
	"webp": "image/webp",
	"avif": "image/avif",
	"jxl":  "image/jxl",
}

// encodableFormats are the formats the service can write.
var encodableFormats = []string{"jpeg", "png", "webp", "avif", "jxl"}

// defaultPreference is the negotiation order used by out=auto when no explicit
// preference is given. Modern formats first, JPEG as the universal fallback.
var defaultPreference = []string{"avif", "webp", "jxl"}

// alphaCapable lists the formats that can store an alpha channel.
var alphaCapable = map[string]bool{"png": true, "webp": true, "avif": true, "jxl": true, "gif": true}

func canonicalFormat(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "jpg" {
		return "jpeg"
	}
	return name
}

func contentType(format string) string {
	if mediaType, found := formatMediaTypes[canonicalFormat(format)]; found {
		return mediaType
	}
	return "application/octet-stream"
}

// acceptsFormat reports whether the Accept header explicitly lists the media
// type of a format with a non-zero quality. Wildcards do not count: browsers
// send image/* and */* for formats they cannot decode.
func acceptsFormat(accept, format string) bool {
	mediaType, known := formatMediaTypes[canonicalFormat(format)]
	if !known {
		return false
	}

	for _, entry := range strings.Split(accept, ",") {
		parts := strings.Split(entry, ";")
		if !strings.EqualFold(strings.TrimSpace(parts[0]), mediaType) {
			continue
		}

		quality := 1.0
		for _, parameter := range parts[1:] {
			keyValue := strings.SplitN(strings.TrimSpace(parameter), "=", 2)
			if len(keyValue) == 2 && strings.EqualFold(keyValue[0], "q") {
				parsed, err := strconv.ParseFloat(keyValue[1], 64)
				if err != nil {
					quality = 0
				} else {
					quality = parsed
				}
			}
		}
		return quality > 0
	}
	return false
}

// acceptedFormats lists the encodable formats a client advertises, most
// preferred first according to the given order.
func acceptedFormats(accept string, preference []string) []string {
	var result []string
	for _, format := range preference {
		if acceptsFormat(accept, format) {
			result = append(result, format)
		}
	}
	return result
}

// parsePreference validates a comma-separated preference list.
func parsePreference(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}

	var preference []string
	for _, item := range splitList(value) {
		format := canonicalFormat(item)
		if !slicesContains(encodableFormats, format) {
			return nil, fmt.Errorf("unsupported format %q in the preference list", item)
		}
		preference = append(preference, format)
	}
	return preference, nil
}

func slicesContains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// selectOutputFormat resolves the out and prefer parameters against the Accept
// header.
//
//   - An explicitly named format is always honoured.
//   - out=auto, or any request carrying prefer, negotiates: the first format of
//     the preference list that the client accepts wins.
//   - Otherwise the source format is kept.
//
// explicit reports whether the format was requested or negotiated, as opposed
// to inherited from the source. A non-explicit format may still be changed by
// finalizeFormat to preserve transparency.
func selectOutputFormat(requested, prefer, sourceFormat, accept string) (format string, explicit bool, err error) {
	preference, err := parsePreference(prefer)
	if err != nil {
		return "", false, err
	}

	sourceFormat = canonicalFormat(sourceFormat)
	if !slicesContains(encodableFormats, sourceFormat) {
		sourceFormat = "jpeg"
	}

	negotiate := func() (string, bool) {
		order := preference
		if len(order) == 0 {
			order = defaultPreference
		}
		if accepted := acceptedFormats(accept, order); len(accepted) > 0 {
			return accepted[0], true
		}
		return sourceFormat, false
	}

	switch canonicalFormat(requested) {
	case "", "source", "original":
		if len(preference) > 0 {
			format, explicit = negotiate()
			return format, explicit, nil
		}
		return sourceFormat, false, nil
	case "auto":
		format, explicit = negotiate()
		return format, explicit, nil
	default:
		format = canonicalFormat(requested)
		if !slicesContains(encodableFormats, format) {
			return "", false, fmt.Errorf("unsupported output format %q", requested)
		}
		return format, true, nil
	}
}

// finalizeFormat keeps transparency alive. A transformation can add an alpha
// channel that the chosen format cannot store, in which case PNG is used
// instead of silently flattening the image onto white.
func finalizeFormat(format string, explicit bool, img image.Image) string {
	if explicit || alphaCapable[format] || !hasAlpha(img) {
		return format
	}
	return "png"
}

// hasAlpha reports whether any pixel is not fully opaque.
func hasAlpha(img image.Image) bool {
	if opaque, ok := img.(interface{ Opaque() bool }); ok {
		return !opaque.Opaque()
	}
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if _, _, _, alpha := img.At(x, y).RGBA(); alpha != 0xffff {
				return true
			}
		}
	}
	return false
}

func encodeImage(img image.Image, format string, quality int) ([]byte, error) {
	var output bytes.Buffer
	var err error

	switch canonicalFormat(format) {
	case "jpeg":
		// JPEG has no alpha channel, so flatten onto white first.
		err = jpeg.Encode(&output, flattenAlpha(img), &jpeg.Options{Quality: quality})
	case "png":
		err = png.Encode(&output, img)
	case "webp":
		err = webp.Encode(&output, img, webp.Options{Quality: quality})
	case "avif":
		// AVIF's quality scale is not JPEG's: the same number produces much
		// larger files, so the requested quality is mapped down.
		avifQuality := int(math.Round(float64(quality) * 0.75))
		err = avif.Encode(&output, img, avif.Options{
			Quality:      max(1, min(avifQuality, 100)),
			QualityAlpha: max(1, min(avifQuality, 100)),
			Speed:        avifSpeed,
		})
	case "jxl":
		err = jpegxl.Encode(&output, img, jpegxl.Options{Quality: quality, Effort: jpegxl.DefaultEffort})
	default:
		return nil, fmt.Errorf("cannot encode output format %q", format)
	}
	if err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// avifSpeed trades encoding time for size. AV1 is slow: at speed 6 an image of
// this size takes seconds, while speed 10 needs about half a second for
// virtually the same byte count.
const avifSpeed = 10

// encoderBackends describes which formats use a shared library and which fall
// back to the bundled WASM builds, for the startup log.
func encoderBackends() string {
	backends := []string{
		"jxl=" + backendName(jpegxl.Dynamic()),
		"avif=" + backendName(avif.Dynamic()),
		"webp=" + backendName(webp.Dynamic()),
	}
	return strings.Join(backends, " ")
}

func backendName(err error) string {
	if err == nil {
		return "shared"
	}
	return "wasm"
}
