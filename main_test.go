package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func testImage(width, height int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	return img
}

func TestParseTransformationsKeepsOrderAndArguments(t *testing.T) {
	items, err := parseTransformations("Resize=(250,125);Crop,rect=(0,0,100,100),gravity=Center,allowExpansion;BackgroundColor,color=00ff00")
	if err != nil {
		t.Fatalf("parseTransformations: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d transformations, want 3", len(items))
	}
	if items[0].Name != "resize" || items[0].Value != "250,125" {
		t.Errorf("resize parsed as %+v", items[0])
	}
	if items[1].Name != "crop" || items[1].Args["rect"] != "0,0,100,100" || !items[1].Flags["allowexpansion"] {
		t.Errorf("crop parsed as %+v", items[1])
	}
	if items[2].Args["color"] != "00ff00" {
		t.Errorf("background color parsed as %+v", items[2])
	}
}

func TestParseTransformationsNestedConditional(t *testing.T) {
	items, err := parseTransformations("IfDimension,value=1000,dimension=width,greaterThan=$(Blur=5),lessThan=$(Resize=(10,10))")
	if err != nil {
		t.Fatalf("parseTransformations: %v", err)
	}
	if got := items[0].Args["greaterthan"]; got != "Blur=5" {
		t.Errorf("greaterThan = %q", got)
	}
	if got := items[0].Args["lessthan"]; got != "Resize=(10,10)" {
		t.Errorf("lessThan = %q", got)
	}
}

func TestSelectOutputFormat(t *testing.T) {
	const modern = "image/avif,image/webp,image/jxl,*/*"

	cases := []struct {
		name                              string
		requested, prefer, source, accept string
		want                              string
		wantExplicit                      bool
		wantError                         bool
	}{
		{name: "source by default", source: "png", accept: modern, want: "png"},
		{name: "explicit avif", requested: "avif", source: "jpeg", accept: "*/*", want: "avif", wantExplicit: true},
		{name: "explicit jxl is never downgraded", requested: "jxl", source: "png", accept: "*/*", want: "jxl", wantExplicit: true},
		{name: "explicit webp", requested: "webp", source: "png", accept: "", want: "webp", wantExplicit: true},
		{name: "auto prefers avif", requested: "auto", source: "jpeg", accept: modern, want: "avif", wantExplicit: true},
		{name: "auto falls back to webp", requested: "auto", source: "jpeg", accept: "image/webp,*/*", want: "webp", wantExplicit: true},
		{name: "auto keeps the source when nothing matches", requested: "auto", source: "png", accept: "image/*,*/*", want: "png"},
		{name: "zero quality is not acceptance", requested: "auto", source: "jpeg", accept: "image/avif;q=0", want: "jpeg"},
		{name: "preference order wins", requested: "auto", prefer: "webp,avif", source: "jpeg", accept: modern, want: "webp", wantExplicit: true},
		{name: "preference negotiates without out", prefer: "jxl", source: "jpeg", accept: modern, want: "jxl", wantExplicit: true},
		{name: "unknown format", requested: "heic", source: "jpeg", wantError: true},
		{name: "unknown preference entry", requested: "auto", prefer: "avif,heic", source: "jpeg", wantError: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, explicit, err := selectOutputFormat(testCase.requested, testCase.prefer, testCase.source, testCase.accept)
			if testCase.wantError {
				if err == nil {
					t.Fatalf("expected an error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("selectOutputFormat: %v", err)
			}
			if got != testCase.want || explicit != testCase.wantExplicit {
				t.Errorf("got %q/%t, want %q/%t", got, explicit, testCase.want, testCase.wantExplicit)
			}
		})
	}
}

func TestEncodeEveryOutputFormat(t *testing.T) {
	source := testImage(48, 32)

	for _, format := range encodableFormats {
		encoded, err := encodeImage(source, format, 80)
		if err != nil {
			t.Errorf("encodeImage(%s): %v", format, err)
			continue
		}
		decoded, detected, err := image.Decode(bytes.NewReader(encoded))
		if err != nil {
			t.Errorf("decode %s: %v", format, err)
			continue
		}
		if decoded.Bounds().Dx() != 48 || decoded.Bounds().Dy() != 32 {
			t.Errorf("%s decoded to %v", format, decoded.Bounds())
		}
		if detected != format {
			t.Errorf("%s round-tripped as %s", format, detected)
		}
		if contentType(format) == "application/octet-stream" {
			t.Errorf("%s has no media type", format)
		}
	}
}

func TestAlphaSurvivesInAlphaCapableFormats(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			alpha := uint8(0)
			if x > 7 {
				alpha = 255
			}
			source.SetNRGBA(x, y, color.NRGBA{R: 200, G: 60, B: 60, A: alpha})
		}
	}

	for _, format := range []string{"png", "webp", "avif", "jxl"} {
		encoded, err := encodeImage(source, format, 90)
		if err != nil {
			t.Fatalf("encodeImage(%s): %v", format, err)
		}
		decoded, _, err := image.Decode(bytes.NewReader(encoded))
		if err != nil {
			t.Fatalf("decode %s: %v", format, err)
		}
		if !hasAlpha(decoded) {
			t.Errorf("%s lost the alpha channel", format)
		}
	}
}

