package main

import (
	"fmt"
	"image"
	"image/color"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/disintegration/imaging"
	"github.com/gin-gonic/gin"
)

type transformation struct {
	Name  string
	Value string
	Args  map[string]string
	Flags map[string]bool
}

func normalizeName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.NewReplacer("-", "", "_", "", " ", "", "/", "").Replace(value)
}

func splitTopLevel(value string, separator rune) []string {
	var result []string
	start, depth := 0, 0
	for index, current := range value {
		switch current {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if current == separator && depth == 0 {
				result = append(result, strings.TrimSpace(value[start:index]))
				start = index + 1
			}
		}
	}
	result = append(result, strings.TrimSpace(value[start:]))
	return result
}

func splitAssignment(value string) (string, string, bool) {
	depth := 0
	for index, current := range value {
		switch current {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case '=':
			if depth == 0 {
				return strings.TrimSpace(value[:index]), strings.TrimSpace(value[index+1:]), true
			}
		}
	}
	return strings.TrimSpace(value), "", false
}

func parseTransformations(value string) ([]transformation, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}

	var result []transformation
	for _, expression := range splitTopLevel(value, ';') {
		if expression == "" {
			continue
		}
		parts := splitTopLevel(expression, ',')
		name, directValue, hasValue := splitAssignment(parts[0])
		if name == "" {
			return nil, fmt.Errorf("invalid empty transformation in %q", expression)
		}
		item := transformation{
			Name:  normalizeName(name),
			Args:  make(map[string]string),
			Flags: make(map[string]bool),
		}
		if hasValue {
			item.Value = trimContainer(directValue)
		}
		for _, rawArgument := range parts[1:] {
			key, argumentValue, assigned := splitAssignment(rawArgument)
			if assigned {
				item.Args[normalizeName(key)] = trimContainer(argumentValue)
				continue
			}
			if open := strings.IndexByte(key, '('); open > 0 && strings.HasSuffix(key, ")") {
				item.Args[normalizeName(key[:open])] = trimContainer(key[open:])
				continue
			}
			item.Flags[normalizeName(key)] = true
		}
		result = append(result, item)
	}
	return result, nil
}

func trimContainer(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "$(") && strings.HasSuffix(value, ")") {
		return value[2 : len(value)-1]
	}
	if strings.HasPrefix(value, "(") && strings.HasSuffix(value, ")") {
		return value[1 : len(value)-1]
	}
	return value
}

func (item transformation) arg(names ...string) string {
	for _, name := range names {
		if value, found := item.Args[normalizeName(name)]; found {
			return value
		}
	}
	return ""
}

func (item transformation) number(defaultValue float64, names ...string) float64 {
	value := item.arg(names...)
	if value == "" {
		// Support both Blur=2 and Blur,strength=2 style arguments.
		value = item.Value
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return defaultValue
	}
	return parsed
}

func parseNumbers(value string) []float64 {
	parts := splitTopLevel(trimContainer(value), ',')
	result := make([]float64, 0, len(parts))
	for _, part := range parts {
		parsed, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err == nil {
			result = append(result, parsed)
		}
	}
	return result
}

func dimensions(item transformation) (int, int) {
	width := int(item.number(0, "width"))
	height := int(item.number(0, "height"))
	shortcut := item.Value
	if shortcut == "" {
		shortcut = item.arg("size")
	}
	if values := parseNumbers(shortcut); len(values) >= 2 {
		width, height = int(values[0]), int(values[1])
	}
	return width, height
}

// transformationQuery returns the transformation chain of a request, resolving
// the imop alias and the legacy width and height parameters.
func transformationQuery(request *http.Request) string {
	query := queryValue(request, "im")
	if imop := queryValue(request, "imop"); imop != "" {
		query = imop
	}
	if query == "" {
		width, height := queryValue(request, "width"), queryValue(request, "height")
		if width != "" || height != "" {
			query = fmt.Sprintf("Resize,width=%s,height=%s", width, height)
		}
	}
	return query
}

type processingState struct {
	remaining int
}

func (ip *ImageProcessor) ProcessImage(img image.Image, c *gin.Context) (image.Image, error) {
	items, err := parseTransformations(transformationQuery(c.Request))
	if err != nil {
		return nil, err
	}
	if err := ip.validateOutputImage(img); err != nil {
		return nil, err
	}
	state := &processingState{remaining: ip.config.withDefaults().MaxTransformations}
	processed := img
	for _, item := range items {
		processed, err = ip.applyTransformation(processed, item, state)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", item.Name, err)
		}
	}
	return processed, nil
}

func (ip *ImageProcessor) applyTransformation(img image.Image, item transformation, state *processingState) (image.Image, error) {
	if state.remaining <= 0 {
		return nil, fmt.Errorf("transformation limit exceeded")
	}
	state.remaining--
	if err := ip.validateTransformationGeometry(img, item); err != nil {
		return nil, err
	}
	result, err := ip.applyTransformationUnchecked(img, item, state)
	if err != nil {
		return nil, err
	}
	if err := ip.validateOutputImage(result); err != nil {
		return nil, err
	}
	return result, nil
}

