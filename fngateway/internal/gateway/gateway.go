// Package gateway 飞牛统一网关适配层。
//
// 职责：把挂在 /app/agent2api 子路径下的请求透明地代理到 agent2api 内部服务，
// 并完成子路径改写（HTML 绝对路径、重定向、Cookie 作用域、SSE），
// 使未做 base-path 适配的官方 Web 面板能直接在网关下工作；
// 另提供下游独立端口的直通代理（仅 /v1/* 与 /health，不做任何改写）。
//
// 与 cli2api 版的差异（agent2api 上游的实测结论，见 README「子路径适配」）：
//   - 面板没有任何前端路由：不读 location.pathname、不写 history，页面切换靠内存
//     状态。因此**不需要**构建期 router 补丁，运行时桥接即可覆盖全部动态 URL。
//   - 面板的 Web 壳（web_shim.rs，编译进二进制）自身用根绝对路径 fetch('/api/...')，
//     改不了源码，必须由桥接脚本在运行时补前缀。
//   - 面板的鉴权密钥存在 localStorage['agent2api.webKey']，服务端注入后浏览器侧
//     只需一个占位值（见 SeedScript）。
//
// 参考 fnos-developer skill references/gateway-proxy.md 与 D:\fnos 下
// fnos-workbuddy2api / deepseek.harness 两个已验证项目的实现。
package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"time"
)

// Prefix 网关挂载前缀（运行时注入）。
type Prefix struct {
	// Path 形如 /app/agent2api（无尾斜杠）。零值表示不做前缀处理。
	Path string
}

// NewPrefix 构造前缀对象；空值回落到本应用默认前缀。
func NewPrefix(p string) Prefix {
	p = strings.TrimRight(strings.TrimSpace(p), "/")
	if p == "" {
		p = "/app/agent2api"
	}
	return Prefix{Path: p}
}

// Strip 剥除前缀，返回以 / 开头的内部路径。
// safe 为 false 表示该路径本就不带前缀（调用方自行决定如何处理）。
//
// 循环剥除，保证幂等：万一请求被拦了两次（外层代理与内层代理都改写过，
// 或调用方误用），/app/x/app/x/api 只剥一层会剩下 /app/x/api 回源，
// 上游必然 404。这里一路剥到底。
func (p Prefix) Strip(path string) (string, bool) {
	ok := false
	for {
		if path == p.Path {
			return "/", true
		}
		if strings.HasPrefix(path, p.Path+"/") {
			path = path[len(p.Path):]
			ok = true
			continue
		}
		break
	}
	return path, ok
}

// ---------------------------------------------------------------------------
// 子路径改写
// ---------------------------------------------------------------------------

var (
	// HTML 中需要加前缀的路径属性：src/href/action/poster。
	// 这里先宽松地取出**任意**带引号的值，再由 shouldRewrite 判定 —— 因为
	// agent2api 面板是手写 HTML，资源引用是**裸相对路径**：
	//   <script src="icons.js">、<script src="islands/ui.js">
	// 而 cli2api 是 Vite 产物（./assets/x.js 或 /assets/x.js）。只匹配
	// 「以 / 或 ./ 开头」会漏掉裸相对路径，页面在网关下会白屏。
	htmlAttrRe = regexp.MustCompile(`(?i)\b(src|href|action|poster)\s*=\s*(["'])([^"']*)`)
	// 带 scheme 的绝对 URL（http:、https:、data:、blob:、mailto:、javascript:…）。
	schemeRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.\-]*:`)
	// Set-Cookie 的 Path=<value>。
	cookiePathRe = regexp.MustCompile(`(?i)\bpath\s*=\s*(/[^;,]*)`)
	// PWA manifest 链接：必须补 crossorigin，否则 manifest 请求不带凭证，
	// 被飞牛网关判为未登录（invalid token）。
	manifestLinkRe = regexp.MustCompile(`(?i)<link[^>]*rel\s*=\s*["']manifest["'][^>]*>`)
	// JS 字符串字面量里的 HTML 片段：src="..." / href="..." / document.write('...')。
	//
	// 这是 v3.1.0 新增的注入点（详见 RewriteScript）。属性名只要前面是空白、
	// 引号或括号就能命中 —— 也就是「位于某个 HTML 标签片段里」的写法，
	// 典型是 document.write('<script src="/i18n/...">')。
	// 值必须是「单个 / 开头」的站内绝对路径：用 '(/[^/"'][^"']*)' 表达
	// （RE2 不支持负向前瞻，所以把「第二个字符不是 /」写进字符类）。
	jsHTMLAttrRe = regexp.MustCompile(`(?i)([\s"'(])(src|href|action|poster)(\s*=\s*\\?["'])(/[^/"'][^"']*)`)
)

