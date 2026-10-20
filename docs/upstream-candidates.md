# 上游候选调研：能否换一个更合适的上游？

调研日期：2026-09-30（**2026-10-18 更新第 8 节：上游仓库迁移与 v3.1.0**）
调研范围：GitHub 公开仓库，目标是「把 WorkBuddy / CodeBuddy / Qoder / Trae 等 AI 客户端登录态转成本地 OpenAI 兼容 API 网关」，
且必须能作为飞牛 fnOS `.fpk` 的上游。

当前上游：`Agent2API/agent2api`（**v3.1.0**，Rust 单二进制，**官方发 Linux 服务端包**，本仓库直接取用）
既有对比基线：`caigee-cmd/cli2api`（Go 控制面 + Node worker）

> 说明：本文所有结论均来自实际抓取的 README / releases.atom / CI 配置 / LICENSE 文件。
> 抓不到的一律标注「未获取到」，不臆测。`api.github.com` REST 在调研期被限流（403），
> 元数据（star/日期）主要来自一次成功的 REST 调用与 releases.atom。
>
> **时间线注意**：第 0～7 节是 2026-09-30 的调研结论，当时的上游是 v2.9 系列
> （旧地址）。2026-10-18 上游迁到 `Agent2API/agent2api` 并发布 v3.1.0，
> **第 1 节的「现状回顾」与候选矩阵里 agent2api 那一行已经过时**，最新形态与
> 取舍见第 8 节。保留旧内容是为了让「当初为什么觉得它不够好」可追溯。

---

## 0. 结论先行

**有更好的上游，但「更好」的方向不是换掉 agent2api 的 provider 覆盖，而是换掉它的「自编译 + 面板形态被钉死」这两个维护负担。**

推荐组合（首选）：

```
宿主：router-for-me/CLIProxyAPI          —— Go 单二进制，官方直发 linux amd64/arm64
插件：mmqz/cpa-multi-plugins             —— 官方直发 linux amd64/arm64 的 .so 插件
```

覆盖：CodeBuddy / WorkBuddy（CN+Intl+Global 三区合并）、Qoder（CN+Intl）、Trae（CN Code + CN SOLO + Intl 三变体）、
ZCode（智谱 GLM 编码套餐）、MiMo（小米）。**正好命中你点名的 workbuddy + qoder，并附带 trae。**

两者都是 **纯 MIT**（比当前上游「MIT + 禁止商业用途附加条款」更干净），
两者都**官方直发 Linux 双架构产物**（不用再自己编 Rust），
插件机制把「上游协议漂移」隔离在一个 `.so` 里（不用 fork 上游）。

**唯一的硬风险**：插件 ABI 绑定宿主主版本。cpa-multi-plugins 的 `go.mod` 锁 `CLIProxyAPI/v7 v7.2.30`，
而宿主已经发到 **v8.0.4**。上 fpk 前**必须实测锁定一组可用组合**（见 §5 验证清单）。

---

## 1. 现状回顾：当前上游的痛点

> ⚠️ 本节记录的是 **2026-09-30 当时的现状**（v2.9），用于解释「当初为什么
> 想换上游」。其中「Linux 产物：上游不发」「CI 自己 cargo build」两行**已经不成立**
> —— v3.1.0 起上游官方发 Linux 服务端包，详见第 8 节。

| 项 | 现状 |
|---|---|
| 上游 | 当时的 `agent2api`，v2.9（旧地址已失效，现为 `Agent2API/agent2api`） |
| 形态 | Rust 单二进制（`agent2api-server`） |
| Linux 产物 | **当时上游不发**。Release 只有 Windows NSIS + macOS dmg + Docker 镜像<br>（**已变化**：v3.1.0 起官方直发 `agent2api-server-linux-<arch>.tar.gz` + `.sha256`） |
| 本仓库 CI（当时） | 拉上游 tag 源码 → `cargo build -p agent2api-server`（amd64 原生 + arm64 交叉），冷编译约 10 分钟<br>（**已变化**：v3.1.0 起改为「下载官方包 + 校验 sha256 + 解包」，不再编 Rust） |
| 面板适配 | 上游 WebUI 被 `UPSTREAM_UI_SHA256` 树哈希钉死，上游改一个字节就构建期失败，必须人工回归 |
| 发布节奏 | 极快：v2.7.3(09-27) → v2.8.0(09-28) → v2.9(09-29)，两天两个 minor |
| 许可 | MIT + 「使用声明」第 3 条禁止商业用途与二次分发牟利（**非纯 MIT**） |
| provider 覆盖 | 已相当全：WorkBuddy / Raccoon / CatPaw / AutoClaw / Qoder / Cline / Accio / CodeArts / Trae / ZCode |

