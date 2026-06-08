# CLAUDE.md

Guidance for Claude Code when working in this repository.

## What cav is

`cav` (Claude Agents View) is a terminal UI — built with Bubble Tea / Lip Gloss —
for managing Claude Code **background sessions**: list, create, delete, reorder,
rename, search, preview, and open them. It's a richer, self-controlled
alternative to the built-in `claude agents` view (the gaps it fills: custom
ordering, content search, a markdown preview pane, and resilience to the daemon
dropping sessions after a laptop sleep).

Module path: `github.com/omeryahud/cav`. Single binary, no runtime config required.

## Build / install / run

```bash
go build -o cav .          # local build (gitignored)
go install .               # installs to ~/go/bin/cav (on PATH via ~/.zshrc)
go vet ./...               # keep clean
cav                        # run (or ~/go/bin/cav)
```

**Stale-binary gotcha (important):** a running Bubble Tea TUI keeps the code it
launched with — it does *not* pick up a rebuild. After `go install`, the user
must quit (`q`) and relaunch `cav`. Most "the fix doesn't work" reports trace
back to viewing an old running instance. When in doubt, check:
`ps -eo pid,lstart,args | grep '[c]av'` and compare to `~/go/bin/cav`'s mtime.

Dev render-test without a TTY of your own: run cav in an isolated tmux server
(`tmux -L cavtest new-session -d -x 150 -y 30 -c <dir> "$PWD/cav"`), let it
paint (a slow command like `go build -a` works as a delay), then
`tmux -L cavtest capture-pane -p -t 0`. Always `tmux -L cavtest kill-server`
after.

## Package layout

- `main.go` — entry; constructs `ui.New()` and runs the Bubble Tea program (alt-screen).
- `internal/claude/` — the only place that talks to the `claude` CLI and the
  on-disk daemon/job state.
  - `types.go` — `Session` (mirrors `claude agents --json`).
  - `client.go` — `List`, `Stop`, `Create`, `AttachCmd`, `LogsShellCmd`, `Logs`,
    `Roster`/`LoadRoster`/`JobID`, `JobState`, `JobRecord`/`ScanJobs`.
- `internal/ui/` — the Bubble Tea app.
  - `model.go` — `Model`, messages, commands (incl. the `doRefresh` merge run by
    the background `refreshLoop`), the `statusOf`/`statusRank`/`bucketLabel`
    status model, and filter/sort helpers.
  - `update.go` — `Update` + per-mode key handlers.
  - `view.go` — layout + rendering, Lip Gloss styles, and `renderSnippets`
    (markdown → ANSI via glamour).
- `internal/order/` — persists a custom session order (`~/.config/cav/order.json`).
- `internal/names/` — cav-local rename overrides (`~/.config/cav/names.json`).
- `internal/dismiss/` — cav-local set of sessions hidden with `d` (`~/.config/cav/dismissed.json`).
- `internal/dirs/` — portable directory candidates for the "new session" picker.
- `internal/preview/` — transcript snippet extraction for the markdown preview
  (non-live sessions).
- `internal/termview/` — reconstructs a live session's terminal screen from
  `claude logs` output via a vt10x emulator, rendered (with color) for the preview.

## How sessions are sourced (the critical knowledge)

There are **three** data sources, and they disagree — getting this right was the
bulk of the work. Don't "simplify" back to a single source.

1. **`claude agents --json`** — *live daemon workers only*. Fields per element:
   `pid, cwd, kind ("background"|"interactive"), startedAt (epoch ms),
   sessionId, name, status ("idle"|"busy")`. `name`/`status` exist only for
   background sessions. After a laptop sleep the daemon drops workers, so this
   list shrinks and sessions "disappear".

2. **`~/.claude/jobs/<jobId>/state.json`** — *durable per-job record*, survives
   sleep. Fields used: `sessionId, cwd, name, state, updatedAt` (RFC3339). The
   **directory name is the job id**. `state` ∈
   `working | done | blocked | stopped | error | ...`. ⚠️ Its `sessionId` field
   can be **stale after a `/branch`** — it holds the *original* session id, not
   the current one.

3. **`~/.claude/daemon/roster.json`** — `workers` keyed by **job id**, each with
   the **current** `sessionId`. This is the authoritative live
   `sessionId → jobId` mapping, *especially after a branch/fork* where (2) lags.

**`attach` / `stop` / `logs` key on the JOB ID** (the short id = jobs/ dir name =
roster worker key), **NOT the full session UUID**. They coincide for most
sessions but differ after branch/respawn (e.g. session `72cdfc0f` ↔ job
`cc12801d`). Passing the session id yields `No job matching ...`.

