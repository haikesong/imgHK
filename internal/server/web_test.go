package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/yixian-huang/imgli/internal/config"
	"github.com/yixian-huang/imgli/internal/model"
)

var testDist = fstest.MapFS{
	"index.html":              &fstest.MapFile{Data: []byte(`<!doctype html><div id="root">SPA</div>`)},
	"assets/index-abc123.js":  &fstest.MapFile{Data: []byte(`console.log("imgli")`)},
	"favicon.svg":             &fstest.MapFile{Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)},
}

func webGet(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestWebIndexAndSPAFallback(t *testing.T) {
	cfg, _ := config.Load("")
	s := New(Options{Cfg: cfg, Web: testDist})
	for _, p := range []string{"/", "/images", "/albums/3", "/login"} {
		rec := webGet(t, s, p)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="root"`) {
			t.Fatalf("GET %s = %d %q, want 200 index.html", p, rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
			t.Errorf("GET %s Content-Type = %q", p, ct)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("GET %s Cache-Control = %q, want no-cache", p, cc)
		}
	}
}

func TestWebStaticAssets(t *testing.T) {
	cfg, _ := config.Load("")
	s := New(Options{Cfg: cfg, Web: testDist})

	rec := webGet(t, s, "/assets/index-abc123.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("assets = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("assets Cache-Control = %q, want immutable", cc)
	}
	if rec := webGet(t, s, "/favicon.svg"); rec.Code != http.StatusOK {
		t.Errorf("favicon = %d, want 200（dist 根文件直出）", rec.Code)
	}
	if rec := webGet(t, s, "/assets/missing.js"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"not_found"`) {
		t.Errorf("missing asset = %d %q, want 404 envelope", rec.Code, rec.Body.String())
	}
}

func TestWebAssetsDirListingBlocked(t *testing.T) {
	cfg, _ := config.Load("")
	s := New(Options{Cfg: cfg, Web: testDist})

	for _, p := range []string{"/assets/", "/assets"} {
		rec := webGet(t, s, p)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"not_found"`) {
			t.Errorf("GET %s = %d %q, want 404 envelope（禁止目录清单）", p, rec.Code, rec.Body.String())
		}
	}
}

func TestWebHeadIndex(t *testing.T) {
	cfg, _ := config.Load("")
	s := New(Options{Cfg: cfg, Web: testDist})

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("HEAD / = %d, want 200", rec.Code)
	}
}

func TestWebNonSPAPathsKeepEnvelope(t *testing.T) {
	cfg, _ := config.Load("")
	s := New(Options{Cfg: cfg, Web: testDist})

	// API 前缀、带扩展名路径、非 GET：都不回落 SPA，仍是信封 404
	cases := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/unknown"},
		{http.MethodGet, "/missing.js"},
		{http.MethodPost, "/no-such"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"not_found"`) {
			t.Errorf("%s %s = %d %q, want 404 envelope", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}

func TestWebInjectsConfiguredSiteName(t *testing.T) {
	cfg, _ := config.Load("")
	dist := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(`<!doctype html><html><head>` +
			`<meta property="og:title" content="图床" /><title>图床</title>` +
			`<meta name="apple-mobile-web-app-title" content="图床" />` +
			`</head><body><div id="root"></div></body></html>`)},
		"manifest.webmanifest": &fstest.MapFile{Data: []byte(`{"name":"图床","short_name":"图床"}`)},
	}
	s := New(Options{Cfg: cfg, DB: model.TestDB(t), Web: dist})

	body := webGet(t, s, "/").Body.String()
	if !strings.Contains(body, "<title>HKSimg</title>") || !strings.Contains(body, `content="HKSimg"`) {
		t.Errorf("index HTML 未注入站点名: %s", body)
	}

	manifest := webGet(t, s, "/manifest.webmanifest").Body.String()
	if !strings.Contains(manifest, `"name": "HKSimg"`) || !strings.Contains(manifest, `"short_name": "HKSimg"`) {
		t.Errorf("manifest 未注入站点名: %s", manifest)
	}
}

func TestWebNoBuildFallback(t *testing.T) {
	cfg, _ := config.Load("")
	s := New(Options{Cfg: cfg, Web: fstest.MapFS{}})
	rec := webGet(t, s, "/")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "make web") {
		t.Fatalf("no-build fallback = %d %q, want 200 提示页", rec.Code, rec.Body.String())
	}
}
