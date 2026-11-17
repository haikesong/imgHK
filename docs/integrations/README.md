# Integrations

Upload to imgli / [img.li](https://img.li) using the same HTTP contract:

| Item | Value |
|------|--------|
| URL | `{BASE_URL}/api/v1/upload` |
| Method | `POST` |
| Body | `multipart/form-data`, file field **`file`** |
| Auth | Header `Authorization: Bearer <API_TOKEN>` |
| URL path in JSON | **`data.links.url`** |

Create a token: **Settings → API Token** (scope `upload` or `full`). Plaintext is shown once.

The table above is the **single-request** contract: it stays valid for small files and third-party clients. Files ≥ 32 MiB can use the chunked session API — `POST /api/v1/upload/session`, `PUT …/{id}/{index}`, `POST …/{id}/complete` — which the bundled CLI selects automatically above that threshold. Payloads and limits are documented in [FORK.md Chunked uploads](../../FORK.md#chunked-uploads--分片上传大文件会话--断点续传).

| Guide | Client |
|-------|--------|
| [picgo.md](../picgo.md) | PicGo / PicGo-Core / Typora / VS Code |
| [sharex.md](sharex.md) | [ShareX](https://getsharex.com/) (Windows) |
| [upic.md](upic.md) | [uPic](https://github.com/gee1k/uPic) / PicList-style custom uploaders |
| README **CLI** | `imgli upload` (single file; auto-chunks ≥32 MiB) · `imgli import-dir` (bulk directory) |

### Optional form fields (upload)

| Field | Notes |
|-------|--------|
| `expires_in` | Seconds; `0` / omit = permanent **unless** the user group forbids it (v0.9+) |
| `max_views` | `0` / omit = unlimited **unless** group `max_max_views` > 0 |
| `visibility` | `public` \| `private` |

The chunked `POST /api/v1/upload/session` accepts the same optional fields
(`visibility`, `expires_in`, `max_views`, plus `album_id` / `policy_id`).

Group limits (token account): `GET /api/v1/user/quota`. Guest:
`GET /api/v1/config` → `guest`. Full policy guide:
[user-groups-lifecycle.md](../user-groups-lifecycle.md).

Error codes for over-cap options: `expires_over_group`, `max_views_over_group` (HTTP 400).

### CLI

```bash
export IMGLI_BASE_URL=https://your-host
export IMGLI_TOKEN='your-api-token'

imgli upload shot.png
imgli upload -verbose shot.png              # print group expiry/views limits (stderr)
imgli upload -expires-in 86400 shot.png
imgli upload big.png                        # ≥32 MiB: session API, server chunk_size (8 MiB), 3 concurrent PUTs
imgli upload -chunked always big.png        # force session API even below 32 MiB
imgli upload -chunked never big.png         # force a single POST /api/v1/upload
imgli import-dir ./photos                   # recursive by default
imgli import-dir -dry-run ./photos
imgli import-dir -visibility private ./in
imgli import-dir -chunked never ./in
```

`import-dir` flags: `-recursive` (default true), `-continue` (default true),
`-base-url`, `-token`, `-chunked`.

`-chunked` (both `upload` and `import-dir`):

| Value | Behavior |
|-------|----------|
| `auto` (default) | Files **≥ 32MiB** use the session API (`/api/v1/upload/session`, server-chosen `chunk_size`, 3 concurrent PUTs); smaller files use `POST /api/v1/upload`. If the server does not expose the session routes (404/405/501 on create), the CLI falls back to a single request. |
| `always` | Always use the session API. Requires a file path; `upload -` (stdin) is rejected. No fallback: if the server lacks the session routes, the upload fails instead of silently sending one huge request. |
| `never` | Always use `POST /api/v1/upload`. |

`imgli upload` and `import-dir` switch to chunked upload at 32 MiB (`-chunked
auto`) and reuse the same retry/resume logic; `import-dir` applies it per file.
**stdin (`-`) stays single-request** because the size is unknown up front, so it
is still subject to any proxy body/timeout limit; `-chunked always` is rejected
for stdin. The server allows at most 4 unfinished sessions per user (or guest
IP) with a 2-hour TTL, and rate-limits `create` and `complete` (chunk
`PUT`/`GET`/`DELETE` are not rate-limited).

Retry behavior differs by endpoint:

- **`create`** is a non-idempotent `POST`. It is retried only for a `429` that
  carries `Retry-After` or for a `5xx` response. Transport errors (connection
  reset, response-header timeout, etc.) are **not** retried, because the request
  may already have created a session; retrying would leave an orphan that counts
  against the "at most 4 unfinished sessions" quota. A `429` without
  `Retry-After` is the quota limit itself and fails immediately.
- **`complete` / `status` / chunk `PUT`** retry `429`/`5xx` a bounded number of
  times, honoring `Retry-After`. A chunk `PUT` that receives
  `409 upload_incomplete` (the server is merging another `complete`) is also
  retried briefly.
- While the server reports `state=completing`, the CLI polls (`status` plus a
  `complete` attempt) for up to the complete timeout instead of deleting the
  session. This covers a `complete` whose response was lost at the gateway while
  the origin was still assembling/saving; deleting then would corrupt an upload
  that was about to succeed.
- Retryable failures (429/5xx, `409 upload_incomplete`, `state=completing` timeout)
  keep the server session. The CLI persists `session_id` in the user cache, so
  the next `upload` / `import-dir` of the **same file** (path + size + mtime)
  queries `GET /upload/session/{id}` and uploads only the missing chunks.
  Permanent validation errors delete the session and the cached state.

### Related HTTP surfaces (v0.3+)

| Surface | Notes |
|---------|--------|
| `POST/GET/PUT/DELETE /api/v1/upload/session…` | Chunked upload session; `POST /upload/session` and `…/complete` count toward group rate limits, chunk `PUT`/`GET`/`DELETE` do not |
| `GET /t/{key}.jpg?w=120\|200\|240\|400\|480\|800\|960\|1600` | Whitelisted thumbnail widths |
| `POST /api/v1/s/{key}/unlock` | Password-protected share unlock |
| `GET /api/v1/a/{id}` · `/a/{id}/images` | Public album visitor API |
| Outbound webhooks | Admin `GET/PUT /api/v1/admin/webhooks` |
| OIDC SSO | Admin `GET/PUT /api/v1/admin/oidc`; user start `/api/v1/auth/oidc/start` |

Product docs: [docs.imgli.com](https://docs.imgli.com) (when published from `docs-imgli/`).

Sample ShareX custom uploader: [imgli.sxcu.example](imgli.sxcu.example).

Replace `https://img.li` with your self-hosted `IMGLI_BASE_URL` everywhere.
