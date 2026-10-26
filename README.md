# browserd

独立浏览器执行服务（与业务系统解耦），提供 `use_browser` / `browser_use` 运行时所需的最小 HTTP 接口。

## 当前能力（V1）
- `POST /v1/sessions`
- `POST /v1/sessions/{runtimeSessionId}/commit`
- `DELETE /v1/sessions/{runtimeSessionId}`
- `POST /v1/sessions/{runtimeSessionId}/cookies/import`
- `POST /v1/profiles/cookies`
- `POST /v1/sessions/{runtimeSessionId}/navigate`
- `GET /v1/sessions/{runtimeSessionId}/snapshot`
- `POST /v1/sessions/{runtimeSessionId}/act`
- `POST /v1/sessions/{runtimeSessionId}/screenshot`
- profile 存储约定：每个 BrowserSession 单文件 `profile.tgz`（同 key 覆盖）

## 本地运行
```bash
go run ./cmd/browserd
```

环境变量：
- `BROWSERD_PORT`（默认 `7011`）
- `BROWSERD_CDP_BASE_URL`（默认 `ws://browserd:9222/devtools/browser`）
- `BROWSERD_PROFILE_STORE`：`s3` 或 `file`
- `BROWSERD_PROFILE_BUCKET`：profile 使用的 S3 bucket，默认 `private`
- `BROWSERD_PROFILE_FILE_ROOT`：`file` 模式下 profile 逻辑路径映射到的本地根目录

## Docker 镜像（内置 Chromium）
- 镜像内已打包 Chromium（路径默认 `CHROME_BIN=/usr/bin/chromium-browser`），可直接用于 chromedp/DevTools 场景。
- 构建：
```bash
docker build -t browserd:dev .
```

## 测试
```bash
go test ./...
```

## API 示例

### Create
```http
POST /v1/sessions
Content-Type: application/json

{
  "profilePath": "/browser-sessions/t_1/c_1/bs_1/profile.tgz",
  "fingerprint": {
    "seed": "fp_7f8c2a0e-57f7-4f34-93d3-45830f3c6c6d",
    "locale": "en-US",
    "languages": ["en-US", "en"],
    "acceptLanguage": "en-US,en;q=0.9",
    "timezone": "America/New_York",
    "platform": "Win32",
    "os": "Windows",
    "userAgent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36",
    "viewportWidth": 1366,
    "viewportHeight": 768,
    "screenWidth": 1366,
    "screenHeight": 768,
    "deviceScaleFactor": 1,
    "hardwareConcurrency": 8,
    "deviceMemory": 8,
    "webglVendor": "Google Inc. (Intel)",
    "webglRenderer": "ANGLE (Intel)"
  },
  "proxyServer": "http://user:pass@proxy.example.com:8080",
  "expectedVersion": "v0",
  "leaseId": "lease_1"
}
```

`POST /v1/sessions` 是同步初始化接口：
- `fingerprint` 必填，且必须传完整浏览器主体配置，包括 `seed`、`locale`、`languages`、`acceptLanguage`、`timezone`、`platform`、`os`、`userAgent`、视口/屏幕尺寸、硬件并发、内存与 WebGL 信息
- `profilePath` 必填，且只能是以 `/` 开头、以 `profile.tgz` 结尾的逻辑路径；不得传 `s3://`、`file://`、`http://` 等协议型路径
- profile 存储后端由配置层统一决定：`s3` 模式使用 `BROWSERD_PROFILE_BUCKET`，`file` 模式使用 `BROWSERD_PROFILE_FILE_ROOT`
- seed-only 请求不再支持；缺失或不完整时直接返回 `INVALID_FINGERPRINT_CONFIG`
- browserd 不从 seed 推导运行时配置；调用方必须把数据库中的完整 fingerprint 配置传入本接口
- `screenWidth` / `screenHeight` 是 live Xvfb/VNC 桌面尺寸，也是 Chromium `--window-size` 的唯一来源
- `viewportWidth` / `viewportHeight` 只用于 CDP `SetDeviceMetricsOverride` 的页面视口配置，不作为 live 桌面或 Chromium 窗口尺寸的 fallback
- `proxyServer` 可选，支持 `http://host:port`、`http://user:pass@host:port`、`socks5://host:port`、`socks5://user:pass@host:port`
- 代理认证信息只用于 Chromium 连接代理；日志、错误和 Chrome 启动参数不得包含明文密码
- 返回 200 前，Chromium 已启动且 DevTools websocket 已 ready
- Chromium 仍以 `about:blank` 启动，不额外执行首屏 navigate
- 若 readiness 失败，接口返回 `503 SESSION_INIT_FAILED`
- 失败时不会保留可继续使用的 `runtimeSessionId`

### Commit
```http
POST /v1/sessions/{runtimeSessionId}/commit
Content-Type: application/json

{
  "ifMatchVersion": "v0"
}
```

### Delete
```http
DELETE /v1/sessions/{runtimeSessionId}
```