// shouldRewrite 判断某个属性值是否需要补前缀。
//
// 不改写的三类：空值/纯锚点、协议相对（//host/x）、带 scheme 的绝对 URL。
// 已带前缀的也不重复加，避免 /app/agent2api/app/agent2api/...。
func (p Prefix) shouldRewrite(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasPrefix(v, "#") {
		return false
	}
	// 协议相对 //host/x 不动。
	if strings.HasPrefix(v, "//") {
		return false
	}
	// 绝对 URL（含 data:/blob:/javascript:）不动。
	if schemeRe.MatchString(v) {
		return false
	}
	rel := strings.TrimPrefix(v, "./")
	if rel == p.Path || strings.HasPrefix(rel, p.Path+"/") {
		return false
	}
	return true
}

// RewriteHTML 改写 HTML 文档中的资源路径与 manifest 链接。
func (p Prefix) RewriteHTML(body []byte) []byte {
	s := p.rewriteAttrs(string(body))

	// PWA manifest 链接：必须补 crossorigin，否则 manifest 请求不带凭证，
	// 被飞牛网关判为未登录（invalid token）。v3.1.0 起面板才第一次带
	// <link rel="manifest">，这条从「预留」变成「每天都走」。
	s = manifestLinkRe.ReplaceAllStringFunc(s, func(m string) string {
		if strings.Contains(strings.ToLower(m), "crossorigin") {
			return m
		}
		return strings.TrimSuffix(m, ">") + ` crossorigin="use-credentials">`
	})

	return []byte(p.RewriteScript(s))
}

// rewriteAttrs 改写真实 HTML 标记里的 src/href/action/poster。
func (p Prefix) rewriteAttrs(s string) string {
	return htmlAttrRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := htmlAttrRe.FindStringSubmatch(m)
		if len(sub) != 4 {
			return m
		}
		attr, quote, val := sub[1], sub[2], sub[3]
		if !p.shouldRewrite(val) {
			return m
		}
		clean := path.Clean("/" + strings.TrimPrefix(strings.TrimSpace(val), "./"))
		return attr + "=" + quote + p.Path + clean
	})
}

// RewriteScript 改写嵌在 <script> 正文里的 HTML 片段中的资源路径。
//
// v3.1.0 的注入点：面板不再只有静态标签，还有运行期用 document.write 拼
// <script src=...> 的写法。典型是 index.html / m.html / login.html 里那段语言
// 引导脚本：
//
//	document.write('<script src="i18n/manifest.js"><\/script>');
//	document.write('<script src="i18n/' + locale + '/' + domains[d] + '.js"><\/script>');
//
// 这两条现在都是**裸相对路径**，实测（v3.1.0，经网关）都能正常加载：第一条
// 是完整字面量，HTML 属性改写本来就能给它补前缀；第二条是运行期拼接，正则
// 看不穿，但它拼进 URL 的前缀片段也是裸相对（i18n/），而文档基址已被
// <base href="{前缀}/"> 钉住，浏览器解析结果正确。
//
// 所以这条改写是**兜底**：管将来（或某个语言 / 域）把 src 写成根绝对路径
// "/i18n/..." 的情况 —— 根绝对路径不吃 <base> 的路径部分，会直接跑到站点根。
// 判定条件刻意收得很窄：只认「属性名位于某个 HTML 标签片段里」且「值是站内
// 根绝对路径」的写法，且跳过 JS 注释（注释里常写 <script src="/x"> 示例）。
func (p Prefix) RewriteScript(s string) string {
	if p.Path == "" || !strings.Contains(s, "/") {
		return s
	}
	// 先把注释区间算出来，落在注释里的匹配一律不动（只做保护，不解析语言）。
	comments := jsCommentSpans(s)
	spans := jsHTMLAttrRe.FindAllStringSubmatchIndex(s, -1)
	if len(spans) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, ix := range spans {
		head := s[ix[2]:ix[3]]
		attr := s[ix[4]:ix[5]]
		eq := s[ix[6]:ix[7]]
		val := s[ix[8]:ix[9]]
		if inAnySpan(comments, ix[0]) {
			continue
		}
		if val == p.Path || strings.HasPrefix(val, p.Path+"/") {
			continue
		}
		b.WriteString(s[last:ix[0]])
		b.WriteString(head + attr + eq + p.Path + val)
		last = ix[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// jsCommentSpans 找出 /* … */ 与 // …（行尾）两类注释的区间。
//
// 为什么需要：面板源码里大量注释用 <script src="x"> 做示例，正则会把它们当成
// 真标签。误改注释虽不影响执行，但会让网关输出与上游源码对不上，排障时反而
// 制造困惑。
//
// ── 为什么要做「字符串感知」（这条是修 bug，不是优化）────────────
// 最初这版只认 `//` 两个字面量，不区分它们是否在字符串里。上游面板是
// **单行 minified** 产物，于是任何一处 `https://…` 或 `"//cdn…"` 都会把
// `//` 之后**整行剩余部分**当成注释 —— 真正的代码（例如后面的
// `<script src="/…">`、`location.href='/login'`）因此被判定为「在注释里」
// 而**漏改**。漏改比误改危险：误改只影响注释文本，漏改会让面板在子路径下
// 真的跳到错误地址。
//
// 所以这里做一层最小词法状态：跟踪 ' " ` 三种引号字符串与「可能起始的正则
// 字面量」，只有**不在字符串里**的 `//` 才开启行注释。不求完整解析，但足以
// 避免「https:// 吞掉整行」。
// 代价：字符串里的 `//` 不再被当注释，于是字符串里的示例**会**被改写 ——
// 这正是要的取舍：宁可多改字符串，不可漏改代码。
func jsCommentSpans(s string) [][2]int {
	var spans [][2]int
	// prevSig：上一个「有意义」的字符（跳过空白），用于判断 `/` 是否可能是
	// 正则字面量的开头（只有表达式起始位置才可能）。
	var prevSig byte
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			if end := strings.Index(s[i+2:], "*/"); end >= 0 {
				spans = append(spans, [2]int{i, i + 2 + end + 2})
				i += 2 + end + 2
			} else {
				spans = append(spans, [2]int{i, len(s)})
				i = len(s)
			}
			continue
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			end := strings.IndexByte(s[i:], '\n')
			if end < 0 {
				spans = append(spans, [2]int{i, len(s)})
				i = len(s)
			} else {
				spans = append(spans, [2]int{i, i + end})
				i += end
			}
			continue
		case c == '"' || c == '\'' || c == '`':
			// 跳过整个字符串字面量（模板字符串同样整体跳过：面板产物里模板
			// 插值内几乎不出现注释语法，跳过只会让字符串里的示例被改写，
			// 方向正确）。
			quote := c
			i++
			for i < len(s) {
				if s[i] == '\\' {
					i += 2
					continue
				}
				if s[i] == quote {
					i++
					break
				}
				i++
			}
			prevSig = quote
			continue
		case c == '/' && regexMayStart(prevSig):
			// 正则字面量：跳到未转义的收尾 `/`，跳过字符类 [...] 里的 `/`。
			i++
			inClass := false
			closed := false
			for i < len(s) {
				ch := s[i]
				if ch == '\\' {
					i += 2
					continue
				}
				if ch == '[' {
					inClass = true
				} else if ch == ']' {
					inClass = false
				} else if ch == '/' && !inClass {
					i++
					closed = true
					break
				} else if ch == '\n' {
					break // 不是正则（跨行），退回普通字符处理
				}
				i++
			}
			if closed {
				prevSig = '/'
			} else {
				prevSig = c
			}
			continue
		default:
			if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
				prevSig = c
			}
			i++
		}
	}
	return spans
}

