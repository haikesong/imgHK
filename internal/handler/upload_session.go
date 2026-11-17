package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/yixian-huang/imgli/internal/model"
	"github.com/yixian-huang/imgli/internal/service/imagesvc"
	"github.com/yixian-huang/imgli/internal/service/upload"
)

// 分片上传（会话 + 断点续传）。
//
// 设计要点：
//   - 每片独立落盘为 parts/<index>，先写临时文件再原子 rename，任意顺序到达都安全；
//     同一片重传直接覆盖（幂等）。
//   - complete 时按 index 顺序拼装成一个临时文件，直接复用 Svc.Save：
//     去重、元数据剥离、缩略图、配额、用户组限制与原单请求路径完全一致。
//   - 会话元数据落在 <DataDir>/.upload-sessions/<id>/meta.json，容器重启不丢。
//   - 2 小时 TTL，启动时与每 10 分钟扫描清理；取消会话立即删除。
const (
	uploadChunkSize     = 8 << 20  // 服务端建议分片大小（客户端按返回值切片）
	uploadChunkMax      = 16 << 20 // 单片请求体硬上限
	uploadSessionTTL    = 2 * time.Hour
	uploadSweepInterval = 10 * time.Minute
	uploadMaxSessions   = 4 // 同一用户/游客 IP 同时存在的会话数上限
)

var uploadSessionIDRe = regexp.MustCompile(`^[a-f0-9]{32}$`)

