// Package cliupload implements the HTTP client for `imgli upload`.
package cliupload

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// Formats supported by --format / FormatOutput.
const (
	FormatURL      = "url"
	FormatMarkdown = "markdown"
	FormatJSON     = "json"
)

// Opts configures an upload (single multipart or chunked session).
type Opts struct {
	BaseURL    string // e.g. https://img.li (no trailing slash required)
	Token      string // API token; empty for guest upload when enabled
	Filename   string // multipart filename (required)
	Visibility string // optional: public | private
	ExpiresIn  int    // optional seconds; 0 = omit field
	Client     *http.Client

	// Chunked 选择分片策略: auto | always | never；空值等同 auto。
	// auto 仅在文件大小 >= ChunkThreshold（默认 32 MiB）时分片。
	Chunked string
	// ChunkThreshold 覆盖 auto 模式阈值；0 = DefaultChunkThreshold。仅测试用。
	ChunkThreshold int64
	// Resume 开启跨进程续传：分片上传把 session_id 写到本地缓存，同一文件
	// 下次上传先 GET 会话状态、只补缺失分片。CLI upload/import-dir 默认开启。
	Resume bool
	// ResumeStateDir 覆盖续传状态目录（仅测试用；空 = 用户缓存目录）。
	ResumeStateDir string

	// resumeStatePath 由 UploadFile 根据文件路径/大小/mtime 计算，仅包内使用。
	resumeStatePath string
}

// Result is the successful upload payload (subset of API data).
type Result struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Instant bool   `json:"instant"`
	Links   Links  `json:"links"`
	Expires any    `json:"expires_at"`
	RawJSON json.RawMessage
}

// Links mirrors API data.links.
type Links struct {
	URL          string `json:"url"`
	Markdown     string `json:"markdown"`
	HTML         string `json:"html"`
	BBCode       string `json:"bbcode"`
	ThumbnailURL string `json:"thumbnail_url"`
}

type envelope struct {
	Status  bool            `json:"status"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// NormalizeBaseURL trims trailing slashes and requires http(s) scheme + host.
func NormalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("base URL 不能为空（设 IMGLI_BASE_URL 或 -base-url）")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("base URL 无效: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("base URL 需为 http 或 https")
	}
	if u.Host == "" {
		return "", fmt.Errorf("base URL 缺少 host")
	}
	// Drop path/query: API is always {base}/api/v1/upload
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

// ValidateFormat returns an error if format is not supported.
func ValidateFormat(format string) error {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case FormatURL, FormatMarkdown, FormatJSON, "":
		return nil
	default:
		return fmt.Errorf("未知 --format %q（支持 url|markdown|json）", format)
	}
}

// FormatOutput renders Result for stdout.
func FormatOutput(format string, res *Result) (string, error) {
	f := strings.ToLower(strings.TrimSpace(format))
	if f == "" {
		f = FormatURL
	}
	switch f {
	case FormatURL:
		if res.Links.URL == "" {
			return "", fmt.Errorf("响应缺少 links.url")
		}
		return res.Links.URL, nil
	case FormatMarkdown:
		if res.Links.Markdown != "" {
			return res.Links.Markdown, nil
		}
		if res.Links.URL == "" {
			return "", fmt.Errorf("响应缺少 links.markdown 与 links.url")
		}
		name := res.Name
		if name == "" {
			name = "image"
		}
		return fmt.Sprintf("![%s](%s)", name, res.Links.URL), nil
	case FormatJSON:
		if len(res.RawJSON) > 0 {
			var buf bytes.Buffer
			if err := json.Indent(&buf, res.RawJSON, "", "  "); err != nil {
				return string(res.RawJSON), nil
			}
			return buf.String(), nil
		}
		b, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			return "", err
		}
		return string(b), nil
	default:
		return "", fmt.Errorf("未知 --format %q（支持 url|markdown|json）", format)
	}
}

// cleanFilename trims and reduces a filename to its base name.
func cleanFilename(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("文件名不能为空")
	}
	// Keep only base name for multipart (avoid path traversal cosmetics).
	name = filepath.Base(name)
	if name == "." || name == string(filepath.Separator) {
		return "", fmt.Errorf("文件名不能为空")
	}
	return name, nil
}

// Upload posts file content as multipart field "file" to /api/v1/upload.
func Upload(ctx context.Context, opts Opts, content io.Reader) (*Result, error) {
	base, err := NormalizeBaseURL(opts.BaseURL)
	if err != nil {
		return nil, err
	}
	name, err := cleanFilename(opts.Filename)
	if err != nil {
		return nil, err
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(fw, content); err != nil {
		return nil, fmt.Errorf("读取文件: %w", err)
	}
	vis := strings.TrimSpace(opts.Visibility)
	if vis != "" {
		if err := mw.WriteField("visibility", vis); err != nil {
			return nil, err
		}
	}
	if opts.ExpiresIn > 0 {
		if err := mw.WriteField("expires_in", fmt.Sprintf("%d", opts.ExpiresIn)); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	endpoint := base + "/api/v1/upload"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if tok := strings.TrimSpace(opts.Token); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应: %w", err)
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("HTTP %d：响应不是 JSON: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !env.Status {
		code := ""
		var data map[string]any
		if len(env.Data) > 0 {
			_ = json.Unmarshal(env.Data, &data)
			if c, ok := data["code"].(string); ok {
				code = c
			}
		}
		msg := env.Message
		if msg == "" {
			msg = string(raw)
		}
		if code != "" {
			return nil, fmt.Errorf("HTTP %d [%s]: %s", resp.StatusCode, code, msg)
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}

	var res Result
	if err := json.Unmarshal(env.Data, &res); err != nil {
		return nil, fmt.Errorf("解析 data: %w", err)
	}
	res.RawJSON = env.Data
	if res.Links.URL == "" {
		return nil, fmt.Errorf("响应缺少 links.url")
	}
	return &res, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
