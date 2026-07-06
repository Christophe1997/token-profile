# token-profile

Renders a GitHub profile dashboard card — headline token/cost usage, a trend
graph, an activity streak, and a usage breakdown — from your local AI coding
agent session data, and publishes it into your `username/username` README.
It merges usage across all the machines you run it on using git itself as
the sync layer: no server, no account, no hosted service.

```
┌────────────────────────────────────────────────────┐
│ Token Profile — last 3 days                        │
│                                                    │
│ Tokens: 2,700   Cost: $4.05                        │
│                                                    │
│ Trend:                                             │
│  1,200 ┼────────╮                                  │
│  1,083 ┤        ╰──────────╮                       │
│    966 ┤                   ╰────╮                  │
│    850 ┤                        ╰───╮              │
│    733 ┤                            ╰────╮         │
│    616 ┤                                 ╰────╮    │
│    500 ┤                                      ╰    │
│        └┬───────────────────┬──────────────────┬   │
│       06-30               07-01              07-02 │
│                        tokens/day                  │
│                                                    │
│ Streak: 3 days                                     │
│                                                    │
│ Breakdown (per model):                             │
│   claude-sonnet-5 — 2,200 tokens ($3.30)           │
│   gpt-5.4 — 500 tokens ($0.75)                     │
│                                                    │
│ Last updated: 2026-07-03 10:19 UTC                 │
└────────────────────────────────────────────────────┘
```

## Prerequisites

- **[agentsview](https://github.com/kenn-io/agentsview)**, installed and on
  your `PATH`. token-profile shells out to it to read local AI coding session
  data — see agentsview's own
  [installation instructions](https://github.com/kenn-io/agentsview#installation).
- `git`, on your `PATH`, with push access to the repo hosting your rendered
  profile (usually `github.com/<you>/<you>`) from every machine you run
  token-profile on.

## Install

Pre-built binaries (linux/darwin, amd64/arm64) with checksums are published
via [GoReleaser](https://goreleaser.com) on each tagged release — download
one from this repo's [Releases](../../releases) page, or build/install from
source with Go 1.26+:

```sh
go install github.com/Christophe1997/token-profile/cmd/token-profile@latest
```

## Quick start

```sh
token-profile init --config ~/.token-profile/config.json
```

This is idempotent and safe to re-run. It:

1. Scaffolds `<!-- token-profile:start -->` / `<!-- token-profile:end -->`
   markers into your target repo's `README.md`, if they aren't there yet.
2. Writes a scheduling-entry snippet (a launchd plist on macOS, a crontab
   line elsewhere) to `--schedule-dest`, for you to review and install
   yourself — token-profile never touches your real crontab or
   `LaunchAgents` directory on its own.
3. Performs a first run: resolves your local usage, writes this machine's
   snapshot, renders the card, and commits + pushes the updated README.

After that, keep the profile fresh by running `token-profile run` again —
manually, or on whatever schedule you installed from the snippet above.

## Manual setup

If you'd rather not use `init`, or need to see exactly what it automates:

1. **Add the markers** to your target repo's `README.md` yourself, on their
   own lines, in the section you want the card to occupy:

   ```html
   <!-- token-profile:start -->
   <!-- token-profile:end -->
   ```

   `token-profile run` replaces only the content between these two lines,
   leaving everything else in the README untouched. If the markers are
   missing, `run` fails with an actionable error rather than guessing where
   to insert them.

2. **Write a config file** (see [Configuration](#configuration) below) with
   at least `targetRepo` set to the local working-copy path of the repo that
   hosts your rendered profile.

3. **Schedule `token-profile run`** yourself, via cron, launchd, or a manual
   habit, on at least one machine where your usage data lives. token-profile
   doesn't provide its own always-on scheduler.

4. **Run it once** to produce the first commit:

   ```sh
   token-profile run --config /path/to/config.json
   ```

## Configuration

token-profile reads a JSON config file, by default at
`~/.token-profile/config.json` (override with `--config` on either
subcommand). A missing file is not an error — every field falls back to its
default.

| Field | Type | Default | Description |
|---|---|---|---|
| `targetRepo` | string | *(required)* | Local working-copy path of the repo hosting your rendered profile. `run`/`init` fail fast if this is unset. |
| `breakdown` | `"per-model"` \| `"per-tool"` \| `"combined"` | `"per-model"` | How the rendered breakdown groups usage: by model, by coding agent/tool, or one combined total. |
| `trailingWindow` | duration string (e.g. `"720h"`) | *(unset)* | How far back to query usage. Unset defers to agentsview's own default trailing window (30 days) rather than diverging from it. |
| `machineIdPath` | string | `~/.token-profile/machine-id` | Where this machine's cached random identity is stored. Identity is random, not derived from hostname, so two machines that happen to share a hostname never collide. |

Example:

```json
{
  "targetRepo": "/Users/you/code/you",
  "breakdown": "per-tool",
  "trailingWindow": "720h"
}
```

**Note on token counts:** "tokens" throughout the card counts input, output,
prompt-cache creation, and prompt-cache read tokens together — every token
dimension agentsview reports, matching what the shown cost is actually
billed against. Totals of a million or more are shortened with a unit
suffix (e.g. `12.3M`, `1.4B`) everywhere a token count is shown, including
the trend graph's y-axis.

## How multi-machine sync works

Each machine writes its own complete usage history as a snapshot file under
`<targetRepo>/.token-profile/snapshots/<machine-id>.json`. Every run reads
every snapshot present in the repo — including ones from machines that
haven't run in a while — and merges them into the totals, trend, and streak
shown on the card. Git is the only sync layer: there's no server, queue, or
shared database, and pushes retry through a bounded fetch-rebase loop if
another one of your machines pushed first.

## Scope

token-profile intentionally stays a README-snippet generator, not a general
analytics dashboard:

- **Deferred, not planned for now:** GitHub Action-based auto-refresh (there's
  no hosted API for an Action runner to poll — agentsview only reads local
  session data); additional profile content blocks beyond the four shipped
  today (e.g. an activity heatmap, badges).
- **Out of scope entirely:** a general usage-analytics or team-leaderboard
  dashboard, and team/org-wide reporting or centralized sync backends. For
  that, see [agentsview](https://github.com/kenn-io/agentsview) itself.

## License

MIT — see [LICENSE](LICENSE).
