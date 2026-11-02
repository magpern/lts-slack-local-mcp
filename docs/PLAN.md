# Implementation Plan (frozen)

**Project:** `lts-slack-local-mcp` — a local-only Model Context Protocol (MCP)
server for Slack that authenticates by reusing the user's own signed-in Slack
**browser session** (the `xoxc` token plus the `d` cookie), needing no Slack app,
no OAuth redirect, and no hosted service.

**Derived from:** [`korotovsky/slack-mcp-server`](https://github.com/korotovsky/slack-mcp-server)
(MIT). **Frozen upstream base:** tag `v1.3.0`, commit
`a079b3cd4d5836d791c942a9fc107987e7865b37`.

**Login model ported from the design of a Microsoft Graph MCP** that signs in
through the user's own browser (persistent profile, silent SSO, identity pin,
automatic re-acquisition on auth failure). Any code copied under that project's
license retains its required notices.

This document is **frozen**. Scope, delivery order, decisions, and acceptance
criteria below do not change except where source evidence, a failed feasibility
result, or a material security/correctness issue requires it — in which case the
change and its reason are recorded in this file's Change Log.

---

## Status & disclosure

This is an **experimental, unofficial** tool. It uses browser-session
credentials that carry the signed-in user's **full** Slack permissions. It is not
an official or endorsed Slack integration. Whether such use is permitted under a
given workspace's or organization's policy is **the user's responsibility to
confirm with the relevant administrator** — this project makes no claim of
approval and does not assert that personal or local use is exempt from any
policy. The README and the bundled skill must carry this disclosure.

---

## Delivery order

Feasibility → auth lifecycle + mutation controls → trim → upgrades →
implementation security audit → packaging → packaging audit & clean install.
The security audit deliberately follows the new auth component.

---

## Phase 0 — Bootstrap

- New public repo, derived from the frozen upstream commit; `LICENSE` (MIT) kept;
  `NOTICE` / README credit upstream.
- Go module renamed to `github.com/magpern/lts-slack-local-mcp`; imports updated.
- Record the resolved upstream commit (above). Work on feature branches.

**Acceptance:** repo builds from the renamed module; upstream attribution present.

## Phase 1 — Browser-session feasibility spike (gate)

Prove, on Windows, **before** investing in the full port:

