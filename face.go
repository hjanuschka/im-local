package main

import (
	"image"
	"log"
	"math"

	"github.com/disintegration/imaging"
	pigo "github.com/esimov/pigo/core"
)

// Detection defaults. The scan is dense (small shift and scale steps) because
// coarse steps make the cascade report weak, badly placed detections on large
// images, and it runs on a downscaled copy to keep that affordable.
const (
	detectionMaxSide     = 900
	defaultMinQuality    = 5.0
	defaultFacePadding   = 0.4
	detectionShiftFactor = 0.05
	detectionScaleFactor = 1.05
	detectionIOU         = 0.2
)

// face is a detected face in image coordinates.
type face struct {
	X, Y  int // centre
	Size  int // diameter
	Score float32
}

func (f face) rect() image.Rectangle {
	radius := f.Size / 2
	return image.Rect(f.X-radius, f.Y-radius, f.X+radius, f.Y+radius)
}

// detectFaces returns the faces whose detection quality reaches minQuality.
func (ip *ImageProcessor) detectFaces(img image.Image, minQuality float64) []face {
	if ip.classifier == nil {
		return nil
	}

	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width == 0 || height == 0 {
		return nil
	}

	// Detect on a bounded copy, then map the results back.
	scale := 1.0
	detectionImage := img
	if longest := max(width, height); longest > detectionMaxSide {
		scale = float64(detectionMaxSide) / float64(longest)
		detectionImage = imaging.Resize(img, int(float64(width)*scale), int(float64(height)*scale), imaging.Linear)
	}

	detectionBounds := detectionImage.Bounds()
	detectionWidth, detectionHeight := detectionBounds.Dx(), detectionBounds.Dy()
	shortest := min(detectionWidth, detectionHeight)

	params := pigo.CascadeParams{
		// A face smaller than 4% of the image is noise for cropping purposes,
		// and the maximum is the whole frame so close-ups still match.
		MinSize:     max(20, shortest*4/100),
		MaxSize:     shortest,
		ShiftFactor: detectionShiftFactor,
		ScaleFactor: detectionScaleFactor,
		ImageParams: pigo.ImageParams{
			Pixels: RgbToGrayscale(detectionImage),
			Rows:   detectionHeight,
			Cols:   detectionWidth,
			Dim:    detectionWidth,
		},
	}

	detections := ip.classifier.ClusterDetections(ip.classifier.RunCascade(params, 0.0), detectionIOU)

	result := make([]face, 0, len(detections))
	for _, detection := range detections {
		if float64(detection.Q) < minQuality {
			continue
		}
		result = append(result, face{
			X:     bounds.Min.X + int(float64(detection.Col)/scale),
			Y:     bounds.Min.Y + int(float64(detection.Row)/scale),
			Size:  int(float64(detection.Scale) / scale),
			Score: detection.Q,
		})
	}
	return result
}

// FaceCrop crops around detected faces, falling back to FeatureCrop when the
// image has none.
func (ip *ImageProcessor) FaceCrop(img image.Image, width, height int) (image.Image, error) {
	return ip.faceCrop(img, width, height, defaultMinQuality, defaultFacePadding, false)
}

func (ip *ImageProcessor) faceCrop(img image.Image, width, height int, minQuality, padding float64, biggestOnly bool) (image.Image, error) {
	faces := ip.detectFaces(img, minQuality)
	if len(faces) == 0 {
		log.Printf("No faces detected, falling back to feature crop")
		return ip.FeatureCrop(img, width, height)
	}

	if biggestOnly {
		biggest := faces[0]
		for _, candidate := range faces[1:] {
			if candidate.Size > biggest.Size {
				biggest = candidate
			}
		}
		faces = []face{biggest}
	}

	region := faces[0].rect()
	largest := faces[0].Size
	for _, current := range faces[1:] {
		region = region.Union(current.rect())
		largest = max(largest, current.Size)
	}
	log.Printf("Face crop around %d face(s) at %v (strongest score %.2f)", len(faces), region, faces[0].Score)

	// Pad the region, biased upwards so hair and forehead stay in frame.
	pad := int(float64(largest) * padding)
	region = image.Rect(region.Min.X-pad, region.Min.Y-pad-pad/2, region.Max.X+pad, region.Max.Y+pad)

	return cropRegionToSize(img, region, width, height)
}

// cropRegionToSize expands a region of interest to the requested aspect ratio,
// keeps it inside the image, and scales it to the requested size.
func cropRegionToSize(img image.Image, region image.Rectangle, width, height int) (image.Image, error) {
	bounds := img.Bounds()
	if width <= 0 && height <= 0 {
		width, height = region.Dx(), region.Dy()
	} else if width <= 0 {
		width = int(math.Round(float64(height) * float64(region.Dx()) / float64(region.Dy())))
	} else if height <= 0 {
		height = int(math.Round(float64(width) * float64(region.Dy()) / float64(region.Dx())))
	}

	targetRatio := float64(width) / float64(height)
	cropWidth, cropHeight := region.Dx(), region.Dy()
	if float64(cropWidth)/float64(cropHeight) < targetRatio {
		cropWidth = int(math.Round(float64(cropHeight) * targetRatio))
	} else {
		cropHeight = int(math.Round(float64(cropWidth) / targetRatio))
	}

	// Never scale a crop beyond the source, it only wastes pixels.
	if cropWidth > bounds.Dx() {
		cropWidth = bounds.Dx()
		cropHeight = int(math.Round(float64(cropWidth) / targetRatio))
	}
	if cropHeight > bounds.Dy() {
		cropHeight = bounds.Dy()
		cropWidth = int(math.Round(float64(cropHeight) * targetRatio))
	}

	centerX, centerY := (region.Min.X+region.Max.X)/2, (region.Min.Y+region.Max.Y)/2
	x := clampInt(centerX-cropWidth/2, bounds.Min.X, bounds.Max.X-cropWidth)
	y := clampInt(centerY-cropHeight/2, bounds.Min.Y, bounds.Max.Y-cropHeight)

	cropped := imaging.Crop(img, image.Rect(x, y, x+cropWidth, y+cropHeight))
	return imaging.Resize(cropped, width, height, imaging.Lanczos), nil
}

func clampInt(value, low, high int) int {
	if high < low {
		return low
	}
	return max(low, min(value, high))
}

// RgbToGrayscale converts an image to the grayscale buffer pigo expects.
func RgbToGrayscale(img image.Image) []uint8 {
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	grayscale := make([]uint8, width*height)

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r, g, b, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			grayscale[y*width+x] = uint8((0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 256)
		}
	}
	return grayscale
}
