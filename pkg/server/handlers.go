/*
 * Iptv-Proxy is a project to proxyfie an m3u file and to proxyfie an Xtream iptv service (client API).
 * Copyright (C) 2020  Pierre-Emmanuel Jacquier
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package server

import (
	"bytes"
	_ "embed"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/riltech/streamer"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

//go:embed fake.ts
var fakeTS []byte

type FFmpegParams struct {
	CRF          string
	BitrateVideo string
	BitrateAudio string
	Preset       string
	Scale        string
	Threads      string
	Tune         string
}

//func NewFFmpegParams() *FFmpegParams {
//	return &FFmpegParams{
//		CRF:          getEnv("CRF", "32"),
//		BitrateVideo: getEnv("BITRATE_VIDEO", "1024k"),
//		BitrateAudio: getEnv("BITRATE_AUDIO", "128k"),
//		Preset:       getEnv("PRESET", "ultrafast"),
//		Scale:        getEnv("SCALE", "1024:720"),
//		Threads:      getEnv("THREADS", strconv.Itoa(runtime.NumCPU())),
//		Tune:         getEnv("TUNE", "zerolatency"),
//	}
//}
//
//func getEnv(key, defaultValue string) string {
//	value := os.Getenv(key)
//	if value == "" {
//		return defaultValue
//	}
//	return value
//}

func (c *Config) getM3U(ctx *gin.Context) {
	ctx.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, c.M3UFileName))
	ctx.Header("Content-Type", "application/octet-stream")

	ctx.File(c.proxyfiedM3UPath)
}

func (c *Config) reverseProxy(ctx *gin.Context) {
	rpURL, err := url.Parse(c.track.URI)
	if err != nil {
		_ = ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	c.stream(ctx, rpURL)
}
func (c *Config) tsHandler(ctx *gin.Context) {
	filename := ctx.Param("filename")
	if !strings.HasSuffix(filename, ".ts") {
		ctx.AbortWithStatus(http.StatusNotFound)
		return
	}

	// Путь к каталогу hlsdownloads
	filePath := "hlsdownloads/" + filename

	// Проверка существования файла
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		// Если файла нет, отдаем фейковый файл
		ctx.Data(http.StatusOK, "video/MP2T", fakeTS)
		return
	}

	// Отдаем реальный файл
	ctx.File(filePath)
}

func (c *Config) m3u8ReverseProxy(ctx *gin.Context) {
	id := ctx.Param("id")

	rpURL, err := url.Parse(strings.ReplaceAll(c.track.URI, path.Base(c.track.URI), id))
	if err != nil {
		_ = ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}
	fullURL := rpURL.Scheme + "://" + rpURL.Host + rpURL.Path
	stream, id := streamer.NewStream(
		fullURL,          // URI of raw RTSP stream
		"./hlsdownloads", // Directory where to store video chunks and indexes. Should exist already
		false,            // Indicates if stream should be keeping files after it is stopped or clean the directory
		true,             // Indicates if Audio should be enabled or not
		streamer.ProcessLoggingOpts{
			Enabled:    true,               // Indicates if process logging is enabled
			Compress:   true,               // Indicates if logs should be compressed
			Directory:  "/tmp/logs/stream", // Directory to store logs
			MaxAge:     0,                  // Max age for a log. 0 is infinite
			MaxBackups: 2,                  // Maximum backups to keep
			MaxSize:    500,                // Maximum size of a log in megabytes
		},
		25*time.Second, // Time to wait before declaring a stream start failed
	)

	// Returns a waitGroup where the stream checking the underlying process for a successful start
	stream.Start().Wait()
	fmt.Println(fullURL)

}

func (c *Config) stream(ctx *gin.Context, oriURL *url.URL) {
	client := &http.Client{}

	req, err := http.NewRequest("GET", oriURL.String(), nil)
	if err != nil {
		_ = ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	mergeHttpHeader(req.Header, ctx.Request.Header)

	resp, err := client.Do(req)
	if err != nil {
		_ = ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	mergeHttpHeader(ctx.Writer.Header(), resp.Header)
	ctx.Status(resp.StatusCode)
	ctx.Stream(func(w io.Writer) bool {
		_, _ = io.Copy(w, resp.Body) // nolint: errcheck
		return false
	})
}

func (c *Config) xtreamStream(ctx *gin.Context, oriURL *url.URL) {
	id := ctx.Param("id")
	if strings.HasSuffix(id, ".m3u8") {
		c.hlsXtreamStream(ctx, oriURL)
		return
	}

	c.stream(ctx, oriURL)
}

type values []string

func (vs values) contains(s string) bool {
	for _, v := range vs {
		if v == s {
			return true
		}
	}

	return false
}

func mergeHttpHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			if values(dst.Values(k)).contains(v) {
				continue
			}
			dst.Add(k, v)
		}
	}
}

// authRequest handle auth credentials
type authRequest struct {
	Username string `form:"username" binding:"required"`
	Password string `form:"password" binding:"required"`
}

func (c *Config) authenticate(ctx *gin.Context) {
	var authReq authRequest
	if err := ctx.Bind(&authReq); err != nil {
		_ = ctx.AbortWithError(http.StatusBadRequest, err) // nolint: errcheck
		return
	}
	if c.ProxyConfig.User.String() != authReq.Username || c.ProxyConfig.Password.String() != authReq.Password {
		ctx.AbortWithStatus(http.StatusUnauthorized)
	}
}

func (c *Config) appAuthenticate(ctx *gin.Context) {
	contents, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		_ = ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	q, err := url.ParseQuery(string(contents))
	if err != nil {
		_ = ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}
	if len(q["username"]) == 0 || len(q["password"]) == 0 {
		_ = ctx.AbortWithError(http.StatusBadRequest, fmt.Errorf("bad body url query parameters")) // nolint: errcheck
		return
	}
	log.Printf("[iptv-proxy] %v | %s |App Auth\n", time.Now().Format("2006/01/02 - 15:04:05"), ctx.ClientIP())
	if c.ProxyConfig.User.String() != q["username"][0] || c.ProxyConfig.Password.String() != q["password"][0] {
		ctx.AbortWithStatus(http.StatusUnauthorized)
	}

	ctx.Request.Body = io.NopCloser(bytes.NewReader(contents))
}
