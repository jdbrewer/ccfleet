#!/usr/bin/env python3
"""Build a fake fleet of Claude Code sessions in tmux and screenshot ccfleet over it.

Everything is invented (repos, prompts, panes), so the images are safe to publish.

    python3 scripts/demo/demo.py            # writes docs/img/*.png
    python3 scripts/demo/demo.py --keep     # leave the demo tmux session up to poke at

Needs tmux, Go and Google Chrome (for the PNG render).
"""
import json
import os
import shutil
import subprocess
import sys
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
HERE = Path(__file__).resolve().parent
TMUX = "ccfleet-demo"
COLS, ROWS = 170, 26
NOW = int(time.time() * 1000)
MIN = 60_000

# name, repo, branch, state, minutes in state, title, prompt, extra
SESSIONS = [
    ("api-3f", "api", "feat/rate-limits", "waiting", 4, "Add per-tenant rate limits",
     "add per-tenant rate limits to the public API; Ada from support says the big tenants keep starving everyone else",
     {"tool": ("Bash", {"command": "pnpm db:migrate", "description": "Run the rate-limit migration"}),
      "edits": ["src/limits/policy.ts", "src/limits/store.ts"], "tokens": 118_000, "pane": "waiting"}),
    ("web-f8", "web", "fix/login-redirect", "done", 12, "Fix login redirect loop",
     "the login page bounces forever after SSO; fix it",
     {"text": "Fixed: the session cookie was set on the wrong path, so the guard never saw it.", "tokens": 64_000}),
    ("docs-2a", "docs", "main", "done", 3, "Write the v2 migration guide",
     "draft the v2 migration guide from the changelog",
     {"text": "The guide is in docs/migrate-v2.md; I left two questions for you at the top.", "tokens": 41_000}),
    ("api-c5", "api", "feat/rate-limits", "busy", 31, "Rate-limit metrics dashboard",
     "add metrics for the new rate limiter and a grafana panel",
     {"tool": ("Edit", {"file_path": "src/limits/policy.ts"}), "edits": ["src/limits/policy.ts"], "tokens": 140_000}),
    ("web-91", "web", "feat/onboarding-v2", "busy", 6, "Onboarding checklist redesign",
     "rebuild the onboarding checklist per the new design; ask Ada which steps support sees people skip",
     {"tool": ("Edit", {"file_path": "src/onboarding/Checklist.tsx"}), "tokens": 92_000}),
    ("infra-7d", "infra", "chore/node-22", "busy", 25, "Upgrade lambdas to Node 22",
     "upgrade every lambda to node 22 and fix whatever breaks",
     {"tool": ("Bash", {"command": "pnpm test --filter lambdas", "description": "Run lambda tests"}),
      "stale": 14, "tokens": 171_000}),
    ("api-44", "api", "feat/invoices", "busy", 9, "Invoice export (team lead)",
     "lead the invoice export build with a tester and a reviewer",
     {"tool": ("Agent", {"description": "Review the CSV exporter"}), "tokens": 77_000}),
    ("api-45", "api", "feat/invoices", "busy", 2, "Invoice export tests",
     "write tests for the invoice CSV exporter",
     {"tool": ("Write", {"file_path": "src/invoices/export.test.ts"}), "tokens": 38_000, "team": ("invoices", "tester")}),
    ("web-0c", "web", "main", "idle", 48, "Triage flaky e2e tests",
     "which e2e tests flaked this week and why", {"text": "Three flakes, all from the same unawaited toast.", "tokens": 55_000}),
    ("docs-6b", "docs", "main", "idle", 95, "Release notes for 3.4",
     "write release notes for 3.4; Ada wants the support-facing changes first", {"text": "Notes are ready for review.", "tokens": 30_000}),
    ("api-e1", "api", "spike/pgvector", "idle", 200, "Spike: pgvector search",
     "spike semantic search with pgvector over help articles", {"text": "Works; recall is good, p95 is 40 ms.", "tokens": 99_000}),
    ("infra-12", "infra", "main", "idle", 1440, "Explain the auth middleware",
     "how does the auth middleware decide which tenant a request belongs to", {"text": "It reads the host header first, then the token.", "tokens": 22_000}),
]

