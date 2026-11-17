package cliupload

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeCreateReq struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Visibility string `json:"visibility"`
	ExpiresIn  int    `json:"expires_in"`
}

type fakeSession struct {
	id          string
	name        string
	size        int64
	chunkSize   int64
	chunksTotal int
	chunks      map[int][]byte
	completing  bool
}

type fakeUploadServer struct {
	chunkSize int64
	server    *httptest.Server

	mu       sync.Mutex
	sessions map[string]*fakeSession
	seq      int

	createStatus          int
	createFailures        int
	createRateLimits      int
	createRetryAfter      string
	createNoRetryAfter    bool
	forceChunkSize        int64
	forceChunksTotal      int
	forceSize             *int64
	putStatus             int
	putFailures           map[int]int
	putIncomplete         map[int]int
	putBadRequestRepair   map[int]int
	putRateLimits         map[int]int
	putRetryAfter         string
	complete409           int
	complete409Chunk      int
	complete409Missing    []int
	complete409Completing bool
	// completeGoneWhenCompleting 模拟「源站合并成功后删了会话、但响应没回来」：
	// 已经处于 completing 的会话再收到 complete 时直接 404 并删除会话。
	completeGoneWhenCompleting bool
	completeRateLimits         int
	completeRetryAfter         string
	completeFailures           int
	completeBadRequest         int
	getFailStatus              int
	getFailures                int
	statusRateLimits           int
	statusRetryAfter           string
	reverseMissing             bool

	putCounts     map[int]int
	createCalls   int
	completeCalls int
	statusCalls   int
	singleCalls   int
	deletes       int
	singleBody    []byte
	createNames   []string
	lastCreate    fakeCreateReq
	authHeaders   []string
	active        int
	maxActive     int
	putDelay      time.Duration
	blockPut      bool
	putStarted    chan struct{}
	unblock       chan struct{}
	expected      []byte
}

func newFakeUploadServer(t *testing.T, chunkSize int64) *fakeUploadServer {
	t.Helper()
	f := &fakeUploadServer{
		chunkSize:           chunkSize,
		sessions:            map[string]*fakeSession{},
		putFailures:         map[int]int{},
		putIncomplete:       map[int]int{},
		putBadRequestRepair: map[int]int{},
		putRateLimits:       map[int]int{},
		complete409Chunk:    -1,
		putCounts:           map[int]int{},
		putStarted:          make(chan struct{}, 1),
		unblock:             make(chan struct{}),
	}
	// 多数用例用 3–4 字节分片覆盖多片路径；生产下限是 64KiB，
	// 这里临时放宽，测试结束自动恢复。
	oldMin := chunkMinSize
	chunkMinSize = 1
	t.Cleanup(func() { chunkMinSize = oldMin })
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/upload/session", f.handleCreate)
	mux.HandleFunc("PUT /api/v1/upload/session/{id}/{index}", f.handlePut)
	mux.HandleFunc("GET /api/v1/upload/session/{id}", f.handleGet)
	mux.HandleFunc("POST /api/v1/upload/session/{id}/complete", f.handleComplete)
	mux.HandleFunc("DELETE /api/v1/upload/session/{id}", f.handleDelete)
	mux.HandleFunc("POST /api/v1/upload", f.handleSingle)
	f.server = httptest.NewServer(mux)
	// Cleanups run LIFO: unblock handlers before waiting for the server to close.
	t.Cleanup(f.server.Close)
	t.Cleanup(func() { close(f.unblock) })
	return f
}

func (f *fakeUploadServer) opts() Opts {
	return Opts{
		BaseURL:    f.server.URL,
		Token:      "tok",
		Filename:   "pic.png",
		Visibility: "public",
		ExpiresIn:  3600,
		Client:     f.server.Client(),
	}
}

func (f *fakeUploadServer) recordAuth(r *http.Request) {
	f.mu.Lock()
	f.authHeaders = append(f.authHeaders, r.Header.Get("Authorization"))
	f.mu.Unlock()
}

func writeOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	raw, _ := json.Marshal(map[string]any{"status": true, "message": "ok", "data": data})
	_, _ = w.Write(raw)
}

func writeAPIError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	raw, _ := json.Marshal(map[string]any{
		"status":  false,
		"message": msg,
		"data":    map[string]any{"code": code},
	})
	_, _ = w.Write(raw)
}

func fakeResultData(name string, size int64) map[string]any {
	return map[string]any{
		"key":     "k1",
		"name":    name,
		"size":    size,
		"instant": false,
		"links": map[string]any{
			"url":           "https://example.test/i/k1.png",
			"markdown":      "![pic.png](https://example.test/i/k1.png)",
			"html":          "",
			"bbcode":        "",
			"thumbnail_url": "",
		},
		"expires_at": nil,
	}
}

func (f *fakeUploadServer) handleCreate(w http.ResponseWriter, r *http.Request) {
	f.recordAuth(r)
	var req fakeCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "bad json")
		return
	}

	f.mu.Lock()
	f.createCalls++
	f.lastCreate = req
	if f.createFailures > 0 {
		f.createFailures--
		f.mu.Unlock()
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "boom")
		return
	}
	if f.createStatus != 0 {
		st := f.createStatus
		f.mu.Unlock()
		w.WriteHeader(st)
		_, _ = w.Write([]byte("session route unavailable"))
		return
	}
	if f.createRateLimits > 0 {
		f.createRateLimits--
		ra := f.createRetryAfter
		noRA := f.createNoRetryAfter
		f.mu.Unlock()
		if !noRA {
			if ra == "" {
				ra = "0"
			}
			w.Header().Set("Retry-After", ra)
		}
		writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "slow down")
		return
	}
	f.seq++
	id := fmt.Sprintf("%032x", f.seq)
	chunkSize := f.chunkSize
	if f.forceChunkSize != 0 {
		chunkSize = f.forceChunkSize
	}
	respSize := req.Size
	if f.forceSize != nil {
		respSize = *f.forceSize
	}
	chunksTotal := int((respSize-1)/chunkSize + 1)
	if f.forceChunksTotal != 0 {
		chunksTotal = f.forceChunksTotal
	}
	sess := &fakeSession{
		id:          id,
		name:        req.Name,
		size:        respSize,
		chunkSize:   chunkSize,
		chunksTotal: chunksTotal,
		chunks:      map[int][]byte{},
	}
	f.sessions[id] = sess
	f.createNames = append(f.createNames, req.Name)
	f.mu.Unlock()

	writeOK(w, map[string]any{
		"session_id":   sess.id,
		"chunk_size":   sess.chunkSize,
		"chunks_total": sess.chunksTotal,
		"size":         sess.size,
		"expires_at":   "2099-01-01T00:00:00Z",
	})
}

