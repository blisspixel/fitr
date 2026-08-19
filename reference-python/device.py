#!/usr/bin/env python3
"""Device fingerprint + device-specific gate profiles.

A model score is meaningless on its own. The SAME model on one laptop scored
"crashes on load" and "daily driver" depending only on a GPU driver update.
So every result carries a fingerprint of the machine and the Ollama config that
produced it, and results are only comparable within a matching fingerprint.

Gates live in profiles/*.json, NOT in this file. A profile is auto-selected by
matching your GPU or hostname; otherwise `default` is used and the run is
clearly marked as running on uncalibrated thresholds.

Cross-platform: Windows / Linux / macOS.
"""
from __future__ import annotations

import json
import os
import platform
import re
import shutil
import subprocess
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
PROFILE_DIR = os.path.join(HERE, "profiles")
OLLAMA = os.environ.get("OLLAMA_BASE_URL", "http://127.0.0.1:11434").rstrip("/")
GB = 1024 ** 3
IS_WIN = platform.system() == "Windows"
IS_MAC = platform.system() == "Darwin"


# --------------------------------------------------------------- shell helpers
def _sh(cmd, timeout=30) -> str:
    try:
        exe = cmd[0]
        if not os.path.isabs(exe) and shutil.which(exe) is None:
            return ""
        return subprocess.run(cmd, capture_output=True, text=True, timeout=timeout).stdout.strip()
    except Exception:
        return ""


def _ps(script: str) -> str:
    exe = os.path.join(os.environ.get("SystemRoot", r"C:\Windows"),
                       r"System32\WindowsPowerShell\v1.0\powershell.exe")
    if not os.path.exists(exe):
        return ""
    return _sh([exe, "-NoProfile", "-NonInteractive", "-Command", script], timeout=60)


# --------------------------------------------------------------- profiles
def _profiles() -> list:
    out = []
    if not os.path.isdir(PROFILE_DIR):
        return out
    for fn in sorted(os.listdir(PROFILE_DIR)):
        if fn.endswith(".json"):
            try:
                out.append(json.load(open(os.path.join(PROFILE_DIR, fn), encoding="utf-8")))
            except Exception:
                pass
    return out


def load_profile(name=None, fp=None) -> dict:
    """Explicit name wins; else match on gpu/host; else `default`."""
    profs = _profiles()
    by_name = {p.get("name"): p for p in profs}
    if name:
        if name not in by_name:
            raise SystemExit(f"profile '{name}' not found. available: {sorted(by_name)}")
        return by_name[name]

    fp = fp or {}
    gpu = (fp.get("gpu") or "").lower()
    host = (fp.get("host") or "").lower()
    for p in profs:
        m = p.get("match") or {}
        if not m:
            continue
        g = (m.get("gpu_contains") or "").lower()
        h = (m.get("host") or "").lower()
        if (g and g in gpu) or (h and h == host):
            return p
    return by_name.get("default") or {"name": "default", "gates": {}, "hints": {}}


# --------------------------------------------------------------- hardware
def gpu_info() -> dict:
    if IS_WIN:
        raw = _ps("Get-CimInstance Win32_VideoController | Select-Object -First 1 "
                  "Name,DriverVersion,@{n='DriverDate';e={$_.DriverDate.ToString('yyyy-MM-dd')}} "
                  "| ConvertTo-Json -Compress")
        try:
            d = json.loads(raw)
            return {"name": d.get("Name", ""), "driver": d.get("DriverVersion", ""),
                    "driver_date": str(d.get("DriverDate", ""))[:10]}
        except Exception:
            pass
    if IS_MAC:
        raw = _sh(["system_profiler", "SPDisplaysDataType"])
        m = re.search(r"Chipset Model:\s*(.+)", raw)
        return {"name": (m.group(1).strip() if m else platform.machine()),
                "driver": platform.mac_ver()[0], "driver_date": ""}
    nv = _sh(["nvidia-smi", "--query-gpu=name,driver_version", "--format=csv,noheader"])
    if nv:
        parts = [x.strip() for x in nv.splitlines()[0].split(",")]
        return {"name": parts[0], "driver": parts[1] if len(parts) > 1 else "",
                "driver_date": ""}
    ro = _sh(["rocm-smi", "--showproductname"])
    if ro:
        m = re.search(r"Card series:\s*(.+)", ro)
        return {"name": (m.group(1).strip() if m else "AMD ROCm GPU"),
                "driver": "", "driver_date": ""}
    lspci = _sh(["bash", "-lc", "lspci | grep -i -m1 'vga\\|3d\\|display'"])
    return {"name": lspci.split(":")[-1].strip() if lspci else "unknown",
            "driver": "", "driver_date": ""}


def total_ram_gb() -> float:
    try:
        if IS_WIN:
            return round(int(_ps("(Get-CimInstance Win32_ComputerSystem).TotalPhysicalMemory")) / GB, 1)
        if IS_MAC:
            return round(int(_sh(["sysctl", "-n", "hw.memsize"])) / GB, 1)
        with open("/proc/meminfo") as fh:
            kb = int(re.search(r"MemTotal:\s+(\d+)", fh.read()).group(1))
        return round(kb * 1024 / GB, 1)
    except Exception:
        return 0.0


def cpu_name() -> str:
    try:
        if IS_WIN:
            return _ps("(Get-CimInstance Win32_Processor | Select-Object -First 1).Name").strip()
        if IS_MAC:
            return _sh(["sysctl", "-n", "machdep.cpu.brand_string"])
        with open("/proc/cpuinfo") as fh:
            m = re.search(r"model name\s*:\s*(.+)", fh.read())
        return m.group(1).strip() if m else platform.processor()
    except Exception:
        return platform.processor()


