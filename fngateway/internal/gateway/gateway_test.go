package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 前缀与改写规则
// ---------------------------------------------------------------------------

func TestPrefixStrip(t *testing.T) {
	p := NewPrefix("/app/agent2api")
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"/app/agent2api", "/", true},
		{"/app/agent2api/", "/", true},
		{"/app/agent2api/api/session", "/api/session", true},
		{"/app/agent2api/islands/ui.js", "/islands/ui.js", true},
		{"/app/agent2apix/api", "/app/agent2apix/api", false},
		{"/other", "/other", false},
		// 幂等：被重复加前缀时一路剥到底，不能只剥一层留下 /app/agent2api/api。
		{"/app/agent2api/app/agent2api/api/session", "/api/session", true},
		{"/app/agent2api/app/agent2api", "/", true},
		{"/app/agent2api/app/agent2api/", "/", true},
	}
	for _, c := range cases {
		got, ok := p.Strip(c.in)
		if got != c.want || ok != c.wantOK {
			t.Errorf("Strip(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

func TestNewPrefixDefault(t *testing.T) {
	if got := NewPrefix("").Path; got != "/app/agent2api" {
		t.Fatalf("NewPrefix(\"\") = %q", got)
	}
	if got := NewPrefix("/app/x/").Path; got != "/app/x" {
		t.Fatalf("NewPrefix 未去掉尾斜杠: %q", got)
	}
}

// 文档 URL 不带尾斜杠时（桌面入口 url = /app/agent2api），裸相对引用会被浏览器
// 解析到上一级，模型图标（assets/providers/*.png）全部 404。
// 注入 <base href="{前缀}/"> 把文档基址钉回前缀之下。
func TestBaseTag(t *testing.T) {
	p := NewPrefix("/app/agent2api")
	if got, want := p.BaseTag(), `<base href="/app/agent2api/">`; got != want {
		t.Fatalf("BaseTag() = %q, want %q", got, want)
	}
	// 前缀本身已去掉尾斜杠，这里再补一个，正好一个且只有一个。
	if strings.Contains(p.BaseTag(), `//"`) {
		t.Fatalf("BaseTag 出现双斜杠: %s", p.BaseTag())
	}
	// 直通模式（无前缀）不注入。
	if got := (Prefix{}).BaseTag(); got != "" {
		t.Fatalf("空前缀应返回空串，实际 %q", got)
	}
}

// agent2api 面板的资源引用是相对路径（<script src="islands/ui.js">），
// 文档 URL 可能是 /app/agent2api（不带尾斜杠），必须改写成绝对前缀路径，
// 否则浏览器会解析到上一级 /app/islands/ui.js。
func TestRewriteHTML(t *testing.T) {
	p := NewPrefix("/app/agent2api")
	in := `<head></head><body>` +
		`<script src="islands/ui.js"></script>` +
		`<link href="./css/tokens.css" rel="stylesheet">` +
		`<img src="/app/agent2api/keep.png">` +
		`<a href="//cdn.example.com/x">c</a>` +
		`<a href="https://other.example.com/y">d</a>` +
		`</body>`
	got := string(p.RewriteHTML([]byte(in)))

	wants := []string{
		`src="/app/agent2api/islands/ui.js"`,
		`href="/app/agent2api/css/tokens.css"`,
		`src="/app/agent2api/keep.png"`, // 已带前缀，原样保留
		`href="//cdn.example.com/x"`,    // 协议相对，不动
		`href="https://other.example.com/y"`,
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("改写结果缺少 %q\n实际: %s", w, got)
		}
	}
	if strings.Contains(got, "/app/agent2api/app/agent2api") {
		t.Errorf("出现重复前缀: %s", got)
	}
}

func TestRewriteHTMLManifestCrossorigin(t *testing.T) {
	p := NewPrefix("/app/agent2api")
	got := string(p.RewriteHTML([]byte(`<link rel="manifest" href="site.webmanifest">`)))
	if !strings.Contains(got, `crossorigin="use-credentials"`) {
		t.Errorf("manifest 链接未补 crossorigin: %s", got)
	}
	// 已有 crossorigin 的不重复加。
	got = string(p.RewriteHTML([]byte(`<link rel="manifest" href="m.webmanifest" crossorigin="anonymous">`)))
	if strings.Count(got, "crossorigin") != 1 {
		t.Errorf("crossorigin 被重复添加: %s", got)
	}
}

func TestRewriteLocation(t *testing.T) {
	p := NewPrefix("/app/agent2api")
	cases := map[string]string{
		"/login":                  "/app/agent2api/login",
		"/app/agent2api/login":    "/app/agent2api/login",
		"//evil.example.com/x":    "//evil.example.com/x",
		"https://a.example.com/b": "https://a.example.com/b",
		"relative/path":           "relative/path",
		"":                        "",
	}
	for in, want := range cases {
		if got := p.RewriteLocation(in); got != want {
			t.Errorf("RewriteLocation(%q) = %q, want %q", in, got, want)
		}
	}
}

// 面板 Cookie 的 Path 必须收窄到前缀下，否则浏览器把它当成整站 Cookie，
// 既越界又可能在别的应用下互相覆盖。
func TestRewriteCookie(t *testing.T) {
	p := NewPrefix("/app/agent2api")
	cases := map[string]string{
		"agent2api-panel=abc; Path=/; HttpOnly":             "agent2api-panel=abc; Path=/app/agent2api/; HttpOnly",
		"agent2api-panel-rt=abc; Path=/api/panel; HttpOnly": "agent2api-panel-rt=abc; Path=/app/agent2api/api/panel; HttpOnly",
		"a=b; path=/; SameSite=Lax":                         "a=b; Path=/app/agent2api/; SameSite=Lax",
		"a=b; Path=/app/agent2api/x; HttpOnly":              "a=b; Path=/app/agent2api/x; HttpOnly",
		"a=b; Path=/app/agent2api; HttpOnly":                "a=b; Path=/app/agent2api; HttpOnly",
		"a=b; HttpOnly":                                     "a=b; HttpOnly",
	}
	for in, want := range cases {
		if got := p.RewriteCookie(in); got != want {
			t.Errorf("RewriteCookie(%q) = %q, want %q", in, got, want)
		}
	}
}

// 种子脚本必须先于服务端注入的 Web 壳：它得排在 <head> 之后第一个位置。
func TestInjectHeadOrder(t *testing.T) {
	shim := `<script>var apiKey=localStorage.getItem('agent2api.webKey')||'';</script>`
	in := `<html><head>` + shim + `</head><body></body></html>`
	got := string(InjectHead([]byte(in), `<script data-fn-gateway-seed="1">S</script>`))

	seedAt := strings.Index(got, "data-fn-gateway-seed")
	shimAt := strings.Index(got, "apiKey=localStorage")
	if seedAt < 0 || shimAt < 0 {
		t.Fatalf("注入失败: %s", got)
	}
	if seedAt > shimAt {
		t.Fatalf("种子脚本必须排在 Web 壳之前（seed@%d shim@%d）: %s", seedAt, shimAt, got)
	}
}

func TestInjectHeadNoHead(t *testing.T) {
	got := string(InjectHead([]byte(`<body>x</body>`), `S`))
	if !strings.HasPrefix(got, "S") {
		t.Fatalf("无 <head> 时应前置注入: %s", got)
	}
}

// ---------------------------------------------------------------------------
// 注入脚本
// ---------------------------------------------------------------------------

func TestSeedScript(t *testing.T) {
	s := SeedScript()
	// 键名必须与上游 web_shim.rs 的 KEY_STORAGE 完全一致，否则面板读不到。
	if !strings.Contains(s, `"agent2api.webKey"`) {
		t.Errorf("SeedScript 未使用 agent2api.webKey: %s", s)
	}
	if !strings.Contains(s, `data-fn-gateway-seed="1"`) {
		t.Errorf("SeedScript 缺少标记属性: %s", s)
	}
	// 必须能挡住「退出登录」把用户卡住：removeItem / 空 setItem 都要回填。
	if !strings.Contains(s, "removeItem") || !strings.Contains(s, "setItem") {
		t.Errorf("SeedScript 未覆盖 setItem/removeItem: %s", s)
	}
}

func TestBridgeScript(t *testing.T) {
	s := BridgeScript(NewPrefix("/app/agent2api"))
	for _, w := range []string{
		`data-fn-gateway-bridge="1"`,
		`var P="/app/agent2api"`,
		`window.__fnGatewayBase=P`,
		// 前缀改写与鉴权头迁移都要在（后者是飞牛 1.2.0604+ 拦 Authorization 的必需项）。
		"x-api-key",
		"Authorization",
		"fetch",
		"WebSocket",
	} {
		if !strings.Contains(s, w) {
			t.Errorf("BridgeScript 缺少 %q", w)
		}
	}
	// 原始字符串里不能出现反引号，否则 Go 源码会编译不过（这里顺带兜住）。
	if strings.Contains(s, "`") {
		t.Errorf("BridgeScript 含反引号")
	}
}

// 桥接必须按「前缀 + /」解析相对引用。
//
// 若仍按 window.location.href 解析：文档 URL 是 /app/agent2api（无尾斜杠），
// 裸相对引用 assets/providers/x.png 会解析成 /app/assets/providers/x.png，
// 再补前缀就成了 /app/agent2api/app/assets/providers/x.png —— 双重前缀 404，
// 正是「添加账号 → 反代」里模型图标全部不显示的成因。
func TestBridgeScriptResolvesRelativeToPrefix(t *testing.T) {
	s := BridgeScript(NewPrefix("/app/agent2api"))
	if !strings.Contains(s, "var RESOLVE_BASE=") {
		t.Fatalf("桥接缺少解析基准 RESOLVE_BASE: %s", s)
	}
	// 基准必须归一成绝对 URL，且落在前缀之下（new URL 的第二个参数必须是绝对 URL）。
	if !strings.Contains(s, "new URL(P+'/',window.location.href)") {
		t.Errorf("解析基准未按前缀归一化: %s", s)
	}
	// toGw 内部不得再拿 window.location.href 当基准（那会把相对路径解到上一级）。
	start := strings.Index(s, "function toGw(v){")
	if start < 0 {
		t.Fatalf("未找到 toGw: %s", s)
	}
	body := s[start:]
	end := strings.Index(body, "function str(url)")
	if end < 0 {
		t.Fatalf("未找到 str(): %s", body)
	}
	if strings.Contains(body[:end], "window.location.href") {
		t.Errorf("toGw 仍以 window.location.href 为基准: %s", body[:end])
	}
}

// jsString 必须转义任何可能提前闭合 <script> 的字符。
func TestJsString(t *testing.T) {
	if got := jsString(`/a"b</script>`); strings.Contains(got, "<") || strings.Contains(got, `"b`) {
		t.Fatalf("jsString 未转义危险字符: %s", got)
	}
	if got := jsString("/app/agent2api"); got != `"/app/agent2api"` {
		t.Fatalf("jsString 正常字符被改坏: %s", got)
	}
}

// ---------------------------------------------------------------------------
// 管理员门
// ---------------------------------------------------------------------------

func adminRequest(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	r.Header.Set("X-Trim-Userid", "1000")
	r.Header.Set("X-Trim-Isadmin", "true")
	return r
}

func TestAdminOnly(t *testing.T) {
	p := NewPrefix("/app/agent2api")
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	h := AdminOnly(next, p)

	cases := []struct {
		name        string
		req         *http.Request
		wantStatus  int
		wantCT      string
		wantContain string
	}{
		{"非管理员访问 API 得到 JSON 403", httptest.NewRequest("GET", "/app/agent2api/api/session", nil), 403, "application/json", "forbidden"},
		{"非管理员访问页面得到 HTML 403", httptest.NewRequest("GET", "/app/agent2api/", nil), 403, "text/html", "需要管理员权限"},
		{"缺少 Userid 视为非管理员", httptest.NewRequest("GET", "/app/agent2api/api/session", nil), 403, "application/json", "forbidden"},
		{"管理员放行", adminRequest("GET", "/app/agent2api/api/session"), 200, "application/json", "ok"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, c.req)
		if rec.Code != c.wantStatus {
			t.Errorf("%s: 状态码 = %d, want %d", c.name, rec.Code, c.wantStatus)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, c.wantCT) {
			t.Errorf("%s: Content-Type = %q, want 含 %q", c.name, ct, c.wantCT)
		}
		if !strings.Contains(rec.Body.String(), c.wantContain) {
			t.Errorf("%s: 响应体缺少 %q: %s", c.name, c.wantContain, rec.Body.String())
		}
	}
}

// IsAdmin 只认网关注入的头，且 Isadmin 只接受真值。
func TestIsAdminTruthiness(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", " yes "} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("X-Trim-Userid", "1")
		r.Header.Set("X-Trim-Isadmin", v)
		if !IsAdmin(r) {
			t.Errorf("Isadmin=%q 应判为管理员", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "2"} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("X-Trim-Userid", "1")
		r.Header.Set("X-Trim-Isadmin", v)
		if IsAdmin(r) {
			t.Errorf("Isadmin=%q 不应判为管理员", v)
		}
	}
}

// ---------------------------------------------------------------------------
// 反代端到端（httptest 上游）
// ---------------------------------------------------------------------------

// fakeUpstream 模拟 agent2api 的关键行为，路由形态与上游
// server/src/server/http.rs + static_files.rs 一致：
// 根路径给 HTML（head 里第一个脚本就是 Web 壳），/api/* 无密钥 401、有密钥 200，
// 另有重定向 / Cookie / SSE 三个探针。
func fakeUpstream(t *testing.T, wantKey string) (*httptest.Server, *string) {
	t.Helper()
	var seenKey string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<html><head><script>var apiKey="";</script></head>`+
				`<body><script src="islands/ui.js"></script></body></html>`)
		case "/api/session":
			seenKey = r.Header.Get("x-api-key")
			if seenKey != wantKey {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(401)
				_, _ = io.WriteString(w, `{"error":{"type":"panel_login_required"}}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"success":true,"data":{"accounts":[]}}`)
		case "/api/redirect":
			w.Header().Set("Location", "/login")
			w.WriteHeader(302)
		case "/api/cookie":
			http.SetCookie(w, &http.Cookie{Name: "agent2api-panel", Value: "v", Path: "/"})
			w.WriteHeader(204)
		case "/api/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: hi\n\n")
		default:
			w.WriteHeader(404)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &seenKey
}

func newPanelHandler(t *testing.T, upstream, key string) http.Handler {
	t.Helper()
	p := NewPrefix("/app/agent2api")
	h, err := NewProxy(Options{
		Prefix:       p,
		InternalAddr: strings.TrimPrefix(upstream, "http://"),
		RewriteHTML:  true,
		Bridge:       func() string { return SeedScript() + BridgeScript(p) },
		Prepare: func(r *http.Request) {
			if key != "" && strings.HasPrefix(r.URL.Path, "/api/") {
				r.Header.Set("x-api-key", key)
			}
		},
	})
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	return AdminOnly(h, p)
}

// 面板密钥由服务端注入：浏览器送空值也能拿到 200，且上游看到的是真密钥。
func TestProxyInjectsKey(t *testing.T) {
	up, seen := fakeUpstream(t, "server-side-key")
	h := newPanelHandler(t, up.URL, "server-side-key")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, adminRequest("GET", "/app/agent2api/api/session"))
	if rec.Code != 200 {
		t.Fatalf("状态码 = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if *seen != "server-side-key" {
		t.Fatalf("上游收到的 x-api-key = %q", *seen)
	}
}

// 浏览器带来的伪造密钥必须被覆盖掉（密钥轮换免疫）。
func TestProxyOverridesClientKey(t *testing.T) {
	up, seen := fakeUpstream(t, "server-side-key")
	h := newPanelHandler(t, up.URL, "server-side-key")

	req := adminRequest("GET", "/app/agent2api/api/session")
	req.Header.Set("x-api-key", "attacker-supplied")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("状态码 = %d, want 200", rec.Code)
	}
	if *seen != "server-side-key" {
		t.Fatalf("客户端密钥未被覆盖，上游收到 %q", *seen)
	}
}

func TestProxyHTMLRewriteAndInject(t *testing.T) {
	up, _ := fakeUpstream(t, "k")
	h := newPanelHandler(t, up.URL, "k")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, adminRequest("GET", "/app/agent2api/"))
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	if !strings.Contains(body, `src="/app/agent2api/islands/ui.js"`) {
		t.Errorf("相对资源未补前缀: %s", body)
	}
	if !strings.Contains(body, `data-fn-gateway-seed="1"`) || !strings.Contains(body, `data-fn-gateway-bridge="1"`) {
		t.Errorf("未注入种子/桥接脚本: %s", body)
	}
	// <base> 是相对引用的兜底：必须注入，且必须排在两个脚本之前
	// （浏览器要求 <base> 先于任何会解析 URL 的元素出现）。
	baseAt := strings.Index(body, `<base href="/app/agent2api/">`)
	seedAt := strings.Index(body, `data-fn-gateway-seed="1"`)
	bridgeAt := strings.Index(body, `data-fn-gateway-bridge="1"`)
	if baseAt < 0 {
		t.Fatalf("未注入 <base> 标签: %s", body)
	}
	if !(baseAt < seedAt && seedAt < bridgeAt) {
		t.Errorf("注入顺序应为 base < seed < bridge，实际 base@%d seed@%d bridge@%d",
			baseAt, seedAt, bridgeAt)
	}
	if strings.Contains(body, "/app/agent2api/app/agent2api") {
		t.Errorf("出现重复前缀: %s", body)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("面板 HTML 应 no-store，实际 %q", cc)
	}
}

func TestProxyRedirectAndCookie(t *testing.T) {
	up, _ := fakeUpstream(t, "k")
	h := newPanelHandler(t, up.URL, "k")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, adminRequest("GET", "/app/agent2api/api/redirect"))
	if loc := rec.Header().Get("Location"); loc != "/app/agent2api/login" {
		t.Errorf("Location = %q, want /app/agent2api/login", loc)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, adminRequest("GET", "/app/agent2api/api/cookie"))
	if sc := rec.Header().Get("Set-Cookie"); !strings.Contains(sc, "Path=/app/agent2api/") {
		t.Errorf("Set-Cookie 未收窄作用域: %q", sc)
	}
}

func TestProxySSE(t *testing.T) {
	up, _ := fakeUpstream(t, "k")
	h := newPanelHandler(t, up.URL, "k")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, adminRequest("GET", "/app/agent2api/api/stream"))
	if got := rec.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("SSE 缺少 X-Accel-Buffering: no，实际 %q", got)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-transform") {
		t.Errorf("SSE Cache-Control 应为 no-transform，实际 %q", cc)
	}
	if !strings.Contains(rec.Body.String(), "data: hi") {
		t.Errorf("SSE 内容未透传: %s", rec.Body.String())
	}
}

// 直通模式（下游端口）不做任何改写：路径原样、不注入脚本。
func TestProxyPassthrough(t *testing.T) {
	up, _ := fakeUpstream(t, "k")
	h, err := NewProxy(Options{InternalAddr: strings.TrimPrefix(up.URL, "http://")})
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	body := rec.Body.String()
	if strings.Contains(body, "data-fn-gateway-bridge") {
		t.Errorf("直通模式不应注入桥接脚本: %s", body)
	}
	if strings.Contains(body, "/app/agent2api/islands/ui.js") {
		t.Errorf("直通模式不应改写路径: %s", body)
	}
}

func TestNewProxyRejectsEmptyAddr(t *testing.T) {
	if _, err := NewProxy(Options{}); err == nil {
		t.Fatal("空内部地址应报错")
	}
}

// ---------------------------------------------------------------------------
// v3.1.0 适配：manifest 正文 / 导航字面量 / script 内嵌 HTML / SW 头
// ---------------------------------------------------------------------------

// PWA manifest 的 start_url / scope 是按站点根写的，经前缀后会把作用域钉到
// 飞牛站点根、启动到错误 URL。网关必须把 JSON 正文里的站内绝对路径补前缀。
func TestRewriteManifest(t *testing.T) {
	p := NewPrefix("/app/agent2api")
	cases := []struct {
		name string
		in   string
		want map[string]string // 字段 → 期望值（解析改写后的 JSON 断言）
		raw  []string          // 原始字符串必须出现的片段
	}{
		{
			name: "桌面 panel：start_url 与 scope 都是根",
			in:   `{"name":"x","start_url":"/","scope":"/"}`,
			want: map[string]string{"start_url": "/app/agent2api/", "scope": "/app/agent2api/"},
		},
		{
			name: "手机端：start_url 是根绝对路径",
			in:   `{"start_url":"/m.html","scope":"/","id":"/m.html"}`,
			want: map[string]string{
				"start_url": "/app/agent2api/m.html",
				"scope":     "/app/agent2api/",
				"id":        "/app/agent2api/m.html",
			},
		},
		{
			name: "根绝对图标要补前缀",
			in:   `{"icons":[{"src":"/assets/a.png"}],"start_url":"/"}`,
			want: map[string]string{"start_url": "/app/agent2api/"},
			raw:  []string{`"/app/agent2api/assets/a.png"`},
		},
		{
			name: "相对图标不能动（相对 manifest 自身 URL 解析，本来就是对的）",
			in:   `{"icons":[{"src":"assets/app-icon-180.png"}],"start_url":"/"}`,
			want: map[string]string{"start_url": "/app/agent2api/"},
			raw:  []string{`"assets/app-icon-180.png"`},
		},
		{
			name: "shortcuts 的 url 带 fragment 也要保形",
			in:   `{"shortcuts":[{"url":"/m.html#checkin","icons":[{"src":"/assets/i.png"}]}],"start_url":"/"}`,
			want: map[string]string{"start_url": "/app/agent2api/"},
			raw:  []string{`"/app/agent2api/m.html#checkin"`, `"/app/agent2api/assets/i.png"`},
		},
		{
			name: "绝对 URL 与协议相对都不动",
			in:   `{"start_url":"https://cdn.example.com/app","scope":"//evil.example.com/x"}`,
			want: map[string]string{"start_url": "https://cdn.example.com/app", "scope": "//evil.example.com/x"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := p.RewriteManifest([]byte(c.in))
			var got map[string]any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("改写后不是合法 JSON: %v\n%s", err, out)
			}
			for k, want := range c.want {
				if v, _ := got[k].(string); v != want {
					t.Errorf("%s = %q, want %q（输出 %s）", k, v, want, out)
				}
			}
			for _, frag := range c.raw {
				if !strings.Contains(string(out), frag) {
					t.Errorf("输出缺少 %q：%s", frag, out)
				}
			}
		})
	}
}

// 幂等：改写过的 manifest 再改一次必须一模一样，不能出现双重前缀。
func TestRewriteManifestIdempotent(t *testing.T) {
	p := NewPrefix("/app/agent2api")
	in := []byte(`{"start_url":"/","scope":"/","icons":[{"src":"/assets/a.png"}],"shortcuts":[{"url":"/m.html#x"}]}`)
	once := p.RewriteManifest(in)
	twice := p.RewriteManifest(once)
	if !bytes.Equal(once, twice) {
		t.Fatalf("非幂等：\n一次: %s\n二次: %s", once, twice)
	}
	if bytes.Contains(twice, []byte("/app/agent2api/app/agent2api")) {
		t.Fatalf("出现双重前缀: %s", twice)
	}
}

// 不是 JSON、或空前缀（下游直通）时必须原样返回，绝不能让 manifest 变成坏响应。
func TestRewriteManifestPassThrough(t *testing.T) {
	p := NewPrefix("/app/agent2api")
	for _, in := range []string{``, `not json`, `[1,2,3]`, `"str"`} {
		if got := p.RewriteManifest([]byte(in)); string(got) != in {
			t.Errorf("非 JSON 输入 %q 被改动为 %q", in, got)
		}
	}
	raw := `{"start_url":"/","scope":"/"}`
	if got := (Prefix{}).RewriteManifest([]byte(raw)); string(got) != raw {
		t.Errorf("空前缀不应改写，得到 %s", got)
	}
}

// 数字必须按原文保留：Unmarshal 到 any 走 float64 会把超过 2^53 的整数改值。
// 当前上游 manifest 没有数值字段，但「网关悄悄改写上游数据」极难排查，
// 所以钉住这条（改用 UseNumber 之前此用例必失败）。
func TestRewriteManifestKeepsNumberPrecision(t *testing.T) {
	p := NewPrefix("/app/agent2api")
	in := `{"scope":"/","start_url":"/","n":12345678901234567890,` +
		`"big":9007199254740993,"f":1.5}`
	out := string(p.RewriteManifest([]byte(in)))
	for _, want := range []string{
		`"n": 12345678901234567890`,
		`"big": 9007199254740993`,
		`"f": 1.5`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("数字失真: 期望保留 %q，实际输出:\n%s", want, out)
		}
	}
}

// 超大的非 manifest JSON 不应被整份搬进内存：RewriteManifest 对超过阈值
// 的响应直接原样放行（宁可 PWA 降级，也不为一个大文件吃内存）。
func TestRewriteManifestSkipsOversized(t *testing.T) {
	p := NewPrefix("/app/agent2api")
	big := bytes.Repeat([]byte(" "), maxManifestRewriteBytes+1)
	big = append(big, []byte(`{"start_url":"/"}`)...)
	if got := p.RewriteManifest(big); !bytes.Equal(got, big) {
		t.Fatalf("超过阈值的 manifest 应原样放行")
	}
}

func TestIsManifestType(t *testing.T) {
	yes := []string{"application/manifest+json", "application/manifest+json; charset=utf-8",
		"Application/Manifest+JSON", "application/manifest"}
	no := []string{"application/json", "text/html", "", "text/javascript", "application/manifest+jsonx"}
	for _, ct := range yes {
		if !isManifestType(ct) {
			t.Errorf("%q 应判为 manifest", ct)
		}
	}
	for _, ct := range no {
		if isManifestType(ct) {
			t.Errorf("%q 不应判为 manifest", ct)
		}
	}
}

// 上游 Web 壳与面板产品代码把 location.href 写死成根绝对路径 /login。
// location.href 的 setter 在 Chromium 里是 [LegacyUnforgeable]，运行时拦不住，
// 只能改服务端下发的字面量。
func TestRewriteNavigationLiterals(t *testing.T) {
	p := NewPrefix("/app/agent2api")
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"Web 壳：单引号 + 空格", `window.location.href = '/login'`, `window.location.href = '/app/agent2api/login'`},
		{"面板：双引号无空格", `window.location.href="/login"`, `window.location.href="/app/agent2api/login"`},
		{"手机端：带 query", `window.location.href="/login?next=%2Fm"`, `window.location.href="/app/agent2api/login?next=%2Fm"`},
		{"assign 形式", `location.assign('/login')`, `location.assign('/app/agent2api/login')`},
		{"replace 形式", `location.replace("/login")`, `location.replace("/app/agent2api/login")`},
		{"裸 location", `location.href='/login'`, `location.href='/app/agent2api/login'`},
		// 不该动的：
		{"已带前缀（幂等）", `location.href='/app/agent2api/login'`, `location.href='/app/agent2api/login'`},
		{"协议相对", `location.href='//evil.example.com/x'`, `location.href='//evil.example.com/x'`},
		{"绝对 URL", `location.href='https://a.example.com/x'`, `location.href='https://a.example.com/x'`},
		{"变量而非字面量", `location.href = authUrl`, `location.href = authUrl`},
		{"只读 location.href 不赋值", `var h = window.location.href;`, `var h = window.location.href;`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(p.RewriteNavigationLiterals([]byte(c.in))); got != c.want {
				t.Fatalf("得到 %q, want %q", got, c.want)
			}
		})
	}
}

// login.html 的 postLoginTarget() 返回 '/?from=login'，但本应用不启用面板登录
// （无管理员、Key 通道），该函数不可达。特意不改 return 字面量，避免放大误改面。
func TestRewriteNavigationLiteralsLeavesReturns(t *testing.T) {
	p := NewPrefix("/app/agent2api")
	in := `function postLoginTarget(){ if(next==='/m') return '/m?from=login'; return '/?from=login'; }`
	if got := string(p.RewriteNavigationLiterals([]byte(in))); got != in {
		t.Fatalf("不应改动 return 字面量: %q", got)
	}
}

// document.write('<script src="...">') 这种字符串里的 HTML 片段。
// 只改「跟在字符串开头」的根绝对路径；注释里的示例不能误改。
// TestJSCommentSpansStringAware 锁定「字符串里的 // 不得吞掉后续代码」。
//
// 这是一个真实缺陷的回归测试：上游面板是单行 minified 产物，旧实现把
// `https://…` 里的 `//` 当行注释开头，于是同一行后面所有真正需要改写的
// `src="/…"` / `location.href='/…'` 都被判定为「在注释里」而**漏改**。
// 漏改比误改危险 —— 误改只动注释文本，漏改会让面板在子路径下跳到错误地址。
func TestJSCommentSpansStringAware(t *testing.T) {
	cases := []struct {
		name string
		in   string
		// 期望「某位置是否被判为注释」，用一段必须被改写的代码验证：
		wantRewritten bool
	}{
		{
			name:          "字符串里的 https:// 不该吞掉后面的 src",
			in:            `var a = 'see https://example.com/x'; var t = '<img src="/logo.png">';`,
			wantRewritten: true,
		},
		{
			name:          "双引号字符串里的协议相对 //cdn 不该吞掉后面的跳转",
			in:            `var a = "//cdn.example.com/x"; location.href = '/login';`,
			wantRewritten: true,
		},
		{
			name:          "模板字符串里的 https:// 不该吞掉后面的跳转",
			in:            "var a = `https://example.com`; location.href = '/login';",
			wantRewritten: true,
		},
		{
			// 注意：RewriteNavigationLiterals **不带**注释保护（它只认
			// location.* 这种强特征写法，宁可改写注释里的示例也不漏改真代码）。
			// 注释保护只在 RewriteScript 那条路径上，所以这里用 <script src>
			// 形态来验证注释被正确跳过。
			name:          "真正的行注释里的示例仍然不改",
			in:            "// 示例 <script src=\"/x\">\nvar x = 1;",
			wantRewritten: false,
		},
		{
			name:          "真正的块注释里的示例仍然不改",
			in:            `/* 示例 <script src="/x"> */ var y = 2;`,
			wantRewritten: false,
		},
	}
	p := NewPrefix("/app/agent2api")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := p.RewriteNavigationLiterals([]byte(c.in))
			after := p.RewriteScript(string(got))
			rewritten := strings.Contains(after, p.Path)
			if rewritten != c.wantRewritten {
				t.Fatalf("改写=%v, want %v\n输入: %s\n输出: %s",
					rewritten, c.wantRewritten, c.in, after)
			}
		})
	}
}