func (f *fakeUploadServer) receivedLocked(s *fakeSession) (int64, []int, []int) {
	var got int64
	var have, missing []int
	for i := 0; i < s.chunksTotal; i++ {
		want := s.chunkSize
		if i == s.chunksTotal-1 {
			want = s.size - int64(i)*s.chunkSize
		}
		if b, ok := s.chunks[i]; ok && int64(len(b)) == want {
			got += int64(len(b))
			have = append(have, i)
		} else {
			missing = append(missing, i)
		}
	}
	return got, have, missing
}

func (f *fakeUploadServer) handlePut(w http.ResponseWriter, r *http.Request) {
	f.recordAuth(r)
	id := r.PathValue("id")
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "bad index")
		return
	}

	f.mu.Lock()
	sess := f.sessions[id]
	if sess == nil {
		f.mu.Unlock()
		writeAPIError(w, http.StatusNotFound, "not_found", "上传会话不存在或已过期")
		return
	}
	if idx < 0 || idx >= sess.chunksTotal {
		f.mu.Unlock()
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "分片序号不合法")
		return
	}
	f.putCounts[idx]++
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()

	f.mu.Lock()
	if n := f.putIncomplete[idx]; n > 0 {
		f.putIncomplete[idx] = n - 1
		f.mu.Unlock()
		writeAPIError(w, http.StatusConflict, "upload_incomplete", "会话正在合并，请稍后重试")
		return
	}
	// 只在补传阶段失败（首轮已经成功过一次），用于测试 409 后补片失败。
	if n := f.putBadRequestRepair[idx]; n > 0 && f.putCounts[idx] > 1 {
		f.putBadRequestRepair[idx] = n - 1
		f.mu.Unlock()
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "forced repair failure")
		return
	}
	f.mu.Unlock()

	select {
	case f.putStarted <- struct{}{}:
	default:
	}
	if f.putDelay > 0 {
		time.Sleep(f.putDelay)
	}
	if f.blockPut {
		select {
		case <-r.Context().Done():
		case <-f.unblock:
		}
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "read body")
		return
	}

	f.mu.Lock()
	if f.putStatus != 0 {
		st := f.putStatus
		f.mu.Unlock()
		writeAPIError(w, st, "invalid_request", "forced failure")
		return
	}
	if n := f.putFailures[idx]; n > 0 {
		f.putFailures[idx] = n - 1
		f.mu.Unlock()
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "boom")
		return
	}
	if n := f.putRateLimits[idx]; n > 0 {
		f.putRateLimits[idx] = n - 1
		ra := f.putRetryAfter
		f.mu.Unlock()
		if ra == "" {
			ra = "0"
		}
		w.Header().Set("Retry-After", ra)
		writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "slow down")
		return
	}
	want := sess.chunkSize
	if idx == sess.chunksTotal-1 {
		want = sess.size - int64(idx)*sess.chunkSize
	}
	if int64(len(body)) != want {
		f.mu.Unlock()
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "分片大小不符")
		return
	}
	if r.ContentLength != want {
		f.mu.Unlock()
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "Content-Length 不符")
		return
	}
	sess.chunks[idx] = append([]byte(nil), body...)
	got, _, missing := f.receivedLocked(sess)
	f.mu.Unlock()

	writeOK(w, map[string]any{
		"index":          idx,
		"received_bytes": got,
		"missing":        len(missing),
	})
}

func (f *fakeUploadServer) handleGet(w http.ResponseWriter, r *http.Request) {
	f.recordAuth(r)
	f.mu.Lock()
	f.statusCalls++
	sess := f.sessions[r.PathValue("id")]
	if sess == nil {
		f.mu.Unlock()
		writeAPIError(w, http.StatusNotFound, "not_found", "上传会话不存在或已过期")
		return
	}
	if f.getFailures > 0 {
		f.getFailures--
		st := f.getFailStatus
		if st == 0 {
			st = http.StatusInternalServerError
		}
		code := "internal_error"
		if st == http.StatusNotFound {
			code = "not_found"
		}
		f.mu.Unlock()
		writeAPIError(w, st, code, "forced status failure")
		return
	}
	if f.statusRateLimits > 0 {
		f.statusRateLimits--
		ra := f.statusRetryAfter
		f.mu.Unlock()
		if ra == "" {
			ra = "0"
		}
		w.Header().Set("Retry-After", ra)
		writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "slow down")
		return
	}
	got, have, missing := f.receivedLocked(sess)
	if f.reverseMissing {
		for i, j := 0, len(missing)-1; i < j; i, j = i+1, j-1 {
			missing[i], missing[j] = missing[j], missing[i]
		}
	}
	state := "open"
	if sess.completing {
		state = "completing"
	}
	f.mu.Unlock()

	writeOK(w, map[string]any{
		"session_id":     sess.id,
		"name":           sess.name,
		"size":           sess.size,
		"chunk_size":     sess.chunkSize,
		"chunks_total":   sess.chunksTotal,
		"received_bytes": got,
		"received":       have,
		"missing":        missing,
		"state":          state,
		"expires_at":     "2099-01-01T00:00:00Z",
	})
}

func (f *fakeUploadServer) handleComplete(w http.ResponseWriter, r *http.Request) {
	f.recordAuth(r)
	f.mu.Lock()
	sess := f.sessions[r.PathValue("id")]
	if sess == nil {
		f.mu.Unlock()
		writeAPIError(w, http.StatusNotFound, "not_found", "上传会话不存在或已过期")
		return
	}
	f.completeCalls++
	if f.completeBadRequest > 0 {
		f.completeBadRequest--
		f.mu.Unlock()
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "forced complete failure")
		return
	}
	if f.completeRateLimits > 0 {
		f.completeRateLimits--
		ra := f.completeRetryAfter
		f.mu.Unlock()
		if ra == "" {
			ra = "0"
		}
		w.Header().Set("Retry-After", ra)
		writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "slow down")
		return
	}
	if f.completeFailures > 0 {
		f.completeFailures--
		f.mu.Unlock()
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "boom")
		return
	}
	if f.complete409 > 0 {
		f.complete409--
		if f.complete409Chunk >= 0 {
			delete(sess.chunks, f.complete409Chunk)
		}
		for _, idx := range f.complete409Missing {
			delete(sess.chunks, idx)
		}
		sess.completing = f.complete409Completing
		f.mu.Unlock()
		writeAPIError(w, http.StatusConflict, "upload_incomplete", "还缺分片")
		return
	}
	if f.completeGoneWhenCompleting && sess.completing {
		delete(f.sessions, sess.id)
		f.mu.Unlock()
		writeAPIError(w, http.StatusNotFound, "not_found", "上传会话不存在或已过期")
		return
	}
	_, _, missing := f.receivedLocked(sess)
	if len(missing) > 0 {
		f.mu.Unlock()
		writeAPIError(w, http.StatusConflict, "upload_incomplete", "还缺分片")
		return
	}
	var assembled []byte
	for i := 0; i < sess.chunksTotal; i++ {
		assembled = append(assembled, sess.chunks[i]...)
	}
	name, size := sess.name, sess.size
	expected := f.expected
	f.mu.Unlock()

	if expected != nil && !bytes.Equal(assembled, expected) {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "拼装大小不符")
		return
	}
	writeOK(w, fakeResultData(name, size))
}

