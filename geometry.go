package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"strings"

	"github.com/disintegration/imaging"
)

func aspectCrop(img image.Image, item transformation) (image.Image, error) {
	ratioWidth, ratioHeight := item.number(1, "width"), item.number(1, "height")
	if values := parseNumbers(item.Value); len(values) >= 2 {
		ratioWidth, ratioHeight = values[0], values[1]
	}
	if ratioWidth <= 0 || ratioHeight <= 0 {
		return nil, fmt.Errorf("aspect width and height must be positive")
	}

	bounds := img.Bounds()
	originalWidth, originalHeight := bounds.Dx(), bounds.Dy()
	targetRatio := ratioWidth / ratioHeight
	xOffset := clamp(item.number(0.5, "xposition", "horizontaloffset"), 0, 1)
	yOffset := clamp(item.number(0.5, "yposition", "verticaloffset"), 0, 1)
	allowExpansion := item.Flags["allowexpansion"]

	if allowExpansion {
		canvasWidth, canvasHeight := originalWidth, originalHeight
		if float64(originalWidth)/float64(originalHeight) > targetRatio {
			canvasHeight = int(math.Ceil(float64(originalWidth) / targetRatio))
		} else {
			canvasWidth = int(math.Ceil(float64(originalHeight) * targetRatio))
		}
		canvas := image.NewNRGBA(image.Rect(0, 0, canvasWidth, canvasHeight))
		x := int(float64(canvasWidth-originalWidth) * xOffset)
		y := int(float64(canvasHeight-originalHeight) * yOffset)
		draw.Draw(canvas, image.Rect(x, y, x+originalWidth, y+originalHeight), img, bounds.Min, draw.Over)
		return canvas, nil
	}

	cropWidth, cropHeight := originalWidth, originalHeight
	if float64(originalWidth)/float64(originalHeight) > targetRatio {
		cropWidth = int(math.Round(float64(originalHeight) * targetRatio))
	} else {
		cropHeight = int(math.Round(float64(originalWidth) / targetRatio))
	}
	x := bounds.Min.X + int(float64(originalWidth-cropWidth)*xOffset)
	y := bounds.Min.Y + int(float64(originalHeight-cropHeight)*yOffset)
	return imaging.Crop(img, image.Rect(x, y, x+cropWidth, y+cropHeight)), nil
}

func crop(img image.Image, item transformation) (image.Image, error) {
	bounds := img.Bounds()
	x, y := int(item.number(0, "xposition", "x")), int(item.number(0, "yposition", "y"))
	width, height := dimensions(item)
	if values := parseNumbers(item.arg("rect")); len(values) >= 4 {
		x, y, width, height = int(values[0]), int(values[1]), int(values[2]), int(values[3])
	}
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("width and height must be positive")
	}

	gravity := normalizeName(item.arg("gravity"))
	switch gravity {
	case "center":
		x, y = (bounds.Dx()-width)/2, (bounds.Dy()-height)/2
	case "northeast", "topright":
		x, y = bounds.Dx()-width, 0
	case "southeast", "bottomright":
		x, y = bounds.Dx()-width, bounds.Dy()-height
	case "southwest", "bottomleft":
		x, y = 0, bounds.Dy()-height
	case "north", "top":
		x, y = (bounds.Dx()-width)/2, 0
	case "south", "bottom":
		x, y = (bounds.Dx()-width)/2, bounds.Dy()-height
	case "east", "right":
		x, y = bounds.Dx()-width, (bounds.Dy()-height)/2
	case "west", "left":
		x, y = 0, (bounds.Dy()-height)/2
	}

	if item.Flags["allowexpansion"] {
		canvas := image.NewNRGBA(image.Rect(0, 0, width, height))
		draw.Draw(canvas, canvas.Bounds(), img, image.Pt(bounds.Min.X+x, bounds.Min.Y+y), draw.Over)
		return canvas, nil
	}
	x = max(0, min(x, bounds.Dx()-width))
	y = max(0, min(y, bounds.Dy()-height))
	width = min(width, bounds.Dx())
	height = min(height, bounds.Dy())
	return imaging.Crop(img, image.Rect(bounds.Min.X+x, bounds.Min.Y+y, bounds.Min.X+x+width, bounds.Min.Y+y+height)), nil
}

func regionOfInterestCrop(img image.Image, item transformation) (image.Image, error) {
	width, height := dimensions(item)
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("width and height must be positive")
	}
	bounds := img.Bounds()
	centerX, centerY := bounds.Dx()/2, bounds.Dy()/2
	roi := item.arg("regionofinterest")
	if values := parseNumbers(roi); len(values) >= 2 {
		centerX, centerY = int(values[0]), int(values[1])
	} else if roi != "" {
		// Accept regionOfInterest=(anchor=(x=900,y=150),width=50,height=50).
		flat := strings.NewReplacer("anchor=(", "", ")", "").Replace(roi)
		for _, part := range splitTopLevel(flat, ',') {
			key, value, ok := splitAssignment(part)
			if !ok {
				continue
			}
			number, _ := strconvAtoi(value)
			switch normalizeName(key) {
			case "x":
				centerX = number
			case "y":
				centerY = number
			}
		}
	}
	x := max(0, min(centerX-width/2, bounds.Dx()-width))
	y := max(0, min(centerY-height/2, bounds.Dy()-height))
	width, height = min(width, bounds.Dx()), min(height, bounds.Dy())
	return imaging.Crop(img, image.Rect(bounds.Min.X+x, bounds.Min.Y+y, bounds.Min.X+x+width, bounds.Min.Y+y+height)), nil
}

