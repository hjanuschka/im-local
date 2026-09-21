package main

// paramSpec describes one tunable argument of a transformation so the
// documentation page can render a control for it.
type paramSpec struct {
	Name    string   `json:"name"`
	Label   string   `json:"label"`
	Kind    string   `json:"kind"` // range, number, color, text, select, flag
	Min     float64  `json:"min,omitempty"`
	Max     float64  `json:"max,omitempty"`
	Step    float64  `json:"step,omitempty"`
	Default string   `json:"default,omitempty"`
	Options []string `json:"options,omitempty"`
}

// transformSpec is the tunable description of a single transformation.
type transformSpec struct {
	Name   string      `json:"name"`
	Label  string      `json:"label"`
	Doc    string      `json:"doc"`
	Params []paramSpec `json:"params"`
}

func numberParam(name, label string, minimum, maximum, step float64, initial string) paramSpec {
	return paramSpec{Name: name, Label: label, Kind: "range", Min: minimum, Max: maximum, Step: step, Default: initial}
}

func gravityParam() paramSpec {
	return paramSpec{Name: "gravity", Label: "Gravity", Kind: "select", Default: "Center",
		Options: []string{"Center", "North", "South", "East", "West", "NorthEast", "NorthWest", "SouthEast", "SouthWest"}}
}

