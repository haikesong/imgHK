package cliupload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 会话式分片上传的客户端约定。服务端接口：
//
//	POST   /api/v1/upload/session               创建会话
//	PUT    /api/v1/upload/session/{id}/{index}  上传单个分片
//	GET    /api/v1/upload/session/{id}          查询已收/缺失分片
//	POST   /api/v1/upload/session/{id}/complete 合并并落库
//	DELETE /api/v1/upload/session/{id}          取消会话
//
// 客户端必须使用创建会话时服务端返回的 chunk_size，不能写死。
const (
	// DefaultChunkThreshold 是 auto 模式启用分片上传的文件大小阈值（32 MiB）。
	DefaultChunkThreshold int64 = 32 << 20

	defaultChunkConcurrency = 3
	chunkRetryAttempts      = 3 // 首次 + 重试 2 次
	chunkCompleteRounds     = 3

	chunkPutTimeout      = 2 * time.Minute
	chunkCompleteTimeout = 5 * time.Minute
	chunkMetaTimeout     = 30 * time.Second // 创建 / 查询
	// 清理会话用独立 context，不能跟着已取消的调用方 ctx 一起失败。
	chunkCleanupTimeout = 5 * time.Second

	chunkedAuto   = "auto"
	chunkedAlways = "always"
	chunkedNever  = "never"
)

// 测试可覆盖，生产值保持不变。
var (
	chunkCompletingWait = 3 * time.Second
	// 服务端 state=completing 时最多等待这么久再放弃；默认不短于单次
	// complete 超时，避免源站还在拼装/落库时客户端就 DELETE 会话。
	chunkCompletingWindow = chunkCompleteTimeout
	chunkRetryBaseDelay   = 100 * time.Millisecond
	// 服务端 429 可能给出任意大的 Retry-After；CLI 不能因此睡上几小时，
	// 超过这个上限就按上限等，等不到再交给用户重跑（会话会保留）。
	chunkMaxRetryWait = 60 * time.Second

	// 服务端返回的 chunk_size / chunks_total 必须有界：客户端会按它们
	// 一次性分配内存（3 路并发读片），不能让不兼容或恶意端点把进程打爆。
	// 下限 64KiB，上限 16MiB（服务端单片硬上限 uploadChunkMax=16MiB）；
	// chunks_total 上限 65536。
	chunkMinSize  int64 = 64 << 10
	chunkMaxSize  int64 = 16 << 20
	chunkMaxTotal       = 1 << 16
)

type createSessionRequest struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Visibility string `json:"visibility,omitempty"`
	ExpiresIn  int    `json:"expires_in,omitempty"`
}

type createSessionResponse struct {
	SessionID   string `json:"session_id"`
	ChunkSize   int64  `json:"chunk_size"`
	ChunksTotal int    `json:"chunks_total"`
	Size        int64  `json:"size"`
	ExpiresAt   string `json:"expires_at"`
}

type chunkPutResponse struct {
	Index         int   `json:"index"`
	ReceivedBytes int64 `json:"received_bytes"`
	Missing       int   `json:"missing"`
}

type sessionStatusResponse struct {
	SessionID     string `json:"session_id"`
	Name          string `json:"name"`
	Size          int64  `json:"size"`
	ChunkSize     int64  `json:"chunk_size"`
	ChunksTotal   int    `json:"chunks_total"`
	ReceivedBytes int64  `json:"received_bytes"`
	Received      []int  `json:"received"`
	Missing       []int  `json:"missing"`
	State         string `json:"state"`
	ExpiresAt     string `json:"expires_at"`
}

// apiHTTPError 携带服务端错误信封里的 code，并记录 429 的 Retry-After。
type apiHTTPError struct {
	StatusCode    int
	Code          string
	Message       string
	RetryAfter    time.Duration
	HasRetryAfter bool
}

