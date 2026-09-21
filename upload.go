package main

import (
	"bytes"
	"crypto/md5"
	"fmt"
	"image"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

// maxUploadBytes bounds an uploaded pristine image.
const maxUploadBytes = 32 << 20

var uploadExtensions = map[string]string{
	"jpeg": ".jpg",
	"png":  ".png",
	"gif":  ".gif",
	"jxl":  ".jxl",
	"webp": ".webp",
}

// HandleUpload stores an uploaded image so the documentation page can run the
// transformations against it.
func (ip *ImageProcessor) HandleUpload(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUploadBytes)

	file, header, err := c.Request.FormFile("image")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "an image file field is required"})
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxUploadBytes))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read the upload"})
		return
	}

	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the upload is not a supported image"})
		return
	}
	extension, supported := uploadExtensions[strings.ToLower(format)]
	if !supported {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported image format " + format})
		return
	}

	if err := os.MkdirAll(ip.config.UploadDirectory, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to prepare the upload directory"})
		return
	}
	name := fmt.Sprintf("%x%s", md5.Sum(data), extension)
	if err := writeCache(filepath.Join(ip.config.UploadDirectory, name), data); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to store the upload"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		// An absolute URL, because /process fetches the pristine image over HTTP.
		"url":      absoluteURL(c, "/uploads/"+name),
		"name":     header.Filename,
		"format":   format,
		"width":    config.Width,
		"height":   config.Height,
		"bytes":    len(data),
		"fileName": name,
	})
}

// HandleUploadedImage serves a previously uploaded pristine image.
func (ip *ImageProcessor) HandleUploadedImage(c *gin.Context) {
	name := filepath.Base(c.Param("name"))
	if name == "." || name == "/" || strings.HasPrefix(name, ".") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid upload name"})
		return
	}

	data, err := os.ReadFile(filepath.Join(ip.config.UploadDirectory, name))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "upload not found"})
		return
	}

	format := "jpeg"
	if _, detected, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		format = detected
	}
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Data(http.StatusOK, contentType(format), data)
}
