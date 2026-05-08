"""
Nanfang - Aero v2 Proxy Client GUI
Lively, animated interface with smooth transitions.
"""
import json
import math
import os
import subprocess
import sys
import threading
import time
import tkinter as tk
from tkinter import ttk, messagebox
from urllib.request import urlopen, Request

BASE_DIR = os.path.dirname(os.path.abspath(__file__))
NANFANG_EXE = os.path.join(BASE_DIR, "nanfang.exe")
NODES_FILE = os.path.join(BASE_DIR, "nodes.json")
SETTINGS_FILE = os.path.join(BASE_DIR, "settings.json")
PROXY_PORT = 7890  # nanfang mixed proxy port (HTTP CONNECT + SOCKS5)
PROXY_KEY = r"Software\Microsoft\Windows\CurrentVersion\Internet Settings"

# ── Colors ──────────────────────────────────────────────
BG_DARK = "#1a1a2e"
BG_CARD = "#16213e"
BG_INPUT = "#0f3460"
FG_TEXT = "#e0e0e0"
FG_DIM = "#7a7a9a"
FG_BRIGHT = "#ffffff"
ACCENT_GREEN = "#00e676"
ACCENT_BLUE = "#448aff"
ACCENT_RED = "#ff5252"
ACCENT_ORANGE = "#ffab40"
GLOW_GREEN = "#00c853"
GLOW_BLUE = "#2962ff"
GLOW_RED = "#d50000"
NODE_NORMAL = "#1e1e3a"
NODE_HOVER = "#2a2a50"
NODE_SELECT = "#0f3460"


def set_system_proxy(host, port):
    try:
        import winreg, ctypes
        key = winreg.OpenKey(winreg.HKEY_CURRENT_USER, PROXY_KEY, 0, winreg.KEY_SET_VALUE)
        winreg.SetValueEx(key, "ProxyEnable", 0, winreg.REG_DWORD, 1)
        winreg.SetValueEx(key, "ProxyServer", 0, winreg.REG_SZ, f"{host}:{port}")
        winreg.CloseKey(key)
        ctypes.windll.wininet.InternetSetOptionW(0, 39, 0, 0)
        ctypes.windll.wininet.InternetSetOptionW(0, 37, 0, 0)
    except Exception as e:
        print(f"set proxy error: {e}")


def clear_system_proxy():
    try:
        import winreg, ctypes
        key = winreg.OpenKey(winreg.HKEY_CURRENT_USER, PROXY_KEY, 0, winreg.KEY_SET_VALUE)
        winreg.SetValueEx(key, "ProxyEnable", 0, winreg.REG_DWORD, 0)
        winreg.CloseKey(key)
        ctypes.windll.wininet.InternetSetOptionW(0, 39, 0, 0)
        ctypes.windll.wininet.InternetSetOptionW(0, 37, 0, 0)
    except:
        pass


def lerp_color(c1, c2, t):
    """Linear interpolate between two hex colors, t in [0,1]."""
    r1, g1, b1 = int(c1[1:3], 16), int(c1[3:5], 16), int(c1[5:7], 16)
    r2, g2, b2 = int(c2[1:3], 16), int(c2[3:5], 16), int(c2[5:7], 16)
    r = int(r1 + (r2 - r1) * t)
    g = int(g1 + (g2 - g1) * t)
    b = int(b1 + (b2 - b1) * t)
    return f"#{r:02x}{g:02x}{b:02x}"


def darken(hex_color, amount=0.3):
    return lerp_color(hex_color, "#000000", amount)


def lighten(hex_color, amount=0.3):
    return lerp_color(hex_color, "#ffffff", amount)


# ═══════════════════════════════════════════════════════
#  Animated GlowDot — pulsing circle for status
# ═══════════════════════════════════════════════════════
class GlowDot(tk.Canvas):
    """Pulsing glow dot that indicates connection status."""

    def __init__(self, parent, color="#00e676", size=14, **kw):
        kw.pop("highlightthickness", None)
        super().__init__(parent, width=size * 3, height=size * 3,
                         highlightthickness=0, bg=BG_DARK, **kw)
        self.color = color
        self.base_color = color
        self.size = size
        self._phase = 0.0
        self._animating = False
        self._draw_glow(0.5)

    def set_color(self, color, animate=True):
        self.base_color = color
        if animate and not self._animating:
            self._start_pulse()
        elif not animate:
            self.color = color
            self._draw_glow(1.0)

    def _start_pulse(self):
        self._animating = True
        self._pulse_tick()

    def _pulse_tick(self):
        if not self._animating:
            return
        self._phase += 0.12
        if self._phase > math.pi * 2:
            self._phase -= math.pi * 2
        t = (math.sin(self._phase) + 1) / 2  # 0..1
        self.color = lerp_color(self.base_color, "#ffffff", t * 0.3)
        self._draw_glow(t)
        self.after(50, self._pulse_tick)

    def _draw_glow(self, brightness):
        self.delete("all")
        cx = self.size * 1.5
        cy = self.size * 1.5
        # Outer glow layers
        for i in range(4, 0, -1):
            r = self.size + i * 4
            alpha_color = lerp_color(self.color, BG_DARK, 0.7 - i * 0.12)
            self.create_oval(cx - r, cy - r, cx + r, cy + r,
                             fill=alpha_color, outline="", width=0)
        # Core
        r = self.size * 0.5
        self.create_oval(cx - r, cy - r, cx + r, cy + r,
                         fill=self.color, outline=lighten(self.color, 0.4), width=1)

    def stop(self):
        self._animating = False


