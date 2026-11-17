# 用 PicGo 对接 img.li

把 [PicGo](https://github.com/Molunerfinn/PicGo)（或 PicGo-Core / vs-picgo）配成上传到 **https://img.li**，截图后直接得到 Markdown / URL。

**前置**：在 img.li 登录 → **设置 → API Token** → 新建 Token（scope 选 `upload` 或 `full`），**只显示一次明文，先复制保存**。

游客上传也可走同一 URL，但 **无 Authorization 时受游客组限速**（当前约 3 次/日/IP），不适合当常驻图床。

---

## 1. 接口契约（已实现，无需改代码）

| 项 | 值 |
|---|---|
| URL | `https://img.li/api/v1/upload` |
| Method | `POST` |
| Body | `multipart/form-data`，文件字段名 **`file`** |
| 鉴权 | Header：`Authorization: Bearer <API_TOKEN>` |
| 可选表单 | `visibility`=`public`\|`private`（默认随用户偏好） |
| 成功 | HTTP 200，JSON 信封见下 |

成功响应形状（节选）：

```json
{
  "status": true,
  "message": "ok",
  "data": {
    "key": "xxxxxxxxxxxx",
    "name": "shot.png",
    "instant": false,
    "reused": false,
    "links": {
      "url": "https://img.li/i/xxxxxxxxxxxx.png",
      "markdown": "![shot.png](https://img.li/i/xxxxxxxxxxxx.png)",
      "html": "<img src=\"https://img.li/i/xxxxxxxxxxxx.png\" alt=\"shot.png\">",
      "bbcode": "[img]https://img.li/i/xxxxxxxxxxxx.png[/img]",
      "thumbnail_url": "https://img.li/t/xxxxxxxxxxxx.jpg"
    }
  }
}
```

**PicGo 取图链路径（JSON Path）**：`data.links.url`  
（若插件用 body 前缀写法，常见为 `data.links.url` / `.data.links.url`，以插件说明为准。）

字段说明（v0.9.2+）：

| 字段 | 含义 |
|---|---|
| `instant` | 原图未重新落盘（内容哈希命中既有物理文件） |
| `reused` | **同用户**命中已有 live 图且选项一致，返回**原 `key`**（图库不增行、不二次扣配额）；跨用户秒传仍为 `instant=true` 且 `reused=false` 并新建 key |

常见错误：

| HTTP | 含义 |
|---|---|
| 401 | Token 无效 / 未带 Bearer |
| 413 | 超过用户组大小上限 |
| 415 | 扩展名不允许 |
| 429 | 限速（看 `Retry-After`） |
| 403 | 游客上传关闭且未登录 |

---

## 2. curl 自测（推荐先过这一关）

```bash
export IMGLI_TOKEN='粘贴你的 token'
curl -sS -X POST 'https://img.li/api/v1/upload' \
  -H "Authorization: Bearer $IMGLI_TOKEN" \
  -F 'file=@/path/to/test.png' \
  -F 'visibility=public' | jq .
# 期望 data.links.url 可浏览器打开
```

---

## 3. PicGo 桌面版（Web 图床 / 自定义）

不同版本菜单位置略有差异，通用步骤：

1. 安装插件 **「web-uploader / Web 图床」**（插件市场搜 `web`）。
2. 图床设置 → 选择该插件 → 新增配置，例如：

| 配置项 | 填法 |
|---|---|
| API / URL | `https://img.li/api/v1/upload` |
| 方法 | `POST` |
| 文件字段名 / paramName | `file` |
| JSON 路径 / body | `data.links.url` |
| 自定义请求头 | `Authorization: Bearer <你的 Token>` |
| （可选）自定义 Body | `visibility=public` 若插件支持额外表单字段 |

3. 设为默认图床 → 上传测试图 → 剪贴板应为 `https://img.li/i/...`。

### PicGo-Core（CLI）配置片段示例

配置文件位置见 [PicGo 文档](https://picgo.github.io/PicGo-Doc/zh/guide/config.html)。  
插件名以你实际安装的 web uploader 为准，逻辑等价于：

```json
{
  "picBed": {
    "uploader": "web-uploader",
    "web-uploader": {
      "url": "https://img.li/api/v1/upload",
      "paramName": "file",
      "jsonPath": "data.links.url",
      "customHeader": "{\"Authorization\":\"Bearer YOUR_TOKEN_HERE\"}"
    }
  }
}
```

> 字段名因插件版本可能叫 `api` / `body` / `headers`，**以插件 README 为准**；自测 curl 成功后，只差把同一 URL / Header / JSON Path 抄进插件。

---

## 4. Typora / VS Code

- **Typora**：偏好设置 → 图像 → 上传服务选 PicGo-Core，并保证 Core 已配好 img.li。  
- **VS Code**：[vs-picgo](https://github.com/PicGo/vs-picgo) 使用与 PicGo-Core 类似的 `picBed` 配置。

---

## 5. 限制与建议

| 项 | 说明 |
|---|---|
| Token | 泄露即等同账密上传权；可设置页作废重建 |
| 配额 | 登录用户受用户组容量与限速约束 |
| 大文件 | PicGo / web-uploader 仍是单请求上传，受橙云 100MB 请求体与 100 秒超时限制；>100MB 请用网页上传或 `imgli` CLI（CLI 对 ≥32MiB 自动分片） |
| 有效期 / 次数 | 组 `max_expires_in` / `max_max_views` 等会拒绝非法 `expires_in`/`max_views`（见 [user-groups-lifecycle.md](user-groups-lifecycle.md)；客户端无 UI 时查 `GET /user/quota`） |
| 游客 | 仅适合临时试用，不要给 PicGo 配游客无 Token |
| 内容安全 | 上传可能进待审；公开直链受机审/词表策略影响 |
| 自建域名 | 把上文 `https://img.li` 换成你的 `base_url` 即可 |

---

## 6. 相关

- 其他客户端：[ShareX](integrations/sharex.md) · [uPic / PicList](integrations/upic.md) · [integrations 索引](integrations/README.md)  
- 用户组有效期策略：[user-groups-lifecycle.md](user-groups-lifecycle.md)  
- CLI：`imgli upload -verbose`（见仓库 README / integrations 索引）  
- 压测：`scripts/loadtest.py write --token …`  
- 健康检查脚本：`deploy/ops/health-check.sh`  