**结论：provider 覆盖其实已经不是短板了**（Trae、CodeArts、Accio、ZCode 在 v2.8 / v2.9 都补上了）。
真正的问题是：① 每次上游发版都要重编 Rust 双架构；② 面板形态漂移探针逼人工回归；③ 许可不是纯 MIT。
**其中 ① 已被上游 v3.1.0 解决**（官方发 Linux 包），②③ 仍在（第 8 节逐条复核）。

---

## 2. 候选对比矩阵（按推荐度排序）

| 仓库 | 形态 | Linux 双架构产物 | provider 覆盖 | 许可 | 活跃度 | 作为 fpk 上游 |
|---|---|---|---|---|---|---|
| **router-for-me/CLIProxyAPI** | Go 单二进制 | ✅ 官方直发 `linux_amd64/aarch64.tar.gz`（另有 `_no-plugin` 变体） | 本体不做国内客户端；靠插件扩展 | MIT | 53.6k★，v8.0.4 (09-29) | ⭐⭐⭐⭐⭐ 宿主 |
| **mmqz/cpa-multi-plugins** | CPA 插件（`.so`） | ✅ 官方直发 `linux-amd64/arm64.zip` | WorkBuddy/CodeBuddy(三区) + Qoder(双区) + Trae(三变体) + ZCode + MiMo | MIT | v0.12.103 (09-30)，10 天 40+ 版 | ⭐⭐⭐⭐⭐ 插件 |
| zhangdailin/API-Console | Go 单二进制 | ⚠️ 仅 `linux-amd64`，**无 arm64** | WorkBuddy / Qoder / Cline / Grok | ❌ **无 LICENSE(404)** | 未获取到 | ⭐⭐ 阻断 |
| CangShui/workbuddy-gateway | Go 单二进制（零 CGO） | ✅ 官方直发 `linux-amd64/arm64` | 仅 CodeBuddy/WorkBuddy 国内+国际 | ❌ **无 LICENSE(404)** | 未获取到 | ⭐⭐ 阻断 |
| linguo2625469/workbuddy2api-panel | Go 单二进制 | ❌ 无 Release，需自编译（有 GHCR 镜像） | 仅 CodeBuddy/WorkBuddy | MIT | 中等，单维护者 | ⭐⭐ 覆盖不足 |
| ithtelab/workbuddy-manager | Python + Next.js | ❌ 无单二进制（tar.gz 仍需 Python） | 仅 CodeBuddy（上游 workbuddy2api） | MIT | 极活跃 v1.0.76 | ⭐⭐ 需 docker.sock |
| smart-open/TraeWorkAssistant | Rust/axum | ❌ 仅 GHCR `linux/amd64`，**无 arm64** | Trae / WorkBuddy / 豆包 | MIT | v3.6.x 活跃 | ⭐⭐⭐ 缺 arm64 |
| shuishuipingan/qoder2api-hub | 纯 Python 零依赖 | ❌ 无产物，需自编译 | 仅 Qoder 双区 | MIT | v1.1.1 单维护者 | ⭐⭐ 单 provider |
| maiphucgiang/codebuddy2api | Python + React | ❌ 无二进制（GHCR 双架构镜像） | WorkBuddy / CodeBuddy | MIT（但 README 自述禁止商用，与 MIT 冲突） | 极活跃 v1.3.1 | ⭐⭐ 需 Python/Docker |
| HUIdada1/AgentHub | Electron 桌面 | ❌ 仅 Windows exe | Trae / WorkBuddy / Zcode | MIT | v1.40.0 | ⭐ 桌面端 |
| 1416277987/proxy-hub | Node.js | ❌ 无 Release | CodeBuddy/Trae/Qoder(CN) | ❌ **无 LICENSE(404)** | 单日 6 commit，已停 | ⭐ 阻断 |
| techysy/CreditDaddy | Node + 桌面 | ❌ | Qoder/WorkBuddy/ZCode | MIT | v0.3.0 | ⭐ 定位不同 |
| caigee-cmd/cli2api | Go + Node worker | ❌ 服务端无二进制（Release 只有 updater） | Qoder/WorkBuddy/Trae/Codex | MIT | v0.6.13 活跃 | ⭐⭐ 自述真账号验收未完成 |
| BoosaIDonng/workbuddy2plus-plugin | CPA 插件（`.so`） | ✅ 但仅 workbuddy.so | 仅 CodeBuddy/WorkBuddy | MIT | v2.8.4 (09-25) | ⭐⭐⭐ 可用但窄 |
| agent2api（**当时**的现状，见第 8 节更新） | Rust 单二进制 | 当时 ❌ 上游不发，CI 自编译<br>（v3.1.0 起 ✅ 官方直发） | 当时 10 家，v3.1.0 为 17 家（最全） | MIT+附加限制 | v2.9 极快，现 v3.1.0 | ⭐⭐⭐⭐ 现状 |