func applyQuery(t *testing.T, img image.Image, query string) image.Image {
	t.Helper()
	processor := &ImageProcessor{}
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodGet, "/process?"+query, nil)
	result, err := processor.ProcessImage(img, context)
	if err != nil {
		t.Fatalf("ProcessImage(%q): %v", query, err)
	}
	return result
}

func TestTransformationGeometry(t *testing.T) {
	source := testImage(400, 200)
	cases := []struct {
		query         string
		width, height int
	}{
		{"im=Resize=(250,125)", 250, 125},
		{"im=Resize,width=100", 100, 50},
		{"im=AspectCrop=(1,1)", 200, 200},
		{"im=AspectCrop=(1,1),allowExpansion", 400, 400},
		{"im=Crop,size=(150,100),gravity=Center", 150, 100},
		{"im=RelativeCrop,north=10,south=10", 400, 180},
		{"im=FitAndFill=(300,300)", 300, 300},
		{"im=Scale,width=0.5,height=0.5", 200, 100},
		{"im=Trim", 400, 200},
		{"im=RegionOfInterestCrop=(100,100),regionOfInterest=(150,100)", 100, 100},
		{"imop=Resize,width=40,height=20", 40, 20},
		{"width=40&height=20", 40, 20},
		{"im=IfOrientation,landscape=$(Resize=(20,10)),portrait=$(Resize=(10,20))", 20, 10},
		{"im=IfDimension,value=100,dimension=width,greaterThan=$(Resize=(50,25))", 50, 25},
	}
	for _, testCase := range cases {
		result := applyQuery(t, source, testCase.query)
		if result.Bounds().Dx() != testCase.width || result.Bounds().Dy() != testCase.height {
			t.Errorf("%s produced %v, want %dx%d", testCase.query, result.Bounds(), testCase.width, testCase.height)
		}
	}
}

func TestTransformationColors(t *testing.T) {
	transparent := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	transparent.SetNRGBA(0, 0, color.NRGBA{R: 10, G: 200, B: 30, A: 255})

	filled := applyQuery(t, transparent, "im=BackgroundColor,color=00ff00")
	if got := color.NRGBAModel.Convert(filled.At(3, 3)).(color.NRGBA); got != (color.NRGBA{G: 255, A: 255}) {
		t.Errorf("background color pixel = %+v", got)
	}

	faded := applyQuery(t, testImage(4, 4), "im=Opacity=0.5")
	if got := color.NRGBAModel.Convert(faded.At(0, 0)).(color.NRGBA).A; got != 127 {
		t.Errorf("opacity alpha = %d, want 127", got)
	}

	gray := applyQuery(t, testImage(4, 4), "im=Grayscale")
	pixel := color.NRGBAModel.Convert(gray.At(2, 2)).(color.NRGBA)
	if pixel.R != pixel.G || pixel.G != pixel.B {
		t.Errorf("grayscale pixel = %+v", pixel)
	}

	keyed := applyQuery(t, transparent, "im=ChromaKey")
	if got := color.NRGBAModel.Convert(keyed.At(0, 0)).(color.NRGBA).A; got != 0 {
		t.Errorf("chroma key alpha = %d, want 0", got)
	}

	removed := applyQuery(t, transparent, "im=RemoveColor,color=0ac81e,tolerance=0.05")
	if got := color.NRGBAModel.Convert(removed.At(0, 0)).(color.NRGBA).A; got != 0 {
		t.Errorf("remove color alpha = %d, want 0", got)
	}

	mono := applyQuery(t, testImage(4, 4), "im=MonoHue,hue=0")
	monoPixel := color.NRGBAModel.Convert(mono.At(3, 3)).(color.NRGBA)
	if monoPixel.R < monoPixel.G || monoPixel.R < monoPixel.B {
		t.Errorf("mono hue pixel = %+v, want red dominant", monoPixel)
	}
}

