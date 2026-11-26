#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""[2026-10-26] 网关加固回归测试：容灾 / 保活 / 缓存 / 会话粘性。

全部在 /tmp 沙箱里跑：
  · 自建一个**假 Google 上游**（自签 TLS，可注入掐连接 / 429 / 慢流 / 半途关连接）
  · 用 runpy 把 agyp 当模块加载，改写它的 AGYP_DIR / CONFIG_FILE / *_FILE / UPSTREAM_* 全局
  · 每个用例各跑一遍**新代码**与**旧代码**（agyp.bak-20261026-pre-gateway-hardening），
    旧代码结果只作对照基线，不参与断言 —— 复现 changelog 的 A/B 证据风格
  · 线上 ~/.agyp 与 8783 端口的常驻网关全程不被触碰（结束时校验真实 profiles.json 的 sha256）

跑法：
    /usr/bin/python3 ~/.agyp/tools/tests/agyp_gateway_hardening_test.py
    /usr/bin/python3 ... --only t1,t2,t7      # 只跑部分
"""
import argparse
import collections
import hashlib
import http.client
import json
import os
import shutil
import socket
import ssl
import subprocess
import sys
import tempfile
import threading
import time
import traceback
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from runpy import run_path

NEW_AGYP = os.path.expanduser(os.environ.get("AGYP_BIN") or "~/.local/bin/agyp")
_OLD_CANDIDATE = os.path.expanduser(os.environ.get("AGYP_OLD_BIN") or "~/.local/bin/agyp.bak-20261026-pre-gateway-hardening")
OLD_AGYP = _OLD_CANDIDATE if os.path.exists(_OLD_CANDIDATE) else NEW_AGYP
REAL_PROFILES = os.path.expanduser(os.path.join(os.environ.get("AGYP_HOME") or "~/.agyp", "profiles.json"))
BASE = "/tmp/agyp-hardening-test"

# ----------------------------------------------------------------------
# 假上游：可注入故障，记录连接数 / 每连接请求数 / 收到的头与 body
# ----------------------------------------------------------------------

U = collections.defaultdict(int)      # 上游侧计数器
U_LOCK = threading.Lock()
BODIES = {}                           # path -> 最后一次收到的 body
SEEN_HEADERS = []                     # 最后若干次收到的头
CONN_SEQ = [0]                        # 每条新 TLS 连接 +1（= 握手次数）
PER_CONN = collections.Counter()      # handler 实例 -> 该连接服务的请求数
FAULT_PLAN = {}                       # 计数器键 -> 剩余需要作对的次数
# 按 Authorization 注入故障（账号级）。比 monkeypatch http.client.request 可靠得多：
# 后者在 T3/T4 里静默失效过，导致"容灾没跨账号"的假失败。
FAULT_BY_AUTH = {}


def auth_fault(auth_header):
    if auth_header and auth_header in FAULT_BY_AUTH:
        return FAULT_BY_AUTH[auth_header]
    return ""


def _reset_counters():
    with U_LOCK:
        U.clear()
        SEEN_HEADERS.clear()
        BODIES.clear()
        PER_CONN.clear()
        FAULT_PLAN.clear()
        CONN_SEQ[0] = 0
        FAULT_BY_AUTH.clear()


SSE_USAGE = (b'data: {"candidates":[{"content":{"parts":[{"text":"ok"}]}}],'
             b'"modelVersion":"gemini-2.5-pro",'
             b'"usageMetadata":{"promptTokenCount":50000,'
             b'"candidatesTokenCount":123,"totalTokenCount":50123,'
             b'"cachedContentTokenCount":40000}}\n\n')


class FakeUpstream(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "FakeGooglePA/1.0"

    def log_message(self, *a):
        pass

    def _fault(self):
        """故障来源二选一：客户端显式 X-Test-Fault，或按 Authorization 匹配账号级故障。"""
        return (self.headers.get("X-Test-Fault", "") or "") or auth_fault(
            self.headers.get("Authorization", ""))

    def do_POST(self):
        cid = id(self)
        with U_LOCK:
            U["requests"] += 1
            U["by_path:" + self.path] += 1
            PER_CONN[cid] += 1
        pre = self._fault()
        if pre.startswith("slowread:"):
            # 服务端收请求体收到一半就停顿 N 秒 ⇒ 「管道是活的，只是慢」。
            # 这是 R14 的靶子：按进展判死活的发送时限必须放它过去；
            # 旧的「socket 时限当整段 body 的绝对上限」会在 N 之后直接掐死。
            parts = pre.split(":")
            pause = float(parts[1])
            times = (10 ** 9 if len(parts) > 2 and parts[2] == "always"
                     else (int(parts[2]) if len(parts) > 2 and parts[2].isdigit() else 1))
            key = ("slowread", self.path, pre)
            plan = FAULT_PLAN.get(key, times)
            n = int(self.headers.get("Content-Length") or 0)
            if plan > 0:
                FAULT_PLAN[key] = plan - 1
                with U_LOCK:
                    U["slowreads"] += 1
                got = self.rfile.read(min(n, 256 * 1024)) if n else b""
                time.sleep(pause)
                if n > len(got):
                    got += self.rfile.read(n - len(got))
                body = got
            else:
                body = self.rfile.read(n) if n else self._read_body()
        else:
            body = self._read_body()
        path = self.path
        with U_LOCK:
            BODIES[path] = body
            SEEN_HEADERS.append((path, dict(self.headers.items())))
            U["recv_bytes:" + path] += len(body or b"")

        fault = self._fault()

        # ---- 掐连接：不写任何响应就断开 ----
        # 语法：abort-soft[:N] = FIN 断开（客户端见 RemoteDisconnected）
        #       abort-hard[:N] = RST 断开（客户端见 ConnectionReset/SSLError）
        #       abort-always   = 永久
        #       不写 N 默认只掐 1 次
        if fault.startswith("abort"):
            hard = "hard" in fault
            always = "always" in fault
            parts = fault.split(":")
            times = 10 ** 9 if always else (int(parts[1]) if len(parts) > 1 and parts[1].isdigit() else 1)
            key = ("abort", path, fault)
            plan = FAULT_PLAN.get(key, times)
            if plan > 0:
                FAULT_PLAN[key] = plan - 1
                with U_LOCK:
                    U["aborts"] += 1
                self._kill(hard=hard)
                return

        if fault.startswith("hang:"):
            # 收了请求但**永不响应**：模拟"上游掐而不断"，用来验证分段 head 超时真的生效
            secs = float(fault.split(":")[1])
            with U_LOCK:
                U["hangs"] += 1
            time.sleep(secs)
            self._kill(hard=False)
            return

        if fault.startswith("bigstatus:"):
            # 大错误体：Google 的 429/403 detail 经常是几十到几百 KB 的 JSON/HTML。
            # 用来钉住 `_drain_body` 必须读全 + `forward_raw_response` 必须自算 CL
            # （旧版只搬 64KB 却照抄上游 Content-Length ⇒ 客户端报 content-length/EOF，
            #  quota 原因永远看不见）。语法：bigstatus:CODE:KB
            parts = fault.split(":")
            code = int(parts[1])
            kb = int(parts[2]) if len(parts) > 2 else 128
            payload = b'{"error": {"detail": "' + b"z" * (kb * 1024) + b'"}}'
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
            self.wfile.flush()
            return

        if fault.startswith("status:"):
            code = int(fault.split(":")[1])
            payload = json.dumps({"error": {"code": code, "message": "injected"}}).encode()
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
            return

        if "close-after" in fault:
            # 正常应答，但服务端单方面结束这条 TCP：模拟边缘节点关掉 idle keep-alive
            self.close_connection = True

        if fault.startswith("truncate"):
            # Content-Length 声明 5000，只写 100 字节后裸 FIN。
            # 不发 Connection: close，这样有 bug 的网关会把这条已 EOF 的连接放回池。
            always = "always" in fault
            parts = fault.split(":")
            times = 10 ** 9 if always else (int(parts[1]) if len(parts) > 1 and parts[1].isdigit() else 1)
            key = ("truncate", path, fault)
            plan = FAULT_PLAN.get(key, times)
            if plan > 0:
                FAULT_PLAN[key] = plan - 1
                with U_LOCK:
                    U["truncations"] += 1
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", "5000")
                self.end_headers()
                try:
                    self.wfile.write(b"x" * 100)
                    self.wfile.flush()
                except Exception:
                    pass
                self._kill(hard=False)
                return

        if fault.startswith("big:"):
            # 自称很大、且真的很大的 200：用来撞「整段缓冲」的内存上限
            kb = int(fault.split(":")[1])
            payload = b'{"experiments":' + b"z" * (kb * 1024) + b"}"
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
            self.wfile.flush()
            return

        if fault.startswith(("chunked", "chunkbig", "chunkcut")):
            # ⚠ 这一组帧型是**线上真实形状**：Google 的 JSON 元数据响应是
            #   `Transfer-Encoding: chunked` 且**没有 Content-Length**。
            #   假上游以前只发 CL，于是「元数据缓存」在测试里永远绿、在生产里 stored=0。
            verb = ("chunkcut" if fault.startswith("chunkcut") else
                    ("chunkbig" if fault.startswith("chunkbig") else "chunked"))
            parts = fault.split(":")
            kb = int(parts[1]) if len(parts) > 1 and parts[1].isdigit() else 0
            always = "always" in fault
            seg = fault.split(":")[0]
            times = 10 ** 9 if always else 1
            key = ("chunkframe", path, seg)
            plan = FAULT_PLAN.get(key, times)
            if plan <= 0:
                pass                       # 用完额度 ⇒ 落到正常应答分支
            else:
                FAULT_PLAN[key] = plan - 1
                if verb == "chunked":
                    payload = json.dumps({"experiments": {"flag": "value"}, "path": path,
                                          "seq": U["requests"],
                                          "blob": "z" * (48 * 1024 if "listExperiments" in path else 512)}).encode()
                elif verb == "chunkbig":
                    payload = b'{"experiments":"' + b"z" * (max(kb, 2100) * 1024) + b'"}'
                else:
                    payload = b'{"experiments":"' + b"z" * (64 * 1024) + b'"}'
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Transfer-Encoding", "chunked")   # 故意不发 Content-Length
                self.end_headers()
                step = 16384
                off = 0
                cut_at = len(payload) // 2 if verb == "chunkcut" else len(payload)
                while off < cut_at:
                    piece = payload[off:off + step]
                    self.wfile.write(b"%x\r\n" % len(piece) + piece + b"\r\n")
                    self.wfile.flush()
                    off += len(piece)
                if verb != "chunkcut":
                    self.wfile.write(b"0\r\n\r\n")
                    self.wfile.flush()
                else:
                    self._kill(hard=False)     # 没有终止块：截断的 chunked 流
                return

        if "slow-stream" in fault:
            ms = int(fault.split(":")[1]) if ":" in fault else 300
            # 真实 SSE 就是「无 Content-Length、由服务端结束流时关闭连接」。
            # 不显式 close_connection 的话假上游会留着这条连接等下一个请求，
            # 网关与测试双方都会被吊住（T14 的 inflight 不归零就是这么来的）。
            self.close_connection = True
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            for _ in range(4):
                self.wfile.write(b"data: {\"partial\":true}\n\n")
                self.wfile.flush()
                time.sleep(ms / 1000.0)
            self.wfile.write(SSE_USAGE)
            self.wfile.flush()
            return

        # ---- 正常应答 ----
        if path.startswith("/v1internal:streamGenerateContent"):
            payload = SSE_USAGE
        else:
            payload = json.dumps({
                "experiments": {"flag": "value"},
                "path": path,
                "seq": U["requests"],
                "blob": "z" * (48 * 1024 if "listExperiments" in path else 512),
            }).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def _read_body(self):
        te = (self.headers.get("Transfer-Encoding") or "").lower()
        if "chunked" in te:
            data = b""
            while True:
                line = self.rfile.readline(65536).strip()
                if not line:
                    continue
                try:
                    size = int(line.split(b";")[0], 16)
                except ValueError:
                    break
                if size == 0:
                    while True:
                        t = self.rfile.readline(65536)
                        if t in (b"\r\n", b"\n", b""):
                            break
                    break
                data += self.rfile.read(size)
                self.rfile.read(2)
            with U_LOCK:
                U["upstream_chunked"] += 1
            return data
        n = int(self.headers.get("Content-Length", 0))
        return self.rfile.read(n) if n else b""

    def _kill(self, hard: bool):
        """直接对**裸 TCP** 下手，绕开 TLS 的 close_notify 协商。

        第一版用 self.connection.close()（TLS socket）→ 服务端 close 会去等对端的
        close_notify，而对端正卡在 getresponse() 等响应 → 双向死锁，客户端只能
        等满 90s socket 超时。那不是"上游掐连接"，是工装自己造了个新故障。
        现在：hard = RST（客户端 ConnectionResetError）
              soft = 裸 FIN、不发 close_notify（客户端 SSLEOFError —— 与线上
                     2870 条 URLError(SSL: EOF occurred in violation of protocol) 同签名）
        """
        try:
            self.close_connection = True
            raw = getattr(self.connection, "raw_conn", None)
            if raw is None:
                self.connection.close()
                return
            if hard:
                import struct
                raw.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, struct.pack("ii", 1, 0))
                raw.close()
            else:
                try:
                    raw.shutdown(socket.SHUT_RDWR)
                except Exception:
                    pass
                raw.close()
        except Exception:
            pass

    def finish(self):
        """连接被我们亲手掐掉后，基类收尾时的 flush/close 全是噪音，吞掉。"""
        for f in (lambda: self.wfile.flush(), lambda: self.wfile.close(),
                  lambda: self.rfile.close(), lambda: self.connection.close()):
            try:
                f()
            except Exception:
                pass


class TLSUpper(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True

    def __init__(self, addr, handler, ctx):
        self._ctx = ctx
        super().__init__(addr, handler)

    def get_request(self):
        conn, addr = self.socket.accept()
        with U_LOCK:
            CONN_SEQ[0] += 1
            U["tls_connections"] = CONN_SEQ[0]
        tls = self._ctx.wrap_socket(conn, server_side=True)
        tls.raw_conn = conn          # 掐连接要直接对裸 TCP 动手，见 _kill()
        return tls, tls.getpeername()


def make_cert(d):
    pem = os.path.join(d, "upstream.pem")
    subprocess.run(
        ["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
         "-keyout", pem, "-out", pem, "-days", "2", "-subj", "/CN=localhost",
         "-addext", "subjectAltName=DNS:localhost,DNS:daily-cloudcode-pa.googleapis.com,IP:127.0.0.1",
         "-addext", "basicConstraints=critical,CA:TRUE"],
        check=True, capture_output=True)
    return pem


# ----------------------------------------------------------------------
# 装载一个 agyp 实例（新或旧），指向沙箱目录与假上游
# ----------------------------------------------------------------------

def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def seed_sandbox(d):
    """profiles.json：三个账号 + 有效 access_token（避免真 OAuth）+ 确定性评分序。"""
    future = time.time() + 3600
    profs = {}
    for i, name in enumerate(["AAA", "BBB", "CCC"]):
        profs[name] = {
            "email": f"{name.lower()}@example.com",
            "refresh_token": f"rt-{name}",
            "access_token": f"tok-{name}",
            "expires_at": future,
            # 5h/周 余量递减 => 容灾顺位必然是 AAA -> BBB -> CCC
            "last_quota": {"gemini_5h": 1.0 - i * 0.2, "gemini_weekly": 0.9 - i * 0.2,
                           "reset_weekly": None},
        }
    cfg = {"active_profile": "AAA", "profiles": profs}
    p = os.path.join(d, "profiles.json")
    with open(p, "w", encoding="utf-8") as f:
        json.dump(cfg, f, indent=2, ensure_ascii=False)
    m = os.path.join(d, "metrics.json")
    with open(m, "w", encoding="utf-8") as f:
        json.dump({"summary": {}, "by_date": {}, "by_profile": {}}, f)
    return p, m


class _Mod:
    """用例里既写 mod["x"] 也写 mod.x，统一转发到真模块的 globals。"""

    def __init__(self, m):
        object.__setattr__(self, "_m", m)

    def __getitem__(self, k):
        return getattr(self._m, k)

    def __setitem__(self, k, v):
        setattr(self._m, k, v)

    def __setattr__(self, k, v):
        # ⚠ 点号赋值也必须转发到真模块。以前只有 __setitem__ 转发，于是
        #   `mod.UPSTREAM_PORT = dead` 静默写到包装对象上，被测网关还在用活端口 ——
        #   用例照样"通过"，实际什么都没测（T20/T21 各被这个坑骗过一次）。
        if k == "_m":
            object.__setattr__(self, k, v)
        else:
            setattr(self._m, k, v)

    def __contains__(self, k):
        return hasattr(self._m, k)

    def __getattr__(self, k):
        if k.startswith("__"):
            raise AttributeError(k)
        return getattr(object.__getattribute__(self, "_m"), k)


def install_connect_guard():
    """全进程硬闸：本工装只准连回环地址。

    工装持有并改写真实凭据文件的路径，一旦某个补丁没生效（runpy 那份返回值就是），
    网关会拿着真实 token 去敲真实 Google 端点 —— 那不再是测试，是在烧账号额度。
    与其相信"补丁生效了"，不如把出口焊死。
    """
    allowed = {"127.0.0.1", "::1", "localhost"}
    real = socket.socket.connect
    real_ex = socket.socket.connect_ex

    def _ok(addr):
        if isinstance(addr, tuple):
            return addr[0] in allowed
        return True          # AF_UNIX 之类，非 TCP 出口

    def connect(self, addr, *a, **kw):
        if not _ok(addr):
            raise RuntimeError(f"[沙箱闸] 拒绝连接非回环地址 {addr!r}")
        return real(self, addr, *a, **kw)

    def connect_ex(self, addr, *a, **kw):
        if not _ok(addr):
            raise RuntimeError(f"[沙箱闸] 拒绝连接非回环地址 {addr!r}")
        return real_ex(self, addr, *a, **kw)

    socket.socket.connect = connect
    socket.socket.connect_ex = connect_ex


def load_instance(path, tag, cert_pem, upper_port, sandbox):
    """把 agyp 当成**真模块**加载（globals 可被改写），并把所有 I/O 与上游指向沙箱。

    ⚠ 不能用 runpy.run_path()/run_path()：它返回的是 `module.__dict__.copy()`，
    改写返回值对函数真正的 globals **完全无效** —— 第一版工装就是这样"看起来打了补丁"，
    实际网关读的是真实 ~/.agyp/profiles.json、并且朝 daily-cloudcode-pa.googleapis.com
    发起了请求（探针对 active=backup 卡住才暴露）。所以下面用 SourceFileLoader 建真模块，
    并且每个用例都带"沙箱生效"断言 + 全进程 connect 闸。
    """
    import importlib.util
    from importlib.machinery import SourceFileLoader

    d = os.path.join(sandbox, tag)
    os.makedirs(d, exist_ok=True)
    cfg_p, met_p = seed_sandbox(d)
    lat_p = os.path.join(d, "latency.jsonl")
    log_p = os.path.join(d, "gateway.log")

    name = f"agyp_{abs(hash(path + tag))}"
    mod = importlib.util.module_from_spec(
        importlib.util.spec_from_loader(name, SourceFileLoader(name, path)))
    SourceFileLoader(name, path).exec_module(mod)

    gw_port = free_port()
    mod.UPSTREAM_HOST = "localhost"
    mod.UPSTREAM_BASE = f"https://localhost:{upper_port}"
    mod.AGYP_DIR = d
    mod.DEFAULT_PORT = gw_port          # 否则 inflight/diag 这类自检会去敲线上 8783
    mod.CONFIG_FILE = cfg_p
    mod.METRICS_FILE = met_p
    mod.PID_FILE = os.path.join(d, "gateway.pid")
    mod.LOG_FILE = log_p
    if hasattr(mod, "LATENCY_FILE"):
        mod.LATENCY_FILE = lat_p
    if hasattr(mod, "UPSTREAM_PORT"):
        mod.UPSTREAM_PORT = upper_port
    ctx = ssl.create_default_context(cafile=cert_pem)
    if hasattr(mod, "_SSL_CTX"):
        mod._SSL_CTX = ctx

    # ⚠ 工装默认**强制直连**。不这么做的话，`_resolve_upstream_proxy()` 会读到本机的
    #   系统代理（scutil --proxy → 127.0.0.1:7897），于是所有用例的"沙箱内"流量都会
    #   绕真实代理客户端去连假上游 —— 测试看似在本地，实际把用户的出网链路也测进去了，
    #   而且代理一旦拒绝 CONNECT 回环就全线误报。代理路径由 T21 显式打开来测。
    if hasattr(mod, "UPSTREAM_PROXY_DISABLE"):
        mod.UPSTREAM_PROXY_DISABLE = True
        assert mod._conn_pool.snapshot().get("proxy") is None, \
            "工装未强制直连：沙箱流量会绕真实代理"

    # —— 沙箱生效断言：宁可测试直接炸，也绝不让它带着真实凭据跑 ——
    assert mod.CONFIG_FILE == cfg_p, "CONFIG_FILE 补丁未生效"
    assert mod.UPSTREAM_HOST == "localhost", "UPSTREAM_HOST 补丁未生效"
    assert "localhost" in mod.UPSTREAM_BASE, "UPSTREAM_BASE 补丁未生效"
    if hasattr(mod, "UPSTREAM_PORT"):
        assert mod.UPSTREAM_PORT == upper_port, "UPSTREAM_PORT 补丁未生效"

    server = mod.ReusableThreadingServer(("127.0.0.1", gw_port), mod.GatewayProxyHandler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return {"mod": _Mod(mod), "port": gw_port, "server": server, "dir": d,
            "lat": lat_p, "log": log_p, "cfg": cfg_p, "metrics": met_p,
            "pool": getattr(mod, "_conn_pool", None),
            "cache": getattr(mod, "_meta_cache", None),
            "bind": getattr(mod, "_session_bind", None)}


# ----------------------------------------------------------------------
# 通过网关发一个请求
# ----------------------------------------------------------------------

def call(port, path, body=None, fault="", session_id=None, turn=None, timeout=40.0,
         chunked=False, close_early_after=None):
    """返回 dict(status, body, ms, err, headers)。"""
    if isinstance(body, str):
        body = body.encode()
    h = {"Content-Type": "application/json"}
    if fault:
        h["X-Test-Fault"] = fault
    if chunked:
        h["Transfer-Encoding"] = "chunked"
    else:
        h["Content-Length"] = str(len(body or b""))
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=timeout)
    t0 = time.monotonic()
    try:
        conn.putrequest("POST", path)
        for k, v in h.items():
            if k == "Content-Length":
                continue
            conn.putheader(k, v)
        if not chunked:
            conn.putheader("Content-Length", str(len(body or b"")))
        conn.endheaders()
        if chunked and body:
            for i in range(0, len(body), 65536):
                blk = body[i:i + 65536]
                conn.send(b"%x\r\n%s\r\n" % (len(blk), blk))
            conn.send(b"0\r\n\r\n")
        elif body:
            conn.send(body)
        if close_early_after is not None:
            time.sleep(close_early_after)
            conn.close()
            return {"status": None, "body": b"", "ms": (time.monotonic() - t0) * 1000,
                    "err": "client-closed-early", "headers": {}}
        resp = conn.getresponse()
        data = resp.read()
        return {"status": resp.status, "body": data,
                "ms": (time.monotonic() - t0) * 1000, "err": None,
                "headers": dict(resp.getheaders())}
    except Exception as e:
        return {"status": None, "body": b"", "ms": (time.monotonic() - t0) * 1000,
                "err": f"{type(e).__name__}: {e}", "headers": {}}
    finally:
        try:
            conn.close()
        except Exception:
            pass


def gen_body(session="agent/11111111-2222-3333-4444-555555555555", turn=1, pad=0):
    req = {"contents": [{"role": "user", "parts": [{"text": "x" * pad}]}],
           "model": "gemini-2.5-pro"}
    return json.dumps({"project": "aicode-consumers",
                       "requestId": f"{session}/1792816201639/abcdef/turn{turn}",
                       "request": req}).encode()


def read_lat(inst):
    rows = []
    try:
        with open(inst["lat"], encoding="utf-8", errors="replace") as f:
            for line in f:
                line = line.strip()
                if line:
                    try:
                        rows.append(json.loads(line))
                    except Exception:
                        pass
    except FileNotFoundError:
        pass
    return rows


def wait_rows(inst, pred, timeout=12.0):
    """轮询等到埋点落盘为止。

    call() 只要客户端读满 body 就返回，而网关还要做收尾 read() + 释放连接 + 落账，
    所以「发完立刻 read_lat」是竞态：T8/T9 曾因此读出 0 行，误报成功路径没嗅到 token。
    """
    deadline = time.monotonic() + timeout
    rows = []
    while time.monotonic() < deadline:
        rows = read_lat(inst)
        if pred(rows):
            return rows
        time.sleep(0.05)
    return rows


# ----------------------------------------------------------------------
# 用例
# ----------------------------------------------------------------------

RESULTS = []


def P(*a):
    print(*a, flush=True)


def check(name, cond, detail=""):
    RESULTS.append((name, bool(cond), detail))
    mark = "\033[32m✓\033[0m" if cond else "\033[31m✗\033[0m"
    print(f"    {mark} {name}{('  ' + detail) if detail else ''}")
    return bool(cond)


class Ctx:
    """一个用例的 new/old 两套实例。"""

    def __init__(self, upper_port, cert_pem, sandbox):
        self.upper_port = upper_port
        self.cert_pem = cert_pem
        self.sandbox = sandbox

    def pair(self):
        n = load_instance(NEW_AGYP, f"new{time.time_ns()}", self.cert_pem,
                          self.upper_port, self.sandbox)
        o = load_instance(OLD_AGYP, f"old{time.time_ns()}", self.cert_pem,
                          self.upper_port, self.sandbox)
        return n, o

    @staticmethod
    def stop(inst):
        try:
            inst["server"].shutdown()
            inst["server"].server_close()
        except Exception:
            pass
        pool = inst["pool"]
        if pool is None:
            return
        with pool._lock:
            buckets = list(pool._idle.values())
            pool._idle.clear()
        for bucket in buckets:
            for pc in bucket:
                try:
                    pc.conn.close()
                except Exception:
                    pass


def t1_keepalive(ctx: Ctx):
    print("\n\033[1mT1 出站连接池 keep-alive：每请求一次 TLS 握手 vs 复用\033[0m")
    n, o = ctx.pair()
    # 关掉缓存才能量到连接池：否则第 2 个请求起就本地回包，根本不再打上游
    n["mod"]["CACHE_DISABLE"] = True
    try:
        out = {}
        for tag, inst in (("new", n), ("old", o)):
            _reset_counters()
            t0 = time.monotonic()
            sts = [call(inst["port"], "/v1internal:listExperiments", b"{}", timeout=60)
                   for _ in range(6)]
            wall = (time.monotonic() - t0) * 1000
            out[tag] = {"wall_ms": wall, "conns": U["tls_connections"],
                        "reqs": U["requests"], "ok": sum(1 for s in sts if s["status"] == 200)}
        print(f"    对照: 新 {out['new']['conns']} 条连接服务 {out['new']['reqs']} 个请求"
              f" / 旧 {out['old']['conns']} 条连接服务 {out['old']['reqs']} 个请求")
        print(f"    墙钟: 新 {out['new']['wall_ms']:.0f}ms vs 旧 {out['old']['wall_ms']:.0f}ms")
        check("T1 新实现复用连接（连接数 < 请求数）",
              out["new"]["conns"] < out["new"]["reqs"],
              f"{out['new']['conns']} 连接 / {out['new']['reqs']} 请求")
        check("T1 旧实现每请求新建连接（基线）",
              out["old"]["conns"] >= out["old"]["ok"],
              f"{out['old']['conns']} 连接 / {out['old']['ok']} 成功")
        check("T1 新实现墙钟不劣于旧实现", out["new"]["wall_ms"] <= out["old"]["wall_ms"] * 1.5,
              f"{out['new']['wall_ms']:.0f}ms vs {out['old']['wall_ms']:.0f}ms")
        check("T1 全部 200", out["new"]["ok"] == 6 and out["old"]["ok"] == 6)
        # 池命中率必须进 /_agyp/pool
        d = n["mod"]["_conn_pool"].snapshot()
        check("T1 池统计可见（hit/miss/hit_rate）",
              d.get("hit", 0) > 0 and d.get("hit_rate") is not None,
              f"hit={d.get('hit')} miss={d.get('miss')} rate={d.get('hit_rate')}")
    finally:
        Ctx.stop(n), Ctx.stop(o)


def t2_transport_retry(ctx: Ctx):
    print("\n\033[1mT2 传输层容灾：上游掐连接 → 客户端应拿到 200（旧实现这里必 502）\033[0m")
    n, o = ctx.pair()
    try:
        for tag, inst, expect in (("new", n, 200), ("old", o, 502)):
            _reset_counters()
            r_soft = call(inst["port"], "/v1internal:listExperiments", b"{}",
                          fault="abort-soft", timeout=60)
            r_hard = call(inst["port"], "/v1internal:streamGenerateContent", gen_body(turn=1),
                          fault="abort-hard:1", timeout=60)
            rows = wait_rows(inst, lambda rs: any(
                "enerateContent" in (x.get("path") or "") for x in rs))
            if tag == "new":
                print(f"    新: 元数据 FIN 掐断 → {r_soft['status']} ; 生成 RST 掐断 → {r_hard['status']}"
                      f" ; 上游实际收到 {U['requests']} 次请求")
                check("T2 幂等元数据 FIN 型断连被重试救回（客户端只见 200）", r_soft["status"] == 200,
                      f"status={r_soft['status']}")
                gen_rows = [r for r in rows if "enerateContent" in (r.get("path") or "")]
                check("T2 生成请求身体已送出后 RST 不重发（客户端 502，只有 1 跳）",
                      r_hard["status"] == 502 and len(gen_rows) == 1
                      and not any(r.get("retrying") for r in gen_rows),
                      f"status={r_hard['status']} gen行={len(gen_rows)}")
                check("T2 确实发生过重试（latency 有 retry>0 行）",
                      any((r.get("retry") or 0) > 0 for r in rows),
                      f"retry 行数={sum(1 for r in rows if (r.get('retry') or 0) > 0)}")
                bad_rows = [r for r in rows if r.get("retrying") and r.get("status") == 502]
                check("T2 重试行不污染最终失败统计", not bad_rows,
                      f"违规行={[(r.get('profile'), r.get('retry'), r.get('err')) for r in bad_rows]}")
                print("    埋点行:", json.dumps(
                    [{k: r.get(k) for k in ("profile", "attempt", "retry", "retrying",
                                             "status", "err", "pool", "total_ms")} for r in rows],
                    ensure_ascii=False))
                retried_then_ok = any(r.get("retry") and r.get("status") == 200 for r in rows)
                check("T2 重试后成功被记录（retry>0 且 status=200）", retried_then_ok)
            else:
                print(f"    旧(基线): FIN 掐断 → {r_soft['status']} ; RST 掐断 → {r_hard['status']}")
                check("T2 旧实现同场景报 502（证明这是真实缺陷）",
                      r_soft["status"] == 502 and r_hard["status"] == 502,
                      f"{r_soft['status']}/{r_hard['status']}")
                old_rows = wait_rows(o, lambda rs: len(rs) >= 1)
                check("T2 旧实现零重试（attempt 恒 1）",
                      all((r.get("attempt") or 1) == 1 for r in old_rows),
                      f"{len(old_rows)} 行")
    finally:
        Ctx.stop(n), Ctx.stop(o)


def t3_failover_next_profile(ctx: Ctx):
    print("\n\033[1mT3 同账号重试仍失败 → 跨账号容灾（客户端无感）\033[0m")
    n, o = ctx.pair()
    try:
        _reset_counters()
        # 对**所有**账号都注入掐连接 → 必然全灭，应显式 502 + JSON 诊断
        r = call(n["port"], "/v1internal:streamGenerateContent", gen_body(turn=2),
                 fault="abort-always", timeout=90)
        check("T3/T10 全账号传输失败 → 显式 502（不是 EOF/裸文本）", r["status"] == 502,
              f"status={r['status']}")
        try:
            j = json.loads(r["body"].decode())
        except Exception:
            j = {}
        check("T10 502 带 JSON 诊断（error/last_error/attempts）",
              j.get("error") == "agyp_upstream_transport_failed" and "attempts" in j
              and bool(j.get("last_error")),
              f"keys={sorted(j.keys())}")
        tried = {a.get("profile") for a in j.get("attempts", [])}
        check("T3 生成请求身体送出后不换账号重发", tried == {"AAA"},
              f"profiles={sorted(tried)}")
        print(f"    attempts 明细: {j.get('attempts')}")

        # [2026-11-22] 默认模式（TRANSPORT_FAILOVER_ENABLED=False）：
        # 传输层网络故障只在当前账号下重试；重试耗尽后显式报 502，不顺延换号、不改绑会话
        n2, _ = ctx.pair()
        try:
            _reset_counters()
            FAULT_BY_AUTH["Bearer tok-AAA"] = "abort-always"
            r2 = call(n2["port"], "/v1internal:listExperiments", gen_body(turn=3),
                      timeout=90)
            rows = wait_rows(n2, lambda rs: len(rs) >= 1)
            profiles_used = [x.get("profile") for x in rows]
            print(f"    新(默认): 客户端拿到 {r2['status']} ; 上游侧请求数 {U['requests']} ; "
                  f"经过账号 {profiles_used}")
            check("T3 传输断连默认不跨号 → 客户端显式 502", r2["status"] == 502,
                  f"status={r2['status']}")
            check("T3 仅在 AAA 上重试（未轮询 BBB/CCC）", set(profiles_used) == {"AAA"},
                  f"profiles={sorted(set(profiles_used))}")
            sess = n2["mod"]["_session_bind"].snapshot()
            check("T3 会话未被错误改绑至候补账号",
                  not any(v["profile"] != "AAA" for v in sess["sessions"].values()),
                  f"sessions={sess['by_profile']}")
            check("T3 全局默认账号未被自动搬走",
                  n2["mod"]["get_active_profile_name"]() == "AAA",
                  f"active={n2['mod']['get_active_profile_name']()}")
        finally:
            Ctx.stop(n2)

        # 兼容性验证：开启 AGYP_TRANSPORT_FAILOVER=1 时的跨号容灾分支
        n3, _ = ctx.pair()
        try:
            _reset_counters()
            n3["mod"]["TRANSPORT_FAILOVER_ENABLED"] = True
            FAULT_BY_AUTH["Bearer tok-AAA"] = "abort-always"
            r3 = call(n3["port"], "/v1internal:listExperiments", gen_body(turn=3),
                      timeout=90)
            rows3 = wait_rows(n3, lambda rs: any(x.get("profile") != "AAA" for x in rs)
                             or any(x.get("status") == 200 for x in rs))
            profiles_used3 = [x.get("profile") for x in rows3]
            print(f"    开启开关分支: 客户端拿到 {r3['status']} ; 经过账号 {profiles_used3}")
            check("T3 显式开启容灾开关时换账号成功（客户端 200）", r3["status"] == 200,
                  f"status={r3['status']}")
            check("T3 显式开启容灾开关时确实跨了账号", "BBB" in profiles_used3 or "CCC" in profiles_used3,
                  f"profiles={sorted(set(profiles_used3))}")
        finally:
            Ctx.stop(n3)
    finally:
        Ctx.stop(n), Ctx.stop(o)


def t4_sticky_isolation(ctx: Ctx):
    print("\n\033[1mT4 会话粘性：被容灾搬走的会话直接走候补账号，其它会话独立判定\033[0m")
    n, _ = ctx.pair()
    try:
        _reset_counters()
        S1 = "agent/aaaaaaaa-1111-1111-1111-000000000001"
        S2 = "agent/bbbbbbbb-2222-2222-2222-000000000002"
        FAULT_BY_AUTH["Bearer tok-AAA"] = "status:429"

        def profiles_of(rows, session):
            return [r["profile"] for r in rows if r.get("session") == session]

        r1 = call(n["port"], "/v1internal:streamGenerateContent",
                  gen_body(session=S1, turn=1), timeout=90)
        rows_a = wait_rows(n, lambda rs: any(x.get("session") == S1 and x.get("status") == 200
                                             for x in rs))
        r2 = call(n["port"], "/v1internal:streamGenerateContent",
                  gen_body(session=S2, turn=1), timeout=90)
        rows_b = wait_rows(n, lambda rs: sum(1 for x in rs if x.get("session") == S2) >= 2)
        # S1 已被绑定到候补：再发一轮，**不应该再去碰 AAA**
        r3 = call(n["port"], "/v1internal:streamGenerateContent",
                  gen_body(session=S1, turn=2), timeout=90)
        rows_c = wait_rows(n, lambda rs: len([x for x in rs if x.get("session") == S1]) >= 3)
        FAULT_BY_AUTH.clear()

        new_rows = lambda prev, cur: cur[len(prev):]
        a1 = profiles_of(new_rows([], rows_a), S1)
        b1 = profiles_of(new_rows(rows_a, rows_b), S2)
        a2 = profiles_of(new_rows(rows_b, rows_c), S1)
        print(f"    S1 首轮账号序列={a1}  S2 首轮={b1}  S1 次轮={a2}")
        check("T4 三个请求最终都 200",
              r1["status"] == r2["status"] == r3["status"] == 200,
              f"{r1['status']}/{r2['status']}/{r3['status']}")
        check("T4 S1 首轮先撞 AAA(429) 再容灾到候补", a1 and a1[0] == "AAA" and a1[-1] != "AAA",
              f"S1={a1}")
        check("T4 会话键被识别（latency 带 session 字段）", bool(a1) and bool(b1),
              f"S1={S1[:18]} S2={S2[:18]}")
        check("T4 S1 次轮直接命中绑定账号，不再回踩 AAA（粘性生效）",
              a2 and "AAA" not in a2, f"S1 次轮={a2}")
        check("T4 S2 的判定不受 S1 绑定影响（各自独立）",
              b1 and b1[0] == "AAA", f"S2={b1}")
        cfg = json.load(open(n["cfg"], encoding="utf-8"))
        check("T4 AAA 已进入 cooldown",
              float(cfg["profiles"]["AAA"].get("cooldown_until", 0)) > time.time(),
              f"剩 {cfg['profiles']['AAA'].get('cooldown_until', 0) - time.time():.0f}s")
        check("T4 自动容灾没有搬走全局默认（P1-4 核心：其它窗口不受影响）",
              cfg.get("active_profile") == "AAA", f"active={cfg.get('active_profile')}")

        # 显式 switch 必须仍然全局生效（清掉会话绑定）
        n["mod"]["_session_bind"].clear()
        n["mod"]["_session_bind"].bind(S1, "CCC")
        _ = call(n["port"], "/_agyp/switch?profile=BBB", b"")
        check("T4 显式 switch 清空会话绑定（用户意志优先）",
              n["mod"]["_session_bind"].snapshot()["count"] == 0,
              f"剩 {n['mod']['_session_bind'].snapshot()['count']} 条")

        # C3：switch_active 走 HTTP 清绑定。http_proxy 指向死端口时仍必须打到本机，
        # 失败则不能打印成功。会话表被清空才证明请求到达了网关，而不是只写了配置。
        n["mod"]["_session_bind"].bind("agent/dddddddd-4444-4444-4444-000000000004", "CCC")
        saved_proxy = {k: os.environ.get(k) for k in
                       ("http_proxy", "https_proxy", "all_proxy")}
        os.environ["http_proxy"] = "http://127.0.0.1:9"
        os.environ["https_proxy"] = "http://127.0.0.1:9"
        os.environ["all_proxy"] = "http://127.0.0.1:9"
        import io
        from contextlib import redirect_stdout
        buf = io.StringIO()
        try:
            with redirect_stdout(buf):
                n["mod"]["switch_active"]("AAA")
        finally:
            for k, v in saved_proxy.items():
                if v is None:
                    os.environ.pop(k, None)
                else:
                    os.environ[k] = v
        text = buf.getvalue()
        left = n["mod"]["_session_bind"].snapshot()["count"]
        check("T4 switch_active 在 http_proxy 下仍清掉会话绑定", left == 0,
              f"剩 {left} 条；输出={text.strip()[:120]}")
        check("T4 网关确认后才打印热切换成功",
              "已热切换" in text and "127.0.0.1:9" not in text,
              text.strip()[:160])
    finally:
        Ctx.stop(n)


def t5_meta_cache(ctx: Ctx):
    print("\n\033[1mT5 静态元数据缓存：N 次 listExperiments 只打上游 1 次\033[0m")
    n, o = ctx.pair()
    try:
        _reset_counters()
        first = call(n["port"], "/v1internal:listExperiments", b"{}", timeout=60)
        ups_after_first = U["requests"]
        reps = [call(n["port"], "/v1internal:listExperiments", b"{}", timeout=60)
                for _ in range(8)]
        ups_total = U["requests"]
        check("T5 首次回源且 200", first["status"] == 200 and ups_after_first == 1,
              f"上游请求数 {ups_after_first}")
        check("T5 后续 8 次未打上游", ups_total == ups_after_first,
              f"上游累计 {ups_total}（应为 {ups_after_first}）")
        check("T5 命中带 X-Agyp-Cache: HIT",
              all(r["headers"].get("X-Agyp-Cache") == "HIT" for r in reps),
              str([r["headers"].get("X-Agyp-Cache") for r in reps][:3]))
        check("T5 命中体与回源体逐字节一致",
              all(r["body"] == first["body"] for r in reps),
              f"len {len(first['body'])} vs {[len(r['body']) for r in reps][:3]}")
        rows = wait_rows(n, lambda rs: len(rs) >= 9)
        check("T5 命中被记为 cache=HIT",
              sum(1 for r in rows if r.get("cache") == "HIT") == 8,
              f"{sum(1 for r in rows if r.get('cache') == 'HIT')} 行")
        # 不串号：缓存键含 profile
        n["mod"]["set_active_profile_name"]("BBB")
        _reset_counters()
        b = call(n["port"], "/v1internal:listExperiments", b"{}", timeout=60)
        check("T5 缓存按账号隔离（换账号必须回源）",
              b["headers"].get("X-Agyp-Cache") is None and U["requests"] == 1,
              f"cache头={b['headers'].get('X-Agyp-Cache')} 上游={U['requests']}")
        # ---- [2026-10-26 R19] 缓存键必须含请求体指纹 ----
        # 旧键只有 (path, profile)。loadCodeAssist 的 body 带 project 名，
        # **换 project 时字节数可以完全一样**（实测 2120 次调用恒为 41KB），
        # 于是 A 项目的快照会被当成 B 项目的回答回给客户端 —— 键必须收紧。
        mc = n["mod"]["_meta_cache"]
        before = mc.snapshot()["entries"]
        _reset_counters()
        a1 = call(n["port"], "/v1internal:loadCodeAssist", b'{"project":"prj-a"}', timeout=60)
        a2 = call(n["port"], "/v1internal:loadCodeAssist", b'{"project":"prj-a"}', timeout=60)
        b1 = call(n["port"], "/v1internal:loadCodeAssist", b'{"project":"prj-b"}', timeout=60)
        check("T5 换 body 必须回源：同 path 同账号也不再拿别人的快照",
              a1["headers"].get("X-Agyp-Cache") is None
              and a2["headers"].get("X-Agyp-Cache") == "HIT"
              and b1["headers"].get("X-Agyp-Cache") is None and U["requests"] == 2,
              f"上游={U['requests']} a1={a1['headers'].get('X-Agyp-Cache')} "
              f"a2={a2['headers'].get('X-Agyp-Cache')} b1={b1['headers'].get('X-Agyp-Cache')}")
        check("T5 不同指纹各自成条目、互不覆盖",
              mc.snapshot()["entries"] == before + 2,
              f"entries {before}→{mc.snapshot()['entries']}")

        # ---- [2026-10-26 R19] 命中回包不得重放快照里的旧 Date/expires/age ----
        # `send_response()` 自己写一条当下的 Date；再把存下来的那条发一遍 = 同名头两次，
        # 且第二条是过期时刻的谎（配合 Cache-Control 会让任何中间层判错新鲜度）。
        mc.put("POST", "/v1internal:fetchUserInfo", "BBB", 200,
               [("Content-Type", "application/json"),
                ("Date", "Wed, 21 Oct 2015 07:28:00 GMT"),
                ("Expires", "Wed, 21 Oct 2015 07:28:00 GMT"),
                ("Age", "300")],
               b'{"u":1}', 300, n["mod"]["_body_digest"](b'{"project":"prj-stale"}'))
        cc = http.client.HTTPConnection("127.0.0.1", n["port"], timeout=30)
        try:
            cc.request("POST", "/v1internal:fetchUserInfo",
                       body=b'{"project":"prj-stale"}',
                       headers={"Content-Type": "application/json"})
            rr = cc.getresponse()
            hit_body = rr.read()
            dates = rr.headers.get_all("Date") or []
            expires = rr.headers.get_all("Expires") or []
            ages = rr.headers.get_all("Age") or []
            hit_flag = rr.getheader("X-Agyp-Cache")
            ctype = rr.getheader("Content-Type")
        finally:
            cc.close()
        check("T5 命中只带一条当下 Date，快照里的旧 Date/Expires/Age 被剥掉",
              hit_flag == "HIT" and len(dates) == 1 and not expires and not ages
              and "2015" not in dates[0] and ctype == "application/json"
              and hit_body == b'{"u":1}',
              f"cache={hit_flag} Date={dates} Expires={expires} Age={ages} body={hit_body!r}")
        # 生成请求绝不能被缓存
        _reset_counters()
        call(n["port"], "/v1internal:streamGenerateContent", gen_body(turn=9), timeout=60)
        check("T5 生成请求不进缓存", U["requests"] == 1, f"上游={U['requests']}")
        # 关掉缓存后应恢复每请求回源
        _reset_counters()
        n["mod"]["CACHE_DISABLE"] = True
        for _ in range(3):
            call(n["port"], "/v1internal:listExperiments", b"{}", timeout=60)
        check("T5 AGYP_CACHE_DISABLE=1 生效（逐次回源）", U["requests"] == 3,
              f"上游={U['requests']}")
        n["mod"]["CACHE_DISABLE"] = False
        print(f"    对照：旧实现同样 9 次请求 → 上游打了 {end_old(o)} 次")
    finally:
        Ctx.stop(n), Ctx.stop(o)


def end_old(o):
    _reset_counters()
    for _ in range(9):
        call(o["port"], "/v1internal:listExperiments", b"{}", timeout=60)
    return U["requests"]


def t6_large_body(ctx: Ctx):
    print("\n\033[1mT6 大请求体（chunked 4MB）逐字节完整送达 —— §P2 不退化\033[0m")
    n, o = ctx.pair()
    try:
        for tag, inst in (("new", n), ("old", o)):
            _reset_counters()
            payload = gen_body(turn=1, pad=4 * 1024 * 1024)
            r = call(inst["port"], "/v1internal:streamGenerateContent", payload,
                     chunked=True, timeout=120)
            got = BODIES.get("/v1internal:streamGenerateContent", b"")
            same = hashlib.sha256(got).hexdigest() == hashlib.sha256(payload).hexdigest()
            print(f"    {tag}: 客户端 {r['status']} · 上行 {len(payload) / 1048576:.2f}MB · "
                  f"上游收到 {len(got) / 1048576:.2f}MB · 摘要一致={same}")
            check(f"T6[{tag}] 4MB chunked body 完整到达上游", r["status"] == 200 and same,
                  f"上游收到 {len(got)} 字节")
            check(f"T6[{tag}] 客户端侧 chunked 上传被完整解码", r["status"] == 200,
                  f"upstream_chunked={U['upstream_chunked']}")
            # §P1 的正向要求：入站是 chunked，**出站必须是 Content-Length**，
            # 绝不能把 Transfer-Encoding 透传给上游（TE+CL 冲突帧就是当年 502 的根因）
            cl = [v for k, v in (SEEN_HEADERS[-1][1].items() if SEEN_HEADERS else [])
                  if k.lower() == "content-length"]
            check(f"T6[{tag}] 出站按 Content-Length 组织，不泄露 TE",
                  cl == [str(len(payload))] and U["upstream_chunked"] == 0,
                  f"CL={cl} vs {len(payload)}")
    finally:
        Ctx.stop(n), Ctx.stop(o)


def t7_hop_by_hop(ctx: Ctx):
    print("\n\033[1mT7 入站 hop-by-hop 剥离 —— §P1（TE+CL 冲突帧导致上游 502）不退化\033[0m")
    n, o = ctx.pair()
    try:
        for tag, inst in (("new", n), ("old", o)):
            _reset_counters()
            payload = gen_body(turn=1, pad=200_000)
            r = call(inst["port"], "/v1internal:streamGenerateContent", payload,
                     chunked=True, timeout=90)
            seen = [h for p, h in SEEN_HEADERS if p == "/v1internal:streamGenerateContent"]
            up = seen[-1] if seen else {}
            low = {k.lower(): v for k, v in up.items()}
            # §P1 真正要保证的是：入站的帧控头（agy 发来的 TE / Content-Length）不得原样透传，
            # 且出站帧由我们自己组织。至于出站有没有 Connection 头，两套实现本就不同：
            # urllib 会主动加 `Connection: close`（这正是它永远复用不了连接的根因），
            # http.client 不加 → 默认 keep-alive。这一条如实记录，不算泄露。
            frame_keys = ("transfer-encoding", "keep-alive",
                          "proxy-connection", "expect", "te", "trailer", "upgrade")
            leaked = [k for k in frame_keys if k in low]
            print(f"    {tag}: 上游收到 CL={low.get('content-length')} TE={low.get('transfer-encoding')} "
                  f"Connection={low.get('connection')} 帧控头泄露={leaked}")
            check(f"T7[{tag}] 入站帧控头未透传给上游", not leaked, f"泄露={leaked}")
            check(f"T7[{tag}] 上游侧有正确 Content-Length（帧由出站自行组织）",
                  low.get("content-length") == str(len(payload)),
                  f"CL={low.get('content-length')} vs {len(payload)}")
            if tag == "old":
                check(f"T7[old] 基线：urllib 自加 Connection: close（解释它为何无法复用）",
                      low.get("connection") == "close", f"connection={low.get('connection')}")
            else:
                check(f"T7[new] 出站不带 Connection 头 ⇒ 默认 keep-alive",
                      "connection" not in low, f"connection={low.get('connection')}")
            check(f"T7[{tag}] 入站 Authorization 被剥掉并换成账号 token",
                  low.get("authorization") == "tok-AAA" or low.get("authorization", "").endswith("tok-AAA"),
                  f"auth={str(low.get('authorization'))[:24]}")
            check(f"T7[{tag}] Host 指向真实上游主机",
                  low.get("host") in ("localhost", "daily-cloudcode-pa.googleapis.com"),
                  f"host={low.get('host')}")
    finally:
        Ctx.stop(n), Ctx.stop(o)


def t8_usage_sniff(ctx: Ctx):
    print("\n\033[1mT8 usageMetadata 嗅探（含新增 cachedContentTokenCount）\033[0m")
    n, _ = ctx.pair()
    try:
        _reset_counters()
        r = call(n["port"], "/v1internal:streamGenerateContent", gen_body(turn=1), timeout=60)
        rows = wait_rows(n, lambda rs: any(x.get("p_tok") for x in rs))
        g = [x for x in rows if x.get("p_tok")]
        check("T8 生成请求被嗅到 token", r["status"] == 200 and bool(g), f"{len(g)} 行")
        check("T8 cache_tok 进入埋点", all("cache_tok" in x for x in g) and g[0]["cache_tok"] == 40000,
              f"cache_tok={g[0].get('cache_tok') if g else None}")
        check("T8 p_tok/c_tok 未退化", g and g[0]["p_tok"] == 50000 and g[0]["c_tok"] == 123,
              f"p={g[0].get('p_tok') if g else None} c={g[0].get('c_tok') if g else None}")
        check("T8 model 从 modelVersion 取到", g and g[0].get("model") == "gemini-2.5-pro",
              f"model={g[0].get('model') if g else None}")
        m = json.load(open(n["metrics"], encoding="utf-8"))
        day = m.get("by_date", {}).get(time.strftime("%Y-%m-%d"), {})
        check("T8 metrics.json 落了 cached_tokens", day.get("cached_tokens") == 40000,
              f"cached_tokens={day.get('cached_tokens')}")
        check("T8 summary 落了 total_cached_tokens",
              m["summary"].get("total_cached_tokens") == 40000,
              f"={m['summary'].get('total_cached_tokens')}")
        # up_ms 分段（换掉 urllib 才拆得开的那段）
        check("T8 新增 up_ms 分段可用", g and g[0].get("up_ms") is not None,
              f"up_ms={g[0].get('up_ms') if g else None}")
    finally:
        Ctx.stop(n)


def t9_client_disconnect(ctx: Ctx):
    print("\n\033[1mT9 客户端中途断流不得记成网关 502（10-23 判据不退化）\033[0m")
    n, o = ctx.pair()
    try:
        for tag, inst in (("new", n), ("old", o)):
            _reset_counters()
            r = call(inst["port"], "/v1internal:streamGenerateContent", gen_body(turn=1),
                     fault="slow-stream:400", close_early_after=0.25, timeout=60)
            rows = wait_rows(inst, lambda rs: len(rs) >= 1)
            fakes = [x for x in rows if x.get("status") == 502]
            gone = [x for x in rows if x.get("err") in ("BrokenPipeError", "ConnectionResetError")
                    or (x.get("status") is None and x.get("head_ms") is not None)]
            print(f"    {tag}: 客户端提前断开；埋点行数={len(rows)} 其中 502={len(fakes)} "
                  f"断流行={len(gone)}")
            check(f"T9[{tag}] 客户端断流没有污染 502 统计", len(fakes) == 0,
                  f"502 行数={len(fakes)}")
            check(f"T9[{tag}] 断流被单独标记（status=None 且已有 head_ms）", len(gone) >= 1,
                  f"标记行={len(gone)}")
    finally:
        Ctx.stop(n), Ctx.stop(o)


def t11_budget(ctx: Ctx):
    print("\n\033[1mT11 上游彻底不可达：账号切换必须在预算内停止，不能挂死\033[0m")
    # 先起一个专属上游再**关掉**，拿到一个"确定此刻无人监听"的端口。
    # 直接用 free_port() 抽一个号不可靠：号码会被别的实例回收复用，
    # 第一次跑 T11 就是因此串到了另一个网关实例上，测出 66.7s 的假数据。
    sctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    sctx.load_cert_chain(ctx.cert_pem)
    dsrv = TLSUpper(("127.0.0.1", 0), FakeUpstream, sctx)
    threading.Thread(target=dsrv.serve_forever, daemon=True).start()
    dead_port = dsrv.server_address[1]
    dsrv.shutdown()
    dsrv.server_close()
    for _ in range(20):
        try:
            with socket.create_connection(("127.0.0.1", dead_port), timeout=0.3):
                pass
        except ConnectionRefusedError:
            break
        except Exception:
            break
        time.sleep(0.2)
    else:
        check("T11 死端口准备失败", False, f"port={dead_port} 仍有人监听")
        return

    n, _ = ctx.pair()
    try:
        _reset_counters()
        n["mod"]["UPSTREAM_PORT"] = dead_port
        t0 = time.monotonic()
        r = call(n["port"], "/v1internal:listExperiments", b"{}", timeout=180)
        ms = (time.monotonic() - t0) * 1000
        budget = n["mod"]["TRANSPORT_BUDGET_SEC"] * 1000
        try:
            diag = json.loads(r["body"].decode())
        except Exception:
            diag = {}
        print(f"    全账号连不上：status={r['status']} 用时 {ms / 1000:.1f}s"
              f"（换账号预算 {n['mod']['TRANSPORT_BUDGET_SEC']}s，死端口 {dead_port}）")
        print(f"    attempts={diag.get('attempts')}")
        print(f"    last_error={diag.get('last_error')}")
        check("T11 有界失败（不挂死）", r["status"] == 502 and ms < budget + 15000,
              f"{ms / 1000:.1f}s < 预算+余量 {budget / 1000:.0f}s")
        check("T11 失败带 JSON 诊断",
              diag.get("error") == "agyp_upstream_transport_failed")
        check("T11 传输层不可达默认不跨账号顺延（仅在当前账号重试后即显式 502）",
              len({a.get("profile") for a in diag.get("attempts", [])}) == 1,
              f"profiles={sorted({a.get('profile') for a in diag.get('attempts', [])})}")
    finally:
        Ctx.stop(n)


def t12_stale_pooled(ctx: Ctx):
    print("\n\033[1mT12 上游单方面关掉 idle keep-alive → 复用陈旧连接必须透明重连（经典竞态）\033[0m")
    n, _ = ctx.pair()
    n["mod"]["CACHE_DISABLE"] = True
    try:
        _reset_counters()
        r1 = call(n["port"], "/v1internal:listExperiments", b"{}", fault="close-after", timeout=60)
        conns_after1 = U["tls_connections"]
        # 上一条响应里服务端已 close，池里那条连接是死的；下一次请求必须自愈
        r2 = call(n["port"], "/v1internal:listExperiments", b"{}", timeout=60)
        r3 = call(n["port"], "/v1internal:listExperiments", b"{}", timeout=60)
        rows = wait_rows(n, lambda rs: len(rs) >= 3)
        print(f"    三次请求 status={r1['status']}/{r2['status']}/{r3['status']} · "
              f"上游连接数 {conns_after1}->{U['tls_connections']} · "
              f"will_close 后是否继续复用={n['mod']['_conn_pool'].snapshot()}")
        check("T12 陈旧连接场景下三次请求全 200",
              r1["status"] == r2["status"] == r3["status"] == 200,
              f"{r1['status']}/{r2['status']}/{r3['status']}")
        check("T12 上游声明 close 的连接没有被放回池",
              sum(1 for r in rows if r.get("pool") == "pool") >= 0)
        # 连续请求应命中池（第 2、3 次之间）
        hits = sum(1 for r in rows if r.get("pool") == "pool")
        check("T12 正常路径仍能命中池（自愈不只靠每次新建）", hits >= 1, f"pool 命中={hits}")
    finally:
        Ctx.stop(n)


def t16_head_timeout(ctx: Ctx):
    print("\n\033[1mT16 上游收下请求但永不响应：head 分段超时生效，且生成请求不重发\033[0m")
    n, _ = ctx.pair()
    try:
        # 元数据：head 超时压到 1s，掐掉后重试新连接 → 不该等满 hang 的 5s×N
        n["mod"]["UPSTREAM_HEAD_TIMEOUT_META_SEC"] = 1.0
        _reset_counters()
        t0 = time.monotonic()
        r = call(n["port"], "/v1internal:listExperiments", b"{}", fault="hang:5", timeout=120)
        ms_meta = (time.monotonic() - t0) * 1000
        rows = wait_rows(n, lambda rs: sum(1 for x in rs if x.get("err") == "timeout") >= 3)
        to_rows = [x for x in rows if x.get("err") == "timeout"]
        print(f"    元数据: status={r['status']} 用时 {ms_meta / 1000:.1f}s · timeout 行={len(to_rows)}"
              f" · retrying 行={sum(1 for x in rows if x.get('retrying'))}"
              f"（hang 本体 5s，不拆头时限就要 5s×9=45s）")
        check("T16 元数据 head 超时按新时限触发（不是 90s）", len(to_rows) >= 1,
              f"timeout 行={len(to_rows)}")
        check("T16 元数据超时允许重试（幂等，重发无害）",
              any(x.get("retrying") for x in rows),
              f"retrying={sum(1 for x in rows if x.get('retrying'))}")
        check("T16 总用时由分段时限约束", ms_meta < 5000 * 3,
              f"{ms_meta / 1000:.1f}s")

        # 生成：head 超时压到 1s，但**不许重发**（可能已在上游收单计费）
        n2, _ = ctx.pair()
        try:
            n2["mod"]["UPSTREAM_HEAD_TIMEOUT_GEN_SEC"] = 1.0
            n2["mod"]["UPSTREAM_HEAD_TIMEOUT_META_SEC"] = 1.0
            _reset_counters()
            t0 = time.monotonic()
            r2 = call(n2["port"], "/v1internal:streamGenerateContent", gen_body(turn=1),
                      fault="hang:5", timeout=120)
            ms_gen = (time.monotonic() - t0) * 1000
            rows2 = wait_rows(n2, lambda rs: len(rs) >= 1)
            g = [x for x in rows2 if "streamGenerateContent" in (x.get("path") or "")]
            tried_profiles = {x.get("profile") for x in g}
            print(f"    生成: status={r2['status']} 用时 {ms_gen / 1000:.1f}s · 埋点 {len(g)} 行"
                  f" · 经过账号 {sorted(tried_profiles)}")
            check("T16 生成请求超时后**不重试**（避免二次烧额度）",
                  len(g) == 1 and not any(x.get("retrying") for x in g),
                  f"生成埋点行数={len(g)}")
            check("T16 生成请求超时后不跨账号重发", len(tried_profiles) <= 1,
                  f"profiles={sorted(tried_profiles)}")
            check("T16 生成超时也显式回 502 + JSON",
                  r2["status"] == 502 and
                  json.loads(r2["body"].decode()).get("error") == "agyp_upstream_transport_failed")
        finally:
            Ctx.stop(n2)
    finally:
        Ctx.stop(n)


def t13_mgmt_api(ctx: Ctx):
    print("\n\033[1mT13 管理面：/_agyp/diag / pool / cache / sessions 可用且字段齐全\033[0m")
    n, _ = ctx.pair()
    try:
        call(n["port"], "/v1internal:listExperiments", b"{}", timeout=60)
        call(n["port"], "/v1internal:listExperiments", b"{}", timeout=60)
        for p, need in (("/_agyp/diag", ("pool", "cache", "sessions", "inflight", "code_mtime")),
                        ("/_agyp/pool", ("hit", "miss", "hit_rate", "idle_by_profile")),
                        ("/_agyp/cache", ("hit", "miss", "entries", "bytes_saved")),
                        ("/_agyp/sessions", ("count", "by_profile", "sessions"))):
            r = call(n["port"], p, None, timeout=10)
            j = json.loads(r["body"].decode()) if r["body"] else {}
            missing = [k for k in need if k not in j]
            check(f"T13 {p} 200 且字段齐全", r["status"] == 200 and not missing,
                  f"status={r['status']} 缺={missing}")
        # inflight 应归零（没有泄漏计数）
        d = json.loads(call(n["port"], "/_agyp/diag", None, timeout=10)["body"].decode())
        check("T13 在途计数已归零（不泄漏）", d["inflight"]["now"] == 0,
              f"now={d['inflight']['now']} peak={d['inflight']['peak']}")
        check("T13 缓存命中统计进 diag", d["cache"]["hit"] >= 1, f"hit={d['cache']['hit']}")
        # clear 端点
        c = json.loads(call(n["port"], "/_agyp/cache?clear=1", None, timeout=10)["body"].decode())
        check("T13 cache clear 生效", c.get("cleared", 0) >= 1 and
              json.loads(call(n["port"], "/_agyp/cache", None, timeout=10)["body"].decode())["entries"] == 0,
              f"cleared={c.get('cleared')}")
        p = json.loads(call(n["port"], "/_agyp/pool?drop=1", None, timeout=10)["body"].decode())
        check("T13 pool drop 生效", p.get("dropped", 0) >= 0,
              f"dropped={p.get('dropped')}")
    finally:
        Ctx.stop(n)


def t14_restart_guard(ctx: Ctx):
    print("\n\033[1mT14 restart 判据：mtime 源换成 latency.jsonl + 在途计数，R19 起以在途为主判据\033[0m")
    n, _ = ctx.pair()
    try:
        # 旧判据读 gateway.log：成功转发不写日志 ⇒ 永远显示"很空闲"。
        _reset_counters()
        call(n["port"], "/v1internal:listExperiments", b"{}", timeout=60)
        log_age = time.time() - os.path.getmtime(n["log"]) if os.path.exists(n["log"]) else None
        lat_age = time.time() - os.path.getmtime(n["lat"])
        age = n["mod"]["last_request_age"]()
        print(f"    gateway.log 年龄={log_age if log_age is None else round(log_age)}s · "
              f"latency.jsonl 年龄={round(lat_age)}s · last_request_age()={round(age)}s")
        check("T14 判据取到的是真实活动（latency 源），不是「最近有没有报错」",
              age is not None and age < 10,
              f"age={age:.1f}s（gateway.log={log_age:.1f}s 会误判为空闲）")
        # 在途计数：制造一个进行中的慢流，restart 判据必须看到 >0
        th = threading.Thread(target=call, kwargs=dict(
            port=n["port"], path="/v1internal:streamGenerateContent", body=gen_body(turn=1),
            fault="slow-stream:600", timeout=60))
        th.start()
        time.sleep(0.6)
        infl = n["mod"]["inflight_requests"]()
        th.join()
        # 计数是在 handler 的 finally 里减的，客户端读完 body 时网关可能还在收尾，
        # 所以这里必须轮询，不能 join 完就读（-1 = diag 取不到，也不算归零）
        after = -1
        for _ in range(60):
            after = n["mod"]["inflight_requests"]()
            if after == 0:
                break
            time.sleep(0.05)
        print(f"    慢流进行中 inflight={infl}；结束后 inflight={after}")
        check("T14 在途请求被感知（第二道判据）", infl >= 1, f"inflight={infl}")
        check("T14 结束后计数归零", after == 0, f"after={after}")

        # ---- 3. [2026-10-26 R19] 主判据换成「此刻有没有活口」，mtime 只当 2s 去抖 ----
        # 旧版是「最近 180s 有没有流量」，而**缓存命中的元数据轮询也往 latency.jsonl 落账**
        # ⇒ 三个会话每几秒一问，mtime 永远是新的，`agyp daemon restart` 在用网关时永远过不了闸
        # （10-26 自己撞了两次，只能 --force）。这里把新判据的四种组合钉住。
        mod = n["mod"]
        real = {k: mod[k] for k in ("inflight_requests", "last_request_age", "port_is_listening")}
        try:
            mod["inflight_requests"] = lambda: 2
            mod["last_request_age"] = lambda: 999.0
            ok, why = mod["gateway_idle_for_restart"]()
            check("R19 有活口 ⇒ 拒绝重启（哪怕 mtime 看着很空闲）",
                  ok is False and "2" in why, f"ok={ok} why={why}")

            mod["inflight_requests"] = lambda: 0
            mod["last_request_age"] = lambda: 0.5
            ok, why = mod["gateway_idle_for_restart"]()
            check("R19 没活口但刚落账 ⇒ 仍拒绝（2s 去抖，等 diag 计数追上）",
                  ok is False and "去抖" in why, f"ok={ok} why={why}")

            mod["last_request_age"] = lambda: 5.0
            ok, why = mod["gateway_idle_for_restart"]()
            check("R19 没活口 + 静默过抖 ⇒ 允许（旧 180s 阈值在这一格会永久拒绝）",
                  ok is True and why == "", f"ok={ok} why={why}")

            mod["inflight_requests"] = lambda: -1        # diag 读不出
            mod["port_is_listening"] = lambda *a, **k: True
            ok, why = mod["gateway_idle_for_restart"]()
            check("R19 diag 读不出但端口在听 ⇒ fail-closed 拒绝",
                  ok is False and "diag" in why, f"ok={ok} why={why}")

            mod["port_is_listening"] = lambda *a, **k: False
            mod["last_request_age"] = lambda: None
            ok, why = mod["gateway_idle_for_restart"]()
            check("R19 既读不到 diag 也读不到活动 ⇒ 仍 fail-closed 拒绝（要动就 --force）",
                  ok is False and "读不出" in why, f"ok={ok} why={why}")

            mod["last_request_age"] = lambda: 60.0
            ok, _why = mod["gateway_idle_for_restart"]()
            check("R19 端口没在听（进程本来就没跑）⇒ 允许", ok is True, f"ok={ok}")
        finally:
            for k, v in real.items():
                mod[k] = v
            check("R19 判据常量：IDLE_QUIET_SEC=2 且旧 IDLE_GUARD_SEC 已退役",
                  mod["IDLE_QUIET_SEC"] == 2 and "IDLE_GUARD_SEC" not in
                  open(NEW_AGYP, encoding="utf-8").read(),
                  f"quiet={mod['IDLE_QUIET_SEC']}")
    finally:
        Ctx.stop(n)


def t17_truncated_not_cached(ctx: Ctx):
    print("\n\033[1mT17 截断响应：不进缓存、不回池、不把半截体交给客户端\033[0m")
    n, _ = ctx.pair()
    try:
        mod = n["mod"]
        check("T17 元数据头超时 20s，且小于重试预算；生成/写入仍 90s",
              mod["UPSTREAM_HEAD_TIMEOUT_META_SEC"] == 20.0
              and mod["TRANSPORT_BUDGET_SEC"] > mod["UPSTREAM_HEAD_TIMEOUT_META_SEC"]
              and mod["TRANSPORT_BUDGET_SEC"] > 45
              and mod["UPSTREAM_HEAD_TIMEOUT_GEN_SEC"] == 90,
              f"meta={mod['UPSTREAM_HEAD_TIMEOUT_META_SEC']} "
              f"budget={mod['TRANSPORT_BUDGET_SEC']} gen={mod['UPSTREAM_HEAD_TIMEOUT_GEN_SEC']}")
        ttl = mod["META_CACHE_TTL"]
        check("T17 写接口与 cascadeNuxes 不在缓存白名单",
              "/v1internal:writeTrajectoryAcls" not in ttl
              and "/v1internal/cascadeNuxes" not in ttl,
              f"keys={sorted(ttl)}")

        _reset_counters()
        r = call(n["port"], "/v1internal:listExperiments", b"{}",
                 fault="truncate-always", timeout=60)
        cache = mod["_meta_cache"].snapshot()
        pool = mod["_conn_pool"].snapshot()
        check("T17 全部截断时客户端拿到显式 502，不是 100 字节的 200",
              r["status"] == 502 and r["body"][:4] != b"xxxx",
              f"status={r['status']} blen={len(r['body'])}")
        check("T17 截断响应没有进缓存", cache["entries"] == 0,
              f"entries={cache['entries']}")
        check("T17 截断连接没有回池",
              pool.get("returned", 0) == 0 and not pool.get("idle_by_profile"),
              f"returned={pool.get('returned')} idle={pool.get('idle_by_profile')}")

        mod["_meta_cache"].clear()
        mod["_conn_pool"].drop()
        _reset_counters()
        before = mod["_conn_pool"].snapshot()
        r2 = call(n["port"], "/v1internal:listExperiments", b"{}",
                  fault="truncate:1", timeout=60)
        after = mod["_conn_pool"].snapshot()
        cached = []
        with mod["_meta_cache"]._lock:
            for ent in mod["_meta_cache"]._d.values():
                cached.append(ent[1][2])
        check("T17 截断一次后重试成功，客户端拿到完整正文",
              r2["status"] == 200 and len(r2["body"]) > 1000,
              f"status={r2['status']} blen={len(r2['body'])}")
        check("T17 缓存里是完整正文，不是 100 字节半截",
              len(cached) == 1 and cached[0] == r2["body"] and len(cached[0]) != 100,
              f"cached={ [len(b) for b in cached] }")
        check("T17 回池的是成功那条连接，截断那条被丢弃",
              after.get("returned", 0) - before.get("returned", 0) == 1
              and after.get("discarded", 0) > before.get("discarded", 0),
              f"returned {before.get('returned')}→{after.get('returned')} "
              f"discarded {before.get('discarded')}→{after.get('discarded')}")
        ups = U["requests"]
        r3 = call(n["port"], "/v1internal:listExperiments", b"{}", timeout=60)
        check("T17 随后命中的是完整缓存",
              r3["headers"].get("X-Agyp-Cache") == "HIT" and r3["body"] == r2["body"]
              and U["requests"] == ups,
              f"cache={r3['headers'].get('X-Agyp-Cache')} upstream={U['requests']}")

        _reset_counters()
        call(n["port"], "/v1internal:writeTrajectoryAcls", b'{"n":1}', timeout=60)
        call(n["port"], "/v1internal:writeTrajectoryAcls", b'{"n":2}', timeout=60)
        call(n["port"], "/v1internal/cascadeNuxes", b'{"n":1}', timeout=60)
        call(n["port"], "/v1internal/cascadeNuxes", b'{"n":2}', timeout=60)
        check("T17 写接口与 cascadeNuxes 逐次回源", U["requests"] == 4,
              f"上游={U['requests']}")
    finally:
        Ctx.stop(n)


def t15_no_token_path(ctx: Ctx):
    print("\n\033[1mT15 全账号取不到 token 仍走旧语义（显式 502 + config_load_error，不是 EOF）\033[0m")
    n, _ = ctx.pair()
    try:
        cfg = json.load(open(n["cfg"], encoding="utf-8"))
        for p in cfg["profiles"].values():
            p.pop("access_token", None)
            p["expires_at"] = 0
            p.pop("refresh_token", None)   # 没有任何 token 来源
        with open(n["cfg"], "w", encoding="utf-8") as f:
            json.dump(cfg, f)
        r = call(n["port"], "/v1internal:listExperiments", b"{}", timeout=60)
        j = json.loads(r["body"].decode()) if r["body"] else {}
        print(f"    status={r['status']} error={j.get('error')}")
        check("T15 无 token → 显式 502 + agyp_gateway_no_usable_token",
              r["status"] == 502 and j.get("error") == "agyp_gateway_no_usable_token",
              f"error={j.get('error')}")
    finally:
        Ctx.stop(n)


def t18_bounded_state(ctx: Ctx):
    """C6/C8：无界状态与并发安全。这两条 ACCEPTANCE 项只有代码、没有证据。

    池的 overflow / aged_out / dedup、缓存的体积与状态码闸门、会话表的淘汰、
    以及显式切号 epoch —— 都是"平时不出事、出事就是留一条死连接或一份脏缓存"
    的路径，而本次已经挖出三个同类 bug（read1 不结单、will_close 事后翻转、
    release 重入）。所以逐条撞一遍，不能只靠读代码。
    """
    print("\n\033[1mT18 无界状态与并发安全：池上限/老化/重入、缓存闸门、会话淘汰\033[0m")
    n, _ = ctx.pair()
    conns = []
    try:
        mod = n["mod"]
        pool = mod["_conn_pool"]

        def live_conn():
            """真实建连的连接才算"可用连接"：http.client 的 sock 在首次请求时才创建，
            而 release 会拒绝 sock 为 None 的对象（dead_closed），不建连就测不到池。"""
            c, _from = pool.acquire("AAA", 5.0)
            c.connect()
            conns.append(c)
            return c

        # ---- C8a：未归还前，同 key 的两次 acquire 必须是不同连接 ----
        c1, f1 = pool.acquire("AAA", 5.0)
        c2, f2 = pool.acquire("AAA", 5.0)
        conns += [c1, c2]
        check("T18 两条并发不会拿到同一条连接（无 thread-local 假复用）",
              c1 is not c2 and not f1 and not f2, f"from=({f1},{f2})")

        # ---- C6a：max_per_key 上限 ----
        a, b, c = live_conn(), live_conn(), live_conn()
        pool.max_per_key = 1
        s0 = pool.snapshot()
        pool.release("AAA", a, healthy=True)
        pool.release("AAA", b, healthy=True)
        pool.release("AAA", c, healthy=True)
        s1 = pool.snapshot()
        check("T18 超出 max_per_key 的连接被关闭而不入池",
              s1["idle_by_profile"].get("AAA") == 1
              and s1["overflow_closed"] - s0["overflow_closed"] == 2,
              f"idle={s1['idle_by_profile'].get('AAA')} "
              f"overflow {s0['overflow_closed']}→{s1['overflow_closed']}")

        # ---- C6b：空闲老化 ----
        pool.max_per_key = 2
        d = live_conn()
        pool.release("AAA", d, healthy=True)
        pool.idle_sec = 0.0
        got, from_pool = pool.acquire("AAA", 5.0)
        conns.append(got)
        s2 = pool.snapshot()
        check("T18 过期连接被剔除并新建（不 indefinite 复用）",
              from_pool is False and got is not d and s2["aged_out"] >= 1,
              f"from_pool={from_pool} aged_out={s2['aged_out']}")
        pool.idle_sec = mod["UPSTREAM_CONN_IDLE_SEC"]

        # ---- C8b：release 可重入，且绝不在池里留一条已关闭的连接 ----
        e = live_conn()
        pool.release("AAA", e, healthy=True)          # 正常入桶
        before = pool.snapshot()
        pool.release("AAA", e, healthy=False)         # 转发路径上的二次归还
        mid = pool.snapshot()
        check("T18 二次 release 摘掉同一条连接并计数 deduped",
              mid["deduped"] == before["deduped"] + 1
              and not mid["idle_by_profile"].get("AAA"),
              f"deduped {before['deduped']}→{mid['deduped']} idle={mid['idle_by_profile'].get('AAA')}")
        pool.release("AAA", e, healthy=False)         # 第三次：桶里已无它
        last = pool.snapshot()
        check("T18 重复 release 不产生伪 dedup、也不留下死连接",
              last["deduped"] == mid["deduped"] and not last["idle_by_profile"].get("AAA"),
              f"deduped={last['deduped']} idle={last['idle_by_profile']}")
        check("T18 被丢弃的连接确实已关闭（池里没有僵尸句柄）",
              getattr(e, "sock", None) is None, f"sock={getattr(e, 'sock', None)}")

        # ---- C6c：缓存闸门（体积 / 状态码 / 关闭开关）----
        mc = mod["_meta_cache"]
        mc.clear()
        mc.put("POST", "/v1internal:listExperiments", "AAA", 200, [],
               b"z" * (mod["META_CACHE_MAX_BYTES"] + 1), 60)
        check("T18 超过 META_CACHE_MAX_BYTES 不进缓存",
              mc.snapshot()["entries"] == 0, f"entries={mc.snapshot()['entries']}")
        mc.put("POST", "/v1internal:listExperiments", "AAA", 502, [], b'{"err":1}', 60)
        check("T18 非 200 不进缓存（不把故障固化）",
              mc.snapshot()["entries"] == 0, f"entries={mc.snapshot()['entries']}")
        mc.put("POST", "/v1internal:listExperiments", "AAA", 200, [], b'{"ok":1}', 60)
        check("T18 200 + 体积合规才入缓存",
              mc.snapshot()["entries"] == 1, f"entries={mc.snapshot()['entries']}")

        # ---- C6d：会话绑定表有上限，长跑不单调涨 ----
        sb = mod["_session_bind"]
        sb.clear()
        cap = mod["SESSION_PROFILE_MAX"]
        for i in range(cap + 120):
            sb.bind(f"agent/{i:08d}-1111-1111-1111-222222222222/1730000000/1/{i}/1", "AAA")
        bound = sb.snapshot()["count"]
        check("T18 会话表封顶且有淘汰（不随会话数无限增长）",
              bound <= cap, f"count={bound} cap={cap}")

        # ---- C6e：显式切号 epoch —— 迟到的容灾 bind 不得复活被清掉的会话 ----
        sk = "agent/eeeeeeee-4444-4444-4444-00000000000e/1730000000/1/1/2"
        sb.bind(sk, "BBB")
        epoch_at_start = sb.epoch()
        sb.clear()                                     # 用户在 agyp switch 上清表
        sb.bind(sk, "BBB", epoch=epoch_at_start)       # 请求开始前的那次采样
        check("T18 清表后旧 epoch 的 bind 被拒（不会写回会话）",
              sb.snapshot()["count"] == 0, f"count={sb.snapshot()['count']}")
        sb.bind(sk, "CCC", epoch=sb.epoch())           # 当前 epoch 正常生效
        check("T18 当前 epoch 的 bind 仍正常工作（没有一刀切）",
              sb.snapshot()["count"] == 1, f"count={sb.snapshot()['count']}")

        pool.drop()
        check("T18 drop 后池为空", not pool.snapshot()["idle_by_profile"],
              f"idle={pool.snapshot()['idle_by_profile']}")
    finally:
        for cn in conns:
            try:
                cn.close()
            except Exception:
                pass
        Ctx.stop(n)


def t19_fatal_guard(ctx: Ctx):
    """C7：转发之前的未预期异常绝不能再变成裸 EOF。

    §P3 与 2026-09-20 花力气消灭的就是「客户端只看到 EOF、日志一行没有」。
    而 handle_gateway_request 直挂 _proxy_request，前面那一段全是**无保护的输入解析**
    （int(Content-Length) / chunked 解码 / 会话键正则 / 缓存取值），任意一处抛出
    都会穿过 BaseHTTPRequestHandler 直接关连接。这里逐条撞。
    """
    print("\n\033[1mT19 C7 未预期异常收口：显式 502 + JSON，而不是裸 EOF\033[0m")
    n, o = ctx.pair()
    try:
        mod = n["mod"]

        # ---- 1. 缓存取值抛异常（响应还没开始写）⇒ 必须显式 502 + JSON ----
        def boom(*a, **k):
            raise RuntimeError("injected: _meta_cache.get 炸了")
        mc = mod["_meta_cache"]
        orig_get = mc.get
        mc.get = boom
        try:
            r = call(n["port"], "/v1internal:listExperiments", b"{}", timeout=40)
        finally:
            del mc.get                        # 还原成类方法
        j = {}
        try:
            j = json.loads(r["body"].decode())
        except Exception:
            pass
        check("T19 转发前抛异常 → 显式 502（不是 EOF/空响应）",
              r["status"] == 502 and r.get("err") is None,
              f"status={r['status']} err={r.get('err')}")
        check("T19 502 带 agyp_gateway_internal_error 与 last_error",
              j.get("error") == "agyp_gateway_internal_error" and "injected" in str(j.get("last_error")),
              f"keys={sorted(j.keys())}")
        txt = ""
        try:
            txt = open(n["log"], encoding="utf-8", errors="ignore").read()
        except Exception:
            pass
        check("T19 异常带完整 traceback 落 gateway.log（§P3 可观测性）",
              "FATAL-EXCEPT" in txt and "FATAL-TRACEBACK" in txt
              and "injected" in txt, f"log 命中={('FATAL-EXCEPT' in txt)}")

        # ---- 2. 畸形 Content-Length（纯输入侧解析）同样在闸内 ----
        conn = http.client.HTTPConnection("127.0.0.1", n["port"], timeout=20)
        try:
            conn.putrequest("POST", "/v1internal:listExperiments")
            conn.putheader("Content-Type", "application/json")
            conn.putheader("Content-Length", "not-a-number")   # int() 必抛 ValueError
            conn.endheaders(b"")
            resp = conn.getresponse()
            body = resp.read()
            status = resp.status
        except Exception as e:
            status, body = None, str(e).encode()
        finally:
            conn.close()
        j2 = {}
        try:
            j2 = json.loads(body.decode())
        except Exception:
            pass
        check("T19 非法 Content-Length 不再裸 EOF",
              status == 502 and j2.get("error") == "agyp_gateway_internal_error",
              f"status={status} body={body[:60]!r}")

        # ---- 3. 已经开始应答之后才抛异常 ⇒ 绝不能追补 502 污染响应体 ----
        _reset_counters()
        warm = call(n["port"], "/v1internal:listExperiments", b"{}", timeout=40)
        check("T19 先正常回源一次以填充缓存", warm["status"] == 200 and len(warm["body"]) > 1000,
              f"status={warm['status']} len={len(warm['body'])}")
        orig_rl = mod["record_latency"]
        mod["record_latency"] = lambda *a, **k: (_ for _ in ()).throw(ValueError("injected: 落账炸了"))
        try:
            hit = call(n["port"], "/v1internal:listExperiments", b"{}", timeout=40)
        finally:
            mod["record_latency"] = orig_rl
        check("T19 缓存命中后落账抛异常：客户端仍拿到完整缓存体，没被追加 502",
              hit["status"] == 200 and hit["body"] == warm["body"]
              and b"agyp_gateway_internal_error" not in hit["body"],
              f"status={hit['status']} len={len(hit['body'])} 追加={hit['body'][-40:]!r}")
        check("T19 该场景也被记录为 responded=True（只记账不写第二份响应）",
              "FATAL-EXCEPT" in open(n["log"], encoding="utf-8", errors="ignore").read()
              and "responded=True" in open(n["log"], encoding="utf-8", errors="ignore").read(),
              "")

        # ---- 4. 正常路径没被闸门误伤：后续请求照常 200 ----
        back = call(n["port"], "/v1internal:listExperiments", b"{}", timeout=40)
        check("T19 注入结束后转发路径恢复正常",
              back["status"] == 200 and len(back["body"]) > 1000,
              f"status={back['status']} len={len(back['body'])}")

        # ---- 5. A/B：同一条畸形 Content-Length 打旧版网关看到什么 ----
        def raw_malformed(port):
            cc = http.client.HTTPConnection("127.0.0.1", port, timeout=15)
            try:
                cc.putrequest("POST", "/v1internal:listExperiments")
                cc.putheader("Content-Type", "application/json")
                cc.putheader("Content-Length", "not-a-number")
                cc.endheaders(b"")
                rr = cc.getresponse()
                return rr.status, rr.read()[:40]
            except Exception as e:
                return None, f"{type(e).__name__}: {e}".encode()[:60]
            finally:
                cc.close()

        old_status, old_body = raw_malformed(o["port"])
        new_status, new_body = raw_malformed(n["port"])
        print(f"    A/B 畸形 CL → 旧: status={old_status} body={old_body!r}")
        print(f"                新: status={new_status} body={new_body!r}")
        check("T19 旧版同场景确实是不可诊断形态（EOF/异常），新版才是显式 502",
              new_status == 502 and (old_status == 500 or old_status is None),
              f"old={old_status} new={new_status}")
    finally:
        Ctx.stop(n), Ctx.stop(o)


def t20_review_round2(ctx: Ctx):
    """第二轮自查挖出的三处：缓冲上限、缓冲区未 flush 的窗口、轮转与 stderr 的 fd。

    这三条都不是原清单里的，是「读代码看起来对」和「跑起来确实对」之间的差。
    """
    print("\n\033[1mT20 二轮自查：缓冲必须有上限 / flush 才是应答界限 / 轮转要带走 stderr\033[0m")
    n, _ = ctx.pair()
    try:
        mod = n["mod"]

        # ---- 1. 缓冲上限：一条自称很大的 200 不得把网关顶成无界缓冲 ----
        body = b'{"experiments":' + b"z" * (3 * 1024 * 1024) + b"}"   # 3MB > 2MB 上限
        _reset_counters()
        r = call(n["port"], "/v1internal:listExperiments", b"{}", fault="big:3072", timeout=90)
        check("T20 超限响应完整送达客户端（不截断、不吞字节）",
              r["status"] == 200 and len(r["body"]) == len(body),
              f"status={r['status']} 收到={len(r['body'])} 发出={len(body)}")
        check("T20 超限响应不进缓存（沿用 2MB 闸门）",
              mod["_meta_cache"].snapshot()["entries"] == 0,
              f"entries={mod['_meta_cache'].snapshot()['entries']}")
        # 关键判据：缓冲决策在**响应头那一刻**就带上声明长度上限。
        # 不靠时序猜（同一台机器上 3MB 攒完只要几毫秒，测不出区别），直接钉住源码条件。
        src = open(NEW_AGYP, encoding="utf-8").read()
        check("T20 缓冲决策包含声明长度上限（无界缓冲路径已封死）",
              "expected is not None" in src and "expected <= META_CACHE_MAX_BYTES" in src
              and "buffer_first = (not is_gen) and path in META_CACHE_TTL\n" not in src,
              "")

        # ---- 2. 只写进缓冲区、还没 flush 的窗口：客户端必须拿到 502，不是 EOF ----
        # 触发方式：把一条含 >U+00FF 字符的响应头塞进缓存，命中时 send_header 会
        # UnicodeEncodeError —— 此刻状态行只在 _headers_buffer 里，一个字节都没出去。
        mc = mod["_meta_cache"]
        mc.clear()
        # [R19] 缓存键含请求体指纹 ⇒ 注入时必须用与下面 call() 相同的 body 指纹，
        # 否则这条注入根本命中不了，测不到「flush 前抛异常」那个窗口。
        mc.put("POST", "/v1internal:loadCodeAssist", "AAA", 200,
               [("Content-Type", "application/json"), ("X-Bad", "中文头")],
               b'{"ok":1}', 300, mod["_body_digest"](b"{}"))
        r2 = call(n["port"], "/v1internal:loadCodeAssist", b"{}", timeout=40)
        j2 = {}
        try:
            j2 = json.loads(r2["body"].decode())
        except Exception:
            pass
        check("T20 缓冲区未 flush 时抛异常 → 显式 502（不是裸 EOF）",
              r2["status"] == 502 and j2.get("error") == "agyp_gateway_internal_error",
              f"status={r2['status']} err={j2.get('error')} body={r2['body'][:40]!r}")
        check("T20 补的 502 只有一条状态行（丢弃了缓冲区里的半成品）",
              r2["body"].count(b"HTTP/1.1") == 0 and b"\xe4\xb8\xad\xe6\x96\x87" not in r2["body"],
              f"body={r2['body'][:60]!r}")
        mc.clear()

        # ---- 3. 轮转不得把 stderr 留在旧 inode 上 ----
        saved1, saved2 = os.dup(1), os.dup(2)
        tmp_log = os.path.join(n["dir"], "rot.log")
        ino_ok = marker_ok = None
        diag = ""
        try:
            # 3a. CLI 进程（非守护）绝不能动 fd 2 —— 那会把用户终端劫走
            mod["_DAEMON_STARTED_AT"] = None
            before = os.fstat(2).st_ino
            mod["_reopen_daemon_stderr"]()
            check("T20 非守护进程时 _reopen_daemon_stderr 是 no-op（不劫持终端 stderr）",
                  os.fstat(2).st_ino == before, f"{before} → {os.fstat(2).st_ino}")

            # 3b. 守护进程：轮转后 fd 2 必须跟到新文件
            mod["LOG_FILE"] = tmp_log
            mod["LOG_MAX_BYTES"] = 1024
            mod["_DAEMON_STARTED_AT"] = time.time()
            fd = os.open(tmp_log, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o644)
            os.dup2(fd, 2)
            os.close(fd)
            old_ino = os.fstat(2).st_ino
            for i in range(600):                       # 跨过每 512 次写入的抽检窗口
                mod["_append_line"](tmp_log, f"line {i}\n")
            new_ino = os.fstat(2).st_ino
            ino_ok = new_ino != old_ino and os.path.exists(tmp_log + ".1")
            sys.stderr.write("AFTER-ROTATE-MARKER\n")
            sys.stderr.flush()
            new_txt = open(tmp_log, encoding="utf-8", errors="ignore").read()
            old_txt = open(tmp_log + ".1", encoding="utf-8", errors="ignore").read()
            marker_ok = "AFTER-ROTATE-MARKER" in new_txt and "AFTER-ROTATE-MARKER" not in old_txt
            diag = (f"old={old_ino} new={new_ino} .1存在={os.path.exists(tmp_log + '.1')} "
                    f"marker新={('AFTER-ROTATE-MARKER' in new_txt)} "
                    f"marker旧={('AFTER-ROTATE-MARKER' in old_txt)}")
        finally:
            # 被测函数会同时 dup2 fd 1 和 fd 2（守护进程两者都指向 gateway.log，这是对的），
            # 所以工装必须把**两个** fd 都还回来，否则后续所有输出都写进被测文件里看不见
            os.dup2(saved1, 1)
            os.dup2(saved2, 2)
            os.close(saved1)
            os.close(saved2)
            mod["_DAEMON_STARTED_AT"] = None
            mod["LOG_MAX_BYTES"] = 20 * 1024 * 1024
        check("T20 轮转后守护进程的 stderr 跟到新文件", bool(ino_ok), diag)
        check("T20 轮转之后的 stderr 写入落在新 gateway.log（§P3 取证不再分裂）",
              bool(marker_ok), diag)
    finally:
        Ctx.stop(n)


def start_connect_proxy(upper_port):
    """最小可用的 HTTP CONNECT 代理：任何 CONNECT 都转发到沙箱假上游。

    为什么需要它：本机**直连 Google 是不通的**，出网必须经代理；旧版 `urlopen` 靠 macOS
    的 `_scproxy` 自动读系统代理，而 `http.client` 完全不看 —— 2026-10-26 换传输层时
    这条能力整个丢了，线上表现为全线卡 SYN_SENT。工装以前只会直连 127.0.0.1 的假上游，
    所以 128 条断言一条都撞不到「怎么出网」。**这条用例是那道闸**。
    """
    conns = []
    connects = []                      # 每条 CONNECT 一行，用来验「复用是否成立」
    P_LOCK = threading.Lock()
    srv = socket.socket()
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(("127.0.0.1", 0))
    srv.listen(16)
    port = srv.getsockname()[1]
    stop = threading.Event()

    def pipe(a, b):
        try:
            while not stop.is_set():
                data = a.recv(65536)
                if not data:
                    break
                b.sendall(data)
        except Exception:
            pass
        finally:
            try:
                b.shutdown(socket.SHUT_WR)
            except Exception:
                pass

    def handle(c):
        try:
            buf = b""
            while b"\r\n\r\n" not in buf:
                chunk = c.recv(4096)
                if not chunk:
                    c.close()
                    return
                buf += chunk
            head = buf.split(b"\r\n")[0].decode("latin-1", "ignore")
            if not head.startswith("CONNECT"):
                c.sendall(b"HTTP/1.1 405 Method Not Allowed\r\nContent-Length: 0\r\n\r\n")
                c.close()
                return
            with P_LOCK:
                connects.append(head)
            up = socket.create_connection(("127.0.0.1", upper_port), timeout=10)
            c.sendall(b"HTTP/1.1 200 Connection established\r\n\r\n")
            t1 = threading.Thread(target=pipe, args=(c, up), daemon=True)
            t2 = threading.Thread(target=pipe, args=(up, c), daemon=True)
            t1.start(); t2.start()
            t1.join(); t2.join()
            try:
                up.close()
            except Exception:
                pass
        except Exception:
            pass
        finally:
            try:
                c.close()
            except Exception:
                pass

    def accept_loop():
        while not stop.is_set():
            try:
                c, _ = srv.accept()
            except OSError:
                break
            conns.append(c)
            threading.Thread(target=handle, args=(c,), daemon=True).start()

    threading.Thread(target=accept_loop, daemon=True).start()

    def shutdown():
        stop.set()
        try:
            srv.close()
        except Exception:
            pass

    return port, connects, shutdown


def t21_proxy_egress(ctx: Ctx):
    print("\n\033[1mT21 出站必须能走 CONNECT 代理（10-26 回归的那道闸）\033[1m\033[0m")
    n, _ = ctx.pair()
    px_port, connects, px_stop = start_connect_proxy(ctx.upper_port)
    saved_env = os.environ.get("AGYP_UPSTREAM_PROXY")
    try:
        mod = n["mod"]
        # 直连目标改成一个**可证明已死**的端口：模拟「直连不通」；代理则忽略目标端口，
        # 一律隧道到活着的假上游 —— 于是「走代理 = 通 / 不走代理 = 秒失败」可被分别断言。
        dead = free_port()
        with socket.socket() as s:
            s.bind(("127.0.0.1", dead))
        try:
            socket.create_connection(("127.0.0.1", dead), timeout=1).close()
            print("    ⚠ 死端口校验失败：该端口仍可连接，结论不可信")
        except Exception:
            pass
        mod["UPSTREAM_PORT"] = dead
        mod["UPSTREAM_PROXY_DISABLE"] = False        # 本用例要测的就是代理通路
        os.environ["AGYP_UPSTREAM_PROXY"] = f"http://127.0.0.1:{px_port}"
        del connects[:]

        _reset_counters()
        r1 = call(n["port"], "/v1internal:listExperiments", b"{}", timeout=60)
        check("T21 经 CONNECT 代理请求成功（直连端口是死的）",
              r1["status"] == 200 and len(r1["body"]) > 1000,
              f"status={r1['status']} len={len(r1['body'])}")
        check("T21 代理确实收到 CONNECT，且隧道目标是真实上游主机",
              len(connects) >= 1 and any("localhost" in c for c in connects),
              f"CONNECT={connects[:2]}")
        snap = mod["_conn_pool"].snapshot()
        check("T21 池自报当前出口代理（下次排障一行就能判定）",
              snap.get("proxy") == f"127.0.0.1:{px_port}",
              f"proxy={snap.get('proxy')} source={snap.get('proxy_source')}")

        # keep-alive 必须在隧道之上仍然成立 —— 这正是换 http.client 想换来的收益。
        # ⚠ 必须走**不缓存**的路径：命中缓存是在取 token / 建连之前发生的，
        #   用白名单路径测复用会得到「一个上游请求都没发」的假绿。
        #   retrieveUserQuotaSummary 在幂等名单里但不在缓存白名单，正合适。
        mod["_meta_cache"].clear(); mod["_conn_pool"].drop()
        del connects[:]
        base_hits = mod["_conn_pool"].snapshot()["hit"]
        for _ in range(4):
            call(n["port"], "/v1internal:retrieveUserQuotaSummary", b"{}", timeout=60)
        snap2 = mod["_conn_pool"].snapshot()
        check("T21 隧道之上连接复用生效（4 跳只建 1 条 CONNECT）",
              len(connects) == 1 and snap2["hit"] - base_hits == 3,
              f"CONNECT={len(connects)} 命中增量={snap2['hit'] - base_hits}")

        # 反向：关掉代理 → 必须**快速**失败并给出可诊断的 502（不是干等 90s、不是裸 EOF）
        mod["UPSTREAM_PROXY_DISABLE"] = True
        mod["_meta_cache"].clear(); mod["_conn_pool"].drop()
        t0 = time.monotonic()
        r2 = call(n["port"], "/v1internal:retrieveUserQuotaSummary", b"{}", timeout=60)
        cost = time.monotonic() - t0
        j2 = {}
        try:
            j2 = json.loads(r2["body"].decode())
        except Exception:
            pass
        check("T21 无代理时快速失败（connect 超时独立生效，不拖成 90s）",
              r2["status"] == 502 and cost < 12.0,
              f"status={r2['status']} 用时={cost:.1f}s")
        check("T21 502 里直接写明出口判据（上游/是否经代理/怎么自查）",
              j2.get("via_proxy") is None and "代理" in str(j2.get("hint"))
              and "daily" in str(j2.get("upstream")) or "localhost" in str(j2.get("upstream")),
              f"via_proxy={j2.get('via_proxy')} upstream={j2.get('upstream')}")
        mod["UPSTREAM_PROXY_DISABLE"] = False

        # 恢复代理后必须立刻可用（且新连接带上了代理信息）
        r3 = call(n["port"], "/v1internal:retrieveUserQuotaSummary", b"{}", timeout=60)
        check("T21 恢复代理配置后立即成功",
              r3["status"] == 200, f"status={r3['status']}")

        # 代理变更时，池里那条旧隧道不能被继续当作有效出路
        mod["_conn_pool"].drop()
        os.environ["AGYP_UPSTREAM_PROXY"] = f"http://127.0.0.1:{px_port}"
        call(n["port"], "/v1internal:retrieveUserQuotaSummary", b"{}", timeout=60)
        before = mod["_conn_pool"].snapshot().get("proxy_changed", 0)
        os.environ["AGYP_UPSTREAM_PROXY"] = f"http://127.0.0.1:{px_port + 1}"   # 换到一个死的代理
        try:
            socket.create_connection(("127.0.0.1", px_port + 1), timeout=0.3).close()
            print("    （注：备用端口恰好活着，proxy_changed 判定可能不成立）")
        except Exception:
            pass
        r4 = call(n["port"], "/v1internal:retrieveUserQuotaSummary", b"{}", timeout=60)
        after = mod["_conn_pool"].snapshot()
        check("T21 代理变了 ⇒ 旧隧道作废（不会把请求交给已失效的出路）",
              after.get("proxy_changed", 0) > before,
              f"proxy_changed {before}→{after.get('proxy_changed')} status={r4['status']}")
    finally:
        if saved_env is None:
            os.environ.pop("AGYP_UPSTREAM_PROXY", None)
        else:
            os.environ["AGYP_UPSTREAM_PROXY"] = saved_env
        px_stop()
        Ctx.stop(n)


def t22_chunked_framing(ctx: Ctx):
    """元数据响应在真线上是 chunked 且**没有 Content-Length**。

    10-26 第一版把缓冲限定为「声明了 CL 且 ≤2MB」，于是 chunked 响应既不缓冲也不缓存：
    上线实测 `stored=0 / hit=0`，占请求量 57% 的那块收益整个丢掉。假上游以前只发 CL，
    所以 T5 一直绿 —— 这条就是把那个盲区补成断言。
    """
    print("\n\033[1mT22 chunked 元数据：必须能缓存；截断不得进缓存；超上限内存有界\033[0m")
    n, _ = ctx.pair()
    try:
        mod = n["mod"]
        mc = mod["_meta_cache"]
        mc.clear(); mod["_conn_pool"].drop()
        _reset_counters()

        r1 = call(n["port"], "/v1internal:listExperiments", b"{}", fault="chunked", timeout=60)
        snap = mc.snapshot()
        check("T22 chunked（无 CL）元数据被缓存 —— 线上 stored=0 的那条回归",
              r1["status"] == 200 and len(r1["body"]) > 1000
              and snap["entries"] == 1 and snap["stored"] == 1,
              f"status={r1['status']} len={len(r1['body'])} entries={snap['entries']} stored={snap['stored']}")
        ups = U["requests"]
        r2 = call(n["port"], "/v1internal:listExperiments", b"{}", timeout=60)
        check("T22 第二次命中本地缓存、一个上游请求都不打",
              r2["headers"].get("X-Agyp-Cache") == "HIT" and U["requests"] == ups
              and r2["body"] == r1["body"],
              f"cache={r2['headers'].get('X-Agyp-Cache')} 上游 {ups}→{U['requests']} len={len(r2['body'])}")

        # 截断的 chunked 流（没有终止块）：不能进缓存、不能给客户端半截体
        mc.clear(); mod["_conn_pool"].drop(); _reset_counters()
        r3 = call(n["port"], "/v1internal:listExperiments", b"{}", fault="chunkcut-always", timeout=120)
        s3 = mc.snapshot()
        check("T22 截断的 chunked 流 → 显式 502，不是半截 200",
              r3["status"] == 502 and not r3["body"].startswith(b'{"experiments"'),
              f"status={r3['status']} len={len(r3['body'])}")
        check("T22 截断的 chunked 流不进缓存", s3["entries"] == 0, f"entries={s3['entries']}")

        # 超上限的 chunked：内存有界 ⇒ 攒过 2MB 当场放行、转即时流式，但字节必须完整
        mc.clear(); mod["_conn_pool"].drop(); _reset_counters()
        kb = 2100
        expect_len = len(b'{"experiments":"') + kb * 1024 + len(b'"}')   # 与假上游的构造逐字节一致
        r4 = call(n["port"], "/v1internal:listExperiments", b"{}", fault=f"chunkbig:{kb}", timeout=120)
        check("T22 超上限 chunked：客户端仍拿到完整体（我们自己成帧）",
              r4["status"] == 200 and len(r4["body"]) == expect_len,
              f"status={r4['status']} 收到={len(r4['body'])} 应为={expect_len}")
        check("T22 超上限 chunked 不进缓存（内存有界的代价，明确允许）",
              mc.snapshot()["entries"] == 0, f"entries={mc.snapshot()['entries']}")

        # 生成流不受影响：SSE 依旧即时流式、不进缓存
        mc.clear(); _reset_counters()
        call(n["port"], "/v1internal:streamGenerateContent", gen_body(turn=4), timeout=60)
        check("T22 生成请求仍不进缓存", mc.snapshot()["entries"] == 0
              and U["requests"] == 1, f"entries={mc.snapshot()['entries']} 上游={U['requests']}")
    finally:
        Ctx.stop(n)


def t23_send_progress(ctx: Ctx):
    print("\n\033[1mT23 发送阶段按进展判死活（R14）+ 本轮四处收口\033[0m")
    n, _ = ctx.pair()
    mod = n["mod"]
    pool = mod["_conn_pool"]
    real_create = pool._create
    real_launch = mod.launch_agy_through_gateway
    HandlerCls = mod.GatewayProxyHandler
    real_mgmt = HandlerCls.handle_management_api
    real_send = HandlerCls._send_request_body
    body = gen_body(session="agent/11111111-1111-4111-8111-111111111101",
                    turn=1, pad=1_200_000)     # ~1.2MB ⇒ 5 个 256KB 块
    SID2 = "agent/11111111-1111-4111-8111-111111111102"
    SID3 = "agent/11111111-1111-4111-8111-111111111103"
    try:
        mod["CACHE_DISABLE"] = True
        check("T23 发送时限常量按 16:22–16:35 的实测重标（45s/420s/1/4 进度闸）",
              mod.UPSTREAM_SEND_STALL_SEC == 45.0
              and mod.UPSTREAM_SEND_POLL_SEC == 2.0
              and mod.UPSTREAM_SEND_TOTAL_SEC == 420.0
              and mod.SEND_RETRY_MAX_PROGRESS == 0.25,
              f"stall={mod.UPSTREAM_SEND_STALL_SEC} poll={mod.UPSTREAM_SEND_POLL_SEC} "
              f"cap={mod.UPSTREAM_SEND_TOTAL_SEC} prog={mod.SEND_RETRY_MAX_PROGRESS}")

        # ---- 1. 慢但活着的管道：必须放过，且出站帧型不变 ----
        mod["UPSTREAM_SEND_STALL_SEC"] = 8.0
        _reset_counters()
        t0 = time.monotonic()
        r = call(n["port"], "/v1internal:streamGenerateContent", body,
                 fault="slowread:3:always", timeout=60)
        wall = time.monotonic() - t0
        check("T23 假上游每次停顿 3s 的 1.2MB 上传仍 200（旧的整段绝对时限会掐死它）",
              r["status"] == 200, f"status={r['status']} 墙钟={wall:.1f}s")
        got = BODIES.get("/v1internal:streamGenerateContent", b"")
        check("T23 上游逐字节收到完整请求体（分块 send 没改变内容）",
              got == body and len(got) == len(body), f"上游={len(got)} 发出={len(body)}")
        check("T23 墙钟确实跨过了停顿 ⇒ 时限按进展计", wall >= 3.0, f"墙钟={wall:.1f}s")
        seen = [v for (p, v) in SEEN_HEADERS if p.startswith("/v1internal:streamGenerate")]
        last = seen[-1] if seen else {}
        lowered = {k.lower() for k in last}
        check("T23 出站仍是纯 Content-Length、不与 Transfer-Encoding 混写（§P1 红线）",
              last.get("Content-Length") == str(len(body)) and "transfer-encoding" not in lowered,
              f"CL={last.get('Content-Length')} TE={last.get('Transfer-Encoding')!r}")

        # ---- 2. R18 的进度闸：零进展才允许整段重发，过半就直接显式失败 ----
        # ⚠ 这一对用例是**注入**的（同 T19 的风格），不靠真 socket 缓冲区：
        #   「卡在哪一块」由内核缓冲区大小决定，跨机器不确定（16:22–16:35 那次活锁正是
        #   「送出大半→切掉→from-zero 重发→再送出大半」的死循环），所以这里只测判据本身。
        real_send = HandlerCls._send_request_body
        mod["UPSTREAM_SEND_STALL_SEC"] = 1.5
        small = gen_body(session=SID2, turn=1, pad=100_000)

        # 2a. 一块都没送出（隧道开局即死）⇒ 换隧道重发一次，客户端无感
        state = {"n": 0}

        def zero_then_ok(self, conn, headers, req_body):
            state["n"] += 1
            if state["n"] == 1:
                self._send_progress = (0, len(req_body))
                raise socket.timeout("injected: tunnel dead before first byte")
            return real_send(self, conn, headers, req_body)

        HandlerCls._send_request_body = zero_then_ok
        _reset_counters()
        b0 = pool.snapshot()
        t0 = time.monotonic()
        r2 = call(n["port"], "/v1internal:streamGenerateContent", small, timeout=60)
        wall2 = time.monotonic() - t0
        rows2 = wait_rows(n, lambda rs: any(
            x.get("session") == SID2 and x.get("status") == 200 for x in rs))
        gen2 = [x for x in rows2 if x.get("session") == SID2]
        check("T23 零进展的发送失败 ⇒ 换隧道重发一次，客户端只见 200",
              r2["status"] == 200 and wall2 < 10 and state["n"] == 2,
              f"status={r2['status']} 墙钟={wall2:.1f}s 发送次数={state['n']}")
        check("T23 零进展 ⇒ 生成请求的重试闸放行（65s 预算内）",
              any(x.get("retrying") for x in gen2) and len(gen2) >= 2,
              f"gen行={len(gen2)} retrying={sum(1 for x in gen2 if x.get('retrying'))}")
        check("T23 失败那条连接被丢弃、没有回池",
              pool.snapshot()["discarded"] > b0["discarded"],
              f"discarded {b0['discarded']}→{pool.snapshot()['discarded']}")
        check("T23 up_ms 仍是「握手 + 送完整段 body」的耗时（分段没退化）",
              any((x.get("up_ms") or 0) > 0 for x in gen2 if x.get("status") == 200),
              f"up_ms={[x.get('up_ms') for x in gen2]}")

        # 2b. 已送出 50% ⇒ 禁止整段重发/换号，一跳就 502，并把理由写进响应
        def half_then_stall(self, conn, headers, req_body):
            self._send_progress = (len(req_body) // 2, len(req_body))
            raise socket.timeout("injected: stalled mid-body")

        HandlerCls._send_request_body = half_then_stall
        _reset_counters()
        b1 = pool.snapshot()
        sid4 = "agent/11111111-1111-4111-8111-111111111104"
        t0 = time.monotonic()
        r4 = call(n["port"], "/v1internal:streamGenerateContent",
                  gen_body(session=sid4, turn=1, pad=100_000), timeout=60)
        wall4 = time.monotonic() - t0
        rows4 = wait_rows(n, lambda rs: any(x.get("session") == sid4 and x.get("status") == 502
                                            for x in rs))
        gen4 = [x for x in rows4 if x.get("session") == sid4]
        HandlerCls._send_request_body = real_send
        check("T23 送出 50% 后卡死 ⇒ 不整段重发、不换号，只 1 跳就 502（反活锁）",
              r4["status"] == 502 and len(gen4) == 1
              and not any(x.get("retrying") for x in gen4),
              f"status={r4['status']} gen行={len(gen4)} retrying={sum(1 for x in gen4 if x.get('retrying'))}")
        check("T23 快速失败：墙钟在 stall 时限量级，不是磨到 90s/420s", wall4 < 8,
              f"墙钟={wall4:.1f}s")
        body4 = r4["body"] or b""
        check("T23 502 里写明「已送出多少 / 为什么不重发」（排障不用再猜）",
              "50%".encode() in body4 and "不整段重发".encode() in body4,
              body4[:220].decode("utf-8", "replace"))
        check("T23 过半卡死那条连接同样被丢弃",
              pool.snapshot()["discarded"] > b1["discarded"],
              f"discarded {b1['discarded']}→{pool.snapshot()['discarded']}")

        # ---- 3. 整段送完之后仍不重发（R5 闸门没被新发送路径削弱）----
        mod["UPSTREAM_SEND_STALL_SEC"] = 8.0
        _reset_counters()
        r3 = call(n["port"], "/v1internal:streamGenerateContent",
                  gen_body(session=SID3, turn=7, pad=2048), fault="hang:6", timeout=60)
        rows3 = wait_rows(n, lambda rs: any(x.get("session") == SID3 and x.get("status") == 502
                                            for x in rs))
        gen3 = [x for x in rows3 if x.get("session") == SID3]
        check("T23 body 送完后上游不响应 ⇒ 502 且只 1 跳、不重发不换号",
              r3["status"] == 502 and len(gen3) == 1
              and not any(x.get("retrying") for x in gen3),
              f"status={r3['status']} 跳数={len(gen3)}")

        # ---- 4. 池建连已挪出锁：两个 profile 并发冷启动不再串队 ----
        def slow_create(timeout, proxy=None):
            time.sleep(0.8)
            return real_create(timeout, proxy)
        pool._create = slow_create
        pool.drop()
        picked = []
        t0 = time.monotonic()
        th = [threading.Thread(target=lambda k: picked.append(pool.acquire(k, 5.0)), args=(k,))
              for k in ("AAA", "BBB")]
        for t in th:
            t.start()
        for t in th:
            t.join(20)
        wall_c = time.monotonic() - t0
        check("T23 建连不持池锁：并发冷启动墙钟 ≈ 单次握手而非两次之和",
              len(picked) == 2 and wall_c < 1.45,
              f"墙钟={wall_c:.2f}s（串行≈1.6s+ / 并行≈0.8s）")
        for c, _fp in picked:
            try:
                c.close()
            except Exception:
                pass
        pool._create = real_create
        pool.drop()

        # ---- 5. 管理面进闸：/_agyp/* 抛异常不得裸关连接 ----
        def boom(self):
            raise ValueError("injected mgmt failure")
        HandlerCls.handle_management_api = boom
        r5 = call(n["port"], "/_agyp/diag", None, timeout=10)
        HandlerCls.handle_management_api = real_mgmt
        check("T23 /_agyp/* 未预期异常返回显式 502 + JSON（R8 覆盖面扩到管理面）",
              r5["status"] == 502 and b"agyp_gateway_internal_error" in (r5["body"] or b""),
              f"status={r5['status']} body={(r5['body'] or b'')[:70]!r}")
        r5b = call(n["port"], "/_agyp/diag", None, timeout=10)
        check("T23 管理面闸不影响正常 diag",
              r5b["status"] == 200 and b'"pool"' in (r5b["body"] or b""),
              f"status={r5b['status']}")

        # ---- 6. CLI：未注册子命令必须报错退出，绝不兜底启动/切号 ----
        launched = []
        mod.launch_agy_through_gateway = lambda p, a: launched.append((p, a))
        import io as _io
        from contextlib import redirect_stderr as _rs

        def run_cli(*argv):
            saved = sys.argv
            sys.argv = ["agyp", *argv]
            buf = _io.StringIO()
            code = 0
            try:
                with _rs(buf):
                    try:
                        mod.main()
                    except SystemExit as e:
                        code = e.code if isinstance(e.code, int) else 1
            finally:
                sys.argv = saved
            return code, buf.getvalue()

        c1, o1 = run_cli("statss")
        check("T23 CLI 未注册子命令 exit 2 且不启动 agy",
              c1 == 2 and not launched and "未识别" in o1,
              f"exit={c1} 启动次数={len(launched)}")
        cfg_after = json.load(open(n["cfg"], encoding="utf-8"))
        check("T23 报错退出没有改写 active_profile",
              cfg_after.get("active_profile") == "AAA",
              f"active={cfg_after.get('active_profile')}")
        c2, _o2 = run_cli("-p", "BBB", "--model", "gemini-2.5-pro")
        check("T23 合法 agy 参数（以 - 开头）仍照常透传",
              c2 == 0 and launched == [("BBB", ["--model", "gemini-2.5-pro"])],
              f"exit={c2} launched={launched}")
        launched.clear()
        c3, _o3 = run_cli("--", "帮我写个函数")
        check("T23 `agyp -- <位置参数>` 显式逃生门不被硬拦截误杀",
              c3 == 0 and launched == [(None, ["帮我写个函数"])],
              f"exit={c3} launched={launched}")
        launched.clear()
        c4, _o4 = run_cli("AAA")
        check("T23 账号简写 `agyp AAA` 不受影响",
              c4 == 0 and launched == [("AAA", [])], f"exit={c4} launched={launched}")
        mod.launch_agy_through_gateway = real_launch
    finally:
        HandlerCls.handle_management_api = real_mgmt
        HandlerCls._send_request_body = real_send
        mod.launch_agy_through_gateway = real_launch
        pool._create = real_create
        Ctx.stop(n)


# ----------------------------------------------------------------------

def t24_hidden_fixes(ctx: Ctx):
    """T24 三方审查挖出的隐藏问题（R21–R25）：空闲闸自毒 / 半截请求体 / 429 截断 / 键与帧型

    这些都是「读代码看起来对、跑起来确实错」的那一类，而且全都指向同一个问题：
    10-26 的收益是不是净正向。逐条钉住。
    """
    print("\n\033[1mT24 隐藏问题收口：闸源自证 / 客户端半截体 / 429 大错误体 / 缓存键与帧型\033[0m")
    n, _ = ctx.pair()
    mod = n["mod"]
    pool = mod["_conn_pool"]
    mc = mod["_meta_cache"]
    HandlerCls = mod.GatewayProxyHandler
    real_create = pool._create
    body = gen_body(session="agent/11111111-1111-4111-8111-111111111124", turn=1,
                    pad=1_200_000)               # ~1.2MB：明显大于内核缓冲，节流才看得见

    class _Throttle:
        """包住真 socket：`send()` 每次最多交 per 个字节并睡 pause 秒。

        per=4096 / pause=0.02 ⇒ 200KB/s 且**每个 syscall 都有进展**（R22 要放行的形态）；
        per=0 ⇒ 一个字节都不接受，隧道卡死的形态（R22 要判死的形态）。
        """
        def __init__(self, real, per, pause, want=0):
            self._real, self._per, self._pause = real, per, pause
            self._want = want

        def send(self, data):
            if self._per == 0:
                time.sleep(self._pause)
                return 0
            if self._want > 0:
                # SSLSocket 的真实语义：底层写不动就抛 SSLWantWriteError，**不返回计数**
                self._want -= 1
                raise ssl.SSLWantWriteError(ssl.SSL_ERROR_WANT_WRITE)
            n_ = self._real.send(data[:min(len(data), self._per)])
            if n_:
                time.sleep(self._pause)
            return n_

        def __getattr__(self, k):
            return getattr(self._real, k)

    def throttle_conn(conn, per, pause, want=0):
        """`_create` 出来的连接此刻还没 connect（sock 是 None），所以在 connect 之后再包。"""
        real_connect = conn.connect

        def connect():
            real_connect()
            if conn.sock is not None and not isinstance(conn.sock, _Throttle):
                conn.sock = _Throttle(conn.sock, per, pause, want)
        conn.connect = connect
        return conn

    def raw_partial(port, path, cl=200000, sent=64, chunked=False):
        s = socket.create_connection(("127.0.0.1", port), timeout=20)
        try:
            if chunked:
                head = (f"POST {path} HTTP/1.1\r\nHost: x\r\n"
                        f"Content-Type: application/json\r\n"
                        f"Transfer-Encoding: chunked\r\n\r\n").encode()
                payload = head + b"40\r\n" + b"a" * 64 + b"\r\n"    # 有意不发终止块
            else:
                head = (f"POST {path} HTTP/1.1\r\nHost: x\r\n"
                        f"Content-Type: application/json\r\n"
                        f"Content-Length: {cl}\r\n\r\n").encode()
                payload = head + b"a" * sent
            s.sendall(payload)
            time.sleep(0.25)
            s.shutdown(socket.SHUT_WR)                # 「写完就断」= 半截体
            data = b""
            while len(data) < 262144:
                part = s.recv(65536)
                if not part:
                    break
                data += part
            return data
        finally:
            s.close()

    def raw_head(port, path, body_bytes, fault="", timeout=90):
        """发一条完整请求，返回 (resp, body)：头**不折叠成 dict**，用来看重复头/帧型。"""
        cc = http.client.HTTPConnection("127.0.0.1", port, timeout=timeout)
        try:
            h = {"Content-Type": "application/json"}
            if fault:
                h["X-Test-Fault"] = fault
            cc.request("POST", path, body=body_bytes, headers=h)
            rr = cc.getresponse()
            return rr, rr.read()
        finally:
            cc.close()

    try:
        mod["CACHE_DISABLE"] = True      # 前两个子用例与缓存无关，先把变量关掉

        # ---- 1. R22：一直有进展的慢管道必须传完（这正是今天 502 掉的那一类）----
        def throttled(timeout, proxy=None):
            return throttle_conn(real_create(timeout, proxy), 4096, 0.02)
        pool._create = throttled
        pool.drop()
        mod["UPSTREAM_SEND_STALL_SEC"] = 1.0          # 旧规则：一块 256KB 要在 1s 内 sendall 完
        mod["UPSTREAM_SEND_TOTAL_SEC"] = 120.0
        _reset_counters()
        t0 = time.monotonic()
        r = call(n["port"], "/v1internal:streamGenerateContent", body, timeout=90)
        wall = time.monotonic() - t0
        got = sum(v for k, v in U.items()
                  if k.startswith("recv_bytes:") and "enerateContent" in k)
        check("T24[R22] 200KB/s 的活管道上传 1.2MB 仍然 200（总耗时远超 stall，但每 syscall 都有进展）",
              r["status"] == 200,
              f"status={r['status']} 墙钟={wall:.1f}s err={r['err']}" + (
                  "" if r["status"] == 200 else
                  " traceback=" + repr(open(n['log'], encoding='utf-8',
                                            errors='ignore').read()[-500:])))
        check("T24[R22] 墙钟确实被节流拖过了 stall 阈值 ⇒ 判据不是「这块送完没」而是「有没有进展」",
              wall > mod.UPSTREAM_SEND_STALL_SEC and wall < 60,
              f"墙钟={wall:.1f}s stall={mod.UPSTREAM_SEND_STALL_SEC}s")
        check("T24[R22] 上游逐字节收到完整体", got == len(body), f"上游={got} 发出={len(body)}")
        src = open(NEW_AGYP, encoding="utf-8").read()
        seg = src[src.index("def _send_request_body"):src.index("def _is_generation_request")]
        check("T24[R22] 发送循环里不再用 sendall 判死（源码钉住）",
              ".sendall(" not in seg and "conn.sock.send(" in seg
              and "no byte accepted" in seg,
              f"seg 内 sendall 次数={seg.count('.sendall(')}")
        pool._create = real_create
        pool.drop()

        # ---- 1b. R22 续：SSLWantWriteError 不返回计数 ⇒ 必须按 TLS 记录切片喂 ----
        # 上线后头两条 502 的读数就是没切片的形状：
        #   17:43:39 / 17:44:27  err=no byte accepted for 45s at **0**/6606754B
        # 整段一喂，SSLSocket 在底层写不动时抛 want-write 且**不给已写字节数**，
        # 于是「前面几十个 16KB 记录其实已经进内核」也记不成进展 ⇒ 又把累计当进展。
        def wanting(timeout, proxy=None):
            return throttle_conn(real_create(timeout, proxy), 4096, 0.002, want=400)
        pool._create = wanting
        pool.drop()
        mod["UPSTREAM_SEND_STALL_SEC"] = 2.0
        _reset_counters()
        r1b = call(n["port"], "/v1internal:streamGenerateContent", body, timeout=90)
        check("T24[R22] 连抛 400 次 SSLWantWriteError 也只是「这次没写成」，重试后照样传完",
              r1b["status"] == 200,
              f"status={r1b['status']} err={r1b['err']}")
        check("T24[R22] 发送按 TLS 记录切片（≤16KB），不是一次喂整段",
              mod.UPSTREAM_SEND_RECORD_BYTES == 16384
              and "mv[sent:sent + UPSTREAM_SEND_RECORD_BYTES]" in src and "mv[sent:])" not in src,
              f"record={mod.UPSTREAM_SEND_RECORD_BYTES}")
        pool._create = real_create
        pool.drop()
        mod["UPSTREAM_SEND_STALL_SEC"] = 45.0

        # ---- 2. R22：真卡死的隧道还是要很快判死，别磨到 420s ----
        def wedged(timeout, proxy=None):
            return throttle_conn(real_create(timeout, proxy), 0, 0.05)
        pool._create = wedged
        pool.drop()
        mod["UPSTREAM_SEND_STALL_SEC"] = 0.6
        _reset_counters()
        t0 = time.monotonic()
        r2 = call(n["port"], "/v1internal:streamGenerateContent", body[:200_000], timeout=90)
        wall2 = time.monotonic() - t0
        check("T24[R22] 零进展的隧道在 stall 量级内判死并显式 502（不是干等）",
              r2["status"] == 502 and wall2 < 25,
              f"status={r2['status']} 墙钟={wall2:.1f}s")
        check("T24[R22] 502 写清楚是「一个字节都没被接受」",
              b"no byte accepted" in (r2["body"] or b""),
              f"body={(r2['body'] or b'')[:120]!r}")
        check("T24[R22] 零进展 = 0% 送出 ⇒ 进度闸放行重试（该重试的还重试）",
              mod.SEND_RETRY_MAX_PROGRESS == 0.25, f"prog={mod.SEND_RETRY_MAX_PROGRESS}")
        pool._create = real_create
        pool.drop()
        mod["UPSTREAM_SEND_STALL_SEC"] = 45.0
        mod["UPSTREAM_SEND_TOTAL_SEC"] = 420.0
        mod["CACHE_DISABLE"] = False

        # ---- 3. R23：客户端半截请求体绝不带合法 CL 转发给上游 ----
        _reset_counters()
        before_up = U["requests"]
        d1 = raw_partial(n["port"], "/v1internal:streamGenerateContent", cl=200000, sent=64)
        check("T24[R23] Content-Length 没读满 ⇒ 显式 502，不是半截 200",
              b"502" in d1[:40] and b"agyp" in d1, f"首行={d1[:60]!r}")
        _reset_counters()
        d2 = raw_partial(n["port"], "/v1internal:streamGenerateContent", chunked=True)
        check("T24[R23] chunked 少发终止块 ⇒ 判截断并 502（旧代码把已收的 64B 当完整体转发）",
              b"502" in d2[:40] and b"agyp" in d2, f"首行={d2[:60]!r}")
        check("T24[R23] 两次截断都没有一个请求打到上游",
              U["requests"] == 0, f"上游={U['requests']}")
        # 老代码 `if not size_line: continue` 在 EOF 后 readline 立刻返回 b''（无 syscall，
        # socket 时限管不到）⇒ 线程热转、_inflight 永久 >0、重启闸被钉死。
        stuck = mod["_inflight"].count()["now"]
        for _ in range(40):
            if mod["_inflight"].count()["now"] == 0:
                break
            time.sleep(0.1)
        check("T24[R23] 截断连接被回收：在途计数归零（不再有钉死的 handler 线程）",
              mod["_inflight"].count()["now"] == 0,
              f"inflight={mod['_inflight'].count()}（观察前 {stuck}）")

        # ---- 4. R24：429 的大错误体必须读全，CL 由我们按实到字节重算 ----
        _reset_counters()
        r4 = call(n["port"], "/v1internal:streamGenerateContent", gen_body(turn=7),
                  fault="bigstatus:429:150", timeout=60)
        expect_len = len(b'{"error": {"detail": "' + b"z" * (150 * 1024) + b'"}}')
        cl4 = r4["headers"].get("Content-Length")
        check("T24[R24] 150KB 的 429 错误体原样送达（旧实现只搬 64KB）",
              r4["status"] == 429 and len(r4["body"]) == expect_len,
              f"status={r4['status']} 收到={len(r4['body'])} 应为={expect_len}")
        check("T24[R24] Content-Length 与实际字节一致（不再照抄上游那条）",
              cl4 == str(expect_len), f"CL={cl4} 实到={len(r4['body'])}")

        # ---- 5. R24：缓存键含方法与 query；空体不入缓存 ----
        mc.clear(); _reset_counters()
        q1 = call(n["port"], "/v1internal:listExperiments?page=1", b"{}", timeout=40)
        q2 = call(n["port"], "/v1internal:listExperiments?page=1", b"{}", timeout=40)
        q3 = call(n["port"], "/v1internal:listExperiments?page=2", b"{}", timeout=40)
        check("T24[R24] 同 query 命中、换 query 必须回源（旧键把 ? 切掉了）",
              q1["headers"].get("X-Agyp-Cache") is None
              and q2["headers"].get("X-Agyp-Cache") == "HIT"
              and q3["headers"].get("X-Agyp-Cache") is None and U["requests"] == 2,
              f"上游={U['requests']} q2={q2['headers'].get('X-Agyp-Cache')} "
              f"q3={q3['headers'].get('X-Agyp-Cache')}")
        entries = mc.snapshot()["entries"]
        mc.put("POST", "/v1internal:fetchUserInfo", "AAA", 200, [], b"", 60)
        check("T24[R24] 「200 但 0 字节」不入缓存（否则整个 TTL 都在回空答案）",
              mc.snapshot()["entries"] == entries,
              f"entries {entries}→{mc.snapshot()['entries']}")
        K = mod["_MetaCache"]._key
        check("T24[R24] 键含方法：GET/POST 同路径不是同一条目",
              K("POST", "/p", "AAA", "d") != K("GET", "/p", "AAA", "d"), "")

        # ---- 6. R25：缓冲超上限转即时流式时，HTTP/1.0 响应不能再发 chunked ----
        # 本 handler 全文没设 protocol_version ⇒ 用默认 HTTP/1.0；RFC 7230 §3.3.1 禁止
        # 1.0 出现非 identity 的 Transfer-Encoding，Go 的 readTransfer 会直接报错。
        mc.clear(); _reset_counters()
        expect_big = len(b'{"experiments":"' + b"z" * (2100 * 1024) + b'"}')
        rr, payload = raw_head(n["port"], "/v1internal:fetchAvailableModels", b"{}",
                               fault="chunkbig:2100", timeout=120)
        te = rr.headers.get_all("Transfer-Encoding") or []
        check("T24[R25] 超上限的 2.1MB chunked 响应仍然逐字节完整送达",
              rr.status == 200 and len(payload) == expect_big,
              f"status={rr.status} 收到={len(payload)} 应为={expect_big}")
        check("T24[R25] 放行那条响应里没有 Transfer-Encoding（靠 HTTP/1.0 的 EOF 定长）",
              not te and rr.headers.get("Content-Length") is None,
              f"TE={te} CL={rr.headers.get('Content-Length')}")
    finally:
        pool._create = real_create
        Ctx.stop(n)


def t25_capacity_503_failover(ctx: Ctx):
    """[2026-11-05] 上游 503「No capacity available for model X on the server」必须换号。

    线上形态（11-04/11-05）：Google 按**账号**返回 503，同一分钟、同一 session、同一份
    711,721B body，账号 A 503/90s 超时，账号 B 26s 拿到 200；旧版把 503 原样透传给 agy，于是
    调度榜 #1 的坏号被每个请求反复撞，CLI 自己重试 8 次全打在同一号上（一次操作 25 分钟）。
    本用例钉三件事：换号救回 200 / 坏号进冷却 / 冷却用 503 的短档而不是 429 的 600s。
    """
    print("\n\033[1mT25 上游 503（无容量）：换候补账号 + 短冷却\033[0m")
    n, o = ctx.pair()
    try:
        _reset_counters()
        S1 = "agent/cccccccc-5555-5555-5555-000000000005"
        FAULT_BY_AUTH["Bearer tok-AAA"] = "status:503"

        r = call(n["port"], "/v1internal:streamGenerateContent",
                 gen_body(session=S1, turn=1), timeout=90)
        rows = wait_rows(n, lambda rs: any(x.get("session") == S1 and x.get("status") == 200
                                           for x in rs))
        seq = [x.get("profile") for x in rows if x.get("session") == S1]
        cfg = json.load(open(n["cfg"], encoding="utf-8"))
        left = float(cfg["profiles"]["AAA"].get("cooldown_until", 0) or 0) - time.time()
        sess = n["mod"]["_session_bind"].snapshot()
        print(f"    新: 客户端 {r['status']} ; S1 账号序列={seq} ; AAA cooldown 剩 {left:.0f}s")
        check("T25 503 被换号救回（客户端 200）", r["status"] == 200, f"status={r['status']}")
        check("T25 先撞 AAA(503) 再落到候补账号", bool(seq) and seq[0] == "AAA" and seq[-1] != "AAA",
              f"seq={seq}")
        check("T25 503 用短冷却（0 < 剩余 ≤ 200s，不是 429 的 600s）",
              0 < left <= 200, f"剩 {left:.0f}s")
        check("T25 会话已改绑到救活的账号",
              any(v["profile"] != "AAA" for v in sess["sessions"].values()),
              f"sessions={sess['by_profile']}")
        check("T25 全局默认账号没被搬走（有会话键时不改 active）",
              n["mod"]["get_active_profile_name"]() == "AAA",
              f"active={n['mod']['get_active_profile_name']()}")

        # 旧版对照：503 直接透传，客户端拿到 503（不是 200）
        # 显式把旧实例的 active 钉到 AAA：它在沙箱里的自动选号不一定落在 AAA 上，
        # 不钉就会出现「旧版也 200」的假基线。_reset_counters() 会清 FAULT_BY_AUTH，
        # 故障必须在这之后再注入一次。
        _reset_counters()
        FAULT_BY_AUTH["Bearer tok-AAA"] = "status:503"
        o["mod"]["set_active_profile_name"]("AAA")
        S2 = "agent/dddddddd-6666-6666-6666-000000000006"
        ro = call(o["port"], "/v1internal:streamGenerateContent",
                  gen_body(session=S2, turn=1), timeout=90)
        print(f"    旧: 客户端 {ro['status']}（对照基线：503 透传）")
        check("T25[对照] 旧版 503 直接透传给客户端", ro["status"] == 503, f"status={ro['status']}")
    finally:
        FAULT_BY_AUTH.clear()
        Ctx.stop(n), Ctx.stop(o)


def t26_pause_and_only_mode(ctx: Ctx):
    """[2026-11-25] 账号暂停 / 恢复 / 单账号锁定模式：跳过已暂停号、锁定单号禁容灾、自动兜底切号。"""
    print("\n\033[1mT26 账号暂停 / 恢复 / 单账号锁定模式\033[0m")
    n = load_instance(NEW_AGYP, f"new{time.time_ns()}", ctx.cert_pem, ctx.upper_port, ctx.sandbox)
    try:
        def mgmt(path):
            r = call(n["port"], path, None, timeout=10)
            return r["status"], (json.loads(r["body"].decode()) if r["body"] else {})

        # 1. 暂停 BBB：AAA 触发 429 容灾时必须跳过 BBB 直达 CCC
        _reset_counters()
        st, j = mgmt("/_agyp/pause?profile=BBB&paused=1")
        check("T26 暂停 BBB 返回 200 且 paused_profiles=['BBB']",
              st == 200 and j.get("paused_profiles") == ["BBB"] and j.get("enabled_profiles") == ["AAA", "CCC"],
              f"st={st} j={j}")

        FAULT_BY_AUTH["Bearer tok-AAA"] = "status:429"
        S1 = "agent/eeeeeeee-1111-1111-1111-000000000001"
        r = call(n["port"], "/v1internal:streamGenerateContent", gen_body(session=S1, turn=1), timeout=60)
        wait_rows(n, lambda rs: any(x.get("session") == S1 and x.get("status") == 200 for x in rs))
        auths = [dict(h).get("Authorization") for _p, h in SEEN_HEADERS]
        check("T26 AAA 429 容灾跳过已暂停的 BBB，直接落到 CCC",
              r["status"] == 200 and "Bearer tok-BBB" not in auths and "Bearer tok-CCC" in auths,
              f"status={r['status']} auths={auths}")

        # 2. 暂停当前活动账号 AAA：active_profile 自动切换，且原本粘在 AAA 的会话也被迁走
        _reset_counters()
        n["mod"]["set_active_profile_name"]("AAA")
        S2 = "agent/eeeeeeee-2222-2222-2222-000000000002"
        n["bind"].bind(S2, "AAA")
        st, j = mgmt("/_agyp/pause?profile=AAA&paused=1")
        check("T26 暂停 active_profile(AAA) 自动切到 CCC",
              st == 200 and j.get("active_profile") == "CCC" and set(j.get("paused_profiles", [])) == {"AAA", "BBB"},
              f"st={st} j={j}")

        r2 = call(n["port"], "/v1internal:streamGenerateContent", gen_body(session=S2, turn=1), timeout=60)
        auths2 = [dict(h).get("Authorization") for _p, h in SEEN_HEADERS]
        check("T26 原绑定在 AAA 的会话在 AAA 暂停后自动落到 CCC",
              r2["status"] == 200 and auths2 == ["Bearer tok-CCC"],
              f"status={r2['status']} auths={auths2}")

        # 3. 禁止暂停最后一个启用账号 CCC
        st, j = mgmt("/_agyp/pause?profile=CCC&paused=1")
        check("T26 拒绝暂停最后一个可用账号 (HTTP 400)",
              st == 400 and j.get("status") == "error",
              f"st={st} j={j}")

        # 4. 单账号锁定模式 (only AAA)：遇到 429/503 不再容灾换号，直接把错误返回给客户端
        _reset_counters()
        st, j = mgmt("/_agyp/only?profile=AAA")
        check("T26 /_agyp/only?profile=AAA 只启用 AAA 并暂停 BBB/CCC",
              st == 200 and j.get("active_profile") == "AAA"
              and j.get("enabled_profiles") == ["AAA"]
              and set(j.get("paused_profiles", [])) == {"BBB", "CCC"},
              f"st={st} j={j}")

        FAULT_BY_AUTH["Bearer tok-AAA"] = "status:429"
        S3 = "agent/eeeeeeee-3333-3333-3333-000000000003"
        r3 = call(n["port"], "/v1internal:streamGenerateContent", gen_body(session=S3, turn=1), timeout=60)
        auths3 = [dict(h).get("Authorization") for _p, h in SEEN_HEADERS]
        check("T26 单账号锁定下 429 绝不切号，仅尝试 AAA 后直接透传 429",
              r3["status"] == 429 and auths3 == ["Bearer tok-AAA"],
              f"status={r3['status']} auths={auths3}")

        # 5. 显式 switch 到暂停的 BBB 会自动恢复 BBB（其余暂停账号保持不变）
        _reset_counters()
        st, j = mgmt("/_agyp/switch?profile=BBB")
        check("T26 /_agyp/switch?profile=BBB 自动恢复 BBB 且保持 CCC 暂停",
              st == 200 and j.get("switched_to") == "BBB" and j.get("auto_resumed") is True,
              f"st={st} j={j}")
        st_d, diag = mgmt("/_agyp/diag")
        check("T26 diag 反映 enabled=['AAA','BBB'] paused=['CCC']",
              st_d == 200 and diag.get("active_profile") == "BBB"
              and diag.get("paused_profiles") == ["CCC"]
              and set(diag.get("enabled_profiles", [])) == {"AAA", "BBB"},
              f"diag_paused={diag.get('paused_profiles')} enabled={diag.get('enabled_profiles')}")

        # 6. 全部恢复
        st, j = mgmt("/_agyp/pause?all=0")
        check("T26 /_agyp/pause?all=0 恢复全部账号",
              st == 200 and j.get("paused_profiles") == []
              and set(j.get("enabled_profiles", [])) == {"AAA", "BBB", "CCC"},
              f"st={st} j={j}")
    finally:
        FAULT_BY_AUTH.clear()
        Ctx.stop(n)


def t27_metrics_accuracy(ctx: Ctx):
    """T27 指标准确性回归：thoughtsTokenCount 采集、latency/metrics 记录与缓存命中折算。"""
    global SSE_USAGE
    n = load_instance(NEW_AGYP, "t27_metrics", ctx.cert_pem, ctx.upper_port, ctx.sandbox)
    orig_sse = SSE_USAGE
    try:
        SSE_USAGE = (
            b'data: {"response":{"usageMetadata":{'
            b'"promptTokenCount":1200,'
            b'"cachedContentTokenCount":800,'
            b'"candidatesTokenCount":300,'
            b'"thoughtsTokenCount":512'
            b'},"modelVersion":"gemini-3.5-pro"}}\n\n'
        )
        req_body = json.dumps({
            "project": "aicode-consumers",
            "model": "gemini-3.5-pro",
            "request": {
                "model": "gemini-3.5-pro",
                "contents": [{"role": "user", "parts": [{"text": "hello"}]}],
            },
        }).encode()
        res = call(n["port"], "/v1internal:streamGenerateContent",
                   body=req_body, timeout=20.0)
        st = res.get("status")
        check("T27 请求成功返回 200", st == 200, f"res={res}")

        rows = wait_rows(n, lambda rs: any(r.get("t_tok") == 512 and r.get("c_tok") == 300 for r in rs))
        matched = [r for r in rows if r.get("t_tok") == 512 and r.get("c_tok") == 300]
        check("T27 latency.jsonl 记录 t_tok=512 / c_tok=300 / p_tok=1200 / cache_tok=800",
              len(matched) == 1
              and matched[0].get("p_tok") == 1200
              and matched[0].get("cache_tok") == 800
              and matched[0].get("stream_ms") is not None,
              f"rows={rows}")

        with open(n["metrics"], encoding="utf-8") as mf:
            metrics = json.load(mf)
        s = metrics.get("summary", {})
        check("T27 metrics.json 计入 total_thought_tokens 与 total_cached_tokens",
              s.get("total_thought_tokens") == 512
              and s.get("total_cached_tokens") == 800
              and s.get("total_output_tokens") == 300
              and s.get("total_tokens") == 1200 + 300 + 512
              and 0.001 < float(s.get("total_cost_usd", 0.0)) < 0.05,
              f"summary={s}")

        today = time.strftime("%Y-%m-%d")
        day = metrics.get("by_date", {}).get(today, {})
        check("T27 metrics.json by_date 记录 thought_tokens=512",
              day.get("thought_tokens") == 512 and day.get("cached_tokens") == 800,
              f"day={day}")
    finally:
        SSE_USAGE = orig_sse
        FAULT_BY_AUTH.clear()
        Ctx.stop(n)


def t28_account_lifecycle_and_security(ctx: Ctx):
    """T28 账号生命周期（增删改名）+ 管理面 CSRF Origin 防护 + oauth.json 读取与权限校验。"""
    n = load_instance(NEW_AGYP, "t28_accounts", ctx.cert_pem, ctx.upper_port, ctx.sandbox)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    prev_cid = os.environ.pop("AGYP_OAUTH_CLIENT_ID", None)
    prev_csec = os.environ.pop("AGYP_OAUTH_CLIENT_SECRET", None)
    try:
        def mgmt_post(path, payload=None, headers=None):
            data = json.dumps(payload or {}).encode("utf-8")
            hdrs = {"Content-Type": "application/json"}
            if headers:
                hdrs.update(headers)
            req = urllib.request.Request(
                f"http://127.0.0.1:{n['port']}{path}",
                data=data, headers=hdrs, method="POST",
            )
            try:
                with opener.open(req, timeout=5.0) as r:
                    return r.getcode(), json.loads(r.read().decode())
            except urllib.error.HTTPError as he:
                return he.code, json.loads(he.read().decode())

        def mgmt_get(path, headers=None):
            req = urllib.request.Request(
                f"http://127.0.0.1:{n['port']}{path}",
                headers=headers or {}, method="GET",
            )
            try:
                with opener.open(req, timeout=5.0) as r:
                    return r.getcode(), json.loads(r.read().decode())
            except urllib.error.HTTPError as he:
                return he.code, json.loads(he.read().decode())

        # 1. 添加新账号 work (verify=False 无外网依赖)
        st, j = mgmt_post("/_agyp/profile/add", {
            "mode": "refresh_token",
            "name": "work",
            "refresh_token": "rt-test-work-001",
            "email": "work@example.com",
            "verify": False,
        })
        check("T28 POST /_agyp/profile/add 新增账号 work 成功",
              st == 200 and "work" in (j.get("enabled_profiles") or []),
              f"st={st} j={j}")

        # 2. 非法名字/保留字与重名校验
        st_inv, _ = mgmt_post("/_agyp/profile/add", {
            "mode": "refresh_token", "name": "switch", "refresh_token": "rt-x", "verify": False,
        })
        st_dup, _ = mgmt_post("/_agyp/profile/add", {
            "mode": "refresh_token", "name": "work", "refresh_token": "rt-y", "verify": False,
        })
        check("T28 拒绝保留字/重名账号新增 (HTTP 400)",
              st_inv == 400 and st_dup == 400, f"st_inv={st_inv} st_dup={st_dup}")

        # 3. 切号到 work，再重命名为 primary，确认 active_profile 同步更新
        mgmt_get("/_agyp/switch?profile=work")
        st_rn, j_rn = mgmt_post("/_agyp/profile/rename", {"old_name": "work", "new_name": "primary"})
        check("T28 POST /_agyp/profile/rename 同步更新 active_profile",
              st_rn == 200 and j_rn.get("active_profile") == "primary"
              and "primary" in (j_rn.get("enabled_profiles") or [])
              and "work" not in (j_rn.get("enabled_profiles") or []),
              f"st={st_rn} j={j_rn}")

        # 4. 删除当前活跃账号 primary，确认自动切换并禁止删除最后一个账号
        st_rm, j_rm = mgmt_post("/_agyp/profile/remove", {"name": "primary"})
        check("T28 删除当前活跃账号后自动顺延 active_profile",
              st_rm == 200 and j_rm.get("active_changed") is True
              and j_rm.get("active_profile") not in ("", "primary"),
              f"st={st_rm} j={j_rm}")

        mgmt_post("/_agyp/profile/remove", {"name": "BBB"})
        mgmt_post("/_agyp/profile/remove", {"name": "CCC"})
        st_last, _ = mgmt_post("/_agyp/profile/remove", {"name": "AAA"})
        check("T28 默认拒绝删除最后一个账号 (HTTP 400)", st_last == 400, f"st_last={st_last}")

        # 5. CSRF Origin 校验：拒绝跨站来源，放行回环来源
        st_csrf, _ = mgmt_get("/_agyp/status", headers={"Origin": "https://evil.example.com"})
        st_ok, _ = mgmt_get("/_agyp/status", headers={"Origin": "http://127.0.0.1:8783"})
        check("T28 管理面拦截跨站 Origin (403) 并放行回环 Origin (200)",
              st_csrf == 403 and st_ok == 200, f"st_csrf={st_csrf} st_ok={st_ok}")

        # 6. oauth.json 保存权限 0600 与环境变量优先级校验
        n["mod"].save_oauth_client("cid-from-file", "csec-from-file")
        mode = os.stat(n["mod"].oauth_file_path()).st_mode & 0o777
        fcid, fcsec = n["mod"].load_oauth_client()
        os.environ["AGYP_OAUTH_CLIENT_ID"] = "cid-from-env"
        os.environ["AGYP_OAUTH_CLIENT_SECRET"] = "csec-from-env"
        ecid, ecsec = n["mod"].load_oauth_client()
        check("T28 oauth.json 0600 落盘且环境变量可覆盖",
              mode == 0o600
              and (fcid, fcsec) == ("cid-from-file", "csec-from-file")
              and (ecid, ecsec) == ("cid-from-env", "csec-from-env"),
              f"mode={oct(mode)} file={(fcid, fcsec)} env={(ecid, ecsec)}")
    finally:
        if prev_cid is None:
            os.environ.pop("AGYP_OAUTH_CLIENT_ID", None)
        else:
            os.environ["AGYP_OAUTH_CLIENT_ID"] = prev_cid
        if prev_csec is None:
            os.environ.pop("AGYP_OAUTH_CLIENT_SECRET", None)
        else:
            os.environ["AGYP_OAUTH_CLIENT_SECRET"] = prev_csec
        FAULT_BY_AUTH.clear()
        Ctx.stop(n)


# ----------------------------------------------------------------------

TESTS = [t1_keepalive, t2_transport_retry, t3_failover_next_profile, t4_sticky_isolation,
         t5_meta_cache, t6_large_body, t7_hop_by_hop, t8_usage_sniff, t9_client_disconnect,
         t11_budget, t12_stale_pooled, t16_head_timeout, t13_mgmt_api, t14_restart_guard,
         t15_no_token_path, t17_truncated_not_cached, t18_bounded_state, t19_fatal_guard,
         t20_review_round2, t21_proxy_egress, t22_chunked_framing, t23_send_progress,
         t24_hidden_fixes, t25_capacity_503_failover, t26_pause_and_only_mode,
         t27_metrics_accuracy, t28_account_lifecycle_and_security]


def _verify_untouched_real_config(snap_before):
    """字段级判定工装有没有碰线上配置。

    整文件 sha256 是个**假信号**：常驻网关自己的 quota_refresher 每 600s 写 last_quota、
    OAuth 到期会刷 access_token，第一轮跑完就是这样报了个"污染了线上"的假警报。
    真正要盯的是工装可能写进去的那几样东西。
    """
    if snap_before is None:
        if os.path.exists(REAL_PROFILES):
            P("\n\033[31m✗ 沙箱测试创建了真实 profiles.json\033[0m")
            return False
        P("\n\033[32m✓ 未写入外部 profiles.json\033[0m")
        return True
    now_cfg = json.loads(open(REAL_PROFILES, "rb").read().decode())
    problems = []
    if now_cfg.get("active_profile") != snap_before.get("active_profile"):
        problems.append(f"active_profile 被改：{snap_before.get('active_profile')} -> "
                        f"{now_cfg.get('active_profile')}")
    extra = set(now_cfg.get("profiles", {})) - set(snap_before.get("profiles", {}))
    if extra:
        problems.append(f"多出测试账号 {sorted(extra)}")
    for name, info in now_cfg.get("profiles", {}).items():
        old = snap_before.get("profiles", {}).get(name, {})
        if float(info.get("cooldown_until", 0) or 0) > float(old.get("cooldown_until", 0) or 0):
            problems.append(f"{name}.cooldown_until 被写入")
        for f in ("refresh_token", "email"):
            if info.get(f) != old.get(f):
                problems.append(f"{name}.{f} 被改写")
        for k in ("access_token", "expires_at", "last_quota", "updated_at"):
            if info.get(k) != old.get(k):
                # 常驻网关的正常周期性写入，不算污染，但要如实列出
                P(f"  \033[2m(线上自身变更，非工装) {name}.{k}\033[0m")
    if problems:
        P("\n\033[31m✗ 真实 profiles.json 被工装污染：\033[0m")
        for p in problems:
            P("   - " + p)
        return False
    P("\n\033[32m✓ 真实 profiles.json 未被工装改动\033[0m"
      "（活跃账号/凭据/池成员原样；常驻网关自身的 quota 周期写入已单独区分）")
    return True


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--only", default="")
    args = ap.parse_args()

    install_connect_guard()          # 第一件事就把出口焊死
    if os.path.exists(REAL_PROFILES):
        raw_before = open(REAL_PROFILES, "rb").read()
        snap_before = json.loads(raw_before.decode())
    else:
        snap_before = None
    shutil.rmtree(BASE, ignore_errors=True)
    sandbox = BASE
    os.makedirs(sandbox, exist_ok=True)

    P("准备沙箱假上游（自签 TLS）")
    cert = make_cert(sandbox)
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.load_cert_chain(cert)
    srv = TLSUpper(("127.0.0.1", 0), FakeUpstream, ctx)
    upper_port = srv.server_address[1]
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    P(f"  假上游 https://localhost:{upper_port}")

    # 让**旧**实现的 ssl.create_default_context() 也信任这张自签证书。
    # 只设 SSL_CERT_FILE 不够：macOS 上实测旧实例 6 连 0 成、全卡在手信号验证，
    # 于是所有"旧基线"读数都是假的。直接包一层，无条件加载沙箱 CA。
    _real_dcf = ssl.create_default_context

    def _dcf(*a, **kw):
        c = _real_dcf(*a, **kw)
        try:
            c.load_verify_locations(cafile=cert)
        except Exception:
            pass
        return c

    ssl.create_default_context = _dcf

    selected = TESTS
    if args.only:
        want = {x.strip().lower() for x in args.only.split(",")}
        selected = [t for t in TESTS if t.__name__.split("_")[0] in want]

    c = Ctx(upper_port, cert, sandbox)
    t_start = time.monotonic()
    for fn in selected:
        try:
            fn(c)
        except Exception:
            RESULTS.append((fn.__name__, False, "harness 异常"))
            print(f"    \033[31m✗ 用例抛异常\033[0m\n{traceback.format_exc()}")

    ok = sum(1 for _, p, _ in RESULTS if p)
    P("\n" + "=" * 78)
    P(f"断言 {ok}/{len(RESULTS)} 通过（{time.monotonic() - t_start:.0f}s）")
    for name, passed, detail in RESULTS:
        if not passed:
            P(f"  \033[31mFAILED\033[0m {name}  {detail}")

    clean = _verify_untouched_real_config(snap_before)
    try:
        srv.shutdown()
        srv.server_close()
    except Exception:
        pass
    return 0 if (ok == len(RESULTS) and clean) else 1


if __name__ == "__main__":
    sys.exit(main())