---

## 3. 首选方案详解：CLIProxyAPI + cpa-multi-plugins

### 3.1 为什么是它

1. **产物形态最省事**。宿主与插件都由上游 CI 直发 Linux amd64/arm64，`build.py` 从「拉源码 + cargo 交叉编译 10 分钟」
   退化成「下载两个压缩包 + 解压」，CI 的 Rust 工具链、`aarch64-linux-gnu-gcc`、glibc 断言那一整套可以拆掉。
2. **插件机制 = 漂移隔离层**。上游改协议时，改的是插件仓库（10 天发 40+ 版，跟进极快），
   而不是让本仓库 fork 上游。这正是当前 agent2api 方案缺的东西——它靠「树哈希钉死 + 人工回归」硬扛。
3. **provider 命中需求**。WorkBuddy/CodeBuddy 三区合并、Qoder 双区、Trae 三变体、ZCode、MiMo。
4. **许可更干净**。两者都是纯 MIT，没有「禁止商业用途」附加条款。
5. **无运行时依赖**。不需要 Node / Python / Docker / 桌面客户端。glibc baseline 2.17（宿主默认构建），
   飞牛 fnOS 1.2.x = Debian 12 = glibc 2.36，**向下兼容，安全**（不像当前 CI 要刻意锁 ubuntu-22.04）。
6. **插件带签到 / 配额调度 / 多账号池**，功能上比单纯转发更完整（credit-aware 调度、4 档冷却状态机、每日签到）。

### 3.2 fpk 内形态

```
app/bin/cli-proxy-api            # 宿主（Go，官方 release 直取）
app/bin/plugins/linux/<arch>/    # workbuddy.so qoder.so trae.so zcode.so mimo.so
config/config.yaml               # plugins.enabled=true + 每个插件显式 enabled
data/auth/                       # 凭据 JSON（每账号一个，0600，原子写）
data/logs/
```

- 宿主监听 8317，插件由宿主 `dlopen` 加载，**不是一个独立进程**，`cmd/main` 只需管宿主一个 PID。
- `plugins.dir` 与 `auth-dir` 必须落在 `${TRIM_PKGVAR}`，否则升级丢凭据。
- 每个插件在 `config.yaml` 里默认 **disabled**，必须显式打开（`workbuddy: { enabled: true }` 等）。

### 3.3 需要注意的适配点

- **管理面板子路径**：宿主管理面在 `/v0/management`，插件面板在 `/v0/resource/plugins/<id>/panel`，
  与当前 `fngateway` 的 HTML 改写思路同构，可以复用（`<base>` + fetch 前缀 + `Location`/`Set-Cookie` 回填）。
- **登录回调**：Trae 的 OAuth 回调强制 `http://127.0.0.1:<port>/authorize`，跨机时插件提供粘贴框
  （`/v0/resource/plugins/trae/panel` 或 `oauth_submit?cb_url=`），fpk 里要引导用户走粘贴路径。
- **时区**：插件签到按自然日 0 点重置，隐含要求宿主时区为 CN 时区。
- **插件崩溃 = 宿主崩溃**（同进程），`cmd/main` 的守护逻辑要能拉起整个宿主。

---

## 4. 明确不建议的候选（及原因）

| 候选 | 阻断原因 |
|---|---|
| `CangShui/workbuddy-gateway` | **无 LICENSE**（`LICENSE`/`LICENSE.md` 均 404），默认保留全部权利，**无再分发授权**，打包分发法律基础缺失。技术形态其实很好（零 CGO 静态双架构二进制），可惜卡在许可。 |
| `1416277987/proxy-hub` | **无 LICENSE** + 全部 6 个 commit 挤在 2026-08-21 单日、此后停更 + 4 个适配器里 3 个依赖 **Windows 桌面端私有路径**（`%APPDATA%`），在 Linux NAS 上根本不成立。 |
| `zhangdailin/API-Console` | **无 LICENSE** + **Redis 硬依赖**（多一个常驻进程）+ 仅 amd64 + `credential.key` 丢失即全部凭据不可解密。 |
| `caigee-cmd/cli2api` | 服务端**无 Linux 二进制**（Release 只有 updater）；Qoder 每账号一个 Node 进程 + 独立 HOME（约 511 MB RSS/账号）；README 自述 Qoder 国内版/WorkBuddy/Trae「真账号验收仍未完成」。 |
| 各类 `workbuddy2api` 系（linguo2625469 / ithtelab / BoosaIDonng / Sliverkiss fork） | **单一 provider**（只做腾讯 CodeBuddy/WorkBuddy），不满足「workbuddy + qoder 等多个」；且原始上游 `Sliverkiss/workbuddy2api` **已被作者删库**，供应链脆弱。 |
| `smart-open/TraeWorkAssistant` | 只有 `linux/amd64` 镜像，**无 arm64**，飞牛部分机型直接不可用。 |

