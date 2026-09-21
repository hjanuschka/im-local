package main

import (
	"fmt"
	"image"
	"image/color"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/disintegration/imaging"
	ort "github.com/yalue/onnxruntime_go"
)

// modelSpec describes a salient-object segmentation model from the rembg model
// zoo, together with the preprocessing it expects.
type modelSpec struct {
	Name string
	URL  string
	Size int
	Mean [3]float32
	Std  [3]float32
}

var segmentationModels = map[string]modelSpec{
	"isnet-general-use": {
		Name: "isnet-general-use",
		URL:  "https://github.com/danielgatis/rembg/releases/download/v0.0.0/isnet-general-use.onnx",
		Size: 1024,
		Mean: [3]float32{0.5, 0.5, 0.5},
		Std:  [3]float32{1, 1, 1},
	},
	"u2net": {
		Name: "u2net",
		URL:  "https://github.com/danielgatis/rembg/releases/download/v0.0.0/u2net.onnx",
		Size: 320,
		Mean: [3]float32{0.485, 0.456, 0.406},
		Std:  [3]float32{0.229, 0.224, 0.225},
	},
	"u2netp": {
		Name: "u2netp",
		URL:  "https://github.com/danielgatis/rembg/releases/download/v0.0.0/u2netp.onnx",
		Size: 320,
		Mean: [3]float32{0.485, 0.456, 0.406},
		Std:  [3]float32{0.229, 0.224, 0.225},
	},
	"silueta": {
		Name: "silueta",
		URL:  "https://github.com/danielgatis/rembg/releases/download/v0.0.0/silueta.onnx",
		Size: 320,
		Mean: [3]float32{0.485, 0.456, 0.406},
		Std:  [3]float32{0.229, 0.224, 0.225},
	},
}

const defaultSegmentationModel = "isnet-general-use"

// sharedLibraryCandidates are the usual install locations of the onnxruntime
// shared library. ONNXRUNTIME_SHARED_LIBRARY overrides the search.
var sharedLibraryCandidates = []string{
	"/usr/local/lib/libonnxruntime.so",
	"/usr/lib/libonnxruntime.so",
	"/usr/lib/x86_64-linux-gnu/libonnxruntime.so",
	"/usr/local/lib/libonnxruntime.dylib",
	"/opt/homebrew/lib/libonnxruntime.dylib",
	"/usr/local/lib/onnxruntime.dll",
}

type loadedModel struct {
	spec    modelSpec
	session *ort.DynamicAdvancedSession
}

// segmenter runs salient-object segmentation with onnxruntime. Both the shared
// library and the model weights are optional at build time; they are resolved
// on first use so the service starts without them.
type segmenter struct {
	modelDirectory string

	mutex       sync.Mutex
	environment error
	initialized bool
	models      map[string]*loadedModel
}

func newSegmenter(modelDirectory string) *segmenter {
	return &segmenter{modelDirectory: modelDirectory, models: make(map[string]*loadedModel)}
}

func findSharedLibrary() (string, error) {
	if path := os.Getenv("ONNXRUNTIME_SHARED_LIBRARY"); path != "" {
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("ONNXRUNTIME_SHARED_LIBRARY=%s is not readable: %w", path, err)
		}
		return path, nil
	}
	for _, candidate := range sharedLibraryCandidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("onnxruntime shared library not found; install it or set ONNXRUNTIME_SHARED_LIBRARY")
}

// initEnvironment loads the onnxruntime library exactly once.
func (s *segmenter) initEnvironment() error {
	if s.initialized {
		return s.environment
	}
	s.initialized = true

	library, err := findSharedLibrary()
	if err != nil {
		s.environment = err
		return err
	}
	ort.SetSharedLibraryPath(library)
	if err := ort.InitializeEnvironment(); err != nil {
		s.environment = fmt.Errorf("failed to initialize onnxruntime (%s): %w", library, err)
		return s.environment
	}
	log.Printf("onnxruntime initialized from %s", library)
	return nil
}

// modelPath returns the local file for a model, downloading it on first use.
func (s *segmenter) modelPath(spec modelSpec) (string, error) {
	if override := os.Getenv("IM_SEGMENTATION_MODEL_" + normalizeName(spec.Name)); override != "" {
		return override, nil
	}
	path := filepath.Join(s.modelDirectory, spec.Name+".onnx")
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		return path, nil
	}
	if os.Getenv("IM_SEGMENTATION_DOWNLOAD") == "0" {
		return "", fmt.Errorf("model %s is missing at %s and downloads are disabled", spec.Name, path)
	}

	if err := os.MkdirAll(s.modelDirectory, 0o755); err != nil {
		return "", err
	}
	log.Printf("downloading segmentation model %s from %s", spec.Name, spec.URL)

	response, err := http.Get(spec.URL)
	if err != nil {
		return "", fmt.Errorf("failed to download model %s: %w", spec.Name, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download model %s: %s", spec.Name, response.Status)
	}

	temporary, err := os.CreateTemp(s.modelDirectory, spec.Name+"-*.part")
	if err != nil {
		return "", err
	}
	defer os.Remove(temporary.Name())

	written, err := io.Copy(temporary, response.Body)
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", fmt.Errorf("failed to store model %s: %w", spec.Name, err)
	}
	if written < 1<<20 {
		return "", fmt.Errorf("downloaded model %s is only %d bytes", spec.Name, written)
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return "", err
	}
	log.Printf("segmentation model %s ready (%d bytes)", spec.Name, written)
	return path, nil
}

