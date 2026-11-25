"""Small, dependency-light sequence-diagram renderer built on matplotlib.

Usage
-----
    from seqlib import Sequence

    s = Sequence("Provision request", subtitle="optional")
    s.participant("dev", "Developer", kind="actor")
    s.participant("api", "Platform API")
    s.msg("dev", "api", "POST /requests")
    s.ret("api", "dev", "201 Created")
    s.self_msg("api", "validate inputs")
    s.note("api", "free text", span=("api", "dev"))
    s.frame("alt", "plan OK")   # opens a frame
    ...
    s.else_("plan failed")      # adds a separator inside alt
    ...
    s.end()                     # closes the innermost frame
    s.render("out.png")

Layout is computed in inches (1 data unit == 1 inch) so that font sizes,
wrapping and figure size stay consistent across diagrams.
"""

from __future__ import annotations

import textwrap
from dataclasses import dataclass, field

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt  # noqa: E402
from matplotlib.font_manager import FontProperties  # noqa: E402
from matplotlib.patches import FancyBboxPatch, Polygon, Rectangle  # noqa: E402
from matplotlib.textpath import TextPath  # noqa: E402

# --------------------------------------------------------------------------
# Palette (shared by every sequence diagram)
# --------------------------------------------------------------------------
FONT = "DejaVu Sans"
INK = "#1F2933"
NAVY = "#1F3A5F"
MUTED = "#5B6675"
KIND_FILL = {
    "actor": ("#FFF1DC", "#C9822B"),     # people
    "service": ("#E6EEF9", "#1F3A5F"),   # the platform components
    "github": ("#ECEAF7", "#4B3F8F"),    # GitHub / Actions
    "aws": ("#FFF6E5", "#B26B00"),       # AWS services / accounts
    "data": ("#E7F4EC", "#2E7D4F"),      # databases / stores
}
NOTE_FILL, NOTE_EDGE = "#FFFBE6", "#C9B458"
FRAME_EDGE, FRAME_TAG = "#7A869A", "#E3E8F0"
FAIL = "#B42318"
FONT_MSG = 8.6
FONT_HEAD = 9.6
FONT_TITLE = 13.5
LINE_H = 0.165  # inches per wrapped line at FONT_MSG


def text_width(s: str, size: float, bold: bool = False) -> float:
    """Rendered text width in inches."""
    fp = FontProperties(family=FONT, weight="bold" if bold else "normal")
    if not s:
        return 0.0
    return TextPath((0, 0), s, size=size, prop=fp).get_extents().width / 72.0


def wrap(s: str, max_w: float, size: float = FONT_MSG) -> list[str]:
    """Greedy word wrap using real glyph widths."""
    out: list[str] = []
    for para in s.split("\n"):
        words = para.split(" ")
        line = ""
        for w in words:
            cand = w if not line else f"{line} {w}"
            if text_width(cand, size) <= max_w or not line:
                line = cand
            else:
                out.append(line)
                line = w
        out.append(line)
    return out


@dataclass
class _P:
    key: str
    label: str
    kind: str
    x: float = 0.0


@dataclass
class _Ev:
    kind: str  # msg | ret | self | note | frame | else | end | gap
    src: str = ""
    dst: str = ""
    text: str = ""
    extra: dict = field(default_factory=dict)


