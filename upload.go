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
	"time"

	"github.com/gin-gonic/gin"
)

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
	if !ip.config.UploadsEnabled {
		c.JSON(http.StatusNotFound, gin.H{"error": "uploads are disabled"})
		return
	}
	if !ip.uploadAuthorized(c) {
		c.Header("WWW-Authenticate", "Bearer")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "upload authorization required"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, ip.config.MaxUploadBytes+(1<<20))

	file, header, err := c.Request.FormFile("image")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "an image file field is required"})
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, ip.config.MaxUploadBytes+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read the upload"})
		return
	}
	if int64(len(data)) > ip.config.MaxUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "upload is too large"})
		return
	}

	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the upload is not a supported image"})
		return
	}
	if err := ip.validateInputConfig(config); err != nil {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": err.Error()})
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
	ip.storageMutex.Lock()
	err = pruneDirectory(ip.config.UploadDirectory, ip.config.UploadTTL, ip.config.UploadStorageBytes, int64(len(data)))
	if err == nil {
		err = writeCache(filepath.Join(ip.config.UploadDirectory, name), data)
	}
	ip.storageMutex.Unlock()
	if err != nil {
		c.JSON(http.StatusInsufficientStorage, gin.H{"error": "upload storage limit reached"})
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

	path := filepath.Join(ip.config.UploadDirectory, name)
	info, err := os.Stat(path)
	if err == nil && time.Since(info.ModTime()) >= ip.config.UploadTTL {
		_ = os.Remove(path)
		err = os.ErrNotExist
	}
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "upload not found"})
		return
	}
	data, err := os.ReadFile(path)
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
