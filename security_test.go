package main

import (
	"bytes"
	"image/png"
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

func TestSourcePolicyFailsClosed(t *testing.T) {
	config := Config{}
	if _, err := config.checkSourceURL("https://example.com/image.jpg"); err == nil {
		t.Fatal("an empty allowlist accepted a network source")
	}
}

func TestSourcePolicyRestrictsPathsAndPorts(t *testing.T) {
	config := Config{
		AllowedHosts:        []string{"im.januschka.com"},
		AllowedPathPrefixes: []string{"/sample.jpg", "/uploads/"},
	}
	for _, accepted := range []string{
		"https://im.januschka.com/sample.jpg",
		"https://im.januschka.com/uploads/abc.jpg",
	} {
		if _, err := config.checkSourceURL(accepted); err != nil {
			t.Errorf("%s rejected: %v", accepted, err)
		}
	}
	for _, rejected := range []string{
		"https://im.januschka.com/process?url=x",
		"https://im.januschka.com/uploads/../process",
		"https://im.januschka.com/uploads/%2e%2e/process",
		"https://im.januschka.com.evil.example/uploads/a.jpg",
		"https://im.januschka.com:8443/uploads/a.jpg",
		"https://user:pass@im.januschka.com/uploads/a.jpg",
	} {
		if _, err := config.checkSourceURL(rejected); err == nil {
			t.Errorf("%s was accepted", rejected)
		}
	}
}

func TestSourceRedirectIsRevalidated(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://localhost/private", http.StatusFound)
	}))
	defer origin.Close()

	processor, err := NewImageProcessor(Config{
		CacheDirectory:       t.TempDir(),
		AllowedHosts:         []string{"127.0.0.1"},
		AllowPrivateNetworks: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := processor.fetchImage(origin.URL); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("redirect was not rejected: %v", err)
	}
}

func TestPrivateAddressBlockedAfterResolution(t *testing.T) {
	processor, err := NewImageProcessor(Config{
		CacheDirectory: t.TempDir(),
		AllowedHosts:   []string{"127.0.0.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := processor.fetchImage("http://127.0.0.1/image.png"); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("private address was not blocked: %v", err)
	}
}

func TestOversizedSourceBodyIsRejected(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), 1025))
	}))
	defer origin.Close()
	processor, err := NewImageProcessor(Config{
		CacheDirectory:       t.TempDir(),
		AllowedHosts:         []string{"127.0.0.1"},
		AllowPrivateNetworks: true,
		MaxImageBytes:        1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := processor.fetchImage(origin.URL); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized body was not rejected: %v", err)
	}
}

func TestOutputGeometryAndTransformationCountAreBounded(t *testing.T) {
	processor := &ImageProcessor{config: Config{MaxDimension: 1000, MaxOutputPixels: 1_000_000, MaxTransformations: 2}}
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodGet, "/process?im=Resize=(1001,10)", nil)
	if _, err := processor.ProcessImage(testImage(10, 10), context); err == nil {
		t.Fatal("oversized output was accepted")
	}

	context.Request = httptest.NewRequest(http.MethodGet, "/process?im=Grayscale;Mirror;Opacity=0.5", nil)
	if _, err := processor.ProcessImage(testImage(10, 10), context); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("long transformation chain was not rejected: %v", err)
	}
}

func TestProcessingQueueTimesOut(t *testing.T) {
	processor, err := NewImageProcessor(Config{
		CacheDirectory: t.TempDir(),
		MaxConcurrent:  1,
		QueueTimeout:   time.Millisecond,
		AllowedHosts:   []string{"example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	processor.processSlots <- struct{}{}
	defer func() { <-processor.processSlots }()

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/process?url="+url.QueryEscape("https://example.com/a.jpg"), nil)
	processor.HandleRequest(context)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
}

func TestUploadsDisabledByDefault(t *testing.T) {
	processor, err := NewImageProcessor(Config{CacheDirectory: t.TempDir(), UploadDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/upload", nil)
	processor.HandleUpload(context)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}

func TestUploadRejectsExcessivePixelDimensions(t *testing.T) {
	processor, err := NewImageProcessor(Config{
		CacheDirectory:  t.TempDir(),
		UploadDirectory: t.TempDir(),
		UploadsEnabled:  true,
		MaxImagePixels:  100,
		MaxDimension:    100,
	})
	if err != nil {
		t.Fatal(err)
	}

	var source bytes.Buffer
	if err := png.Encode(&source, testImage(11, 10)); err != nil {
		t.Fatal(err)
	}
	request := multipartImageRequest(t, source.Bytes())
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request
	processor.HandleUpload(context)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func multipartImageRequest(t *testing.T, data []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("image", "image.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/upload", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func TestDemoCleanupOnlyRemovesExpiredArtifacts(t *testing.T) {
	directory := t.TempDir()
	oldPath := filepath.Join(directory, "old")
	newPath := filepath.Join(directory, "new")
	if err := os.WriteFile(oldPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-demoArtifactRetention - time.Minute)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	removeExpiredFiles(directory, demoArtifactRetention)
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("expired artifact remains: %v", err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Errorf("fresh artifact was removed: %v", err)
	}
}
