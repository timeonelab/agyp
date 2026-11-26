#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""agyp 网关自愈看门狗 —— 由 macOS LaunchAgent 定时调用。

## 为什么存在

`agy` 的所有请求都走 `127.0.0.1:8783` 上的 agyp 反向代理网关，而该网关**没有开机自启**
（历史上靠手工 `agyp daemon start`）。于是有两个手工动作会周期性咬人：

1. **重启机器后** agy 全线报错，直到有人想起手动起网关；
2. 手工起网关时如果 shell 里带着代理变量（agent 沙箱会注入 `HTTP_PROXY=127.0.0.1:49552`），
   网关会继承代理 → 上游全部 502。

本脚本把这两件事自动化：由 launchd 在**干净环境**（无任何代理变量）里调用，且幂等。

还覆盖第三种已实际发生过的故障：**网关进程活着、但内部 `quota_refresher` 线程出网已废**
（表现为所有后台账号的 `last_quota.updated_at` 长期停滞，仅活跃账号由状态栏钩子刷新）。
`is_daemon_running()` 对此完全无感，因为它只探 `/_agyp/status`。判据就是"所有账号的配额时间戳是否整体陈旧"。

## 设计约束（重要）

* **只用系统 python 3.9+**（`/usr/bin/python3`），避免依赖临时虚拟环境或外部沙箱解释器路径。
* **不 `import agyp`**（import 会产生副作用），只通过 `agyp daemon start|stop` 子命令操作。
* **幂等**：网关健康 → 什么都不做，只记一行日志。
* **只在空闲时重启**：`/_agyp/diag` 的 inflight > 0 → 放弃重启（会打断在途 SSE）。