# ═══════════════════════════════════════════════════════
#  Spinner — animated loading indicator
# ═══════════════════════════════════════════════════════
class Spinner(tk.Canvas):
    """Animated spinning loader."""

    def __init__(self, parent, color=ACCENT_BLUE, size=20, **kw):
        kw.pop("highlightthickness", None)
        super().__init__(parent, width=size * 2, height=size * 2,
                         highlightthickness=0, bg=BG_DARK, **kw)
        self.color = color
        self.size = size
        self._angle = 0
        self._running = False

    def start(self):
        self._running = True
        self._tick()

    def stop(self):
        self._running = False
        self.delete("all")

    def _tick(self):
        if not self._running:
            return
        self.delete("all")
        cx, cy = self.size, self.size
        r = self.size * 0.7
        # Draw arc
        self.create_arc(cx - r, cy - r, cx + r, cy + r,
                        start=self._angle, extent=120,
                        style="arc", outline=self.color, width=3)
        self.create_arc(cx - r, cy - r, cx + r, cy + r,
                        start=self._angle + 140, extent=60,
                        style="arc", outline=lighten(self.color, 0.3), width=2)
        self._angle = (self._angle - 12) % 360
        self.after(50, self._tick)


# ═══════════════════════════════════════════════════════
#  ActionButton — rich animated button
# ═══════════════════════════════════════════════════════
class ActionButton(tk.Canvas):
    """Button with smooth hover/press/glow animation."""

    def __init__(self, parent, text, color, command=None, **kw):
        self.w = kw.pop("width", 155)
        self.h = kw.pop("height", 52)
        kw.pop("highlightthickness", None)
        super().__init__(parent, width=self.w, height=self.h,
                         highlightthickness=0, bg=BG_DARK, **kw)
        self.color_base = color
        self.color_current = color
        self.color_target = color
        self.text = text
        self.command = command
        self._enabled = True
        self._is_active = False
        self._is_loading = False
        self._hover = False
        self._press = False
        self._glow_phase = 0.0
        self._glow_running = False

        self.bind("<Enter>", self._on_enter)
        self.bind("<Leave>", self._on_leave)
        self.bind("<ButtonPress-1>", self._on_press)
        self.bind("<ButtonRelease-1>", self._on_release)
        self._draw()

    def _on_enter(self, e):
        if self._enabled and not self._is_loading:
            self._hover = True
            self.color_target = lighten(self.color_base, 0.15)
            self._start_color_transition()

    def _on_leave(self, e):
        self._hover = False
        if not self._is_active:
            self.color_target = self.color_base
        else:
            self.color_target = lighten(self.color_base, 0.2)
        self._start_color_transition()

    def _on_press(self, e):
        if self._enabled and not self._is_loading:
            self._press = True
            self.color_target = darken(self.color_base, 0.15)
            self._start_color_transition()

    def _on_release(self, e):
        if self._enabled and self._press:
            self._press = False
            if self._hover:
                self.color_target = lighten(self.color_base, 0.15)
            elif self._is_active:
                self.color_target = lighten(self.color_base, 0.2)
            else:
                self.color_target = self.color_base
            self._start_color_transition()
            if self.command:
                self.command()

    def _start_color_transition(self):
        self.after(1, self._smooth_color_tick)

    def _smooth_color_tick(self):
        r1 = int(self.color_current[1:3], 16)
        g1 = int(self.color_current[3:5], 16)
        b1 = int(self.color_current[5:7], 16)
        r2 = int(self.color_target[1:3], 16)
        g2 = int(self.color_target[3:5], 16)
        b2 = int(self.color_target[5:7], 16)
        diff = abs(r1 - r2) + abs(g1 - g2) + abs(b1 - b2)
        if diff > 3:
            self.color_current = lerp_color(self.color_current, self.color_target, 0.35)
            self._draw()
            self.after(16, self._smooth_color_tick)
        else:
            self.color_current = self.color_target
            self._draw()

    def _draw(self):
        self.delete("all")
        # Shadow
        self.create_rectangle(2, 3, self.w + 2, self.h + 3,
                              fill=darken(self.color_current, 0.6), outline="", width=0)
        # Button body
        self.create_rectangle(0, 0, self.w, self.h,
                              fill=self.color_current, outline="", width=0)
        # Top highlight line
        self.create_line(0, 0, self.w, 0,
                         fill=lighten(self.color_current, 0.3), width=1)
        # Glow when active
        if self._is_active and not self._is_loading:
            glow_color = lerp_color(self.color_current, "#ffffff",
                                    0.15 + 0.1 * math.sin(self._glow_phase))
            self.create_rectangle(0, 0, self.w, self.h,
                                  fill="", outline=glow_color, width=2)
        # Text
        fg = FG_BRIGHT if self._enabled else "#666666"
        self.create_text(self.w // 2, self.h // 2,
                         text=self.text, fill=fg,
                         font=("Microsoft YaHei", 12, "bold"))

    def _start_glow(self):
        self._glow_running = True
        self._glow_tick()

    def _glow_tick(self):
        if not self._glow_running:
            return
        self._glow_phase += 0.15
        self._draw()
        self.after(60, self._glow_tick)

    def set_active(self, text=None):
        self._is_active = True
        self._enabled = True
        if text:
            self.text = text
        self.color_target = lighten(self.color_base, 0.2)
        self.color_current = self.color_target
        self._start_color_transition()
        self._start_glow()

    def set_normal(self, text=None):
        self._is_active = False
        self._enabled = True
        self._is_loading = False
        self._glow_running = False
        if text:
            self.text = text
        self.color_target = self.color_base
        self._start_color_transition()

    def set_loading(self, text="启动中..."):
        self._is_loading = True
        self._enabled = False
        self._is_active = False
        self._glow_running = False
        self.text = text
        self.color_target = darken(self.color_base, 0.1)
        self.color_current = self.color_target
        self._draw()

    def set_disabled(self, text=None):
        self._is_loading = False
        self._enabled = False
        self._is_active = False
        self._glow_running = False
        if text:
            self.text = text
        self.color_target = "#3a3a4a"
        self.color_current = "#3a3a4a"
        self._draw()

    def set_enabled(self):
        self._is_loading = False
        self._enabled = True
        self.color_target = self.color_base
        self._start_color_transition()