func TestTransformationMirrorAndRotate(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	source.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	source.SetNRGBA(1, 0, color.NRGBA{B: 255, A: 255})

	mirrored := applyQuery(t, source, "im=Mirror,horizontal")
	if got := color.NRGBAModel.Convert(mirrored.At(0, 0)).(color.NRGBA); got.B != 255 {
		t.Errorf("mirrored pixel = %+v", got)
	}

	rotated := applyQuery(t, testImage(4, 2), "im=Rotate,degrees=90")
	if rotated.Bounds().Dx() != 2 || rotated.Bounds().Dy() != 4 {
		t.Errorf("rotate bounds = %v, want 2x4", rotated.Bounds())
	}
}

func TestUnsupportedTransformationIsRejected(t *testing.T) {
	processor := &ImageProcessor{}
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodGet, "/process?im=Teleport", nil)
	if _, err := processor.ProcessImage(testImage(4, 4), context); err == nil {
		t.Fatal("expected error for unsupported transformation")
	}
}

func TestHandleRequestNegotiatesJXL(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var pristine bytes.Buffer
	if err := png.Encode(&pristine, testImage(60, 40)); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(pristine.Bytes())
	}))
	defer origin.Close()

	processor, err := NewImageProcessor(Config{
		CacheDirectory:       t.TempDir(),
		CacheDuration:        time.Hour,
		AllowedHosts:         []string{"127.0.0.1"},
		AllowPrivateNetworks: true,
	})
	if err != nil {
		t.Fatalf("NewImageProcessor: %v", err)
	}
	router := gin.New()
	router.GET("/process", processor.HandleRequest)

	target := "/process?url=" + url.QueryEscape(origin.URL+"/image.png") + "&im=Resize=(30,20)&out=jxl"

	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("Accept", "image/avif,image/jxl,image/webp,*/*")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "image/jxl" {
		t.Errorf("content type = %q, want image/jxl", got)
	}
	if got := recorder.Header().Get("Vary"); got != "Accept" {
		t.Errorf("vary = %q, want Accept", got)
	}
	decoded, _, err := image.Decode(bytes.NewReader(recorder.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Bounds().Dx() != 30 || decoded.Bounds().Dy() != 20 {
		t.Errorf("decoded bounds = %v, want 30x20", decoded.Bounds())
	}

	// out=auto negotiates: a client without JPEG XL but with WebP gets WebP,
	// and one that accepts neither keeps the source format.
	for accept, want := range map[string]string{
		"image/webp,*/*": "image/webp",
		"image/*,*/*":    "image/png",
	} {
		fallbackTarget := strings.Replace(target, "out=jxl", "out=auto", 1)
		fallbackRequest := httptest.NewRequest(http.MethodGet, fallbackTarget, nil)
		fallbackRequest.Header.Set("Accept", accept)
		fallbackRecorder := httptest.NewRecorder()
		router.ServeHTTP(fallbackRecorder, fallbackRequest)

		if got := fallbackRecorder.Header().Get("Content-Type"); got != want {
			t.Errorf("Accept %q produced %q, want %q", accept, got, want)
		}
		if _, _, err := image.Decode(bytes.NewReader(fallbackRecorder.Body.Bytes())); err != nil {
			t.Errorf("Accept %q: undecodable body: %v", accept, err)
		}
	}
}

func TestSemicolonChainsSurviveQueryParsing(t *testing.T) {
	result := applyQuery(t, testImage(400, 200), "im=Resize=(100,50);Grayscale;Blur=2")
	if result.Bounds().Dx() != 100 || result.Bounds().Dy() != 50 {
		t.Fatalf("chained transformations produced %v, want 100x50", result.Bounds())
	}
	pixel := color.NRGBAModel.Convert(result.At(50, 25)).(color.NRGBA)
	if pixel.R != pixel.G || pixel.G != pixel.B {
		t.Errorf("chained grayscale pixel = %+v", pixel)
	}
}

func TestShorthandAndNamedArgumentsAreEquivalent(t *testing.T) {
	source := testImage(8, 8)
	shorthand := applyQuery(t, source, "im=Opacity=0.5")
	named := applyQuery(t, source, "im=Opacity,opacity=0.5")
	if color.NRGBAModel.Convert(shorthand.At(0, 0)) != color.NRGBAModel.Convert(named.At(0, 0)) {
		t.Errorf("opacity shorthand and named form differ")
	}

	bright := applyQuery(t, source, "im=Contrast=1.5")
	if bright.Bounds() != source.Bounds() {
		t.Errorf("contrast changed bounds to %v", bright.Bounds())
	}
}

func TestParseQuality(t *testing.T) {
	if got, err := parseQuality(""); err != nil || got != defaultQuality {
		t.Errorf("parseQuality(\"\") = %d, %v", got, err)
	}
	if got, err := parseQuality("40"); err != nil || got != 40 {
		t.Errorf("parseQuality(\"40\") = %d, %v", got, err)
	}
	for _, invalid := range []string{"0", "101", "high"} {
		if _, err := parseQuality(invalid); err == nil {
			t.Errorf("parseQuality(%q) expected error", invalid)
		}
	}
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)

	processor, err := NewImageProcessor(Config{
		CacheDirectory:       t.TempDir(),
		UploadDirectory:      t.TempDir(),
		CacheDuration:        time.Hour,
		AllowedHosts:         []string{"127.0.0.1"},
		AllowPrivateNetworks: true,
		UploadsEnabled:       true,
	})
	if err != nil {
		t.Fatalf("NewImageProcessor: %v", err)
	}
	router := gin.New()
	processor.RegisterRoutes(router)
	return httptest.NewServer(router)
}

