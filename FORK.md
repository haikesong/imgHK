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

## Build / 构建

```bash
docker build -t imgli-fork:local .
```

The `fork-image` workflow also publishes `ghcr.io/haikesong/imghk:<tag>`.

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