# ═══════════════════════════════════════════════════════
#  StatusTicker — animated scrolling status text
# ═══════════════════════════════════════════════════════
class StatusTicker(tk.Canvas):
    """Status bar with animated color changes and ticker."""

    def __init__(self, parent, **kw):
        kw.pop("highlightthickness", None)
        super().__init__(parent, height=36, highlightthickness=0,
                         bg=BG_CARD, **kw)
        self._text = "就绪"
        self._color = FG_DIM
        self._target_color = FG_DIM
        self._animating = False

    def set_status(self, text, color=FG_DIM, animate=True):
        self._text = text
        self._target_color = color
        if animate:
            self._animating = True
            self._anim_tick()
        else:
            self._color = color
            self._draw()

    def _anim_tick(self):
        if not self._animating:
            return
        r1 = int(self._color[1:3], 16)
        g1 = int(self._color[3:5], 16)
        b1 = int(self._color[5:7], 16)
        r2 = int(self._target_color[1:3], 16)
        g2 = int(self._target_color[3:5], 16)
        b2 = int(self._target_color[5:7], 16)
        self._color = f"#{int(r1+(r2-r1)*0.2):02x}{int(g1+(g2-g1)*0.2):02x}{int(b1+(b2-b1)*0.2):02x}"
        self._draw()
        if abs(int(self._color[1:3], 16) - r2) + abs(int(self._color[3:5], 16) - g2) + abs(int(self._color[5:7], 16) - b2) > 5:
            self.after(30, self._anim_tick)
        else:
            self._color = self._target_color
            self._draw()
            self._animating = False

    def _draw(self):
        self.delete("all")
        w = self.winfo_width()
        if w < 10:
            w = 500
        # Subtle top border
        self.create_line(0, 0, w, 0, fill=lighten(BG_CARD, 0.1), width=1)
        self.create_text(12, 18, anchor="w", text=self._text,
                         fill=self._color, font=("Microsoft YaHei", 10))