func TestRewriteScript(t *testing.T) {
	p := NewPrefix("/app/agent2api")
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"双引号属性", `document.write('<script src="/i18n/x.js"><\/script>')`,
			`document.write('<script src="/app/agent2api/i18n/x.js"><\/script>')`},
		{"单引号属性", `document.write("<script src='/a.js'>")`,
			`document.write("<script src='/app/agent2api/a.js'>")`},
		{"已带前缀幂等", `x('<script src="/app/agent2api/a.js">')`, `x('<script src="/app/agent2api/a.js">')`},
		// 裸相对路径不吃前缀（它相对文档 / <base> 解析），保持原样。
		{"裸相对不动", `document.write('<script src="i18n/manifest.js">')`,
			`document.write('<script src="i18n/manifest.js">')`},
		// 注释里的示例前面不是引号/括号，不能命中。
		{"注释示例不动", `/* 例如 <script src="/x"> */`, `/* 例如 <script src="/x"> */`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := p.RewriteScript(c.in); got != c.want {
				t.Fatalf("得到 %q, want %q", got, c.want)
			}
		})
	}
}

func TestIsJavaScriptType(t *testing.T) {
	for _, ct := range []string{"text/javascript", "text/javascript; charset=utf-8",
		"application/javascript", "application/x-javascript"} {
		if !isJavaScriptType(ct) {
			t.Errorf("%q 应判为脚本", ct)
		}
	}
	for _, ct := range []string{"application/json", "text/html", "text/css", "", "text/plain"} {
		if isJavaScriptType(ct) {
			t.Errorf("%q 不应判为脚本", ct)
		}
	}
}

