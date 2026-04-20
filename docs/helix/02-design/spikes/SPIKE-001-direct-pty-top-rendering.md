# SPIKE-001: Direct PTY Rendering With Unix Top

| Date | Status | Related |
|------|--------|---------|
| 2026-04-20 | Completed | `ADR-002`, `ADR-003`, `CONTRACT-003` |

## Question

Can DDX Agent run a real TUI through a direct PTY, control it with user-like
input, capture raw ANSI bytes, and render useful screen frames without tmux?

## Setup

A throwaway Go module was created outside the repo at `/tmp/agent-pty-spike`.
It used:

- `github.com/creack/pty v1.1.24` for direct PTY process control.
- `github.com/hinshun/vt10x` for VT/ANSI terminal emulation.
- `/usr/bin/top` with `TERM=xterm-256color`, initial size `80x24`.

The spike:

1. Started `top` under a direct PTY.
2. Read raw PTY bytes.
3. Fed those bytes to a terminal emulator.
4. Captured several screen frames.
5. Sent `1` to toggle per-CPU display.
6. Resized the PTY and terminal model to `100x30`.
7. Sent `q` to exit.

## Observed Result

The run completed successfully:

```text
raw_bytes=16714 frames=4
frame=0 t_ms=106 marker=none first_line=""
frame=1 t_ms=702 marker=top-header first_line="top - 17:09:47 up 2 days,  6:56,  6 users,  load average: 0.70, 0.80, 0.80"
frame=2 t_ms=1207 marker=top-header first_line="top - 17:09:48 up 2 days,  6:56,  6 users,  load average: 0.70, 0.80, 0.80"
frame=3 t_ms=1904 marker=top-header first_line="Tasks:  74 total,   1 running,  73 sleeping,   0 stopped,   0 zombie"
```

The final rendered screen excerpt included per-CPU rows after sending `1`,
showing that input injection changed the TUI state. The raw prefix showed dense
ANSI control sequences:

```text
"\x1b[?1h\x1b=\x1b[?25l\x1b[H\x1b[2J..."
```

## Findings

- Direct PTY control is tractable for `top`: start, read, write, resize, and
  exit all worked without tmux.
- Raw PTY bytes are not directly usable as assertions. Even simple `top`
  output includes cursor motion, screen clears, SGR style changes, mode
  changes, and carriage-return/newline behavior.
- A real terminal emulator layer is required. Regex-based ANSI stripping is not
  sufficient for screen assertions, frame diffs, model/menu extraction, or
  replay inspection.
- Useful assertions should target semantic screen facts after rendering, such
  as presence of a `top` header, `Tasks:` row, per-CPU rows after input, and
  changed dimensions after resize.
- TUI frames are volatile. Clocks, uptime, PIDs, CPU percentages, process
  ordering, and animation counters require normalization separate from secret
  scrubbing.
- The terminal emulator backend is a real dependency decision. It must be
  wrapped behind `internal/pty/terminal` so the project can replace it if a
  library fails on Unicode, alternate screen, OSC sequences, resize behavior,
  or other TUI edge cases.

## Implications

- The PTY library must include `internal/pty/terminal`; raw byte capture alone
  is not enough.
- `internal/pty/terminal` should wrap an existing terminal emulator rather than
  implementing ANSI parsing from scratch.
- The cassette format should preserve `output.raw` while storing derived
  `frames.jsonl` from the terminal model. Raw bytes are evidence; frames are
  the review and assertion surface.
- `top` is a good first conformance target because it exercises screen clear,
  cursor positioning, style sequences, refresh frames, live input, resize, and
  volatile content.

## Follow-Up Requirements

- Compare terminal emulator candidates before locking one in:
  `vt10x`, `go-expect` internals, Charmbracelet/x ANSI tooling, and any small
  maintained VT parser that fits the required API.
- Add conformance fixtures for:
  - `top` multi-frame rendering and input/resize behavior.
  - A pager flow such as `less`.
  - An editor or curses-style full-screen flow such as `vim`, `nano`, or
    `dialog`.
- Add tests for Unicode/wide characters, color/style preservation policy,
  alternate-screen behavior, cursor visibility, scrollback, and resize races.

## Confidence

Medium. The spike proves direct PTY plus terminal emulation is feasible, but it
also confirms the terminal model is the hard part. The design is tractable only
if DDX Agent wraps a real emulator and validates it against multiple TUIs.