# --------------------------------------------------------------- ollama
def ollama_version() -> str:
    return _sh(["ollama", "--version"]).replace("ollama version is ", "").strip()


def _server_log_path() -> str:
    if IS_WIN:
        return os.path.expandvars(r"%LOCALAPPDATA%\Ollama\server.log")
    return os.path.expanduser("~/.ollama/logs/server.log")


def _read_log() -> str:
    try:
        with open(_server_log_path(), "r", encoding="utf-8", errors="ignore") as fh:
            return fh.read(40_000_000)
    except Exception:
        return ""


CONFIG_KEYS = [
    "OLLAMA_MODELS", "OLLAMA_IGPU_ENABLE", "OLLAMA_FLASH_ATTENTION",
    "OLLAMA_KV_CACHE_TYPE", "OLLAMA_MAX_LOADED_MODELS", "OLLAMA_NUM_PARALLEL",
    "OLLAMA_CONTEXT_LENGTH", "LLAMA_ARG_FIT",
]


def ollama_config() -> dict:
    out = {k: os.environ.get(k, "") for k in CONFIG_KEYS}
    text = _read_log()
    for k in CONFIG_KEYS:
        hits = re.findall(rf"{k}:([^\s\]]*)", text)
        if hits:
            out[k] = hits[-1].replace("\\\\", "\\")
    return out


def inference_device(loaded_model=None) -> str:
    """What Ollama actually computes on.

    Primary signal is /api/ps for a loaded model (cross-platform, authoritative:
    size_vram > 0 means it is on the GPU). Falls back to the server log.
    """
    try:
        with urllib.request.urlopen(f"{OLLAMA}/api/ps", timeout=15) as r:
            for m in (json.loads(r.read()).get("models") or []):
                if loaded_model and (m.get("name") != loaded_model):
                    continue
                total, vram = m.get("size", 0), m.get("size_vram", 0)
                if total:
                    pct = round(100 * vram / total)
                    return f"GPU {pct}%" if vram else "CPU"
    except Exception:
        pass
    hits = re.findall(r'msg="inference compute".*', _read_log())
    if hits:
        lib = re.search(r"library=(\S+)", hits[-1])
        desc = re.search(r'description="([^"]*)"', hits[-1])
        return f"{lib.group(1) if lib else '?'} / {desc.group(1) if desc else '?'}"
    return "unknown"


def model_meta(model: str) -> dict:
    try:
        req = urllib.request.Request(
            f"{OLLAMA}/api/show", data=json.dumps({"model": model}).encode(),
            headers={"Content-Type": "application/json"})
        d = json.loads(urllib.request.urlopen(req, timeout=60).read())
        det = d.get("details", {}) or {}
        return {"parameter_size": det.get("parameter_size", ""),
                "quantization": det.get("quantization_level", ""),
                "family": det.get("family", ""),
                "capabilities": d.get("capabilities", []) or [],
                "context_train": (d.get("model_info", {}) or {}).get("general.context_length")}
    except Exception:
        return {"parameter_size": "", "quantization": "", "family": "",
                "capabilities": [], "context_train": None}


def ollama_reachable() -> bool:
    try:
        urllib.request.urlopen(f"{OLLAMA}/api/tags", timeout=8).read(1)
        return True
    except Exception:
        return False


def installed_models() -> list:
    try:
        with urllib.request.urlopen(f"{OLLAMA}/api/tags", timeout=15) as r:
            return [m.get("name") for m in (json.loads(r.read()).get("models") or [])]
    except Exception:
        return []


# --------------------------------------------------------------- fingerprint
def fingerprint() -> dict:
    g = gpu_info()
    return {
        "host": platform.node(),
        "os": f"{platform.system()} {platform.release()}",
        "cpu": cpu_name()[:60],
        "ram_gb": total_ram_gb(),
        "gpu": g["name"],
        "gpu_driver": g["driver"],
        "gpu_driver_date": g["driver_date"],
        "ollama": ollama_version(),
        "inference_device": inference_device(),
        "config": ollama_config(),
    }


def fingerprint_key(fp: dict) -> str:
    """Two results are comparable only if this matches."""
    c = fp.get("config", {})
    return "|".join([
        fp.get("host", ""), fp.get("gpu", ""), fp.get("gpu_driver", ""),
        fp.get("ollama", ""),
        c.get("OLLAMA_FLASH_ATTENTION", ""), c.get("OLLAMA_KV_CACHE_TYPE", ""),
    ])


def is_dense_and_big(meta: dict, profile: dict) -> bool:
    """Heuristic hint (never a gate): a dense model above the profile's limit
    will crawl on a bandwidth-bound device."""
    limit = (profile.get("hints") or {}).get("dense_param_b_interactive_max")
    if not limit:
        return False
    ps = (meta.get("parameter_size") or "").upper()
    try:
        params = float(re.sub(r"[^0-9.]", "", ps) or 0)
    except Exception:
        params = 0.0
    is_moe = "moe" in (meta.get("family", "") or "").lower()
    return (not is_moe) and params > float(limit)


if __name__ == "__main__":
    fp = fingerprint()
    prof = load_profile(None, fp)
    print(json.dumps(fp, indent=2))
    print("\nfingerprint_key:", fingerprint_key(fp))
    print("profile        :", prof.get("name"), "-", prof.get("description", ""))