`doRefresh` (in `model.go`) merges them. It runs **continuously in a background
goroutine** (`refreshLoop`) — no fixed poll delay, so the list updates as fast as
a refresh completes (~0.5s, bounded by `claude agents --json`; a small `minRefresh`
floor only guards against a hot spin if a refresh returns instantly). Results flow
to the update loop via a channel (`waitRefresh`), which re-arms itself each time.
The merge:
1. List live sessions; build `sessionId → jobId` from `LoadRoster` (correct
   post-branch). State for live sessions comes from `JobState(jobId)`.
2. Add on-disk `ScanJobs` records **not** already covered by a live session
   (dedup by sessionId *and* job id — a branched session shares its job with the
   live one) and updated within `recentDays` (7). This keeps stopped and
   sleep-dropped sessions visible and resumable.
3. **Interactive** sessions (`kind:"interactive"` from `agents --json` — a plain
   `claude` REPL, a `!` bash command, or an agent-mode/stream-json child) are
   **filtered out** in `doRefresh` (step 1), so they never enter `m.all`. cav
   manages background sessions; these carry no name/status and aren't attachable
   here, so they were just noise. (`notAttachableReason` keeps a defensive
   interactive branch in case one ever leaks in via a job dir.)

Transcripts live at `~/.claude/projects/<encoded-cwd>/<sessionId>.jsonl`; the
encoding is lossy, so `internal/preview` **globs by `<sessionId>.jsonl`** rather
than reconstructing the path. Read the **tail** (not a whole-file scan): a single
huge line stalls `bufio.Scanner`.

## Status model (`statusOf`)

Status combines the live busy/idle signal with the job lifecycle state. **Key
insight:** `state == "working"` means the session is *alive*, NOT executing —
only `agents` status `busy` means actually running.

| condition | bucket | dot |
|---|---|---|
| agents status `busy` | `running` | ● green |
| state `blocked`/`waiting`/`needs_input`/`paused` | `waiting` (for input) | ◆ orange |
| state `error`/`failed` | `error` | ✗ red |
| state `working`/`idle`/`ready` and not busy | `idle` | ○ dim |
| state `done`/`complete` | `complete` | ✓ cyan |
| state `stopped` | `stopped` | ◌ dim |
| interactive / unknown | (other) | · dim |

Bucket sub-headers and dots are color-coded and kept in sync.

## UI behavior

- **Grouping** (`o` cycles three `groupMode`s): **dir→status** (default; by cwd,
  then status) → **status→dir** (by status, then cwd) → **none** (manual order).
  A directory header is the **name** (bold) with the **full path faint on its own
  line**; a status header is the color-coded bucket label. Whichever key is the
  *secondary* one is shown indented under the primary. The active non-default mode
  shows in the header (`group:status→dir` / `manual-order`). Rows show only
  **name · status · age** — no conversation snippet (that lives in the preview
  pane); cav doesn't read transcripts per-refresh for the list.
- **Stopped window:** stopped sessions live in a **separate window**, not the main
  list. `s` switches between the main (active) window and the stopped window.
  Selecting a stopped session and pressing `↵`/`→` **resumes** it (see Open/resume)
  and returns to the main window. Stopping a live session (see **Remove**) also
  moves it here.