---

## 5. 上 fpk 前的验证清单（按顺序）

1. **ABI 组合实测**（最关键）：插件 `go.mod` 锁 `CLIProxyAPI/v7 v7.2.30`，宿主已到 v8.0.4。
   必须下载宿主 v7.2.x 与 v8.0.4 各一份，把 `linux-amd64` 插件 `.so` 分别放入
   `plugins/linux/amd64/`，看日志是否出现 `pluginhost: plugin registered plugin_id=...`。
   **锁定一组可用组合后再写进 `build.py` 常量。**
2. **glibc 断言**：宿主默认构建要求 GLIBC 2.17 baseline，验证产物最高 GLIBC 符号 ≤ 2.36（沿用现有 CI 断言）。
3. **`_no-plugin` 变体不可用**：必须用默认构建（CGO 版）才支持 `.so` 插件，别拿 `no-plugin` 省事。
4. **双架构一致性**：arm64 的 `.so` 与 arm64 宿主同组验证（插件的 amd64/arm64 是分开编译的）。
5. **登录链路**：Qoder 设备码 / WorkBuddy state 轮询 / Trae 回调或粘贴，各跑通一次真账号。
6. **数据目录持久化**：`auth-dir` + `config.yaml` + `plugins/` 三者都映射到 `${TRIM_PKGVAR}`，升级不重建。
7. **许可合规**：确认 `LICENSE.upstream` 需要随包附带**两个**上游（宿主 + 插件）的 MIT 全文。

---

## 6. 备选路径（风险最小）

如果不想动现有已跑通的 `fnos-agent2api`，还有两条低风险路线：

**A. 并行新增一个 fpk 应用**（推荐给「先试试」）
新建 `fnos-cliproxyapi` 仓库，与 `fnos-agent2api` 并存。两者端口（8317 vs 3065）、
数据目录、生命周期完全独立，互不干扰。跑一段时间对比稳定性与覆盖率，再决定是否替换。

**B. 只把「自编译」这一步优化掉**
保留 agent2api 作上游，但在 `build.py` 里增加「优先从 Docker 镜像提取 `agent2api-server`」的分支，
把 Rust 交叉编译降级为兜底路径。收益有限（面板漂移探针与许可问题仍在），不解决根本问题。

> 事后看：**这条 B 路线已被上游自己在 v3.1.0 里做掉了** —— 上游官方发
> Linux 服务端包，本仓库直接从 Release 取，自编译这条路整条删掉。见第 8 节。

---

## 7. 参考：本轮抓取到的关键事实

- `CLIProxyAPI` v8.0.4 Release 说明原文：
  「`CLIProxyAPI_<version>_linux_<arch>.tar.gz` is the default Linux build. It supports dynamic library plugins and
  is built against a **GLIBC 2.17 baseline**.」
- `cpa-multi-plugins` v0.12.103 资产：`linux-amd64.zip` (17.5 MB) / `linux-arm64.zip` (15.9 MB)，
  安装方式为「把 `.so` 拷到 `plugins/linux/amd64/` 并在 `config.yaml` 里逐个 `enabled: true`」，
  且标注 *Installation (verified against real CLIProxyAPI host)*。
- `agent2api` Release 资产（v2.7.1 实测）：仅 `.exe` / `.dmg` / Source code，**无 Linux 二进制**。
- 本仓库 CI 用 `ubuntu-22.04` 编、`ubuntu-24.04` 打包，注释明确：飞牛 = glibc 2.36，
  用 24.04(glibc 2.39) 编出来的二进制在真机上会 `version 'GLIBC_2.38' not found`。

---

## 8. 2026-10-18 更新：上游换仓库 + v3.1.0 发布（本文结论的新基线）

### 8.1 发生了什么

- 上游仓库**整体迁移**到 `Agent2API/agent2api`。旧地址的 API 与 codeload 现在
  都是 **404**（实测），所以本仓库这次是**换仓库**，不是「改个版本号」。
- 新仓库**没有** v2.9.x 及更早的 tag，只有 **v3.1.0** 起。因此本仓库的应用版本
  也从 `2.9.x-N` 跳到 `3.1.0-1`（约定仍是「上游版本-N」）。
- v3.1.0 Release：`published_at` = 2026-10-18T11:28:19Z，非 pre-release，
  **11 个资产**。

### 8.2 上游形态对比（这才是「换上游」判断的关键）