func (f *fakeUploadServer) handleDelete(w http.ResponseWriter, r *http.Request) {
	f.recordAuth(r)
	f.mu.Lock()
	f.deletes++
	delete(f.sessions, r.PathValue("id"))
	f.mu.Unlock()
	writeOK(w, nil)
}

func (f *fakeUploadServer) handleSingle(w http.ResponseWriter, r *http.Request) {
	f.recordAuth(r)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "bad multipart")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "no file")
		return
	}
	defer file.Close()
	body, err := io.ReadAll(file)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "read file")
		return
	}
	f.mu.Lock()
	f.singleCalls++
	f.singleBody = body
	f.mu.Unlock()
	writeOK(w, fakeResultData("pic.png", int64(len(body))))
}

func (f *fakeUploadServer) snapshot() (create, complete, single, deletes int, putCounts map[int]int, maxActive int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	putCounts = make(map[int]int, len(f.putCounts))
	for k, v := range f.putCounts {
		putCounts[k] = v
	}
	return f.createCalls, f.completeCalls, f.singleCalls, f.deletes, putCounts, f.maxActive
}

func TestDefaultChunkThreshold(t *testing.T) {
	if DefaultChunkThreshold != 32<<20 {
		t.Fatalf("DefaultChunkThreshold = %d, want %d", DefaultChunkThreshold, int64(32<<20))
	}
}

func TestUploadChunkedSuccess(t *testing.T) {
	data := []byte("abcdefghij") // 10 bytes, chunk size 3 => 3,3,3,1
	f := newFakeUploadServer(t, 3)
	f.expected = data
	f.putDelay = 20 * time.Millisecond

	res, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if res.Links.URL != "https://example.test/i/k1.png" {
		t.Fatalf("links.url = %q", res.Links.URL)
	}
	if len(res.RawJSON) == 0 || !bytes.Contains(res.RawJSON, []byte(`"links"`)) {
		t.Fatalf("RawJSON not set: %q", res.RawJSON)
	}

	create, complete, single, deletes, puts, maxActive := f.snapshot()
	if create != 1 {
		t.Errorf("createCalls = %d, want 1", create)
	}
	if complete != 1 {
		t.Errorf("completeCalls = %d, want 1", complete)
	}
	if single != 0 {
		t.Errorf("singleCalls = %d, want 0", single)
	}
	if deletes != 0 {
		t.Errorf("deletes = %d, want 0 on success", deletes)
	}
	for i := 0; i < 4; i++ {
		if puts[i] != 1 {
			t.Errorf("putCounts[%d] = %d, want 1", i, puts[i])
		}
	}
	if maxActive < 2 {
		t.Errorf("maxActive = %d, want >= 2 (chunk PUTs should be concurrent)", maxActive)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lastCreate.Visibility != "public" {
		t.Errorf("visibility = %q", f.lastCreate.Visibility)
	}
	if f.lastCreate.ExpiresIn != 3600 {
		t.Errorf("expires_in = %d", f.lastCreate.ExpiresIn)
	}
	if len(f.authHeaders) == 0 {
		t.Fatal("no requests recorded")
	}
	for _, a := range f.authHeaders {
		if a != "Bearer tok" {
			t.Errorf("Authorization = %q, want Bearer tok", a)
		}
	}
}

func TestUploadChunkedRetries(t *testing.T) {
	data := []byte("abcdefg") // 7 bytes, chunk size 3 => 3 chunks

	t.Run("500 once", func(t *testing.T) {
		f := newFakeUploadServer(t, 3)
		f.expected = data
		f.putFailures[0] = 1

		if _, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data))); err != nil {
			t.Fatal(err)
		}
		_, _, _, deletes, puts, _ := f.snapshot()
		if puts[0] != 2 {
			t.Errorf("putCounts[0] = %d, want 2", puts[0])
		}
		if deletes != 0 {
			t.Errorf("deletes = %d", deletes)
		}
	})

	t.Run("429 retry after", func(t *testing.T) {
		f := newFakeUploadServer(t, 3)
		f.expected = data
		f.putRateLimits[1] = 1

		if _, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data))); err != nil {
			t.Fatal(err)
		}
		_, _, _, deletes, puts, _ := f.snapshot()
		if puts[1] != 2 {
			t.Errorf("putCounts[1] = %d, want 2", puts[1])
		}
		if deletes != 0 {
			t.Errorf("deletes = %d", deletes)
		}
	})
}

func TestUploadChunkedCreateRetry(t *testing.T) {
	data := []byte("abcdefg") // 7 bytes, chunk size 3 => 3 chunks

	t.Run("429 with Retry-After retries", func(t *testing.T) {
		f := newFakeUploadServer(t, 3)
		f.expected = data
		f.createRateLimits = 1
		f.createRetryAfter = "0"

		if _, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data))); err != nil {
			t.Fatal(err)
		}
		create, _, _, deletes, _, _ := f.snapshot()
		if create != 2 {
			t.Errorf("createCalls = %d, want 2 (one 429 + one retry)", create)
		}
		if deletes != 0 {
			t.Errorf("deletes = %d, want 0", deletes)
		}
	})

	t.Run("429 without Retry-After fails fast", func(t *testing.T) {
		f := newFakeUploadServer(t, 3)
		f.createRateLimits = 3
		f.createNoRetryAfter = true

		_, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data)))
		if err == nil {
			t.Fatal("want error")
		}
		var he *apiHTTPError
		if !errors.As(err, &he) || he.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("err = %v, want 429 apiHTTPError", err)
		}
		create, _, single, _, _, _ := f.snapshot()
		if create != 1 {
			t.Errorf("createCalls = %d, want 1 (no retry without Retry-After)", create)
		}
		if single != 0 {
			t.Errorf("singleCalls = %d, want 0 (429 must not fall back)", single)
		}
	})

	t.Run("500 once retries", func(t *testing.T) {
		f := newFakeUploadServer(t, 3)
		f.expected = data
		f.createFailures = 1

		if _, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data))); err != nil {
			t.Fatal(err)
		}
		create, _, _, deletes, _, _ := f.snapshot()
		if create != 2 {
			t.Errorf("createCalls = %d, want 2 (one 500 + one retry)", create)
		}
		if deletes != 0 {
			t.Errorf("deletes = %d, want 0", deletes)
		}
	})
}