func (e *apiHTTPError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("HTTP %d [%s]: %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Message)
}

// UploadFile 打开文件并按 Opts.Chunked / Opts.ChunkThreshold 选择单请求或分片上传。
func UploadFile(ctx context.Context, opts Opts, path string) (*Result, error) {
	mode := strings.ToLower(strings.TrimSpace(opts.Chunked))
	if mode == "" {
		mode = chunkedAuto
	}
	switch mode {
	case chunkedAuto, chunkedAlways, chunkedNever:
	default:
		return nil, fmt.Errorf("未知的分片模式 %q（支持 auto|always|never）", opts.Chunked)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, fmt.Errorf("不是文件: %s", path)
	}
	if strings.TrimSpace(opts.Filename) == "" {
		opts.Filename = filepath.Base(path)
	}

	useChunked := false
	switch mode {
	case chunkedAlways:
		useChunked = true
	case chunkedNever:
		useChunked = false
	default: // auto
		threshold := opts.ChunkThreshold
		if threshold <= 0 {
			threshold = DefaultChunkThreshold
		}
		useChunked = st.Size() >= threshold
	}
	if useChunked {
		if opts.Resume {
			opts.resumeStatePath = resumeStateFile(opts.ResumeStateDir, path, st.Size(), st.ModTime())
		}
		return UploadChunked(ctx, opts, f, st.Size())
	}
	return Upload(ctx, opts, f)
}

// UploadChunked 走完整的会话式分片上传流程：创建会话、并发传片、合并。
// 服务端不支持分片路由（404/405/501）时回退到单请求 Upload。
//
// 当 opts.resumeStatePath 非空（UploadFile + Opts.Resume 设置）时，客户端
// 会把 session_id 持久化到该路径；下次上传同一文件先 GET 会话状态，只补
// missing，避免可重试失败留下的会话占满服务端「未完成会话」配额。
func UploadChunked(ctx context.Context, opts Opts, r io.ReaderAt, size int64) (*Result, error) {
	if r == nil {
		return nil, fmt.Errorf("分片上传需要可随机读取的输入")
	}
	if size <= 0 {
		return nil, fmt.Errorf("文件大小无效: %d", size)
	}
	base, err := NormalizeBaseURL(opts.BaseURL)
	if err != nil {
		return nil, err
	}
	name, err := cleanFilename(opts.Filename)
	if err != nil {
		return nil, err
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{}
	}

	u := &chunkUploader{
		client: client,
		base:   base,
		token:  opts.Token,
		size:   size,
		r:      r,
	}

	statePath := opts.resumeStatePath
	vis := strings.TrimSpace(opts.Visibility)
	resumed := false
	var pending []int
	var sess *createSessionResponse
	if statePath != "" {
		// 即使状态不可用也要拿到 SessionID，用来删掉那个已作废的旧会话。
		rs, ok := loadResumeState(statePath, base, size, name, vis, opts.ExpiresIn)
		if ok {
			u.sessionID = rs.SessionID
			st, serr := u.status(ctx)
			switch {
			case serr == nil:
				if st.SessionID != "" {
					u.sessionID = st.SessionID
				}
				u.chunkSize = st.ChunkSize
				u.chunksTotal = st.ChunksTotal
				sess = &createSessionResponse{
					SessionID:   u.sessionID,
					ChunkSize:   st.ChunkSize,
					ChunksTotal: st.ChunksTotal,
					Size:        st.Size,
					ExpiresAt:   st.ExpiresAt,
				}
				// state=completing 时服务端正在合并，不能再补片；
				// 交给 completeWithRecovery 轮询等待。
				if st.State != "completing" {
					pending = st.Missing
				}
				resumed = true
			case isSessionGone(serr), isSessionUnusable(serr):
				// 会话已被消费/过期，或属于别的用户/IP：清掉状态，按新上传处理。
				u.sessionID = ""
				removeResumeState(statePath)
			default:
				return nil, fmt.Errorf("查询续传会话失败: %w", serr)
			}
		} else {
			// 文件或上传参数变了（或状态损坏/过期）：旧会话不再使用，
			// 尽力删除，避免占用「最多 4 个未完成会话」的配额。
			if rs.SessionID != "" {
				u.sessionID = rs.SessionID
				u.cancelSession()
				u.sessionID = ""
			}
			removeResumeState(statePath)
		}
	}

	if !resumed {
		sess, err = createUploadSession(ctx, client, base, opts, size, name)
		if err != nil {
			if isMissingSessionRoute(err) {
				// -chunked always 的意义就是绕开单请求的 100 秒限制，
				// 服务端不支持分片时静默回退会让大文件继续 524。
				if strings.EqualFold(strings.TrimSpace(opts.Chunked), chunkedAlways) {
					return nil, fmt.Errorf("服务端不支持分片上传，无法使用 -chunked always: %w", err)
				}
				return uploadSingleFallback(ctx, opts, base, r, size)
			}
			return nil, err
		}
		if sess.SessionID == "" {
			return nil, fmt.Errorf("创建上传会话失败: 响应缺少 session_id")
		}
		u.sessionID = sess.SessionID
		if statePath != "" {
			saveResumeState(statePath, resumeState{
				BaseURL:     base,
				SessionID:   sess.SessionID,
				Size:        size,
				Name:        name,
				Visibility:  vis,
				ExpiresIn:   opts.ExpiresIn,
				ChunkSize:   sess.ChunkSize,
				ChunksTotal: sess.ChunksTotal,
				ExpiresAt:   sess.ExpiresAt,
				SavedAt:     time.Now().UTC().Format(time.RFC3339),
			})
		}
	}

	// 会话已建立：任何后续校验失败都必须 DELETE，否则会一直占用
	// 服务端「同一用户最多 4 个未完成会话」配额直到 2 小时 TTL。
	// 因此清理 defer 必须早于所有响应校验安装。可重试失败保留会话，
	// 并保留本地续传状态供下次上传使用；成功或删除会话时一并清掉状态。
	completed := false
	defer func() {
		if completed || !u.keepSession {
			removeResumeState(statePath)
		}
		if !completed && !u.keepSession {
			u.cancelSession()
		}
	}()

	if err := validateCreateResponse(sess, size); err != nil {
		return nil, err
	}
	u.chunkSize = sess.ChunkSize
	u.chunksTotal = sess.ChunksTotal

	var indices []int
	if resumed {
		indices = pending
	} else {
		indices = make([]int, sess.ChunksTotal)
		for i := range indices {
			indices[i] = i
		}
	}
	if err := u.uploadIndices(ctx, indices); err != nil {
		// 可重试错误/整体超时保留会话；用户 Ctrl-C 或校验类错误才清理。
		u.keepSession = keepSessionOnError(ctx, err)
		return nil, err
	}
	res, err := u.completeWithRecovery(ctx)
	if err != nil {
		return nil, err
	}
	completed = true
	return res, nil
}

// validateCreateResponse 校验服务端下发的分片参数，避免按不可信数值分配内存。
func validateCreateResponse(sess *createSessionResponse, size int64) error {
	if sess.ChunkSize < chunkMinSize || sess.ChunkSize > chunkMaxSize {
		return fmt.Errorf("创建上传会话失败: 服务端返回的 chunk_size 无效（%d，允许 %d–%d）",
			sess.ChunkSize, chunkMinSize, chunkMaxSize)
	}
	if sess.ChunksTotal <= 0 || sess.ChunksTotal > chunkMaxTotal {
		return fmt.Errorf("创建上传会话失败: 服务端返回的 chunks_total 无效（%d，允许 1–%d）",
			sess.ChunksTotal, chunkMaxTotal)
	}
	wantChunks := int((size-1)/sess.ChunkSize + 1)
	if sess.ChunksTotal != wantChunks {
		return fmt.Errorf("创建上传会话失败: chunks_total=%d，按 chunk_size=%d 期望 %d",
			sess.ChunksTotal, sess.ChunkSize, wantChunks)
	}
	if sess.Size != size {
		return fmt.Errorf("创建上传会话失败: 服务端会话大小 %d 与本地文件 %d 不一致", sess.Size, size)
	}
	return nil
}

func createUploadSession(ctx context.Context, client *http.Client, base string, opts Opts, size int64, name string) (*createSessionResponse, error) {
	reqBody := createSessionRequest{
		Name:       name,
		Size:       size,
		Visibility: strings.TrimSpace(opts.Visibility),
	}
	// 与单请求上传保持一致：只有 >0 才发送 expires_in。omitempty 不会
	// 丢弃负数，服务端会以 400 拒绝，导致同一命令按文件大小表现不同。
	if opts.ExpiresIn > 0 {
		reqBody.ExpiresIn = opts.ExpiresIn
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 1; attempt <= chunkRetryAttempts; attempt++ {
		// 每次重试都要新的 request body；bytes.Reader 不能复用已读的实例。
		reqCtx, cancel := context.WithTimeout(ctx, chunkMetaTimeout)
		req, rerr := newAPIRequest(reqCtx, http.MethodPost, base+"/api/v1/upload/session", bytes.NewReader(payload), "application/json", opts.Token)
		if rerr != nil {
			cancel()
			return nil, rerr
		}
		var out createSessionResponse
		_, derr := doRequest(client, req, &out)
		cancel()
		if derr == nil {
			return &out, nil
		}
		lastErr = derr
		if attempt == chunkRetryAttempts || !retryableCreateError(ctx, derr) {
			return nil, derr
		}
		if werr := sleepCtx(ctx, retryDelay(derr, attempt)); werr != nil {
			return nil, werr
		}
	}
	return nil, lastErr
}

// retryableCreateError 只对明确可判断「服务端没有建会话」的响应重试：
// 429 仅在带 Retry-After 时重试（无 Retry-After 是「未完成的上传过多」，
// 重试只会继续撞配额），以及 5xx。create 是非幂等 POST，传输层错误
// （连接重置、等响应头超时等）无法判断请求是否已到达服务端，重试可能
// 创建第二个会话并让第一个变成孤儿、占用「未完成会话」配额，因此不重试。
func retryableCreateError(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	// 会话路由不存在（404/405/501）应立即回退单请求，重试没有意义。
	if isMissingSessionRoute(err) {
		return false
	}
	var he *apiHTTPError
	if errors.As(err, &he) {
		if he.StatusCode == http.StatusTooManyRequests {
			return he.HasRetryAfter
		}
		return he.StatusCode >= 500
	}
	return false
}

type chunkUploader struct {
	client      *http.Client
	base        string
	token       string
	sessionID   string
	size        int64
	chunkSize   int64
	chunksTotal int
	r           io.ReaderAt

	// keepSession 为 true 时，失败清理不再 DELETE 会话：用于 complete 的
	// 429/5xx/网络类可重试失败，保留已上传分片供后续续传。
	keepSession bool
}

func (u *chunkUploader) sessionURL() string {
	return u.base + "/api/v1/upload/session/" + url.PathEscape(u.sessionID)
}

func (u *chunkUploader) chunkLen(index int) (int64, error) {
	if index < 0 || index >= u.chunksTotal {
		return 0, fmt.Errorf("分片序号 %d 超出范围 [0,%d)", index, u.chunksTotal)
	}
	n := u.chunkSize
	if index == u.chunksTotal-1 {
		// 最后一片 = 总大小 - 前面所有分片。
		n = u.size - int64(index)*u.chunkSize
	}
	if n <= 0 {
		return 0, fmt.Errorf("分片 %d 大小无效: %d", index, n)
	}
	return n, nil
}

func (u *chunkUploader) readChunk(index int) ([]byte, error) {
	n, err := u.chunkLen(index)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, int(n))
	got, err := u.r.ReadAt(buf, int64(index)*u.chunkSize)
	// io.ReaderAt 允许在读到输入末尾时同时返回完整 n 和 io.EOF；
	// 先按字节数判断，合法的 EOF 不应被当成读片失败。
	if int64(got) == n {
		return buf, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取分片 %d: %w", index, err)
	}
	return nil, fmt.Errorf("读取分片 %d: 期望 %d 字节，实际 %d", index, n, got)
}

func (u *chunkUploader) putOnce(ctx context.Context, index int) error {
	data, err := u.readChunk(index)
	if err != nil {
		return err
	}
	reqCtx, cancel := context.WithTimeout(ctx, chunkPutTimeout)
	defer cancel()
	endpoint := u.sessionURL() + "/" + strconv.Itoa(index)
	req, err := newAPIRequest(reqCtx, http.MethodPut, endpoint, bytes.NewReader(data), "application/octet-stream", u.token)
	if err != nil {
		return err
	}
	// io.Reader 形式在 net/http 里不总能自动推导长度，显式设置。
	req.ContentLength = int64(len(data))
	var out chunkPutResponse
	_, err = doRequest(u.client, req, &out)
	return err
}

func (u *chunkUploader) putIndex(ctx context.Context, index int) error {
	var lastErr error
	for attempt := 1; attempt <= chunkRetryAttempts; attempt++ {
		err := u.putOnce(ctx, index)
		if err == nil {
			return nil
		}
		lastErr = err
		if attempt == chunkRetryAttempts || !retryableError(ctx, err) {
			return fmt.Errorf("上传分片 %d: %w", index, err)
		}
		if werr := sleepCtx(ctx, retryDelay(err, attempt)); werr != nil {
			return fmt.Errorf("上传分片 %d: %w", index, werr)
		}
	}
	return fmt.Errorf("上传分片 %d: %w", index, lastErr)
}

// uploadIndices 用固定并发数上传给定分片；首个错误会取消其余任务。
func (u *chunkUploader) uploadIndices(ctx context.Context, indices []int) error {
	if len(indices) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan int)
	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	worker := func() {
		defer wg.Done()
		for idx := range jobs {
			if err := u.putIndex(ctx, idx); err != nil {
				once.Do(func() {
					firstErr = err
					cancel()
				})
				return
			}
		}
	}
	workers := defaultChunkConcurrency
	if len(indices) < workers {
		workers = len(indices)
	}
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go worker()
	}
