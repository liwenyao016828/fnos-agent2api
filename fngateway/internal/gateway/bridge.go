package gateway

import (
	"strconv"
	"strings"
)

// BridgeScript 返回注入 HTML 的运行时桥接脚本。
//
// 为什么必须有它：正则只能改写静态 HTML，JS 运行时拼接的 URL（fetch、XHR、
// 动态插入的 iframe 等）不经过 HTML，必须靠脚本在运行时拦截。
// 这是网关适配中最容易漏掉、也最容易导致白屏的一环（见 skill
// references/gateway-proxy.md §4）。本脚本承担三件事：
//
//  1. 前缀改写：所有同源 URL 补上 /app/agent2api，已带前缀的不重复加。
//  2. 鉴权头迁移：Authorization: Bearer <key> → x-api-key。
//     飞牛统一网关（1.2.0604+）会把应用自己的 Authorization 当成非法飞牛
//     票据直接拦截（invalid token），而 agent2api 的 require_api_key 同时
//     接受 Authorization 与 x-api-key（server/src/server/access.rs），
//     因此这层迁移是必需的。
//  3. 整页跳转兜底：location.assign / location.replace 的站内绝对路径补前缀。
//  4. 展示地址纠正：面板「接口地址」显示的必须是下游独立端口（apiPort），
//     而不是统一网关的 origin。见脚本里「接口地址」那一节。
//
// 为什么 agent2api 不需要 cli2api 那套「构建期 router 补丁」：
// 实测面板产物（desktop-tauri/ui）**没有任何前端路由** —— islands/ui.js 与
// app.js 里 location.pathname / history.pushState / createBrowserRouter 的出现
// 次数均为 0，页面切换靠内存里的 currentPage 变量 + CSS class。因此不存在
// 「读侧拿不到前缀」的问题，运行时改写就足以覆盖全部动态 URL。
//
// 安全约束（同 skill 要求）：
//   - 同源检查：只改写同源 URL，外链/CDN 不动。
//   - 幂等检查：已带前缀不重复加，避免 /app/agent2api/app/agent2api/...。
//   - 协议白名单：只处理 http/https/ws/wss。
//   - 全程 try/catch：桥接脚本自身报错会让整页白屏，异常必须吞掉。
//   - 全局标记防重复安装。
//
// 该脚本只会经 ${TRIM_APPDEST}/agent2api.sock 注入到 /app/agent2api 前缀下的
// HTML，因此可以假定页面本身位于前缀之下。
//
// apiPort 是下游独立接入端口（manifest 的 service_port，main.go 的 lay.port）；
// 传 0 或负数表示不纠正展示地址，行为与改写前完全一致（给测试与直连场景兜底）。
func BridgeScript(prefix Prefix, apiPort int) string {
	var b strings.Builder
	b.WriteString(`<script data-fn-gateway-bridge="1">(function(){`)
	b.WriteString(`if(window.__fnGatewayBridgeReady)return;window.__fnGatewayBridgeReady=1;`)
	b.WriteString(`var P=`)
	b.WriteString(jsString(prefix.Path))
	b.WriteString(`;window.__fnGatewayBase=P;`)
	if apiPort <= 0 {
		apiPort = 0
	} else if apiPort > 65535 {
		apiPort = 65535
	}
	b.WriteString(`var AP=`)
	b.WriteString(strconv.Itoa(apiPort))
	b.WriteString(`;`)
	b.WriteString(bridgeBody)
	b.WriteString(`})();</script>`)
	return b.String()
}

// SeedScript 给面板的密钥槽位放一个占位值。
//
// agent2api 的 Web 壳（server/src/web_shim.rs，编译进二进制）在脚本顶层把
// localStorage['agent2api.webKey'] 读进闭包变量 apiKey，之后每个 /api 请求
// 都带上 `x-api-key: apiKey`；缺失时会弹「需要网关 API Key」输入层。
// 真实密钥由网关在服务端注入（见 main.go 的 Prepare 钩子），**不下发到浏览器**；
// 这里只放一个占位值，让面板不必弹框、也不必读用户输入。
//
// 本脚本由 InjectHead 插到 <head> 之后，即**先于**服务端注入的那段 Web 壳执行，
// 因此这里写入的值一定能被它读到。
func SeedScript() string {
	return `<script data-fn-gateway-seed="1">(function(){try{` +
		`var K="agent2api.webKey",S="fnos-gateway-session",ls=window.localStorage;` +
		`ls.setItem(K,S);` +
		`var set=ls.setItem,rm=ls.removeItem;` +
		`ls.setItem=function(k,v){if(k===K&&!String(v==null?"":v).trim()){v=S}return set.call(ls,k,v)};` +
		`ls.removeItem=function(k){if(k===K){return set.call(ls,K,S)}return rm.call(ls,k)};` +
		`}catch(e){}})();</script>`
}

