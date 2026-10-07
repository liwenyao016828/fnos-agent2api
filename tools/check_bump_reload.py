#!/usr/bin/env python3
"""自检：tools/bump_upstream.py 改写 build.py 之后，必须能读到**新**常量。

为什么需要这个自检
------------------
bump_upstream.py 对 build.py 的两处改写都是**等长**的：

  * UPSTREAM_VERSION    "2.9.0" -> "2.9.5"                （都是 5 个字符）
  * UPSTREAM_UI_SHA256  64 位十六进制 -> 64 位十六进制     （都是 64 个字符）

而 CPython 判定 __pycache__ 里的 .pyc 是否新鲜，只看两样东西：**源文件 mtime 的
整秒**和**源文件字节数**（见 importlib._bootstrap_external._validate_timestamp_pyc）。
两者同时命中时，旧 .pyc 被当成有效，importlib.reload() 会原样复用旧字节码 ——
文件明明改了，模块里的常量却还是旧的。

这就造成过一个只在 CI 上偶发、本地复现不出来的失败：第 3 步刚打印出新的树哈希，
第 4 步 verify_webui_provenance() 却拿**旧常量**去比新算出的哈希，报出一句看不出
真因的「上游 WebUI 树哈希与钉住的值不一致」。两个改写之间只隔着一次下载 + 解包 +
同步（实测可以不到一秒），所以只有恰好落在同一整秒里才会命中 —— 随机复现。

本自检把那个时序**确定性地**造出来
----------------------------------
不等运气，直接把源文件 mtime 钉回「编译那一秒」（见 _pin_mtime_to_second），
然后要求 load_build() 之后的常量等于文件里的值。修好之前必失败，修好之后必通过。

不联网、不改动仓库（全程在临时目录里做）。
"""
import importlib
import importlib.util
import os
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

        def check(const, want):
            got = getattr(bu.build, const, None)
            if got != want:
                failures.append((const, got, want))

        # --- 第 1 处改写：UPSTREAM_VERSION（等长）---
        bu.rewrite_pin("9.9.9")
        _pin_mtime_to_second(tmp_build, compile_second)
        bu.load_build()
        check("UPSTREAM_VERSION", "9.9.9")

        # --- 第 2 处改写：UPSTREAM_UI_SHA256（等长）+ 注释里的文件数（等长）---
        new_hash = "0123456789abcdef" * 4
        bu.rewrite_ui_hash(new_hash, 72)
        _pin_mtime_to_second(tmp_build, compile_second)
        bu.load_build()
        check("UPSTREAM_UI_SHA256", new_hash)

        if failures:
            print("  [失败] 改写 build.py 后 load_build() 读到的仍是旧常量：")
            for const, got, want in failures:
                print(f"    {const}: 模块里 {got!r} != 文件里 {want!r}")
            print("    —— 字节码缓存没有失效。CI 上这一步会表现为第 4 步")
            print("    「上游 WebUI 树哈希与钉住的值不一致」，而第 3 步刚打印过新哈希。")
            return 1

        print("  [通过] 同一整秒内的等长改写，load_build() 也能读到新常量")
        print(f"         UPSTREAM_VERSION   = {bu.build.UPSTREAM_VERSION}")
        print(f"         UPSTREAM_UI_SHA256 = {bu.build.UPSTREAM_UI_SHA256}")
        return 0
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