### Navigate
```http
POST /v1/sessions/{runtimeSessionId}/navigate
Content-Type: application/json

{
  "url": "https://www.baidu.com/",
  "waitUntil": "load",
  "timeoutMs": 30000,
  "afterLoadScreenshotS3Path": "s3://browserd-snapshots/team_1/conv_1/1737373333.png"
}
```

说明：
- `navigate` 是唯一的页面跳转接口，不新增 `relocation` 同义接口。
- `afterLoadScreenshotS3Path` 为可选字段，必须传完整 `s3://bucket/key`。
- 截图 bucket 不从 `profilePath` 推导，允许与 userdata bucket 不同。

### Snapshot
```http
GET /v1/sessions/{runtimeSessionId}/snapshot?mode=refs
```

响应示例：

```json
{
  "data": {
    "snapshotId": "snap_123",
    "page": {
      "url": "https://www.baidu.com/",
      "title": "百度一下，你就知道",
      "formatVersion": 3,
      "encoding": "dom-tree-tuples-v1",
      "attributes": [],
      "states": [],
      "tree": ["html", "n1", {}, ["body", "n2", {},
        ["button", "n3", {"ref": "e1"}, ["#text", "n4", "百度一下"]]
      ]],
      "capture": {"scope": "light-dom", "complete": true, "omissions": []}
    }
  },
  "error": null
}
```

约束：
- `snapshot.page` 是唯一页面阅读结构
- `page.formatVersion: 3` 使用单一紧凑 DOM 树，不同时输出 `groups`
- 元素为 `[tag,id,properties,...children]`；`properties.attrs/state` 是字典的零基索引
- `n*` 是本次采集的阅读节点 ID，不是操作 ref
- `e*` 表示可操作元素
- 文本作为 `tag: "#text"` 的树节点出现，不注册为点击目标

### Act
```http
POST /v1/sessions/{runtimeSessionId}/act
Content-Type: application/json

{
  "action": "click",
  "ref": "e13",
  "motionProfile": "humanized"
}
```

点击视口坐标（用于 closed shadow DOM/custom element 内部热区等无法稳定暴露 `ref` 的页面）：

```http
POST /v1/sessions/{runtimeSessionId}/act
Content-Type: application/json

{
  "action": "click",
  "x": 742.5,
  "y": 724,
  "motionProfile": "direct"
}
```

输入并提交（一次调用完成）：

```http
POST /v1/sessions/{runtimeSessionId}/act
Content-Type: application/json

{
  "action": "type",
  "ref": "e1",
  "text": "复旦附中",
  "clear": true,
  "submit": true
}
```

约束：
- `click` / `fill` / `press` / `hover` / `select` / `waitFor` 只接受 `e*`
- `click` 可不传 `ref`，改传视口坐标 `x` / `y`；坐标点击同样通过 CDP mouse events 执行
- `click` 通过 CDP mouse events 执行，默认 `motionProfile=humanized` 会先生成 mousemove 轨迹再 press/release
- live viewer 会显示 browserd 内部虚拟鼠标；虚拟鼠标由 `/act` 的 mousemove/press/release 状态驱动，不修改目标页面 DOM，也不改变 `/v1/sessions/{runtimeSessionId}/act` 的外部请求结构
- `type` 必须显式指定 `ref`，先聚焦目标，再通过 CDP `Input.insertText` 插入文本；不依赖当前焦点
- `type` 的 `submit` 为 `true` 时，在同一次 CDP 往返里紧接着按下 Enter。提交表单或触发搜索一律用它，
  不要用 `type` 后再发一次 `press`——两次 `/act` 之间页面可能重渲染并让 `ref` 失效
- `type` 的 `text` 不能为空，所以 `submit` 不能当作单独的「按回车」使用；那是 `press` 的职责
- `press` 的 `key` 只接受以下命名键（W3C `KeyboardEvent.key` 写法，大小写敏感）：
  `ArrowDown`、`ArrowLeft`、`ArrowRight`、`ArrowUp`、`Backspace`、`Delete`、`End`、`Enter`、`Escape`、`Home`、`PageDown`、`PageUp`、`Space`、`Tab`；
  或任意单个可打印字符，例如 `"a"`、`"/"`
- `press` 不接受别名（`Return`、`esc`、`up`）和修饰键组合（`Control+s`）；非法 `key` 返回 `INVALID_KEY`，错误信息会给出正确写法，不会把 `key` 当作文本输入
- `scrollIntoView` 接受 `e*` 与 `t*`
- 对 `t*` 执行 `click` 会返回 `INVALID_REF`

### Screenshot
```http
POST /v1/sessions/{runtimeSessionId}/screenshot
Content-Type: application/json

{
  "mode": "selector",
  "selector": ".product-card",
  "format": "png",
  "screenshotS3Prefix": "/browser-screenshots/2026-08/team_1/conv_1/"
}
```

