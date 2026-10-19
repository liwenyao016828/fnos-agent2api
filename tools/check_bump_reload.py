#!/usr/bin/env python3
r"""自检：tools/bump_upstream.py 改写 build.py 之后，必须能读到**新**常量。

为什么需要这个自检
------------------
bump_upstream.py 对 build.py 的改写全是**等长**的：

  * UPSTREAM_VERSION        "3.1.0" -> "9.9.9"              （都是 5 个字符）
  * UPSTREAM_BUNDLE_SHA256  {"amd64": "<64 hex>", "arm64": "<64 hex>"}
                            -> 同样长度的另外两个 64 位十六进制（**每个值**都是 64 字符）
  * UPSTREAM_UI_SHA256      64 位十六进制 -> 64 位十六进制   （都是 64 个字符）
  * 树哈希上方注释里的文件数（例如 208 -> 72，等长时才真正等长）

而 CPython 判定 __pycache__ 里的 .pyc 是否新鲜，只看两样东西：**源文件 mtime 的
整秒**和**源文件字节数**（见 importlib._bootstrap_external._validate_timestamp_pyc）。
两者同时命中时，旧 .pyc 被当成有效，importlib.reload() 会原样复用旧字节码 ——
文件明明改了，模块里的常量却还是旧的。

这就造成过一个只在 CI 上偶发、本地复现不出来的失败：第 3 步刚打印出新的树哈希，
第 4 步 verify_webui_provenance() 却拿**旧常量**去比新算出的哈希，报出一句看不出
真因的「上游 WebUI 树哈希与钉住的值不一致」。两个改写之间只隔着一次下载 + 解包 +
同步（实测可以不到一秒），所以只有恰好落在同一整秒里才会命中 —— 随机复现。

v3.1.0 起又多了一处等长改写（UPSTREAM_BUNDLE_SHA256），而且它比树哈希更靠前：
上游改了包但版本号段长不变时，**整条 build.py 的字节数完全不变**，命中概率更高。
所以三处都必须在这里被钉住。

本自检把那个时序**确定性地**造出来
----------------------------------
不等运气，直接把源文件 mtime 钉回「编译那一秒」（见 _pin_mtime_to_second），
然后要求 load_build() 之后的常量等于文件里的值。修好之前必失败，修好之后必通过。

不联网、不改动仓库（全程在临时目录里做）。
"""
import importlib
import importlib.util
import os
import re
import shutil
import sys
import tempfile

sys.stdout.reconfigure(encoding="utf-8", errors="replace")

PROJECT_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TOOLS_DIR = os.path.join(PROJECT_DIR, "tools")


def _pin_mtime_to_second(path, second):
    """把 path 的 mtime 钉在 second 这个**整秒**内。

    .pyc 里记的是 int(源文件 mtime)，所以只要 mtime 的整秒相同、字节数又一样，
    旧 .pyc 就会被判定为仍然有效 —— 这正是 CI 上偶发命中的那个条件。
    这里显式造出来，让自检不依赖运气。
    """
    os.utime(path, (second + 0.25, second + 0.25))


def _len_matched_pin(current):
    """造一个与当前版本号**等长**的假版本号（例如 3.1.0 -> 9.9.9）。

    版本号跨十进制位（9.9.9 -> 10.0.0）时整份 build.py 的字节数会变、.pyc
    自然失效，那样就测不到这个坑了。所以固定取等长值。
    """
    v = "9." + "9." + "9"
    if len(v) != len(current):
        raise SystemExit(f"ERROR: build.py 的 UPSTREAM_VERSION={current!r} 长度 {len(current)}"
                         f" 与自检假设的 'x.y.z'（5 字符）不同，请同步更新本自检")
    return v


def _len_matched_count(build_path):
    """造一个与 build.py 注释里现有文件数**等长**的假值。

    注释写的是「… 208 个文件」。若这里随便传 72（少一位），整份 build.py 的
    字节数就变了，.pyc 自然失效 —— 自检会因为「文件变短」而假通过，测不到
    「等长改写 + 同秒」这个真正的坑。所以按原值的位数造一个同样位数的数。
    """
    with open(build_path, "r", encoding="utf-8") as f:
        text = f.read()
    m = re.search(r"(?m)^# 上游 WebUI（[^）]*）的确定性树哈希 —— (\d+)( 个文件)", text)
    if not m:
        raise SystemExit("ERROR: 在 build.py 里找不到「上游 WebUI（…）的确定性树哈希 —— N 个文件」"
                         "这句注释，请同步更新 rewrite_ui_hash() 与本自检")
    cur = m.group(1)
    return int("1" * len(cur))