| 项 | v2.9（2026-09-30 调研时） | v3.1.0（2026-10-18） |
| --- | --- | --- |
| 仓库 | 旧地址（现 404） | `Agent2API/agent2api` |
| Linux 产物 | ❌ 上游不发，本仓库 CI `cargo build` | ✅ **官方直发** `agent2api-server-linux-{amd64,arm64}.tar.gz` + 同名 `.sha256` |
| 服务端包内容 | — | 根下 `agent2api-server`（headless 二进制，amd64 13,751,728 B）+ `ui/` 面板 + `install.sh` / `agent2api.service` / `agent2api.env.example` |
| 二进制体积（Release 资产） | — | amd64 8,826,710 B / arm64 8,423,577 B（tar.gz，实测取自 GitHub API） |
| 面板形态 | 71 个文件 | **208 个文件**；新增 i18n（en/ja/ko/pt-BR/zh-Hant 五种非简体语言包，加简中共**六种界面语言**）、手机端 `/m`、PWA（`manifest.webmanifest` + `sw.js` + `assets/app-icon-*.png`）、新 CSS 结构；提供商图标 30 → **41** |
| provider 覆盖 | 10 家 | **17 家** + 自定义提供商（WorkBuddy 国内/国际、小浣熊、CatPaw、AutoClaw、Qoder、Cline、Accio、ZCode、CodeArts、Trae、Loomy、KukuAI、MonkeyCode、Command Code、Antigravity、OfficeAce、OrcaTerm、智谱清言） |
| 许可 | MIT + 使用声明（第 3 条禁商用与二次分发牟利） | **同构**：MIT 正文 + 「使用声明（Usage Notice）」六条，第 3 条仍是禁止商业用途与二次分发牟利。仍**非纯 MIT** |
| 署名 | 旧组织 | `Copyright (c) 2026 Agent2API (https://github.com/Agent2API)` |
| 额外资产（本项目**不取**） | — | `Agent2API-3.1.0.apk`、`Agent2API_3.1.0_x64-setup.exe`、`_universal.dmg`、`_amd64.deb`、`_amd64.AppImage`、`agent2api-server-windows-amd64.zip` |

> **泄漏面提醒**：v3.1.0 的 Release 里还有 android apk / AppImage / deb / dmg /
> exe 与 Windows 服务端包，本项目**只取** `agent2api-server-linux-<arch>.tar.gz`
> 与 `ui/`。`build.py` 的下载白名单就是这两个资产名。

### 8.3 第 1 节三个痛点，现在还剩几个

| 原痛点 | 现状 |
| --- | --- |
| ① 每次上游发版都要重编 Rust 双架构 | ✅ **已解决** —— 直接取官方 Linux 服务端包（含 sha256），CI 里 Rust 工具链 / `aarch64-linux-gnu-gcc` / Rust 缓存整条删掉 |
| ② 面板形态漂移探针逼人工回归 | ⚠️ **仍在** —— 面板从 71 涨到 208 个文件、又多了 i18n / `/m` / PWA / 新 CSS，`UPSTREAM_UI_SHA256` 树哈希仍然钉死，改了就得人工确认网关改写是否还成立 |
| ③ 许可不是纯 MIT | ⚠️ **仍在** —— v3.1.0 仍是 MIT + 使用声明 |

### 8.4 所以还要不要换成 CLIProxyAPI + cpa-multi-plugins

**暂时不换，理由变了。** 当初推动「换上游」的最大理由是 ①（自编译负担），
而它已经被上游自己解决；再加上这次迁移刚做完、面板形态大改，此时再叠一次
上游替换，等于把两个未验证变量同时引入。

仍然值得留意的、CLIProxyAPI 组合更好的地方（都不急着现在换）：

- **许可更干净**：CLIProxyAPI 与 cpa-multi-plugins 都是纯 MIT，没有「禁止商业
  用途」附加条款。本项目的自用分发不受限制，但下游若要再分发/商用，这条差异
  是实质性的。
- **插件化隔离漂移**：上游改协议只改 `.so` 插件，不用每次确认「面板形态是否还是
  网关能改写的那一种」。agent2api 这条链路仍然是「树哈希钉死 + 人工回归」。
- 但它的**硬风险没变**：插件 ABI 绑定宿主主版本，上 fpk 前必须实测锁定一组可用
  组合（见第 5 节验证清单），这件事一天都没做。

**触发重新评估的条件**（建议写成判断依据，而不是凭感觉）：上游再换一次仓库 /
面板形态再来一次大改 / 上游许可收紧 / 现有 fngateway 改写模式被上游打破。

### 8.5 本轮实测到的事实（可直接引用）