class Sequence:
    def __init__(self, title: str, subtitle: str = "", min_gap: float = 1.9,
                 max_gap: float = 3.2, number: bool = True):
        self.title = title
        self.subtitle = subtitle
        self.parts: list[_P] = []
        self.events: list[_Ev] = []
        self.min_gap = min_gap
        self.max_gap = max_gap
        self.number = number
        self._open = 0

    # ---------------------------------------------------------------- API
    def participant(self, key: str, label: str, kind: str = "service"):
        self.parts.append(_P(key, label, kind))
        return self

    def msg(self, src, dst, text, fail=False):
        self.events.append(_Ev("msg", src, dst, text, {"fail": fail}))

    def ret(self, src, dst, text, fail=False):
        self.events.append(_Ev("ret", src, dst, text, {"fail": fail}))

    def self_msg(self, p, text, fail=False):
        self.events.append(_Ev("self", p, p, text, {"fail": fail}))

    def note(self, over, text, span=None):
        """Note over one participant, or across `span=(a, b)`."""
        a, b = span if span else (over, over)
        self.events.append(_Ev("note", a, b, text))

    def frame(self, kind, label):
        self._open += 1
        self.events.append(_Ev("frame", text=label, extra={"tag": kind}))

    def else_(self, label):
        self.events.append(_Ev("else", text=label))

    def end(self):
        self._open -= 1
        self.events.append(_Ev("end"))

    def gap(self, h=0.12):
        self.events.append(_Ev("gap", extra={"h": h}))

    # ------------------------------------------------------------- layout
    def _idx(self, key):
        for i, p in enumerate(self.parts):
            if p.key == key:
                return i
        raise KeyError(key)

    def _layout_columns(self):
        n = len(self.parts)
        gaps = [self.min_gap] * max(n - 1, 0)
        # header width demands
        for i in range(n - 1):
            need = (self._head_w(self.parts[i]) + self._head_w(self.parts[i + 1])) / 2 + 0.25
            gaps[i] = max(gaps[i], need)
        # message demands: try to fit label on <= 2 lines
        for e in self.events:
            if e.kind in ("msg", "ret"):
                i, j = sorted((self._idx(e.src), self._idx(e.dst)))
                span = j - i
                if span == 0:
                    continue
                w_needed = text_width(e.text, FONT_MSG) + 0.6
                per = min(w_needed / span, self.max_gap)
                for k in range(i, j):
                    gaps[k] = max(gaps[k], per)
            elif e.kind == "self":
                i = self._idx(e.src)
                if i < n - 1:
                    w_needed = min(text_width(e.text, FONT_MSG) / 2 + 0.6, self.max_gap)
                    gaps[i] = max(gaps[i], w_needed)
        x = self._head_w(self.parts[0]) / 2 + 0.35
        for i, p in enumerate(self.parts):
            p.x = x
            if i < n - 1:
                x += gaps[i]
        self.width = x + max(self._head_w(self.parts[-1]) / 2, 1.4) + 0.35

    def _head_w(self, p):
        lines = p.label.split("\n")
        return max(text_width(l, FONT_HEAD, bold=True) for l in lines) + 0.3

    # ------------------------------------------------------------- render
    def render(self, path, dpi=150):
        assert self._open == 0, "unclosed frame"
        self._layout_columns()
        head_h = max(len(p.label.split("\n")) for p in self.parts) * 0.17 + 0.22
        top = 0.35 + (0.28 if self.subtitle else 0) + 0.3
        y = top + head_h + 0.3

        draw = []          # deferred draw ops (need final height)
        frames_stack = []  # open frames: dict
        frames_done = []
        num = 0

        def touch(xa, xb):
            for f in frames_stack:
                f["xmin"] = min(f["xmin"], xa)
                f["xmax"] = max(f["xmax"], xb)

        for e in self.events:
            if e.kind in ("msg", "ret"):
                a, b = self.parts[self._idx(e.src)], self.parts[self._idx(e.dst)]
                avail = max(abs(b.x - a.x) - 0.45, 1.0)
                lines = wrap(e.text, avail)
                num += 1
                lab_h = len(lines) * LINE_H
                y += lab_h + 0.06
                draw.append(("arrow", a.x, b.x, y, lines, e.kind == "ret",
                             e.extra.get("fail"), num))
                touch(min(a.x, b.x), max(a.x, b.x))
                y += 0.2
            elif e.kind == "self":
                p = self.parts[self._idx(e.src)]
                i = self._idx(e.src)
                room = (self.parts[i + 1].x - p.x - 0.55) if i < len(self.parts) - 1 else 2.2
                lines = wrap(e.text, max(room, 1.0))
                num += 1
                h = max(len(lines) * LINE_H, 0.28)
                y += 0.06
                draw.append(("self", p.x, y, h, lines, e.extra.get("fail"), num))
                touch(p.x, p.x + 0.4 + max(text_width(l, FONT_MSG) for l in lines))
                y += h + 0.24
            elif e.kind == "note":
                ia, ib = sorted((self._idx(e.src), self._idx(e.dst)))
                xa, xb = self.parts[ia].x, self.parts[ib].x
                if ia == ib:
                    w = min(max(text_width(e.text, FONT_MSG) + 0.3, 1.4), 2.6)
                    xa, xb = xa - w / 2, xb + w / 2
                else:
                    xa, xb = xa - 0.35, xb + 0.35
                lines = wrap(e.text, xb - xa - 0.2)
                h = len(lines) * LINE_H + 0.16
                y += 0.06
                draw.append(("note", xa, xb, y, h, lines))
                touch(xa, xb)
                y += h + 0.2
            elif e.kind == "frame":
                y += 0.08
                f = {"tag": e.extra["tag"], "label": e.text, "y0": y,
                     "xmin": 1e9, "xmax": -1e9, "elses": [],
                     "depth": len(frames_stack)}
                frames_stack.append(f)
                y += 0.3
            elif e.kind == "else":
                f = frames_stack[-1]
                y += 0.06
                f["elses"].append((y, e.text))
                y += 0.3
            elif e.kind == "end":
                f = frames_stack.pop()
                y += 0.08
                f["y1"] = y
                if f["xmin"] > f["xmax"]:
                    f["xmin"], f["xmax"] = self.parts[0].x, self.parts[-1].x
                labels_w = max([text_width(f"[{f['label']}]", 8.6)
                                + text_width(f["tag"], 8.6, bold=True) + 0.55]
                               + [text_width(f"[{t}]", 8.6) + 0.3 for _, t in f["elses"]])
                f["xmax"] = max(f["xmax"], f["xmin"] - 0.28 + labels_w - 0.28 + 0.1)
                frames_done.append(f)
                # parent frames must cover child frame
                touch(f["xmin"] - 0.2, f["xmax"] + 0.2)
                y += 0.14
            elif e.kind == "gap":
                y += e.extra["h"]

        y += 0.15
        bottom_heads = y
        height = y + head_h + 0.35
        width = self.width

        fig = plt.figure(figsize=(width, height), dpi=dpi)
        ax = fig.add_axes([0, 0, 1, 1])
        ax.set_xlim(0, width)
        ax.set_ylim(height, 0)
        ax.axis("off")
        fig.patch.set_facecolor("white")

        # title
        ax.text(0.35, 0.38, self.title, fontsize=FONT_TITLE, fontweight="bold",
                color=NAVY, family=FONT, va="center", ha="left")
        if self.subtitle:
            ax.text(0.35, 0.66, self.subtitle, fontsize=9, color=MUTED,
                    family=FONT, va="center", ha="left", style="italic")

        head_y = top
        # lifelines
        for p in self.parts:
            ax.plot([p.x, p.x], [head_y + head_h, bottom_heads], color="#AAB4C3",
                    lw=1.0, ls=(0, (4, 3)), zorder=1)

        # frames (draw outer first)
        for f in sorted(frames_done, key=lambda f: f["depth"]):
            pad = 0.28
            x0, x1 = f["xmin"] - pad, f["xmax"] + pad
            if x0 > x1:  # empty frame
                x0, x1 = 0.4, width - 0.4
            x0 = max(x0, 0.12)
            x1 = min(x1, width - 0.12)
            ax.add_patch(Rectangle((x0, f["y0"]), x1 - x0, f["y1"] - f["y0"],
                                   fill=False, ec=FRAME_EDGE, lw=1.1, zorder=2))
            tw = text_width(f["tag"], 8.6, bold=True) + 0.22
            th = 0.24
            ax.add_patch(Polygon([(x0, f["y0"]), (x0 + tw + 0.08, f["y0"]),
                                  (x0 + tw + 0.08, f["y0"] + th - 0.07),
                                  (x0 + tw, f["y0"] + th), (x0, f["y0"] + th)],
                                 closed=True, fc=FRAME_TAG, ec=FRAME_EDGE, lw=1.1, zorder=3))
            ax.text(x0 + 0.1, f["y0"] + th / 2, f["tag"], fontsize=8.6, fontweight="bold",
                    color=NAVY, family=FONT, va="center", ha="left", zorder=4)
            ax.text(x0 + tw + 0.2, f["y0"] + th / 2, f"[{f['label']}]", fontsize=8.6,
                    color=INK, family=FONT, va="center", ha="left", style="italic", zorder=4,
                    bbox=dict(fc="white", ec="none", pad=0.5))
            for (ey, etext) in f["elses"]:
                ax.plot([x0, x1], [ey, ey], color=FRAME_EDGE, lw=1.0, ls=(0, (5, 3)), zorder=2)
                ax.text(x0 + 0.12, ey + 0.15, f"[{etext}]", fontsize=8.6, color=INK,
                        family=FONT, va="center", ha="left", style="italic", zorder=4,
                        bbox=dict(fc="white", ec="none", pad=0.5))

        # heads (top and bottom)
        for p in self.parts:
            fc, ec = KIND_FILL.get(p.kind, KIND_FILL["service"])
            w = self._head_w(p)
            for hy in (head_y, bottom_heads):
                ax.add_patch(FancyBboxPatch((p.x - w / 2, hy), w, head_h,
                                            boxstyle="round,pad=0,rounding_size=0.06",
                                            fc=fc, ec=ec, lw=1.3, zorder=5))
                ax.text(p.x, hy + head_h / 2, p.label, fontsize=FONT_HEAD,
                        fontweight="bold", color=INK, family=FONT, ha="center",
                        va="center", zorder=6, linespacing=1.15)

        # messages
        for d in draw:
            if d[0] == "arrow":
                _, xa, xb, ay, lines, is_ret, fail, n = d
                color = FAIL if fail else (MUTED if is_ret else INK)
                ax.annotate("", xy=(xb, ay), xytext=(xa, ay),
                            arrowprops=dict(arrowstyle="-|>" if not is_ret else "->",
                                            color=color, lw=1.25,
                                            ls=(0, (5, 3)) if is_ret else "-",
                                            shrinkA=0, shrinkB=0, mutation_scale=12),
                            zorder=7)
                mid = (xa + xb) / 2
                for k, line in enumerate(lines):
                    ly = ay - 0.07 - (len(lines) - 1 - k) * LINE_H
                    ax.text(mid, ly, line, fontsize=FONT_MSG, color=FAIL if fail else INK,
                            family=FONT, ha="center", va="bottom", zorder=8,
                            bbox=dict(fc="white", ec="none", pad=0.6, alpha=0.92))
                if self.number:
                    self._badge(ax, xa + (0.16 if xb > xa else -0.16), ay - 0.0, n, fail)
            elif d[0] == "self":
                _, x, sy, h, lines, fail, n = d
                color = FAIL if fail else INK
                ax.plot([x, x + 0.32, x + 0.32], [sy, sy, sy + h], color=color, lw=1.25, zorder=7)
                ax.annotate("", xy=(x, sy + h), xytext=(x + 0.32, sy + h),
                            arrowprops=dict(arrowstyle="-|>", color=color, lw=1.25,
                                            shrinkA=0, shrinkB=0, mutation_scale=12), zorder=7)
                for k, line in enumerate(lines):
                    ax.text(x + 0.42, sy + h / 2 + (k - (len(lines) - 1) / 2) * LINE_H, line,
                            fontsize=FONT_MSG, color=FAIL if fail else INK, family=FONT,
                            ha="left", va="center", zorder=8,
                            bbox=dict(fc="white", ec="none", pad=0.6, alpha=0.92))
                if self.number:
                    self._badge(ax, x - 0.16, sy, n, fail)
            elif d[0] == "note":
                _, xa, xb, ny, h, lines = d
                ax.add_patch(Polygon([(xa, ny), (xb - 0.12, ny), (xb, ny + 0.12),
                                      (xb, ny + h), (xa, ny + h)], closed=True,
                                     fc=NOTE_FILL, ec=NOTE_EDGE, lw=1.0, zorder=7))
                for k, line in enumerate(lines):
                    ax.text((xa + xb) / 2, ny + 0.08 + (k + 0.5) * LINE_H, line,
                            fontsize=FONT_MSG, color=INK, family=FONT, ha="center",
                            va="center", zorder=8)

        fig.savefig(path, dpi=dpi, facecolor="white")
        plt.close(fig)

    @staticmethod
    def _badge(ax, x, y, n, fail):
        ax.text(x, y, str(n), fontsize=6.8, color="white", family=FONT, fontweight="bold",
                ha="center", va="center", zorder=9,
                bbox=dict(boxstyle="circle,pad=0.22", fc=FAIL if fail else NAVY, ec="none"))