// countingErrTransport 模拟「请求可能已送达但响应丢失」的传输层错误。
type countingErrTransport struct{ calls atomic.Int32 }

func (t *countingErrTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return nil, errors.New("connection reset by peer")
}

func TestUploadChunkedCreateTransportErrorNotRetried(t *testing.T) {
	data := []byte("abcdefg")
	f := newFakeUploadServer(t, 3)
	tr := &countingErrTransport{}
	opts := f.opts()
	opts.Client = &http.Client{Transport: tr}

	_, err := UploadChunked(context.Background(), opts, bytes.NewReader(data), int64(len(data)))
	if err == nil {
		t.Fatal("want error")
	}
	if got := tr.calls.Load(); got != 1 {
		t.Errorf("create attempts = %d, want 1 (non-idempotent create must not retry transport errors)", got)
	}
	// 单元层面同样确认。
	uerr := &url.Error{Op: "Post", URL: "http://example.test", Err: errors.New("reset")}
	if retryableCreateError(context.Background(), uerr) {
		t.Error("retryableCreateError(url.Error) = true, want false")
	}
}

func TestUploadChunkedCompleteRetry(t *testing.T) {
	data := []byte("abcdefghi") // 9 bytes, chunk size 4 => 3 chunks

	t.Run("429 with Retry-After retries", func(t *testing.T) {
		f := newFakeUploadServer(t, 4)
		f.expected = data
		f.completeRateLimits = 1
		f.completeRetryAfter = "0"

		if _, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data))); err != nil {
			t.Fatal(err)
		}
		_, complete, _, deletes, puts, _ := f.snapshot()
		if complete != 2 {
			t.Errorf("completeCalls = %d, want 2", complete)
		}
		if deletes != 0 {
			t.Errorf("deletes = %d, want 0 (keep session for retry)", deletes)
		}
		for i := 0; i < 3; i++ {
			if puts[i] != 1 {
				t.Errorf("putCounts[%d] = %d, want 1", i, puts[i])
			}
		}
	})

	t.Run("5xx retries", func(t *testing.T) {
		f := newFakeUploadServer(t, 4)
		f.expected = data
		f.completeFailures = 1

		if _, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data))); err != nil {
			t.Fatal(err)
		}
		_, complete, _, deletes, _, _ := f.snapshot()
		if complete != 2 {
			t.Errorf("completeCalls = %d, want 2", complete)
		}
		if deletes != 0 {
			t.Errorf("deletes = %d, want 0", deletes)
		}
	})

	t.Run("exhausted 5xx keeps session", func(t *testing.T) {
		f := newFakeUploadServer(t, 4)
		f.completeFailures = 100

		_, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data)))
		if err == nil {
			t.Fatal("want error")
		}
		_, complete, _, deletes, _, _ := f.snapshot()
		if complete != chunkRetryAttempts {
			t.Errorf("completeCalls = %d, want %d", complete, chunkRetryAttempts)
		}
		if deletes != 0 {
			t.Errorf("deletes = %d, want 0 (retryable failure keeps session)", deletes)
		}
	})

	t.Run("non-retryable 400 deletes session", func(t *testing.T) {
		f := newFakeUploadServer(t, 4)
		f.expected = data
		f.completeBadRequest = 1

		_, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data)))
		if err == nil {
			t.Fatal("want error")
		}
		var he *apiHTTPError
		if !errors.As(err, &he) || he.StatusCode != http.StatusBadRequest {
			t.Fatalf("err = %v, want 400 apiHTTPError", err)
		}
		_, complete, _, deletes, _, _ := f.snapshot()
		if complete != 1 {
			t.Errorf("completeCalls = %d, want 1 (400 must not retry)", complete)
		}
		if deletes != 1 {
			t.Errorf("deletes = %d, want 1 (permanent complete failure must clean up)", deletes)
		}
	})
}

func TestUploadChunkedStatusFailureBranches(t *testing.T) {
	data := []byte("abcdefghi") // 9 bytes, chunk size 4 => 3 chunks

	t.Run("409 then status 404 deletes session", func(t *testing.T) {
		f := newFakeUploadServer(t, 4)
		f.expected = data
		f.complete409 = 1
		f.getFailStatus = http.StatusNotFound
		f.getFailures = 1

		_, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data)))
		if err == nil {
			t.Fatal("want error")
		}
		var he *apiHTTPError
		if !errors.As(err, &he) || he.StatusCode != http.StatusNotFound {
			t.Fatalf("err = %v, want 404 apiHTTPError", err)
		}
		_, complete, _, deletes, _, _ := f.snapshot()
		if complete != 1 {
			t.Errorf("completeCalls = %d, want 1", complete)
		}
		if deletes != 1 {
			t.Errorf("deletes = %d, want 1 (expired session cannot be resumed)", deletes)
		}
	})

	t.Run("409 then exhausted status 5xx keeps session", func(t *testing.T) {
		f := newFakeUploadServer(t, 4)
		f.expected = data
		f.complete409 = 1
		f.getFailStatus = http.StatusInternalServerError
		f.getFailures = 100

		_, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data)))
		if err == nil {
			t.Fatal("want error")
		}
		var he *apiHTTPError
		if !errors.As(err, &he) || he.StatusCode != http.StatusInternalServerError {
			t.Fatalf("err = %v, want 500 apiHTTPError", err)
		}
		_, _, _, deletes, _, _ := f.snapshot()
		f.mu.Lock()
		statusCalls := f.statusCalls
		f.mu.Unlock()
		if statusCalls != chunkRetryAttempts {
			t.Errorf("statusCalls = %d, want %d", statusCalls, chunkRetryAttempts)
		}
		if deletes != 0 {
			t.Errorf("deletes = %d, want 0 (retryable status failure keeps session)", deletes)
		}
	})
}

func TestUploadChunkedStatusRetry(t *testing.T) {
	data := []byte("abcdefghi") // 9 bytes, chunk size 4 => 3 chunks
	f := newFakeUploadServer(t, 4)
	f.expected = data
	f.complete409 = 1
	f.complete409Chunk = 1
	f.statusRateLimits = 1
	f.statusRetryAfter = "0"

	if _, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	_, complete, _, deletes, puts, _ := f.snapshot()
	f.mu.Lock()
	statusCalls := f.statusCalls
	f.mu.Unlock()
	if statusCalls != 2 {
		t.Errorf("statusCalls = %d, want 2 (one 429 + one retry)", statusCalls)
	}
	if complete != 2 {
		t.Errorf("completeCalls = %d, want 2", complete)
	}
	if puts[1] != 2 {
		t.Errorf("putCounts[1] = %d, want 2", puts[1])
	}
	if deletes != 0 {
		t.Errorf("deletes = %d, want 0", deletes)
	}
}