- 旧地址 API 与 codeload：**404**。
- 新仓库 tag `v3.1.0` 源码 tarball 的 `desktop-tauri/ui/`：**208 个文件**，
  `assets/providers/*.png` **41 个**（预置列表声明 24 个、反代索引引用 18 个，
  41 个文件全部被引用，磁盘 − 引用 = 0）。
- 官方包 `agent2api-server-linux-amd64.tar.gz`：8,826,710 B，sha256
  `019d92bf65bf3ba35f5fba7421bfb938031f1eb6b6fd5ba5c4e1174362cb98fb`
  —— 与上游 `.sha256` 资产、`build.py` 里钉住的值三者一致。
- 官方包内 `ui/` 与当时的仓库 `app/ui/`（排除本项目的 `config` / `images`）
  `diff -rq` **零差异**。（注：面板自 2026-10-20 起已不入库，见 §9.4。）
- v3.1.0 `LICENSE`：5,434 B，sha256
  `5869d5ab03a0130bc33c484bbfa8f17dc37f4ad4f07d243879e0eb2fbab4c832`，
  `LICENSE.upstream` 与之逐字节一致。
- 端到端打包（amd64 + arm64 双架构均跑通）：
  `agent2api-3.1.0-1-amd64.fpk` = 12,025,372 B（包内 `platform`=`x86`）、
  `agent2api-3.1.0-1-arm64.fpk` = 11,294,484 B（包内 `platform`=`arm`）；
  `app.tgz` 各 215 个文件，其中 `app/ui/` 各 211 个（上游 208 + `config` +
  两个 icon），**两个架构的 `app/ui/` 逐文件 sha256 相同**；包内
  `agent2api-server*` 与官方服务端包内二进制等长（amd64 13,751,728 B）。
- **PWA 子路径适配已解决（两侧分工）**：`manifest.webmanifest` 的
  `start_url` / `scope` 由 `fngateway` 的 `RewriteManifest()` 做字段级改写；
  `m/mobile.js` 里写死的 `scope: "/"` 由 `build.py` 的 `patch_sw_scope()` 在
  同步面板时改为 `scope: "./"`（配 `verify_sw_scope()` 守卫）。注意
  `Service-Worker-Allowed` 头**救不了**这个问题（实测带该头时 `scope:"/"`
  仍被拒），只能改 scope 本身。
- **已实测**：两个架构的 fpk 均能完整构建（数字见上）；v3.1.0 面板在
  fngateway 子路径下的 HTTP 级改写效果（142 个静态文件逐字节一致、
  仅 7 个文件按预期改写、manifest 语义正确、`/api/*` Key 通道有效）。
- **未验证**：真机（飞牛）上的浏览器交互与 PWA 安装/离线、v2.9 数据向
  v3.1.0 的迁移。

---

## 9. 2026-10-20 更新：上游 v3.2.0-beta.2 与仓库 404

> 上一节（§8）的基线是「上游迁到 `Agent2API/agent2api` 并发了 v3.1.0」。
> 本节记录两天后的新事实：**上游又发了一版预发布，同时该仓库本身变成 404。**

### 9.1 版本与面板增量（实测）

| 项 | v3.1.0（§8 基线） | v3.2.0-beta.2（本节） | 变化 |
| --- | --- | --- | --- |
| 上游版本 | `3.1.0` | **`3.2.0-beta.2`**（预发布） | — |
| 上游 `ui/` 文件数 | 208 | **213** | **+5 / −0 / 改 69** |
| 新增顶层条目 | — | **`sidebar-layout.js`** | 侧边栏布局重构 |
| 新增文件 | — | `sidebar-layout.js`、`css/page-sidebar-pane.css`、`assets/providers/{atomcode,lobsterai,modelscope}.png` | — |
| 提供商图标 | 41（全部被引用） | **44**（全部被引用，磁盘 − 引用 = 0，引用 − 磁盘 = 0） | +3 |
| i18n 语言目录 | 5（en/ja/ko/pt-BR/zh-Hant） | **仍 5 个**，每语言 19 词典域 | 无新增 |
| `index.html` 根绝对路径 | 1（`/m`） | **仍 1（`/m`）**，52 个资源引用，未登记 0 | 不变 |
| 上游二进制 | amd64 13,751,728 B | **amd64 14,352,784 B / arm64 12,611,032 B** | 变大 |
| 最高 GLIBC | 2.34 | **2.34**（飞牛上限 2.36） | 不变 |
| 官方包 tar.gz | 8,826,710 B | **amd64 9,343,829 B / arm64 8,925,518 B** | — |

**三个新图标意味着什么**：`assets/providers/` 下新增 `atomcode.png`、`lobsterai.png`、
`modelscope.png`，对应上游新增了 **AtomCode、LobsterAI（有道龙虾）、ModelScope（魔搭）**
三家的适配。这会带来两件事：