- **Preview pane** (right, 50% width, `p` toggles), reloaded on a ~2s throttle
  (`previewRefresh`) even though the list refreshes continuously, so `claude logs`
  isn't hammered; a selection change reloads it immediately:
  - For a session with a **live worker**, it shows the **actual terminal screen**:
    cav pulls `claude logs` (the raw recent terminal output) and reconstructs it
    through a **vt10x emulator** (`internal/termview`), rendered with color. The
    emulator runs at a generous size (≥ the session's own width) so the session's
    layout reconstructs faithfully; each row is then **word-wrapped to the pane
    width** (breaking at spaces, not mid-word) so no horizontal content is lost.
    Note the session renders at its own width and cav can't resize it, so wrapped
    lines still have short remainders. The wrapped screen can be taller than
    the pane, so it's **bottom-anchored** to the latest content. It's a ~2s snapshot
    (no held `attach`), not keystroke-live.
  - For a session with **no live worker** (done/complete/stopped, or sleep-dropped),
    there's no terminal to show, so it falls back to the **recent conversation
    rendered from markdown via glamour** (role-labelled user=green,
    assistant=lavender, bottom-anchored).
  - Rendered async and cached; the cache is cleared on resize (width *or* height,
    since the live view depends on both).
  - **Scrollable:** `ctrl+u`/`ctrl+d` (half page) and `pgup`/`pgdn` (full page)
    scroll the pane. It's bottom-anchored with an upward offset (`previewScroll`);
    the header shows `↑`/`↑↓`/`↓` for which way more content exists, and the offset
    resets to the bottom whenever the selected session changes.
- **Open / resume** (`↵` or `→`): hands the current terminal to
  `claude attach <jobId>` via `tea.ExecProcess`; on exit, cav resumes in place. For
  a **stopped** session this *is* the resume path — `claude attach` respawns it from
  the `respawnFlags`/`resumeSessionId` the daemon stored in `state.json` (`claude
  stop`'s own help: "resume it later with `claude attach <id>`"), and cav switches
  back to the main window. You cannot attach to a session that's already attached
  elsewhere (e.g. the one you're typing in).
- **Remove** (`d`): branches on whether the session has a **live worker**. With one
  (status from `agents --json`), runs `claude stop` — moving it to the stopped
  window (optimistic hide, reconciled on refresh). With **no** live worker
  (done/complete/error, or sleep-dropped), `claude stop` is a no-op, so cav
  instead **hides it in a
  cav-local dismissed set** (`~/.config/cav/dismissed.json`) and never lists it
  again — dismissing needs only the session id, so it works without a job id.
  Non-destructive: the session stays on disk / keeps running and is still
  reachable via the `claude` CLI; undo by editing that file. The confirm prompt
  names which action will run.
- **Keys:** `↑/↓`/`jk` move · `g/G` top/bottom · `↵`/`→` open (resume from the
  stopped window) · `n` new (highlights it) · `N` new project (new dir) · `R` rename ·
  `d` remove · `l` logs · `o` group (cycle dir→status / status→dir / off) ·
  `s` stopped-window toggle · `J/K` reorder · `p` preview · `^u`/`^d` (or `pgup`/`pgdn`)
  scroll preview · `/` filter (metadata; **live fuzzy** — type to narrow
  (subsequence match, ranked best-first as a flat list overriding the grouping),
  `↑/↓` (or `ctrl+j/k`) move the selection without leaving the prompt, `↵` opens
  the selected session directly, and it starts empty each time) · `f` search
  (transcript content) · `esc` clear · `r` refresh · `q` quit.

## Config files

- `~/.config/cav/order.json` — custom session order (manual-order mode).
- `~/.config/cav/names.json` — cav-local rename overrides (the `claude` CLI has
  no rename verb and the daemon name isn't writable, so renames are cav-only).
- `~/.config/cav/dismissed.json` — cav-local set of session IDs hidden with `d`
  (those with no live worker). Non-destructive: the sessions stay on disk and are
  still resumable via the `claude` CLI; cav just never lists them.
- `~/.config/cav/roots.txt` — optional roots for the new-session dir picker
  (one path per line, `#` comments, `~` expansion). If absent, common dev dirs
  are auto-detected (`~/go/src`, `~/src`, `~/dev`, `~/projects`, ...).

The dir picker uses a native Go walk (depth-capped, noise dirs pruned), no `fd`.
Beyond cav's own roots it also walks the **`cdf` roots** in `~/.config/cdfpaths.txt`
(or `$CDFPATHS_FILE`) — same one-path-per-line format — so the picker offers the
same directories as the `cdf` command, **except `$HOME` itself**: `cdf` walks home
lazily with `fd`, but cav's eager walk of all of `$HOME` is far too slow/large
(~120k dirs here).

Both create flows are a small wizard — **session name, then an initial prompt**
(both optional, in that order) — and then they create the session and
**highlight it in the list** (move the cursor to it) rather than attaching:
- `n` (new session): fuzzy-pick an existing directory, then name → prompt.
- `N` (**new project**): type a name → cav makes
  `~/go/src/github.com/omeryahud/<name>` → session name (defaults to that dir) →
  prompt (empty = idle).

`claude.Create` parses the new job id out of `claude --bg`'s output
(`backgrounded · <id> …`); a new session registers with the daemon
asynchronously, so cav stashes that id (`selectJobID`) and moves the cursor to
the session once it shows up in a refresh (the continuous background loop catches
it, ~0.5s).
(`--bg` ignores `--session-id`, so we can't choose the id ourselves.)

## Conventions

- Keep `go vet ./...` clean and the build green before claiming done.
- All `claude`-CLI and on-disk-state access goes through `internal/claude` — keep
  it there.
- `Date.now`-style nondeterminism is fine here (normal Go binary, not a sandbox).
- Override the claude binary for testing with `$CLAUDE_BIN`.