// jsString 安全地把 Go 字符串编码为 JS 字面量（防注入/防提前闭合 script）。
func jsString(s string) string {
	// 只允许网关前缀这种受控字符集；任何异常字符一律 unicode 转义。
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '/', r == '-', r == '_', r == '.', r == '~':
			b.WriteRune(r)
		default:
			const hex = "0123456789abcdef"
			b.WriteString(`\u`)
			b.WriteByte(hex[(r>>12)&0xF])
			b.WriteByte(hex[(r>>8)&0xF])
			b.WriteByte(hex[(r>>4)&0xF])
			b.WriteByte(hex[r&0xF])
		}
	}
	b.WriteByte('"')
	return b.String()
}

// bridgeBody 桥接脚本主体（纯 JS，不含 Go 模板；注意不能出现反引号，
// 因为它被写成 Go 原始字符串）。
const bridgeBody = `
function safe(fn){try{return fn()}catch(e){return null}}
function already(p){return p===P||p.indexOf(P+'/')===0}
/* 解析基准必须显式带上尾斜杠。
   飞牛桌面入口的 url 是 /app/agent2api（不带尾斜杠），文档 URL 就是
   https://host/app/agent2api。此时把裸相对引用（assets/providers/x.png）
   按 window.location.href 解析会退到上一级 /app/assets/...，再补前缀就变成
   /app/agent2api/app/assets/...，必然 404 —— 这正是「添加账号 → 反代」里
   模型图标全部不显示的原因。
   用 P+'/' 作基准后，裸相对引用解析结果天然落在前缀之下，already() 判定为
   「已带前缀」直接放行，由浏览器按 <base> 自行解析，桥接不再插手改坏它。
   new URL 的第二个参数必须是**绝对** URL，所以这里用 P+'/' 相对当前文档
   求一次绝对值；万一环境异常求不出来，退回原来的 href（行为不劣于改前）。 */
var RESOLVE_BASE=safe(function(){return new URL(P+'/',window.location.href).toString()})||window.location.href;
function toGw(v){
  if(v===null||v===undefined||v==='')return null;
  var str=String(v).trim();
  if(/^(blob:|data:|javascript:|about:|#)/i.test(str))return null;
  var u; try{u=new URL(str,RESOLVE_BASE)}catch(e){return null}
  if(!/^(https?|wss?):$/.test(u.protocol))return null;
  if(u.origin!==window.location.origin)return null;
  if(already(u.pathname))return null;
  u.pathname=P+(u.pathname.charAt(0)==='/'?u.pathname:'/'+u.pathname);
  return u;
}
function str(url){var u=toGw(url);return u?u.toString():null}

/* ---- 鉴权头迁移：Authorization -> x-api-key（飞牛网关拦截 Authorization）---- */
function bareSecret(v){
  var s=String(v===null||v===undefined?'':v).trim();
  return s.replace(/^Bearer\s+/i,'');
}
function migrateHeaders(h){
  var out=safe(function(){
    if(!h)return null;
    if(typeof Headers!=='undefined'&&h instanceof Headers){
      var v=h.get('authorization');
      if(v){h.delete('authorization');if(!h.get('x-api-key'))h.set('x-api-key',bareSecret(v));}
      return h;
    }
    var nh=new Headers();
    if(typeof h.forEach==='function'&&!Array.isArray(h)){h.forEach(function(val,key){nh.append(key,val)});}
    else if(Array.isArray(h)){h.forEach(function(pair){if(pair&&pair.length===2)nh.append(pair[0],pair[1])});}
    else if(typeof h==='object'){Object.keys(h).forEach(function(k){nh.append(k,h[k])});}
    else return null;
    var raw=nh.get('authorization');
    if(raw){nh.delete('authorization');if(!nh.get('x-api-key'))nh.set('x-api-key',bareSecret(raw));}
    return nh;
  });
  return out||h;
}

/* fetch */
if(window.fetch){
  var _f=window.fetch;
  window.fetch=function(input,init){
    safe(function(){
      if(typeof input==='string'){
        var s=str(input);if(s)input=s;
        if(init&&init.headers)init.headers=migrateHeaders(init.headers);
        return;
      }
      if(input&&input.url){
        /* Request 对象：url 与 headers 只读，必须重建（保留原方法与头体）。 */
        var s2=str(input.url);
        var h2=input.headers?migrateHeaders(input.headers):null;
        if(s2||h2){
          try{
            input=new Request(s2||input.url,{
              method:input.method,headers:h2||input.headers,body:input.body,
              mode:input.mode,credentials:input.credentials,cache:input.cache,
              redirect:input.redirect,referrer:input.referrer,
              referrerPolicy:input.referrerPolicy,integrity:input.integrity,
              keepalive:input.keepalive,signal:input.signal,duplex:input.duplex
            });
          }catch(e){ /* body 已被消费等情况：退回原始对象，不阻断请求 */ }
        }
        return;
      }
      if(init&&init.headers)init.headers=migrateHeaders(init.headers);
    });
    return _f.call(window,input,init);
  };
}

/* XMLHttpRequest：路径加前缀 + 鉴权头迁移 */
if(window.XMLHttpRequest){
  var _o=XMLHttpRequest.prototype.open;
  XMLHttpRequest.prototype.open=function(m,u){
    var s=safe(function(){return str(u)});
    if(s)arguments[1]=s;
    return _o.apply(this,arguments);
  };
  if(XMLHttpRequest.prototype.setRequestHeader){
    var _srh=XMLHttpRequest.prototype.setRequestHeader;
    XMLHttpRequest.prototype.setRequestHeader=function(name,value){
      if(typeof name==='string'&&/^authorization$/i.test(name)){
        return _srh.call(this,'x-api-key',bareSecret(value));
      }
      return _srh.call(this,name,value);
    };
  }
}

/* WebSocket：只改写同源同端口的 URL */
if(window.WebSocket){
  var _W=window.WebSocket;
  var W=function(url,protocols){
    var fixed=safe(function(){
      var u=new URL(String(url),RESOLVE_BASE);
      var wp=(window.location.protocol==='https:'?'wss:':'ws:');
      if(u.protocol!==wp)return null;
      if(u.hostname!==window.location.hostname)return null;
      if(u.port!==window.location.port)return null;
      if(already(u.pathname))return null;
      u.pathname=P+(u.pathname.charAt(0)==='/'?u.pathname:'/'+u.pathname);
      return u.toString();
    });
    return protocols===undefined?new _W(fixed||url):new _W(fixed||url,protocols);
  };
  W.prototype=_W.prototype;
  ['CONNECTING','OPEN','CLOSING','CLOSED'].forEach(function(k){try{W[k]=_W[k]}catch(e){}});
  window.WebSocket=W;
}

/* EventSource */
if(window.EventSource){
  var _E=window.EventSource;
  var E=function(url,cfg){
    var fixed=safe(function(){return str(url)});
    return cfg===undefined?new _E(fixed||url):new _E(fixed||url,cfg);
  };
  E.prototype=_E.prototype;
  window.EventSource=E;
}

/* innerHTML / insertAdjacentHTML：只做字符串级属性替换，不执行内容 */
function rewriteHtml(strHtml){
  return safe(function(){
    if(typeof strHtml!=='string'||strHtml.indexOf('/')<0)return strHtml;
    return strHtml.replace(/(\b(?:src|href|action|poster)\s*=\s*["'])(\/(?!\/)[^"']*)/gi,
      function(m,p1,p2){
        if(already(p2))return m;
        return p1+P+p2;
      });
  })||strHtml;
}
try{
  var _ih=Object.getOwnPropertyDescriptor(Element.prototype,'innerHTML');
  if(_ih&&_ih.set){
    var oldIH=_ih.set;
    Object.defineProperty(Element.prototype,'innerHTML',{
      configurable:true,enumerable:_ih.enumerable,get:_ih.get,
      set:function(v){return oldIH.call(this,rewriteHtml(v))}
    });
  }
}catch(e){}
try{
  var _iah=Element.prototype.insertAdjacentHTML;
  Element.prototype.insertAdjacentHTML=function(pos,html){
    return _iah.call(this,pos,rewriteHtml(html));
  };
}catch(e){}

/* Worker 脚本路径 */
try{
  if(window.Worker){
    var _K=window.Worker;
    var K=function(url,opts){
      var fixed=safe(function(){return str(url)});
      return opts===undefined?new _K(fixed||url):new _K(fixed||url,opts);
    };
    K.prototype=_K.prototype;
    window.Worker=K;
  }
}catch(e){}

/* 读侧前缀剥离：agent2api 面板没有任何前端路由（不读 location.pathname、
   不写 history），因此不存在 cli2api 那种「读侧拿不到前缀」的问题，这里
   不需要任何 Location 原型补丁（Chrome 的 Location 属性是
   [LegacyUnforgeable]，本来也打不上）。 */

/* history API：本版面板不写 history，这里只为将来加了路由的版本兜底，
   写入时补前缀，保证 URL 栏可刷新、可深链。 */
try{
  var _ps=history.pushState,_rs=history.replaceState;
  function fixState(u){
    return safe(function(){
      if(u===null||u===undefined)return u;
      var s=str(u);return s||u;
    })||u;
  }
  history.pushState=function(st,t,u){return _ps.call(this,st,t,fixState(u))};
  history.replaceState=function(st,t,u){return _rs.call(this,st,t,fixState(u))};
}catch(e){}

/* location.assign / replace：站内绝对路径补前缀。
   ⚠️ 浏览器实测（Chrome headless，本仓库验证脚本）：这段拦不住任何东西。
   Location 的 assign / replace / href 都是 [LegacyUnforgeable]，
   给 window.location.assign 赋新函数会静默失效（再读仍是原生函数）。
   保留它只为在少数实现宽松的引擎上仍有兜底，不要指望它生效。

   真正兜住「写死 location.href='/login'」的是服务端改写：
   gateway.RewriteNavigationLiterals 会把脚本正文里的这类字面量换成带前缀的
   版本（上游 Web 壳、islands/ui.js 的登出、m/mobile.js 的登录跳转三处）。 */
try{
  var _as=window.location.assign,_rp=window.location.replace;
  window.location.assign=function(u){
    var s=safe(function(){return str(u)});
    return _as.call(window.location,s||u);
  };
  window.location.replace=function(u){
    var s=safe(function(){return str(u)});
    return _rp.call(window.location,s||u);
  };
}catch(e){}

/* Element.setAttribute */
try{
  var _sa=Element.prototype.setAttribute;
  Element.prototype.setAttribute=function(n,v){
    if(/^(src|href|action|poster|data)$/i.test(n)){
      var s=safe(function(){return str(v)});if(s)v=s;
    }
    return _sa.call(this,n,v);
  };
}catch(e){}

/* 属性 setter 劫持 */
var PROPS=[['HTMLImageElement','src'],['HTMLImageElement','srcset'],['HTMLLinkElement','href'],
  ['HTMLScriptElement','src'],['HTMLIFrameElement','src'],['HTMLMediaElement','src'],
  ['HTMLVideoElement','poster'],['HTMLSourceElement','src'],['HTMLSourceElement','srcset'],
  ['HTMLFormElement','action'],['HTMLObjectElement','data'],['HTMLEmbedElement','src']];
PROPS.forEach(function(pair){
  try{
    var Ctor=window[pair[0]];if(!Ctor)return;
    var d=Object.getOwnPropertyDescriptor(Ctor.prototype,pair[1]);if(!d||!d.set)return;
    var oldSet=d.set;
    Object.defineProperty(Ctor.prototype,pair[1],{
      configurable:true,enumerable:d.enumerable,
      get:d.get,
      set:function(v){
        var s=safe(function(){return str(v)});
        return oldSet.call(this,s||v);
      }
    });
  }catch(e){}
});

/* 整页跳转兜底（<a> 点击，冒泡阶段）。
   面板没有路由接管 <a>，裸 <a href="/xxx"> 补前缀后整页跳转，
   避免落到飞牛网关的 404 上。 */
document.addEventListener('click',function(ev){
  if(ev.defaultPrevented)return;
  var a=safe(function(){
    var el=ev.target;
    while(el&&el.tagName!=='A')el=el.parentElement;
    return el;
  });
  if(!a||!a.getAttribute)return;
  var href=a.getAttribute('href');
  if(!href||href.charAt(0)==='#'||/^[a-z]+:/i.test(href))return;
  if(a.target&&a.target!=='_self')return;
  var s=safe(function(){return str(href)});
  if(s)ev.preventDefault(),window.location.assign(s);
},false);

/* 动态插入的 iframe 递归安装桥接 */
safe(function(){
  if(!window.MutationObserver)return;
  new MutationObserver(function(muts){
    muts.forEach(function(m){
      Array.prototype.forEach.call(m.addedNodes||[],function(n){
        if(n&&n.tagName==='IFRAME'){safe(function(){
          n.addEventListener('load',function(){safe(function(){
            try{if(n.contentWindow&&!n.contentWindow.__fnGatewayBridgeReady){
              n.contentWindow.__fnGatewayBridgeReady=1;
            }}catch(e){}
          })});
        })}
      });
    });
  }).observe(document.documentElement,{childList:true,subtree:true});
});

/* ===========================================================================
   展示地址纠正（飞牛专有，BridgeScript 文档第 4 条）
   ===========================================================================
   上游 Web 壳（server/src/web_shim.rs，platform='web'）用 location.origin 拼
   「接口地址」，理由写在 ui-islands/src/islands/port-panel.tsx 顶部注释里：反代、
   端口映射、内网穿透之后，进程从原理上不知道访问者看到的外网地址，地址栏
   （location.origin）是唯一权威。这个理由在飞牛上**不成立**：

     · 面板只能经飞牛统一网关打开（http://<飞牛IP>:4398/app/agent2api/，仅管理员），
       origin 因此恒为 https://<飞牛IP>:4398 或 :4399；
     · 而下游 OpenAI 兼容客户端要连的是本应用自己的 TCP 端口（manifest 的
       service_port，默认 3065）—— 统一网关路径外部客户端走不通：飞牛会把客户端
       必带的 Authorization 当成自己的票据拦掉（invalid token），见 main.go 顶部；
     · 实测 http://<飞牛IP>:4398/v1/models 落到的是飞牛影音那份 SPA HTML，
       不是本应用的模型列表。

   所以这里把**展示给人看的地址文本**换成下游端口，同时**不动 location.origin**：
   面板自己的 fetch / XHR / <img> 仍然走 origin、经网关代理，请求侧一律不受影响。
   这也是为什么不做 Location 原型补丁（那既打不上、又会把面板自己的请求带偏）。

   覆盖两处渲染路径与一处复制路径（上游两套界面各自的实现，都已逐条核对 v3.3.0）：
     1. 桌面文档页 islands/ui.js 的 TP()：整串写进 id=api-base/api-chat/
        api-responses/api-messages/api-models 这几个元素的 textContent，
        形如 POST http://host:4398/v1/chat/completions ；
     2. 手机端 m/mobile.js：文档页各行是 origin + /v1/models 这类整串（React
        单文本子节点 → 赋给宿主元素 textContent）；「更多」页那一行把地址与
        '/v1' 拆成两个文本节点（createTextNode / nodeValue）；首页卡片显示的
        是去掉协议的 host:4398/v1 。三者都落在下面 apiText() 的两种形态上；
     3. 复制：桌面走 app/ui/clipboard.js（读 data-copy-from 元素的 DOM 文本，
        已被第 1/2 条覆盖）；手机首页卡片直接复制 Rl()+'/v1' 这个算出来的
        字符串、不读 DOM，所以另拦一层 navigator.clipboard.writeText。
   只认「本页 authority + /v1」这一种形态：外部地址（预设提供商的
   https://api.openai.com/v1 这类）、以及「网关状态」那种不带 /v1 的地址一律不动。 */
function apiText(o){
  if(typeof o!=='string'||AP<=0||o.indexOf('/v1')<0)return o;
  return safe(function(){
    var l=window.location;
    if(!l||!l.hostname)return o;
    if(String(l.port)===String(AP))return o;      /* 直连下游端口打开时本来就是对的 */
    var org=l.origin;                             /* http://host:4398 */
    if(!org||org==='null')return o;
    var auth=l.host;                              /* host:4398（手机端卡片不带协议） */
    var plain=l.hostname+':'+AP;                  /* host:3065 */
    /* 全文替换（不是只认开头）：面板里既有「POST http://host:4398/v1/chat/…」
       这种带前缀的整串，也有「curl http://host:4398/v1/models -H "…"」这种
       嵌在命令示例中间的地址；手机端首页卡片还去掉了协议。
       先长后短：org+'/v1' 是 auth+'/v1' 的超串，先替换掉带协议的就不会重复命中。 */
    var out=o.split(org+'/v1').join('http://'+plain+'/v1');
    if(auth)out=out.split(auth+'/v1').join(plain+'/v1');
    return out;
  })||o;
}

/* 只改写「直接文本子节点」，绝不用容器的 textContent 整串覆写。
   为什么：容器 textContent 会把整棵子树的文字压成一条字符串，写回第一个
   文本节点后，子孙里原本的那些文字还在 —— 同一段话出现两遍，整页文字糊成
   一坨（实测：手机端首页变成几万字的重复串）。这里只碰本层的文本节点，
   元素子节点（比如卡片里的复制图标）一概不动。 */
function apiFixDirectText(el){
  var kids=el.childNodes,i,node,v,joined='',changed=false;
  for(i=0;i<kids.length;i++){
    node=kids[i];
    if(node.nodeType!==3||typeof node.nodeValue!=='string')continue;
    v=node.nodeValue;
    joined+=v;
    var f=apiText(v);
    if(f!==v){node.nodeValue=f;changed=true;}
  }
  if(changed)return;
  /* 单个文本节点都没命中、拼起来才命中：上游把地址与尾部 '/v1' 拆成两个
     文本节点（手机端首页那张 Base URL 卡片）。只在短文本上合并改写，
     混排长句不做合并，免得打乱别的文字与图标之间的顺序。 */
  if(joined.length>256)return;
  var g=apiText(joined);
  if(g===joined)return;
  var first=null;
  for(i=0;i<kids.length;i++){
    node=kids[i];
    if(node.nodeType!==3)continue;
    if(first===null)first=node;else node.nodeValue='';
  }
  if(first!==null)first.nodeValue=g;
}

function pageAuth(){return safe(function(){return window.location.host})||''}

/* 从这次变更牵到的节点往下找「本层文本里带本页 authority」的元素并改写。
   为什么要往下走：React 换页时往往一次挂上整棵子树（childList 的 addedNodes
   只有那棵子树的根），地址行在更深处 —— 只看宿主元素本身会漏（实测漏过
   手机文档页那五行）。剪枝：子树文字里没有 host:port 就不进去；
   预算 4000 个元素，够换页用，也不会变成常驻开销。 */
function apiWalk(node){
  var auth=pageAuth();
  if(!auth||!node)return;
  var el=node.nodeType===3?node.parentNode:node;
  if(!el||el.nodeType!==1)return;
  var stack=[el],n=0,i,kids,cur;
  while(stack.length&&n++<4000){
    cur=stack.pop();
    kids=cur.childNodes;
    if(!kids)continue;
    for(i=0;i<kids.length;i++){
      if(kids[i].nodeType===3&&typeof kids[i].nodeValue==='string'&&kids[i].nodeValue.indexOf(auth)>=0){
        apiFixDirectText(cur);
        break;
      }
    }
    var ch=cur.children;
    if(!ch)continue;
    for(i=ch.length-1;i>=0;i--){
      /* safe(fn) 自己会调用 fn，末尾不能再加一对括号 —— apiWalk 里多写一个 ()
         会对返回值再调用一次并抛错，整批 mutation 直接中断（实测：同一批里
         只有第一个新增节点被纠正，后面的都被跳过）。 */
      var ct=safe(function(x){return function(){return x.textContent}}(ch[i]));
      if(typeof ct==='string'&&ct.indexOf(auth)>=0)stack.push(ch[i]);
    }
  }
}

/* 3. 复制：手机首页卡片把算好的字符串直接交给剪贴板，不经过 DOM。 */
try{
  if(navigator.clipboard&&navigator.clipboard.writeText){
    var _wt=navigator.clipboard.writeText.bind(navigator.clipboard);
    navigator.clipboard.writeText=function(txt){return _wt(apiText(txt))};
  }
}catch(e){}

/* 1 / 2. 渲染：面板把地址写进 DOM 时一定留下一次 mutation ——
   characterData 是就地改文本值（宿主是它的 parentNode），
   childList 是增删文本/元素节点（逐个 addedNode 往下找）。
   改写后文本里不再有本页 authority，回调不会再触发第二次，不会打转。 */
try{
  if(window.MutationObserver&&document.documentElement){
    new MutationObserver(function(muts){
      for(var i=0;i<muts.length;i++){
        var m=muts[i];
        if(m.type==='characterData'){
          var p=m.target&&m.target.parentNode;
          if(p&&p.nodeType===1)safe(function(x){return function(){apiFixDirectText(x)}}(p));
        }else{
          var ns=m.addedNodes;
          if(!ns)continue;
          for(var j=0;j<ns.length;j++)safe(function(x){return function(){apiWalk(x)}}(ns[j]));
        }
      }
    }).observe(document.documentElement,{childList:true,subtree:true,characterData:true});
  }
}catch(e){}
`