// uploadSessionMeta 是会话的持久化元数据。
// ExpiresIn 存秒数而不是绝对时间：完成时才计算 expires_at，与单请求路径一致。
type uploadSessionMeta struct {
	ID                 string    `json:"id"`
	UserID             uint64    `json:"user_id"`
	GuestIP            string    `json:"guest_ip,omitempty"`
	Name               string    `json:"name"`
	Size               int64     `json:"size"`
	ChunkSize          int64     `json:"chunk_size"`
	ChunksTotal        int       `json:"chunks_total"`
	Visibility         string    `json:"visibility"`
	AlbumID            *uint64   `json:"album_id,omitempty"`
	PolicyID           uint64    `json:"policy_id,omitempty"`
	ExpiresIn          int       `json:"expires_in,omitempty"`
	MaxViews           int       `json:"max_views,omitempty"`
	AccessPasswordHash string    `json:"access_password_hash,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
}

// sessionLock 串行化同一会话的状态迁移：
// 分片写入只是短暂占用（inflight++），complete 会等所有在途分片写完再拼装。
type sessionLock struct {
	mu         sync.Mutex
	cond       *sync.Cond
	inflight   int
	completing bool
}

// uploadSessionStore 管理 <root>/<id>/ 下的会话目录。
type UploadSessionStore struct {
	root      string
	chunkSize int64 // 新会话的建议分片大小（测试可调小）
	mu        sync.Mutex
	locks     map[string]*sessionLock
}

func NewUploadSessionStore(root string) (*UploadSessionStore, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	st := &UploadSessionStore{root: root, chunkSize: uploadChunkSize, locks: map[string]*sessionLock{}}
	st.sweep(time.Now())
	return st, nil
}

func (st *UploadSessionStore) dir(id string) string {
	return filepath.Join(st.root, id)
}

func (st *UploadSessionStore) partsDir(id string) string {
	return filepath.Join(st.dir(id), "parts")
}

func (st *UploadSessionStore) partPath(id string, idx int) string {
	return filepath.Join(st.partsDir(id), strconv.Itoa(idx))
}

func (st *UploadSessionStore) lock(id string) *sessionLock {
	st.mu.Lock()
	defer st.mu.Unlock()
	l, ok := st.locks[id]
	if !ok {
		l = &sessionLock{}
		l.cond = sync.NewCond(&l.mu)
		st.locks[id] = l
	}
	return l
}

func (st *UploadSessionStore) dropLock(id string) {
	st.mu.Lock()
	delete(st.locks, id)
	st.mu.Unlock()
}

func (st *UploadSessionStore) create(meta *uploadSessionMeta) error {
	if err := os.MkdirAll(st.partsDir(meta.ID), 0o700); err != nil {
		return err
	}
	return st.writeMeta(meta)
}

func (st *UploadSessionStore) writeMeta(meta *uploadSessionMeta) error {
	b, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(st.dir(meta.ID), "meta-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, filepath.Join(st.dir(meta.ID), "meta.json"))
}

func (st *UploadSessionStore) load(id string) (*uploadSessionMeta, error) {
	b, err := os.ReadFile(filepath.Join(st.dir(id), "meta.json"))
	if err != nil {
		return nil, err
	}
	var meta uploadSessionMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		return nil, err
	}
	if meta.ID != id || meta.Size <= 0 || meta.ChunksTotal <= 0 || meta.ChunkSize <= 0 {
		return nil, errors.New("uploadsession: 元数据损坏")
	}
	return &meta, nil
}

func (st *UploadSessionStore) remove(id string) {
	if err := os.RemoveAll(st.dir(id)); err != nil {
		slog.Warn("清理上传会话失败", "id", id, "err", err)
	}
	st.dropLock(id)
}

// expectedChunkSize 第 idx 片应有的字节数：除最后一片外都是 ChunkSize。
func expectedChunkSize(meta *uploadSessionMeta, idx int) int64 {
	if idx < meta.ChunksTotal-1 {
		return meta.ChunkSize
	}
	return meta.Size - int64(meta.ChunksTotal-1)*meta.ChunkSize
}

// received 统计已完整收到的分片；size 不符的分片视为未收到。
func (st *UploadSessionStore) received(meta *uploadSessionMeta) (int64, []int, []int) {
	have := make([]int, 0, meta.ChunksTotal)
	missing := make([]int, 0)
	var bytes int64
	for i := 0; i < meta.ChunksTotal; i++ {
		fi, err := os.Stat(st.partPath(meta.ID, i))
		if err == nil && fi.Size() == expectedChunkSize(meta, i) {
			have = append(have, i)
			bytes += fi.Size()
			continue
		}
		missing = append(missing, i)
	}
	return bytes, have, missing
}

// countFor 统计某属主在 TTL 内仍存活的会话数（防单用户堆积临时文件）。
func (st *UploadSessionStore) countFor(userID uint64, guestIP string, now time.Time) int {
	entries, err := os.ReadDir(st.root)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() || !uploadSessionIDRe.MatchString(e.Name()) {
			continue
		}
		meta, err := st.load(e.Name())
		if err != nil || now.Sub(meta.CreatedAt) > uploadSessionTTL {
			continue
		}
		if userID != 0 && meta.UserID == userID {
			n++
		} else if userID == 0 && meta.UserID == 0 && meta.GuestIP == guestIP {
			n++
		}
	}
	return n
}

// sweep 清理超过 TTL 的会话；在途合并的会话也一并清理（其分片已无人等待）。
func (st *UploadSessionStore) sweep(now time.Time) {
	entries, err := os.ReadDir(st.root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !uploadSessionIDRe.MatchString(e.Name()) {
			continue
		}
		id := e.Name()
		meta, err := st.load(id)
		if err != nil {
			// 元数据损坏或写了一半：按过期处理
			if fi, serr := e.Info(); serr == nil && now.Sub(fi.ModTime()) > uploadSessionTTL {
				st.remove(id)
			}
			continue
		}
		if now.Sub(meta.CreatedAt) > uploadSessionTTL {
			st.remove(id)
		}
	}
}

// sweepLoop 供 server.Run 启动；ctx 取消即退出。
func (st *UploadSessionStore) StartSweeper(ctx context.Context) {
	t := time.NewTicker(uploadSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			st.sweep(now)
		}
	}
}

// ---- HTTP handlers ----

type createSessionReq struct {
	Name           string  `json:"name"`
	Size           int64   `json:"size"`
	Visibility     string  `json:"visibility"`
	AlbumID        *uint64 `json:"album_id"`
	PolicyID       uint64  `json:"policy_id"`
	ExpiresIn      int     `json:"expires_in"`
	MaxViews       int     `json:"max_views"`
	AccessPassword string  `json:"access_password"`
}

func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// sessionOwner 返回会话属主：登录用户 ID，游客则为 0 + IP。
func sessionOwner(r *http.Request) (uint64, string) {
	if p := PrincipalFrom(r); p != nil && p.User != nil {
		return p.User.ID, ""
	}
	return 0, ClientIP(r)
}

func (h *UploadHandlers) owns(meta *uploadSessionMeta, r *http.Request) bool {
	uid, ip := sessionOwner(r)
	if uid != 0 {
		return meta.UserID == uid
	}
	return meta.UserID == 0 && meta.GuestIP != "" && meta.GuestIP == ip
}

// loadSession 校验 id 与属主；失败时已写好响应，返回 nil。
func (h *UploadHandlers) loadSession(w http.ResponseWriter, r *http.Request) (*UploadSessionStore, *uploadSessionMeta) {
	st := h.D.Sessions
	if st == nil {
		Fail(w, http.StatusNotImplemented, CodeInternal, "分片上传未启用")
		return nil, nil
	}
	id := chi.URLParam(r, "id")
	if !uploadSessionIDRe.MatchString(id) {
		Fail(w, http.StatusNotFound, CodeNotFound, "上传会话不存在")
		return nil, nil
	}
	meta, err := st.load(id)
	if err != nil {
		Fail(w, http.StatusNotFound, CodeNotFound, "上传会话不存在或已过期")
		return nil, nil
	}
	if !h.owns(meta, r) {
		Fail(w, http.StatusForbidden, CodeForbidden, "无权访问该上传会话")
		return nil, nil
	}
	return st, meta
}

// CreateUploadSession POST /api/v1/upload/session
func (h *UploadHandlers) CreateUploadSession(w http.ResponseWriter, r *http.Request) {
	if h.D.Sessions == nil {
		Fail(w, http.StatusNotImplemented, CodeInternal, "分片上传未启用")
		return
	}
	var req createSessionReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if err := dec.Decode(&req); err != nil {
		Fail(w, http.StatusBadRequest, CodeInvalidRequest, "请求体不合法")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 255 {
		Fail(w, http.StatusBadRequest, CodeInvalidRequest, "文件名不合法")
		return
	}
	if req.Size <= 0 {
		Fail(w, http.StatusBadRequest, CodeInvalidRequest, "文件大小不合法")
		return
	}
	if req.Size > h.D.MaxBytes {
		failUpload(w, upload.ErrFileTooLarge)
		return
	}
	if req.ExpiresIn < 0 || req.ExpiresIn > MaxExpiresInSec {
		Fail(w, http.StatusBadRequest, CodeInvalidRequest, "expires_in 不合法")
		return
	}
	if req.MaxViews < 0 || req.MaxViews > imagesvc.MaxViewsMax {
		Fail(w, http.StatusBadRequest, CodeInvalidRequest, "max_views 不合法")
		return
	}
	var accessHash string
	if ap := strings.TrimSpace(req.AccessPassword); ap != "" {
		if len(ap) > imgPassMaxLen {
			Fail(w, http.StatusBadRequest, CodeInvalidRequest, "access_password 过长")
			return
		}
		hsh, herr := HashAccessPassword(ap)
		if herr != nil {
			Fail(w, http.StatusBadRequest, CodeInvalidRequest, "access_password 不合法")
			return
		}
		accessHash = hsh
	}

	var u *model.User
	if p := PrincipalFrom(r); p != nil {
		u = p.User
	}
	// 在接收任何字节之前预检：游客开关、组上限、配额、月流量。
	if err := h.D.Svc.CheckSizeAllowed(u, req.Size); err != nil {
		failUpload(w, err)
		return
	}

	uid, ip := sessionOwner(r)
	if h.D.Sessions.countFor(uid, ip, time.Now()) >= uploadMaxSessions {
		Fail(w, http.StatusTooManyRequests, CodeRateLimited, "未完成的上传过多，请先取消或等待过期")
		return
	}

	id, err := newSessionID()
	if err != nil {
		Fail(w, http.StatusInternalServerError, CodeInternal, "服务器内部错误")
		return
	}
	chunkSize := h.D.Sessions.chunkSize
	if chunkSize <= 0 {
		chunkSize = uploadChunkSize
	}
	meta := &uploadSessionMeta{
		ID:                 id,
		UserID:             uid,
		GuestIP:            ip,
		Name:               name,
		Size:               req.Size,
		ChunkSize:          chunkSize,
		ChunksTotal:        int((req.Size + chunkSize - 1) / chunkSize),
		Visibility:         req.Visibility,
		AlbumID:            req.AlbumID,
		PolicyID:           req.PolicyID,
		ExpiresIn:          req.ExpiresIn,
		MaxViews:           req.MaxViews,
		AccessPasswordHash: accessHash,
		CreatedAt:          time.Now(),
	}
	if err := h.D.Sessions.create(meta); err != nil {
		slog.Warn("创建上传会话失败", "err", err)
		Fail(w, http.StatusInternalServerError, CodeInternal, "服务器内部错误")
		return
	}
	OK(w, map[string]any{
		"session_id":   meta.ID,
		"chunk_size":   meta.ChunkSize,
		"chunks_total": meta.ChunksTotal,
		"size":         meta.Size,
		"expires_at":   meta.CreatedAt.Add(uploadSessionTTL).UTC().Format(time.RFC3339),
	})
}

// PutUploadChunk PUT /api/v1/upload/session/{id}/{index}
func (h *UploadHandlers) PutUploadChunk(w http.ResponseWriter, r *http.Request) {
	st, meta := h.loadSession(w, r)
	if st == nil {
		return
	}
	idx, err := strconv.Atoi(chi.URLParam(r, "index"))
	if err != nil || idx < 0 || idx >= meta.ChunksTotal {
		Fail(w, http.StatusBadRequest, CodeInvalidRequest, "分片序号不合法")
		return
	}
	want := expectedChunkSize(meta, idx)

	lock := st.lock(meta.ID)
	lock.mu.Lock()
	if lock.completing {
		lock.mu.Unlock()
		Fail(w, http.StatusConflict, CodeUploadIncomplete, "会话正在合并，请稍后重试")
		return
	}
	lock.inflight++
	lock.mu.Unlock()
	defer func() {
		lock.mu.Lock()
		lock.inflight--
		if lock.inflight == 0 {
			lock.cond.Broadcast()
		}
		lock.mu.Unlock()
	}()

	body := http.MaxBytesReader(w, r.Body, uploadChunkMax)
	tmp, err := os.CreateTemp(st.partsDir(meta.ID), "part-*")
	if err != nil {
		Fail(w, http.StatusInternalServerError, CodeInternal, "接收分片失败")
		return
	}
	tmpName := tmp.Name()
	n, err := io.Copy(tmp, body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmpName)
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			Fail(w, http.StatusRequestEntityTooLarge, CodeFileTooLarge, "分片过大")
			return
		}
		Fail(w, http.StatusInternalServerError, CodeInternal, "接收分片失败")
		return
	}
	if n != want {
		os.Remove(tmpName)
		Fail(w, http.StatusBadRequest, CodeInvalidRequest,
			fmt.Sprintf("分片大小不符：期望 %d 字节，收到 %d 字节", want, n))
		return
	}
	if err := os.Rename(tmpName, st.partPath(meta.ID, idx)); err != nil {
		os.Remove(tmpName)
		Fail(w, http.StatusInternalServerError, CodeInternal, "保存分片失败")
		return
	}
	got, _, missing := st.received(meta)
	OK(w, map[string]any{
		"index":          idx,
		"received_bytes": got,
		"missing":        len(missing),
	})
}

// GetUploadSession GET /api/v1/upload/session/{id}
// 断点续传用：返回已收到的分片与缺失分片。
func (h *UploadHandlers) GetUploadSession(w http.ResponseWriter, r *http.Request) {
	st, meta := h.loadSession(w, r)
	if st == nil {
		return
	}
	bytes, have, missing := st.received(meta)
	lock := st.lock(meta.ID)
	lock.mu.Lock()
	completing := lock.completing
	lock.mu.Unlock()
	state := "open"
	if completing {
		state = "completing"
	}
	OK(w, map[string]any{
		"session_id":     meta.ID,
		"name":           meta.Name,
		"size":           meta.Size,
		"chunk_size":     meta.ChunkSize,
		"chunks_total":   meta.ChunksTotal,
		"received_bytes": bytes,
		"received":       have,
		"missing":        missing,
		"state":          state,
		"expires_at":     meta.CreatedAt.Add(uploadSessionTTL).UTC().Format(time.RFC3339),
	})
}

// CompleteUploadSession POST /api/v1/upload/session/{id}/complete
func (h *UploadHandlers) CompleteUploadSession(w http.ResponseWriter, r *http.Request) {
	st, meta := h.loadSession(w, r)
	if st == nil {
		return
	}
	lock := st.lock(meta.ID)
	lock.mu.Lock()
	if lock.completing {
		lock.mu.Unlock()
		Fail(w, http.StatusConflict, CodeUploadIncomplete, "会话正在合并，请稍后重试")
		return
	}
	lock.completing = true
	for lock.inflight > 0 {
		lock.cond.Wait()
	}
	lock.mu.Unlock()

	resetOpen := func() {
		lock.mu.Lock()
		lock.completing = false
		lock.mu.Unlock()
	}

	_, _, missing := st.received(meta)
	if len(missing) > 0 {
		resetOpen()
		Fail(w, http.StatusConflict, CodeUploadIncomplete,
			fmt.Sprintf("还缺 %d 个分片", len(missing)))
		return
	}

	// 按序拼装成临时文件，交给 Save（Save 内部 defer 删除临时文件）。
	// 放在会话目录里而不是系统 /tmp：容器里 /data 是挂载卷，容量与上传数据同盘。
	out, err := os.CreateTemp(st.dir(meta.ID), "assembled-*")
	if err != nil {
		resetOpen()
		Fail(w, http.StatusInternalServerError, CodeInternal, "服务器内部错误")
		return
	}
	assembled := out.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(assembled)
		}
	}()
	var total int64
	for i := 0; i < meta.ChunksTotal; i++ {
		f, oerr := os.Open(st.partPath(meta.ID, i))
		if oerr != nil {
			out.Close()
			resetOpen()
			Fail(w, http.StatusInternalServerError, CodeInternal, "分片读取失败")
			return
		}
		n, cerr := io.Copy(out, f)
		f.Close()
		if cerr != nil {
			out.Close()
			resetOpen()
			Fail(w, http.StatusInternalServerError, CodeInternal, "分片拼装失败")
			return
		}
		total += n
	}
	if err := out.Close(); err != nil {
		resetOpen()
		Fail(w, http.StatusInternalServerError, CodeInternal, "分片拼装失败")
		return
	}
	if total != meta.Size {
		os.Remove(assembled)
		resetOpen()
		st.remove(meta.ID)
		Fail(w, http.StatusBadRequest, CodeInvalidRequest,
			fmt.Sprintf("拼装大小不符：期望 %d 字节，得到 %d 字节", meta.Size, total))
		return
	}
	ok = true // 交给 Save 清理

	opts := upload.Opts{
		Visibility:         meta.Visibility,
		AlbumID:            meta.AlbumID,
		PolicyID:           meta.PolicyID,
		MaxViews:           meta.MaxViews,
		AccessPasswordHash: meta.AccessPasswordHash,
	}
	if meta.ExpiresIn > 0 {
		t := time.Now().Add(time.Duration(meta.ExpiresIn) * time.Second)
		opts.ExpiresAt = &t
	}
	var u *model.User
	if p := PrincipalFrom(r); p != nil {
		u = p.User
	}
	res, err := h.D.Svc.Save(r.Context(), assembled, meta.Name, u, opts, ClientIP(r))
	if err != nil {
		if permanentUploadError(err) {
			st.remove(meta.ID) // 重试也不会成功，别占着磁盘
		} else {
			resetOpen()
		}
		failUpload(w, err)
		return
	}
	st.remove(meta.ID)
	if h.D.Hooks != nil && res != nil && res.Image != nil {
		h.D.Hooks.Emit("image.uploaded", map[string]any{
			"key": res.Image.Key, "name": res.Image.Name, "status": res.Image.Status,
		})
	}
	OK(w, uploadResultDTO(res, h.D.Res))
}

// CancelUploadSession DELETE /api/v1/upload/session/{id}
func (h *UploadHandlers) CancelUploadSession(w http.ResponseWriter, r *http.Request) {
	st, meta := h.loadSession(w, r)
	if st == nil {
		return
	}
	st.remove(meta.ID)
	OK(w, nil)
}

// permanentUploadError 判断错误是否属于"重试也没用"的校验失败。
func permanentUploadError(err error) bool {
	switch {
	case errors.Is(err, upload.ErrFileTooLarge),
		errors.Is(err, upload.ErrQuotaExceeded),
		errors.Is(err, upload.ErrBandwidthExceeded),
		errors.Is(err, upload.ErrExtNotAllowed),
		errors.Is(err, upload.ErrHeicUnavailable),
		errors.Is(err, upload.ErrDimensionOver),
		errors.Is(err, upload.ErrInvalidImage),
		errors.Is(err, upload.ErrPolicyNotAllowed),
		errors.Is(err, upload.ErrAlbumNotFound),
		errors.Is(err, upload.ErrGuestNotSupported),
		errors.Is(err, upload.ErrExpiresOverGroup),
		errors.Is(err, upload.ErrMaxViewsOverGroup):
		return true
	}
	return false
}
