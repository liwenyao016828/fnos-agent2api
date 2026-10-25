# fnos-agent2api

把上游 [Agent2API/agent2api](https://github.com/Agent2API/agent2api) 打包成飞牛
fnOS 的原生应用（`.fpk`），以**统一网关**方式接入桌面。

上游是 Rust 单文件静态二进制，自带官方 Web 控制台，内置多家 AI 客户端提供商适配
（WorkBuddy 国内版/国际版、小浣熊 Raccoon、CatPaw、AutoClaw、Qoder、Cline
Free/Pass、Accio、ZCode、CodeArts、Trae、Loomy、KukuAI、MonkeyCode、
Command Code、Antigravity、OfficeAce、OrcaTerm、智谱清言，另支持自定义提供商），
把客户端登录态复用成标准 OpenAI 兼容接口。

本仓库**不含上游源码**，只提供飞牛适配层 + 上游 WebUI 的一份副本：

```
fngateway/     Go 反向代理：持有 Unix socket、注入网关密钥、改面板子路径
cmd/           飞牛生命周期脚本（install / upgrade / config / uninstall）
config/        privilege（run-as=package）、resource
wizard/        安装/升级/卸载/配置向导文案
app/ui/        只含本项目桌面入口 config 与 images/；上游面板 213 个文件不再入库，
               构建时从官方包注入（见 §9.4.8）
manifest       应用元信息
build.py       打包脚本（本地与 CI 共用同一条路径）
.github/       CI：取上游官方 Linux 服务端包 + 出 fpk + 发 Release
```

> 这是个人自用项目。上游 LICENSE 是 MIT 正文 + 一份「使用声明」，其中第 3 条
> 禁止商业用途与二次分发牟利（个人学习、研究、自用不受限）。详见 §8。

> **当前版本对应上游 v3.2.0-beta.2**（应用版本 `3.2.0-beta.2-1`，约定是「上游版本-N」）。
> 2026-10-18 起上游仓库从旧地址迁到
> [`Agent2API/agent2api`](https://github.com/Agent2API/agent2api)：旧地址 API 与
> codeload 均为 404（实测），新仓库只有 v3.1.0 起的 tag，所以当时是**换仓库**
> 而不是「改个版本号」。本仓库历史上 `v2.9` 系列那两个应用版本跟的是旧仓库，
> Release 页上仍可下载。
>
> ⚠️ **2026-10-20 实测：该新仓库本身也已不可达** —— `Agent2API/agent2api` 与
> `Agent2API` 账号的 REST API（`/repos`、`/users`、`/orgs`）与网页均为 **HTTP 404**
> （同一时刻 `torvalds/linux` 返回 200，故非本机网络问题）。后果：源码 tarball 与
> Release 资产都无法下载，CI 的取包链路与 `bump_upstream.py` 的定时跟进会失败。
> 本版因此**只能用已落地的官方包离线构建**。详见 §9.4。
>
> **2026-10-25 本仓库改动**：上游仓库常量改回官方
> [`aimod-cc/agent2api`](https://github.com/aimod-cc/agent2api)（`Agent2API/agent2api`
> 至今仍 404，同时刻实测），pin 随之升到 `v3.3.0-beta.3`（应用版本 `3.3.0-beta.3-1`）。
> 本 README 正文（含 §9.4 与各处实测计数）仍是 v3.2.0-beta.2 时期的原文，**未随本次
> pin 改写** —— 那些是当时的实测数字，要重新实测才能改，不能直接套用。
>
> **2026-10-25 上游参考仓库的 CI 修复**：参考仓库 `ebab655`（「上游面板移出仓库，
> 改为构建时从官方包注入」）之后，`build-and-release.yml` 的 binaries job 是「先跑
> `python3 build.py --fetch-binaries`，再用 Go 现编 fngateway」，而 `--fetch-binaries`
> 结尾的 `check_binaries()` 同时要求 `fngateway-linux-{amd64,arm64}` 已存在 ——
> 这一步因此必然失败（参考仓库自己在 `ebab655` 这次 push 触发的构建就是 18 秒红，
> 此后每天的定时跟进也一直失败，Release 停在 `v3.2.0-beta.2-1`）。本仓库给
> `check_binaries()` 加了 `only_upstream` 开关：fetch 模式只预检这一步真正取回的
> 上游包二进制，fngateway 仍由 workflow 的「Verify binaries (arch + glibc)」步骤与
> 打包期的完整预检覆盖，检查面没有减少。

---

## 1. 架构

```
浏览器 ──https──> 飞牛统一网关 ──unix socket──> fngateway ──tcp 127.0.0.1:<随机>──> agent2api-server
（仅管理员）        /app/agent2api            （本仓库）      （内核分配）              （上游二进制）

OpenAI SDK ──http──> http://<飞牛IP>:3065/v1 ─────────────────────────────────────> agent2api-server
（任意客户端）        （独立下游端口，不经统一网关）
```

一个应用，两条入口，互不干扰：

| 入口 | 路径 / 端口 | 给谁用 | 鉴权 |
| --- | --- | --- | --- |
| 统一网关 | `/app/agent2api` | 飞牛桌面里的 Web 控制台 | 飞牛登录态 + 仅管理员 |
| 下游端口 | `http://<飞牛IP>:3065/v1` | 任意 OpenAI 兼容客户端 | `Authorization: Bearer <网关密钥>` |

### 1.1 为什么还要 fngateway 这一层

上游 `agent2api-server` 只监听 TCP 端口，不认 Unix socket，而飞牛统一网关要求
应用监听 `${TRIM_APPDEST}/<appname>.sock`。有两条路：改上游源码加 socket 支持，
或者在外面套一层自己持有 socket。

这里选了后者，理由是**上游源码零改动** —— 上游发布节奏很快（v2.7 → v2.9 期间
密集迭代，v3.1.0 又是一次面板大改），一旦本地打了 patch，每次跟进上游都要重放
冲突。`fngateway` 反过来把上游当黑盒：它自己监听 socket，再代理到内核分配的回环
端口上的子进程。

除了 socket，`fngateway` 还负责三件上游不管的事：

1. **网关密钥注入。** 上游的管理面板有自己的登录体系（注册管理员 + 会话
   Cookie）。飞牛这边不希望用户在应用里再注册一套账号，所以由 `fngateway`
   在 `${TRIM_PKGVAR}/apikey` 生成 32 字节随机密钥（0600），通过
   `AGENT2API_PROXY_API_KEY` 传给子进程。密钥进了 `active_api_keys()`，
   面板的登录闸门就**永远不启用**，也就**从不下发 Cookie** —— 没有 Cookie
   就没有「飞牛网关剥离 `Set-Cookie` 导致登录后掉线」那类问题。
   密钥只存在于服务端，浏览器拿不到。
2. **面板子路径改写。** 见 §1.2。
3. **生命周期与日志。** 轮转日志、优雅停机（转发 SIGTERM，等子进程退出）、
   清掉残留 socket。

### 1.2 面板的子路径适配（为什么 agent2api 比 cli2api 省事）

飞牛把应用挂在 `/app/agent2api` 下，而上游面板是**按根路径 `/` 写的**：
`fetch('/api/panel/status')`、`href="css/tokens.css"`。不改写就会全部 404。

这里有一条关键约束：Chromium 里 `Location` 的 `pathname` 等属性是
`[LegacyUnforgeable]`，**运行期 JS 无法覆盖 `location.pathname`**。所以纯前端
方案走不通，必须在服务端把响应改写掉。`fngateway` 做的是：

- **读方向**：剥掉 `/app/agent2api` 前缀再转给子进程；把响应里的
  `Location` 补回前缀；把 `Set-Cookie` 的 `Path` 从 `/` 收窄到
  `/app/agent2api/`（子路径迁移），否则 Cookie 越界且会被同域其它应用覆盖。
- **写方向**：HTML 里 `src` / `href` / `action` / `poster` 的**裸相对路径**
  改写成带前缀；紧随 `<head>` 之后注入 `<base href="/app/agent2api/">` 与两段
  脚本 —— 一段把面板要的密钥种进 `localStorage['agent2api.webKey']`，一段补
  `fetch`/XHR/`WebSocket` 等运行时 API 的前缀。

**为什么还要 `<base>`（这一条是踩出来的）。** 桌面入口的 `url` 是
`/app/agent2api`，**不带尾斜杠**，于是文档 URL 就是
`https://host/app/agent2api`。这种文档 URL 下，任何**裸相对引用**都会被浏览器
按「上一级目录」解析：

```
new URL('assets/providers/workbuddy.png', 'https://host/app/agent2api')
  → https://host/app/assets/providers/workbuddy.png     ← agent2api 这一段被吃掉了
```

`RewriteHTML` 只改写**静态 HTML 里**的引用，救不了这些引用 —— 它们全部是运行期
由面板 JS 拼出来的：`preset-providers.js` 的 `iconOf()` 返回
`assets/providers/<file>.png`，反代列表的图标则编译在 `islands/ui.js` 的
`PROVIDER_ICONS` 里。表现就是**「添加账号 → 反代」里各个模型的图标全部不显示**
（只剩首字母兜底徽章）。

修法是双保险，两处都在 `fngateway` 内、不碰上游产物：

1. 响应里注入 `<base href="{前缀}/">`，把文档基址钉回前缀之下 ——
   与 `fnos-logmanager`（`ServeIndexWithBase`）、`nas`（`injectPrefix`）、
   `deepseek.harness`（`fnGatewayBridgeScript`）四处做法一致。
2. 桥接脚本解析相对引用时用 `P + '/'` 作基准（`RESOLVE_BASE`），
   而不是 `window.location.href`。**这一步不是可有可无的防御**：加了 `<base>`
   之后浏览器已经能正确解析裸相对引用，但桥接脚本仍会把这个 URL 按无斜杠的
   文档 URL 解析、再补一次前缀，亲手把它改成 `/app/agent2api/app/assets/...`
   的双重前缀 404。两者必须一起改。

> 这个缺陷在本地 `devcheck` 里**看不出来**：`devcheck` 打印的地址带尾斜杠
> （`…/app/agent2api/`），带斜杠的文档 URL 解析是正常的。只有模拟飞牛桌面入口
> 那种不带斜杠的地址才会复现。`devcheck` 现在会把这一点打在日志里。

之所以说 agent2api 比 cli2api 省事：上游面板**没有客户端路由**（全仓库
`ui/` 下 `location.pathname` / `pushState` / `basename` 出现 0 次，翻页是内存里
一个 `currentPage` 变量），所以不需要像 cli2api 那样在压缩产物里做正则锚点
打补丁、也不怕上游改压缩方式导致锚点失配。

**v3.1.0 面板带来的三处新适配面（均在网关侧处理，未改上游产物）：**

| 新形态 | 位置 | 网关侧要处理的 |
| --- | --- | --- |
| 多语言 | `i18n.js` + `i18n/{en,ja,ko,pt-BR,zh-Hant}/*.js`（每语言 19 个文件），由 `index.html` 在 head 里动态注入 | 语言包走裸相对路径加载，依赖 §1.2 的 `<base>`；缺一个语言只在那种语言下白屏 |
| 手机端入口 | `m.html` / `m/`（`mobile.js` / `mobile.css` / `manifest.webmanifest`），桌面面板 head 里有 `<link rel="alternate" href="/m">` | 该 `<link>` 是**根绝对路径**，是 `index.html` 里唯一一处 —— `build.py` 的 `ROOT_ABS_ALLOWED` 白名单只放行 `/m` 并补前缀 |
| PWA | `sw.js`、`manifest.webmanifest`、`assets/app-icon-*.png` | 已适配（两侧各自解决），见下方说明 |

> **PWA / 子路径 scope：已适配（两侧分工，均已实测）。**
> 上游把 `manifest.webmanifest` 的 `start_url` / `scope` 写成 `/`，`m/mobile.js`
> 里也写死了 `register("sw.js", { scope: "/" })` —— 都是按「面板挂在站点根」写的。
> 经 `/app/agent2api` 子路径部署时，这会让 PWA 指向站点根、并让 Service Worker
> 注册抛 `SecurityError`（实测：`The path of the provided scope ('/') is not
> under the max scope allowed ('/app/agent2api/')`）。
>
> 两侧分工：
> 1. **`manifest.webmanifest` 正文** —— 由 `fngateway` 的 `RewriteManifest()`
>    按 `Content-Type: application/manifest+json` 识别后做**字段级**改写
>    （`start_url` / `scope` / `id` / `icons[].src` / `shortcuts[].url` …）。
>    只改以单个 `/` 开头的站内绝对路径；**相对路径刻意不动** —— manifest 里的
>    相对 URL 是相对 manifest 自身 URL 解析的，本来就在 `/app/agent2api/` 下，
>    补前缀反而错位。实测：`start_url`/`scope` → `/app/agent2api/`；
>    `/m/manifest.webmanifest` 的 `id`、`start_url`、shortcuts 均正确带前缀，
>    且 `#fragment` 保形。
> 2. **Service Worker 的注册 scope** —— 由 `build.py` 的 `patch_sw_scope()`
>    在同步面板时把 `scope: "/"` 改成 `scope: "./"`（解析成脚本所在目录，
>    即 `/app/agent2api/`），并配 `verify_sw_scope()` 守卫：scope 一旦退回 `/`，
>    `--check` 直接失败并给出修复指引。这样改是安全的：上游 `sw.js` 的预缓存
>    清单 `SHELL` 全部是 `./` 相对路径，本来就是按「可藏在子路径下」写的。
>
> ⚠️ 一条**不要走错的路**：给 `sw.js` 响应加 `Service-Worker-Allowed` 头
> **并不能**救这个问题。该头只能把 scope 上限放宽到「头里给的那个值」，
> 对「scope 必须在脚本目录之下」这条硬约束不起作用 —— 实测带
> `Service-Worker-Allowed: /app/agent2api/` 时，`scope: "/"` 仍被浏览器拒绝。
> 真正的解法只有让 scope 用相对值。
>
> 面板主体（桌面端）不受影响：上游 `sw.js` 的 fetch 处理只认 `/m` 与 `/m/*`。

**上游 WebUI 的形态被钉住了。** `build.py` 记着上游面板的确定性树哈希
（`UPSTREAM_UI_SHA256`，v3.2.0-beta.2 = 213 个文件），上游一旦改动面板就构建期失败，
逼人工确认「形态是否还是 fngateway 能改写的那一种」。同理，
`check_webui()` 会拒绝出现根绝对路径资源引用的 `index.html`（白名单见上表）。

> 校验对象是**官方包解包目录**（`.local-build/upstream-bundle-<arch>/ui/`）里的面板，
> **不是仓库副本** —— 面板已不进仓库（见 §9.4.8）；且 `verify_webui_provenance()`
> 现在**逐架构各校一次**（amd64 / arm64 各一行日志）。

> ⚠️ **哈希口径**：这个 pin 钉的是**打完 `patch_sw_scope` 之后**的面板树哈希，
> 不是上游原始 `ui/` 的哈希 —— 注入/拷贝面板时会先调 `patch_sw_scope()` 把
> `m/mobile.js` 的 `scope:"/"` 改成 `scope:"./"`（**+1 字节**），
> 而 `verify_webui_provenance()` 算的是补丁后的树。v3.1.0 就是这样（原始包 457,837 B
> → 补丁后 457,838 B），v3.2.0-beta.2 同样如此（544,898 B → 544,899 B）。
>
> 自 2026-10-20 起校验对象已不是「仓库副本」而是**官方包解包目录**里的面板，
> 并且 `verify_webui_provenance()` 对 **amd64 与 arm64 各校一次**（两次日志）。

### 1.3 面板展示的「接口地址」为什么必须纠正（2026-10-25）

面板（上游 v3.3.0-beta.3）的「接口地址」**不是**读服务端配置，而是运行时用**页面 origin** 拼出来的：

- 桌面 `ui/islands/ui.js`：`DJ()` 在 web 平台恒返回 `window.location.origin`；
- 手机 `ui/m/mobile.js`：非 Capacitor 原生时 `ts()` 返回 `{ base: location.origin }`，另有 `qn()` 只剥掉 `http://`。

而飞牛的应用面板**只能经统一网关打开**（`http://＜飞牛IP＞:4398/app/agent2api/`），
浏览器里算出的 origin 必然是 `http://＜飞牛IP＞:4398` → 文档页显示 `http://＜飞牛IP＞:4398/v1`。
这个地址给下游客户端**连不通**：4398 是 trim nginx 网关，根路径 `/v1/models` 落到的是飞牛影音 SPA。

实测对照（同一台机器，2026-10-25）：

| 地址 | 结果 |
| --- | --- |
| `http://＜飞牛IP＞:4398/v1/models` | 200，但 body 是飞牛影音 SPA 的 HTML |
| `http://＜飞牛IP＞:3065/v1/models` | 200 JSON，**102 个模型** |

所以 fngateway 在注入的桥接脚本里把这串**展示文本**改写成 `http://＜飞牛IP＞:3065/v1`
（端口取应用自身的 `service_port`，即 `BridgeScript(prefix, apiPort)` 的 `AP`）：
**只改给人看的字符串，不动 `location.origin`** —— 面板自身对 `/api/*` 的请求仍走网关同源代理，
请求侧行为完全不变。改写只针对「本页 origin + `/v1`」这一种形态，因此外部提供商地址
（`https://api.openai.com/v1/...`）与侧栏「网关运行中 · `host:port`」一行都不受影响。

---

## 2. 目录结构

```
fnos-agent2api/
├── manifest                    # 应用元信息（appname/version/service_port/入口）
├── build.py                    # 打包脚本（本地与 CI 共用）
├── LICENSE                     # 本项目（适配层）的许可
├── LICENSE.upstream            # 上游 agent2api 的完整许可（v3.1.0 原文；v3.2.0-beta.2 的 LICENSE 未获取到，见 §9.4.4）
├── ICON.PNG / ICON_256.PNG     # 桌面图标（64 / 256）
├── .github/workflows/
│   ├── check-upstream.yml      # 每天查上游；有新版就改 pin+manifest 并派发构建
│   └── build-and-release.yml   # 取上游官方包 + 出 fpk + 发 Release
├── config/
│   ├── privilege               # run-as=package, username/groupname=agent2api
│   └── resource                # {}
├── cmd/                        # 飞牛生命周期脚本（必须 LF + 可执行位）
│   ├── install_init / install_callback
│   ├── upgrade_init / upgrade_callback
│   ├── config_init  / config_callback
│   ├── uninstall_init / uninstall_callback
│   └── main                    # start / stop / status
├── wizard/                     # install / upgrade / uninstall / config 向导
├── fngateway/                  # Go 适配层（纯标准库，CGO_ENABLED=0）
│   ├── main.go                 # socket、密钥、子进程、日志、信号
│   ├── rotate.go               # 轮转日志 writer
│   ├── build.ps1               # 本地交叉编译（Windows 直出 linux 双架构）
│   ├── internal/gateway/
│   │   ├── gateway.go          # 前缀剥离/回填、HTML 改写、Cookie Path 改写
│   │   ├── bridge.go           # 注入面板的 seed + 运行时 API 补丁脚本
│   │   └── gate.go             # 管理员闸门
│   └── *_test.go               # 37 个测试
├── app/ui/                     # 只含本项目桌面入口：config + images/
│                               #   （上游面板 213 文件不入库，构建时从官方包注入）
├── tools/                      # 本地开发辅助脚本（不参与打包）
│   ├── make_icons.py           # 从一张主图生成四个图标文件
│   ├── ui_hash.py              # 算/核对上游 WebUI 树哈希
│   ├── bump_upstream.py        # 查上游新版本 → 改四处 pin + manifest（CI 定时用）
│   ├── inspect_fpk.py          # 独立复核生成的 fpk（与 build.py 不同的代码路径）
│   ├── check_workflow.py       # 静态校验 CI workflow（needs/runs-on/artifact/cron/语法）
│   ├── check_bump_reload.py    # 自检：改完 build.py 后 load_build() 必须读到新常量
│   └── check_text.py           # 编码/换行/可执行位检查
└── .local-build/               # 中间产物（gitignore）
    ├── bin/                    # 放 4 个 Linux 二进制（2 个上游 + 2 个 fngateway）
    ├── tools/fnpack(.exe)
    ├── upstream-pkg/           # 解包后的上游官方 Linux 服务端包
    └── upstream-src/           # --sync-upstream 拉的上游源码
```

---

## 3. 构建

### 3.1 前置条件

**本机不需要 Rust 工具链，也不需要 WSL / Docker。** v3.1.0 起上游**自己发布官方
Linux 服务端包**（Release 资产 `agent2api-server-linux-<arch>.tar.gz` + 同名
`.sha256`，包内含 `agent2api-server` 二进制、`ui/` 面板、`install.sh`、
`agent2api.service`、`agent2api.env.example`），正好是本项目需要的两样东西，所以
**不再在 CI 里交叉编译 Rust**（v2.9 时代那条 `cargo build --release --locked
-p agent2api-server` + `aarch64-linux-gnu-gcc` + Rust 缓存的路子已经删掉）。

上游包本身的 sha256 在 `build.py` 里**钉死**（`UPSTREAM_BUNDLE_SHA256`），并把
上游发的 `.sha256` 资产当第二意见交叉核对：上游资产被重新上传时，构建期立刻硬
失败，而不是静默换掉二进制。

> `python build.py --check` 实测（本地，2026-10-18）：
> `agent2api-server-linux-amd64` 13,751,728 B、`agent2api-server-linux-arm64`
> 12,086,056 B，两者 ELF 架构正确。**上游二进制引用的最高 GLIBC 符号版本是
> 2.34**，即它要求 **glibc ≥ 2.34**；飞牛 fnOS 1.2.x（Debian 12）为 **2.36**，
> 满足该要求（2.34 是上游的门槛，不是飞牛的下限）。
> `readelf` 的 `NEEDED` 列表与 `RUNPATH` 结论仍只有 v2.9 时代的（见 §9），
> v3.1.0 未复核。

本机只需要：

| 用途 | 依赖 |
| --- | --- |
| `fngateway` 交叉编译 | Go ≥ 1.27（纯标准库，`CGO_ENABLED=0` 即静态） |
| 打包 | Python ≥ 3.8 |
| 打包 | `fnpack`（`build.py` 会自动下载到 `.local-build/tools/`） |

```powershell
# 编 fngateway 的 linux 双架构（Windows 上直接出 ELF）
pwsh -File fngateway\build.ps1 -Version 3.2.0-beta.2-1
# 产物：.local-build\bin\fngateway-linux-{amd64,arm64}
```

### 3.2 命令

```powershell
# 全量预检（不联网、不打包）：图标 / WebUI / 溯源 / 契约 / 二进制
python build.py --check

# 打包（amd64 + arm64 各一个 fpk；需 .local-build/bin 里已有 4 个二进制）
python build.py
python build.py --arch arm64        # 只出 arm64
python build.py --version 3.2.0-beta.2-1   # 覆盖版本号

# 维护：解包上游官方包并核对面板溯源（不写工作区）；显式落盘到 app/ui 做人工 diff 才加 --materialize-ui
python build.py --sync-upstream
python build.py --sync-upstream --force-upstream   # 忽略缓存重下

# 维护：查上游新版本并改齐 pin/manifest（CI 定时跑的同一个脚本，见 §3.5）
python tools\bump_upstream.py --detect             # 只看，不改文件
python tools\bump_upstream.py --apply              # 有新版就改到位
```

`--sync-upstream` 现在**只解包**（不再写工作区）：把官方包解到
`.local-build/upstream-bundle-<arch>/` 并核对面板溯源，不继续打包。需要把它落到
`app/ui` 做人工 diff 时，才加 `--materialize-ui`。

> **面板不需要进仓库。** 离线打包靠 `.local-build/` 里的官方包缓存
> （`python build.py --fetch-binaries` 落盘，sha256 已在 `UPSTREAM_BUNDLE_SHA256` 里钉死），
> 构建时由 `stage_ui()` 注入面板。

### 3.3 CI 构建

两条 workflow，分工明确：

| workflow | 触发 | 干什么 |
| --- | --- | --- |
| [`check-upstream.yml`](.github/workflows/check-upstream.yml) | 每天 03:23 UTC 定时（也可手动） | 查上游有没有新版；有就改 pin+manifest、提交 `main`、打 tag、派发构建 |
| [`build-and-release.yml`](.github/workflows/build-and-release.yml) | `v*` tag / 被派发 / 手动 | 取上游官方 Linux 服务端包（校验 sha256）→ 编 fngateway → 出 fpk → 发 Release（含 `SHA256SUMS.txt`） |

**自动升级（默认路径）**：上游发新版后，最迟一周（下个周一），`check-upstream.yml` 会
自动走完「改四处 pin → 同步面板 → 提交 → 打 tag → 构建 → 发 Release」。
平时无事发生时它只是一次几秒的空转，不会重复发版（幂等：提交后 pin 已等于
上游最新版，下次判定「无更新」直接跳过）。

**为什么是「派发」而不是「push tag 自动触发」**：GitHub 有一条防递归规则 ——
用内置 `GITHUB_TOKEN` 推的 tag **不会触发**其它 workflow 运行，只有
`workflow_dispatch` / `repository_dispatch` 两个事件例外。所以如果让定时任务
去 push tag 等构建自己开跑，tag 会推上去但构建永远不开始；而那时 pin 已经
提交进 `main`，下次检查会判定「无更新」，这个版本就**永远不会有 Release**。
改成显式 `gh workflow run --ref <tag>` 派发，构建逻辑仍然只有一份。

**手动跑 `build-and-release.yml`**：

- `ref` 选 **tag** → 构建 + 发 Release（等价于 tag 触发）
- `ref` 选 **分支** → 只构建，产物走 artifact，不发 Release；
  勾上 `force_release` 才会发（tag 名取 `manifest` 里的版本号，不存在则自动创建）
- 重发某个版本：`ref` 填对应 tag 即可（同名 Release 会被替换，不会报 already exists）

**为什么还留 `ubuntu-22.04` 做二进制相关的 job** —— glibc 兼容性：

| runner | glibc | 结论 |
| --- | --- | --- |
| `ubuntu-22.04` | 2.35 | ✅ 低于飞牛的 2.36，安全 |
| `ubuntu-24.04` | 2.39 | ❌ 可能报 `version 'GLIBC_2.38' not found` |

二进制在哪个 glibc 上编，就要求目标机 glibc ≥ 那个版本。飞牛 fnOS 1.2.x 基于
Debian 12 (bookworm) = glibc 2.36。这个坑**不会在构建期报错，只在真机启动时
炸**，所以 workflow 里有一道 `readelf`/`objdump` 断言，把「runner 镜像悄悄
升级」这种漂移变成构建期硬失败。注意 v3.1.0 起**上游二进制不再由本项目编译**，
这道断言现在守的是「上游发的包实际要求的 glibc 别超过 2.36」—— 上游在哪个
glibc 上编不受本仓库控制，所以这道检查比 v2.9 时代更重要。
（`check-upstream.yml` 只跑 Python 与 git，不产二进制，所以没有这个约束，
跑在 `ubuntu-24.04` 上无所谓。）

编出来的二进制要落到 `.local-build/bin/` 才能打包，命名固定：

```
fngateway-linux-amd64           fngateway-linux-arm64
agent2api-server-linux-amd64    agent2api-server-linux-arm64
```

其中两个 `agent2api-server` 由官方包解出（包内原名就是 `agent2api-server`，
按架构改名）。从 CI 的 artifact 下载后放进该目录，`python build.py` 即可离线打包。

**fpk 不是逐字节可复现的** —— `fnpack` 自己写 tar 元数据（mtime/uid），gzip 层
也带时间，包内 `manifest` 的 `checksum` 字段随之改变。所以本地重打同一个
commit 与 CI 的产物哈希天然对不上，**这不是 bug**。已逐文件比对确认：
v3.2.0-beta.2 的包内 `app.tgz` 共 **220 个文件**（其中 `app/ui/` **216 个** = 上游
**213** 个 + 本项目 `config` + `images/icon_64.png` + `images/icon_256.png`；
其余 4 个为 `bin/` 两个二进制与 `config/privilege`、`config/resource`）。
v3.1.0 时该组计数是 `app/ui` 211 个（上游 208）、`app.tgz` 215 个；v2.9 时代是
78 个，差异同样只应出现在打包层。`UPSTREAM.txt` 的构建时间戳尊重 `SOURCE_DATE_EPOCH`（CI 喂的是
提交时间），所以同一 commit 重跑时那一行不再漂。

### 3.4 构建期守卫

都是「编译能过、真机才炸」的静默缺陷，所以做成硬失败：

| 守卫 | 防的是什么 |
| --- | --- |
| `check_text_hygiene` | CRLF 破坏 `cmd/*` 的 shebang；BOM 让 fnpack 解析出带 `\ufeff` 的键名而静默失配 |
| `check_manifest_values` | fnpack 把 manifest 取值里的 `;` 当终止符，之后内容**静默丢弃** |
| `verify_icons` | 图标缺失/尺寸错 → 桌面图标空白 |
| `check_webui`（图标） | `ui/assets/providers/*.png` 缺失或索引引用的图标不存在 →「添加账号」里模型图标空白。**两个索引写法不同**（反代在 `islands/ui.js` 里写完整路径 `assets/providers/x.png`；预置在 `preset-providers.js` 里只写 `icon: 'x.png'`，由 `iconOf()` 运行期拼目录），故配了两条正则 —— 实测只配一条时预置图标会全部漏检（v2.9 是 20 个；v3.1.0 磁盘 41 / 预置声明 24；**v3.2.0-beta.2 磁盘 44 / 预置声明 24 / 反代 21 / 合计 44 / 缺失 0**，这些计数均已实测） |
| `check_webui` | 上游 WebUI 缺文件（面板白屏）或资源引用变成根绝对路径（改写失效）。`UI_REQUIRED` 里每一项都对应 v3.1.0 的一个新形态：`i18n.js` + `i18n/manifest.js` + `i18n/pt-BR/common.js`、`m.html` / `m/mobile.js` / `m/mobile.css`、`manifest.webmanifest`、`sw.js`、`css/mobile.css`。根绝对路径白名单只放行 `/m`。**v3.2.0-beta.2 复核结果：16 项全在；`index.html` 52 个资源引用、根绝对路径仍只有 `/m` 一个、未登记 0 个；SW 注册仍是 `register("sw.js",{scope:"/"})`；i18n 仍 5 语言 × 19 词典，无 `zh-CN` 目录** |
| `verify_webui_provenance` | 面板形态漂移（上游改了 HTML/localStorage 键/fetch 形态） |
| `verify_upstream_version` | tag 被移动 / 镜像给了旧包 → 版本号对不上（v3.1.0 起从源码里 `desktop-tauri/src-tauri/server/Cargo.toml` 断言） |
| 官方包 sha256 | 上游资产被重新上传/被中间人替换 → `UPSTREAM_BUNDLE_SHA256` 与上游 `.sha256` 两次独立校验都不放行 |
| `check_consistency` | 前缀 / socket / 端口 / 入口 / 运行用户在四处文件里不一致 |
| `check_binaries` | 二进制缺失、不是 ELF、架构不符 |
| `_gateway_freshness` | `.local-build/bin/fngateway-linux-<arch>` 是**旧版**（改了桥接脚本却没重编）→ 包里带着老网关上架。按产物字节里的标记判定：`<base href=`（图标反代修复）、`RESOLVE_BASE`（模型图标反代修复）、`apiText`（面板「接口地址」端口纠正）；缺任一即拒绝复用缓存二进制 |
| CI `Verify binaries` | 二进制架构不符、要求的 glibc 超过飞牛的 2.36、**以及替身二进制**（无 `rustc` 指纹或体积 < 8 MB） |
| `verify_fpk` | 打包后重新解包核对：ELF 架构、可执行位、`cmd/*` 权限、包内 manifest 逐值一致 |
| `check_workflow.py` | workflow 静态错误：`needs` 指向不存在的 job、缺 `runs-on`、引用不存在的脚本、下载了本 workflow 没上传的 artifact、非法 cron、未声明的 `inputs.X`、`uses` 缺版本号或跨文件版本不一致、`bash -n` 语法错误、派发目标不可手动触发 |
| `check_bump_reload.py` | `bump_upstream.py` 改完 `build.py` 后读到**旧**常量。CPython 判定 `.pyc` 新鲜只看源文件 **mtime 整秒 + 字节数**，所以只要改写后字节数不变且发生在同一整秒内，就会复用旧字节码 —— 当时表现为第 3 步刚打印新树哈希、第 4 步却报「树哈希与钉住的值不一致」，且只在 CI 偶发。自检把该时序确定性地造出来：三处 pin 一律走**逐字符等长替换**（版本号 `3.2.0-beta.2` → `9.9.9-zzzz.9`，两个 sha256 → 同长的 `a1a1…` / `b2b2…`），并断言 `load_build()` 读到的是新值 |

`fngateway` 自身还有 37 个 Go 测试：

```powershell
cd fngateway; go vet ./...; go test ./...
```

独立复核（不依赖 `build.py` 的自检，重新解包逐项检查）：

```powershell
python tools\inspect_fpk.py
python tools\check_workflow.py
python tools\check_bump_reload.py
python tools\check_text.py
```

### 3.5 换图标 / 升级上游版本

**换图标**：把主图（建议 1024×1024 PNG）放到任意位置，然后

```powershell
python tools\make_icons.py 主图.png
```

它会按中心方形裁剪 + LANCZOS 缩放，写出 `ICON.PNG`、`ICON_256.PNG`、
`app/ui/images/icon_64.png`、`app/ui/images/icon_256.png` 四个文件。

**升级上游**：正常情况下**不用手动做** —— `check-upstream.yml` 每天自动查、
自动改、自动发包（见 §3.3）。需要立刻升级或想先在本地看一眼时：

```powershell
# 只看上游有没有新版，不改任何文件（只读，安全）
python tools\bump_upstream.py --detect

# 有新版就改到位：build.py 的 pin（含官方包 sha256）+ manifest 的 version/changelog
python tools\bump_upstream.py --apply

# 上游已是最新时也重打一次当前版本（验证流水线用）
python tools\bump_upstream.py --apply --force

# 指定版本（比如上游最新是 3.2.0，想先跳到 3.1.5 验证）
python tools\bump_upstream.py --apply --upstream-version 3.1.5
```

脚本会**一次性改齐四处耦合值**，漏改任何一处都会让 `build.py` 硬失败：

| 位置 | 内容 |
| --- | --- |
| `build.py` `UPSTREAM_VERSION` | 驱动 `UPSTREAM_TAG`、源码 URL 与官方包 URL |
| `build.py` `UPSTREAM_BUNDLE_SHA256` | 上游官方 Linux 服务端包的 sha256（每架构一个） |
| `build.py` `UPSTREAM_UI_SHA256` | 面板树哈希（**打完 `patch_sw_scope` 之后**的树，对象是官方包解包目录，逐架构各校一次；含注释里的文件数，v3.1.0 为 208，v3.2.0-beta.2 为 213） |
| `manifest` `version` | 格式 `x.y.z-N` |
| `manifest` `changelog` | 覆盖为最新一版（只留本版说明，不累加旧条目） |

改完还会自己跑一遍不需要二进制的构建期守卫（卫生/图标/面板/契约），
不过就非零退出、不留半成品。若上游改了面板，官方包解包目录里的面板
会变，树哈希随之变化 —— 这一步是自动的，不再需要人工抄哈希（面板本身不进仓库）。

> 只跑 `python build.py --sync-upstream` 仍然可用：它只做「解包官方包
> 并核对溯源」，不碰 pin 和 manifest，**也不写工作区**。需要落盘到 `app/ui`
> 做人工 diff 时加 `--materialize-ui`。面板不在仓库里，离线打包靠
> `.local-build/` 的官方包缓存即可出完整包。

> **换仓库这件事没有自动化。** `bump_upstream.py` 只在**同一个上游仓库内**
> 跟版本；仓库迁移（这次从上游旧地址换到 `Agent2API/agent2api`）是
> 人工判断 + 手工改 `UPSTREAM_REPO`，因为「旧仓库 404、新仓库只有新 tag」
> 这种情况脚本无法自己分辨是迁移还是上游挂了。

---

## 4. 安装与升级

### 4.1 下载现成的包（推荐）

已构建好的包挂在 Release 上，按机器架构选一个：

- <https://github.com/sushazhi/fnos-agent2api/releases/latest>

| 架构 | 适用机型 | 包大小（实测） | v2.9 对照 |
| --- | --- | --- | --- |
| `amd64` | x86_64 机型 | **12,563,435 B ≈ 12.0 MB** | ≈ 8.5 MB |
| `arm64` | 飞牛 ARM 机型（如瑞芯微/ARM 主板） | **11,812,003 B ≈ 11.3 MB** | ≈ 8.0 MB |

> fpk 比 v2.9 涨了约 3.5~4 MB，主要是面板从 71 个文件增至 213 个
> （含 i18n 五个语言目录共 95 个文件、`m/mobile.js` 单文件 533 KB）。

下载后校验（可选，但建议）：

```bash
sha256sum -c SHA256SUMS.txt
```

### 4.2 安装

把 `agent2api-<版本>-<arch>.fpk` 装进飞牛应用中心即可。安装向导会说明提供商
范围、下游端口地址与凭据风险。

命令行管理：

```bash
# 生命周期（TRIM_* 由飞牛注入，需在应用上下文里执行）
./cmd/main start | stop | status      # status: 0=运行中 3=未运行 1=参数错
```

- **升级**：数据（`data/agent2api.db`）与网关密钥（`apikey`）都会保留，
  升级脚本是幂等的，不会重建账号数据。
- **卸载**：向导里有 `wizard_keep_data` 选项，默认**保留**数据。

> **从 v2.9 系列升到当前版本（`3.2.0-beta.2-1`）待验证。** 上游 v3.x 的面板与 API 都有变化；
> 本适配层的数据目录布局（`${TRIM_PKGVAR}/data/agent2api.db`）没变，`apikey`
> 也没变，但**上游 v2.9 / v3.1.0 的数据库结构在 v3.2.0-beta.2 上是否被自动迁移、账号是否需要
> 重新登录，均未实测**。保守做法：升级前备份 `${TRIM_PKGVAR}`（`agent2api.db`
> 与 `-wal` / `-shm` 一起拷）。

---

## 5. 使用

### 5.1 控制台（管理员）

飞牛桌面点应用图标，走统一网关 `/app/agent2api`，**仅管理员可见可访问**
（`allUsers: false`）。面板**不需要单独登录** —— 网关密钥由后端注入，浏览器
侧只拿到一个占位值。

面板里可以：加账号（扫码/设备码登录上游客户端）、管模型、发 API 密钥、看
运行日志与请求报表、定时任务、余额与用量、对话测试台。v3.1.0 起界面支持
简体中文 / 繁體中文 / English / 日本語 / 한국어 / Português (BR) 六种语言，
并带手机端页面（`/m`）。

> **注意**：控制台自带的「检查更新」在飞牛上不可用（没有宿主机 updater
> 进程），属预期降级 —— 升级请用应用中心。

### 5.2 下游接入（外部客户端）

标准 OpenAI 兼容接口在**独立端口**：

```
http://<飞牛IP>:3065/v1
```

网关密钥在 `${TRIM_PKGVAR}/apikey`（root/应用属主可读）：

```bash
cat /var/apps/agent2api/var/apikey
```

客户端配置（以 OpenAI SDK 为例）：

```python
from openai import OpenAI
client = OpenAI(
    base_url="http://192.168.0.2:3065/v1",
    api_key="<apikey 内容>",
)
```

> **为什么不把外部客户端指到 `/app/agent2api`？** 飞牛 1.2.0604+ 会拦截非
> 飞牛 ticket 的 `Authorization` 头，外部客户端的 `Bearer` 会被吃掉。统一网关
> 那条路径只给桌面面板用。

### 5.3 数据与日志位置

全部在 `${TRIM_PKGVAR}`（即 `/var/apps/agent2api/var/`）：

| 路径 | 内容 |
| --- | --- |
| `data/agent2api.db` | 账号、日志、请求统计、调试报文、脱敏词表、网关配置（SQLite） |
| `data/agent2api.db-wal` / `-shm` | WAL 附属文件，**备份时要一起拷** |
| `apikey` | 网关密钥（0600，首次启动自动生成） |
| `home/` | 子进程的 `HOME`（0700） |
| `panel.log` | `fngateway` 自己的日志（8 MB 轮转） |
| `agent2api.log` | 上游子进程的 stdout/stderr（8 MB 轮转） |
| `main.log` | 飞牛生命周期脚本日志 |

---

## 6. 权限与安全设计

- **最小权限**：`config/privilege` 声明 `run-as=package`，以 `agent2api`
  用户/组运行，不用 root。
- **网关密钥只走服务端**：`fngateway` 生成 → 环境变量传子进程 → 面板里只显示
  掩码。密钥从不下发给浏览器，所以「前端能看到密钥」这条路不存在。
- **无面板登录 = 无 Cookie**：密钥进了 `active_api_keys()`，面板登录闸门不启用，
  全程不下发 `Set-Cookie`。即便将来启用面板登录，`fngateway` 的
  `RewriteCookie` 也已把 `Path` 收窄到应用子路径。
- **socket 权限最小化**：socket 建在 `${TRIM_APPDEST}`，启动前与停止时都清理
  残留（残留会让新进程 bind 失败）。
- **下游端口绑定所有网卡**：`agent2api-server` 监听 `0.0.0.0:3065`（不是
  `127.0.0.1`）—— 只绑回环会让局域网客户端连不上；子进程内部用的回环端口由
  内核分配、不对外暴露。这与上游服务端形态的默认值一致（其
  `agent2api.env.example` 里 `AGENT2API_HOST=0.0.0.0`、`AGENT2API_PROXY_PORT=3065`）。
- **上游配置不外泄**：`fngateway` 刻意**不**设置 `AGENT2API_ADMIN_USER` /
  `AGENT2API_ADMIN_PASSWORD` / `AGENT2API_ALLOW_NO_KEY` / `AGENT2API_PANEL_PORT`，
  避免意外打开面板登录或 fail-open。
- **上游自带的自更新天然关闭**：裸机/fpk 形态**未设置**
  `AGENT2API_SELF_UPDATE`，而上游 `server/core/update/selfupdate.rs` 的
  `enabled()` 是 `std::env::var("AGENT2API_SELF_UPDATE")` 后按
  **严格等于 `"1"`** 判定、读不到即 `false` —— 即 **fail-closed**。所以
  `fngateway` 不需要显式转发 `=0`（它的 `childEnv()` 里也确实没有这个变量），
  效果与上游服务端包 `agent2api.service` 里显式写 `=0` 一致。
  因此面板里的「容器内原地更新」入口**不可用** —— 升级走 fpk 重装（应用中心）。
- **凭据明文**：上游把 accessToken/refreshToken 以**明文**存在
  `data/agent2api.db`。这是上游的既有设计（其 LICENSE 使用声明第 4 条有说明），
  本适配层没有改变它。不要在多人共享的设备上使用，也不要提交/分享该文件。
- **路径安全**：卸载脚本先确认 `DATA_DIR != "/"` 才递归删除。

---

## 7. 与 fnos-cli2api 的关系

两者是同一套飞牛适配思路的两个实例，适配层同源、内核不同：

| | fnos-cli2api | fnos-agent2api（本仓库） |
| --- | --- | --- |
| 上游 | caigee-cmd/cli2api | Agent2API/agent2api（当前随包 v3.2.0-beta.2） |
| 上游形态 | Go 控制面 + Node worker | Rust 单文件二进制（上游官方发 Linux 服务端包） |
| 运行时依赖 | 需要应用中心 Node.js 运行时 | 无（静态链接，仅 glibc） |
| 包体积 | ~95 MB（含 Node 与 CLI bundle） | 面板 213 文件 + 上游二进制 14.35 MB（amd64）/ 12.61 MB（arm64）；fpk **12,563,435 B（amd64）/ 11,812,003 B（arm64）**（v2.9 时代为 ≈ 8.5 MB / ≈ 8.0 MB） |
| 每账号开销 | 一个 Node 进程 + 独立 HOME（Qoder 账号约 511 MB RSS） | 无独立进程 |
| 面板适配 | 需要在压缩产物里做正则锚点打补丁 | 无客户端路由，只需服务端改写（v3.x 另需处理根绝对路径 `/m` 与 PWA scope） |
| 上游漂移敏感度 | 高（钉死 Qoder CLI 版本 + 6 处压缩串） | 低（零源码改动，但面板树哈希仍需钉死） |
| 账号凭据 | 应用数据目录 | 应用数据目录（明文，上游设计） |

选 agent2api 的直接收益是**运行时依赖与内存**：不再需要 Node 运行时、不再有
每账号一份的 Node 进程，包体积从 ~95 MB 降到十几 MB 量级。

需要留意的是提供商覆盖不同：cli2api 在你这台环境里实测通过的是 Qoder
Intl/CN + WorkBuddy Intl/CN + Trae CN；agent2api v3.1.0 时内置 17 家提供商（清单
见文首）并支持自定义提供商。**v2.9 时代验证过的登录链路**是：WorkBuddy、
Qoder、Cline 走设备码轮询登录，在只有浏览器的场景下最顺；AutoClaw / CatPaw 走
loopback 回调（要求浏览器与网关同机）；小浣熊用自定义 scheme
`office-raccoon://` 回调，只能靠粘贴凭据。**v3.1.0 / v3.2.0-beta.2 是否仍如此均未验证**
—— 上游 README 新增了「智谱清言访客模式」等登录形态，各家的回调方式需要逐个
复验。

---

## 8. 上游与许可

本仓库自己的代码（`fngateway/`、`cmd/`、`config/`、`wizard/`、`manifest`、
`build.py`、图标与桌面入口）以 **MIT** 发布，见 [`LICENSE`](LICENSE)。

上游 agent2api 有**自己的许可**，随仓库提交在
[`LICENSE.upstream`](LICENSE.upstream)，并会被 `build.py` 原样放进 fpk 包内：
MIT 条款正文**外加**一份「使用声明（Usage Notice）」，其中第 3 条禁止商业用途
与二次分发牟利。

`LICENSE.upstream` 是上游 **v3.1.0** `LICENSE` 的逐字节副本（5,434 B，sha256
`5869d5ab03a0130bc33c484bbfa8f17dc37f4ad4f07d243879e0eb2fbab4c832`），未做任何
改写 —— 包括其中提到的上游 `desktop-tauri/src-tauri/Cargo.toml` 用
`license-file` 指向该文件这件事，也是上游原文。

完整授权 = MIT 条款 + 使用声明，两者冲突时以更严格的一方为准。就本项目而言：
使用声明第 3 条明确「个人学习、研究、自用，以及不以牟利为目的的分享与交流，
不受本条限制」，所以自用打包与分发不受该条约束。

上游：<https://github.com/Agent2API/agent2api>（`Copyright (c) 2026 Agent2API`），
本包对应 tag [`v3.2.0-beta.2`](https://github.com/Agent2API/agent2api/releases/tag/v3.2.0-beta.2)
（**tag 写法为推断**：该仓库既有 tag 是 `v3.1.0` 这种 `v` 前缀形态，但
`v3.2.0-beta.2` 这个 tag 本身**无法核实** —— 仓库已于 2026-10-20 变为 404，见 §9.4）。

---

## 9. 已验证 / 未验证

> 本节按「跟着哪一版上游」分开写，v2.9 时代的结论**不自动适用于 v3.x**。
> §9.1 **描述的是 2026-10-18 那一轮（v3.1.0）**，保留作为历史记录；
> 当前随包版本（v3.2.0-beta.2）的实测结论在 **§9.4**。

### 9.1 v3.1.0 / 本轮迁移已验证（2026-10-18）【历史记录，非当前版本】

- 旧上游地址的 API 与 codeload 均为 **404**（换仓库的直接原因）
- 上游 Releases/tags/v3.1.0 存在，`published_at` = 2026-10-18T11:28:19Z，非
  pre-release，共 11 个资产
- 上游 v3.1.0 源码 tag 的 `desktop-tauri/ui/` 共 **208 个文件**，
  `assets/providers/*.png` 共 **41 个**（v2.9 为 30 个）；41 个图标全部被面板
  引用（预置列表声明 24 个、反代索引引用 18 个，二者无交集遗漏，磁盘 − 引用 = 0，
  引用 − 磁盘 = 0）
- i18n 目录实测为 `en` / `ja` / `ko` / `pt-BR` / `zh-Hant` 五种**非简体**语言包
  （每种 19 个文件），加上简体中文（默认语言，文件即根目录下的中文文案）共
  **六种界面语言**。上游 README 原文也是「界面六语」
- 官方 Linux 服务端包 `agent2api-server-linux-amd64.tar.gz` 下载成功，
  8,826,710 B，sha256
  `019d92bf65bf3ba35f5fba7421bfb938031f1eb6b6fd5ba5c4e1174362cb98fb`
  —— 与上游随包发的 `.sha256` 资产、以及 `build.py` 里钉住的值三者完全一致
- 官方包内顶层为 `agent2api-server`（13,751,728 B，`-rwxr-xr-x`）+ `ui/`
  + `install.sh` / `agent2api.service` / `agent2api.env.example`
- 官方包内 `ui/` 与**当时仓库里的** `app/ui/`（排除本项目自己的 `config` / `images`）
  `diff -rq` **零差异**（这是 2026-10-18 的情况；面板自 2026-10-20 起已不入库，见 §9.4.8）
- 上游 v3.1.0 `LICENSE`（raw 与源码 tarball 两处取得）5,434 B，sha256
  `5869d5ab…c832`；`LICENSE.upstream` 与之**逐字节一致**（本轮替换）
- `python build.py --check` 全绿（本地，2026-10-18）：图标尺寸正确；`index.html`
  50 处资源引用、根绝对路径 0 处、未登记 0 处；i18n 5 个语言目录各 19 个文件；
  模型图标「磁盘 41 个，被引用 41 个，缺失 0 个」；`app/ui` 溯源 208 个文件树哈希
  通过；契约一致；四个二进制均为正确架构的 ELF，`agent2api-server` 两个架构
  引用的最高 GLIBC 符号为 2.34（要求 glibc ≥ 2.34，飞牛为 2.36，满足）
- **真机之外的端到端打包已对 amd64 / arm64 双双跑通**（「组装 → `fnpack build`
  → 解包自检」，由 lead 执行；下列计数我用 tarfile 逐项复核过）：
  - `agent2api-3.1.0-1-amd64.fpk` = 12,025,372 B，包内 `manifest` 的
    `platform` = **`x86`**，`bin/` 为 `agent2api-server-linux-amd64` 13,751,728 B
    + `fngateway-linux-amd64` 7,647,392 B
  - `agent2api-3.1.0-1-arm64.fpk` = 11,294,484 B，包内 `platform` = **`arm`**，
    `bin/` 为 `agent2api-server-linux-arm64` 12,086,056 B +
    `fngateway-linux-arm64` 7,078,048 B
  - 两个包共有：包根 9 个条目（`ICON.PNG` / `ICON_256.PNG` / `LICENSE.upstream`
    / `UPSTREAM.txt` / `app.tgz` / `cmd` / `config` / `manifest` / `wizard`）；
    `app.tgz` 215 个文件，其中 `app/ui/` **211 个**（上游 208 + `config` +
    两个 icon），**两个架构的 `app/ui/` 逐文件 sha256 完全相同**；
    包内 `manifest` 的 `version` = 3.1.0-1、changelog 只有 1 条、取值无 ASCII 分号
  - 包内 `bin/agent2api-server-*` 与官方服务端包内二进制等长（amd64 13,751,728 B）
- `manifest` 通过 `build.py` 的 `check_manifest_values`：所有取值无 ASCII 分号；
  `version` = `3.1.0-1` 符合 `x.y.z-N`；`changelog` 只有 v3.1.0-1 一条

### 9.2 v2.9 时代已验证、与版本无关的部分

以下结论来自本轮迁移之前，前提（上游设计、飞牛侧行为、fngateway 代码）未随
上游 v3.x 改变，故仍成立：

- `fngateway`：`go vet` 干净，`go test ./...` 37 个测试全过（含 HTML 改写、
  Cookie Path 改写、密钥生成与复用、前缀剥离/回填），其中修「模型图标不显示」
  时新增 `TestBaseTag`、`TestBridgeScriptResolvesRelativeToPrefix` 与
  `TestProxyHTMLRewriteAndInject` 里的 `<base>` 注入顺序断言
- 图标根因用 Node 实测复现（无尾斜杠文档 URL）：
  `new URL('assets/providers/openai.png','http://host/app/agent2api')`
  → `http://host/app/assets/providers/openai.png`（`agent2api` 段被吃掉），
  加尾斜杠后才落到 `.../app/agent2api/assets/providers/openai.png`
- 各构建期守卫逐个做负例注入（篡改面板字节 / 写错前缀 / manifest 塞分号 /
  删图标 / 抽掉二进制），均如期硬失败
- 自动升级脚本 `tools/bump_upstream.py` 端到端验证：`--detect` 只读、不改文件；
  `--apply` 各步骤全过，改完的 `manifest` 差异精确到只有 `version` 与
  `changelog` 两处
- workflow 静态检查 `tools/check_workflow.py` 做了多项负例注入，全部如期检出
- 陈旧字节码缺陷（见 §3.4 `check_bump_reload.py`）根因定位到 CPython 源码：
  `importlib._bootstrap_external._validate_timestamp_pyc` 只比
  `mtime & 0xFFFFFFFF` 与 `source_size & 0xFFFFFFFF`。等长改写 + 同秒 ⇒ 旧
  `.pyc` 被判定有效。实测 12/12 命中；对照实验里 `importlib.invalidate_caches()`
  单独用 **0/6 生效**，删 `.pyc` 或 `exec(compile(源码))` 均 **6/6 生效**
  （`sys.dont_write_bytecode` 单独用 5/6，故未采用）。
- 打包管线端到端跑通（v2.9 的包）：组装 → `fnpack build` → 解包自检，
  amd64 与 arm64 均通过；`tools/inspect_fpk.py` 独立复核了包内 ELF 架构、
  `cmd/*` 权限、`ui/config` 内容与 `UPSTREAM.txt` 文案

### 9.3 v3.1.0 尚未验证（本次不给结论）【历史记录，v3.1.0 那一轮】

> **打包这一环已全部验证完毕**：amd64 与 arm64 两个包都是「组装 →
> `fnpack build` → 解包自检」端到端跑通的实测状态（数字见 §9.1）。
> 下面列出的都是**打包之外**仍未验证的部分。
- ✅ **fngateway 对 v3.1.0 面板的改写效果已实测**（HTTP 级 + 全量逐字节比对）。
  方法：新上游服务端（Windows 包）在本机 `127.0.0.1` 跑起来、`AGENT2API_UI_DIR`
  指向**当时仓库里的** `app/ui`，前面挂 `devcheck` 复现网关链路。结论：
  · `/app/agent2api/` 200，`<base>`/seed/bridge 注入正常；
  · 面板 142 个 JS/CSS/HTML/JSON 文件经网关后与上游**逐字节一致**（未被误改）；
  · 只有 7 个文件发生变化，且全部是预期改写：3 个 HTML（资源前缀）、
    2 个 manifest（字段级改写）、`islands/ui.js` 与 `m/mobile.js`（各 1 处
    `/login` 跳转补前缀，其余字节不变）；
  · manifest 语义改写正确：`start_url`/`scope`/`id`/shortcuts 均带前缀、
    `#fragment` 保形、相对路径 icons 不动、根绝对路径 icons 正确补前缀；
  · `/api/session` 200（注入密钥的 Key 通道有效，未被弹去登录页）；
  · 面板 `/m` 与 `/m.html`、`sw.js`、`i18n/*`、`assets/*` 全部 200。
  仍未覆盖：**真机（飞牛）上的浏览器交互**，见下一条。
- ✅ **PWA / 手机端在子路径下的 scope 已适配**：manifest 由网关做字段级改写，
  Service Worker 的注册 scope 由 `build.py` 的 `patch_sw_scope()` 在同步时改为
  `./`，并有守卫。详见 §1.2。**未验证的部分**：手机端 `/m` 在真实移动端浏览器
  里的 PWA 安装与离线行为（需要在 `https` 环境下用真机点「添加到主屏」）。
- ⚠️ **真机安装**。§9.2 里所有结论都停在构建期，**尚未在真实飞牛设备上跑起来**
  （v2.9 时代也一样）。待验证项：socket 路径与权限、`X-Trim-*` 管理员闸门、
  面板经网关后的渲染与交互、下游 `:3065/v1` 的局域网可达性、
  「添加账号 → 反代」模型图标在真机上是否真的显示。
- ⚠️ **v2.9 → v3.1.0 的数据兼容**（见 §4.2）。
- ⚠️ **自动升级链路的首次真实运行**。`check-upstream.yml` 的每一步都在本地
  验证过，但「定时触发 → 提交 → 打 tag → 派发 → 构建 → 发 Release」这条完整
  链路还没有被上游真实的新版本跑通过。已知无法在本地验证的点：Actions 对
  `secrets.GITHUB_TOKEN` 的 `actions: write` 授权是否允许 `gh workflow run`
  （若被拒，退路是 `repository_dispatch` + 一个 PAT）。
- ⚠️ **图标守卫的残余风险**。守卫依赖 `preset-providers.js` 的
  `icon: 'x.png'` 写法与 `islands/ui.js` 的完整路径字面量。v3.1.0 里这两处写法
  仍在（41 个图标全部被引用，已实测），但上游若再改写法，两条正则可能同时失效
  —— 已有「解析出的图标数不得少于磁盘数一半」的兜底断言，断言本身失效时仍会
  静默通过。
- ⚠️ **替身二进制拦截仍依赖 `rustc` 指纹**。上游 v3.1.0 的 `agent2api-server`
  实测 amd64 13,751,728 B / arm64 12,086,056 B，都在现有 8 MB 下限之上；若上游
  哪天换工具链或产物形态（例如不再留 `rustc` 字样），这道拦截会误报，
  需要重新确认。

### 9.4 v3.2.0-beta.2 本轮实测（2026-10-20）

> **本轮没有飞牛真机。** 下面凡标「实测」的，都是在 Windows 构建机上跑出来的
> （二进制/面板字节级 + 构建期守卫 + `fnpack` 实打包）；凡涉及真机运行时、
> 浏览器交互、数据迁移的，一律标「未验证」。

#### 9.4.1 版本与产物（实测）

| 项 | 值 | 状态 |
| --- | --- | --- |
| 上游版本 | `3.2.0-beta.2` | 实测（二进制内 3 处 + deb `Version:`） |
| 上游 tag | `v3.2.0-beta.2` | **推断**（该仓库既有 tag 形如 `v3.1.0`；tag 本身无法核实，见 9.4.4） |
| 应用版本 `manifest.version` | `3.2.0-beta.2-1` | 实测 |
| 产物名与体积 | `agent2api-3.2.0-beta.2-1-amd64.fpk` = **12,563,435 B**（包内 `platform`=`x86`，sha256 `8ad692e3…987a`）；`-arm64.fpk` = **11,812,003 B**（`platform`=`arm`，sha256 `7322fbc3…94c7`） | 实测（第 5 轮，已重出） |
| 包内 `app.tgz` | 各 **220 个文件**（`app/ui/` 各 **216** = 上游 213 + `config` + 两个 icon） | 实测 |
| 官方包 amd64 | `agent2api-server-linux-amd64.tar.gz`，**9,343,829 B**，sha256 `89ec624cbd633fc9fe22a085c05a518b7f6a91828930eba60d450898376ad338` | 实测（自算 = 包内 `.sha256`） |
| 官方包 arm64 | `agent2api-server-linux-arm64.tar.gz`，**8,925,518 B**，sha256 `e2eb299343fa704174113e63f4aa33d5280ae949bd6c7e6ab4ef16617fc8aee3` | 实测（同上） |
| 包内二进制 amd64 | **14,352,784 B**，sha256 `3dd08278…` | 实测 |
| 包内二进制 arm64 | **12,611,032 B**，sha256 `32d822a8…` | 实测 |
| 最高 GLIBC 符号版本 | **2.34**（飞牛上限 2.36） | 实测 |
| `rustc` 指纹 | amd64 101 次 / arm64 102 次（自报 rustc 1.99.1） | 实测 |
| ELF 机器字 | amd64 `0x3E` / arm64 `0xB7` | 实测 |

#### 9.4.2 面板（实测）

| 项 | v3.1.0 | v3.2.0-beta.2 | 说明 |
| --- | --- | --- | --- |
| 上游 `ui/` 文件数 | 208 | **213** | 两架构官方包内完全一致（`diff -rq` 退出 0）；自 2026-10-20 起这 213 个文件**不入库**，见 §9.4.8 |
| 相对上一版 | — | **新增 5 / 删除 0 / 修改 69** | 69 中 i18n 53、非 i18n 16 |
| 新增顶层条目 | — | **`sidebar-layout.js`** | 侧边栏布局重构 |
| 新增文件 | — | `sidebar-layout.js`、`css/page-sidebar-pane.css`、`assets/providers/{atomcode,lobsterai,modelscope}.png` | 后三个图标意味着新增 AtomCode / LobsterAI / ModelScope 三家适配 |
| 提供商图标 | 磁盘 41 / 引用 41 / 缺失 0 | **磁盘 44 / 引用 44 / 缺失 0** | 预置声明 24 + 反代引用 21（并集 44） |
| i18n 语言 | 5（en/ja/ko/pt-BR/zh-Hant） | **仍 5 个**，每语言 19 词典域 | **`zh-CN` 不再是 i18n 目录**（简体中文由基准文案承担），故不可把它写进 `UI_REQUIRED` |
| `index.html` 资源引用 | 52 | **52**，根绝对路径仍只有 `/m`，未登记 0 | 网关改写前提不变 |
| SW 注册形态 | `register("sw.js",{scope:"/"})` | **同**（`patch_sw_scope` 照常 +1 B 改 `"./"`） | 544,898 B → 544,899 B |
| `app/ui` 树哈希（**补丁后**） | `29ceadd6…499221`（208 文件） | **`011b905c2ac3ae70d69f5865922e35f89f59af5d20a889ca0a87860e1f32059e`（213 文件）** | 口径见 §1.2 末尾的说明 |

#### 9.4.3 网关改写面复核（实测，结论：仍适用）

对新面板重新比对 `fngateway` 依赖的全部形态，**未发现需要改网关源码的变化**：

| 复核点 | 3.1.0 | 3.2.0-beta.2 | 判定 |
| --- | --- | --- | --- |
| `localStorage['agent2api.webKey']`（seed 脚本依赖） | 1 处 | **1 处** | 不变 |
| `fetch('/api/...')` 根绝对写法 | 5 处 | **5 处** | 不变 |
| 裸相对 `api/`、`./`、`http(s)` 绝对 fetch | 0 / 0 / 0 | **0 / 0 / 0** | 不变 |
| `/api/` 字面量 | 39 个 | **46 个**（新增 7 个端点） | 只增不改，仍在既有 `AdminOnly` + 密钥注入范围内 |
| 面板内硬编码 `/app/agent2api` | 0 | **0** | 不变（好） |
| 新增 localStorage 键 | — | `agent2api.mobile.updateAutoCheck` | 与网关无关 |

#### 9.4.4 ⚠️ 上游仓库 404（实测，本轮最重要的事实）

探测时间 2026-10-20 18:29–18:45 +08:00：

| 探测 | 结果 |
| --- | --- |
| `GET api.github.com/repos/Agent2API/agent2api` | **404** |
| `GET api.github.com/users/Agent2API` / `/orgs/Agent2API` | **404**（连账号本身都不存在） |
| `HEAD github.com/Agent2API/agent2api` | **404** |
| `HEAD codeload…/tar.gz/refs/tags/v3.1.0` 与 `…/v3.2.0-beta.2` | **404** |
| 镜像 `v4.gh-proxy.org` / `gh.dpik.top` 取两个版本的资产 | **全 404** |
| 对照：`api.github.com/repos/torvalds/linux` | **200** |

结论与后果：

- 这些 404 **不是本机网络问题**（同刻对照仓库 200）。GitHub 对「仓库被删 / 账号被封 / 改名」
  都回 404，因此**无法区分是哪一种**。旁证：第三方追踪站 Upstar 的缓存快照
  （标注 `Checked Oct 19, 2026`）当时还显示该仓库 `Latest release v3.1.0`。
- **源码 tarball 与 Release 资产现在都无法下载** → CI 的取包链路、`check-upstream.yml`
  的定时跟进、`bump_upstream.py --apply` 都会失败。
- ⚠️ **面板移出仓库后，这个后果比以前严重得多。** 以前面板随仓库走，即使上游 404 也还能
  用仓库里的面板打包（只是版本不会前进）；自从 §9.4.8 把面板移出仓库，**连面板都取不到了**
  —— 现在构建**必须依赖 `.local-build/` 里的本地缓存**，没有任何后备来源。
  换句话说：上游 404 从「无法升级」升级成了「无法从干净环境构建」。
- 本版因此是**离线构建**：用已落地的两个官方包（sha256 已钉进 `UPSTREAM_BUNDLE_SHA256`）。
- **本机也没有 3.2.0-beta.2 的源码 tarball**（只有 3.1.0 的），所以
  `verify_upstream_version()`（读 `server/Cargo.toml`）这条路本轮**跑不到**；
  其期望值 `3.2.0-beta.2` 仅由 deb `Version:` 推定，**未验证**。
- 上游何时/是否恢复**未知**；恢复后应按 §3.5 正常 `bump_upstream.py --detect` 复核。

#### 9.4.5 ⚠️ 已知问题：登出跳转回归（实测，非阻断）

3.2.0 把面板「退出登录」的跳转改成了**三目表达式**（`app/ui/islands/ui.js`，原文）：

```js
window.location.href = e ? "login.html" : "/login"
```

而 `fngateway` 的 `navigationLiteralRe` 要求 `location.href=` 之后**紧跟**带引号的字面量
（`=(["'])(/[^/"'…])`），三目里的 `e ?` 让它**匹配不到**：

| 版本 | 全面板导航字面量命中 | 登出命中 |
| --- | --- | --- |
| 3.1.0 | **2 处**（`islands/ui.js` → `/login`、`m/mobile.js` → `/login?next=%2Fm`） | 命中 1 次 |
| 3.2.0-beta.2 | **1 处**（只剩 `m/mobile.js` → `/login?next=%2Fm`） | **命中 0 次** |

后果：飞牛下 `platform === "web"`，三目取 `"/login"` 分支且**不会被补前缀** →
登出跳到**站点根**而不是 `{应用前缀}/login`。表现为「退出登录后回到飞牛首页而不是本应用登录页」，
不影响功能与数据。

最小修复（二选一，本轮**未实施**）：
1. 产物层：同步时把两分支都改成裸相对 `login.html`（与 `patch_sw_scope` 同一类产物适配）；
2. 网关层：`navigationLiteralRe` 增加三目 `else` 分支，并补一条单测。

#### 9.4.6 已修的构建缺陷：上游二进制来源 sidecar 守卫（实测）

原本 `verify_fpk()` 只核对包内二进制的**架构与 ELF**，不核对**它是不是本次 pin 的那一版**。
于是「同步过新面板、但没重取二进制」的场景下，另一种架构会把**上一版的上游二进制**装进包，
而 ELF / 架构 / rustc / 体积 / glibc 五项检查**全部绿灯**。

现修法：`fetch_server_bundle()` 在落二进制时同时写溯源 sidecar
`.local-build/bin/agent2api-server-linux-<arch>.sha256`，`check_binaries()` 复查其与当前
`UPSTREAM_BUNDLE_SHA256` 解出的二进制一致。本轮两个 sidecar 均已生成（各 95 B），
日志中可见 `溯源 sidecar: agent2api-server-linux-amd64.sha256`。

#### 9.4.7 未验证清单（本轮不给结论）

- ⚠️ **真机安装与运行**：本轮无飞牛设备。socket 路径与权限、`X-Trim-*` 管理员闸门、
  面板经网关渲染、`:3065/v1` 局域网可达性、模型图标真机显示 —— 全部沿用 v3.1.0 的未验证状态。
- ⚠️ **3.2.0 的面板在真实浏览器里的交互**（含新侧边栏布局 `sidebar-layout.js`）。
- ⚠️ **v3.1.0 → v3.2.0-beta.2 的数据兼容**（SQLite 库结构是否有迁移；二进制内可见
  `dbSchemaVersion` 字段，但未做升级实测）。
- ⚠️ **`v3.2.0-beta.2` 这个 tag 是否真的存在过**（仓库 404，无法核实）。
- ⚠️ **预发布版本号在飞牛应用中心侧的接受度**：`fnpack 1.2.3` 实测**不校验**版本号格式
  （连 `not-a-version` 都收），仅空值被拒；因此「应用中心是否接受 `3.2.0-beta.2-1`」
  只能在真机/应用中心验证 —— **未验证**。
- ⚠️ **自动升级链路**仍未首次真实运行（同上版）。

---

_本节所有数字均可在 `.local-build/scout320/recon.md`（侦察原始输出，含每条命令与原始返回）
与 `.local-build/builder320/logs/`（构建日志）中复核。_

#### 9.4.8 上游面板移出仓库（2026-10-20）

**动机：消除三份副本之间的漂移。** 在这之前，上游面板实际存在三份：仓库里的
`app/ui/`、`stage` 里组装出来的那份、以及最终 `fpk` 里 `app.tgz` 内那份。三者在
任何一次「同步了面板但没重取二进制」「改了 stage 但没重新同步」的错配下就会分叉，
而所有守卫仍然绿灯 —— 这正是本版新增 sidecar 溯源守卫（§9.4.6）要堵的那类问题。
把面板从仓库里拿掉，真相只剩一份。

**内容：`app/ui/` 现在只保留本项目的 3 个文件。**

| 路径 | 说明 |
| --- | --- |
| `app/ui/config` | 桌面入口定义（`fngateway` 与契约校验都要读它） |
| `app/ui/images/icon_64.png` | 桌面图标 |
| `app/ui/images/icon_256.png` | 桌面图标 |

上游面板的 **213 个文件不再提交进仓库**。

**做法：构建时注入 + `.gitignore` 白名单。**

- `build.py` 的 `stage_ui(arch, dst_ui)` 在组装阶段先从
  `.local-build/upstream-bundle-<arch>/ui/` 拷入面板（**拷贝前先跑 `patch_sw_scope`**），
  再叠加本地的 `config` 与 `images/`。面板的唯一真相来源就是官方包解包目录。
- `.gitignore` 用 `app/ui/*` 挡住全部面板文件，再用 `!app/ui/config`
  `!app/ui/images/` `!app/ui/images/**` 放行那两个条目。
  （`images/` 那条必须带尾斜杠并单独放行其内容，否则目录被忽略后里面的图标也跟着丢。）
- `--sync-upstream` 现在**只解包、不写工作区**；要把它落到 `app/ui` 做人工
  diff 时，才加新参数 `--materialize-ui`。
- `verify_webui_provenance()` 改为**逐架构校验**（amd64 / arm64 各一行日志），
  对象是解包目录里的面板 + SW 补丁。`UPSTREAM_UI_SHA256` 的**语义不变**
  （仍是打完 `patch_sw_scope` 后的树哈希，`011b905c…`），但**校验对象**从
  「仓库副本」换成了「官方包面板 + 补丁」。

**代价（必须如实写出来）：牺牲 `git diff` 的可审阅性。**

面板不在仓库里，意味着**上游改了面板不会再体现在任何代码评审里** ——
以前一次面板升级会在 PR 里显示「69 个文件变更，可逐行 review」，现在这个信号**没有了**。
剩下的两道防线是：

1. **`UPSTREAM_UI_SHA256` 漂移探针**：哈希一变就构建期硬失败，逼人确认；
2. **解包目录对比**：拿两份 `upstream-bundle-<arch>/ui/` 做 `diff -rq`
   （或先 `--materialize-ui` 落盘再 `git diff`）。

也就是说，漂移**仍然会被发现**，但从「默认被看见」降级为「需要主动去看」。
这是本变更明确接受的取舍。

> **兜底手段：`--materialize-ui`。** 需要把面板临时落盘到 `app/ui` 做人工对比时用它；
> 用完记得清理 —— 它是**调试/审阅**用途，不是常规工作流，落盘的面板也**不应该**被提交。

**⚠️ 前置条件：面板已移出仓库，构建不再自包含。**

面板不在仓库里，就意味着**光有干净的克隆是构建不出包的**。干净克隆后必须先让官方包落地：

```powershell
python build.py --fetch-binaries
```

也就是把 `agent2api-server-linux-<arch>.tar.gz` 放进 `.local-build/`
（或确认上游 Release 可达，让 `--fetch-binaries` 自己下）。

- 缺失时 `build.py` 会**直接 fail-fast 退出**（并打印可操作的指引：要跑哪个命令、
  tar.gz 该放哪里），**不会**产出缺面板的错包 —— 这一点是安全的；
- 但**结论是构建不出包**。拿不到官方包 = 打不了包。

> 这也是目前**上游仓库 404 期间的唯一可行路径**（见 §9.4.4）：本机之所以能出包，
> 是因为 `.local-build/` 里**已经有官方包缓存**。换一台干净的机器、或清掉
> `.local-build/`，在上游恢复前都打不出新包。

**第 5 轮产物（面板改由构建注入后重出）：**

| 架构 | 体积 | sha256 |
| --- | --- | --- |
| amd64 | **12,563,435 B** | `8ad692e3fc1b77888a4c9446222f651a054d2c8eb045dc5c74ce1be53fb4987a` |
| arm64 | **11,812,003 B** | `7322fbc3907179e34284efe22ef847f47906ef9a120ebef74421c682041894c7` |

包内容不变：`app.tgz` 各 **220 个文件**，其中 `app/ui/` 各 **216 个**
（213 面板 + `config` + 2 个图标）—— 说明「面板不入库」对产物体积与内容**没有影响**，
只是仓库里的文件数少了。

### 9.5 展示地址纠正的浏览器级验证（2026-10-25）

不依赖飞牛设备：用仓库自带 `fngateway/devcheck` 把「剥前缀 → 桥接注入 → 面板密钥注入 → 管理员门」
整条链路挂到回环 TCP（`-upstream 127.0.0.1:17899 -addr 127.0.0.1:17901 -prefix /app/agent2api -apiport 3065`），
上游用 `.local-build/bin/agent2api-server-linux-amd64`（`AGENT2API_PROXY_PORT=17899`），再用真实浏览器取 DOM 文本：

| 位置 | 实测结果 |
| --- | --- |
| 手机端首页卡片 | `127.0.0.1:3065/v1`，改写后**复制图标 `<svg>` 仍在**（早期实现整体重写容器 `textContent`，会抹掉元素子节点） |
| 手机端「接入」「更多」两页 | 5 行地址 + `curl` 示例全部为 `:3065`，页面**无重复文本块**（早期实现把容器聚合文本写回，出现整句重复） |
| 桌面文档页 | `#api-base` = `http://127.0.0.1:3065/v1`、`#api-chat` = `POST …/v1/chat/completions`、`#api-models` = `GET …/v1/models` |
| 负向对照 | `https://api.openai.com/v1/chat/completions` 原样保留；侧栏「网关运行中 · 127.0.0.1:17901」原样保留（该行不含 `/v1`） |
| 复制按钮 | `navigator.clipboard.writeText` 被包装成「先 `apiText` 再写剪贴板」，复制出来的是纠正后的地址 |

两个只在真浏览器里才暴露的坑，已写进实现：

1. **整体重写容器文本会抹掉子元素**（复制图标）。故只改写「直接文本子节点」，不碰元素子节点；
2. **把容器的 `textContent` 聚合成整串再写回**会让文本重复（父子重复渲染）。故仅当**所有直接文本子节点拼起来**才命中改写形态时才合并重写，且长度上限 256；应当改写的是那个**直接持有该文本的子节点**。

> 切换网关二进制后必须清 Service Worker 缓存（`unregister` + 删 `caches` + 强制刷新），
> 否则页面仍在跑旧桥接脚本 —— 这一条在验证过程中真的骗过一次。