func (s *segmenter) load(name string) (*loadedModel, error) {
	spec, found := segmentationModels[name]
	if !found {
		return nil, fmt.Errorf("unknown segmentation model %q", name)
	}
	if model, cached := s.models[name]; cached {
		return model, nil
	}
	if err := s.initEnvironment(); err != nil {
		return nil, err
	}

	path, err := s.modelPath(spec)
	if err != nil {
		return nil, err
	}
	inputs, outputs, err := ort.GetInputOutputInfo(path)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect model %s: %w", name, err)
	}
	if len(inputs) == 0 || len(outputs) == 0 {
		return nil, fmt.Errorf("model %s has no inputs or outputs", name)
	}

	session, err := ort.NewDynamicAdvancedSession(path, []string{inputs[0].Name}, []string{outputs[0].Name}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to load model %s: %w", name, err)
	}
	model := &loadedModel{spec: spec, session: session}
	s.models[name] = model
	return model, nil
}

// mask runs the model and returns the segmentation mask at the size of img.
//
// The image is letterboxed into a square before inference instead of being
// squashed, because the models are trained on square inputs and distorting the
// aspect ratio costs edge accuracy.
func (s *segmenter) mask(img image.Image, name string) (*mask, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	model, err := s.load(name)
	if err != nil {
		return nil, err
	}
	side := model.spec.Size

	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	longest := max(width, height)
	scaledWidth := max(1, width*side/longest)
	scaledHeight := max(1, height*side/longest)
	offsetX, offsetY := (side-scaledWidth)/2, (side-scaledHeight)/2

	letterboxed := imaging.New(side, side, color.NRGBA{A: 255})
	letterboxed = imaging.Paste(letterboxed, imaging.Resize(img, scaledWidth, scaledHeight, imaging.Linear), image.Pt(offsetX, offsetY))

	input := make([]float32, 3*side*side)
	plane := side * side
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			pixel := letterboxed.NRGBAAt(x, y)
			offset := y*side + x
			input[offset] = (float32(pixel.R)/255 - model.spec.Mean[0]) / model.spec.Std[0]
			input[plane+offset] = (float32(pixel.G)/255 - model.spec.Mean[1]) / model.spec.Std[1]
			input[2*plane+offset] = (float32(pixel.B)/255 - model.spec.Mean[2]) / model.spec.Std[2]
		}
	}

	inputTensor, err := ort.NewTensor(ort.NewShape(1, 3, int64(side), int64(side)), input)
	if err != nil {
		return nil, err
	}
	defer inputTensor.Destroy()

	outputs := []ort.Value{nil}
	if err := model.session.Run([]ort.Value{inputTensor}, outputs); err != nil {
		return nil, fmt.Errorf("segmentation failed: %w", err)
	}
	outputTensor, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		outputs[0].Destroy()
		return nil, fmt.Errorf("unexpected model output type %T", outputs[0])
	}
	defer outputTensor.Destroy()

	prediction := outputTensor.GetData()
	if len(prediction) < plane {
		return nil, fmt.Errorf("model returned %d values, want at least %d", len(prediction), plane)
	}
	prediction = prediction[:plane]

	minimum, maximum := prediction[0], prediction[0]
	for _, value := range prediction {
		minimum = min32(minimum, value)
		maximum = max32(maximum, value)
	}
	span := maximum - minimum
	if span == 0 {
		span = 1
	}

	square := image.NewGray(image.Rect(0, 0, side, side))
	for index, value := range prediction {
		square.Pix[index] = uint8(clamp(float64((value-minimum)/span)*255, 0, 255))
	}

	// Undo the letterbox, then scale to the source size.
	cropped := imaging.Crop(square, image.Rect(offsetX, offsetY, offsetX+scaledWidth, offsetY+scaledHeight))
	scaled := imaging.Resize(cropped, width, height, imaging.CatmullRom)

	result := newMask(width, height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			result.data[y*width+x] = float32(scaled.NRGBAAt(x, y).R) / 255
		}
	}
	return result, nil
}

func min32(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}

func max32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}
