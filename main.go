package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itchyny/gojq"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

var config = koanf.New(".")

func extraHeaders(header http.Header) map[string]string {
	headers := make(map[string]string, len(header))
	for key, values := range header {
		switch http.CanonicalHeaderKey(key) {
		case "Content-Length", "Content-Type":
			continue
		}
		headers[key] = strings.Join(values, ",")
	}
	return headers
}

func compileFilter(routeConfig *koanf.Koanf, key string) (*gojq.Code, error) {
	if !routeConfig.Exists(key) {
		return nil, nil
	}

	query, err := gojq.Parse(routeConfig.String(key))
	if err != nil {
		return nil, fmt.Errorf("%s jq parse: %w", key, err)
	}

	code, err := gojq.Compile(query)
	if err != nil {
		return nil, fmt.Errorf("%s jq compile: %w", key, err)
	}
	return code, nil
}

func runFilter(code *gojq.Code, data any, label string) ([]byte, error) {
	value, ok := code.Run(data).Next()
	if !ok {
		return nil, fmt.Errorf("%s jq produced no output", label)
	}
	if err, ok := value.(error); ok {
		return nil, fmt.Errorf("%s jq run: %w", label, err)
	}

	body, err := gojq.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%s jq serialize: %w", label, err)
	}
	return body, nil
}

func transformJSON(raw []byte, code *gojq.Code, label string) ([]byte, error) {
	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		log.Printf("%s json decode: %v", label, err)
		return raw, nil
	}
	return runFilter(code, data, label)
}

func requestBody(c *gin.Context, filter *gojq.Code) (io.Reader, int64, error) {
	if filter == nil || c.Request.ContentLength == 0 {
		return c.Request.Body, c.Request.ContentLength, nil
	}

	raw, err := c.GetRawData()
	if err != nil {
		return nil, 0, fmt.Errorf("request read: %w", err)
	}

	body, err := transformJSON(raw, filter, "request")
	if err != nil {
		return nil, 0, err
	}
	return bytes.NewReader(body), int64(len(body)), nil
}

func responseBody(res *http.Response, filter *gojq.Code) (io.Reader, int64, error) {
	if filter == nil || res.ContentLength == 0 {
		return res.Body, res.ContentLength, nil
	}

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("proxy response read: %w", err)
	}

	body, err := transformJSON(raw, filter, "response")
	if err != nil {
		return nil, 0, err
	}
	return bytes.NewReader(body), int64(len(body)), nil
}

func upstreamRequestURL(base *url.URL, suffix, rawQuery string) string {
	dst := *base
	if suffix != "" {
		dst.Path = path.Join(dst.Path, suffix)
	}
	if rawQuery != "" {
		if dst.RawQuery == "" {
			dst.RawQuery = rawQuery
		} else {
			dst.RawQuery += "&" + rawQuery
		}
	}
	return dst.String()
}

func envKey(name string) string {
	return strings.ReplaceAll(strings.ToLower(name), "_", ".")
}

func registerRoute(engine *gin.Engine, routeConfig *koanf.Koanf) error {
	requestFilter, err := compileFilter(routeConfig, "request")
	if err != nil {
		return err
	}
	responseFilter, err := compileFilter(routeConfig, "response")
	if err != nil {
		return err
	}

	if !routeConfig.Exists("upstream") {
		return fmt.Errorf("upstream is missing")
	}
	upstream, err := url.Parse(routeConfig.String("upstream"))
	if err != nil {
		return fmt.Errorf("upstream parse: %w", err)
	}
	if upstream.Scheme == "" || upstream.Host == "" {
		return fmt.Errorf("upstream must be an absolute URL")
	}

	routePath := routeConfig.String("path")
	if routePath == "" {
		routePath = "/"
	}
	if !strings.HasPrefix(routePath, "/") {
		return fmt.Errorf("route path %q must start with /", routePath)
	}
	if strings.HasSuffix(routePath, "/") {
		routePath += "*suffix"
	}

	engine.Any(routePath, func(c *gin.Context) {
		if err := proxyRequest(c, routeConfig, upstream, requestFilter, responseFilter); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		}
	})
	return nil
}

func proxyRequest(c *gin.Context, routeConfig *koanf.Koanf, upstream *url.URL, requestFilter, responseFilter *gojq.Code) error {
	body, contentLength, err := requestBody(c, requestFilter)
	if err != nil {
		return err
	}

	suffix, _ := c.Params.Get("suffix")
	req, err := http.NewRequestWithContext(
		c.Request.Context(),
		c.Request.Method,
		upstreamRequestURL(upstream, suffix, c.Request.URL.RawQuery),
		body,
	)
	if err != nil {
		return fmt.Errorf("proxy build: %w", err)
	}
	req.ContentLength = contentLength
	req.Header = c.Request.Header.Clone()
	req.Header.Del("Accept-Encoding")
	if routeConfig.Exists("set.request.contenttype") {
		req.Header.Set("Content-Type", routeConfig.String("set.request.contenttype"))
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("proxy request failed: %w", err)
	}
	defer res.Body.Close()

	if routeConfig.Exists("set.response.contenttype") {
		res.Header.Set("Content-Type", routeConfig.String("set.response.contenttype"))
	}

	responseReader, responseLength, err := responseBody(res, responseFilter)
	if err != nil {
		return err
	}
	c.DataFromReader(
		res.StatusCode,
		responseLength,
		res.Header.Get("Content-Type"),
		responseReader,
		extraHeaders(res.Header),
	)
	return nil
}

func main() {
	configFile := flag.String("c", "config.yml", "configuration yaml")
	flag.Parse()

	if err := config.Load(
		confmap.Provider(map[string]any{
			"listen": ":8080",
		}, "."), nil,
	); err != nil {
		log.Fatal(err)
	}

	if err := config.Load(
		env.Provider("JQHTTP_", ".", envKey),
		nil,
	); err != nil {
		log.Fatal(err)
	}

	if _, err := os.Stat(*configFile); err == nil {
		if err := config.Load(file.Provider(*configFile), yaml.Parser()); err != nil {
			log.Fatal(err)
		}
	} else if !os.IsNotExist(err) {
		log.Fatal(err)
	}

	engine := gin.Default()
	if config.Exists("jqhttp") {
		if err := registerRoute(engine, config.Cut("jqhttp")); err != nil {
			log.Fatal(err)
		}
	}
	for _, routeConfig := range config.Slices("routes") {
		if err := registerRoute(engine, routeConfig); err != nil {
			log.Fatal(err)
		}
	}
	if err := engine.Run(config.String("listen")); err != nil {
		log.Fatal(err)
	}
}