# ═══════════════════════════════════════════════════════
#  NodeList — custom styled node list
# ═══════════════════════════════════════════════════════
class NodeList(tk.Frame):
    """Node list using Listbox for reliable click handling, with custom styling."""

    def __init__(self, parent, nodes, on_select=None, **kw):
        super().__init__(parent, bg=BG_CARD, **kw)
        self.nodes = nodes
        self.on_select = on_select
        self.selected_idx = -1
        self.latencies = {}
        self._updating = False  # Lock to prevent callback during updates

        # Scrollbar
        scrollbar = tk.Scrollbar(self, troughcolor=BG_CARD, bg=FG_DIM,
                                  highlightthickness=0, bd=0)
        scrollbar.pack(side=tk.RIGHT, fill=tk.Y)

        self.listbox = tk.Listbox(
            self, bg=BG_CARD, fg=FG_TEXT, selectbackground=NODE_SELECT,
            selectforeground=FG_BRIGHT, highlightthickness=0, bd=0,
            font=("Microsoft YaHei", 10), activestyle="none",
            yscrollcommand=scrollbar.set, selectmode=tk.SINGLE,
        )
        self.listbox.pack(fill=tk.BOTH, expand=True)
        scrollbar.config(command=self.listbox.yview)

        self.listbox.bind("<<ListboxSelect>>", self._on_select)

    def set_nodes(self, nodes):
        self.nodes = nodes
        self.latencies = {}
        self.selected_idx = -1
        self.listbox.delete(0, tk.END)
        for node in nodes:
            name = node.get("name", "Node")
            self.listbox.insert(tk.END, f"  {name}")

    def set_selection(self, idx):
        if 0 <= idx < len(self.nodes):
            self._updating = True
            self.listbox.selection_clear(0, tk.END)
            self.listbox.selection_set(idx)
            self.listbox.see(idx)
            self.selected_idx = idx
            self._updating = False

    def set_latency(self, idx, value):
        self.latencies[idx] = value
        if 0 <= idx < len(self.nodes):
            self._updating = True
            name = self.nodes[idx].get("name", "Node")
            lat_val = self.latencies.get(idx)
            if lat_val is None:
                display = f"  {name}"
            elif isinstance(lat_val, int):
                display = f"  {name}  ─  {lat_val}ms"
            else:
                display = f"  {name}  {lat_val}"
            saved_sel = self.listbox.curselection()
            self.listbox.delete(idx)
            self.listbox.insert(idx, display)
            if saved_sel:
                self.listbox.selection_set(saved_sel)
            elif self.selected_idx >= 0:
                self.listbox.selection_set(self.selected_idx)
            self._updating = False

    def _on_select(self, event):
        if self._updating:
            return
        sel = self.listbox.curselection()
        if sel:
            idx = sel[0]
            self.selected_idx = idx
            if self.on_select:
                self.on_select(idx)