// v31Upstream 按 v3.1.0 的真实形态布置上游响应：
// manifest 是 application/manifest+json 且 start_url/scope 都写死 "/"；
// 脚本里带根绝对跳转；sw.js 不设 Service-Worker-Allowed；
// /api/session 无密钥 401、有密钥 200（Key 通道）。
func v31Upstream(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			// Web 壳：head 里第一个脚本就写死了根绝对跳转与密钥读取。
			_, _ = io.WriteString(w, `<html><head><script>var KEY_STORAGE='agent2api.webKey';`+
				`if(bad){window.location.href = '/login';}</script></head>`+
				`<body><link rel="manifest" href="manifest.webmanifest">`+
				`<a class="m" href="/m">手机端</a></body></html>`)
		case "/manifest.webmanifest":
			w.Header().Set("Content-Type", "application/manifest+json; charset=utf-8")
			_, _ = io.WriteString(w, `{"name":"面板","start_url":"/","scope":"/",`+
				`"icons":[{"src":"assets/app-icon-192.png"},{"src":"/assets/abs.png"}]}`)
		case "/m/manifest.webmanifest":
			w.Header().Set("Content-Type", "application/manifest+json; charset=utf-8")
			_, _ = io.WriteString(w, `{"start_url":"/m.html","scope":"/","id":"/m.html",`+
				`"shortcuts":[{"url":"/m.html#checkin","icons":[{"src":"/assets/i.png"}]}]}`)
		case "/islands/ui.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			// 登出函数：写死 window.location.href="/login"（双引号、无空格）。
			_, _ = io.WriteString(w, `async function logout(){window.location.href="/login"}`)
		case "/m/mobile.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = io.WriteString(w, `f=()=>{window.location.href="/login?next=%2Fm"}`)
		case "/sw.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = io.WriteString(w, `self.addEventListener('install',function(e){})`)
		case "/api/session":
			if r.Header.Get("x-api-key") != "server-key" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"error":{"type":"panel_login_required"}}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"success":true}`)
		case "/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"ok"}`)
		default:
			w.WriteHeader(404)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestProxyManifestRewriteEndToEnd(t *testing.T) {
	up := v31Upstream(t)
	h := newPanelHandler(t, up.URL, "server-key")

	cases := []struct {
		path string
		want map[string]string
		raw  []string
	}{
		{
			path: "/app/agent2api/manifest.webmanifest",
			want: map[string]string{"start_url": "/app/agent2api/", "scope": "/app/agent2api/"},
			// 相对图标保持相对（相对 manifest 自身 URL 解析），根绝对图标补前缀。
			raw: []string{`"assets/app-icon-192.png"`, `"/app/agent2api/assets/abs.png"`},
		},
		{
			path: "/app/agent2api/m/manifest.webmanifest",
			want: map[string]string{
				"start_url": "/app/agent2api/m.html",
				"scope":     "/app/agent2api/",
				"id":        "/app/agent2api/m.html",
			},
			raw: []string{`"/app/agent2api/m.html#checkin"`},
		},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, adminRequest("GET", c.path))
			if rec.Code != 200 {
				t.Fatalf("状态码 %d", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "manifest+json") {
				t.Errorf("Content-Type 被改动: %q", got)
			}
			var doc map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
				t.Fatalf("响应不是合法 JSON: %v\n%s", err, rec.Body.String())
			}
			for k, want := range c.want {
				if got, _ := doc[k].(string); got != want {
					t.Errorf("%s = %q, want %q", k, got, want)
				}
			}
			for _, frag := range c.raw {
				if !strings.Contains(rec.Body.String(), frag) {
					t.Errorf("缺少 %q：%s", frag, rec.Body.String())
				}
			}
			// Content-Length 必须与改写后的正文一致，否则浏览器会截断。
			if cl := rec.Header().Get("Content-Length"); cl != "" {
				if cl != fmt.Sprint(rec.Body.Len()) {
					t.Errorf("Content-Length=%s 与正文 %d 不符", cl, rec.Body.Len())
				}
			}
		})
	}
}