func TestUploadChunkedNegativeExpiresInOmitted(t *testing.T) {
	data := []byte("abcdefg")
	f := newFakeUploadServer(t, 3)
	f.expected = data
	opts := f.opts()
	opts.ExpiresIn = -5

	if _, err := UploadChunked(context.Background(), opts, bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	got := f.lastCreate.ExpiresIn
	f.mu.Unlock()
	if got != 0 {
		t.Errorf("expires_in = %d, want 0 (negative must be omitted, not serialized)", got)
	}
}

func TestChunkMaxMirrorsServerCap(t *testing.T) {
	// 服务端 uploadChunkMax = 16MiB。客户端上限更大时，服务端返回的
	// chunk_size 会通过校验却在每个 PUT 上 413，直到删会话才报错。
	if chunkMaxSize != 16<<20 {
		t.Fatalf("chunkMaxSize = %d, want %d", chunkMaxSize, int64(16<<20))
	}
}

func TestUploadChunkedInputValidation(t *testing.T) {
	f := newFakeUploadServer(t, 4)
	if _, err := UploadChunked(context.Background(), f.opts(), nil, 10); err == nil ||
		!strings.Contains(err.Error(), "随机读取") {
		t.Fatalf("nil reader err = %v", err)
	}
	if _, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(nil), 0); err == nil ||
		!strings.Contains(err.Error(), "文件大小无效") {
		t.Fatalf("size=0 err = %v", err)
	}
	create, _, _, deletes, _, _ := f.snapshot()
	if create != 0 || deletes != 0 {
		t.Errorf("create=%d deletes=%d, want 0/0 (reject before creating a session)", create, deletes)
	}
}

// shortReaderAt 返回比请求更少的字节但不报错，模拟截断/稀疏文件。
type shortReaderAt struct{ data []byte }

func (s shortReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(s.data)) {
		return 0, io.EOF
	}
	return copy(p, s.data[off:]), nil
}

// errReaderAt 模拟读片时的 IO 错误。
type errReaderAt struct{}

func (errReaderAt) ReadAt([]byte, int64) (int, error) { return 0, errors.New("disk error") }

func TestUploadChunkedReadErrorsDeleteSession(t *testing.T) {
	cases := []struct {
		name string
		r    io.ReaderAt
		want string
	}{
		{"short read", shortReaderAt{data: []byte("abc")}, "期望"},
		{"read error", errReaderAt{}, "disk error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeUploadServer(t, 4)
			_, err := UploadChunked(context.Background(), f.opts(), tc.r, 10)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
			_, _, _, deletes, _, _ := f.snapshot()
			if deletes != 1 {
				t.Errorf("deletes = %d, want 1 (created session must be cleaned up)", deletes)
			}
		})
	}
}

func TestChunkLenLastChunk(t *testing.T) {
	u := &chunkUploader{
		chunkSize:   4,
		chunksTotal: 3,
		size:        10,
		r:           bytes.NewReader([]byte("abcdefghij")),
	}
	n, err := u.chunkLen(2)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("last chunk len = %d, want 2 (10 - 2*4)", n)
	}
	buf, err := u.readChunk(2)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ij" {
		t.Errorf("last chunk = %q, want %q", buf, "ij")
	}
}

func TestUploadFileResumeAfterKeptSession(t *testing.T) {
	data := []byte("abcdefghij") // 10 bytes, chunk size 3 => 4 chunks
	dir := t.TempDir()
	path := filepath.Join(dir, "pic.png")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	f := newFakeUploadServer(t, 3)
	f.expected = data
	f.completeFailures = chunkRetryAttempts // 首次 complete 的可重试失败耗尽

	stateDir := t.TempDir()
	opts := f.opts()
	opts.Chunked = "always"
	opts.Resume = true
	opts.ResumeStateDir = stateDir
	opts.Filename = ""

	if _, err := UploadFile(context.Background(), opts, path); err == nil {
		t.Fatal("want first run to fail")
	}
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("resume state files = %d, want 1 (retryable complete failure must persist session)", len(entries))
	}

	f.mu.Lock()
	f.completeFailures = 0
	f.mu.Unlock()

	res, err := UploadFile(context.Background(), opts, path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Links.URL == "" {
		t.Fatal("missing links.url")
	}
	create, _, _, deletes, puts, _ := f.snapshot()
	if create != 1 {
		t.Errorf("createCalls = %d, want 1 (second run must resume, not create)", create)
	}
	if deletes != 0 {
		t.Errorf("deletes = %d, want 0", deletes)
	}
	for i := 0; i < 4; i++ {
		if puts[i] != 1 {
			t.Errorf("putCounts[%d] = %d, want 1 (resume uploads only missing chunks)", i, puts[i])
		}
	}
	entries, err = os.ReadDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("resume state files = %d after success, want 0", len(entries))
	}
}

func TestUploadChunkedRejectsInvalidCreateResponse(t *testing.T) {
	data := []byte("abcdefghij")
	cases := []struct {
		name      string
		configure func(f *fakeUploadServer, respSize *int64)
		want      string
	}{
		{"chunk size below minimum", func(f *fakeUploadServer, _ *int64) { f.forceChunkSize = 1024 }, "chunk_size"},
		{"chunk size above maximum", func(f *fakeUploadServer, _ *int64) { f.forceChunkSize = chunkMaxSize + 1 }, "chunk_size"},
		{"chunks total above cap", func(f *fakeUploadServer, _ *int64) {
			f.forceChunkSize = chunkMinSize
			f.forceChunksTotal = chunkMaxTotal + 1
		}, "chunks_total"},
		{"chunks total mismatch", func(f *fakeUploadServer, _ *int64) {
			f.forceChunkSize = chunkMinSize
			f.forceChunksTotal = 99
		}, "chunks_total"},
		{"session size mismatch", func(f *fakeUploadServer, respSize *int64) {
			f.forceChunkSize = chunkMinSize
			*respSize = 999
			f.forceSize = respSize
		}, "大小"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeUploadServer(t, 3)
			// 恢复生产下限，确保非法 chunk_size 确实被拒绝。
			chunkMinSize = 64 << 10
			respSize := int64(0)
			tc.configure(f, &respSize)

			_, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
			_, _, _, deletes, _, _ := f.snapshot()
			if deletes != 1 {
				t.Errorf("deletes = %d, want 1 (created session must be cleaned up)", deletes)
			}
		})
	}
}

func TestUploadChunkedCompleteIncomplete(t *testing.T) {
	data := []byte("abcdefghi") // 9 bytes, chunk size 4 => 3 chunks
	f := newFakeUploadServer(t, 4)
	f.expected = data
	f.complete409 = 1
	f.complete409Chunk = 1 // 模拟第 2 片没收到

	if _, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	create, complete, _, deletes, puts, _ := f.snapshot()
	if create != 1 {
		t.Errorf("createCalls = %d", create)
	}
	if complete != 2 {
		t.Errorf("completeCalls = %d, want 2", complete)
	}
	if puts[1] != 2 {
		t.Errorf("putCounts[1] = %d, want 2 (re-upload after 409)", puts[1])
	}
	if deletes != 0 {
		t.Errorf("deletes = %d", deletes)
	}
}

