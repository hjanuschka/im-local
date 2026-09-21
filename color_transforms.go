package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"strconv"
	"strings"
)

func mapNRGBA(img image.Image, transform func(color.NRGBA) color.NRGBA) *image.NRGBA {
	bounds := img.Bounds()
	result := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := 0; y < bounds.Dy(); y++ {
		for x := 0; x < bounds.Dx(); x++ {
			current := color.NRGBAModel.Convert(img.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.NRGBA)
			result.SetNRGBA(x, y, transform(current))
		}
	}
	return result
}

func parseHexColor(value string) (color.NRGBA, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "#")
	if len(value) == 3 {
		value = string([]byte{value[0], value[0], value[1], value[1], value[2], value[2]})
	}
	if len(value) != 6 && len(value) != 8 {
		return color.NRGBA{}, fmt.Errorf("color must be RGB or RGBA hexadecimal")
	}
	parsed, err := strconv.ParseUint(value, 16, 32)
	if err != nil {
		return color.NRGBA{}, fmt.Errorf("invalid color %q", value)
	}
	if len(value) == 6 {
		return color.NRGBA{R: uint8(parsed >> 16), G: uint8(parsed >> 8), B: uint8(parsed), A: 255}, nil
	}
	return color.NRGBA{R: uint8(parsed >> 24), G: uint8(parsed >> 16), B: uint8(parsed >> 8), A: uint8(parsed)}, nil
}

func backgroundColor(img image.Image, value string) (image.Image, error) {
	background, err := parseHexColor(value)
	if err != nil {
		return nil, err
	}
	bounds := img.Bounds()
	result := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(result, result.Bounds(), &image.Uniform{C: background}, image.Point{}, draw.Src)
	draw.Draw(result, result.Bounds(), img, bounds.Min, draw.Over)
	return result, nil
}

func opacity(img image.Image, multiplier float64) image.Image {
	return mapNRGBA(img, func(pixel color.NRGBA) color.NRGBA {
		pixel.A = uint8(clamp(float64(pixel.A)*multiplier, 0, 255))
		return pixel
	})
}

func rgbToHSL(pixel color.NRGBA) (float64, float64, float64) {
	r, g, b := float64(pixel.R)/255, float64(pixel.G)/255, float64(pixel.B)/255
	maximum, minimum := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	lightness := (maximum + minimum) / 2
	if maximum == minimum {
		return 0, 0, lightness
	}
	delta := maximum - minimum
	saturation := delta / (1 - math.Abs(2*lightness-1))
	var hue float64
	switch maximum {
	case r:
		hue = math.Mod((g-b)/delta, 6)
	case g:
		hue = (b-r)/delta + 2
	default:
		hue = (r-g)/delta + 4
	}
	hue *= 60
	if hue < 0 {
		hue += 360
	}
	return hue, saturation, lightness
}

func hueToRGB(p, q, t float64) float64 {
	if t < 0 {
		t++
	}
	if t > 1 {
		t--
	}
	switch {
	case t < 1.0/6:
		return p + (q-p)*6*t
	case t < 0.5:
		return q
	case t < 2.0/3:
		return p + (q-p)*(2.0/3-t)*6
	default:
		return p
	}
}

func hslToRGB(hue, saturation, lightness float64, alpha uint8) color.NRGBA {
	hue = math.Mod(hue, 360)
	if hue < 0 {
		hue += 360
	}
	saturation, lightness = clamp(saturation, 0, 1), clamp(lightness, 0, 1)
	if saturation == 0 {
		gray := uint8(math.Round(lightness * 255))
		return color.NRGBA{R: gray, G: gray, B: gray, A: alpha}
	}
	q := lightness * (1 + saturation)
	if lightness >= 0.5 {
		q = lightness + saturation - lightness*saturation
	}
	p := 2*lightness - q
	h := hue / 360
	return color.NRGBA{
		R: uint8(math.Round(hueToRGB(p, q, h+1.0/3) * 255)),
		G: uint8(math.Round(hueToRGB(p, q, h) * 255)),
		B: uint8(math.Round(hueToRGB(p, q, h-1.0/3) * 255)),
		A: alpha,
	}
}

func rgbToHSV(pixel color.NRGBA) (float64, float64, float64) {
	r, g, b := float64(pixel.R)/255, float64(pixel.G)/255, float64(pixel.B)/255
	maximum, minimum := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	delta := maximum - minimum
	var hue float64
	if delta != 0 {
		switch maximum {
		case r:
			hue = 60 * math.Mod((g-b)/delta, 6)
		case g:
			hue = 60 * ((b-r)/delta + 2)
		default:
			hue = 60 * ((r-g)/delta + 4)
		}
	}
	if hue < 0 {
		hue += 360
	}
	saturation := 0.0
	if maximum != 0 {
		saturation = delta / maximum
	}
	return hue, saturation, maximum
}

