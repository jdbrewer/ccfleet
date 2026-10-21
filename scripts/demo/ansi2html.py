#!/usr/bin/env python3
"""Render `tmux capture-pane -e` output (stdin) as a terminal-window HTML page (stdout).

usage: ansi2html.py COLS ROWS
"""
import html
import re
import sys

FG, BG = "#d4d4d8", "#18181b"  # default terminal colours (dark theme)
BASIC = ["#27272a", "#f87171", "#4ade80", "#facc15", "#60a5fa", "#c084fc", "#22d3ee", "#e4e4e7",
         "#52525b", "#fca5a5", "#86efac", "#fde047", "#93c5fd", "#d8b4fe", "#67e8f9", "#fafafa"]
CHAR_W, LINE_H, PAD = 7.83, 17, 18


def xterm256(n):
    if n < 16:
        return BASIC[n]
    if n < 232:
        n -= 16
        v = [0, 95, 135, 175, 215, 255]
        return "#%02x%02x%02x" % (v[n // 36], v[n // 6 % 6], v[n % 6])
    g = 8 + (n - 232) * 10
    return "#%02x%02x%02x" % (g, g, g)


def apply(st, params):
    p = [int(x) if x else 0 for x in params.split(";")] if params else [0]
    i = 0
    while i < len(p):
        c = p[i]
        if c == 0:
            st.clear()
        elif c == 1:
            st["b"] = True
        elif c == 2:
            st["dim"] = True
        elif c == 3:
            st["i"] = True
        elif c == 4:
            st["u"] = True
        elif c == 7:
            st["rev"] = True
        elif c == 22:
            st.pop("b", None); st.pop("dim", None)
        elif c == 23:
            st.pop("i", None)
        elif c == 24:
            st.pop("u", None)
        elif c == 27:
            st.pop("rev", None)
        elif 30 <= c <= 37 or 90 <= c <= 97:
            st["fg"] = BASIC[c - 30 if c < 90 else c - 82]
        elif 40 <= c <= 47 or 100 <= c <= 107:
            st["bg"] = BASIC[c - 40 if c < 100 else c - 92]
        elif c == 39:
            st.pop("fg", None)
        elif c == 49:
            st.pop("bg", None)
        elif c in (38, 48) and i + 1 < len(p):
            key = "fg" if c == 38 else "bg"
            if p[i + 1] == 5 and i + 2 < len(p):
                st[key] = xterm256(p[i + 2]); i += 2
            elif p[i + 1] == 2 and i + 4 < len(p):
                st[key] = "#%02x%02x%02x" % tuple(p[i + 2:i + 5]); i += 4
        i += 1


def css(st):
    fg, bg = st.get("fg", FG), st.get("bg")
    if st.get("rev"):
        fg, bg = bg or BG, fg
    out = [f"color:{fg}"]
    if bg:
        out.append(f"background:{bg}")
    if st.get("b"):
        out.append("font-weight:700")
    if st.get("dim"):
        out.append("opacity:.6")
    if st.get("i"):
        out.append("font-style:italic")
    if st.get("u"):
        out.append("text-decoration:underline")
    return ";".join(out)


def main():
    cols, rows = int(sys.argv[1]), int(sys.argv[2])
    st, out = {}, []
    for line in sys.stdin.read().split("\n")[:rows]:
        st = dict(st)
        parts, pos = [], 0
        for m in re.finditer(r"\x1b\[([0-9;]*)m", line):
            if m.start() > pos:
                parts.append(f'<span style="{css(st)}">{html.escape(line[pos:m.start()])}</span>')
            apply(st, m.group(1))
            pos = m.end()
        if pos < len(line):
            parts.append(f'<span style="{css(st)}">{html.escape(line[pos:])}</span>')
        out.append("".join(parts))
    w = int(cols * CHAR_W + 2 * PAD)
    h = int(rows * LINE_H + 2 * PAD + 30)
    print(f"""<!--size {w}x{h}--><!doctype html><meta charset="utf-8">
<style>
html,body{{margin:0;background:{BG}}}
.bar{{height:30px;display:flex;align-items:center;gap:8px;padding:0 14px;background:#27272a}}
.bar i{{width:12px;height:12px;border-radius:50%;display:inline-block}}
pre{{margin:0;padding:{PAD}px;font:13px/{LINE_H}px Menlo,monospace;color:{FG};white-space:pre}}
</style>
<div class="bar"><i style="background:#ff5f57"></i><i style="background:#febc2e"></i><i style="background:#28c840"></i></div>
<pre>{chr(10).join(out)}</pre>""")


main()