send:
	for _, idx := range indices {
		select {
		case jobs <- idx:
		case <-ctx.Done():
			break send
		}
	}
	close(jobs)
	wg.Wait()

	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (u *chunkUploader) status(ctx context.Context) (*sessionStatusResponse, error) {
	var lastErr error
	for attempt := 1; attempt <= chunkRetryAttempts; attempt++ {
		reqCtx, cancel := context.WithTimeout(ctx, chunkMetaTimeout)
		req, rerr := newAPIRequest(reqCtx, http.MethodGet, u.sessionURL(), nil, "", u.token)
		if rerr != nil {
			cancel()
			return nil, rerr
		}
		var out sessionStatusResponse
		_, derr := doRequest(u.client, req, &out)
		cancel()
		if derr == nil {
			return &out, nil
		}
		lastErr = derr
		if attempt == chunkRetryAttempts || !retryableError(ctx, derr) {
			return nil, derr
		}
		if werr := sleepCtx(ctx, retryDelay(derr, attempt)); werr != nil {
			return nil, werr
		}
	}
	return nil, lastErr
}

func (u *chunkUploader) complete(ctx context.Context) (*Result, error) {
	var lastErr error
	for attempt := 1; attempt <= chunkRetryAttempts; attempt++ {
		res, err := u.completeOnce(ctx)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if isUploadIncomplete(err) {
			// 409 由 completeWithRecovery 查状态后决定等待（completing）
			// 还是补传 missing；在这里重试只会重复拿到同一个 409。
			return nil, err
		}
		if attempt == chunkRetryAttempts || !retryableError(ctx, err) {
			// 可重试错误重试耗尽：保留会话，避免把已传分片随 DELETE 丢掉。
			// 用 OR：调用方可能已经因为 state=completing 设过 keepSession，
			// 这里的 404 等错误不应当把那个决定覆盖掉。
			u.keepSession = u.keepSession || keepSessionOnError(ctx, err)
			return nil, err
		}
		if werr := sleepCtx(ctx, retryDelay(err, attempt)); werr != nil {
			u.keepSession = keepSessionOnError(ctx, werr)
			return nil, werr
		}
	}
	return nil, lastErr
}

