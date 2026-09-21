package main

import (
	"image"
	"image/color"
	"math"
)

// mask is a single channel float image in the range [0,1].
type mask struct {
	width, height int
	data          []float32
}

func newMask(width, height int) *mask {
	return &mask{width: width, height: height, data: make([]float32, width*height)}
}

func (m *mask) at(x, y int) float32 { return m.data[y*m.width+x] }

// boxSum returns the mean of each (2r+1)² window using a summed-area table, so
// the cost does not depend on the radius.
func boxMean(values []float32, width, height, radius int) []float32 {
	integral := make([]float64, (width+1)*(height+1))
	for y := 0; y < height; y++ {
		rowSum := 0.0
		for x := 0; x < width; x++ {
			rowSum += float64(values[y*width+x])
			integral[(y+1)*(width+1)+x+1] = integral[y*(width+1)+x+1] + rowSum
		}
	}

	result := make([]float32, width*height)
	for y := 0; y < height; y++ {
		top := max(0, y-radius)
		bottom := min(height, y+radius+1)
		for x := 0; x < width; x++ {
			left := max(0, x-radius)
			right := min(width, x+radius+1)
			sum := integral[bottom*(width+1)+right] - integral[top*(width+1)+right] -
				integral[bottom*(width+1)+left] + integral[top*(width+1)+left]
			result[y*width+x] = float32(sum / float64((bottom-top)*(right-left)))
		}
	}
	return result
}

// guidedFilter snaps a soft mask to the edges of a guide image. It is the cheap
// stand-in for alpha matting: the segmentation mask comes back from the model at
// a lower resolution, and this pulls its edges onto the real ones.
func guidedFilter(guide, input *mask, radius int, epsilon float32) *mask {
	width, height := input.width, input.height
	size := width * height

	guideInput := make([]float32, size)
	guideSquared := make([]float32, size)
	for index := 0; index < size; index++ {
		guideInput[index] = guide.data[index] * input.data[index]
		guideSquared[index] = guide.data[index] * guide.data[index]
	}

	meanGuide := boxMean(guide.data, width, height, radius)
	meanInput := boxMean(input.data, width, height, radius)
	meanGuideInput := boxMean(guideInput, width, height, radius)
	meanGuideSquared := boxMean(guideSquared, width, height, radius)

	scale := make([]float32, size)
	offset := make([]float32, size)
	for index := 0; index < size; index++ {
		variance := meanGuideSquared[index] - meanGuide[index]*meanGuide[index]
		covariance := meanGuideInput[index] - meanGuide[index]*meanInput[index]
		scale[index] = covariance / (variance + epsilon)
		offset[index] = meanInput[index] - scale[index]*meanGuide[index]
	}

	meanScale := boxMean(scale, width, height, radius)
	meanOffset := boxMean(offset, width, height, radius)

	result := newMask(width, height)
	for index := 0; index < size; index++ {
		result.data[index] = float32(clamp(float64(meanScale[index]*guide.data[index]+meanOffset[index]), 0, 1))
	}
	return result
}

// luminanceMask extracts the guide channel used by the guided filter.
func luminanceMask(img image.Image) *mask {
	bounds := img.Bounds()
	result := newMask(bounds.Dx(), bounds.Dy())
	for y := 0; y < result.height; y++ {
		for x := 0; x < result.width; x++ {
			r, g, b, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			result.data[y*result.width+x] = float32(0.299*float64(r)+0.587*float64(g)+0.114*float64(b)) / 65535
		}
	}
	return result
}

// composeCutout applies the alpha mask, optionally removing the background
// colour that bleeds into partially transparent pixels.
//
// For a pixel I = a*F + (1-a)*B, the foreground F is recovered as
// (I - (1-a)*B)/a, with B estimated from the surrounding background-weighted
// average. Without this the cut-out keeps a halo of the old background.
func composeCutout(img image.Image, alpha *mask, decontaminate bool) *image.NRGBA {
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	result := image.NewNRGBA(image.Rect(0, 0, width, height))

	var backgroundR, backgroundG, backgroundB []float32
	if decontaminate {
		weights := make([]float32, width*height)
		channelR := make([]float32, width*height)
		channelG := make([]float32, width*height)
		channelB := make([]float32, width*height)
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				index := y*width + x
				pixel := color.NRGBAModel.Convert(img.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.NRGBA)
				weight := 1 - alpha.data[index]
				weights[index] = weight
				channelR[index] = weight * float32(pixel.R) / 255
				channelG[index] = weight * float32(pixel.G) / 255
				channelB[index] = weight * float32(pixel.B) / 255
			}
		}

		radius := max(4, min(width, height)/60)
		meanWeight := boxMean(weights, width, height, radius)
		backgroundR = boxMean(channelR, width, height, radius)
		backgroundG = boxMean(channelG, width, height, radius)
		backgroundB = boxMean(channelB, width, height, radius)
		for index := range meanWeight {
			weight := meanWeight[index] + 1e-4
			backgroundR[index] /= weight
			backgroundG[index] /= weight
			backgroundB[index] /= weight
		}
	}

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			index := y*width + x
			pixel := color.NRGBAModel.Convert(img.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.NRGBA)
			coverage := alpha.data[index]

			if decontaminate && coverage > 0.01 && coverage < 0.99 {
				pixel.R = unmix(pixel.R, backgroundR[index], coverage)
				pixel.G = unmix(pixel.G, backgroundG[index], coverage)
				pixel.B = unmix(pixel.B, backgroundB[index], coverage)
			}
			pixel.A = uint8(clamp(float64(pixel.A)*float64(coverage), 0, 255))
			result.SetNRGBA(x, y, pixel)
		}
	}
	return result
}

func unmix(observed uint8, background, coverage float32) uint8 {
	// Below this coverage the division amplifies noise more than it helps.
	divisor := math.Max(float64(coverage), 0.2)
	value := (float64(observed)/255 - (1-float64(coverage))*float64(background)) / divisor
	return uint8(clamp(value*255, 0, 255))
}