PANES = {
    "waiting": """\
⏺ The rate limiter is in: a token bucket per tenant, sized from the plan tier,
  with a shared fallback bucket so one tenant can't starve the rest.

⏺ Update(src/limits/policy.ts)
  ⎿  Updated src/limits/policy.ts with 38 additions and 4 removals

⏺ Next I need to run the migration that adds the tenant_limits table.

╭────────────────────────────────────────────────────────────────────╮
│ Bash command                                                       │
│                                                                    │
│   pnpm db:migrate                                                  │
│   Run the rate-limit migration                                     │
│                                                                    │
│ Do you want to proceed?                                            │
│ ❯ 1. Yes                                                           │
│   2. Yes, and don't ask again for pnpm db:migrate commands         │
│   3. No, and tell Claude what to do differently                    │
╰────────────────────────────────────────────────────────────────────╯
""",
}


def sh(*args, **kw):
    return subprocess.run(args, check=True, text=True, capture_output=True, **kw).stdout


def tmux(*args):
    return sh("tmux", *args)


def pane_text(name, title, prompt, extra):
    if extra.get("pane") in PANES:
        return PANES[extra["pane"]]
    out = f"> {prompt}\n\n⏺ {title}.\n"
    if "text" in extra:
        out += f"\n⏺ {extra['text']}\n"
    if "tool" in extra:
        tool, inp = extra["tool"]
        arg = inp.get("file_path") or inp.get("description") or ""
        out += f"\n⏺ {tool}({arg})\n  ⎿  Running…\n"
    return out


def transcript(name, repo, branch, state, minutes, title, prompt, extra):
    at = NOW - minutes * MIN
    if "stale" in extra:
        at = NOW - extra["stale"] * MIN

    def ts(ms):
        return time.strftime("%Y-%m-%dT%H:%M:%S.000Z", time.gmtime(ms / 1000))

    usage = {"input_tokens": 2, "cache_read_input_tokens": extra.get("tokens", 50_000), "cache_creation_input_tokens": 0}
    lines = [
        {"type": "user", "gitBranch": branch, "timestamp": ts(at - 3 * MIN), "message": {"role": "user", "content": prompt}},
        {"type": "assistant", "gitBranch": branch, "timestamp": ts(at - 2 * MIN),
         "message": {"model": "claude-opus-5-5", "usage": usage, "content": [{"type": "text", "text": f"On it: {title.lower()}."}]}},
    ]
    for i, path in enumerate(extra.get("edits", [])):
        lines.append({"type": "assistant", "gitBranch": branch, "timestamp": ts(NOW - (5 + i) * MIN),
                      "message": {"model": "claude-opus-5-5", "usage": usage, "content": [
                          {"type": "tool_use", "id": f"e{i}", "name": "Edit", "input": {"file_path": f"{{repo}}/{path}"}}]}})
        lines.append({"type": "user", "timestamp": ts(NOW - (5 + i) * MIN),
                      "message": {"content": [{"type": "tool_result", "tool_use_id": f"e{i}", "content": "ok"}]}})
    blocks = []
    if "text" in extra:
        blocks.append({"type": "text", "text": extra["text"]})
    if "tool" in extra:
        tool, inp = extra["tool"]
        inp = {k: (f"{{repo}}/{v}" if k == "file_path" else v) for k, v in inp.items()}
        blocks.append({"type": "tool_use", "id": "cur", "name": tool, "input": inp})
    if blocks:
        lines.append({"type": "assistant", "gitBranch": branch, "timestamp": ts(at),
                      "message": {"model": "claude-opus-5-5", "usage": usage, "content": blocks}})
    lines.append({"type": "ai-title", "aiTitle": title})
    lines.append({"type": "last-prompt", "lastPrompt": prompt})
    return lines