func TestDocPageListsEveryTransformation(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	response, err := http.Get(server.URL + "/doc")
	if err != nil {
		t.Fatalf("GET /doc: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	page := string(body)

	for _, name := range []string{
		"Append", "AspectCrop", "BackgroundColor", "Blur", "ChromaKey", "Composite",
		"Contrast", "Crop", "FaceCrop", "FeatureCrop", "FitAndFill", "Goop", "Grayscale",
		"HSL", "HSV", "IfDimension", "IfOrientation", "MaxColors", "Mirror", "MonoHue",
		"Opacity", "RegionOfInterestCrop", "RelativeCrop", "RemoveColor", "Resize",
		"Rotate", "Scale", "Shear", "SmartCrop", "Trim", "UnsharpMask",
	} {
		if !strings.Contains(page, name) {
			t.Errorf("documentation page is missing %s", name)
		}
	}
}

func TestEveryDocExampleRenders(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	sample := server.URL + "/sample.jpg"
	studio := server.URL + "/sample-studio.jpg"
	for _, section := range docSections() {
		for _, example := range section.Examples {
			source := sample
			if example.Studio {
				source = studio
			}
			target := server.URL + buildProcessURL(source, example.Query)
			response, err := http.Get(target)
			if err != nil {
				t.Errorf("%s/%s: %v", section.Title, example.Title, err)
				continue
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Errorf("%s/%s: status %d, body %s", section.Title, example.Title, response.StatusCode, body)
				continue
			}
			if _, _, err := image.Decode(bytes.NewReader(body)); err != nil {
				t.Errorf("%s/%s: response is not a decodable image: %v", section.Title, example.Title, err)
			}
		}
	}
}

func TestColorBackgroundRemovalKeepsSubjectAndInteriorHoles(t *testing.T) {
	// Uniform backdrop, a subject in the middle, and a hole inside the subject
	// painted in the backdrop colour.
	source := image.NewNRGBA(image.Rect(0, 0, 60, 60))
	backdrop := color.NRGBA{R: 240, G: 240, B: 235, A: 255}
	subject := color.NRGBA{R: 30, G: 60, B: 200, A: 255}
	for y := 0; y < 60; y++ {
		for x := 0; x < 60; x++ {
			source.SetNRGBA(x, y, backdrop)
		}
	}
	for y := 15; y < 45; y++ {
		for x := 15; x < 45; x++ {
			source.SetNRGBA(x, y, subject)
		}
	}
	for y := 25; y < 30; y++ {
		for x := 25; x < 30; x++ {
			source.SetNRGBA(x, y, backdrop)
		}
	}

	result := applyQuery(t, source, "im=BackgroundRemove,method=color")

	if got := color.NRGBAModel.Convert(result.At(2, 2)).(color.NRGBA).A; got != 0 {
		t.Errorf("backdrop alpha = %d, want 0", got)
	}
	if got := color.NRGBAModel.Convert(result.At(30, 40)).(color.NRGBA).A; got != 255 {
		t.Errorf("subject alpha = %d, want 255", got)
	}
	if got := color.NRGBAModel.Convert(result.At(27, 27)).(color.NRGBA).A; got != 255 {
		t.Errorf("enclosed hole alpha = %d, want 255 (only border-connected background is removed)", got)
	}
}

func TestBackgroundRemovalReportsMissingRuntime(t *testing.T) {
	processor := &ImageProcessor{segmenter: newSegmenter(t.TempDir())}
	t.Setenv("ONNXRUNTIME_SHARED_LIBRARY", filepath.Join(t.TempDir(), "missing.so"))

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodGet, "/process?im=BackgroundRemove", nil)
	if _, err := processor.ProcessImage(testImage(8, 8), context); err == nil {
		t.Fatal("expected an error when onnxruntime is unavailable")
	}
}

func TestSegmentationMatchesReference(t *testing.T) {
	processor, err := NewImageProcessor(Config{CacheDirectory: t.TempDir(), ModelDirectory: "models", CacheDuration: time.Hour})
	if err != nil {
		t.Fatalf("NewImageProcessor: %v", err)
	}
	if _, err := os.Stat(filepath.Join("models", defaultSegmentationModel+".onnx")); err != nil {
		t.Skipf("segmentation model not present: %v", err)
	}

	source := testImage(64, 48)
	coverage, err := processor.segmenter.mask(source, defaultSegmentationModel)
	if err != nil {
		t.Skipf("segmentation unavailable: %v", err)
	}
	if coverage.width != 64 || coverage.height != 48 {
		t.Errorf("mask is %dx%d, want the source size 64x48", coverage.width, coverage.height)
	}
	for _, value := range coverage.data {
		if value < 0 || value > 1 {
			t.Fatalf("mask value %f is outside [0,1]", value)
		}
	}
}

func TestCacheHeadersAndReuse(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	target := server.URL + "/process?url=" + url.QueryEscape(server.URL+"/sample.jpg") + "&im=Resize,width=120"

	first, err := http.Get(target)
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	body, _ := io.ReadAll(first.Body)
	first.Body.Close()

	if got := first.Header.Get("X-Cache"); got != "MISS" {
		t.Errorf("first X-Cache = %q, want MISS", got)
	}
	if got := first.Header.Get("X-Image-Width"); got != "120" {
		t.Errorf("X-Image-Width = %q, want 120", got)
	}
	if first.Header.Get("Cache-Control") == "" || first.Header.Get("ETag") == "" {
		t.Errorf("missing cache validators: %v", first.Header)
	}
	if len(body) == 0 {
		t.Fatal("empty body")
	}

	second, err := http.Get(target)
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	second.Body.Close()
	if got := second.Header.Get("X-Cache"); got != "HIT" {
		t.Errorf("second X-Cache = %q, want HIT", got)
	}

	// The key is canonical, so a different parameter order still hits.
	reordered, err := http.Get(server.URL + "/process?im=Resize,width=120&url=" + url.QueryEscape(server.URL+"/sample.jpg"))
	if err != nil {
		t.Fatalf("reordered request: %v", err)
	}
	reordered.Body.Close()
	if got := reordered.Header.Get("X-Cache"); got != "HIT" {
		t.Errorf("reordered X-Cache = %q, want HIT", got)
	}

	bypass, err := http.Get(target + "&nocache=1")
	if err != nil {
		t.Fatalf("nocache request: %v", err)
	}
	bypass.Body.Close()
	if got := bypass.Header.Get("X-Cache"); got != "MISS" {
		t.Errorf("nocache X-Cache = %q, want MISS", got)
	}

	request, _ := http.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("If-None-Match", first.Header.Get("ETag"))
	conditional, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("conditional request: %v", err)
	}
	conditional.Body.Close()
	if conditional.StatusCode != http.StatusNotModified {
		t.Errorf("conditional status = %d, want 304", conditional.StatusCode)
	}
}