func hsvToRGB(hue, saturation, value float64, alpha uint8) color.NRGBA {
	hue = math.Mod(hue, 360)
	if hue < 0 {
		hue += 360
	}
	saturation, value = clamp(saturation, 0, 1), clamp(value, 0, 1)
	chroma := value * saturation
	x := chroma * (1 - math.Abs(math.Mod(hue/60, 2)-1))
	m := value - chroma
	var r, g, b float64
	switch int(hue / 60) {
	case 0:
		r, g = chroma, x
	case 1:
		r, g = x, chroma
	case 2:
		g, b = chroma, x
	case 3:
		g, b = x, chroma
	case 4:
		r, b = x, chroma
	default:
		r, b = chroma, x
	}
	return color.NRGBA{uint8(math.Round((r + m) * 255)), uint8(math.Round((g + m) * 255)), uint8(math.Round((b + m) * 255)), alpha}
}

func adjustHSL(img image.Image, item transformation) image.Image {
	hue := item.number(0, "hue")
	saturation := item.number(1, "saturation")
	lightness := item.number(1, "lightness")
	return mapNRGBA(img, func(pixel color.NRGBA) color.NRGBA {
		h, s, l := rgbToHSL(pixel)
		return hslToRGB(h+hue*360, s*saturation, l*lightness, pixel.A)
	})
}

func adjustHSV(img image.Image, item transformation) image.Image {
	hue := item.number(0, "hue")
	saturation := item.number(1, "saturation")
	value := item.number(1, "value")
	return mapNRGBA(img, func(pixel color.NRGBA) color.NRGBA {
		h, s, v := rgbToHSV(pixel)
		return hsvToRGB(h+hue*360, s*saturation, v*value, pixel.A)
	})
}

func monoHue(img image.Image, hue float64) image.Image {
	return mapNRGBA(img, func(pixel color.NRGBA) color.NRGBA {
		_, saturation, lightness := rgbToHSL(pixel)
		return hslToRGB(hue, saturation, lightness, pixel.A)
	})
}

func featherFactor(distance, tolerance, feather float64) float64 {
	if distance <= tolerance {
		return 0
	}
	if feather <= 0 || distance >= tolerance+feather {
		return 1
	}
	return (distance - tolerance) / feather
}

func chromaKey(img image.Image, item transformation) image.Image {
	targetHue := item.number(120, "hue")
	hueTolerance := item.number(0.083, "huetolerance")
	hueFeather := item.number(0.083, "huefeather")
	saturationTolerance := item.number(0.75, "saturationtolerance")
	saturationFeather := item.number(0.1, "saturationfeather")
	lightnessTolerance := item.number(0.75, "lightnesstolerance")
	lightnessFeather := item.number(0.1, "lightnessfeather")
	return mapNRGBA(img, func(pixel color.NRGBA) color.NRGBA {
		hue, saturation, lightness := rgbToHSL(pixel)
		hueDistance := math.Abs(hue-targetHue) / 360
		hueDistance = math.Min(hueDistance, 1-hueDistance)
		hueAlpha := featherFactor(hueDistance, hueTolerance, hueFeather)
		saturationAlpha := featherFactor(1-saturation, 1-saturationTolerance, saturationFeather)
		lightnessDistance := math.Abs(lightness - 0.5)
		lightnessAlpha := featherFactor(lightnessDistance, lightnessTolerance/2, lightnessFeather/2)
		pixel.A = uint8(float64(pixel.A) * math.Max(hueAlpha, math.Max(saturationAlpha, lightnessAlpha)))
		return pixel
	})
}

func removeColor(img image.Image, item transformation) (image.Image, error) {
	target, err := parseHexColor(firstNonEmpty(item.arg("color"), item.Value))
	if err != nil {
		return nil, err
	}
	tolerance := clamp(item.number(0.2, "tolerance"), 0, 1)
	feather := clamp(item.number(0, "feather"), 0, 1)
	return mapNRGBA(img, func(pixel color.NRGBA) color.NRGBA {
		dr := float64(pixel.R) - float64(target.R)
		dg := float64(pixel.G) - float64(target.G)
		db := float64(pixel.B) - float64(target.B)
		distance := math.Sqrt(dr*dr+dg*dg+db*db) / math.Sqrt(3*255*255)
		pixel.A = uint8(float64(pixel.A) * featherFactor(distance, tolerance, feather))
		return pixel
	}), nil
}

func maxColors(img image.Image, count int) image.Image {
	count = max(2, min(count, 256))
	levels := max(2, int(math.Floor(math.Cbrt(float64(count)))))
	quantize := func(value uint8) uint8 {
		return uint8(math.Round(float64(value)/255*float64(levels-1)) * 255 / float64(levels-1))
	}
	return mapNRGBA(img, func(pixel color.NRGBA) color.NRGBA {
		pixel.R, pixel.G, pixel.B = quantize(pixel.R), quantize(pixel.G), quantize(pixel.B)
		return pixel
	})
}

// flattenAlpha composites the image onto white for encoders without an alpha
// channel.
func flattenAlpha(img image.Image) image.Image {
	if opaque, ok := img.(interface{ Opaque() bool }); ok && opaque.Opaque() {
		return img
	}
	bounds := img.Bounds()
	result := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(result, result.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(result, result.Bounds(), img, bounds.Min, draw.Over)
	return result
}