func (u *chunkUploader) completeOnce(ctx context.Context) (*Result, error) {
	reqCtx, cancel := context.WithTimeout(ctx, chunkCompleteTimeout)
	defer cancel()
	req, err := newAPIRequest(reqCtx, http.MethodPost, u.sessionURL()+"/complete", nil, "", u.token)
	if err != nil {
		return nil, err
	}
	var res Result
	raw, err := doRequest(u.client, req, &res)
	if err != nil {
		return nil, err
	}
	res.RawJSON = raw
	if res.Links.URL == "" {
		return nil, fmt.Errorf("响应缺少 links.url")
	}
	return &res, nil
}

// completeWithRecovery 处理 complete 的 409 upload_incomplete：
// 先查会话；正在合并就有界轮询等待，否则补传 missing 后再合并。
// 初始最多 chunkCompleteRounds 次 complete；每次成功补传会额外换取一次
// complete，确保「最后一轮补传」不会被轮次上限直接丢掉。
func (u *chunkUploader) completeWithRecovery(ctx context.Context) (*Result, error) {
	var lastErr error
	rounds := chunkCompleteRounds
	repairs := 0
	var completingSince time.Time
	for round := 1; round <= rounds; round++ {
		res, err := u.complete(ctx)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if !isUploadIncomplete(err) {
			// 之前观察到 completing、随后会话消失：服务端很可能已经合并成功
			// 并在写响应前删掉了会话（Cloudflare 524 / 连接中断时会这样）。
			if !completingSince.IsZero() && isSessionGone(err) {
				return nil, fmt.Errorf("上传会话已被服务端清理，但合并可能已经成功；请到图库确认后再决定是否重试: %w", err)
			}
			return nil, err
		}
		st, serr := u.status(ctx)
		if serr != nil {
			if !completingSince.IsZero() && isSessionGone(serr) {
				return nil, fmt.Errorf("上传会话已被服务端清理，但合并可能已经成功；请到图库确认后再决定是否重试: %w", serr)
			}
			// status 的可重试失败同样保留会话（分片都还在服务端）。
			u.keepSession = u.keepSession || keepSessionOnError(ctx, serr)
			return nil, serr
		}
		if st.State == "completing" {
			// 服务端正在拼装/落库（另一个 complete 在跑，或本客户端上一次
			// complete 在网关侧超时但源站仍在处理）。此时绝不能 DELETE：
			// Save 可能正在读 assembled 文件。用不短于单次 complete 超时的
			// 时间窗反复查询，直到服务端结束合并。
			u.keepSession = true
			if completingSince.IsZero() {
				completingSince = time.Now()
			}
			if time.Since(completingSince) >= chunkCompletingWindow {
				return nil, fmt.Errorf("服务端仍在合并上传会话（已等待 %s）: %w", chunkCompletingWindow, lastErr)
			}
			if werr := sleepCtx(ctx, chunkCompletingWait); werr != nil {
				return nil, werr
			}
			round-- // 等待合并不消耗补传轮次
			continue
		}
		completingSince = time.Time{}
		if len(st.Missing) > 0 {
			if repairs >= chunkCompleteRounds {
				return nil, fmt.Errorf("合并上传会话失败（补传 %d 次后仍缺分片）: %w", repairs, lastErr)
			}
			if uerr := u.uploadIndices(ctx, st.Missing); uerr != nil {
				// 补片撞上服务端合并（409 upload_incomplete）、可重试错误或
				// 整体超时：保留会话，本地续传状态会在下次上传时继续。
				u.keepSession = keepSessionOnError(ctx, uerr)
				return nil, uerr
			}
			repairs++
			rounds++ // 本次补传之后允许再 complete 一次
			continue
		}
		// 没有缺失片但仍 409（可能刚合并完），稍等再试。
		if werr := sleepCtx(ctx, chunkRetryBaseDelay); werr != nil {
			u.keepSession = keepSessionOnError(ctx, werr)
			return nil, werr
		}
	}
	return nil, fmt.Errorf("合并上传会话失败（已 complete %d 次）: %w", rounds, lastErr)
}