func uploadFile(t *testing.T, serverURL, fieldName, fileName string, content []byte) (*http.Response, map[string]any) {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(fieldName, fileName)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	part.Write(content)
	writer.Close()

	response, err := http.Post(serverURL+"/upload", writer.FormDataContentType(), &body)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer response.Body.Close()

	result := map[string]any{}
	json.NewDecoder(response.Body).Decode(&result)
	return response, result
}

func TestUploadedImageIsUsableAsSource(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	var pristine bytes.Buffer
	if err := png.Encode(&pristine, testImage(80, 40)); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}

	response, result := uploadFile(t, server.URL, "image", "photo.png", pristine.Bytes())
	if response.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d, body %v", response.StatusCode, result)
	}

	uploaded, _ := result["url"].(string)
	if !strings.HasPrefix(uploaded, "http://") {
		t.Fatalf("upload url = %q, want an absolute URL", uploaded)
	}
	if result["width"].(float64) != 80 || result["height"].(float64) != 40 {
		t.Errorf("reported size = %vx%v, want 80x40", result["width"], result["height"])
	}

	processed, err := http.Get(server.URL + "/process?url=" + url.QueryEscape(uploaded) + "&im=Resize,width=20")
	if err != nil {
		t.Fatalf("process uploaded: %v", err)
	}
	defer processed.Body.Close()
	if processed.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(processed.Body)
		t.Fatalf("process uploaded status = %d, body %s", processed.StatusCode, body)
	}
	if got := processed.Header.Get("X-Image-Width"); got != "20" {
		t.Errorf("derivative width = %q, want 20", got)
	}
}