// regexMayStart 判断 prevSig 之后出现的 `/` 是不是正则字面量的开头。
//
// 只在「表达式起始位置」才可能。判错的方向刻意是「当成除法」—— 那样不会
// 误把除号当正则开头而吞掉后面一大段代码。
func regexMayStart(prevSig byte) bool {
	switch prevSig {
	case 0, '(', ',', '=', ':', '[', '!', '&', '|', '?', '{', '}', ';', '+', '-', '*', '%', '<', '>', '~', '^':
		return true
	}
	return false
}

// inAnySpan 判断 pos 是否落在任一半开区间 [a,b) 内。
func inAnySpan(spans [][2]int, pos int) bool {
	for _, sp := range spans {
		if pos >= sp[0] && pos < sp[1] {
			return true
		}
	}
	return false
}

// navigationLiteralRe 匹配「整页跳转到站内根绝对路径」的 JS 字面量。
//
// 只覆盖这些形态：location.href=/x、location.assign('/x')、location.replace('/x')，
// 前缀可以是 window. / top. / self. / parent.，也可以没有。
//
// 刻意**不**包含 `return '/x'`：实测 login.html 的 postLoginTarget() 确实
// `return '/?from=login'`，但那是登录成功后的导航，而本应用根本不启用面板
// 登录（无管理员账号，Key 通道），该函数不可达。为一条不可达路径放宽到
// 「任何 return 根绝对字面量」会无谓地放大误改面，不值得。
var navigationLiteralRe = regexp.MustCompile(
	`((?:(?:window|top|self|parent)\s*\.\s*)?location\s*\.\s*(?:href\s*=\s*|(?:assign|replace)\s*\(\s*))(["'])(/[^/"'\r\n][^"'\r\n]*)(["'])`)