# ═══════════════════════════════════════════════════════
#  Main App
# ═══════════════════════════════════════════════════════
class NanfangApp:
    def __init__(self, root):
        self.root = root
        self.root.title("Nanfang")
        self.root.geometry("520x720")
        self.root.minsize(420, 560)
        self.root.configure(bg=BG_DARK)
        self.root.protocol("WM_DELETE_WINDOW", self._on_close)

        self.process = None
        self.nodes = []
        self.selected_idx = 0
        self.current_mode = None
        self.settings = self._load_settings()

        self._load_nodes_from_file()
        self._build_ui()

        # Start idle pulse on status dot (must be before _measure_latencies)
        self._status_pulse_running = True
        self._pulse_tick()

        # Restore saved node selection, or select first node
        if self.nodes:
            self.node_list.set_nodes(self.nodes)
            saved_id = self.settings.get("selected_node_id")
            idx = self._find_node_idx(saved_id) if saved_id is not None else 0
            self.selected_idx = idx
            self.node_list.set_selection(idx)
            node = self.nodes[idx]
            self.info_var.set(f"已选: {node.get('name', '?')} (ID:{node.get('node_id', '?')})")
            self._measure_latencies()

    def _build_ui(self):
        # ── Header ──
        header = tk.Frame(self.root, bg=BG_DARK, height=60)
        header.pack(fill=tk.X, padx=16, pady=(12, 0))
        header.pack_propagate(False)

        # App title
        tk.Label(header, text="NANFANG", fg=ACCENT_GREEN, bg=BG_DARK,
                 font=("Consolas", 20, "bold")).pack(side=tk.LEFT)

        # Status dot (animated)
        self.status_dot = GlowDot(header, color="#666666", size=6)
        self.status_dot.pack(side=tk.RIGHT, padx=(8, 0), pady=10)

        tk.Label(header, text="v1.0", fg=FG_DIM, bg=BG_DARK,
                 font=("Microsoft YaHei", 9)).pack(side=tk.RIGHT, padx=(0, 4))

        # ── Subscription ──
        sub_frame = tk.Frame(self.root, bg=BG_CARD, padx=12, pady=10)
        sub_frame.pack(fill=tk.X, padx=12, pady=(10, 6))

        tk.Label(sub_frame, text="订阅链接", fg=FG_DIM, bg=BG_CARD,
                 font=("Microsoft YaHei", 9), anchor="w").pack(fill=tk.X)

        saved_url = self.settings.get("subscription_url", "")
        self.url_var = tk.StringVar(value=saved_url)
        url_entry = tk.Entry(sub_frame, textvariable=self.url_var,
                             bg=BG_INPUT, fg=FG_TEXT, insertbackground=FG_TEXT,
                             font=("Consolas", 9), relief="flat", bd=0,
                             highlightthickness=1, highlightbackground=FG_DIM,
                             highlightcolor=ACCENT_BLUE)
        url_entry.pack(fill=tk.X, ipady=6, pady=(4, 6))

        btn_row = tk.Frame(sub_frame, bg=BG_CARD)
        btn_row.pack(fill=tk.X)

        self.btn_fetch = ActionButton(btn_row, "拉取节点", "#1565c0",
                                       command=self._fetch_nodes, width=110, height=36)
        self.btn_fetch.pack(side=tk.LEFT)

        self.btn_local = ActionButton(btn_row, "加载本地", "#5d4037",
                                       command=self._load_nodes, width=110, height=36)
        self.btn_local.pack(side=tk.LEFT, padx=8)

        self.spinner = Spinner(btn_row, color=ACCENT_BLUE, size=12)
        self.spinner.pack(side=tk.LEFT, padx=4)

        self.status_var = tk.StringVar(value="就绪")
        self.status_label = tk.Label(btn_row, textvariable=self.status_var,
                                     fg=FG_DIM, bg=BG_CARD, font=("Microsoft YaHei", 9))
        self.status_label.pack(side=tk.RIGHT)

        # ── Node List ──
        list_frame = tk.Frame(self.root, bg=BG_CARD, padx=4, pady=4)
        list_frame.pack(fill=tk.BOTH, expand=True, padx=12, pady=6)

        tk.Label(list_frame, text="  节点列表", fg=FG_DIM, bg=BG_CARD,
                 font=("Microsoft YaHei", 9), anchor="w").pack(fill=tk.X, pady=(0, 4))

        self.node_list = NodeList(list_frame, self.nodes,
                                   on_select=self._on_node_select)
        self.node_list.pack(fill=tk.BOTH, expand=True)

        # ── Bottom Panel ──
        bottom = tk.Frame(self.root, bg=BG_DARK, padx=16, pady=10)
        bottom.pack(fill=tk.X, side=tk.BOTTOM)

        # Info line
        self.info_var = tk.StringVar(
            value=f"已加载 {len(self.nodes)} 个节点" if self.nodes else "无节点")
        tk.Label(bottom, textvariable=self.info_var, fg=ACCENT_BLUE, bg=BG_DARK,
                 font=("Microsoft YaHei", 9), anchor="w").pack(fill=tk.X, pady=(0, 8))

        # Action buttons
        btn_row2 = tk.Frame(bottom, bg=BG_DARK)
        btn_row2.pack(fill=tk.X)

        self.btn_sys = ActionButton(btn_row2, "系统代理", ACCENT_GREEN,
                                     command=self._toggle_system_proxy,
                                     width=155, height=50)
        self.btn_sys.pack(side=tk.LEFT, padx=(0, 8))

        self.btn_tun = ActionButton(btn_row2, "TUN 模式", ACCENT_BLUE,
                                     command=self._toggle_tun,
                                     width=155, height=50)
        self.btn_tun.pack(side=tk.LEFT, padx=8)

        self.btn_stop = ActionButton(btn_row2, "停止", ACCENT_RED,
                                      command=self._stop,
                                      width=100, height=50)
        self.btn_stop.set_disabled()
        self.btn_stop.pack(side=tk.LEFT, padx=(8, 0))

        # Status ticker
        self.ticker = StatusTicker(bottom, highlightthickness=0)
        self.ticker.pack(fill=tk.X, pady=(10, 0))

    def _on_node_select(self, idx):
        self.selected_idx = idx
        if 0 <= idx < len(self.nodes):
            node = self.nodes[idx]
            name = node.get("name", "?")
            self.info_var.set(f"已选: {name} (ID:{node.get('node_id', '?')})")
            self._save_settings("selected_node_id", node.get("node_id"))

            # Auto-restart proxy with new node if currently running
            if self.current_mode and self.process:
                self._restart_proxy()

    # ── Settings persistence ──

    def _load_settings(self):
        if os.path.exists(SETTINGS_FILE):
            try:
                with open(SETTINGS_FILE, "r", encoding="utf-8") as f:
                    return json.load(f)
            except Exception:
                pass
        return {}

    def _save_settings(self, key, value):
        self.settings[key] = value
        try:
            with open(SETTINGS_FILE, "w", encoding="utf-8") as f:
                json.dump(self.settings, f, ensure_ascii=False, indent=2)
        except Exception:
            pass

    def _find_node_idx(self, node_id):
        for i, n in enumerate(self.nodes):
            if n.get("node_id") == node_id:
                return i
        return 0

    def _load_nodes_from_file(self):
        if os.path.exists(NODES_FILE):
            try:
                with open(NODES_FILE, "r", encoding="utf-8") as f:
                    self.nodes = json.load(f)
                self.nodes = [n for n in self.nodes
                              if n.get("type") == "aero_v2" or "password" in n]
            except Exception:
                self.nodes = []

    def _load_nodes(self):
        self._load_nodes_from_file()
        self.node_list.set_nodes(self.nodes)
        self.status_var.set("本地已加载")
        if self.nodes:
            saved_id = self.settings.get("selected_node_id")
            idx = self._find_node_idx(saved_id) if saved_id is not None else 0
            self.selected_idx = idx
            self.node_list.set_selection(idx)
            node = self.nodes[idx]
            self.info_var.set(f"已选: {node.get('name', '?')} (ID:{node.get('node_id', '?')})")
            self._measure_latencies()
        else:
            self.info_var.set("无节点")

    def _measure_latencies(self):
        """Show node ID as identifier since all nodes share one edge server."""
        for i, node in enumerate(self.nodes):
            nid = node.get("node_id", "?")
            self.node_list.set_latency(i, f"#{nid}")
        # Also start real latency test via active proxy
        self._test_proxy_latency()

    def _test_proxy_latency(self):
        """Test actual latency through the running proxy (if active)."""
        import socket as _sock

        def _do_test():
            while self._status_pulse_running:
                if self.process and self.process.poll() is None:
                    try:
                        t0 = time.time()
                        # SOCKS5 connect to httpbin.org:80
                        s = _sock.create_connection(("127.0.0.1", PROXY_PORT), timeout=3)
                        # SOCKS5 handshake: version=5, nmethods=1, method=0(no auth)
                        s.sendall(b"\x05\x01\x00")
                        s.settimeout(3)
                        s.recv(2)  # server reply
                        # SOCKS5 CONNECT: ver=5, cmd=1, rsv=0, atyp=3(domain)
                        host = b"www.gstatic.com"
                        s.sendall(b"\x05\x01\x00\x03" + bytes([len(host)]) + host + b"\x00\x50")
                        s.recv(10)  # reply
                        # Send HTTP request
                        s.sendall(b"HEAD /generate_204 HTTP/1.1\r\nHost: www.gstatic.com\r\n\r\n")
                        resp = s.recv(256)
                        s.close()
                        ms = int((time.time() - t0) * 1000)
                        if b"200" in resp or b"204" in resp:
                            self.root.after(0, lambda m=ms: self.ticker.set_status(
                                f"● 代理运行中 — 延迟 {m}ms", ACCENT_GREEN))
                    except Exception:
                        pass
                time.sleep(5)

        threading.Thread(target=_do_test, daemon=True).start()

    def _fetch_nodes(self):
        url = self.url_var.get().replace(" ", "").strip()
        if not url:
            messagebox.showerror("错误", "请输入订阅链接")
            return
        self.status_var.set("拉取中...")
        self.spinner.start()
        self.btn_fetch.set_loading("拉取中...")
        self.btn_local.set_disabled()

        def do_fetch():
            try:
                req = Request(url, headers={"User-Agent": "nanfang/1.0"})
                ctx = __import__("ssl")._create_unverified_context()
                with urlopen(req, timeout=15, context=ctx) as resp:
                    data = json.loads(resp.read())
                nodes = [n for n in data if n.get("type") == "aero_v2"]
                self.nodes = nodes
                with open(NODES_FILE, "w", encoding="utf-8") as f:
                    json.dump(nodes, f, ensure_ascii=False, indent=2)
                self.root.after(0, lambda: self._on_fetch_ok(nodes))
            except Exception as e:
                self.root.after(0, lambda: self._on_fetch_fail(str(e)))

        threading.Thread(target=do_fetch, daemon=True).start()

    def _on_fetch_ok(self, nodes):
        self.spinner.stop()
        self.node_list.set_nodes(nodes)
        self.btn_fetch.set_normal("拉取节点")
        self.btn_local.set_enabled()
        self.status_var.set(f"成功: {len(nodes)} 个节点")
        if nodes:
            # Save the URL that was used
            url = self.url_var.get().strip()
            if url:
                self._save_settings("subscription_url", url)
            # Restore saved selection or default to first
            saved_id = self.settings.get("selected_node_id")
            idx = self._find_node_idx(saved_id) if saved_id is not None else 0
            self.selected_idx = idx
            self.node_list.set_selection(idx)
            node = self.nodes[idx]
            self.info_var.set(f"已选: {node.get('name', '?')} (ID:{node.get('node_id', '?')})")
            self._measure_latencies()

    def _on_fetch_fail(self, err):
        self.spinner.stop()
        self.btn_fetch.set_normal("拉取节点")
        self.btn_local.set_enabled()
        self.status_var.set(f"失败: {err}")

    def _start_nanfang(self, mode):
        """Start nanfang SOCKS5 proxy. Must be called from main thread. Returns True/False."""
        if not self.nodes:
            messagebox.showerror("错误", "没有节点，请先拉取订阅")
            return False
        if not os.path.exists(NANFANG_EXE):
            messagebox.showerror("错误", f"找不到: {NANFANG_EXE}")
            return False

        self._stop_nanfang()

        idx = max(0, min(self.selected_idx, len(self.nodes) - 1))
        node = self.nodes[idx]
        node_id = node.get("node_id", 0)
        name = node.get("name", "?")

        with open(os.path.join(BASE_DIR, "debug.log"), "a", encoding="utf-8") as f:
            f.write(f"[{time.strftime('%H:%M:%S')}] _start_nanfang: selected_idx={idx} name={name} node_id={node_id}\n")

        try:
            cmd = [NANFANG_EXE, "serve", "--nodes-file", NODES_FILE,
                   "--node-id", str(node_id), "--listen", f"127.0.0.1:{PROXY_PORT}"]
            with open(os.path.join(BASE_DIR, "debug.log"), "a", encoding="utf-8") as f:
                f.write(f"[{time.strftime('%H:%M:%S')}] Starting: {' '.join(cmd)}\n")
            # Use STARTUPINFO to hide the console window without CREATE_NO_WINDOW
            si = subprocess.STARTUPINFO()
            si.dwFlags |= subprocess.STARTF_USESHOWWINDOW
            si.wShowWindow = 0  # SW_HIDE
            self.process = subprocess.Popen(
                cmd, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                startupinfo=si, cwd=BASE_DIR,
            )
            with open(os.path.join(BASE_DIR, "debug.log"), "a", encoding="utf-8") as f:
                f.write(f"[{time.strftime('%H:%M:%S')}] Process started, pid={self.process.pid}\n")
        except Exception as e:
            with open(os.path.join(BASE_DIR, "debug.log"), "a", encoding="utf-8") as f:
                f.write(f"[{time.strftime('%H:%M:%S')}] Start FAILED: {e}\n")
            messagebox.showerror("错误", str(e))
            return False

        return True

    def _check_nanfang_alive(self):
        """Check if nanfang is still running. Call after a short delay."""
        if self.process and self.process.poll() is not None:
            code = self.process.returncode
            with open(os.path.join(BASE_DIR, "debug.log"), "a", encoding="utf-8") as f:
                f.write(f"[{time.strftime('%H:%M:%S')}] Process DIED, exit code={code}\n")
            self.process = None
            self._stop()
            messagebox.showerror("错误", f"nanfang 启动失败 (exit code: {code})")
            return False
        return True

    def _stop_nanfang(self):
        if self.process:
            self.process.terminate()
            try:
                self.process.wait(timeout=3)
            except:
                self.process.kill()
            self.process = None
            # Give wintun driver time to clean up adapter state
            time.sleep(1)

    def _cleanup_tun_routes(self):
        """Remove TUN routes that were added by nanfang."""
        tun_gw = "10.0.0.1"
        try:
            subprocess.run(["route", "delete", "0.0.0.0", "mask", "128.0.0.0", tun_gw],
                           capture_output=True, timeout=5)
            subprocess.run(["route", "delete", "128.0.0.0", "mask", "128.0.0.0", tun_gw],
                           capture_output=True, timeout=5)
        except Exception:
            pass

    def _set_buttons_busy(self):
        """Disable all action buttons during startup."""
        self.btn_sys.set_disabled("系统代理")
        self.btn_tun.set_disabled("TUN 模式")
        self.btn_stop.set_disabled()
        self.spinner.start()

    def _set_buttons_idle(self):
        """Restore buttons to normal state."""
        self.spinner.stop()
        if self.current_mode is None:
            self.btn_sys.set_normal("系统代理")
            self.btn_tun.set_normal("TUN 模式")
            self.btn_stop.set_disabled()
        elif self.current_mode == "system_proxy":
            self.btn_sys.set_active("✓ 系统代理")
            self.btn_tun.set_disabled()
            self.btn_stop.set_enabled()
        elif self.current_mode == "tun":
            self.btn_sys.set_disabled()
            self.btn_tun.set_active("✓ TUN 模式")
            self.btn_stop.set_enabled()

    def _toggle_system_proxy(self):
        if self.current_mode == "system_proxy":
            self._stop()
            return

        node = self.nodes[self.selected_idx] if self.nodes else {}
        name = node.get("name", "?")

        # Debug: log which node is selected
        import traceback
        with open(os.path.join(BASE_DIR, "debug.log"), "a", encoding="utf-8") as f:
            f.write(f"[{time.strftime('%H:%M:%S')}] Toggle system_proxy: selected_idx={self.selected_idx} name={name} node_id={node.get('node_id')} nodes_total={len(self.nodes)}\n")

        self._set_buttons_busy()

        if not self._start_nanfang("system_proxy"):
            self._set_buttons_idle()
            return

        # Wait 1.5s then check if nanfang survived
        def _after_delay():
            if not self._check_nanfang_alive():
                self._set_buttons_idle()
                return
            # nanfang is alive, set proxy
            set_system_proxy("127.0.0.1", str(PROXY_PORT))
            self.current_mode = "system_proxy"
            self._set_buttons_idle()
            self.info_var.set(f"系统代理已开启 | {name} | 127.0.0.1:{PROXY_PORT}")
            self.status_dot.set_color(ACCENT_GREEN)
            self.ticker.set_status(f"● 系统代理运行中 — 127.0.0.1:{PROXY_PORT}", ACCENT_GREEN)

        self.root.after(1500, _after_delay)

    def _restart_proxy(self):
        """Restart proxy with currently selected node."""
        self._stop_nanfang()
        idx = max(0, min(self.selected_idx, len(self.nodes) - 1))
        node = self.nodes[idx]
        node_id = node.get("node_id", 0)
        name = node.get("name", "?")

        try:
            cmd = [NANFANG_EXE, "serve", "--nodes-file", NODES_FILE,
                   "--node-id", str(node_id), "--listen", f"127.0.0.1:{PROXY_PORT}"]
            si = subprocess.STARTUPINFO()
            si.dwFlags |= subprocess.STARTF_USESHOWWINDOW
            si.wShowWindow = 0
            self.process = subprocess.Popen(
                cmd, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                startupinfo=si, cwd=BASE_DIR,
            )
            self.info_var.set(f"已切换: {name} (ID:{node_id}) | 127.0.0.1:{PROXY_PORT}")
        except Exception as e:
            self.info_var.set(f"切换失败: {e}")

    def _toggle_tun(self):
        """Start TUN mode — captures all system traffic."""
        if self.current_mode == "tun":
            # Already in TUN mode, do nothing (use stop button)
            return

        if not self.nodes:
            messagebox.showerror("错误", "没有节点，请先拉取订阅")
            return
        if not os.path.exists(NANFANG_EXE):
            messagebox.showerror("错误", f"找不到: {NANFANG_EXE}")
            return

        idx = max(0, min(self.selected_idx, len(self.nodes) - 1))
        node = self.nodes[idx]
        name = node.get("name", "?")

        self._set_buttons_busy()
        self._stop_nanfang()

        try:
            cmd = [NANFANG_EXE, "tun", "--nodes-file", NODES_FILE]
            with open(os.path.join(BASE_DIR, "debug.log"), "a", encoding="utf-8") as f:
                f.write(f"[{time.strftime('%H:%M:%S')}] Starting TUN: {' '.join(cmd)}\n")
            si = subprocess.STARTUPINFO()
            si.dwFlags |= subprocess.STARTF_USESHOWWINDOW
            si.wShowWindow = 0
            tun_log = open(os.path.join(BASE_DIR, "tun.log"), "w", encoding="utf-8")
            self.process = subprocess.Popen(
                cmd, stdout=tun_log, stderr=subprocess.STDOUT,
                startupinfo=si, cwd=BASE_DIR,
            )
        except Exception as e:
            self._set_buttons_idle()
            messagebox.showerror("错误", str(e))
            return

        def _after_delay():
            if not self._check_nanfang_alive():
                self._set_buttons_idle()
                messagebox.showerror("错误", "TUN 模式启动失败，请检查 wintun.dll 是否存在")
                return
            self.current_mode = "tun"
            self._set_buttons_idle()
            self.info_var.set(f"TUN 模式已开启 | {name}")
            self.status_dot.set_color(ACCENT_GREEN)
            self.ticker.set_status(f"● TUN 模式运行中 — 捕获所有流量", ACCENT_GREEN)

        self.root.after(2000, _after_delay)

    def _stop(self):
        was_tun = self.current_mode == "tun"
        self._stop_nanfang()
        clear_system_proxy()
        if was_tun:
            self._cleanup_tun_routes()

        self.current_mode = None
        self.btn_sys.set_normal("系统代理")
        self.btn_tun.set_normal("TUN 模式")
        self.btn_stop.set_disabled()
        self.info_var.set("已停止")
        self.status_dot.set_color("#666666")
        self.ticker.set_status("已停止", FG_DIM)

    def _pulse_tick(self):
        if not self._status_pulse_running:
            return
        # If not connected, dim pulse
        if self.current_mode is None:
            self.status_dot.base_color = "#555555"
        self.root.after(100, self._pulse_tick)

    def _on_close(self):
        self._status_pulse_running = False
        self.status_dot.stop()
        self._stop()
        self.root.destroy()


if __name__ == "__main__":
    root = tk.Tk()
    NanfangApp(root)
    root.mainloop()
