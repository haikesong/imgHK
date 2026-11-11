package server

import (
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/yixian-huang/imgli/internal/handler"
	"github.com/yixian-huang/imgli/internal/model"
	"github.com/yixian-huang/imgli/internal/service/albumsvc"
	"github.com/yixian-huang/imgli/internal/service/imagesvc"
	"github.com/yixian-huang/imgli/internal/service/settings"
	"github.com/yixian-huang/imgli/internal/service/storagesvc"
)

func init() {
	// Go 默认不识别 .webmanifest,直出会落 octet-stream 触发浏览器告警;显式注册。
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

// noBuildHTML：dist 未构建（无 index.html）时的提示页。
const noBuildHTML = `<!doctype html><meta charset="utf-8"><title>图床</title>
<p style="font-family:system-ui;margin:40px">前端未构建：请运行 <code>make web</code> 后重启；开发请在 web/ 下 <code>npm run dev</code>（代理到 :8686）。</p>`

var (
	reHTMLTitle    = regexp.MustCompile(`(?is)<title>.*?</title>`)
	reOGTitle      = regexp.MustCompile(`(?is)(<meta\s+property="og:title"\s+content=")[^"]*(")`)
	reAppleAppName = regexp.MustCompile(`(?is)(<meta\s+name="apple-mobile-web-app-title"\s+content=")[^"]*(")`)
	reJSONName     = regexp.MustCompile(`"name"\s*:\s*"[^"]*"`)
	reJSONShort    = regexp.MustCompile(`"short_name"\s*:\s*"[^"]*"`)
)

// siteName 返回后台配置的站点名；未配置/未播种时返回空串。
func (s *Server) siteName() string {
	if s.opts.DB == nil {
		return ""
	}
	var name string
	if err := settings.New(s.opts.DB).Get(model.SettingSiteName, &name); err != nil {
		return ""
	}
	return strings.TrimSpace(name)
}

// injectSiteName 用后台配置的站点名覆写首屏 HTML 的标题类字段。
// 前端要等 /config 返回后才更新 document.title，服务端先写好可避免
// 打开页面时先闪出内置默认名再变成站点名。
func (s *Server) injectSiteName(doc []byte) []byte {
	name := s.siteName()
	if name == "" {
		return doc
	}
	esc := html.EscapeString(name)
	out := string(doc)
	out = reHTMLTitle.ReplaceAllStringFunc(out, func(string) string { return "<title>" + esc + "</title>" })
	out = replaceAttr(out, reOGTitle, esc)
	out = replaceAttr(out, reAppleAppName, esc)
	return []byte(out)
}

func replaceAttr(doc string, re *regexp.Regexp, value string) string {
	return re.ReplaceAllStringFunc(doc, func(m string) string {
		sub := re.FindStringSubmatch(m)
		if len(sub) < 3 {
			return m
		}
		return sub[1] + value + sub[2]
	})
}

// injectManifestName 同步 PWA 清单里的站名（静态文件按站点配置动态出）。
func (s *Server) injectManifestName(b []byte) []byte {
	name := s.siteName()
	if name == "" {
		return b
	}
	q, err := json.Marshal(name)
	if err != nil {
		return b
	}
	v := string(q)
	out := reJSONName.ReplaceAllStringFunc(string(b), func(string) string { return `"name": ` + v })
	out = reJSONShort.ReplaceAllStringFunc(out, func(string) string { return `"short_name": ` + v })
	return []byte(out)
}

// mountWeb 挂载 SPA：/assets/* 走 immutable 静态服务；其余 GET/HEAD 未匹配
// 且不属 API/直链/带扩展名的路径回落 index.html。API 路径 404 信封不变
// （/api/v1 子路由在本方法执行前已捕获 JSON NotFound）。
func (s *Server) mountWeb(dist fs.FS) {
	fileServer := http.FileServer(http.FS(dist))
	s.mux.Handle("/assets/*", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if strings.HasSuffix(r.URL.Path, "/") || !fs.ValidPath(name) {
			handler.Fail(w, http.StatusNotFound, handler.CodeNotFound, "资源不存在")
			return
		}
		if st, err := fs.Stat(dist, name); err != nil || st.IsDir() {
			handler.Fail(w, http.StatusNotFound, handler.CodeNotFound, "资源不存在")
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		fileServer.ServeHTTP(w, r)
	}))

	index := func(w http.ResponseWriter, r *http.Request) {
		b, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			b = []byte(noBuildHTML)
		}
		b = s.injectSiteName(b)
		if inj := s.ogInject(r); inj != "" {
			b = injectHead(b, inj)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	}
	s.mux.Get("/", index)
	s.mux.Head("/", index)

	jsonNotFound := func(w http.ResponseWriter, r *http.Request) {
		handler.Fail(w, http.StatusNotFound, handler.CodeNotFound, "资源不存在")
	}
	s.mux.NotFound(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if (r.Method != http.MethodGet && r.Method != http.MethodHead) ||
			strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/i/") ||
			strings.HasPrefix(p, "/t/") || strings.HasPrefix(p, "/assets/") {
			jsonNotFound(w, r)
			return
		}
		// dist 根文件（favicon 等）直出；目录路径（如无尾斜杠的 /assets）不暴露清单；
		// 其余带扩展名的未命中不回落 SPA
		name := strings.TrimPrefix(path.Clean(p), "/")
		if name != "" && fs.ValidPath(name) {
			if st, err := fs.Stat(dist, name); err == nil {
				if st.IsDir() {
					jsonNotFound(w, r)
					return
				}
				if name == "manifest.webmanifest" {
					if b, err := fs.ReadFile(dist, name); err == nil {
						w.Header().Set("Content-Type", "application/manifest+json")
						w.Header().Set("Cache-Control", "no-cache")
						_, _ = w.Write(s.injectManifestName(b))
						return
					}
				}
				http.ServeFileFS(w, r, dist, name)
				return
			}
		}
		if path.Ext(name) != "" {
			jsonNotFound(w, r)
			return
		}
		index(w, r)
	})
}