// RewriteNavigationLiterals 把脚本里「整页跳转到根绝对路径」的字面量补上前缀。
//
// 为什么必须动 JS 正文（v3.1.0 实测结论）：
//   - 上游 Web 壳（编译进二进制，Web 壳源码即 server/src/web_shim.rs）会话失效
//     分支写死 `window.location.href = '/login'`；
//   - 面板产品代码 islands/ui.js 的登出函数写死
//     `window.location.href="/login"`（"退出登录"按钮）；
//   - 手机端 m/mobile.js 写死 `window.location.href="/login?next=%2Fm"`。
//
// 这三处都在 JS 字符串里，HTML 正则看不见；而 `location.href` 在 Chromium 里
// 是 [LegacyUnforgeable]，运行时 JS **无法**覆盖其 setter —— 浏览器实测（本仓库
// fngateway 的浏览器验证脚本，Chrome headless）：
//
//	Object.getOwnPropertyDescriptor(Location.prototype,'href').set === undefined
//	window.location.assign = spy  →  spy 不生效（assign 也是 unforgeable）
//
// 也就是说「运行时桥接拦 location.assign/replace」这条兜底是**无效的**，
// 只能改服务端下发的字面量。登出按钮在本项目的配置下**确实可见**
// （islands/ui.js 的 WP() 判 `platform==='web'`，而上游 Web 壳正是注入
// platform:'web'），所以这不是死代码。
//
// 幂等：已带前缀的字面量不再改写。只改字符串内容，不改变 JS 语法（引号 1 对 1）。
// 失败模式：把注释里恰好写成 `location.href = '/x'` 的示例也改了 —— 无副作用。
func (p Prefix) RewriteNavigationLiterals(body []byte) []byte {
	if p.Path == "" {
		return body
	}
	return navigationLiteralRe.ReplaceAllFunc(body, func(m []byte) []byte {
		sub := navigationLiteralRe.FindSubmatch(m)
		if len(sub) != 5 {
			return m
		}
		val := string(sub[3])
		if val == p.Path || strings.HasPrefix(val, p.Path+"/") {
			return m
		}
		return []byte(string(sub[1]) + string(sub[2]) + p.Path + val + string(sub[4]))
	})
}

// BaseTag 生成把文档基址钉在网关前缀下的 <base> 标签。
//
// 为什么必须有它：飞牛桌面入口的 url 是 /app/agent2api（**不带尾斜杠**），
// 文档 URL 因此就是 https://host/app/agent2api。这时任何裸相对引用都会按
// 「上一级目录」解析：
//
//	new URL('assets/providers/workbuddy.png', 'https://host/app/agent2api')
//	  → https://host/app/assets/providers/workbuddy.png
//
// agent2api 这一段被吃掉了。表现就是「添加账号 → 反代」里的模型图标全部不显示
// —— 图标（PROVIDER_ICONS / wbPresetProviders.iconOf）走的正是裸相对路径。
//
// 注入 <base href="{前缀}/"> 后，浏览器把文档基址固定成
// https://host/app/agent2api/，裸相对引用重新落回前缀之下。
//
// 它只影响相对引用：以 / 开头的根绝对路径只借用 base 的 origin，路径部分不受
// 影响，因此静态改写（RewriteHTML）与运行时桥接都不会被它打乱。
// 与 fnos-logmanager（ServeIndexWithBase）、nas（injectPrefix）、
// deepseek.harness（fnGatewayBridgeScript）三处的做法一致。
func (p Prefix) BaseTag() string {
	if p.Path == "" {
		return ""
	}
	return `<base href="` + p.Path + `/">`
}

// InjectHead 把片段插入 HTML 的 <head> 之后。
//
// 为什么是「紧随 <head> 之后」而不是「</head> 之前」：agent2api 的 Web 壳
// （server/src/web_shim.rs）把自己那段脚本作为 <head> 的第一个子元素注入，
// 而它**在脚本顶层**就把 localStorage['agent2api.webKey'] 读进了闭包变量
// （`var apiKey = readStoredKey()`）。种子脚本必须排在它前面才来得及改写
// localStorage —— 放到 </head> 之前就已经太晚，面板会拿不到密钥。
func InjectHead(body []byte, snippet string) []byte {
	if snippet == "" {
		return body
	}
	s := string(body)
	if idx := indexFold(s, "<head>"); idx >= 0 {
		at := idx + len("<head>")
		return []byte(s[:at] + snippet + s[at:])
	}
	// 无 <head>（异常文档）：退化为前置，至少保证脚本先于其它脚本执行。
	return []byte(snippet + s)
}

func indexFold(s, sub string) int {
	return strings.Index(strings.ToLower(s), strings.ToLower(sub))
}

// RewriteLocation 改写重定向 Location 的站内绝对路径。
func (p Prefix) RewriteLocation(loc string) string {
	if loc == "" {
		return loc
	}
	if !strings.HasPrefix(loc, "/") || strings.HasPrefix(loc, "//") {
		return loc
	}
	if loc == p.Path || strings.HasPrefix(loc, p.Path+"/") {
		return loc
	}
	return p.Path + loc
}