def build(demo: Path):
    bindir = demo / "bin"
    bindir.mkdir(parents=True)
    sh("go", "build", "-o", str(bindir / "ccfleet"), ".", cwd=ROOT)
    # named "claude" so it looks like a Claude Code process to anything inspecting it
    sh("go", "build", "-o", str(bindir / "claude"), "./scripts/demo/fakeclaude", cwd=ROOT)

    claude = demo / "claude"
    (claude / "sessions").mkdir(parents=True)
    (claude / "projects" / "-demo").mkdir(parents=True)
    repos = {}
    for repo in sorted({s[1] for s in SESSIONS}):
        d = demo / "src" / repo
        d.mkdir(parents=True)
        sh("git", "init", "-q", str(d))
        repos[repo] = str(d)

    tmux("new-session", "-d", "-s", TMUX, "-n", "board", "-x", str(COLS), "-y", str(ROWS))
    seen = {}
    team_panes = {}
    for name, repo, branch, state, minutes, title, prompt, extra in SESSIONS:
        text = demo / f"{name}.txt"
        text.write_text(pane_text(name, title, prompt, extra))
        tmux("new-window", "-d", "-t", f"{TMUX}:", "-n", name, "-c", repos[repo],
             f"cat {text}; exec {bindir / 'claude'}")
        pane, window, pid = tmux("display", "-p", "-t", f"{TMUX}:{name}", "#{pane_id} #{window_id} #{pane_pid}").split()
        sid = f"demo-{name}"
        (claude / "sessions" / f"{pid}.json").write_text(json.dumps({
            "pid": int(pid), "sessionId": sid, "name": name, "cwd": repos[repo], "kind": "interactive",
            "version": "2.1.305", "status": state if state != "done" else "idle",
            # ccfleet treats a process that started after its session as a reused pid
            "startedAt": int(time.time() * 1000), "statusUpdatedAt": NOW - minutes * MIN, "tmux": f"{TMUX}:{window}.{pane}"}))
        lines = transcript(name, repo, branch, state, minutes, title, prompt, extra)
        body = "\n".join(json.dumps(l, separators=(",", ":")) for l in lines).replace("{repo}", repos[repo]) + "\n"
        (claude / "projects" / "-demo" / f"{sid}.jsonl").write_text(body)
        if state == "idle":
            seen[sid] = NOW  # looked at already; "done" sessions stay unseen
        if "team" in extra:
            team_panes[pane] = extra["team"]
        if name == "api-44":
            lead = sid
    members = [{"name": "team-lead", "agentType": "team-lead"}] + [
        {"name": role, "agentType": "general-purpose", "tmuxPaneId": pane} for pane, (_, role) in team_panes.items()]
    (claude / "teams" / "invoices").mkdir(parents=True)
    (claude / "teams" / "invoices" / "config.json").write_text(
        json.dumps({"name": "invoices", "leadSessionId": lead, "members": members}))
    (demo / "state" / "ccfleet").mkdir(parents=True)
    (demo / "state" / "ccfleet" / "seen.json").write_text(json.dumps(seen))
    return bindir


def board(demo: Path, bindir: Path, keys, out: Path):
    env = f"CLAUDE_CONFIG_DIR={demo / 'claude'} XDG_STATE_HOME={demo / 'state'} CCFLEET_THEME=dark"
    tmux("respawn-pane", "-k", "-t", f"{TMUX}:board", f"env {env} {bindir / 'ccfleet'}")
    time.sleep(2)
    for k in keys:
        tmux("send-keys", "-t", f"{TMUX}:board", *k)
        time.sleep(0.6)
    time.sleep(1.2)
    ansi = tmux("capture-pane", "-e", "-p", "-t", f"{TMUX}:board")
    html = demo / (out.stem + ".html")
    html.write_text(subprocess.run([sys.executable, str(HERE / "ansi2html.py"), str(COLS), str(ROWS)],
                                   input=ansi, text=True, capture_output=True, check=True).stdout)
    chrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
    if not os.path.exists(chrome):
        chrome = shutil.which("google-chrome") or shutil.which("chromium") or "chromium"
    w, h = html.read_text().split("<!--size ")[1].split("-->")[0].split("x")
    out.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run([chrome, "--headless=new", "--disable-gpu", "--hide-scrollbars", "--force-device-scale-factor=2",
                    f"--window-size={w},{h}", f"--screenshot={out}", html.as_uri()],
                   check=True, capture_output=True)
    print("wrote", out.relative_to(ROOT))


def main():
    keep = "--keep" in sys.argv
    subprocess.run(["tmux", "kill-session", "-t", TMUX], capture_output=True)
    demo = Path("/tmp/acme")  # short, readable cwd in the screenshots
    shutil.rmtree(demo, ignore_errors=True)
    demo.mkdir()
    try:
        bindir = build(demo)
        img = ROOT / "docs" / "img"
        board(demo, bindir, [], img / "board.png")
        board(demo, bindir, [["/"], ["a", "d", "a"], ["Enter"]], img / "search.png")
    finally:
        if not keep:
            subprocess.run(["tmux", "kill-session", "-t", TMUX], capture_output=True)
            shutil.rmtree(demo, ignore_errors=True)
        else:
            print(f"demo left running: tmux attach -t {TMUX}  (files in {demo})")


if __name__ == "__main__":
    main()
