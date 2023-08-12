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
	"github.com/canhlinh/hlsdl"
	"github.com/gin-gonic/gin"
	"github.com/grafov/m3u8"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"
)

//go:embed fake.ts
var fakeTS []byte

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

	// Загрузите оригинальный плейлист
	resp, err := http.Get(fullURL)
	if err != nil {
		_ = ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		_ = ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	// Разберите плейлист
	p, listType, err := m3u8.DecodeFrom(bytes.NewReader(body), true)
	if err != nil {
		_ = ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	// Создайте новый экземпляр HlsDl
	downloader := hlsdl.New(fullURL, nil, "hlsdownloads", 10, true, "")

	filepath, err := downloader.Download()
	if err != nil {
		log.Fatalf("Ошибка при загрузке: %v", err)
	}

	// Добавление суффикса к имени файла
	dir := filepath[:strings.LastIndex(filepath, "/")]
	base := filepath[strings.LastIndex(filepath, "/")+1:]
	ext := base[strings.LastIndex(base, "."):]
	newName := base[:len(base)-len(ext)] + "_compressed" + ext
	outputFile := dir + "/" + newName

	// Сжатие файла с помощью ffmpeg
	cmd := exec.Command("ffmpeg", "-i", filepath, "-vf", "scale=720:480", "-c:v", "libx265", "-b:v", "600k", "-b:a", "128k", "-preset", "medium", outputFile)
	err = cmd.Run()
	if err != nil {
		log.Fatalf("Ошибка при сжатии файла: %v", err)
	}

	if listType == m3u8.MEDIA {
		mediaList := p.(*m3u8.MediaPlaylist)

		// Замените сегменты на свои
		for _, segment := range mediaList.Segments {
			if segment != nil {
				segment.URI = "/" + outputFile // Замените на ваш путь к сегменту
			}
		}

		// Генерируйте новый плейлист
		modifiedPlaylist := mediaList.Encode().Bytes()

		// Отправьте новый плейлист пользователю
		ctx.Data(http.StatusOK, "application/vnd.apple.mpegurl", modifiedPlaylist)
	}

	//c.stream(ctx, rpURL)
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
