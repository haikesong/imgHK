# Fork notes

This repository is a **modified fork** of
[imgli](https://github.com/yixian-huang/imgli), used to self-host
<https://img.hks.hk>. / 本仓库是 imgli 的修改版分支。

- Fork: <https://github.com/haikesong/imgHK>
- Upstream: <https://github.com/yixian-huang/imgli>

## License

AGPL-3.0-only. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

This fork, including every modification made here, is distributed under the
same license. / 本分支及其修改同样按 AGPL-3.0-only 发布。

## Source offer / 源码提供

**This repository is the Corresponding Source** for the instance at
<https://img.hks.hk>.

Anyone interacting with that instance remotely can obtain the complete source
of the running version here, at no charge and without asking for permission —
including the modifications and the build scripts needed to build and run it.

If the deployed version does not match `main` (for example a pinned tag), the
exact commit or tag is recorded in the deployment repository and in the release
notes.

## Modifications / 修改记录

Keep this table up to date. One row per user-visible change.

| Date | Commit | Change |
|---|---|---|
| 2026-11-10 | [`e0f7f17`](https://github.com/haikesong/imgHK/commit/e0f7f171f5c4e78c5e1ce96a2c239414957590c2) | 登录/注册与邮件流页脚移除上游产品署名，改为站点名 + AGPL 源码链接（`source_url`）。 |
| 2026-11-10 | [`93946c6`](https://github.com/haikesong/imgHK/commit/93946c6) | 登录失败按账号/IP 双维度退避锁定（账号 5 次/15 分钟、IP 30 次/10 分钟），429 带 `Retry-After`；未知账号同样计数，避免枚举。 |
| 2026-11-16 | [`62aa89f`](https://github.com/haikesong/imgHK/commit/62aa89f) | 大图上传：元数据剥离改容器级无损重写；vips 缩略图上限放宽到 256MiB / 4 亿像素；非 JSON 网关错误给出具体文案。 |

## Large uploads / 大图上传

线上 `img.hks.hk` 在 Cloudflare 橙云后面：**请求体 ≤ 100MB，源站响应 ≤ 100 秒**。
2026-11-16 的一次 95.6MB / 10500×15886 PNG 上传暴露了两个问题，修改如下：

- **`strip_exif`（默认开）不再整图重编码**。旧实现走 `image.Decode` + 重编码，
  1.6 亿像素要跑数分钟，先撞 Cloudflare 的 100 秒超时（浏览器只看到「响应格式错误」），
  服务端却已保存成功。现在走 `internal/imaging/strip.go` 的容器级无损重写：
  JPEG 丢 APP1/APP12/APP13/COM，PNG 丢 eXIf/tEXt/zTXt/iTXt/tIME，像素流逐字节保留，
  ICC/Adobe/色彩块不动。无元数据的图字节完全不变。
- **缩略图上限按构建后端分档**（`internal/imaging/limits_*.go`）：
  libvips 构建 256MiB / 4 亿像素（`vips_thumbnail_*` 流式解码），纯 Go 构建
  仍是 24MiB / 1600 万像素。可用 `IMGLI_THUMB_MAX_BYTES` / `IMGLI_THUMB_MAX_PIXELS` 覆盖。
  超过上限时 `/t/{key}` 返回灰色 `NOT FOUND` 占位 SVG（原图 `/i/{key}` 不受影响）。
- **缩放/水印/转 WebP** 仍需解码：超过 `upload.MaxProcessPixels`（1 亿像素）时整链
  跳过并记 `slog.Warn`，保留原字节，避免 OOM 与 100 秒超时。
- **前端**把 413/524/502 等非 JSON 响应翻译成具体原因（`web/src/upload/uploader.ts`），
  不再统一显示「响应格式错误」。

验证方式：`go test ./internal/imaging/... ./internal/service/upload/...`；
`make build-vips` 后传一张 >1 亿像素的 PNG，确认 `/t/{key}` 有缩略图、
`/i/{key}` 与原文件字节一致（或只少了元数据段）。

2026-11-16 在 2 CPU / 2GB 容器里实测（`VIPS_CONCURRENCY=1`）：

| 输入 | 结果 |
|---|---|
| 113.9MB PNG（首次上传，含缩略图） | 7.8s，落盘字节不变，`/i` sha256 与源文件一致，`/t` 399×399 WebP |
| 8MB / 166MP（10500×15886）PNG | 4.6s，`/t` 263×399 WebP（旧上限 16MP 时这里是 `NOT FOUND`） |
| 同字节再次上传 | 秒传命中（`instant=true`） |

## Chunked uploads / 分片上传（大文件会话 + 断点续传）

单请求上传受 Cloudflare **100 秒源站读取超时**限制：上行 1MB/s 时，100MB 文件
光传输就要 100 秒，浏览器大概率只等到 `524`，即使源站最终保存成功。分片把一次
长传输拆成若干 8MiB 的短请求，每片都远在 100 秒内完成。

**客户端触发条件**（`web/src/upload/uploader.ts`）：

| 常量 | 值 | 说明 |
|---|---|---|
| `CHUNK_THRESHOLD` | 32MiB | 小于此值仍走原来的 `POST /api/v1/upload`，行为不变 |
| `CHUNK_SIZE` | 8MiB | 以服务端 `chunk_size` 返回值为准 |
| `CHUNK_CONCURRENCY` | 3 | 单文件最多 3 片同时在途，任意顺序完成 |

**API**（全部在 `/api/v1` 下，鉴权与单请求上传一致：session 或 Bearer，游客按 IP）：

| 方法 | 路径 | 作用 |
|---|---|---|
| POST | `/upload/session` | 创建会话：`{name,size,visibility,album_id,policy_id,expires_in,max_views}` → `{session_id,chunk_size,chunks_total,expires_at}` |
| PUT | `/upload/session/{id}/{index}` | 上传第 `index` 片（原始字节，`0..chunks_total-1`），返回已收字节与缺失片数 |
| GET | `/upload/session/{id}` | 断点续传状态：`{received,missing,received_bytes,state}` |
| POST | `/upload/session/{id}/complete` | 拼装并落库，返回与单请求上传相同的 `UploadResult` |
| DELETE | `/upload/session/{id}` | 取消并删除临时分片 |

**服务端实现**（`internal/handler/upload_session.go`）：

- 会话目录 `<data_dir>/.upload-sessions/<id>/`：`meta.json` + `parts/<index>`。
  分片先写临时文件再原子 rename，因此**任意顺序、重复上传同一片**都安全（幂等覆盖）。
- 分片大小严格校验（最后一片除外）：大小不符返回 400 且不计入已收分片。
  单片请求体硬上限 16MiB，会话总大小仍受 `maxUploadBytes`（256MiB）与用户组限制约束。
- `complete` 等所有在途分片写完，按 index 顺序拼装成会话目录里的临时文件，再调用
  原有的 `upload.Service.Save`——去重（SHA-256）、元数据剥离、缩略图、配额、
  用户组限制、相册/策略/有效期与单请求路径**完全一致**。
- 创建会话时先做预检（`CheckSizeAllowed`：游客开关、组上限、配额、本月流量），
  避免用户白传几十 MB。
- 会话 TTL 2 小时；启动时与每 10 分钟扫描清理；同一用户/游客 IP 最多 4 个未完成会话。
- 限速：`POST /upload/session` 与 `complete` 计入用户组限速；**分片 PUT / GET / DELETE
  不计**——一个 100MB 文件是十余个请求，按每分钟额度会把正常上传打成 429。
  分片请求仍受鉴权、会话属主、单片大小与总大小约束。
- 会话属主：登录用户按 `user_id`，游客按 IP；跨用户/跨 IP 访问返回 403。

**断点续传**：

- 前端把 `file.name + size + lastModified` → `session_id` 记在 `localStorage`
  （`imgli:upload-session:*`，2 小时过期）。网络中断后点重试，先 `GET` 会话状态，
  只补 `missing` 里的分片。
- `complete` 返回 409 `upload_incomplete`（分片在服务端被判缺失/正在合并）时，
  客户端查一次状态：`state=completing` 就等 3 秒再合并（另一轮 complete 还在跑），
  否则补传 `missing` 后再合并一次。会话已被消费（404）时清掉续传记录、按可重试
  失败上报，下次重试新建会话（落库仍有 SHA-256 秒传兜底）。
- 用户删除上传卡片（abort）时：先中止在途 XHR，再 `DELETE` 会话并清掉续传记录。
- 网络错误/限速：**保留**会话与续传记录，卡片可重试；校验类错误（类型/配额/
  文件名不合法）删除会话，重试也不会成功。
- 注意：`complete` 仍在请求内完成 `Save`。若超大图的缩略图生成超过 Cloudflare
  100 秒，浏览器仍可能收到 524；这次重试会重建会话并重传，但 `Save` 的 SHA-256
  秒传会命中已落盘对象，不会产生第二份存储。

**验证**：

```bash
go test ./internal/handler/ -run TestUploadSession -count=1   # 乱序/重传/越权/清理
go test ./internal/service/upload/ -run TestCheckSizeAllowed -count=1
cd web && npx vitest run src/upload                            # 3 路并发/续传/取消
```

`_test.go` 里通过 `UploadSessionStore.chunkSize`（包内字段）把分片调小，用一张
几百字节的 PNG 覆盖多分片路径，不需要真的传大文件。

2026-11-16 本地镜像实测（2 CPU / 2GB，`VIPS_CONCURRENCY=1`，loopback）：

| 输入 | 分片数 | `complete` 耗时 | 端到端（切片+传输+complete+逐字节校验） |
|---|---|---|---|
| 113.9MB PNG（10500×15886） | 14 | 6.7s | 48s |
| 250MB PNG（640×480 + 私有 ancillary chunk 填充） | 30 | 30.8s | 120s |

两次 `/i` 下载都与源文件逐字节一致、`/t` 200、会话目录清理正常。端到端时间包含
在 `/mnt/d`（9p）上切片与 100–250MB 的本地校验 IO；真实瓶颈是每片 8MiB 的 PUT：
慢上行（0.6–1MB/s）下单片 8–13 秒，仍远低于 Cloudflare 的 100 秒。
`complete` 里是 SHA-256、容器级元数据重写与落盘，250MB 也要 30 秒左右——
真实超大图若在 `complete` 阶段撞上 100 秒，按上面的重试语义处理。

## Build / 构建

```bash
docker build -t imgli-fork:local .
```

The `fork-image` workflow also publishes `ghcr.io/haikesong/imghk:<tag>`.

**Windows 工作区构建要注意 CRLF。** 本机 checkout 的 `docker-entrypoint.sh` 可能是 CRLF，
`COPY` 进镜像后容器启动直接报
`exec /usr/local/bin/docker-entrypoint.sh: no such file or directory`（其实是 shebang 带了 `\r`）。
构建前先转回 LF：

```bash
sed -i 's/\r$//' docker-entrypoint.sh   # 或在 WSL/Git Bash 里 git checkout-index -f -- docker-entrypoint.sh
docker build -t imgli-fork:local .
```

仓库里提交的 blob 本身是 LF，CI（Linux）不受影响；只有从 CRLF 工作区本地构建才会踩到。

## Deploy / 部署

Deployment configuration (Compose, Caddyfile, environment templates) lives in a
separate private repository. **No secrets, `.env` files, server addresses or
credentials belong in this repository.**

## Sync with upstream / 跟进上游

```bash
git fetch upstream
git rebase upstream/main
```

`upstream` points at <https://github.com/yixian-huang/imgli>.

After a sync, verify the fork customisations survived:

```bash
git log --oneline upstream/main..main          # fork commits still on top
grep -rn "图鲤\|开源项目 imgli" web/src/i18n/locales web/src/ui || true
cd web && npx vitest run src/pages/auth/AuthPage.test.tsx
```

- Resolve conflicts by keeping the fork changes. Conflicts usually land in
  `web/src/i18n/locales/*/auth.ts`, `web/src/pages/auth/AuthPage.tsx` and
  `AuthShell.tsx`.
- Never `git reset --hard upstream/main`: it also drops the lowercase image-name
  commit (`7c5b41e`) that the GHCR workflow depends on.
- A rebase/merge keeps the existing modifications, but it does **not** remove
  new upstream branding added elsewhere. Review the diff and grep again.

## Rules for this fork / 本仓库的约束

1. Do not remove or weaken the AGPL license, copyright notices or the source
   offer. Upstream product branding may be changed, but the AGPL source link
   must stay prominent (that link is the licence obligation; the product name
   is not).
2. Do not add secrets, tokens, `.env` files or server addresses.
3. Keep the public source in sync with what is deployed. If you deploy a tag,
   push that tag here; if you deploy a commit, push that commit.
4. Any user-facing instance must link back to this repository (footer or About
   page) so the source offer is prominent.