def main():
    tmp = tempfile.mkdtemp(prefix="bump-reload-check-")
    try:
        tmp_build = os.path.join(tmp, "build.py")
        shutil.copyfile(os.path.join(PROJECT_DIR, "build.py"), tmp_build)

        # 按 bump_upstream 的方式载入一份临时 build 模块；这一步会写出一条 .pyc，
        # 它的头部记下此刻的 (整秒 mtime, 字节数)。
        sys.path.insert(0, tmp)
        spec = importlib.util.spec_from_file_location("build", tmp_build)
        mod = importlib.util.module_from_spec(spec)
        sys.modules["build"] = mod
        spec.loader.exec_module(mod)
        compile_second = int(os.stat(tmp_build).st_mtime)

        sys.path.insert(0, TOOLS_DIR)
        import bump_upstream as bu
        bu.build = mod
        bu.PROJECT_DIR = tmp

        failures = []

        def check(const, want, sub=None):
            got = getattr(bu.build, const, None)
            if sub is not None:
                got = (got or {}).get(sub) if isinstance(got, dict) else got
                label = f"{const}[{sub!r}]"
            else:
                label = const
            if got != want:
                failures.append((label, got, want))

        # --- 第 1 处改写：UPSTREAM_VERSION（等长）---
        new_ver = _len_matched_pin(mod.UPSTREAM_VERSION)
        size_before = os.path.getsize(tmp_build)
        bu.rewrite_pin(new_ver)
        if os.path.getsize(tmp_build) != size_before:
            failures.append(("(改写未保持字节数)", os.path.getsize(tmp_build), size_before))
        _pin_mtime_to_second(tmp_build, compile_second)
        bu.load_build()
        check("UPSTREAM_VERSION", new_ver)

        # --- 第 2 处改写：UPSTREAM_BUNDLE_SHA256 两个架构（等长，v3.1.0 新增）---
        # 先确认 build.py 里真有这两个键；缺了说明契约没落地，直接报出来，
        # 而不是让下面的 check() 用 None 静默通过/失败。
        if not isinstance(getattr(mod, "UPSTREAM_BUNDLE_SHA256", None), dict):
            print("  [失败] build.py 里没有 UPSTREAM_BUNDLE_SHA256（dict），"
                  "与 bump_upstream 的搬运契约不符")
            return 1
        bundle_new = {"amd64": "a1" * 32, "arm64": "b2" * 32}
        size_before = os.path.getsize(tmp_build)
        bu.rewrite_bundle_hashes(bundle_new)
        if os.path.getsize(tmp_build) != size_before:
            failures.append(("(bundle 改写未保持字节数)",
                             os.path.getsize(tmp_build), size_before))
        _pin_mtime_to_second(tmp_build, compile_second)
        bu.load_build()
        check("UPSTREAM_BUNDLE_SHA256", bundle_new["amd64"], sub="amd64")
        check("UPSTREAM_BUNDLE_SHA256", bundle_new["arm64"], sub="arm64")

        # --- 第 3 处改写：UPSTREAM_UI_SHA256（等长）+ 注释里的文件数（等长）---
        new_hash = "0123456789abcdef" * 4
        # 文件数也必须是等长的假值（真值 208，这里用 137），否则整份文件字节数
        # 一变 .pyc 就自然失效，这个坑就测不到了。
        fake_files = _len_matched_count(tmp_build)
        size_before = os.path.getsize(tmp_build)
        bu.rewrite_ui_hash(new_hash, fake_files)
        if os.path.getsize(tmp_build) != size_before:
            failures.append(("(ui 哈希改写未保持字节数)",
                             os.path.getsize(tmp_build), size_before))
        _pin_mtime_to_second(tmp_build, compile_second)
        bu.load_build()
        check("UPSTREAM_UI_SHA256", new_hash)

        # 磁盘上的文件确实已经是新值（排除「模块对了、文件没改」的假通过）
        with open(tmp_build, "r", encoding="utf-8") as f:
            disk = f.read()
        for arch, want in bundle_new.items():
            if not re.search(rf'(?m)^\s*["\']?{arch}["\']?\s*:\s*["\']?{want}', disk):
                failures.append((f"build.py 磁盘内容 UPSTREAM_BUNDLE_SHA256[{arch}]", "未写入", want))

        if failures:
            print("  [失败] 改写 build.py 后 load_build() 读到的仍是旧常量：")
            for const, got, want in failures:
                print(f"    {const}: 模块里 {got!r} != 文件里 {want!r}")
            print("    —— 字节码缓存没有失效。CI 上这一步会表现为后面某步用到旧常量")
            print("    （例如「上游 WebUI 树哈希与钉住的值不一致」，而前一步刚打印过新哈希）。")
            return 1

        print("  [通过] 同一整秒内的等长改写（三处），load_build() 也能读到新常量")
        print(f"         UPSTREAM_VERSION        = {bu.build.UPSTREAM_VERSION}")
        print(f"         UPSTREAM_BUNDLE_SHA256  = {bu.build.UPSTREAM_BUNDLE_SHA256}")
        print(f"         UPSTREAM_UI_SHA256      = {bu.build.UPSTREAM_UI_SHA256}")
        return 0
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