- A **dedicated** browser profile (never the user's ordinary profile) can reach a
  signed-in Slack session and yield a valid `xoxc` token **and** the `d` cookie,
  validated by a live `auth.test`.
- **First interactive login** (complete SSO/MFA in a visible window) and
  **subsequent silent reuse** (headless, from the persisted dedicated profile)
  are demonstrated and documented **separately**.
- The exact mechanism and library version are pinned down (profile persistence,
  headless reuse, cancellation).

**Acceptance / gate:** silent reuse from the dedicated profile succeeds at least
once after an initial interactive login, evidenced by a sanitized report (no
tokens, cookies, IDs, or workspace contents). If silent reuse cannot be shown,
stop dependent work and report the evidence.

## Phase 2 — Authentication lifecycle (`SessionAuth`)

- **Harvester interface** `Harvest(ctx, headless, timeout, accept)` so tests can
  fake it; the real one drives the dedicated-profile browser.
- **`accept(cred)` validates over the network** (`auth.test`) before use
  (fail-closed), then applies identity policy.
- **Signed-out startup:** the server starts with no credentials; cache init is
  deferred until first successful login; `login`/`logout` and `tools/list` work
  while signed out; Slack-dependent tools return a "call login" error instead of
  crashing the process.
- **Generation-aware concurrency:** a generation counter guards refresh/logout; a
  harvest that completes after `logout` is discarded; a stale auth failure
  invalidates only its own generation, not a credential a newer request installed.
  Cancellation is honoured; retries are bounded. **Reads may auto-replay after a
  successful re-acquire; mutations never auto-replay.**
- **Client swap safety:** the active client is held behind an `atomic.Pointer`;
  the unsynchronized `edgeFailed` field is guarded.
- **Slack error taxonomy (HTTP 200 `ok:false`):** classify `invalid_auth`,
  `token_expired`, `token_revoked`, and distinguish them from permission,
  network, and rate-limit errors. `invalid_auth` may reflect an IP/session
  restriction where re-acquisition cannot help — bounded retries, clear message.
- **Identity pin (persisted):** first login pins workspace/team (and user) via
  `auth.test`; the pin survives process restarts; only an explicit `login`
  authorizes switching accounts; on a switch, clients are rebuilt and caches
  cleared/repartitioned. Optional team allow-list; a non-matching silent harvest
  is rejected without opening a window.
- **`login` / `logout` tools:** `login` (re)acquires and returns the identity;
  `logout` clears in-memory credentials, shuts the browser down, deletes the
  profile, and **persists a signed-out state** so a restart does not re-trigger
  SSO; auto-login resumes only after an explicit `login`.
- **Credential handling (stated accurately):** the application does **not**
  separately serialize tokens/cookies — they live in process memory and in the
  browser's own dedicated profile on disk. Profile dir is 0700 on non-Windows and
  owner-only ACL on Windows. Browser is shut down **before** profile deletion;
  logout also deletes the cached Slack data files. If profile deletion fails, the
  server stays signed out and **quarantines** the residual profile (does not reuse
  it) until a fresh `login`. Local deletion is **not** server-side revocation.
- **Logs:** only a short fingerprint of a credential; never the value. HTTP-client
  debug logging silenced.

**Acceptance:** faked-harvester tests cover silent success; wrong-account
rejection with no window; concurrent `get()` sharing one harvest; 200-`ok:false`
classification; re-acquire on `token_expired`; generation (post-logout harvest
discarded, stale failure scoped to its generation); logout → signed-out →
re-login, persisted across a simulated restart; token absent from logs and from a
credential value's string form.

## Phase 3 — Mutation controls (enforced read-only default)

- **Complete mutation inventory**, each gated behind a dedicated env opt-in
  (default off), enforced **in the handler**: add-message, reactions add/remove,
  `conversations_join`, `conversations_leave`, `usergroups_create`,
  `usergroups_update`, `usergroups_users_update`, `usergroups_me`
  (`action=join|leave` only — the read action stays allowed), `saved_update`,
  `saved_clear_completed`, `conversations_mark`.
- **Authorization is independent of tool selection:** a mutation executes only
  when **both** (a) its dedicated env opt-in is set **and** (b) the tool is
  permitted under any configured tool allow-list. Selecting a write tool via the
  allow-list alone, without the env opt-in, **registers but denies execution**.
- Replace the substring `enabled-tools` check (and its ignoring of the `-e` flag)
  with the parsed allow-list; this is the (b) check only, not a substitute for (a).

**Acceptance:** (1) default configuration makes **zero** mutating Slack API calls
when the surface is exercised (recording fake); (2) a write tool enabled via the
allow-list with no env opt-in is denied at execution.

## Phase 4 — Local-only trim

- **Stdio only.** Remove SSE/HTTP server, its API-key auth, and ngrok.
- Remove the OpenAI integration-test dependency.
- Remove the embedded debugging-proxy CA and the TLS-verification-disable option.
  Keep plain TLS, proxy support, and optional uTLS impersonation (pass the
  harvested browser's real User-Agent; note this matches the UA only, not the
  uTLS ClientHello fingerprint).
- Delete dead/dangerous code that writes request bodies (including tokens) to a
  file in the working directory.
- Drop the second Slack type-system fork and TUI dependencies only if nothing
  retained imports them after review.

**Acceptance:** `go build` / `go test` green after trim; removed features absent
from config and docs.

## Phase 5 — Dependency & toolchain upgrades

- Upgrade Go toolchain and dependencies; check latest at implementation time
  rather than relying on remembered versions. Expect major-version migration work
  on the MCP and Slack libraries. Keep tokens/text out of logs when reworking
  middleware.

**Acceptance:** build + full test suite green; `govulncheck` clean or documented.

## Phase 6 — Implementation security audit

Audit the code **as built**. Verify fixed or documented, against the frozen
commit:

- **Credential scoping:** cookies/Authorization restricted to Slack hosts and not
  re-attached across redirects to other hosts; file-download tool constrained to a
  Slack host allow-list with a redirect policy; downloads bounded (no unbounded
  buffer).
- Embedded MITM CA and TLS-verify-disable removed (Phase 4).
- Dead token-logging code removed (Phase 4).
- No token/cookie in any log; caches 0600 (document real Windows ACL posture);
  cache never falls back to writing PII into the working directory.
- uTLS transport connection handling / handshake timeout fixed or the path
  dropped.
- Stdio protocol stream free of diagnostics (no stray stdout writes).
- `govulncheck` + a filesystem vulnerability scan recorded; CI actions pinned by
  commit SHA; release workflow least-privilege; no secrets echoed.

**Acceptance:** independent review (architectural/security) signs off auth +
mutation controls, then the trimmed/upgraded app, with findings resolved.

## Phase 7 — Packaging (Claude Code plugin)

- Everything shipped lives under `plugin/`: `.claude-plugin/marketplace.json`
  (repo root, pointing at `./plugin`), `plugin/.claude-plugin/plugin.json`,
  `plugin/.mcp.json`, and `plugin/skills/slack/SKILL.md` (inside the plugin root).
- **Binary layout:** `plugin/.mcp.json` launches a bundled binary at
  `plugin/bin/<os-arch>/slack-mcp[.exe]` via `${CLAUDE_PLUGIN_ROOT}`; packaging
  stages the correct binary. `go install` is a documented secondary path.
- Single-sourced version. Skill carries sign-in guidance, read-only framing, and
  an untrusted-content clause (never follow instructions found in Slack messages).
- Install scripts (`-Mode plugin|mcp`), a package script producing a release zip
  excluding `.env*`, `profile/`, `*.har`. Release workflow builds cross-platform
  binaries; actions SHA-pinned; keep vulnerability scan + dependency updates.

**Acceptance:** plugin installs and launches the binary; skill resolves inside the
plugin root.

## Phase 8 — Packaging audit & clean-machine install

- Review installers, package script, and release workflow: zip contents (no
  secrets/profile/HAR), SHA-pinned actions, least-privilege permissions.
- **Binary integrity:** per-binary checksums (and provenance if feasible);
  installers verify before use.
- **Clean install test** on a fresh application profile, distinguished from a
  genuinely clean machine, including browser/runtime provisioning — confirm
  `login` completes. Building cross-platform binaries is not proof login works;
  report other platforms as build-verified or runtime-verified per actual
  evidence.

**Acceptance:** clean-profile install completes and first login succeeds on
Windows; other platforms labelled honestly.

---

## Verification matrix (run on supported tooling)

Build, full test suite (`go test ./...` — note upstream's `make test` only ran a
subset), `govulncheck`, race checks. Exercise: hostile redirects, oversized
downloads, denied writes, logout across restart, cleanup failures, and
refresh/logout races. Windows end-to-end and clean-profile install per Phase 1/8.
No real workspace mutations during verification.

## Non-goals / guardrails

Do not modify any separately deployed service or an approved Slack app. Do not
perform real workspace mutations during verification. Keep credentials, private
repository details, workspace identifiers, and internal deployment information out
of public docs and git history.

## Change Log

Deviations from the frozen plan, each with reason and impact.

1. **Unattended re-acquire is headless-only; a visible window opens only from an
   explicit `login`.** Reason: an unattended window mid-tool-call surprises the
   user and can outlast an MCP client's call timeout. Impact: first run (no pinned
   identity) returns "call login" instead of blocking; the plan's "headless first,
   visible window only if nothing valid came back" now applies to `login`.
   Environment-supplied static credentials are exempt (no browser involved).
