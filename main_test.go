package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/v2"
)

func testRouteConfig(t *testing.T, values map[string]any) *koanf.Koanf {
	t.Helper()

	routeConfig := koanf.New(".")
	if err := routeConfig.Load(confmap.Provider(values, "."), nil); err != nil {
		t.Fatal(err)
	}
	return routeConfig
}

func TestRegisterRouteTransformsRequestAndResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var upstreamPath string
	var upstreamBody string
	var upstreamContentType string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream request body: %v", err)
			return
		}

		upstreamPath = r.URL.RequestURI()
		upstreamBody = string(body)
		upstreamContentType = r.Header.Get("Content-Type")

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":` + strconv.Quote(r.URL.Path) + `}`))
	}))
	defer upstream.Close()

	engine := gin.New()
	routeConfig := testRouteConfig(t, map[string]any{
		"path":                    "/proxy/",
		"upstream":                upstream.URL + "/api",
		"request":                 `{"wrapped": .name}`,
		"response":                `.path`,
		"set.request.contenttype": "application/vnd.test+json",
	})
	if err := registerRoute(engine, routeConfig); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/proxy/items?debug=1", strings.NewReader(`{"name":"alice"}`))
	request.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if upstreamPath != "/api/items?debug=1" {
		t.Fatalf("upstream path = %q, want %q", upstreamPath, "/api/items?debug=1")
	}
	if upstreamBody != `{"wrapped":"alice"}` {
		t.Fatalf("upstream body = %q, want transformed JSON", upstreamBody)
	}
	if upstreamContentType != "application/vnd.test+json" {
		t.Fatalf("upstream content type = %q, want override", upstreamContentType)
	}
	if body := strings.TrimSpace(recorder.Body.String()); body != `"/api/items"` {
		t.Fatalf("response body = %q, want transformed JSON string", body)
	}
}

func TestRegisterRouteDefaultsToRootPath(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.URL.Path))
	}))
	defer upstream.Close()

	engine := gin.New()
	routeConfig := testRouteConfig(t, map[string]any{
		"upstream": upstream.URL + "/api",
	})
	if err := registerRoute(engine, routeConfig); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if body := strings.TrimSpace(recorder.Body.String()); body != "/api/v1/ping" {
		t.Fatalf("response body = %q, want default root proxy path", body)
	}
}

func TestEnvProviderMapsSingleRouteConfig(t *testing.T) {
	t.Setenv("JQHTTP_UPSTREAM", "http://example.test")

	envConfig := koanf.New(".")
	if err := envConfig.Load(env.Provider("JQHTTP_", ".", envKey), nil); err != nil {
		t.Fatal(err)
	}

	if got := envConfig.String("jqhttp.upstream"); got != "http://example.test" {
		t.Fatalf("jqhttp.upstream = %q, want env upstream", got)
	}
}