// cancelSession 用独立的 5 秒 context 尽力删除会话。
func (u *chunkUploader) cancelSession() {
	ctx, cancel := context.WithTimeout(context.Background(), chunkCleanupTimeout)
	defer cancel()
	req, err := newAPIRequest(ctx, http.MethodDelete, u.sessionURL(), nil, "", u.token)
	if err != nil {
		return
	}
	_, _ = doRequest(u.client, req, nil)
}

func uploadSingleFallback(ctx context.Context, opts Opts, base string, r io.ReaderAt, size int64) (*Result, error) {
	if seeker, ok := r.(io.Seeker); ok {
		if _, err := seeker.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("回退单请求上传: 重置文件位置失败: %w", err)
		}
	}
	opts.BaseURL = base
	return Upload(ctx, opts, io.NewSectionReader(r, 0, size))
}

func newAPIRequest(ctx context.Context, method, endpoint string, body io.Reader, contentType, token string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if tok := strings.TrimSpace(token); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	return req, nil
}

// doRequest 执行请求并按服务端信封解析；错误保留 data.code。
func doRequest(client *http.Client, req *http.Request, out any) (json.RawMessage, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, apiErrorFrom(resp, raw)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("HTTP %d：响应不是 JSON: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	if !env.Status {
		return nil, apiErrorFrom(resp, raw)
	}
	if out != nil {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return nil, fmt.Errorf("解析 data: %w", err)
		}
	}
	return env.Data, nil
}