1. **对本项目的适配层零工作量** —— 图标与适配器全部编在上游二进制里，`ui/` 只是
   静态资源；`check_webui()` 的图标守卫（磁盘 vs 两个索引的引用）实测 44/44 全对上。
2. **对「换上游」判断是有利信号** —— 上游仍在**活跃扩充提供商覆盖**（两天内 +3 家），
   这正是第 1 节列出的「覆盖面」痛点在持续改善，而不是靠本项目自己补。

### 9.2 ⚠️ 上游仓库 404（实测，本轮最重要的事实）

| 探测 | 结果 |
| --- | --- |
| `GET api.github.com/repos/Agent2API/agent2api` | **404** |
| `GET api.github.com/users/Agent2API`、`GET api.github.com/orgs/Agent2API` | **404**（连账号本身都不存在） |
| `HEAD github.com/Agent2API/agent2api` | **404** |
| `HEAD codeload.github.com/…/tar.gz/refs/tags/v3.1.0` 与 `…/v3.2.0-beta.2` | **404** |
| 镜像 `v4.gh-proxy.org` / `gh.dpik.top` 取两个版本的资产 | **全 404** |
| 对照：`api.github.com/repos/torvalds/linux` | **200**（同刻，故非本机网络问题） |

补充说明两点，避免误读：

- GitHub 对「仓库被删除 / 账号被封禁 / 仓库改名」**都**返回 404，脚本侧无法区分是哪一种。
  第三方追踪站 Upstar 的缓存快照（标注 `Checked Oct 19, 2026`）当时还显示该仓库
  `Latest release v3.1.0`，说明它是 **10-19 到 10-20 之间**变的。
- `git ls-remote` 在本机对 github.com:443 **对所有仓库都超时**（包括已知存在的仓库），
  所以**不能**把「`ls-remote` 失败」当成仓库不存在的证据；以上结论全部来自
  GitHub REST API 与网页 HEAD。

**对本项目自动化的影响（这是实质性的）：**

| 链路 | 影响 |
| --- | --- |
| `build.py --fetch-binaries` / `fetch_server_bundle()` | 三个下载源（直连 + 两个加速源）**全 404**，取不到官方包 |
| `build.py --sync-upstream`（拉源码 tarball） | **404**，源码同步路径不可用 |
| `check-upstream.yml`（每天 03:23 UTC） | 查不到 Release/tag，**定时跟进会失败** |
| `bump_upstream.py --detect` / `--apply` | **同上**，无法发现新版本 |
| CI `build-and-release.yml` | 取包步骤会失败；**除非**用已落地的包走离线路径 |
| `verify_upstream_version()`（读 `server/Cargo.toml`） | 本机**没有** 3.2.0-beta.2 的源码 tarball（只有 3.1.0 的），这条守卫本轮**跑不到** |

**本版的实际做法：离线构建。** 用已经下载到本地的两个官方包
（amd64 9,343,829 B / arm64 8,925,518 B，sha256 已钉进 `UPSTREAM_BUNDLE_SHA256` 并在
构建时二次校验），不依赖任何联网取包步骤。

### 9.3 §8.3 / §8.4 的结论要不要重新评估？

**结论不变（仍不换上游），但触发条件里多了两条，且其中一条已经触发。**

先看 §8.3 的三个痛点现状：

| 原痛点 | v3.1.0 时 | 现在（v3.2.0-beta.2） |
| --- | --- | --- |
| ① 每次发版都要自编 Rust 双架构 | ✅ 已解决 | ✅ **仍解决**（官方包照样带二进制 + sha256） |
| ② 面板漂移靠树哈希 + 人工回归 | ⚠️ 仍在 | ⚠️ **仍在**，但本轮回归成本很低（见下） |
| ③ 许可不是纯 MIT | ⚠️ 仍在 | ⚠️ **仍无法核实**（LICENSE 取不到了） |

本轮对 ② 的实测结论是**乐观**的：3.2.0-beta.2 虽然改了 69 个文件、新增一个顶层 JS，
但网关依赖的全部形态**一个都没变** —— `localStorage['agent2api.webKey']`、
`fetch('/api/...')` 根绝对写法、`register("sw.js",{scope:"/"})`、`index.html` 的
52 个资源引用与唯一的 `/m` 根绝对路径，全部与 3.1.0 同构。也就是说「树哈希钉死 +
人工回归」这条机制**这次只花了很低的成本就放行了**，② 的实际风险权重应当下调。

**但 9.2 带出一个全新风险，它才是现在最该盯的东西：上游仓库本身可能随时消失。**
§8.4 原来的触发条件是「上游再换一次仓库 / 面板再大改 / 许可收紧 / 改写模式被打破」，
建议**追加两条**：

