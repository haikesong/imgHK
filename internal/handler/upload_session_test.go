package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"github.com/yixian-huang/imgli/internal/model"
)

// 分片上传测试：会话创建、乱序/重传分片、断点续传查询、合并、取消、TTL 清理、越权。
// 测试里把 store.chunkSize 调小，用一张几百字节的 PNG 就能覆盖多分片路径。

type testEnvelope struct {
	Status  bool            `json:"status"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type sessionCreateDTO struct {
	SessionID   string `json:"session_id"`
	ChunkSize   int64  `json:"chunk_size"`
	ChunksTotal int    `json:"chunks_total"`
	Size        int64  `json:"size"`
}

type sessionStateDTO struct {
	SessionID     string `json:"session_id"`
	ChunkSize     int64  `json:"chunk_size"`
	ChunksTotal   int    `json:"chunks_total"`
	ReceivedBytes int64  `json:"received_bytes"`
	Received      []int  `json:"received"`
	Missing       []int  `json:"missing"`
	State         string `json:"state"`
}

type chunkResultDTO struct {
	Index         int   `json:"index"`
	ReceivedBytes int64 `json:"received_bytes"`
	Missing       int   `json:"missing"`
}

type errorDTO struct {
	Code string `json:"code"`
}

// anonSessionMux 是未登录（游客）视角的路由：同一批 handler，不带 principal。
func anonSessionMux(h *UploadHandlers) *chi.Mux {
	mux := chi.NewRouter()
	mux.Post("/api/v1/upload/session", h.CreateUploadSession)
	mux.Post("/api/v1/upload/session/{id}/complete", h.CompleteUploadSession)
	mux.Put("/api/v1/upload/session/{id}/{index}", h.PutUploadChunk)
	mux.Get("/api/v1/upload/session/{id}", h.GetUploadSession)
	mux.Delete("/api/v1/upload/session/{id}", h.CancelUploadSession)
	return mux
}

func doReq(t *testing.T, hnd http.Handler, method, path string, body []byte, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	r := httptest.NewRequest(method, path, rdr)
	if remoteAddr != "" {
		r.RemoteAddr = remoteAddr
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	hnd.ServeHTTP(rec, r)
	return rec
}

func decodeSessionEnv(t *testing.T, rec *httptest.ResponseRecorder) testEnvelope {
	t.Helper()
	var env testEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return env
}

func createSession(t *testing.T, mux http.Handler, size int64) sessionCreateDTO {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"name": "big.png", "size": size})
	rec := doReq(t, mux, http.MethodPost, "/api/v1/upload/session", body, "")
	env := decodeSessionEnv(t, rec)
	if rec.Code != http.StatusOK || !env.Status {
		t.Fatalf("create session = %d %s", rec.Code, rec.Body.String())
	}
	var dto sessionCreateDTO
	if err := json.Unmarshal(env.Data, &dto); err != nil {
		t.Fatal(err)
	}
	if dto.SessionID == "" || dto.ChunkSize <= 0 || dto.ChunksTotal <= 0 || dto.Size != size {
		t.Fatalf("bad session dto: %+v", dto)
	}
	return dto
}

func putChunk(t *testing.T, mux http.Handler, id string, idx int, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	return doReq(t, mux, http.MethodPut,
		fmt.Sprintf("/api/v1/upload/session/%s/%d", id, idx), data, "")
}

func getSession(t *testing.T, mux http.Handler, id string) (int, sessionStateDTO) {
	t.Helper()
	rec := doReq(t, mux, http.MethodGet, "/api/v1/upload/session/"+id, nil, "")
	env := decodeSessionEnv(t, rec)
	var dto sessionStateDTO
	if env.Status {
		if err := json.Unmarshal(env.Data, &dto); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, dto
}

func completeSession(t *testing.T, mux http.Handler, id string) (*httptest.ResponseRecorder, testEnvelope) {
	t.Helper()
	rec := doReq(t, mux, http.MethodPost, "/api/v1/upload/session/"+id+"/complete", nil, "")
	return rec, decodeSessionEnv(t, rec)
}

func errCode(t *testing.T, env testEnvelope) string {
	t.Helper()
	var e errorDTO
	if err := json.Unmarshal(env.Data, &e); err != nil {
		t.Fatalf("decode error data %s: %v", env.Data, err)
	}
	return e.Code
}

// chunkSlice 第 idx 片的字节（最后一片比 ChunkSize 短）。
func chunkSlice(data []byte, size int64, idx int) []byte {
	start := int64(idx) * size
	end := start + size
	if end > int64(len(data)) {
		end = int64(len(data))
	}
	return data[start:end]
}

// readStoredBytes 从存储策略里读回最终落盘的对象，用来验证拼装顺序没错。
func readStoredBytes(t *testing.T, h *UploadHandlers, db *gorm.DB, key string) []byte {
	t.Helper()
	var img model.Image
	if err := db.Where("key = ?", key).First(&img).Error; err != nil {
		t.Fatalf("image %s: %v", key, err)
	}
	var f model.File
	if err := db.First(&f, img.FileID).Error; err != nil {
		t.Fatal(err)
	}
	var p model.StoragePolicy
	if err := db.First(&p, f.StoragePolicyID).Error; err != nil {
		t.Fatal(err)
	}
	d, err := h.D.Res.Driver(&p)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := d.Open(context.Background(), f.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newTestSessionSize(t *testing.T) ([]byte, int64) {
	t.Helper()
	data := encodeTestPNG(t, 90, 60)
	if len(data) < 60 {
		t.Fatalf("测试 PNG 太小（%d 字节），无法切出多片", len(data))
	}
	return data, int64(len(data)) / 3
}

func TestUploadSessionOutOfOrderAndResume(t *testing.T) {
	db, h, _, mux := setupUploadHandler(t)
	data, cs := newTestSessionSize(t)
	h.D.Sessions.chunkSize = cs

	dto := createSession(t, mux, int64(len(data)))
	if dto.ChunkSize != cs {
		t.Errorf("chunk_size = %d, want %d", dto.ChunkSize, cs)
	}
	wantTotal := (len(data) + int(cs) - 1) / int(cs)
	if dto.ChunksTotal != wantTotal {
		t.Errorf("chunks_total = %d, want %d", dto.ChunksTotal, wantTotal)
	}

	// 倒序上传：先传最后一片，再传前面的，验证任意顺序到达。
	for idx := dto.ChunksTotal - 1; idx >= 0; idx-- {
		rec := putChunk(t, mux, dto.SessionID, idx, chunkSlice(data, cs, idx))
		if rec.Code != http.StatusOK {
			t.Fatalf("put chunk %d = %d %s", idx, rec.Code, rec.Body.String())
		}
		if idx > 0 {
			code, st := getSession(t, mux, dto.SessionID)
			if code != http.StatusOK {
				t.Fatalf("get session = %d", code)
			}
			if len(st.Missing) != idx {
				t.Errorf("after chunk %d: missing=%v, want %d 片", idx, st.Missing, idx)
			}
			wantBytes := int64(len(data)) - int64(idx)*cs
			if st.ReceivedBytes != wantBytes {
				t.Errorf("received_bytes = %d, want %d", st.ReceivedBytes, wantBytes)
			}
		}
	}

	rec, env := completeSession(t, mux, dto.SessionID)
	if rec.Code != http.StatusOK || !env.Status {
		t.Fatalf("complete = %d %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Key  string `json:"key"`
		Size int64  `json:"size"`
	}
	if err := json.Unmarshal(env.Data, &res); err != nil {
		t.Fatal(err)
	}
	if res.Size != int64(len(data)) {
		t.Errorf("size = %d, want %d", res.Size, len(data))
	}
	if got := readStoredBytes(t, h, db, res.Key); !bytes.Equal(got, data) {
		t.Errorf("落盘内容与源文件不一致（got %d bytes, want %d）", len(got), len(data))
	}
	// 合并成功后会话目录应被删除。
	if _, err := os.Stat(h.D.Sessions.dir(dto.SessionID)); !os.IsNotExist(err) {
		t.Errorf("会话目录未清理: %v", err)
	}
}

func TestUploadSessionDuplicateChunkIsIdempotent(t *testing.T) {
	_, h, _, mux := setupUploadHandler(t)
	data, cs := newTestSessionSize(t)
	h.D.Sessions.chunkSize = cs
	dto := createSession(t, mux, int64(len(data)))

	part := chunkSlice(data, cs, 0)
	for i := 0; i < 2; i++ {
		rec := putChunk(t, mux, dto.SessionID, 0, part)
		if rec.Code != http.StatusOK {
			t.Fatalf("put #%d = %d %s", i, rec.Code, rec.Body.String())
		}
		var out chunkResultDTO
		if err := json.Unmarshal(decodeSessionEnv(t, rec).Data, &out); err != nil {
			t.Fatal(err)
		}
		if out.ReceivedBytes != int64(len(part)) {
			t.Errorf("重传后 received_bytes = %d, want %d（不应重复累计）", out.ReceivedBytes, len(part))
		}
	}
	_, st := getSession(t, mux, dto.SessionID)
	if len(st.Received) != 1 || st.Received[0] != 0 {
		t.Errorf("received = %v, want [0]", st.Received)
	}
}

func TestUploadSessionRejectsWrongChunkSize(t *testing.T) {
	_, h, _, mux := setupUploadHandler(t)
	data, cs := newTestSessionSize(t)
	h.D.Sessions.chunkSize = cs
	dto := createSession(t, mux, int64(len(data)))

	rec := putChunk(t, mux, dto.SessionID, 0, chunkSlice(data, cs, 0)[:cs-1])
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("短分片 = %d %s, want 400", rec.Code, rec.Body.String())
	}
	_, st := getSession(t, mux, dto.SessionID)
	if len(st.Missing) != dto.ChunksTotal || len(st.Received) != 0 {
		t.Errorf("被拒分片不应计入: received=%v missing=%v", st.Received, st.Missing)
	}
	// 越界序号。
	if rec := putChunk(t, mux, dto.SessionID, dto.ChunksTotal, []byte("x")); rec.Code != http.StatusBadRequest {
		t.Errorf("越界分片 = %d, want 400", rec.Code)
	}
}

func TestUploadSessionCompleteIncompleteThenRetry(t *testing.T) {
	db, h, _, mux := setupUploadHandler(t)
	data, cs := newTestSessionSize(t)
	h.D.Sessions.chunkSize = cs
	dto := createSession(t, mux, int64(len(data)))

	if rec := putChunk(t, mux, dto.SessionID, 0, chunkSlice(data, cs, 0)); rec.Code != http.StatusOK {
		t.Fatalf("put = %d", rec.Code)
	}
	rec, env := completeSession(t, mux, dto.SessionID)
	if rec.Code != http.StatusConflict || errCode(t, env) != CodeUploadIncomplete {
		t.Fatalf("缺片 complete = %d %s", rec.Code, rec.Body.String())
	}
	// 失败后会话必须回到 open，客户端才能补传。
	code, st := getSession(t, mux, dto.SessionID)
	if code != http.StatusOK || st.State != "open" {
		t.Fatalf("complete 失败后 state=%q code=%d, want open/200", st.State, code)
	}
	for idx := 1; idx < dto.ChunksTotal; idx++ {
		if rec := putChunk(t, mux, dto.SessionID, idx, chunkSlice(data, cs, idx)); rec.Code != http.StatusOK {
			t.Fatalf("补传 %d = %d", idx, rec.Code)
		}
	}
	rec, env = completeSession(t, mux, dto.SessionID)
	if rec.Code != http.StatusOK || !env.Status {
		t.Fatalf("补传后 complete = %d %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(env.Data, &res)
	if got := readStoredBytes(t, h, db, res.Key); !bytes.Equal(got, data) {
		t.Error("补传后落盘内容不一致")
	}
}

func TestUploadSessionCancel(t *testing.T) {
	_, h, _, mux := setupUploadHandler(t)
	data, cs := newTestSessionSize(t)
	h.D.Sessions.chunkSize = cs
	dto := createSession(t, mux, int64(len(data)))
	if rec := putChunk(t, mux, dto.SessionID, 0, chunkSlice(data, cs, 0)); rec.Code != http.StatusOK {
		t.Fatalf("put = %d", rec.Code)
	}
	rec := doReq(t, mux, http.MethodDelete, "/api/v1/upload/session/"+dto.SessionID, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	if code, _ := getSession(t, mux, dto.SessionID); code != http.StatusNotFound {
		t.Errorf("取消后 GET = %d, want 404", code)
	}
	if _, err := os.Stat(h.D.Sessions.dir(dto.SessionID)); !os.IsNotExist(err) {
		t.Errorf("取消后目录仍在: %v", err)
	}
}

func TestUploadSessionOwnership(t *testing.T) {
	_, h, _, mux := setupUploadHandler(t)
	data, cs := newTestSessionSize(t)
	h.D.Sessions.chunkSize = cs
	dto := createSession(t, mux, int64(len(data)))

	// 未登录请求（游客视角）访问登录用户的会话 → 403，且不能删除/合并。
	anon := anonSessionMux(h)
	const ip = "198.51.100.9:1234"
	paths := []struct {
		method string
		path   string
		body   []byte
	}{
		{http.MethodGet, "/api/v1/upload/session/" + dto.SessionID, nil},
		{http.MethodPut, fmt.Sprintf("/api/v1/upload/session/%s/0", dto.SessionID), chunkSlice(data, cs, 0)},
		{http.MethodPost, "/api/v1/upload/session/" + dto.SessionID + "/complete", nil},
		{http.MethodDelete, "/api/v1/upload/session/" + dto.SessionID, nil},
	}
	for _, p := range paths {
		rec := doReq(t, anon, p.method, p.path, p.body, ip)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d %s, want 403", p.method, p.path, rec.Code, rec.Body.String())
		}
	}
	if _, err := os.Stat(h.D.Sessions.dir(dto.SessionID)); err != nil {
		t.Errorf("越权请求不应影响会话: %v", err)
	}

	// 不存在的 id 与非法 id 一律 404。
	if code, _ := getSession(t, mux, strings.Repeat("0", 32)); code != http.StatusNotFound {
		t.Errorf("未知会话 = %d, want 404", code)
	}
	if code, _ := getSession(t, mux, strings.Repeat("z", 32)); code != http.StatusNotFound {
		t.Errorf("非法 id = %d, want 404", code)
	}
}

func TestUploadSessionGuestOwnedByIP(t *testing.T) {
	_, h, _, _ := setupUploadHandler(t)
	st := h.D.Sessions
	meta := &uploadSessionMeta{
		ID: "0123456789abcdef0123456789abcdef", UserID: 0, GuestIP: "198.51.100.7",
		Name: "guest.png", Size: 4, ChunkSize: 4, ChunksTotal: 1,
		Visibility: "public", CreatedAt: time.Now(),
	}
	if err := st.create(meta); err != nil {
		t.Fatal(err)
	}
	anon := anonSessionMux(h)
	if rec := doReq(t, anon, http.MethodGet, "/api/v1/upload/session/"+meta.ID, nil, "198.51.100.8:1"); rec.Code != http.StatusForbidden {
		t.Errorf("同游客不同 IP = %d, want 403", rec.Code)
	}
	if rec := doReq(t, anon, http.MethodGet, "/api/v1/upload/session/"+meta.ID, nil, "198.51.100.7:1"); rec.Code != http.StatusOK {
		t.Errorf("同 IP 游客 = %d %s, want 200", rec.Code, rec.Body.String())
	}
}

func TestUploadSessionMaxConcurrentSessions(t *testing.T) {
	_, h, _, mux := setupUploadHandler(t)
	data, cs := newTestSessionSize(t)
	h.D.Sessions.chunkSize = cs
	for i := 0; i < uploadMaxSessions; i++ {
		createSession(t, mux, int64(len(data)))
	}
	body, _ := json.Marshal(map[string]any{"name": "one-more.png", "size": len(data)})
	rec := doReq(t, mux, http.MethodPost, "/api/v1/upload/session", body, "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("超额会话 = %d %s, want 429", rec.Code, rec.Body.String())
	}
	if got := errCode(t, decodeSessionEnv(t, rec)); got != CodeRateLimited {
		t.Errorf("code = %q, want %q", got, CodeRateLimited)
	}
}

func TestUploadSessionCreateValidation(t *testing.T) {
	_, _, _, mux := setupUploadHandler(t)
	cases := []struct {
		name string
		body map[string]any
		want int
		code string
	}{
		{"缺文件名", map[string]any{"size": 100}, http.StatusBadRequest, CodeInvalidRequest},
		{"零大小", map[string]any{"name": "a.png", "size": 0}, http.StatusBadRequest, CodeInvalidRequest},
		{"超过硬上限", map[string]any{"name": "a.png", "size": (10 << 20) + 1}, http.StatusRequestEntityTooLarge, CodeFileTooLarge},
		{"expires_in 越界", map[string]any{"name": "a.png", "size": 100, "expires_in": MaxExpiresInSec + 1}, http.StatusBadRequest, CodeInvalidRequest},
	}
	for _, c := range cases {
		body, _ := json.Marshal(c.body)
		rec := doReq(t, mux, http.MethodPost, "/api/v1/upload/session", body, "")
		if rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body.String())
			continue
		}
		if got := errCode(t, decodeSessionEnv(t, rec)); got != c.code {
			t.Errorf("%s: code = %q, want %q", c.name, got, c.code)
		}
	}
}

func TestUploadSessionSweepExpired(t *testing.T) {
	_, h, _, mux := setupUploadHandler(t)
	data, cs := newTestSessionSize(t)
	h.D.Sessions.chunkSize = cs
	old := createSession(t, mux, int64(len(data)))
	fresh := createSession(t, mux, int64(len(data)))

	st := h.D.Sessions
	meta, err := st.load(old.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	meta.CreatedAt = time.Now().Add(-3 * time.Hour)
	if err := st.writeMeta(meta); err != nil {
		t.Fatal(err)
	}
	// 会话龄超过 TTL 后 countFor 也不再计入。
	if n := st.countFor(meta.UserID, "", time.Now()); n != 1 {
		t.Errorf("countFor = %d, want 1（只剩未过期会话）", n)
	}
	st.sweep(time.Now())
	if _, err := os.Stat(st.dir(old.SessionID)); !os.IsNotExist(err) {
		t.Errorf("过期会话未清理: %v", err)
	}
	if _, err := os.Stat(st.dir(fresh.SessionID)); err != nil {
		t.Errorf("未过期会话被误删: %v", err)
	}
}

func TestUploadSessionConcurrentChunks(t *testing.T) {
	db, h, _, mux := setupUploadHandler(t)
	data, _ := newTestSessionSize(t)
	cs := int64(len(data)) / 24
	if cs < 2 {
		cs = 2
	}
	h.D.Sessions.chunkSize = cs
	dto := createSession(t, mux, int64(len(data)))

	order := make([]int, dto.ChunksTotal)
	for i := range order {
		order[i] = i
	}
	rand.New(rand.NewSource(1)).Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })

	// 3 路并发、乱序上传；每片单独 goroutine 抢序号。
	jobs := make(chan int)
	var wg sync.WaitGroup
	errs := make(chan string, len(order))
	for w := 0; w < 3; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				rec := putChunk(t, mux, dto.SessionID, idx, chunkSlice(data, cs, idx))
				if rec.Code != http.StatusOK {
					errs <- fmt.Sprintf("chunk %d: %d %s", idx, rec.Code, rec.Body.String())
				}
			}
		}()
	}
	for _, idx := range order {
		jobs <- idx
	}
	close(jobs)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}

	_, st := getSession(t, mux, dto.SessionID)
	if len(st.Missing) != 0 {
		t.Fatalf("并发上传后仍缺 %v", st.Missing)
	}
	rec, env := completeSession(t, mux, dto.SessionID)
	if rec.Code != http.StatusOK || !env.Status {
		t.Fatalf("complete = %d %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(env.Data, &res)
	if got := readStoredBytes(t, h, db, res.Key); !bytes.Equal(got, data) {
		t.Error("并发乱序拼装后内容不一致")
	}
}