// RewriteCookie 把 Set-Cookie 的 Path 收窄/迁移到网关前缀下。
//
// 三种情况：
//   - Path=/          → Path=/app/agent2api/          （根作用域收窄到应用下）
//   - Path=/api/panel → Path=/app/agent2api/api/panel （子路径整体迁移）
//   - 已带前缀        → 原样保留，避免重复加
//
// 为什么必须做：飞牛网关把应用挂在子路径上，浏览器按「整站」理解 Path=/，
// Cookie 既越界又可能被同域其它应用覆盖；而上游 agent2api 的会话 Cookie
// （agent2api-panel / agent2api-panel-rt）都带 Path（见 server/src/server/access.rs），
// 不改写会直接导致「登录后掉线」。
func (p Prefix) RewriteCookie(v string) string {
	return cookiePathRe.ReplaceAllStringFunc(v, func(m string) string {
		sub := cookiePathRe.FindStringSubmatch(m)
		if len(sub) != 2 {
			return m
		}
		val := strings.TrimSpace(sub[1])
		if val == p.Path || strings.HasPrefix(val, p.Path+"/") {
			return "Path=" + val
		}
		if val == "/" {
			return "Path=" + p.Path + "/"
		}
		return "Path=" + p.Path + val
	})
}

// ---------------------------------------------------------------------------
// PWA manifest 正文改写
// ---------------------------------------------------------------------------

// isManifestType 判断响应是不是 Web App Manifest。
//
// 按 MIME 判定，而不是按 `.webmanifest` 后缀：后缀匹配会漏掉上游将来改用
// application/json 下发的情况，又会误伤任何同名的普通 JSON。上游两个 manifest
// （ui/manifest.webmanifest、ui/m/manifest.webmanifest）都用
// application/manifest+json，实测一致。
func isManifestType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct == "application/manifest+json" || ct == "application/manifest"
}

// RewriteManifest 改写 Web App Manifest 正文里的根绝对路径。
//
// 为什么必须改（v3.1.0 新增，实测确认）：上游 manifest 是**按站点根 / 写的**，
// 两个文件都是 start_url="/"（手机端是 "/m.html"）、scope="/"，icons[].src
// 与 shortcuts[].url 也混着根绝对路径。经 /app/agent2api 前缀后：
//
//   - scope="/" 让 PWA 的作用域**盖到整个飞牛站点**，与其它飞牛应用的 PWA
//     作用域重叠 —— 谁先注册谁生效，表现为「从主屏打开的是别的应用」；
//   - start_url="/" 会把独立窗口启动到飞牛门户根，而不是面板；
//   - 根绝对图标/快捷方式直接 404（图标显示成通用方块）。
//
// 为什么只能改正文：<link rel="manifest"> 的 href 能靠 HTML 改写补前缀，但
// manifest **文件内容**是浏览器解析的 JSON，HTML 正则看不到。这是本适配层里
// 唯一必须动 JSON 正文的地方 —— 代价是要保证输出仍是合法 JSON。
//
// 改写规则（全部满足才动，否则原样返回）：
//   - 只改键名语义上是「站内路径」的字段：start_url、scope、id、icons[].src、
//     screenshots[].src、shortcuts[].url、shortcuts[].icons[].src；
//   - 只改**以单个 / 开头的站内绝对路径**；相对路径一律不动 —— manifest 里的
//     相对 URL 是相对 **manifest 文件自己的 URL** 解析的（即
//     https://host/app/agent2api/），本来就是对的，补前缀反而会变成
//     /app/agent2api/m/../xxx 这种错位；
//   - 绝对 URL（http:/https:/data:/blob:）、协议相对 //host、已带前缀的都不动。
//
// 幂等：已带前缀的直接返回原文，因此多层代理重复改写不会出现双重前缀。
// 失败模式：输出不再合法 JSON 时，浏览器整份 manifest 作废（PWA 降级为普通
// 网页，**不会白屏**，面板本身照常可用）。这里用 json.Indent 重新缩进而不是
// 字符串替换，就是为了让「输出一定可解析」成为构造性保证；且解析失败时
// 原样返回原文，绝不返回半成品。
// maxManifestRewriteBytes 是 RewriteManifest 的体积上限。
//
// manifest 正常只有几 KB；超过这个量的「manifest」几乎一定不是 manifest
// （isManifestType 走宽口径，任何 */json 都会落到这里）。超限直接原样放行，
// 避免为一个非 manifest 的大 JSON 付「Unmarshal + 重新编码」的内存与 CPU
// 代价（实测 3.4 MB 输入经 Encoder 重排会膨胀到 7 MB）。1 MiB 远高于真实
// manifest，又不至于让误判路径失控。
const maxManifestRewriteBytes = 1 << 20