// transformSpecs powers the playground. The defaults mirror the defaults of the
// transformations themselves.
func transformSpecs() []transformSpec {
	return []transformSpec{
		{"Resize", "Resize", "Resize to a width and height.", []paramSpec{
			numberParam("width", "Width", 0, 2000, 10, "600"),
			numberParam("height", "Height", 0, 2000, 10, "0"),
		}},
		{"Scale", "Scale", "Scale relative to the current size.", []paramSpec{
			numberParam("width", "Width factor", 0.05, 4, 0.05, "0.5"),
			numberParam("height", "Height factor", 0.05, 4, 0.05, "0.5"),
		}},
		{"FitAndFill", "Fit and Fill", "Fit into a box and fill the rest with a blurred copy.", []paramSpec{
			numberParam("width", "Width", 10, 2000, 10, "600"),
			numberParam("height", "Height", 10, 2000, 10, "600"),
		}},
		{"Crop", "Crop", "Cut out a rectangle.", []paramSpec{
			numberParam("width", "Width", 10, 2000, 10, "400"),
			numberParam("height", "Height", 10, 2000, 10, "400"),
			gravityParam(),
			{Name: "allowExpansion", Label: "Allow expansion", Kind: "flag"},
		}},
		{"AspectCrop", "Aspect Crop", "Crop or expand to an aspect ratio.", []paramSpec{
			numberParam("width", "Ratio width", 1, 32, 1, "16"),
			numberParam("height", "Ratio height", 1, 32, 1, "9"),
			numberParam("xPosition", "Horizontal offset", 0, 1, 0.05, "0.5"),
			numberParam("yPosition", "Vertical offset", 0, 1, 0.05, "0.5"),
			{Name: "allowExpansion", Label: "Allow expansion", Kind: "flag"},
		}},
		{"RelativeCrop", "Relative Crop", "Shrink or expand each edge.", []paramSpec{
			numberParam("north", "North", -500, 500, 5, "0"),
			numberParam("south", "South", -500, 500, 5, "0"),
			numberParam("east", "East", -500, 500, 5, "0"),
			numberParam("west", "West", -500, 500, 5, "0"),
		}},
		{"RegionOfInterestCrop", "Region of Interest Crop", "Crop around a focal point.", []paramSpec{
			numberParam("width", "Width", 10, 2000, 10, "400"),
			numberParam("height", "Height", 10, 2000, 10, "400"),
			{Name: "regionOfInterest", Label: "Focal point (x,y)", Kind: "text", Default: "(300,200)"},
		}},
		{"FeatureCrop", "Feature Crop", "Crop around prominent features.", []paramSpec{
			numberParam("width", "Width", 10, 2000, 10, "400"),
			numberParam("height", "Height", 10, 2000, 10, "400"),
		}},
		{"FaceCrop", "Face Crop", "Crop around detected faces.", []paramSpec{
			numberParam("width", "Width", 10, 2000, 10, "400"),
			numberParam("height", "Height", 10, 2000, 10, "400"),
		}},
		{"SmartCrop", "Smart Crop", "Faces when present, features otherwise.", []paramSpec{
			numberParam("width", "Width", 10, 2000, 10, "400"),
			numberParam("height", "Height", 10, 2000, 10, "400"),
		}},
		{"Trim", "Trim", "Trim uniform edges.", []paramSpec{
			numberParam("tolerance", "Tolerance", 0, 1, 0.01, "0.02"),
		}},
		{"Rotate", "Rotate", "Rotate around the centre.", []paramSpec{
			numberParam("degrees", "Degrees", -180, 180, 1, "15"),
		}},
		{"Mirror", "Mirror", "Flip the image.", []paramSpec{
			{Name: "direction", Label: "Direction", Kind: "select", Default: "horizontal", Options: []string{"horizontal", "vertical"}},
		}},
		{"Shear", "Shear", "Slant into a parallelogram.", []paramSpec{
			numberParam("x", "Horizontal", -1, 1, 0.05, "0.3"),
			numberParam("y", "Vertical", -1, 1, 0.05, "0"),
		}},
		{"Goop", "Goop", "Distort the image.", []paramSpec{
			numberParam("chaos", "Chaos", 0, 1, 0.05, "0.5"),
		}},
		{"Blur", "Blur", "Gaussian blur.", []paramSpec{
			numberParam("sigma", "Strength", 0, 20, 0.5, "4"),
		}},
		{"UnsharpMask", "Unsharp Mask", "Sharpen edges.", []paramSpec{
			numberParam("gain", "Gain", 0, 10, 0.1, "2"),
		}},
		{"Contrast", "Contrast", "Multiply the contrast.", []paramSpec{
			numberParam("contrast", "Contrast", 0, 3, 0.05, "1.5"),
		}},
		{"Grayscale", "Grayscale", "Shades of gray.", nil},
		{"HSL", "Hue/Saturation/Lightness", "Adjust colours with HSL.", []paramSpec{
			numberParam("hue", "Hue rotation", -1, 1, 0.01, "0.1"),
			numberParam("saturation", "Saturation", 0, 3, 0.05, "1.5"),
			numberParam("lightness", "Lightness", 0, 3, 0.05, "1"),
		}},
		{"HSV", "Hue/Saturation/Value", "Adjust colours with HSV.", []paramSpec{
			numberParam("hue", "Hue rotation", -1, 1, 0.01, "0.1"),
			numberParam("saturation", "Saturation", 0, 3, 0.05, "1.5"),
			numberParam("value", "Value", 0, 3, 0.05, "1"),
		}},
		{"MonoHue", "Mono Hue", "Force a single hue.", []paramSpec{
			numberParam("hue", "Hue (degrees)", 0, 360, 1, "210"),
		}},
		{"MaxColors", "Max Colors", "Reduce the palette.", []paramSpec{
			numberParam("colors", "Colours", 2, 256, 1, "16"),
		}},
		{"BackgroundColor", "Background Color", "Fill transparency with a colour.", []paramSpec{
			{Name: "color", Label: "Colour", Kind: "color", Default: "0b7285"},
		}},
		{"Opacity", "Opacity", "Multiply the opacity.", []paramSpec{
			numberParam("opacity", "Opacity", 0, 2, 0.05, "0.5"),
		}},
		{"ChromaKey", "Chroma Key", "Remove a hue range.", []paramSpec{
			numberParam("hue", "Hue (degrees)", 0, 360, 1, "120"),
			numberParam("hueTolerance", "Hue tolerance", 0, 1, 0.01, "0.083"),
			numberParam("hueFeather", "Hue feather", 0, 1, 0.01, "0.083"),
			numberParam("saturationTolerance", "Saturation tolerance", 0, 1, 0.05, "0.75"),
			numberParam("lightnessTolerance", "Lightness tolerance", 0, 1, 0.05, "0.75"),
		}},
		{"RemoveColor", "Remove Color", "Remove one colour.", []paramSpec{
			{Name: "color", Label: "Colour", Kind: "color", Default: "ffffff"},
			numberParam("tolerance", "Tolerance", 0, 1, 0.01, "0.2"),
			numberParam("feather", "Feather", 0, 1, 0.01, "0.05"),
		}},
		{"BackgroundRemove", "Background Remove", "Cut the subject out with a local segmentation model.", []paramSpec{
			{Name: "model", Label: "Model", Kind: "select", Default: "isnet-general-use",
				Options: []string{"isnet-general-use", "u2net", "silueta", "u2netp"}},
			numberParam("threshold", "Threshold", 0, 1, 0.05, "0"),
			numberParam("feather", "Feather", 0, 1, 0.05, "0.1"),
			{Name: "method", Label: "Method", Kind: "select", Default: "model", Options: []string{"model", "color"}},
			{Name: "fallback", Label: "Fallback", Kind: "select", Default: "", Options: []string{"", "color"}},
			{Name: "refine", Label: "Edge refinement", Kind: "select", Default: "1", Options: []string{"1", "0"}},
			{Name: "decontaminate", Label: "Remove colour bleed", Kind: "select", Default: "1", Options: []string{"1", "0"}},
		}},
		{"Composite", "Composite", "Overlay or underlay another image.", []paramSpec{
			{Name: "image", Label: "Image URL", Kind: "text", Default: "(url={sample})"},
			gravityParam(),
			numberParam("scale", "Scale", 0.05, 2, 0.05, "0.25"),
			numberParam("opacity", "Opacity", 0, 1, 0.05, "1"),
			{Name: "placement", Label: "Placement", Kind: "select", Default: "over", Options: []string{"over", "under"}},
		}},
		{"Append", "Append", "Place another image beside this one.", []paramSpec{
			{Name: "image", Label: "Image URL", Kind: "text", Default: "(url={sample})"},
			{Name: "gravity", Label: "Direction", Kind: "select", Default: "East", Options: []string{"East", "South"}},
		}},
	}
}