func TestUploadRejectsNonImages(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	response, result := uploadFile(t, server.URL, "image", "notes.txt", []byte("this is not an image"))
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %v)", response.StatusCode, result)
	}
}

func TestPlaygroundSchemaUsesSupportedTransformations(t *testing.T) {
	processor := &ImageProcessor{}
	source := testImage(40, 40)

	for _, spec := range transformSpecs() {
		items, err := parseTransformations(spec.Name)
		if err != nil || len(items) != 1 {
			t.Errorf("%s: unparseable transformation name: %v", spec.Name, err)
			continue
		}
		state := &processingState{remaining: defaultMaxTransformations}
		if _, err := processor.applyTransformation(source, items[0], state); err != nil &&
			strings.Contains(err.Error(), "unsupported transformation") {
			t.Errorf("%s is offered by the playground but not implemented", spec.Name)
		}

		for _, param := range spec.Params {
			if param.Name == "" || param.Label == "" || param.Kind == "" {
				t.Errorf("%s has an incomplete parameter: %+v", spec.Name, param)
			}
		}
	}
}

func TestTransparencySurvivesFormatSelection(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	// The sample is an opaque JPEG; rotating it adds transparent corners.
	source := url.QueryEscape(server.URL + "/sample.jpg")

	cases := []struct {
		name, query, wantType string
		wantAlpha             bool
	}{
		{"negotiated fallback keeps alpha as png", "im=Resize,width=80;Rotate,degrees=15", "image/png", true},
		{"explicit jxl is never downgraded", "im=Resize,width=80;Rotate,degrees=15&out=jxl", "image/jxl", true},
		{"explicit jpeg still flattens", "im=Resize,width=80;Rotate,degrees=15&out=jpeg", "image/jpeg", false},
		{"opaque results keep the source format", "im=Resize,width=80", "image/jpeg", false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response, err := http.Get(server.URL + "/process?url=" + source + "&" + testCase.query)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()

			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, body %s", response.StatusCode, body)
			}
			if got := response.Header.Get("Content-Type"); got != testCase.wantType {
				t.Fatalf("content type = %q, want %q", got, testCase.wantType)
			}

			decoded, _, err := image.Decode(bytes.NewReader(body))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got := hasAlpha(decoded); got != testCase.wantAlpha {
				t.Errorf("hasAlpha = %t, want %t", got, testCase.wantAlpha)
			}
		})
	}
}