func (p Prefix) RewriteManifest(body []byte) []byte {
	if p.Path == "" {
		return body
	}
	// 体积上限：manifest 正常只有几 KB。一个「被当成 manifest 下发的大 JSON」
	// 会被整份 Unmarshal + 重新编码，既吃内存又会因 Encoder 重排而膨胀
	// （实测 3.4 MB 输入 → 7.0 MB 输出）。超限就原样放行：宁可 PWA 在子路径
	// 下降级，也不能让网关为一个大文件耗尽内存。
	if len(body) > maxManifestRewriteBytes {
		return body
	}
	var doc any
	// UseNumber：把 JSON 数字保留成原文，避免 Unmarshal 到 any 时一律走 float64。
	// 否则 `{"n": 12345678901234567890}` 会被静默改成 12345678901234567000
	// （超过 2^53 的整数丢精度），9007199254740993 变成 …992。
	// 当前上游两个 manifest 没有任何数值字段，但「网关悄悄改写上游数据」这类
	// 缺陷一旦发生极难排查，所以这里从源头堵住。
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return body // 不是 JSON：原样放行，不破坏上游响应
	}
	root, ok := doc.(map[string]any)
	if !ok {
		return body
	}
	changed := false
	fix := func(v any) (any, bool) {
		s, ok := v.(string)
		if !ok {
			return v, false
		}
		fixed, ok := p.manifestURL(s)
		return fixed, ok
	}
	setKey := func(m map[string]any, key string) {
		if v, ok := fix(m[key]); ok {
			m[key] = v
			changed = true
		}
	}
	// 顶层单值字段。
	for _, k := range []string{"start_url", "scope", "id"} {
		setKey(root, k)
	}
	// 图标 / 截图数组。
	for _, k := range []string{"icons", "screenshots"} {
		arr, _ := root[k].([]any)
		for _, item := range arr {
			if m, ok := item.(map[string]any); ok {
				setKey(m, "src")
			}
		}
	}
	// 快捷方式：url 与它们各自的图标。
	if shortcuts, ok := root["shortcuts"].([]any); ok {
		for _, item := range shortcuts {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			setKey(m, "url")
			if arr, ok := m["icons"].([]any); ok {
				for _, ic := range arr {
					if im, ok := ic.(map[string]any); ok {
						setKey(im, "src")
					}
				}
			}
		}
	}
	if !changed {
		return body
	}

	// 保持上游的缩进风格（2 空格），Content-Length 由调用方按新长度回填。
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // 面板文案里有 & < >，不能被转成 \u0026 之类的实体
	enc.SetIndent("", "  ")
	if err := enc.Encode(root); err != nil {
		return body
	}
	// Encoder 会补一个换行，上游文件末尾也有，保持一致即可。
	return buf.Bytes()
}

// manifestURL 给 manifest 里的站内绝对路径补前缀；不需要改时第二个返回值为 false。
func (p Prefix) manifestURL(v string) (string, bool) {
	s := strings.TrimSpace(v)
	if !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") {
		return v, false
	}
	if s == p.Path || strings.HasPrefix(s, p.Path+"/") {
		return v, false
	}
	// 只补路径部分，query / fragment 原样保留（shortcuts[].url 常见 /m.html#checkin）。
	rest := s
	tail := ""
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest, tail = rest[:i], rest[i:]
	}
	clean := path.Clean("/" + strings.TrimPrefix(rest, "/"))
	if clean == "/" {
		return p.Path + "/" + tail, true
	}
	return p.Path + clean + tail, true
}

// ---------------------------------------------------------------------------
// 反向代理
// ---------------------------------------------------------------------------

// Options 反代构造参数。
type Options struct {
	// Prefix 网关前缀；零值表示直通（下游独立端口）。
	Prefix Prefix
	// InternalAddr 内部服务地址，形如 127.0.0.1:3065。
	InternalAddr string
	// Timeout 建立到内部服务的连接与响应头超时。0 表示使用默认值。
	Timeout time.Duration
	// RewriteHTML 为 true 时改写真响应（HTML/Location/Cookie）。
	RewriteHTML bool
	// Bridge 返回注入 HTML 的脚本片段（RewriteHTML 为 true 时生效）。
	Bridge func() string
	// Prepare 在转发前改写出站请求（例如注入面板密钥）。
	Prepare func(*http.Request)
}