func injectHead(htmlDoc []byte, snippet string) []byte {
	s := string(htmlDoc)
	low := strings.ToLower(s)
	if i := strings.Index(low, "</head>"); i >= 0 {
		return []byte(s[:i] + snippet + s[i:])
	}
	return append([]byte(snippet), htmlDoc...)
}

// ogInject 为 /s/{key} 与 /a/{id} 注入 Open Graph 元数据（爬虫可读）。
func (s *Server) ogInject(r *http.Request) string {
	if s.opts.DB == nil || s.opts.Cfg == nil {
		return ""
	}
	p := r.URL.Path
	base := strings.TrimRight(strings.TrimSpace(s.opts.Cfg.BaseURL), "/")
	if base == "" {
		scheme := "http"
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	switch {
	case strings.HasPrefix(p, "/s/"):
		ref := strings.TrimPrefix(p, "/s/")
		ref = strings.Split(ref, "/")[0]
		if ref == "" {
			return ""
		}
		svc := s.imgSvc
		if svc == nil {
			svc = imagesvc.New(s.opts.DB, storagesvc.New(s.opts.Cfg, s.opts.DB), nil)
		}
		row, err := svc.GetPublicShare(ref)
		if err != nil || row == nil {
			return ""
		}
		title := html.EscapeString(row.Img.Name)
		// UGC share pages: allow link previews but do not target search ranking.
		robots := `<meta name="robots" content="noindex,follow"/>`
		siteLabel := s.ogSiteLabel()
		if strings.TrimSpace(row.Img.AccessPasswordHash) != "" {
			return fmt.Sprintf(`
%s
<meta property="og:type" content="website"/>
<meta property="og:title" content="%s"/>
<meta name="twitter:card" content="summary"/>
`, robots, title)
		}
		imgURL := html.EscapeString(base + "/i/" + row.Img.Key + "." + row.Img.Ext)
		pageURL := html.EscapeString(base + "/s/" + row.Img.Key)
		desc := html.EscapeString(fmt.Sprintf("%s · %s", row.Img.Name, siteLabel))
		if row.File.Width > 0 && row.File.Height > 0 {
			desc = html.EscapeString(fmt.Sprintf("%s · %d×%d · %s", row.Img.Name, row.File.Width, row.File.Height, siteLabel))
		}
		return fmt.Sprintf(`
%s
<meta property="og:type" content="website"/>
<meta property="og:title" content="%s"/>
<meta property="og:description" content="%s"/>
<meta property="og:url" content="%s"/>
<meta property="og:image" content="%s"/>
<meta name="twitter:card" content="summary_large_image"/>
<meta name="twitter:image" content="%s"/>
`, robots, title, desc, pageURL, imgURL, imgURL)
	case strings.HasPrefix(p, "/a/"):
		idStr := strings.TrimPrefix(p, "/a/")
		idStr = strings.Split(idStr, "/")[0]
		id, err := strconv.ParseUint(idStr, 10, 64)
		if err != nil || id == 0 {
			return ""
		}
		v, err := albumsvc.New(s.opts.DB).GetPublic(id)
		if err != nil || v == nil {
			return ""
		}
		title := html.EscapeString(v.Album.Name)
		pageURL := html.EscapeString(base + "/a/" + idStr)
		imgMeta := ""
		if v.CoverKey != "" {
			iu := html.EscapeString(base + "/t/" + v.CoverKey + ".jpg")
			imgMeta = fmt.Sprintf(`
<meta property="og:image" content="%s"/>
<meta name="twitter:image" content="%s"/>
`, iu, iu)
		}
		siteLabel := s.ogSiteLabel()
		return fmt.Sprintf(`
<meta name="robots" content="noindex,follow"/>
<meta property="og:type" content="website"/>
<meta property="og:title" content="%s"/>
<meta property="og:description" content="%s"/>
<meta property="og:url" content="%s"/>
<meta name="twitter:card" content="summary_large_image"/>
%s`, title, html.EscapeString(v.Album.Name+" · "+siteLabel), pageURL, imgMeta)
	default:
		return ""
	}
}

// ogSiteLabel prefers configured site_name; falls back to the base URL or a neutral label.
func (s *Server) ogSiteLabel() string {
	if name := s.siteName(); name != "" {
		return name
	}
	if s.opts.Cfg != nil {
		if h := strings.TrimSpace(s.opts.Cfg.BaseURL); h != "" {
			return strings.TrimRight(h, "/")
		}
	}
	return "图床"
}