func apiErrorFrom(resp *http.Response, raw []byte) *apiHTTPError {
	e := &apiHTTPError{StatusCode: resp.StatusCode}
	var env envelope
	if json.Unmarshal(raw, &env) == nil {
		e.Message = env.Message
		var data map[string]any
		if len(env.Data) > 0 && json.Unmarshal(env.Data, &data) == nil {
			if c, ok := data["code"].(string); ok {
				e.Code = c
			}
		}
	}
	if e.Message == "" {
		e.Message = truncate(strings.TrimSpace(string(raw)), 200)
	}
	if e.Message == "" {
		e.Message = http.StatusText(resp.StatusCode)
	}
	e.RetryAfter, e.HasRetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	return e
}

func parseRetryAfter(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	var d time.Duration
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0, true
		}
		d = time.Duration(secs) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = time.Until(t)
	} else {
		return 0, false
	}
	if d < 0 {
		d = 0
	}
	// 这里只负责解析，不做截断；上限在 retryDelay 里按 chunkMaxRetryWait
	// （60 秒）处理，避免按小时/按天窗口的 Retry-After 让 CLI 睡过整次调用。
	return d, true
}

func retryableError(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	var he *apiHTTPError
	if errors.As(err, &he) {
		// 409 upload_incomplete 是服务端明确要求稍后重试（会话正在合并）。
		// 与 Web 端 retryableChunkError 保持一致，PUT 撞上它时短暂退避重试。
		if he.StatusCode == http.StatusConflict {
			return he.Code == "" || he.Code == "upload_incomplete"
		}
		return he.StatusCode == http.StatusTooManyRequests || he.StatusCode >= 500
	}
	var ue *url.Error
	return errors.As(err, &ue)
}

