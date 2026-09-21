package main

import (
	_ "embed"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed assets/sample.jpg
var sampleImage []byte

//go:embed assets/sample-studio.jpg
var studioSampleImage []byte

//go:embed assets/facefinder
var embeddedCascade []byte

// samplePlaceholder is replaced with the absolute URL of the built-in sample
// image so the examples work on any host the service runs on.
const samplePlaceholder = "{sample}"

type docExample struct {
	Title       string
	Description string
	Query       string
	// Studio selects the flat-backdrop sample image instead of the default one.
	Studio bool
}

type docSection struct {
	Title    string
	Examples []docExample
}

// renderedExample is a docExample with the sample URL resolved.
type renderedExample struct {
	docExample
	ImageURL string
}

type renderedSection struct {
	Title    string
	Examples []renderedExample
}

func docSections() []docSection {
	return []docSection{
		{
			Title: "Output format and encoding",
			Examples: []docExample{
				{"Source format", "No out parameter keeps the format of the pristine image.", "", false},
				{"WebP", "Widely supported, carries an alpha channel.", "out=webp", false},
				{"AVIF", "Usually the smallest of the three at the same quality, and the slowest to encode.", "out=avif", false},
				{"JPEG XL", "Supported by few browsers today; useful when the client is known.", "out=jxl", false},
				{"Negotiated", "out=auto picks the first format the client accepts, from avif, webp, jxl.", "out=auto", false},
				{"Preference order", "prefer sets that order explicitly, here webp before avif.", "out=auto&prefer=webp,avif,jxl", false},
				{"Quality", "Quality is mapped per encoder; 1 to 100, default 90.", "quality=20", false},
				{"Transparency is kept", "A negotiated format switches to PNG when a transformation adds an alpha channel.", "im=Resize,width=300;Rotate,degrees=15", false},
			},
		},
		{
			Title: "Resizing and scaling",
			Examples: []docExample{
				{"Resize", "Resize=(width,height) resizes to exact dimensions.", "im=Resize=(300,150)", false},
				{"Resize, one dimension", "Omit a dimension to keep the aspect ratio.", "im=Resize,width=220", false},
				{"Scale", "Scale by a factor of the current size.", "im=Scale,width=0.25,height=0.25", false},
				{"Fit and Fill", "Fit inside the box and fill the rest with a blurred copy.", "im=FitAndFill=(320,320)", false},
			},
		},
		{
			Title: "Cropping",
			Examples: []docExample{
				{"Crop", "Crop a rectangle, optionally with a gravity.", "im=Crop,rect=(0,0,300,300),gravity=Center", false},
				{"Crop with expansion", "allowExpansion pads with transparency instead of clamping.", "im=Crop,size=(1200,300),allowExpansion&out=png", false},
				{"Aspect Crop", "Crop to an aspect ratio; xPosition/yPosition pick the kept region.", "im=AspectCrop=(16,9),xPosition=.5,yPosition=.3", false},
				{"Aspect Crop, expanded", "allowExpansion adds transparent pixels instead of cropping.", "im=AspectCrop=(1,1),allowExpansion&out=png", false},
				{"Relative Crop", "Shrink or expand each edge relative to the current size.", "im=RelativeCrop,north=100,south=100,east=50,west=50", false},
				{"Region of Interest Crop", "Crop around a focal point.", "im=RegionOfInterestCrop=(300,300),regionOfInterest=(300,220)", false},
				{"Feature Crop", "Crop around the most prominent features.", "im=FeatureCrop,size=(300,300)", false},
				{"Face Crop", "Crop around detected faces.", "im=FaceCrop,width=300,height=300", false},
				{"Smart Crop", "Faces when present, features otherwise.", "im=SmartCrop,size=(300,300)", false},
				{"Trim", "Trim uniform edges.", "im=Trim,tolerance=0.05;Resize,width=300", false},
			},
		},
		{
			Title: "Orientation and distortion",
			Examples: []docExample{
				{"Mirror", "Flip horizontally or vertically.", "im=Mirror,horizontal;Resize,width=300", false},
				{"Rotate", "Positive degrees rotate clockwise; the canvas grows.", "im=Resize,width=300;Rotate,degrees=13&out=png", false},
				{"Shear", "Slant into a parallelogram.", "im=Resize,width=300;Shear,x=0.3&out=png", false},
				{"Goop", "Distort the image; chaos ranges from 0 to 1.", "im=Resize,width=300;Goop,chaos=1", false},
			},
		},
		{
			Title: "Color",
			Examples: []docExample{
				{"Grayscale", "Render in shades of gray.", "im=Resize,width=300;Grayscale", false},
				{"Contrast", "Values above 1 increase contrast.", "im=Resize,width=300;Contrast,contrast=1.6", false},
				{"HSL", "Hue rotation plus saturation and lightness multipliers.", "im=Resize,width=300;HSL,hue=0.2,saturation=1.6,lightness=1.1", false},
				{"HSV", "Same idea using value instead of lightness.", "im=Resize,width=300;HSV,hue=0.5,saturation=2,value=1", false},
				{"Mono Hue", "Force every hue to one value, in degrees.", "im=Resize,width=300;MonoHue,hue=210", false},
				{"Max Colors", "Reduce the palette to shrink the file.", "im=Resize,width=300;MaxColors,colors=8", false},
				{"Background Color", "Fill transparency with a color.", "im=Resize,width=300;Rotate,degrees=13;BackgroundColor,color=00ff00", false},
			},
		},
		{
			Title: "Transparency",
			Examples: []docExample{
				{"Opacity", "Multiplier on the current opacity.", "im=Resize,width=300;Opacity=0.35&out=png", false},
				{"Chroma Key", "Remove a hue range, green by default.", "im=Resize,width=300;ChromaKey,hue=40,hueTolerance=0.15&out=png", false},
				{"Remove Color", "Remove one color with tolerance and feathering.", "im=Resize,width=300;RemoveColor,color=e8e2d8,tolerance=0.08,feather=0.05&out=png", false},
			},
		},
		{
			Title: "Background removal",
			Examples: []docExample{
				{"Background Remove", "Cut the subject out with a locally executed segmentation model, no hosted service involved.", "im=Resize,width=320;BackgroundRemove,fallback=color&out=png", false},
				{"Hard edges", "threshold turns the soft mask into a firmer cut-out; feather controls the ramp.", "im=Resize,width=320;BackgroundRemove,threshold=0.6,feather=0.05,fallback=color&out=png", false},
				{"Replace the background", "Chain BackgroundColor to drop the subject onto a flat colour.", "im=Resize,width=320;BackgroundRemove,fallback=color;BackgroundColor,color=0b7285", false},
			},
		},
		{
			Title: "Background removal without a model",
			Examples: studio([]docExample{
				{"Colour method", "method=color needs no model at all. It suits clean backdrops like this one, not busy scenes.", "im=Resize,width=320;BackgroundRemove,method=color&out=png", false},
				{"Colour method, replaced", "The same cut-out dropped onto a flat colour.", "im=Resize,width=320;BackgroundRemove,method=color;BackgroundColor,color=0b7285", false},
				{"Remove Color", "Remove one specific colour instead of learning it from the border.", "im=Resize,width=320;RemoveColor,color=eceae4,tolerance=0.1,feather=0.05&out=png", false},
			}),
		},
		{
			Title: "Detail",
			Examples: []docExample{
				{"Blur", "Gaussian blur; higher is blurrier.", "im=Resize,width=300;Blur=4", false},
				{"Unsharp Mask", "Sharpen edges and detail.", "im=Resize,width=300;UnsharpMask,gain=3", false},
			},
		},
		{
			Title: "Compositing",
			Examples: []docExample{
				{"Composite", "Overlay another image, for example a watermark.", "im=Resize,width=300;Composite,image=(url=" + samplePlaceholder + "),gravity=SouthEast,scale=0.1,opacity=0.7", false},
				{"Composite, underlay", "placement=under puts the source image on top.", "im=Resize,width=300;Rotate,degrees=10;Composite,image=(url=" + samplePlaceholder + "),placement=under,gravity=Center,scale=0.4", false},
				{"Append", "Place another image beside the source.", "im=Resize,width=200;Append,image=(url=" + samplePlaceholder + "),gravity=East", false},
			},
		},
		{
			Title: "Conditions",
			Examples: []docExample{
				{"If Dimension", "Branch on the current width or height.", "im=IfDimension,value=500,dimension=width,greaterThan=$(Resize=(300,200)),lessThan=$(Grayscale)", false},
				{"If Orientation", "Branch on portrait, landscape, or square.", "im=IfOrientation,landscape=$(Resize=(300,169)),portrait=$(Resize=(169,300)),square=$(Resize=(300,300))", false},
			},
		},
		{
			Title: "Chaining and legacy parameters",
			Examples: []docExample{
				{"Chained transformations", "Transformations run left to right, separated by semicolons.", "im=AspectCrop=(1,1);Resize,width=300;Grayscale;Blur=2", false},
				{"Legacy imop", "The imop parameter is an alias for im.", "imop=Resize,width=300,height=150", false},
				{"Legacy width and height", "Bare width and height still resize.", "width=300&height=150", false},
			},
		},
	}
}

// studio marks examples that should use the flat-backdrop sample image.
func studio(examples []docExample) []docExample {
	for index := range examples {
		examples[index].Studio = true
	}
	return examples
}

// absoluteURL turns a server path into an absolute URL, which is what the
// url parameter of /process expects.
func absoluteURL(c *gin.Context, path string) string {
	scheme := "http"
	if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host + path
}

func sampleURL(c *gin.Context) string {
	return absoluteURL(c, "/sample.jpg")
}

func buildProcessURL(sample, query string) string {
	target := "/process?url=" + url.QueryEscape(sample)
	if query != "" {
		target += "&" + strings.ReplaceAll(query, samplePlaceholder, url.QueryEscape(sample))
	}
	return target
}

// HandleSample serves the built-in example image used by the documentation.
func (ip *ImageProcessor) HandleSample(c *gin.Context) {
	c.Data(http.StatusOK, "image/jpeg", sampleImage)
}

// HandleStudioSample serves the flat-backdrop variant of the example image.
func (ip *ImageProcessor) HandleStudioSample(c *gin.Context) {
	c.Data(http.StatusOK, "image/jpeg", studioSampleImage)
}

// HandleDoc renders the documentation page with a live example per feature.
func (ip *ImageProcessor) HandleDoc(c *gin.Context) {
	sample := sampleURL(c)
	studioSample := strings.Replace(sample, "/sample.jpg", "/sample-studio.jpg", 1)

	sections := make([]renderedSection, 0, len(docSections()))
	for _, section := range docSections() {
		rendered := renderedSection{Title: section.Title}
		for _, example := range section.Examples {
			source := sample
			if example.Studio {
				source = studioSample
			}
			rendered.Examples = append(rendered.Examples, renderedExample{
				docExample: example,
				ImageURL:   buildProcessURL(source, example.Query),
			})
		}
		sections = append(sections, rendered)
	}

	schema, err := json.Marshal(transformSpecs())
	if err != nil {
		c.String(http.StatusInternalServerError, "failed to encode the transformation schema: %v", err)
		return
	}

	data := struct {
		Sections       []renderedSection
		SampleURL      string
		StudioURL      string
		SchemaJSON     template.JS
		CacheDirectory string
		CacheTTL       string
	}{
		Sections:       sections,
		SampleURL:      sample,
		StudioURL:      studioSample,
		SchemaJSON:     template.JS(schema),
		CacheDirectory: ip.config.CacheDirectory,
		CacheTTL:       ip.config.CacheDuration.String(),
	}

	c.Status(http.StatusOK)
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := docTemplate.Execute(c.Writer, data); err != nil {
		c.String(http.StatusInternalServerError, "failed to render documentation: %v", err)
	}
}

//go:embed assets/doc.html
var docHTML string

var docTemplate = template.Must(template.New("doc").Parse(docHTML))
