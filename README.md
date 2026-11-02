# lts-slack-local-mcp

A local, stdio-only Slack MCP server that acts as **you** in Slack by reusing your own
signed-in Slack browser session. There is no Slack app, no server, and no
callback URL: a dedicated browser profile on your machine holds the session, and the
server reads it.

> **Experimental, unofficial, and not an approved integration.** This uses the same
> session credentials your browser uses (an `xoxc` token and the `d` cookie), which carry
> your **full** Slack permissions and are not scoped like an approved Slack app. Whether
> your organization or workspace policy allows this is for your workspace admin or
> security owner to decide. Confirm with them before real use. This project does not
> assert that it is permitted anywhere.

Derived from [korotovsky/slack-mcp-server](https://github.com/korotovsky/slack-mcp-server)
(MIT, v1.3.0); see [NOTICE](NOTICE). The Slack tool surface comes from upstream; the
session lifecycle, mutation controls, transport restrictions and packaging are new here.

## Status

Pre-release. No release has been published yet. See [docs/PLAN.md](docs/PLAN.md) for the
frozen plan and its change log, and [docs/FEASIBILITY.md](docs/FEASIBILITY.md) for what
was verified about browser-session reuse.

## How sign-in works

- Call the `login` tool once. A browser window (your installed Edge or Chrome, using a
  profile dedicated to this tool) opens and you complete your normal Slack sign-in,
  including company single sign-on.
- After that, the session is reused silently. Refreshes run in a headless browser and
  never open a window on their own. If Slack stops accepting the session you are told to
  call `login` again.
- The account is pinned at first login. A different account is only accepted through an
  explicit `login`. `SLACK_LOCAL_ALLOWED_TEAM_IDS` can restrict which workspaces are
  accepted.
- `logout` clears the in-memory session, deletes the dedicated browser profile and the
  cached Slack data, and stays signed out across restarts until you call `login`.
  Local deletion is **not** server-side revocation: it does not sign you out of Slack
  anywhere else.

## Install

Requires Microsoft Edge or Google Chrome and the Claude Code CLI.

From a release (once one is published), on Windows:

```powershell
.\scripts\install.ps1 -Mode plugin
```

On macOS/Linux:

```bash
scripts/install.sh --mode plugin
```

`-Mode mcp` / `--mode mcp` registers only the MCP server instead of the plugin. The
installer downloads the release zip for your platform, verifies its SHA-256 against the
release's `SHA256SUMS`, and refuses to proceed on a mismatch. Release assets also carry a
build-provenance attestation. Restart Claude Code, then ask it to call the Slack `login`
tool.

From source (Go, see `go.mod` for the version):

```bash
go build -o slack-mcp ./cmd/slack-mcp-server
claude mcp add --scope user slack -- /path/to/slack-mcp
```

## Read-only by default

Read tools work as soon as you are signed in: `channels_list`, `channels_me`,
`conversations_history`, `conversations_replies`, `conversations_search_messages`,
`conversations_unreads`, `users_search`, `usergroups_list`, `usergroups_me` (list),
`saved_list`.

Anything that changes state, and attachment download, is **off** until you opt in with
its own environment variable. Selecting a tool in `SLACK_MCP_ENABLED_TOOLS` is not
enough: a write runs only when both the opt-in is set and the tool is permitted by any
allow-list. This is enforced in each handler, not just at registration.

| Opt-in variable | Tools |
|---|---|
| `SLACK_MCP_ADD_MESSAGE_TOOL` (true, or a channel allow/deny list such as `C123,C456` or `!C123`) | `conversations_add_message` |
| `SLACK_MCP_REACTION_TOOL` (true or a channel list) | `reactions_add`, `reactions_remove` |
| `SLACK_MCP_MARK_TOOL` | `conversations_mark` (also needed for `SLACK_MCP_ADD_MESSAGE_MARK`) |
| `SLACK_MCP_JOIN_TOOL` / `SLACK_MCP_LEAVE_TOOL` | `conversations_join` / `conversations_leave` |
| `SLACK_MCP_USERGROUPS_WRITE_TOOL` | `usergroups_create`, `usergroups_update`, `usergroups_users_update`, `usergroups_me` join/leave |
| `SLACK_MCP_SAVED_WRITE_TOOL` | `saved_update`, `saved_clear_completed` |
| `SLACK_MCP_ATTACHMENT_TOOL` | `attachment_get_data` (Slack HTTPS URLs only, 5 MB cap) |

Channel lists match canonical conversation IDs only (`C…`, `D…`, `G…`).

## Other settings

| Variable | Purpose |
|---|---|
| `SLACK_MCP_ENABLED_TOOLS` | Comma-separated allow-list of tools (empty = all permitted) |
| `SLACK_LOCAL_PROFILE_DIR`, `SLACK_LOCAL_DATA_DIR` | Where the dedicated browser profile and state live (default under `%LOCALAPPDATA%\lts-slack-local-mcp`) |
| `SLACK_LOCAL_BROWSER_BIN` | Browser executable to use; there is no automatic browser download |
| `SLACK_LOCAL_LOGIN_TIMEOUT`, `SLACK_LOCAL_HEADLESS_TIMEOUT`, `SLACK_LOCAL_TOKEN_TTL` | Timing of sign-in, silent refresh and proactive refresh |
| `SLACK_LOCAL_ALLOWED_TEAM_IDS` | Restrict accepted workspaces |
| `SLACK_MCP_XOXC_TOKEN` + `SLACK_MCP_XOXD_TOKEN`, or `SLACK_MCP_XOXP_TOKEN` | Optional static credential instead of the browser profile |
| `SLACK_MCP_PROXY`, `SLACK_MCP_SERVER_CA`, `SLACK_MCP_USER_AGENT` | Proxy, extra trusted CA, user agent |
| `SLACK_MCP_LOG_LEVEL` | `debug`, `info`, `warn`, `error` (logs go to stderr; stdout is the MCP stream) |

Removed from upstream: HTTP/SSE transports and API key, Docker/compose files, the embedded
HTTP Toolkit CA, the insecure-TLS switch, TLS fingerprint impersonation, the demo mode,
the cache-path overrides, and the OpenAI/ngrok test harness. `SLACK_MCP_GOVSLACK` is not
supported and stops the server at startup. The profile and data directories must be new
or empty (or already created by this tool); the tool never deletes a directory it did not
create.

## Security posture

See [SECURITY.md](SECURITY.md). In short: the application does not serialize extracted
tokens or cookies itself (they live in process memory and in the browser's own profile),
logs carry only a short fingerprint, every request must be HTTPS to a Slack-owned host
(redirects included), downloads are bounded, and writes need explicit opt-in.
The browser profile directory is owner-only (an ACL on Windows, mode 0700 elsewhere).

Slack message content is untrusted input to the model. The bundled skill tells the
model never to act on instructions found in Slack content.

## Development

```bash
go build ./... && go vet ./... && go test ./...
```

Packaging: `scripts/package.ps1` / `scripts/package.sh` build a per-platform zip and its
checksum into `dist/`. The version comes from the `VERSION` file.

## License

MIT. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