// keepSessionOnError 判断失败后是否保留服务端会话与本地续传状态。
// 可重试错误（429/5xx/网络）、服务端正在合并、以及整体超时都保留；
// 用户主动取消（Ctrl-C）或校验类错误才清理。
func keepSessionOnError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if isUploadIncomplete(err) || retryableError(ctx, err) {
		return true
	}
	// retryableError 在 ctx 已取消时会返回 false；整体超时不是用户放弃，
	// 已经传完的分片应该留到下次续传。
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return true
	}
	return false
}

func retryDelay(err error, attempt int) time.Duration {
	var he *apiHTTPError
	if errors.As(err, &he) && he.StatusCode == http.StatusTooManyRequests {
		if he.HasRetryAfter {
			if he.RetryAfter > chunkMaxRetryWait {
				return chunkMaxRetryWait
			}
			return he.RetryAfter
		}
		return time.Second
	}
	return time.Duration(attempt) * chunkRetryBaseDelay
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isMissingSessionRoute(err error) bool {
	var he *apiHTTPError
	if !errors.As(err, &he) {
		return false
	}
	switch he.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	default:
		return false
	}
}

func isUploadIncomplete(err error) bool {
	var he *apiHTTPError
	if !errors.As(err, &he) || he.StatusCode != http.StatusConflict {
		return false
	}
	return he.Code == "" || he.Code == "upload_incomplete"
}