// 脚本正文里的根绝对跳转必须被改写：location.href 的 setter 在 Chromium 里
// 拦不住（[LegacyUnforgeable]），只能改服务端下发的字面量。
func TestProxyRewritesNavigationLiterals(t *testing.T) {
	up := v31Upstream(t)
	h := newPanelHandler(t, up.URL, "server-key")

	// notWant 必须是"前面没有任何前缀的裸根绝对跳转"，因此用带引号边界的写法，
	// 否则 `location.href="/login"` 会是 `location.href="/app/agent2api/login"` 的子串。
	cases := []struct{ path, want, notWant string }{
		{"/app/agent2api/islands/ui.js", `location.href="/app/agent2api/login"`, `location.href="/login"`},
		{"/app/agent2api/m/mobile.js", `location.href="/app/agent2api/login?next=%2Fm"`, `location.href="/login?next=%2Fm"`},
		{"/app/agent2api/", `location.href='/app/agent2api/login'`, `{window.location.href = '/login';}`},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, adminRequest("GET", c.path))
			body := rec.Body.String()
			if !strings.Contains(body, c.want) {
				t.Errorf("缺少 %q：%s", c.want, body)
			}
			if strings.Contains(body, c.notWant) {
				t.Errorf("仍残留 %q：%s", c.notWant, body)
			}
			if strings.Contains(body, "/app/agent2api/app/agent2api") {
				t.Errorf("出现双重前缀：%s", body)
			}
		})
	}
}