// NewProxy 构造到内部服务的反向代理。
func NewProxy(opt Options) (http.Handler, error) {
	if strings.TrimSpace(opt.InternalAddr) == "" {
		return nil, errors.New("gateway: 内部服务地址为空")
	}
	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	target := &url.URL{Scheme: "http", Host: opt.InternalAddr}
	rp := httputil.NewSingleHostReverseProxy(target)

	// 默认 ErrorHandler 返回 502 纯文本；这里给友好 HTML 并记录。
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("网关代理失败 %s %s: %v", r.Method, r.URL.Path, err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8">`+
			`<body style="font-family:system-ui;padding:40px;background:#0f1115;color:#e6e6e6">`+
			`<h2>⚠️ Agent2API 服务不可达</h2>`+
			`<p>上游 agent2api 可能仍在启动中，请稍候刷新；若持续出现，请在飞牛应用中心重启本应用。</p>`+
			`<pre style="color:#888">`+htmlEscape(err.Error())+`</pre></body>`)
	}

	// DisableCompression：避免拿到 gzip 内容后无法做 HTML 改写。
	// 回源是明文 HTTP 回环，不需要 HTTP/2 协商。
	rp.Transport = &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		MaxIdleConns:          64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    true,
		ForceAttemptHTTP2:     false,
		ResponseHeaderTimeout: timeout,
	}

	orig := rp.Director
	rp.Director = func(req *http.Request) {
		orig(req)

		// 1) 剥除网关前缀（Path 与 RawPath 都要处理，否则编码路径会错乱）。
		if opt.Prefix.Path != "" {
			internal, _ := opt.Prefix.Strip(req.URL.Path)
			if !strings.HasPrefix(internal, "/") {
				internal = "/" + internal
			}
			req.URL.Path = internal
			if req.URL.RawPath != "" {
				rawInternal, _ := opt.Prefix.Strip(req.URL.RawPath)
				if !strings.HasPrefix(rawInternal, "/") {
					rawInternal = "/" + rawInternal
				}
				req.URL.RawPath = rawInternal
			}
		}

		// 2) 清理干扰内部服务或可被客户端伪造的头。
		req.Header.Del("Accept-Encoding")
		req.Header.Del("X-Forwarded-Host")
		req.Header.Del("X-Forwarded-Proto")
		req.Header.Set("X-Forwarded-Prefix", opt.Prefix.Path)
		// 本应用以飞牛登录态为唯一身份（见 gate.go），
		// 内部服务只认密钥，不读 X-Trim-*，无需为它们盖章。

		// 3) 出站改写（控制台密钥注入等）。
		if opt.Prepare != nil {
			opt.Prepare(req)
		}
	}

	rp.ModifyResponse = func(resp *http.Response) error {
		if !opt.RewriteHTML {
			tuneSSE(resp)
			return nil
		}

		// 3) 重定向 Location 改写。
		if loc := resp.Header.Get("Location"); loc != "" {
			resp.Header.Set("Location", opt.Prefix.RewriteLocation(loc))
		}
		// 4) Cookie 作用域改写。
		if cookies, ok := resp.Header["Set-Cookie"]; ok {
			for i, c := range cookies {
				cookies[i] = opt.Prefix.RewriteCookie(c)
			}
		}

		// 5) SSE 流式响应：关闭缓冲，否则事件流被攒住。
		if tuneSSE(resp) {
			return nil
		}

		ct := resp.Header.Get("Content-Type")
		switch {
		case strings.HasPrefix(ct, "text/html"):
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				return err
			}
			body = opt.Prefix.RewriteHTML(body)
			// Web 壳（编译进上游二进制的那段）里写死了根绝对跳转，改不了源码，
			// 只能在注入阶段把字面量换成带前缀的版本。
			body = opt.Prefix.RewriteNavigationLiterals(body)
			// 注入顺序有讲究：<base> 必须最靠前（浏览器要求它先于任何会解析
			// URL 的元素出现），种子脚本其次（要抢在 Web 壳读 localStorage
			// 之前），桥接脚本最后。三者合成一个片段一次插入。
			snippet := opt.Prefix.BaseTag()
			if opt.Bridge != nil {
				snippet += opt.Bridge()
			}
			body = InjectHead(body, snippet)
			resp.Body = io.NopCloser(strings.NewReader(string(body)))
			resp.ContentLength = int64(len(body))
			resp.Header.Set("Content-Length", fmt.Sprint(len(body)))
			// 面板页面每次都要重新注入脚本与 no-store，禁止中间层缓存。
			resp.Header.Set("Cache-Control", "no-store")
			// 改写会注入脚本；若上游带了 CSP，注入会被拦掉。
			resp.Header.Del("Content-Security-Policy")
			resp.Header.Del("Content-Security-Policy-Report-Only")
		case isManifestType(ct):
			// PWA manifest：唯一需要动 JSON 正文的地方（理由见 RewriteManifest）。
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				return err
			}
			fixed := opt.Prefix.RewriteManifest(body)
			if !bytes.Equal(fixed, body) {
				resp.Body = io.NopCloser(bytes.NewReader(fixed))
				resp.ContentLength = int64(len(fixed))
				resp.Header.Set("Content-Length", fmt.Sprint(len(fixed)))
				// 正文按前缀改写过了；上游的 no-store 若缺失，补上，
				// 避免中间层把「按某个前缀改写过的」版本缓存给另一个前缀。
				resp.Header.Set("Cache-Control", "no-store")
			} else {
				// 未改动也必须把已读空的 Body 换回来：上面已经 ReadAll + Close，
				// 而 ReverseProxy 之后还会从 resp.Body 拷正文。漏了这一步，
				// 响应就会变成「Content-Length 有、正文为空」—— 浏览器拿到的
				// 是一个被截断的 manifest / 空白脚本。
				resp.Body = io.NopCloser(bytes.NewReader(body))
			}
		case isJavaScriptType(ct):
			// 脚本正文里的根绝对跳转（location.href/assign/replace）。
			// 理由与实测见 RewriteNavigationLiterals：location.href 的 setter
			// 在 Chromium 里无法用运行时 JS 覆盖，只能在这里改字面量。
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				return err
			}
			fixed := opt.Prefix.RewriteNavigationLiterals(body)
			if !bytes.Equal(fixed, body) {
				resp.Body = io.NopCloser(bytes.NewReader(fixed))
				resp.ContentLength = int64(len(fixed))
				resp.Header.Set("Content-Length", fmt.Sprint(len(fixed)))
				resp.Header.Set("Cache-Control", "no-store")
			} else {
				// 同上：未改动也要换回 Body，否则 CopyResponse 复制到空正文。
				resp.Body = io.NopCloser(bytes.NewReader(body))
			}
		}

		// 6) Service Worker 的 scope 放行头。
		//
		// 只作为「将来上游改成相对 scope」时的前置条件，**不解决当前问题** ——
		// 浏览器实测（Chrome headless，本仓库验证脚本）证明：脚本在
		// {前缀}/sw.js、注册写死 scope:"/" 时，即便响应带
		// Service-Worker-Allowed: {前缀}/，仍然被拒：
		//
		//	SecurityError: The path of the provided scope ('/') is not under
		//	the max scope allowed (set by Service-Worker-Allowed: '/app/agent2api/')
		//
		// 也就是说这个头放宽的是「脚本目录 → 前缀」，而 upstream 要的是
		// 「前缀 → 站点根」，超出这个头能授权的范围。真正的解法只能是上游把
		// scope 改成 './'（相对脚本 URL）或干脆去掉 scope 参数。
		//
		// 上游把注册包在 .catch 里，失败只是把 swState 置成 unsupported
		// （静默降级、离线壳不可用），**不会白屏**，面板照常工作。
		// 这里补头无害且能让 scope 落在前缀内的将来版本直接可用，故保留。
		if isServiceWorkerRequest(resp.Request) {
			if resp.Header.Get("Service-Worker-Allowed") == "" {
				resp.Header.Set("Service-Worker-Allowed", opt.Prefix.Path+"/")
			}
		}
		return nil
	}

	return rp, nil
}