func strconvAtoi(value string) (int, error) {
	parsed := parseNumbers(value)
	if len(parsed) == 0 {
		return 0, fmt.Errorf("invalid number %q", value)
	}
	return int(parsed[0]), nil
}

func relativeCrop(img image.Image, item transformation) image.Image {
	bounds := img.Bounds()
	west := int(item.number(0, "west"))
	east := int(item.number(0, "east"))
	north := int(item.number(0, "north"))
	south := int(item.number(0, "south"))
	newWidth := max(1, bounds.Dx()-west-east)
	newHeight := max(1, bounds.Dy()-north-south)
	canvas := image.NewNRGBA(image.Rect(0, 0, newWidth, newHeight))
	draw.Draw(canvas, canvas.Bounds(), img, image.Pt(bounds.Min.X+west, bounds.Min.Y+north), draw.Over)
	return canvas
}

func fitAndFill(img image.Image, width, height int) image.Image {
	if width <= 0 || height <= 0 {
		return img
	}
	background := imaging.Blur(imaging.Fill(img, width, height, imaging.Center, imaging.Lanczos), 20)
	foreground := imaging.Fit(img, width, height, imaging.Lanczos)
	x := (width - foreground.Bounds().Dx()) / 2
	y := (height - foreground.Bounds().Dy()) / 2
	return imaging.Overlay(background, foreground, image.Pt(x, y), 1)
}

func scaleImage(img image.Image, item transformation) image.Image {
	widthFactor := item.number(1, "width")
	heightFactor := item.number(1, "height")
	return imaging.Resize(img,
		max(1, int(math.Round(float64(img.Bounds().Dx())*widthFactor))),
		max(1, int(math.Round(float64(img.Bounds().Dy())*heightFactor))), imaging.Lanczos)
}

func shear(img image.Image, item transformation) image.Image {
	xFactor := item.number(0, "x", "horizontal", "width")
	yFactor := item.number(0, "y", "vertical", "height")
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	xShift := int(math.Ceil(math.Abs(xFactor) * float64(height)))
	yShift := int(math.Ceil(math.Abs(yFactor) * float64(width)))
	result := image.NewNRGBA(image.Rect(0, 0, width+xShift, height+yShift))
	for y := 0; y < result.Bounds().Dy(); y++ {
		for x := 0; x < result.Bounds().Dx(); x++ {
			sourceX := float64(x) - math.Max(0, -xFactor*float64(height)) - xFactor*float64(y)
			sourceY := float64(y) - math.Max(0, -yFactor*float64(width)) - yFactor*sourceX
			if sourceX >= 0 && sourceX < float64(width) && sourceY >= 0 && sourceY < float64(height) {
				result.Set(x, y, img.At(bounds.Min.X+int(sourceX), bounds.Min.Y+int(sourceY)))
			}
		}
	}
	return result
}

func goop(img image.Image, chaos float64) image.Image {
	chaos = clamp(chaos, 0, 1)
	bounds := img.Bounds()
	result := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	amplitude := chaos * float64(min(bounds.Dx(), bounds.Dy())) * 0.08
	for y := 0; y < bounds.Dy(); y++ {
		for x := 0; x < bounds.Dx(); x++ {
			sx := float64(x) + amplitude*math.Sin(float64(y)*0.071+math.Sin(float64(x)*0.017))
			sy := float64(y) + amplitude*math.Sin(float64(x)*0.053+math.Cos(float64(y)*0.013))
			if sx >= 0 && sx < float64(bounds.Dx()) && sy >= 0 && sy < float64(bounds.Dy()) {
				result.Set(x, y, img.At(bounds.Min.X+int(sx), bounds.Min.Y+int(sy)))
			}
		}
	}
	return result
}

func trimImage(img image.Image, tolerance float64) image.Image {
	bounds := img.Bounds()
	base := color.NRGBAModel.Convert(img.At(bounds.Min.X, bounds.Min.Y)).(color.NRGBA)
	different := func(candidate color.Color) bool {
		current := color.NRGBAModel.Convert(candidate).(color.NRGBA)
		distance := math.Abs(float64(current.R)-float64(base.R)) + math.Abs(float64(current.G)-float64(base.G)) + math.Abs(float64(current.B)-float64(base.B)) + math.Abs(float64(current.A)-float64(base.A))
		return distance/(4*255) > tolerance
	}
	left, top, right, bottom := bounds.Max.X, bounds.Max.Y, bounds.Min.X, bounds.Min.Y
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if different(img.At(x, y)) {
				left, top, right, bottom = min(left, x), min(top, y), max(right, x+1), max(bottom, y+1)
			}
		}
	}
	if left >= right || top >= bottom {
		return img
	}
	return imaging.Crop(img, image.Rect(left, top, right, bottom))
}