func TestUploadChunkedMultipleMissingOutOfOrder(t *testing.T) {
	data := []byte("abcdefghijkl") // 12 bytes, chunk size 3 => 4 chunks
	f := newFakeUploadServer(t, 3)
	f.expected = data
	f.complete409 = 1
	f.complete409Missing = []int{0, 2, 3}
	f.reverseMissing = true // GET 返回乱序 missing: [3,2,0]

	if _, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	_, complete, _, deletes, puts, _ := f.snapshot()
	if complete != 2 {
		t.Errorf("completeCalls = %d, want 2", complete)
	}
	for _, idx := range []int{0, 2, 3} {
		if puts[idx] != 2 {
			t.Errorf("putCounts[%d] = %d, want 2 (re-uploaded after 409)", idx, puts[idx])
		}
	}
	if puts[1] != 1 {
		t.Errorf("putCounts[1] = %d, want 1 (present chunk must not be re-uploaded)", puts[1])
	}
	if deletes != 0 {
		t.Errorf("deletes = %d, want 0", deletes)
	}
}

func TestUploadChunkedLastRepairGetsComplete(t *testing.T) {
	data := []byte("abcdefghi") // 9 bytes, chunk size 4 => 3 chunks
	f := newFakeUploadServer(t, 4)
	f.expected = data
	f.complete409 = 3
	f.complete409Chunk = 1 // 每次都丢掉第 2 片

	if _, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	_, complete, _, deletes, puts, _ := f.snapshot()
	// 3 次 409 各补传一次，第 4 次 complete 必须真的发生（旧实现第 3 次补传白做）。
	if complete != 4 {
		t.Errorf("completeCalls = %d, want 4", complete)
	}
	if puts[1] != 4 {
		t.Errorf("putCounts[1] = %d, want 4", puts[1])
	}
	if deletes != 0 {
		t.Errorf("deletes = %d, want 0", deletes)
	}
}

func TestUploadChunkedSingleChunkSession(t *testing.T) {
	data := []byte("0123456789") // 10 bytes; 64KiB chunk => one chunk
	f := newFakeUploadServer(t, 64<<10)
	chunkMinSize = 64 << 10 // 生产下限；chunk_size 大于整个文件
	f.expected = data

	res, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if res.Links.URL == "" {
		t.Fatal("missing links.url")
	}
	create, complete, _, deletes, puts, _ := f.snapshot()
	if create != 1 || complete != 1 {
		t.Errorf("create=%d complete=%d, want 1/1", create, complete)
	}
	if len(puts) != 1 || puts[0] != 1 {
		t.Errorf("putCounts = %v, want only index 0 once", puts)
	}
	if deletes != 0 {
		t.Errorf("deletes = %d, want 0", deletes)
	}
	f.mu.Lock()
	gotSize := f.lastCreate.Size
	f.mu.Unlock()
	if gotSize != int64(len(data)) {
		t.Errorf("create size = %d, want %d", gotSize, len(data))
	}
}

func TestUploadChunkedCompleteCompleting(t *testing.T) {
	old := chunkCompletingWait
	chunkCompletingWait = 5 * time.Millisecond
	t.Cleanup(func() { chunkCompletingWait = old })

	data := []byte("abcdefgh") // 8 bytes, chunk size 4 => 2 chunks
	f := newFakeUploadServer(t, 4)
	f.expected = data
	f.complete409 = 1
	f.complete409Completing = true // GET 报告 state=completing

	if _, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	_, complete, _, deletes, puts, _ := f.snapshot()
	if complete != 2 {
		t.Errorf("completeCalls = %d, want 2", complete)
	}
	if puts[0] != 1 || puts[1] != 1 {
		t.Errorf("completing state must not re-upload chunks: %v", puts)
	}
	if deletes != 0 {
		t.Errorf("deletes = %d", deletes)
	}
}

func TestUploadChunkedCompletingKeepsSession(t *testing.T) {
	oldWait, oldWindow := chunkCompletingWait, chunkCompletingWindow
	chunkCompletingWait = 5 * time.Millisecond
	chunkCompletingWindow = 25 * time.Millisecond
	t.Cleanup(func() {
		chunkCompletingWait = oldWait
		chunkCompletingWindow = oldWindow
	})

	data := []byte("abcdefgh") // 8 bytes, chunk size 4 => 2 chunks
	f := newFakeUploadServer(t, 4)
	f.expected = data
	f.complete409 = 1 << 20
	f.complete409Completing = true // 服务端一直报告 state=completing

	_, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data)))
	if err == nil {
		t.Fatal("want error after bounded wait")
	}
	if !strings.Contains(err.Error(), "仍在合并") {
		t.Fatalf("err = %v, want completing-window error", err)
	}
	_, _, _, deletes, puts, _ := f.snapshot()
	if deletes != 0 {
		t.Errorf("deletes = %d, want 0 (never delete while state=completing)", deletes)
	}
	// 等待合并期间不能重传分片。
	if puts[0] != 1 || puts[1] != 1 {
		t.Errorf("putCounts = %v, want each chunk once", puts)
	}
}

func TestUploadChunkedPutIncompleteRetries(t *testing.T) {
	data := []byte("abcdefgh") // 8 bytes, chunk size 4 => 2 chunks
	f := newFakeUploadServer(t, 4)
	f.expected = data
	f.putIncomplete = map[int]int{1: 1} // 补片撞上服务端合并，409 一次后成功

	if _, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	_, _, _, deletes, puts, _ := f.snapshot()
	if puts[1] != 2 {
		t.Errorf("putCounts[1] = %d, want 2 (409 upload_incomplete must retry)", puts[1])
	}
	if deletes != 0 {
		t.Errorf("deletes = %d, want 0", deletes)
	}
}

func TestUploadChunkedRepairPutFailureDeletesSession(t *testing.T) {
	data := []byte("abcdefghi") // 9 bytes, chunk size 3 => 3 chunks
	f := newFakeUploadServer(t, 3)
	f.expected = data
	f.complete409 = 1
	f.complete409Missing = []int{2}
	f.putBadRequestRepair = map[int]int{2: 1} // 首轮成功，补传时 400

	_, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data)))
	if err == nil {
		t.Fatal("want error")
	}
	_, _, _, deletes, puts, _ := f.snapshot()
	if puts[2] != 2 {
		t.Errorf("putCounts[2] = %d, want 2 (first upload + failed repair)", puts[2])
	}
	if deletes != 1 {
		t.Errorf("deletes = %d, want 1 (non-retryable repair failure must clean up)", deletes)
	}
}

