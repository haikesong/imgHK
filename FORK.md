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
| — | — | 初始 fork，尚未修改 |

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

## Rules for this fork / 本仓库的约束

1. Do not remove or weaken the AGPL license, copyright notices or attribution.
2. Do not add secrets, tokens, `.env` files or server addresses.
3. Keep the public source in sync with what is deployed. If you deploy a tag,
   push that tag here; if you deploy a commit, push that commit.
4. Any user-facing instance must link back to this repository (footer or About
   page) so the source offer is prominent.