func (ip *ImageProcessor) applyTransformationUnchecked(img image.Image, item transformation, state *processingState) (image.Image, error) {
	width, height := dimensions(item)
	switch item.Name {
	case "resize":
		return ip.Resize(img, width, height)
	case "featurecrop":
		return ip.FeatureCrop(img, width, height)
	case "facecrop", "smartcrop":
		return ip.faceCropTransformation(img, item, width, height)
	case "aspectcrop":
		return aspectCrop(img, item)
	case "crop":
		return crop(img, item)
	case "regionofinterestcrop":
		return regionOfInterestCrop(img, item)
	case "relativecrop":
		return relativeCrop(img, item), nil
	case "fitandfill":
		return fitAndFill(img, width, height), nil
	case "backgroundcolor":
		return backgroundColor(img, firstNonEmpty(item.arg("color"), item.Value))
	case "blur":
		return imaging.Blur(img, item.number(1, "sigma", "strength", "blur")), nil
	case "contrast":
		factor := item.number(1, "contrast")
		return imaging.AdjustContrast(img, (factor-1)*100), nil
	case "grayscale":
		return imaging.Grayscale(img), nil
	case "mirror":
		if item.Flags["vertical"] || strings.EqualFold(item.Value, "vertical") {
			return imaging.FlipV(img), nil
		}
		return imaging.FlipH(img), nil
	case "rotate":
		return imaging.Rotate(img, -item.number(0, "degrees"), color.Transparent), nil
	case "scale":
		return scaleImage(img, item), nil
	case "opacity":
		return opacity(img, item.number(1, "opacity")), nil
	case "hsl":
		return adjustHSL(img, item), nil
	case "hsv":
		return adjustHSV(img, item), nil
	case "monohue":
		return monoHue(img, item.number(0, "hue")), nil
	case "chromakey":
		return chromaKey(img, item), nil
	case "backgroundremove", "removebackground":
		if normalizeName(item.arg("method")) == "color" {
			return removeBackground(img, item), nil
		}
		cut, err := ip.removeBackgroundML(img, item)
		if err != nil && normalizeName(item.arg("fallback")) == "color" {
			log.Printf("segmentation unavailable, falling back to the color method: %v", err)
			return removeBackground(img, item), nil
		}
		return cut, err
	case "removecolor":
		return removeColor(img, item)
	case "maxcolors":
		return maxColors(img, int(item.number(256, "colors"))), nil
	case "shear":
		return shear(img, item), nil
	case "trim":
		return trimImage(img, item.number(0.02, "tolerance")), nil
	case "unsharpmask":
		return imaging.Sharpen(img, item.number(1, "gain")), nil
	case "goop":
		return goop(img, item.number(0.5, "chaos")), nil
	case "ifdimension":
		return ip.ifDimension(img, item, state)
	case "iforientation":
		return ip.ifOrientation(img, item, state)
	case "append", "composite":
		return ip.composeRemote(img, item)
	default:
		return nil, fmt.Errorf("unsupported transformation")
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (ip *ImageProcessor) applyNested(img image.Image, query string, state *processingState) (image.Image, error) {
	items, err := parseTransformations(query)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		img, err = ip.applyTransformation(img, item, state)
		if err != nil {
			return nil, err
		}
	}
	return img, nil
}

func (ip *ImageProcessor) ifDimension(img image.Image, item transformation, state *processingState) (image.Image, error) {
	actual := img.Bounds().Dx()
	if normalizeName(item.arg("dimension")) == "height" {
		actual = img.Bounds().Dy()
	}
	target := int(item.number(0, "value"))
	branch := item.arg("default")
	if actual > target {
		branch = firstNonEmpty(item.arg("greaterthan"), branch)
	} else if actual < target {
		branch = firstNonEmpty(item.arg("lessthan"), branch)
	} else {
		branch = firstNonEmpty(item.arg("equal"), branch)
	}
	return ip.applyNested(img, branch, state)
}

func (ip *ImageProcessor) ifOrientation(img image.Image, item transformation, state *processingState) (image.Image, error) {
	bounds := img.Bounds()
	branch := item.arg("default")
	switch {
	case bounds.Dx() > bounds.Dy():
		branch = firstNonEmpty(item.arg("landscape"), branch)
	case bounds.Dx() < bounds.Dy():
		branch = firstNonEmpty(item.arg("portrait"), branch)
	default:
		branch = firstNonEmpty(item.arg("square"), branch)
	}
	return ip.applyNested(img, branch, state)
}

// faceCropTransformation serves both FaceCrop and SmartCrop; both crop around
// faces and fall back to features when none are found.
func (ip *ImageProcessor) faceCropTransformation(img image.Image, item transformation, width, height int) (image.Image, error) {
	if width <= 0 && height <= 0 {
		side := min(img.Bounds().Dx(), img.Bounds().Dy())
		width, height = side, side
	}
	return ip.faceCrop(img, width, height,
		item.number(defaultMinQuality, "minquality", "confidence"),
		item.number(defaultFacePadding, "padding"),
		normalizeName(item.arg("focus")) == "biggestface")
}

func clamp(value, low, high float64) float64 {
	return math.Min(high, math.Max(low, value))
}

// queryValue reads a parameter straight from the raw query string. Go's
// url.ParseQuery rejects semicolon separators, which are used here to chain
// transformations, so those requests would otherwise lose the parameter.
func queryValue(request *http.Request, name string) string {
	if request == nil {
		return ""
	}
	for _, pair := range strings.Split(request.URL.RawQuery, "&") {
		key, value, _ := strings.Cut(pair, "=")
		decodedKey, err := url.QueryUnescape(key)
		if err != nil || decodedKey != name {
			continue
		}
		decodedValue, err := url.QueryUnescape(value)
		if err != nil {
			return value
		}
		return decodedValue
	}
	return ""
}
