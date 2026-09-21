package main

import (
	"fmt"
	"image"
	"image/color"
	"math"
)

// backgroundSampleStep keeps the colour model cheap on large images by sampling
// every n-th border pixel.
const backgroundSampleStep = 2

type colorVector struct {
	r, g, b float64
}

func (v colorVector) distance(other colorVector) float64 {
	dr, dg, db := v.r-other.r, v.g-other.g, v.b-other.b
	return math.Sqrt(dr*dr+dg*dg+db*db) / math.Sqrt(3)
}

func toVector(pixel color.NRGBA) colorVector {
	return colorVector{float64(pixel.R) / 255, float64(pixel.G) / 255, float64(pixel.B) / 255}
}

// borderSamples collects the pixels along the image edges, which are assumed to
// be background for an automatic cut-out.
func borderSamples(img *image.NRGBA, margin int) []colorVector {
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	margin = max(1, min(margin, min(width, height)/2))

	var samples []colorVector
	for y := 0; y < height; y += backgroundSampleStep {
		for x := 0; x < width; x += backgroundSampleStep {
			if x >= margin && x < width-margin && y >= margin && y < height-margin {
				continue
			}
			samples = append(samples, toVector(img.NRGBAAt(x, y)))
		}
	}
	return samples
}

// kMeans groups the border colours so gradients and multi-tone backdrops are
// modelled by more than a single colour.
func kMeans(samples []colorVector, clusters, iterations int) ([]colorVector, []float64) {
	if len(samples) == 0 {
		return nil, nil
	}
	clusters = max(1, min(clusters, len(samples)))

	centers := make([]colorVector, clusters)
	for index := range centers {
		centers[index] = samples[index*len(samples)/clusters]
	}

	assignments := make([]int, len(samples))
	for iteration := 0; iteration < iterations; iteration++ {
		changed := false
		for sampleIndex, sample := range samples {
			best, bestDistance := 0, math.MaxFloat64
			for centerIndex, center := range centers {
				if distance := sample.distance(center); distance < bestDistance {
					best, bestDistance = centerIndex, distance
				}
			}
			if assignments[sampleIndex] != best {
				assignments[sampleIndex] = best
				changed = true
			}
		}

		sums := make([]colorVector, clusters)
		counts := make([]int, clusters)
		for sampleIndex, sample := range samples {
			cluster := assignments[sampleIndex]
			sums[cluster].r += sample.r
			sums[cluster].g += sample.g
			sums[cluster].b += sample.b
			counts[cluster]++
		}
		for centerIndex := range centers {
			if counts[centerIndex] == 0 {
				continue
			}
			count := float64(counts[centerIndex])
			centers[centerIndex] = colorVector{sums[centerIndex].r / count, sums[centerIndex].g / count, sums[centerIndex].b / count}
		}
		if !changed {
			break
		}
	}

	support := make([]float64, clusters)
	for _, assignment := range assignments {
		support[assignment] += 1 / float64(len(samples))
	}
	return centers, support
}

// removeBackground cuts the subject out of an image without any external
// service or model. The background colour model is learned from the image
// border, and only background-coloured regions that are connected to the border
// are erased, so matching colours inside the subject survive.
func removeBackground(img image.Image, item transformation) image.Image {
	tolerance := clamp(item.number(0.12, "tolerance"), 0, 1)
	feather := clamp(item.number(0.08, "feather"), 0, 1)
	clusters := int(clamp(item.number(4, "colors", "clusters"), 1, 16))
	margin := int(item.number(2, "margin"))

	source := mapNRGBA(img, func(pixel color.NRGBA) color.NRGBA { return pixel })
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width == 0 || height == 0 {
		return source
	}

	centers, support := kMeans(borderSamples(source, margin), clusters, 12)
	if len(centers) == 0 {
		return source
	}

	// A subject that touches the border is sampled as background, so only
	// colours that dominate the border are treated as the backdrop.
	minimumSupport := clamp(item.number(0.25, "minsupport"), 0, 1)
	dominant := make([]colorVector, 0, len(centers))
	best := 0
	for index, share := range support {
		if share > support[best] {
			best = index
		}
		if share >= minimumSupport {
			dominant = append(dominant, centers[index])
		}
	}
	if len(dominant) == 0 {
		dominant = append(dominant, centers[best])
	}
	centers = dominant

	// alpha holds the cut-out opacity before connectivity is taken into account.
	alpha := make([]float64, width*height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			sample := toVector(source.NRGBAAt(x, y))
			nearest := math.MaxFloat64
			for _, center := range centers {
				if distance := sample.distance(center); distance < nearest {
					nearest = distance
				}
			}
			alpha[y*width+x] = featherFactor(nearest, tolerance, feather)
		}
	}

	connected := floodFillBackground(alpha, width, height)
	result := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			index := y*width + x
			pixel := source.NRGBAAt(x, y)
			if connected[index] {
				pixel.A = uint8(float64(pixel.A) * alpha[index])
			}
			result.SetNRGBA(x, y, pixel)
		}
	}
	return result
}

// floodFillBackground marks the partially transparent pixels that are reachable
// from the image border, so holes of background colour inside the subject stay
// opaque.
func floodFillBackground(alpha []float64, width, height int) []bool {
	visited := make([]bool, width*height)
	queue := make([]int, 0, width*2+height*2)

	push := func(x, y int) {
		index := y*width + x
		if visited[index] || alpha[index] >= 1 {
			return
		}
		visited[index] = true
		queue = append(queue, index)
	}

	for x := 0; x < width; x++ {
		push(x, 0)
		push(x, height-1)
	}
	for y := 0; y < height; y++ {
		push(0, y)
		push(width-1, y)
	}

	for cursor := 0; cursor < len(queue); cursor++ {
		index := queue[cursor]
		x, y := index%width, index/width
		if x > 0 {
			push(x-1, y)
		}
		if x < width-1 {
			push(x+1, y)
		}
		if y > 0 {
			push(x, y-1)
		}
		if y < height-1 {
			push(x, y+1)
		}
	}
	return visited
}

// refineMask sharpens the model mask against the image edges and applies the
// threshold and feather controls.
func refineMask(img image.Image, coverage *mask, threshold, feather float64, refine bool) *mask {
	if refine {
		radius := max(3, min(coverage.width, coverage.height)/100)
		coverage = guidedFilter(luminanceMask(img), coverage, radius, 1e-4)
	}

	low, high := 0.02, 0.98
	if threshold > 0 {
		half := math.Max(feather, 0.001) / 2
		low, high = threshold-half, threshold+half
	}

	result := newMask(coverage.width, coverage.height)
	for index, value := range coverage.data {
		result.data[index] = float32(clamp((float64(value)-low)/math.Max(high-low, 1e-6), 0, 1))
	}
	return result
}

// removeBackgroundML cuts the subject out with a locally executed segmentation
// model, the same class of model that hosted background removal services use.
func (ip *ImageProcessor) removeBackgroundML(img image.Image, item transformation) (image.Image, error) {
	if ip.segmenter == nil {
		return nil, fmt.Errorf("segmentation is not configured")
	}

	model := firstNonEmpty(item.arg("model"), item.Value, defaultSegmentationModel)
	coverage, err := ip.segmenter.mask(img, model)
	if err != nil {
		return nil, err
	}

	alpha := refineMask(img,
		coverage,
		clamp(item.number(0, "threshold"), 0, 1),
		clamp(item.number(0.1, "feather"), 0, 1),
		item.arg("refine") != "0",
	)
	return composeCutout(img, alpha, item.arg("decontaminate") != "0"), nil
}
