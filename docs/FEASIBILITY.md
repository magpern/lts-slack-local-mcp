# Feasibility result

**Question (Phase 1 gate):** can a *dedicated* browser profile (never the user's
ordinary profile) yield a usable Slack web session — the `xoxc` token plus the
`d` cookie — after one interactive sign-in, and then be reused **silently and
headlessly** on later runs?

**Result: GO.** Proven on Windows 11 with installed Microsoft Edge, driven via a
persistent dedicated profile. Only sanitized capability data is recorded here: no
tokens, cookies, team/user identifiers, or workspace contents.

## Method

A dedicated persistent profile is launched with the installed Edge channel. The
Slack web client is opened; once it has booted for a workspace, the `xoxc` token
is read from the client's `localStorage` (`localConfig_v2` → the active team's
`token`) and the `d` cookie is read from the browser context. The pair is
validated against Slack `auth.test` before being considered usable (fail-closed).

| Run | Profile state | Window | Captured token | Captured `d` cookie | `auth.test` ok | Team+user present | Time |
|---|---|---|---|---|---|---|---|
| 1 | fresh dedicated profile | visible (interactive SSO) | yes | yes | yes | yes | ~26 s |
| 2 | same profile, warm | headless, no interaction | yes | yes | yes | yes | ~2 s |

## Conclusions

- First login requires a visible window for SSO/MFA; subsequent refresh is silent
  and headless from the persisted dedicated profile.
- The token is present in the client's `localStorage` once a workspace has booted;
  the `d` cookie persists in the dedicated profile. No ordinary/user profile is
  touched.
- `auth.test` is a suitable pre-use validation and identity probe.

## Decisions this settles for the port

- Harvest mechanism: drive the installed browser with a dedicated persistent
  profile; read the token from `localStorage` and the `d` cookie from the context;
  validate with `auth.test` before use.
- Headless-first refresh, visible window only for first login / when silent
  refresh yields nothing.

## Not established by this spike

- Behaviour on non-Windows platforms or other browsers.
- Behaviour across a real token expiry over time (covered by the auth lifecycle's
  error-driven re-acquisition and its tests).
- Whether Slack changes the web client's storage/boot behaviour in future.
