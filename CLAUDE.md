# ccfleet

TUI board for every live Claude Code session in tmux: who needs you, who is working, jump and peek.
Plan: `docs/plans/2026-10-20-ccfleet-v1.md`. Plans go in `docs/plans/YYYY-MM-DD-<slug>.md`.

- Build: `go build -o bin/ccfleet .`
- Verify: `go vet ./... && go test ./...`, then `./bin/ccfleet ls` against live sessions
- TUI check: run it in a detached tmux pane (`tmux new -d -s t -x 160 -y 45 ./bin/ccfleet`) and `tmux capture-pane -p -t t`
- All `~/.claude` parsing lives in `internal/fleet` (session.go, transcript.go, teams.go). It's an internal format, so keep it there.

## Gotchas

| Gotcha | Why |
|---|---|
| `tmux send-keys -t s 5 j` reaches Bubble Tea as one `"5j"` rune message | `key()` splits multi-rune messages. Keep that split. |
| Keys for a popup can't be sent with `send-keys` | They have to come in through the client's tty. For tests, use a pty client from Python (unset `TMUX`, pass `-S` for the socket). |
| macOS kills a copied `/bin/sleep` when it's renamed to fake `claude` | Code signing. Build a tiny Go binary instead. |
