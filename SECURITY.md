# Security Policy

## Reporting a vulnerability

Please do not open a public issue. Use GitHub's private vulnerability reporting
for this repository (Security tab, "Report a vulnerability"). Include steps to
reproduce, the affected version, and the impact you see.

## Scope and threat model

This is a local-only MCP server that acts as the signed-in user through a Slack
web session (an `xoxc` token plus the `d` cookie) read from a dedicated browser
profile on the same machine. It is a stdio process: it opens no listening
socket and has no server component.

What the design protects:

- Session credentials are held in process memory and in the dedicated browser
  profile. The application does not separately write extracted tokens or cookies
  anywhere. Logs carry only a short SHA-256 fingerprint.
- Every request, including redirect hops, must be HTTPS to a Slack-owned host;
  anything else is refused before the session is attached.
- State-changing tools are off by default and need a dedicated environment
  opt-in in addition to any tool allow-list.
- Logout is persisted across restarts and deletes the dedicated profile and the
  cached Slack data. Local deletion is not server-side revocation: it does not
  sign you out of Slack elsewhere.

What it does not protect against: other processes running as the same user
(they can read the profile directory like any browser profile), and an
administrator who forbids session-based access to the workspace. Confirm your
organization's policy before use.