// isSessionGone 判断续传会话是否已被消费或过期。
func isSessionGone(err error) bool {
	var he *apiHTTPError
	if !errors.As(err, &he) {
		return false
	}
	return he.StatusCode == http.StatusNotFound || he.StatusCode == http.StatusGone
}

// isSessionUnusable 判断续传会话是否已不可能再使用：换了用户/IP（403）
// 或会话 id 不再被接受（400）。401 不在此列——token 修好后同一会话仍可续传。
func isSessionUnusable(err error) bool {
	var he *apiHTTPError
	if !errors.As(err, &he) {
		return false
	}
	return he.StatusCode == http.StatusBadRequest || he.StatusCode == http.StatusForbidden
}

// resumeState 是跨进程续传用的本地状态。CLI 无法在失败后直接复用内存里的
// session_id，因此把它写到用户缓存目录，键为「文件绝对路径 + 大小 + mtime」。
type resumeState struct {
	BaseURL     string `json:"base_url"`
	SessionID   string `json:"session_id"`
	Size        int64  `json:"size"`
	Name        string `json:"name,omitempty"`
	Visibility  string `json:"visibility,omitempty"`
	ExpiresIn   int    `json:"expires_in,omitempty"`
	ChunkSize   int64  `json:"chunk_size,omitempty"`
	ChunksTotal int    `json:"chunks_total,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	SavedAt     string `json:"saved_at,omitempty"`
}

// resumeStateFile 计算状态文件路径；dir 为空时用 os.UserCacheDir()/imgli/uploads。
// 返回空字符串表示当前环境无法确定可写目录，退化为不做跨进程续传。
func resumeStateFile(dir, path string, size int64, modTime time.Time) string {
	if dir == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(cache, "imgli", "uploads")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d", abs, size, modTime.UnixNano())))
	return filepath.Join(dir, fmt.Sprintf("%x.json", sum[:16]))
}

// loadResumeState 读取并校验本地续传状态。ok=false 表示不能复用该会话；
// 返回的 resumeState 仍带着 SessionID（如果文件本身可读），调用方据此删除旧会话。
// 文件名、可见性、有效期任一变化都必须重开会话：这些是创建会话时定下的，
// 服务端不会按请求参数更新，沿用旧会话会把新参数静默丢掉。
func loadResumeState(path, base string, size int64, name, visibility string, expiresIn int) (resumeState, bool) {
	var rs resumeState
	raw, err := os.ReadFile(path)
	if err != nil {
		return rs, false
	}
	if err := json.Unmarshal(raw, &rs); err != nil {
		return rs, false
	}
	if rs.SessionID == "" || rs.BaseURL != base || rs.Size != size {
		return rs, false
	}
	if rs.Name != name || rs.Visibility != visibility || rs.ExpiresIn != expiresIn {
		return rs, false
	}
	if rs.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, rs.ExpiresAt); err == nil && time.Now().After(t) {
			return rs, false
		}
	}
	return rs, true
}

func saveResumeState(path string, rs resumeState) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	raw, err := json.Marshal(rs)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

func removeResumeState(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}