func TestUploadChunkedCreateRouteMissingFallback(t *testing.T) {
	data := []byte("hello fallback")
	for _, status := range []int{404, 405, 501} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			f := newFakeUploadServer(t, 4)
			f.createStatus = status

			res, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			if res.Links.URL == "" {
				t.Fatal("missing links.url")
			}
			create, _, single, deletes, _, _ := f.snapshot()
			if create != 1 {
				t.Errorf("createCalls = %d, want 1", create)
			}
			if single != 1 {
				t.Errorf("singleCalls = %d, want 1 (fallback)", single)
			}
			if deletes != 0 {
				t.Errorf("deletes = %d, want 0 (no session created)", deletes)
			}
			f.mu.Lock()
			got := f.singleBody
			f.mu.Unlock()
			if !bytes.Equal(got, data) {
				t.Errorf("fallback body = %q, want %q", got, data)
			}
		})
	}
}

func TestUploadChunkedFailureDeletesSession(t *testing.T) {
	data := []byte("abcdefghij")
	f := newFakeUploadServer(t, 3)
	f.putStatus = http.StatusBadRequest // non-retryable

	_, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data)))
	if err == nil {
		t.Fatal("want error")
	}
	_, _, _, deletes, _, _ := f.snapshot()
	if deletes != 1 {
		t.Errorf("deletes = %d, want 1 after failure", deletes)
	}
}

func TestUploadChunkedCancelDeletesSession(t *testing.T) {
	data := []byte("abcdefghij")
	f := newFakeUploadServer(t, 3)
	f.blockPut = true

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := UploadChunked(ctx, f.opts(), bytes.NewReader(data), int64(len(data)))
		done <- err
	}()

	select {
	case <-f.putStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("chunk PUT did not start")
	}
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want cancellation error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("UploadChunked did not return after cancel")
	}

	_, _, _, deletes, _, _ := f.snapshot()
	if deletes != 1 {
		t.Errorf("deletes = %d, want 1 after cancel", deletes)
	}
}