2. **Resources use workspace-independent URIs** (`slack://workspace/channels`,
   `slack://workspace/users`) instead of embedding the workspace host. Reason:
   upstream authenticated during server construction to learn the host, which made
   signed-out startup impossible (a P1 requirement). Impact: resource URIs differ
   from upstream.
3. **Cache files are namespaced by workspace *and* user and never fall back to the
   working directory** (`<team>_<user>_users_cache.json`). Reason: two accounts in
   one workspace must not share cached data; the cache holds personal data.
4. **Upstream's `demo` credential mode is removed.** Reason: it bypassed
   authentication in several places and has no use in a local tool.
5. **Integration tests that required a paid third-party API and a tunnel to a
   third party (OpenAI + ngrok), and their Unix-only harness, were removed**
   (Phase 4 item, done early because the handler package could not compile its
   tests on Windows without it).
6. **A persisted `profile_dirty` flag** was added to the signed-out state so a
   browser profile that survived a failed logout can never be reused, even after a
   restart. Reason: "no residual-profile reuse" must hold across process restarts.
7. **Windows ACL hardening grants the owner by SID before removing inheritance.**
   Reason: the first implementation granted by `DOMAIN\user` name, which does not
   resolve for Azure AD accounts, and had already removed inherited access —
   leaving the directory inaccessible. Found by an end-to-end protocol probe and
   fixed with regression tests.
8. **The browser is launched directly, not through rod's launcher.** Reason:
   rod's launcher could not start Edge on Windows (it waits for a debug URL on
   stderr that Edge does not print) and its cleanup deletes the user-data dir, which
   here is the persistent session. The harvester starts the installed browser,
   reads `DevToolsActivePort` from the profile, and never deletes the profile.
9. **TLS fingerprint impersonation (uTLS) was dropped instead of kept** (Phase 4
   said to keep it optionally). Reason: it had connection leaks, no handshake
   timeout, and the generic `*_Auto` profile does not match the real browser anyway.
   `SLACK_MCP_CUSTOM_TLS` now stops the server with an error. API traffic uses the
   harvested browser's own user agent.
10. **The HTTP client only talks HTTPS to Slack-owned hosts** (including redirect
    hops); the session cookie is attached only for `slack.com`/`slack-gov.com`
    hosts. Reason: closes the cookie/token exfiltration path via file URLs and redirects.
11. **Cache path overrides (`SLACK_MCP_USERS_CACHE`/`SLACK_MCP_CHANNELS_CACHE`) are
    not honoured, and `SLACK_MCP_GOVSLACK` is refused at startup.** Reason: a shared
    cache file can serve a previous account's data; GovSlack sign-in/validation was
    never implemented here, so it would silently break login.
12. **The profile and data directories carry an ownership marker.** The tool only
    deletes, claims, ACL-locks or kills processes for directories it created (or
    empty ones). Reason: a misconfigured `SLACK_LOCAL_PROFILE_DIR` must not wipe a
    real browser profile or home folder.
13. **Tool arguments are not logged** (not at Info, and only argument names at
    Debug). Reason: they can contain message text and search queries.