用法：`/usr/bin/python3 ~/.agyp/ensure-gateway.py [--status]`
"""

import json
import os
import re
import subprocess
import sys
import time
import urllib.request
from datetime import datetime, timezone

AGYP_DIR = os.path.expanduser(os.environ.get("AGYP_HOME") or "~/.agyp")
AGYP_BIN = os.path.expanduser(os.environ.get("AGYP_BIN") or "~/.local/bin/agyp")
PYTHON = "/usr/bin/python3"
LOG_FILE = os.path.join(AGYP_DIR, "ensure-gateway.log")
STATE_FILE = os.path.join(AGYP_DIR, "ensure-gateway.state")
GW_LOG = os.path.join(AGYP_DIR, "gateway.log")
LATENCY_FILE = os.path.join(AGYP_DIR, "latency.jsonl")
PROFILES = os.path.join(AGYP_DIR, "profiles.json")

PORT = 8783
STALE_LIMIT = 1500      # 守护进程每 600s 刷一轮 → 超过 2.5 轮未动即判 refresher 已废
IDLE_QUIET = 2          # diag 说没有在途后，再静默 N 秒才动手（与 agyp 本体 IDLE_QUIET_SEC 同步）
RESTART_COOLDOWN = 1800 # 两次自愈重启的最小间隔，防抖

_PROXY_KEYS = ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
               "http_proxy", "https_proxy", "all_proxy", "no_proxy")


def log(msg, level="INFO"):
    line = f"{datetime.now().strftime('%Y-%m-%d %H:%M:%S')} [{level}] {msg}"
    print(line, flush=True)
    try:
        with open(LOG_FILE, "a", encoding="utf-8") as f:
            f.write(line + "\n")
    except Exception:
        pass


def scrub_env():
    """清掉所有代理变量：网关（及其子进程）继承代理就会翻不了墙。"""
    dropped = [k for k in _PROXY_KEYS if k in os.environ]
    for k in dropped:
        os.environ.pop(k, None)
    os.environ.setdefault("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
    return dropped


def probe(timeout=1.0):
    """探 /_agyp/status。显式禁用代理（即使环境里漏了也不受影响）。"""
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(f"http://127.0.0.1:{PORT}/_agyp/status", timeout=timeout) as r:
            return json.loads(r.read().decode()).get("status") == "online"
    except Exception:
        return False


def agyp(*args):
    return subprocess.run([PYTHON, AGYP_BIN, *args], capture_output=True, text=True)


def start_daemon():
    r = agyp("daemon", "start")
    for _ in range(150):  # 最长等 15s
        if probe():
            return True
        time.sleep(0.1)
    log(f"daemon start 未能让网关上线：{r.stdout.strip()}{r.stderr.strip()}", "ERROR")
    return False


def stop_daemon():
    agyp("daemon", "stop")
    for _ in range(150):
        if not probe(timeout=0.3):
            return True
        time.sleep(0.1)
    log("daemon stop 后端口仍在响应", "WARN")
    return False


def parse_age(u):
    try:
        return time.time() - datetime.fromisoformat(u.replace("Z", "+00:00")).timestamp()
    except Exception:
        return None


def quota_state():
    """判 daemon 的 quota_refresher 是否还活着。

    ⚠ 判据必须只看**非 active 账号**：daemon 每 600s 刷**全部**账号，而 agy 的 statusline
    hook 每 30s 只刷 **active** 那一个。所以 active 的时间戳永远新鲜 —— 用它做判据会
    会把「refresher 已废」误判成健康（例如其他备用账号时间戳停滞，只有当前活跃账号仍在被 statusline 刷新）。

    返回 (非 active 账号里最新的年龄秒 or None, 非 active 账号数, 陈旧账号名列表)
    """
    try:
        with open(PROFILES, "r", encoding="utf-8") as f:
            cfg = json.load(f)
    except Exception as e:
        log(f"读 profiles.json 失败：{e}", "ERROR")
        return None, 0, []
    active = cfg.get("active_profile")
    ages, stale_names = [], []
    for name, p in (cfg.get("profiles") or {}).items():
        if name == active:
            continue
        a = parse_age((p.get("last_quota") or {}).get("updated_at") or "")
        if a is None:
            stale_names.append(f"{name}(无数据)")
            continue
        ages.append(a)
        if a > STALE_LIMIT:
            stale_names.append(f"{name}({a / 60:.0f}min)")
    if not ages:
        return None, 0, stale_names
    return min(ages), len(ages), stale_names


def gateway_idle():
    """网关此刻是否真的没有会话在跑。判据与 agyp 本体 restart 保持同一套（R19/R21）。

    主判据 = /_agyp/diag 的 inflight：它直接回答「现在有没有活口」，包括一条跑了
    5 分钟、一行都还没落的 SSE。
    ⚠ 去抖只读 latency.jsonl，**绝不掺 gateway.log 的 mtime**。log 是每个请求都写的
    （access log + 错误日志同一份文件），而本函数之前先 `probe()` 了一次
    `/_agyp/status` —— 于是自己把 log 的 mtime 刷成 0，判据永远「忙」。
    ensure-gateway.log 里 410 行「本次不重启」就是这么来的，包括 10-25 02:54–04:04
    那段 latency.jsonl 一行都没有的凌晨（那正是本脚本该动手的场合）。
    ⚠ latency.jsonl 的 mtime 只当 2s 去抖，不能当「忙」的判据：缓存命中的元数据轮询
    同样落账 ⇒ 只要你在用 agy，mtime 永远是新的，旧版 `min(ages) <= 180` 永远不满足。
    diag 读不出来一律按「忙」处理（宁可不重启，也不能打断会话）。
    """
    try:
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with opener.open(f"http://127.0.0.1:{PORT}/_agyp/diag", timeout=2) as r:
            d = json.loads(r.read().decode())
        now = int((d.get("inflight") or {}).get("now", 1))
    except Exception:
        return False
    if now > 0:
        return False
    try:
        lat_age = time.time() - os.path.getmtime(LATENCY_FILE)
    except Exception:
        return False
    return lat_age > IDLE_QUIET


def cooldown_left():
    try:
        with open(STATE_FILE) as f:
            return max(0, RESTART_COOLDOWN - (time.time() - float(f.read().strip())))
    except Exception:
        return 0


def mark_restart():
    try:
        with open(STATE_FILE, "w") as f:
            f.write(str(time.time()))
    except Exception:
        pass


def main():
    dropped = scrub_env()

    if "--status" in sys.argv:  # 只读巡检，不写日志、不动进程
        age, n, stale = quota_state()
        print(f"清除的代理变量 : {','.join(dropped) or '无'}")
        print(f"网关在线       : {probe()}")
        print(f"非 active 配额 : {'n/a' if age is None else f'最新 {age:.0f}s 前'}（{n} 个账号，阈值 {STALE_LIMIT}s）")
        print(f"陈旧账号       : {', '.join(stale) if stale else '无'}")
        print(f"网关空闲       : {gateway_idle()}（去抖 {IDLE_QUIET}s + diag inflight）")
        print(f"自愈冷却剩余   : {cooldown_left():.0f}s")
        return 0

    if dropped:
        log(f"已清除代理变量：{','.join(dropped)}")

    if not probe():
        log("网关未响应 → 启动")
        if start_daemon():
            log("网关已启动 ✅")
            return 0
        return 1

    # 网关在线：检查 refresher 是否还活着。刚由本脚本启动的轮次跳过（refresher 需几秒才刷完）。
    age, n, stale = quota_state()
    if age is None:
        log(f"网关在线（无非 active 配额数据可判）{('；待观察：' + ', '.join(stale)) if stale else ''}")
        return 0
    if age < STALE_LIMIT:
        log(f"网关在线，非 active 账号 {age:.0f}s 前刚刷新（{n} 个）")
        return 0

    log(f"非 active 账号整体陈旧 {age / 60:.0f}min（阈值 {STALE_LIMIT / 60:.0f}min）→ 疑似 quota_refresher 已废："
        f"{', '.join(stale)}", "WARN")
    if not gateway_idle():
        log("但网关此刻有在途请求/刚有请求（可能在跑 SSE）→ 本次不重启", "WARN")
        return 0
    left = cooldown_left()
    if left > 0:
        log(f"仍在自愈冷却期（剩 {left:.0f}s）→ 跳过", "WARN")
        return 0

    log("网关空闲且配额陈旧 → 重启以恢复 refresher")
    mark_restart()
    stop_daemon()
    if start_daemon():
        log("网关已重启 ✅")
        return 0
    return 1


if __name__ == "__main__":
    sys.exit(main())