func sampleProcessor(t *testing.T) (*ImageProcessor, image.Image) {
	t.Helper()

	processor, err := NewImageProcessor(Config{CacheDirectory: t.TempDir(), CacheDuration: time.Hour})
	if err != nil {
		t.Fatalf("NewImageProcessor: %v", err)
	}
	if processor.classifier == nil {
		t.Skip("face detection unavailable")
	}
	source, _, err := image.Decode(bytes.NewReader(sampleImage))
	if err != nil {
		t.Fatalf("decode sample: %v", err)
	}
	return processor, source
}

func TestDetectFacesFindsTheSampleFace(t *testing.T) {
	processor, source := sampleProcessor(t)

	faces := processor.detectFaces(source, defaultMinQuality)
	if len(faces) == 0 {
		t.Fatal("no face detected in the built-in sample")
	}

	// The face in the built-in sample sits here; the dog next to it is also
	// reported by the cascade, so look for a detection on the person.
	humanFace := image.Rect(330, 200, 460, 300)
	found := false
	for _, candidate := range faces {
		if candidate.Score < defaultMinQuality {
			t.Errorf("face %+v is below the quality threshold", candidate)
		}
		if !candidate.rect().Overlaps(source.Bounds()) {
			t.Errorf("detected face %v lies outside the image %v", candidate.rect(), source.Bounds())
		}
		if image.Pt(candidate.X, candidate.Y).In(humanFace) {
			found = true
		}
	}
	if !found {
		t.Errorf("no detection on the sample face, got %+v", faces)
	}
}

func TestDetectFacesIgnoresWeakDetections(t *testing.T) {
	processor, _ := sampleProcessor(t)

	// A flat gradient has no faces; a permissive threshold may report noise,
	// the default one must not.
	noise := testImage(400, 400)
	if faces := processor.detectFaces(noise, defaultMinQuality); len(faces) != 0 {
		t.Errorf("detected %d faces in a synthetic image: %+v", len(faces), faces)
	}
}

func TestFaceCropFocusModes(t *testing.T) {
	processor, source := sampleProcessor(t)

	all, err := processor.faceCrop(source, 240, 240, defaultMinQuality, defaultFacePadding, false)
	if err != nil {
		t.Fatalf("faceCrop(all): %v", err)
	}
	biggest, err := processor.faceCrop(source, 240, 240, defaultMinQuality, defaultFacePadding, true)
	if err != nil {
		t.Fatalf("faceCrop(biggest): %v", err)
	}

	for name, result := range map[string]image.Image{"allFaces": all, "biggestFace": biggest} {
		if result.Bounds().Dx() != 240 || result.Bounds().Dy() != 240 {
			t.Errorf("%s produced %v, want 240x240", name, result.Bounds())
		}
	}
}

func TestCropRegionToSizeStaysInsideTheImage(t *testing.T) {
	source := testImage(200, 100)

	// A region hanging off the top left corner.
	result, err := cropRegionToSize(source, image.Rect(-50, -40, 30, 20), 60, 60)
	if err != nil {
		t.Fatalf("cropRegionToSize: %v", err)
	}
	if result.Bounds().Dx() != 60 || result.Bounds().Dy() != 60 {
		t.Errorf("result = %v, want 60x60", result.Bounds())
	}

	// A region larger than the image is clamped to the source.
	wide, err := cropRegionToSize(source, image.Rect(-500, -500, 700, 600), 0, 50)
	if err != nil {
		t.Fatalf("cropRegionToSize(wide): %v", err)
	}
	if wide.Bounds().Dy() != 50 {
		t.Errorf("height = %d, want 50", wide.Bounds().Dy())
	}
}

