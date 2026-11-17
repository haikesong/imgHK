package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRunUploadStdinRejectsChunkedAlways(t *testing.T) {
	err := runUpload([]string{"-chunked", "always", "-"})
	if err == nil || !strings.Contains(err.Error(), "stdin") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunUploadRejectsUnknownChunkedMode(t *testing.T) {
	err := runUpload([]string{"-chunked", "sometimes", "-"})
	if err == nil || !strings.Contains(err.Error(), "-chunked") {
		t.Fatalf("err = %v", err)
	}
}

// isolateUserCache 把 os.UserCacheDir() 指到临时目录，避免 CLI 续传状态
// 写进跑测试的人的真实缓存。
func isolateUserCache(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LocalAppData", dir)
	t.Setenv("LOCALAPPDATA", dir)
	t.Setenv("XDG_CACHE_HOME", dir)
	t.Setenv("HOME", dir)
}

func TestRunUploadFileChunkedAlwaysUsesSession(t *testing.T) {
	isolateUserCache(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "big.png")
	data := make([]byte, 64<<10) // 服务端分片下限 64KiB，恰好一片
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	var sessions, single, puts atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/upload/session", func(w http.ResponseWriter, r *http.Request) {
		sessions.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":true,"message":"ok","data":{"session_id":"s1","chunk_size":65536,"chunks_total":1,"size":65536,"expires_at":"2099-01-01T00:00:00Z"}}`)
	})
	mux.HandleFunc("PUT /api/v1/upload/session/{id}/{index}", func(w http.ResponseWriter, r *http.Request) {
		puts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":true,"message":"ok","data":{"index":0,"received_bytes":4,"missing":2}}`)
	})
	mux.HandleFunc("POST /api/v1/upload/session/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":true,"message":"ok","data":{"links":{"url":"https://example.test/i/x.png"}}}`)
	})
	mux.HandleFunc("POST /api/v1/upload", func(w http.ResponseWriter, r *http.Request) {
		single.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":true,"message":"ok","data":{"links":{"url":"https://example.test/i/single.png"}}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	err := runUploadWithStdin(
		[]string{"-base-url", srv.URL, "-chunked", "always", path},
		strings.NewReader(""),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := sessions.Load(); got != 1 {
		t.Errorf("session creates = %d, want 1", got)
	}
	if got := single.Load(); got != 0 {
		t.Errorf("single uploads = %d, want 0 (-chunked always)", got)
	}
	if got := puts.Load(); got != 1 {
		t.Errorf("chunk PUTs = %d, want 1", got)
	}
}

func TestRunUploadStdinNameOverride(t *testing.T) {
	isolateUserCache(t)
	nameCh := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/upload", func(w http.ResponseWriter, r *http.Request) {
		if file, hdr, err := r.FormFile("file"); err == nil {
			_ = file.Close()
			nameCh <- hdr.Filename
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":true,"message":"ok","data":{"links":{"url":"https://example.test/i/x.png"}}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	err := runUploadWithStdin(
		[]string{"-base-url", srv.URL, "-name", "custom.png", "-"},
		strings.NewReader("hello"),
	)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-nameCh:
		if got != "custom.png" {
			t.Errorf("multipart filename = %q, want custom.png", got)
		}
	default:
		t.Fatal("no multipart upload received")
	}
}

func TestRunUploadStdinAutoAndNeverUseSingleRequest(t *testing.T) {
	for _, mode := range []string{"auto", "never"} {
		t.Run(mode, func(t *testing.T) {
			var single, sessions atomic.Int32
			mux := http.NewServeMux()
			mux.HandleFunc("POST /api/v1/upload", func(w http.ResponseWriter, r *http.Request) {
				single.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"status":true,"message":"ok","data":{"links":{"url":"https://example.test/i/x.png"}}}`)
			})
			mux.HandleFunc("POST /api/v1/upload/session", func(w http.ResponseWriter, r *http.Request) {
				sessions.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"status":true,"message":"ok","data":{}}`)
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()

			err := runUploadWithStdin(
				[]string{"-base-url", srv.URL, "-chunked", mode, "-"},
				strings.NewReader("hello"),
			)
			if err != nil {
				t.Fatal(err)
			}
			if got := single.Load(); got != 1 {
				t.Errorf("single uploads = %d, want 1", got)
			}
			if got := sessions.Load(); got != 0 {
				t.Errorf("session calls = %d, want 0 (stdin has no size for chunking)", got)
			}
		})
	}
}