// isJavaScriptType 判断响应是不是脚本。
//
// 只认 text/javascript 与 application/javascript：上游实测用
// text/javascript; charset=utf-8（ui/sw.js、islands/ui.js、m/mobile.js 一致），
// 历史上也用过 application/javascript。**不认 text/plain**，避免把普通文本
// 也读进内存改写；也没必要认，上游不会那样下发。
//
// 为什么要动脚本正文：见 RewriteNavigationLiterals。这些文件都是 no-store，
// 不会污染中间缓存。
func isJavaScriptType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct == "text/javascript" || ct == "application/javascript" ||
		ct == "application/x-javascript"
}

// isServiceWorkerRequest 判断请求是不是 Service Worker 脚本（按路径判定）。
//
// 用「剥掉网关前缀后」的内部路径判定，这样无论上游把 sw.js 放在根还是子目录
// 都能命中；本应用只有 ui/sw.js 一个。用 resp.Request 而不是外部变量，
// 保证并发安全（每个请求有自己的 Request）。
func isServiceWorkerRequest(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	p := req.URL.Path
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		p = p[i+1:]
	}
	return p == "sw.js"
}

// tuneSSE 针对事件流关闭缓冲；返回 true 表示该响应按 SSE 处理。
func tuneSSE(resp *http.Response) bool {
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return false
	}
	resp.Header.Set("Cache-Control", "no-cache, no-transform")
	resp.Header.Set("X-Accel-Buffering", "no")
	resp.Header.Del("Content-Length")
	return true
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// ---------------------------------------------------------------------------
// 监听
// ---------------------------------------------------------------------------

// ListenUnix 在指定路径上监听 Unix Socket（先清理残留），权限 0600。
func ListenUnix(path string) (net.Listener, error) {
	if _, err := os.Stat(path); err == nil {
		if rmErr := os.Remove(path); rmErr != nil {
			return nil, fmt.Errorf("清理旧 Socket %s 失败: %w", path, rmErr)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("监听 Unix Socket %s 失败: %w", path, err)
	}
	// 网关以 root 身份连接，收紧到属主可读写。
	if err := os.Chmod(path, 0o600); err != nil {
		log.Printf("警告：设置 Socket 权限失败: %v", err)
	}
	return ln, nil
}