func TestBoxMeanAveragesTheWindow(t *testing.T) {
	values := []float32{
		1, 1, 1,
		1, 10, 1,
		1, 1, 1,
	}
	means := boxMean(values, 3, 3, 1)

	// The centre averages all nine samples.
	if got := means[4]; math.Abs(float64(got)-18.0/9) > 1e-5 {
		t.Errorf("centre mean = %f, want 2", got)
	}
	// A corner averages its four in-bounds samples.
	if got := means[0]; math.Abs(float64(got)-13.0/4) > 1e-5 {
		t.Errorf("corner mean = %f, want 3.25", got)
	}

	flat := boxMean([]float32{5, 5, 5, 5}, 2, 2, 3)
	for _, value := range flat {
		if math.Abs(float64(value)-5) > 1e-5 {
			t.Errorf("uniform input produced %f, want 5", value)
		}
	}
}

func TestGuidedFilterSharpensAgainstTheGuide(t *testing.T) {
	const width, height = 64, 16

	guide := newMask(width, height)
	blurred := newMask(width, height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			index := y*width + x
			// A hard edge in the guide, a soft ramp in the mask.
			if x >= width/2 {
				guide.data[index] = 1
			}
			ramp := (float64(x) - float64(width)/2 + 8) / 16
			blurred.data[index] = float32(clamp(ramp, 0, 1))
		}
	}

	refined := guidedFilter(guide, blurred, 6, 1e-6)

	// The refined mask steps where the guide steps instead of following the
	// smooth ramp it was given.
	row := height / 2
	inputJump := blurred.at(width/2, row) - blurred.at(width/2-1, row)
	refinedJump := refined.at(width/2, row) - refined.at(width/2-1, row)
	if refinedJump < 4*inputJump {
		t.Errorf("edge jump did not grow: input %f, refined %f", inputJump, refinedJump)
	}
	if refined.at(0, height/2) > 0.05 || refined.at(width-1, height/2) < 0.95 {
		t.Errorf("refined mask is wrong far from the edge: %f .. %f",
			refined.at(0, height/2), refined.at(width-1, height/2))
	}
}

func TestComposeCutoutRemovesBackgroundBleed(t *testing.T) {
	const size = 32

	// A red subject on a white background, with a half transparent edge column
	// that mixes both colours.
	source := image.NewNRGBA(image.Rect(0, 0, size, size))
	alpha := newMask(size, size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			index := y*size + x
			switch {
			case x < size/2-1:
				source.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
				alpha.data[index] = 1
			case x == size/2-1:
				// 50% red over white.
				source.SetNRGBA(x, y, color.NRGBA{R: 255, G: 128, B: 128, A: 255})
				alpha.data[index] = 0.5
			default:
				source.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
			}
		}
	}

	plain := composeCutout(source, alpha, false)
	cleaned := composeCutout(source, alpha, true)

	edgeX, edgeY := size/2-1, size/2
	if got := plain.NRGBAAt(edgeX, edgeY); got.G < 100 {
		t.Fatalf("test setup: the plain edge should keep the white bleed, got %+v", got)
	}
	cleanedEdge := cleaned.NRGBAAt(edgeX, edgeY)
	if cleanedEdge.G > 60 || cleanedEdge.B > 60 {
		t.Errorf("decontaminated edge = %+v, want the white bleed removed", cleanedEdge)
	}
	if cleanedEdge.A != 127 && cleanedEdge.A != 128 {
		t.Errorf("edge alpha = %d, want about 128", cleanedEdge.A)
	}
	if got := cleaned.NRGBAAt(2, edgeY); got.A != 255 || got.R != 255 {
		t.Errorf("opaque subject pixel changed: %+v", got)
	}
	if got := cleaned.NRGBAAt(size-2, edgeY); got.A != 0 {
		t.Errorf("background alpha = %d, want 0", got.A)
	}
}

func TestRefineMaskThresholdHardensTheEdge(t *testing.T) {
	coverage := newMask(4, 1)
	copy(coverage.data, []float32{0, 0.4, 0.6, 1})

	hardened := refineMask(testImage(4, 1), coverage, 0.5, 0.05, false)
	want := []float32{0, 0, 1, 1}
	for index, value := range hardened.data {
		if math.Abs(float64(value-want[index])) > 1e-5 {
			t.Errorf("value %d = %f, want %f", index, value, want[index])
		}
	}
}
