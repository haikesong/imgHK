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