约束：
- `mode` 可选值为 `viewport`、`fullpage`、`selector`；未传时默认为 `viewport`
- `selector` 只允许在 `mode=selector` 时传入，且必须匹配唯一可见元素
- `ref` 和 `fullPage` 已不再支持；需要元素截图时使用 `mode=selector` 与 `selector`
- `screenshotS3Prefix` 必填，且只能是以 `/` 开头、以 `/` 结尾的逻辑路径；不得传 `s3://`、`file://`、`http://` 等协议型路径
- browserd 使用 `BROWSERD_PROFILE_BUCKET` 指定的 bucket 上传截图，返回 `screenshotId` 为 `<uuid>.png`，返回 `s3Path` 为逻辑路径

### Navigation with a snapshot

The v3 runtime emits a compact pruned DOM tree with ordered text nodes and
independent images. Full observed resource URLs and hrefs are preserved;
browserd does not infer product identity, prices or image associations. Generic
attributes, control states and necessary ancestor boundaries are retained.
`capture.complete` describes bounded light-DOM capture, not page loading or
business completeness. Frame/shadow-root boundaries and capture limits are
explicit in `capture.omissions`. Structural node IDs cannot be used for actions.
The embedded runtime is generated from the pinned shared `browser-snapshot`
revision with `bash internal/browser/generate_browser_snapshot_runtime.sh`.
For an explicit local-source trial, set `BROWSER_SNAPSHOT_SOURCE` to the shared
package checkout when running that script. It builds that source and records
the exported runtime SHA-256 in the generated header; it does not update the
release pin. The published Go SDK supplies `DecodeSnapshotPage` for explicit
legacy/v2/v3 decoding; release builds and tests use `GOWORK=off` and the versions
in go.mod. Existing clients that require
`page.groups` are incompatible until their version-aware readers are migrated;
do not add empty groups or silently fall back to legacy data to conceal this.

Set `includeSnapshot: true` on `POST /v1/sessions/{runtimeSessionId}/navigate`
with `waitUntil: "load"` (or omit waitUntil). The response includes
`snapshot: {snapshotId, page}`; page URL/title are also the navigation result's
URL/title. The returned refs are already registered and can be used immediately.
`snapshotCleared: true` means the **previous** snapshot state was cleared, not
that the newly returned snapshot is absent. Omit includeSnapshot to keep the
navigation-only response. Other waitUntil modes are not implemented by this
runtime; combined navigation explicitly rejects them instead of pretending to
honor them.

Navigation and snapshot extraction share timeoutMs (default runtime budget:
20 seconds), and request cancellation cancels the combined operation. A load
event is not proof of complete application data: inspect the observation and,
when needed, explicitly wait for an observed page condition before taking a new
snapshot. For example use `condition: {type: "element_visible", selector: "#buy"}`.
There is no fixed sleep or implicit second snapshot request.

Snapshot extraction failure returns `502 SNAPSHOT_FAILED`, with no valid
snapshot; navigation may already have happened. Old snapshot state is not
restored. Conflicting same-session HTTP operations return `409 SESSION_BUSY`
without executing, including handoff and deletion. Different sessions remain
independent; passive live-view streams do not hold the request guard.

Real-browser regression (start native browserd with Chromium; no Docker needed):

```bash
BROWSERD_BASE_URL=http://127.0.0.1:7011 go test ./e2e -run '^TestNavigateSnapshotE2E' -count=1 -v
```

The deterministic test site is served by the test process and must be reachable
from browserd (this command assumes they run on the same machine). A missing
BROWSERD_BASE_URL is reported as NOT EXECUTED / SKIP, never as browser validation.

## 导入 Cookie

`POST /v1/sessions/{runtimeSessionId}/cookies/import`

```json
{
  "cookies": [
    {"name": "session", "value": "opaque=value", "url": "https://example.com/", "httpOnly": true}
  ]
}
```

每条必须提供 name、value、HTTP/HTTPS 根 URL，可选 httpOnly（默认 false）。导入为目标主机的 Cookie，path 为 `/`，HTTPS 自动设置 secure；不推测子域名或原 Cookie 的过期时间。值原样保留，不执行 URL 解码。同名同作用域 Cookie 覆盖，其他 Cookie 保留。拒绝重复项、非法字段与超过大小限制的输入。允许在手动浏览器会话中导入。

成功响应 `data.importedCount` 是经 CDP 读取确认的数量；写入或校验失败返回错误。批量导入不是跨 Cookie 的事务，失败时部分 Cookie 可能已写入，可修正输入后重试。此接口不自行提交 profile；调用方可以保存导入配置，并在新会话初始化时重新导入。

Host 配置同步使用 `POST /v1/profiles/cookies`，请求包含 `profilePath`、正整数 `revision` 及 `cookies`，响应 `data.updatedSessions` 表示更新的活跃会话数。它匹配 profile 的全部当前会话，不依赖 Host 的会话登记表。Cookie 数组可以为空，表示不再自动导入，不清除已有 Cookie。会话导入接口也接受同一配置，且校验会话所属 profile。较旧 revision 会被拒绝，调用方需刷新配置并重试创建会话；该版本约束在 browserd 当前进程的会话生命周期内有效。