1. **【已触发】上游仓库不可达（404 / 长期无响应）持续超过一个发布周期。**
   若 404 只是一次改名/误操作并在数天内恢复，本项目的离线构建可以照常走，
   不必换上游；**若持续不可达且拿不到新版本，就等于上游已经停更** —— 那时
   本项目被锁死在最后一版可用的官方包上，此时才是**认真启动**第 3 节
   CLIProxyAPI + cpa-multi-plugins 验证清单的时机（而不是现在）。
2. **拿不到上游源码时，`verify_upstream_version()` 长期失效。** 这条守卫是
   「tag 被移动 / 镜像给旧包」的探测器；源码不可得它就只能靠官方包 sha256 +
   二进制内版本串兜底。若 404 持续，应在 `build.py` 里补一条「从二进制内抽版本串
   并与 `UPSTREAM_VERSION` 比对」的替代守卫 —— **本轮未实现**，建议列入待办。

**一句话判断：仍然不换上游，但把「上游可用性」提升为与「面板形态漂移」同级的
持续监控项。** 现有 §5 的验证清单不需要因为这次 404 而提前启动。

### 9.4 本轮新增/待办（归本项目，不是换上游的事）

- **登出跳转回归**（实测，非阻断）：3.2.0 把登出改成三目
  `window.location.href = e ? "login.html" : "/login"`，而 `fngateway` 的
  `navigationLiteralRe` 要求 `href=` 后紧跟字面量，故**命中从 1 处降为 0 处** →
  飞牛下取 `"/login"` 分支且不补前缀，登出会跳到站点根而非 `{应用}/login`。
  修复二选一：产物层把两分支改裸相对 `login.html`，或网关正则补三目 `else` 分支 + 单测。
- **`UPSTREAM_UI_SHA256` 的口径**：这个值钉的是**打完 `patch_sw_scope` 之后**的
  `app/ui` 树哈希，不是上游原始 `ui/` 的哈希（`m/mobile.js` 会 +1 字节：
  544,898 → 544,899 B）。v3.2.0-beta.2 的正确值 = **`011b905c…`（213 文件）**，
  上游原始 `ui/` 的 `d388d79f…` **不能**用作此 pin。
- **双架构产物已实测**（第 5 轮，面板改由构建注入后重出）：
  · `agent2api-3.2.0-beta.2-1-amd64.fpk` = **12,563,435 B**（包内 `platform` = `x86`），
    sha256 = `8ad692e3fc1b77888a4c9446222f651a054d2c8eb045dc5c74ce1be53fb4987a`
  · `agent2api-3.2.0-beta.2-1-arm64.fpk` = **11,812,003 B**（包内 `platform` = `arm`），
    sha256 = `7322fbc3907179e34284efe22ef847f47906ef9a120ebef74421c682041894c7`

  以上为当前定稿值。此前各轮的中间体积均已作废，不再引用。
  两个包的 `app.tgz` 各 **220 个文件**（`app/ui/` 各 **216 个**
  = 上游 213 + `config` + 两个 icon；另 4 个为 `bin/` 双二进制与 `config/{privilege,resource}`）。
  包内 `bin/agent2api-server-linux-{amd64,arm64}` 分别为 14,352,784 B / 12,611,032 B，
  与官方包内二进制等长。

- **结构变更（2026-10-20）：上游面板不再提交进仓库。** `app/ui/` 现在只保留本项目的
  `config` 与 `images/`（3 个文件），面板 213 个文件由 `build.py` 的 `stage_ui()` 在构建时
  从 `.local-build/upstream-bundle-<arch>/ui/` 注入（拷贝前跑 `patch_sw_scope`），`.gitignore`
  用 `app/ui/*` + 放行 `config`/`images/` 把它挡在仓库外。`UPSTREAM_UI_SHA256` 语义不变
  （仍是补丁后的树哈希 `011b905c…`），但校验对象从「仓库副本」变成「官方包解包目录」，
  且**逐架构各校一次**。`--sync-upstream` 改为只解包；要落盘做人工 diff 用 `--materialize-ui`。
  **代价是牺牲 `git diff` 的可审阅性**（上游改面板不再出现在代码评审里，只能靠漂移探针 +
  解包目录对比发现）。对「换上游」判断的影响：无变化 —— 这条变更动的是本仓库的存放方式，
  不改变上游形态、许可或许可风险。
  ⚠️ **前置条件**：构建不再自包含，干净克隆后必须先 `python build.py --fetch-binaries`
  把官方包落到 `.local-build/`；缺失时 `build.py` fail-fast 退出（不会产出错包，但也出不了包）。

---

_本节所有数字均可在 `.local-build/scout320/recon.md`（侦察原始输出，含每条命令与原始返回）
与 `.local-build/builder320/logs/`（构建日志）中复核。_