// sw.js 要带 Service-Worker-Allowed（上游不设），其它响应不能带。
func TestProxyServiceWorkerAllowedHeader(t *testing.T) {
	up := v31Upstream(t)
	h := newPanelHandler(t, up.URL, "server-key")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, adminRequest("GET", "/app/agent2api/sw.js"))
	if got := rec.Header().Get("Service-Worker-Allowed"); got != "/app/agent2api/" {
		t.Errorf("sw.js 的 Service-Worker-Allowed = %q, want /app/agent2api/", got)
	}
	// 脚本正文不应被导航改写波及（没有 location 字面量）。
	if !strings.Contains(rec.Body.String(), "addEventListener") {
		t.Errorf("sw.js 正文被破坏: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, adminRequest("GET", "/app/agent2api/islands/ui.js"))
	if got := rec.Header().Get("Service-Worker-Allowed"); got != "" {
		t.Errorf("非 sw.js 不应带 Service-Worker-Allowed，实际 %q", got)
	}
}

// 下游直通端口（无前缀）绝不做 manifest / 脚本正文改写：
// 那边没有前缀概念，改了就是把上游原文改坏。
func TestProxyPassthroughDoesNotRewriteV31(t *testing.T) {
	up := v31Upstream(t)
	h, err := NewProxy(Options{InternalAddr: strings.TrimPrefix(up.URL, "http://")})
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	for _, path := range []string{"/manifest.webmanifest", "/islands/ui.js"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		body := rec.Body.String()
		if strings.Contains(body, "/app/agent2api") {
			t.Errorf("直通模式 %s 被改写: %s", path, body)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/sw.js", nil))
	if got := rec.Header().Get("Service-Worker-Allowed"); got != "" {
		t.Errorf("直通模式不应补头，实际 %q", got)
	}
}