func TestUploadFileModes(t *testing.T) {
	data := []byte("0123456789") // 10 bytes
	dir := t.TempDir()
	path := filepath.Join(dir, "pic.png")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name        string
		mode        string
		threshold   int64
		wantChunked bool
	}{
		{"auto small threshold", "auto", 4, true},
		{"auto at threshold", "auto", 10, true},
		{"auto just above threshold", "auto", 11, false},
		{"auto large threshold", "auto", 100, false},
		{"auto default threshold", "auto", 0, false},
		{"always", "always", 100, true},
		{"never", "never", 1, false},
		{"empty is auto", "", 100, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeUploadServer(t, 4)
			f.expected = data

			opts := f.opts()
			opts.Chunked = tc.mode
			opts.ChunkThreshold = tc.threshold
			opts.Filename = "" // UploadFile should derive it from the path

			res, err := UploadFile(context.Background(), opts, path)
			if err != nil {
				t.Fatal(err)
			}
			if res.Links.URL == "" {
				t.Fatal("missing links.url")
			}

			create, _, single, deletes, puts, _ := f.snapshot()
			if tc.wantChunked {
				if create != 1 {
					t.Errorf("createCalls = %d, want 1", create)
				}
				if single != 0 {
					t.Errorf("singleCalls = %d, want 0", single)
				}
				if len(puts) != 3 {
					t.Errorf("put indices = %v, want 3 chunks", puts)
				}
				f.mu.Lock()
				name := f.lastCreate.Name
				f.mu.Unlock()
				if name != "pic.png" {
					t.Errorf("create name = %q, want pic.png (from path)", name)
				}
			} else {
				if create != 0 {
					t.Errorf("createCalls = %d, want 0", create)
				}
				if single != 1 {
					t.Errorf("singleCalls = %d, want 1", single)
				}
			}
			if deletes != 0 {
				t.Errorf("deletes = %d", deletes)
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	future := time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)
	past := time.Now().Add(-2 * time.Second).UTC().Format(http.TimeFormat)

	cases := []struct {
		name   string
		value  string
		want   time.Duration
		wantOK bool
		check  func(time.Duration) bool
	}{
		{name: "zero", value: "0", want: 0, wantOK: true},
		{name: "seconds", value: "2", want: 2 * time.Second, wantOK: true},
		{name: "http date", value: future, want: 0, wantOK: true, check: func(d time.Duration) bool {
			return d > 0 && d <= 3*time.Second
		}},
		{name: "past http date", value: past, want: 0, wantOK: true},
		{name: "negative", value: "-5", want: 0, wantOK: true},
		// 不能被截断到 30 秒：小时/日级窗口会给出更大的值。
		{name: "large", value: "86400", want: 24 * time.Hour, wantOK: true},
		{name: "empty", value: "", want: 0, wantOK: false},
		{name: "invalid", value: "soon", want: 0, wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseRetryAfter(tc.value)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if tc.check != nil {
				if !tc.check(got) {
					t.Fatalf("duration = %v, want custom range", got)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("duration = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRetryDelayUsesRetryAfter(t *testing.T) {
	withRA := &apiHTTPError{StatusCode: http.StatusTooManyRequests, RetryAfter: 2 * time.Second, HasRetryAfter: true}
	if got := retryDelay(withRA, 1); got != 2*time.Second {
		t.Errorf("retryDelay = %v, want 2s (server Retry-After)", got)
	}
	noRA := &apiHTTPError{StatusCode: http.StatusTooManyRequests}
	if got := retryDelay(noRA, 1); got != time.Second {
		t.Errorf("retryDelay = %v, want 1s fallback", got)
	}
	if got := retryDelay(&apiHTTPError{StatusCode: http.StatusBadGateway}, 2); got != 2*chunkRetryBaseDelay {
		t.Errorf("retryDelay = %v, want %v", got, 2*chunkRetryBaseDelay)
	}
}

// eofExactReaderAt 模拟「读满 n 字节并同时返回 io.EOF」的 ReaderAt。
// os.File / bytes.Reader 在恰好读满时通常返回 nil，但接口允许 EOF 同时出现。
type eofExactReaderAt struct{ data []byte }

func (r eofExactReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n := copy(p, r.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, io.EOF
}

func TestReadChunkAcceptsFullReadWithEOF(t *testing.T) {
	u := &chunkUploader{
		chunkSize:   8,
		chunksTotal: 1,
		size:        8,
		r:           eofExactReaderAt{data: []byte("abcdefgh")},
	}
	buf, err := u.readChunk(0)
	if err != nil {
		t.Fatalf("readChunk: %v (完整 n 字节 + io.EOF 不算失败)", err)
	}
	if string(buf) != "abcdefgh" {
		t.Errorf("chunk = %q", buf)
	}
}

func TestRetryDelayCapsRetryAfter(t *testing.T) {
	long := &apiHTTPError{StatusCode: http.StatusTooManyRequests, RetryAfter: 24 * time.Hour, HasRetryAfter: true}
	if got := retryDelay(long, 1); got != chunkMaxRetryWait {
		t.Errorf("retryDelay = %v, want %v (Retry-After 上限)", got, chunkMaxRetryWait)
	}
	short := &apiHTTPError{StatusCode: http.StatusTooManyRequests, RetryAfter: 5 * time.Second, HasRetryAfter: true}
	if got := retryDelay(short, 1); got != 5*time.Second {
		t.Errorf("retryDelay = %v, want 5s (未超过上限)", got)
	}
}

func TestUploadChunkedRetryExhaustionKeepsSession(t *testing.T) {
	data := []byte("abcdefghij") // 10 bytes, chunk size 3 => 4 chunks
	f := newFakeUploadServer(t, 3)
	f.putFailures[0] = chunkRetryAttempts // 分片 0 的重试全部用尽（每次 500）

	_, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data)))
	if err == nil {
		t.Fatal("want error after retries exhausted")
	}
	_, _, _, deletes, puts, _ := f.snapshot()
	if puts[0] != chunkRetryAttempts {
		t.Errorf("putCounts[0] = %d, want %d", puts[0], chunkRetryAttempts)
	}
	if deletes != 0 {
		t.Errorf("deletes = %d, want 0 (可重试失败不能让已传分片随 DELETE 丢掉)", deletes)
	}
}

func TestUploadChunkedAlwaysDoesNotFallBack(t *testing.T) {
	data := []byte("hello")
	f := newFakeUploadServer(t, 4)
	f.createStatus = http.StatusNotFound

	opts := f.opts()
	opts.Chunked = chunkedAlways
	_, err := UploadChunked(context.Background(), opts, bytes.NewReader(data), int64(len(data)))
	if err == nil || !strings.Contains(err.Error(), "不支持分片上传") {
		t.Fatalf("err = %v, want explicit -chunked always failure", err)
	}
	_, _, single, deletes, _, _ := f.snapshot()
	if single != 0 {
		t.Errorf("singleCalls = %d, want 0 (always 不能静默回退单请求)", single)
	}
	if deletes != 0 {
		t.Errorf("deletes = %d, want 0", deletes)
	}
}

func TestUploadChunkedCompleteGoneAfterCompleting(t *testing.T) {
	oldWait, oldWindow := chunkCompletingWait, chunkCompletingWindow
	chunkCompletingWait = 5 * time.Millisecond
	chunkCompletingWindow = time.Minute
	t.Cleanup(func() {
		chunkCompletingWait = oldWait
		chunkCompletingWindow = oldWindow
	})

	data := []byte("abcdefgh") // 8 bytes, chunk size 4 => 2 chunks
	f := newFakeUploadServer(t, 4)
	f.expected = data
	f.complete409 = 1
	f.complete409Completing = true
	f.completeGoneWhenCompleting = true

	_, err := UploadChunked(context.Background(), f.opts(), bytes.NewReader(data), int64(len(data)))
	if err == nil || !strings.Contains(err.Error(), "合并可能已经成功") {
		t.Fatalf("err = %v, want hint that merge may have succeeded", err)
	}
	_, _, _, deletes, _, _ := f.snapshot()
	if deletes != 0 {
		t.Errorf("deletes = %d, want 0", deletes)
	}
}

// failingFirstRun 跑一次注定失败（complete 可重试失败耗尽）的上传，
// 留下服务端会话与本地续传状态，用于续传分支的测试。
func failingFirstRun(t *testing.T, f *fakeUploadServer, path string, opts Opts) {
	t.Helper()
	f.mu.Lock()
	f.completeFailures = chunkRetryAttempts
	f.mu.Unlock()
	if _, err := UploadFile(context.Background(), opts, path); err == nil {
		t.Fatal("want first run to fail")
	}
	f.mu.Lock()
	f.completeFailures = 0
	f.mu.Unlock()
}

func TestUploadFileResumeUnusableStartsNewSession(t *testing.T) {
	data := []byte("abcdefghij")
	dir := t.TempDir()
	path := filepath.Join(dir, "pic.png")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	f := newFakeUploadServer(t, 3)
	f.expected = data
	stateDir := t.TempDir()
	opts := f.opts()
	opts.Chunked = chunkedAlways
	opts.Resume = true
	opts.ResumeStateDir = stateDir
	opts.Filename = ""

	failingFirstRun(t, f, path, opts)

	// 会话属于别的用户/IP 或已不可用：GET 返回 403。
	f.mu.Lock()
	f.getFailures = 1
	f.getFailStatus = http.StatusForbidden
	f.mu.Unlock()

	res, err := UploadFile(context.Background(), opts, path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Links.URL == "" {
		t.Fatal("missing links.url")
	}
	create, _, _, deletes, _, _ := f.snapshot()
	if create != 2 {
		t.Errorf("createCalls = %d, want 2 (403 后必须新开会话)", create)
	}
	if deletes != 0 {
		t.Errorf("deletes = %d, want 0 (403 已说明会话不可用，不必再 DELETE)", deletes)
	}
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("resume state files = %d, want 0", len(entries))
	}
}

func TestUploadFileResumeParamChangeStartsNewSession(t *testing.T) {
	data := []byte("abcdefghij")
	dir := t.TempDir()
	path := filepath.Join(dir, "pic.png")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	f := newFakeUploadServer(t, 3)
	f.expected = data
	stateDir := t.TempDir()
	opts := f.opts()
	opts.Chunked = chunkedAlways
	opts.Resume = true
	opts.ResumeStateDir = stateDir
	opts.Filename = ""

	failingFirstRun(t, f, path, opts)

	// 可见性变了：旧会话是按 public 建的，沿用会静默丢掉新参数。
	opts.Visibility = "private"

	res, err := UploadFile(context.Background(), opts, path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Links.URL == "" {
		t.Fatal("missing links.url")
	}
	create, _, _, deletes, _, _ := f.snapshot()
	if create != 2 {
		t.Errorf("createCalls = %d, want 2 (参数变化必须新开会话)", create)
	}
	if deletes != 1 {
		t.Errorf("deletes = %d, want 1 (旧会话应尽力删除)", deletes)
	}
	f.mu.Lock()
	gotVis := f.lastCreate.Visibility
	f.mu.Unlock()
	if gotVis != "private" {
		t.Errorf("create visibility = %q, want private", gotVis)
	}
}

func TestUploadFileInvalidMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pic.png")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := UploadFile(context.Background(), Opts{
		BaseURL: "http://127.0.0.1:1",
		Chunked: "sometimes",
	}, path)
	if err == nil || !strings.Contains(err.Error(), "分片模式") {
		t.Fatalf("err = %v", err)
	}
}
