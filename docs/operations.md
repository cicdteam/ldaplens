# Logs and operations

Logs, accountability and what to do when something goes wrong.

## Logs and accountability

ldaplens logs to stdout, one `key=value` line per event. The lines below,
except `auth failed`, end with who made them:

- `session`: the first 8 hex characters of the SHA-256 of the browser's
  session cookie, empty without one. A connect gives the browser a new
  session ID: its `connected` line carries the new one, and the one the
  request came with in `previous`. A connect that used the shared
  connection keeps the session as it is and has no `previous`; a password
  typed for a shared profile opens the browser's own connection, so it gets
  a new session ID. It ties one browser's lines together across connects;
  the session ID itself is never logged.
- `remote`: the client address, without the port. From a proxy in
  `--trusted-proxies` it is taken from `X-Forwarded-For` (see [Behind a
  reverse proxy](deployment.md#behind-a-reverse-proxy)); behind any other proxy it is the
  proxy's: match it with the proxy's access log.
- `proxy`: the address and port of the trusted proxy the request came
  through, only when that is not `remote`.

The lines:

| Message | Fields |
|---|---|
| `connected`, `connect failed` | `profile`, `bindDN`; `connected` adds `previous` (the `session` fingerprint of the cookie the request carried, empty without one) unless it used the shared connection, `connect failed` adds `error` |
| `connect throttled` | `profile`, `limit` (`account` for its DN, `client` for its client address), `bindDN`: a connect refused with 429 `too_many_attempts` |
| `disconnected` | `profile`, `bindDN` |
| `modified` | `profile`, `dn`, `changes` (how many), `attrs` (the attribute of each change), `bindDN` |
| `added` | `profile`, `dn`, `attributes` (how many), `bindDN` |
| `renamed` | `profile`, `dn`, `newDN`, `bindDN` |
| `password set` | `profile`, `dn`, `method`, `bindDN` |
| `deleted` | `profile`, `dn`, `bindDN` |
| `modify rejected`, `add rejected`, `rename rejected`, `password rejected`, `delete rejected` | the fields of the write, `code`, `ldapResultCode`, `bindDN`; an unexpected error also logs `<op> failed` with `error` |
| `profile saved` | `name`, `oldURL` (empty for a new profile), `url`, `readOnly`, `insecureSkipVerify` |
| `auth failed` | `remote` only, and `proxy` when it differs from `remote`: a request refused with 401 for a missing or wrong token |

`bindDN` is the DN the connection bound as: the profile's `bindDN`, or for a
`bindDNTemplate` profile the template with the username filled in; empty for
an anonymous bind. Attribute values, passwords, tokens and
session IDs are never logged, and a DN that does not parse or has a secret
attribute in an RDN is logged as `<redacted>`.

At startup the server logs `listening` with `version`, `mode` (`local` or
`server`), `addr`, `basePath`, `profiles` (how many from `connections.yaml`),
`saved` (how many saved connections it loaded) and `dataDir` (empty when
saving is disabled). In local mode, a token that no flag or variable set adds
the login link, and no data dir adds the warning `saving profiles disabled
(no data dir)` (see [Two modes](configuration.md#two-modes)). A shutdown that runs out of
time logs the warning `shutdown drain timed out` with `after`.

The directory's own audit log sees only the bind DN. A profile bound as a
shared service account (`passwordEnv` or `passwordFile`) makes every write
that account's, and with one shared token only `session` and `remote` tell
people apart. For per-person accountability, use [per-user
login](deployment.md#per-user-login): everyone binds as themselves with their own
password, and both the directory and ldaplens log their DN.
## Operations

- **Logs** go to stdout, one `key=value` line per event (see [Logs and
  accountability](#logs-and-accountability)): `kubectl -n ldaplens logs
  -l app.kubernetes.io/name=ldaplens --prefix --tail=-1` for every replica, or
  `journalctl -u ldaplens` for the systemd unit. A startup
  error is one line on stderr that starts with `ldaplens:`, and the process
  exits with status 1; for a pod in CrashLoopBackOff, add `--previous` to
  `kubectl logs <pod>`.
- **A broken `profiles.json`** is moved to `profiles.json.bad` next to it
  (`/data/ldaplens` in the image, `/var/lib/ldaplens` with the unit), and
  ldaplens starts without saved profiles and logs `profiles file was
  invalid and was moved aside` with its `path` and `error`. The next broken
  file replaces it. To
  put a fixed copy back, stop ldaplens first, because it rewrites the file
  from its own list at the next save.
- **Debugging** the image, which has no shell: attach a debug container
  that runs as the app's user. `--custom` sets that user even when the pod
  sets none, because the `restricted` policy refuses busybox's root.

  ```sh
  echo '{"securityContext":{"runAsUser":65532,"runAsGroup":65532}}' > debug.json
  kubectl -n ldaplens debug -it <pod> --profile=restricted --target=ldaplens \
    --image=busybox --custom=debug.json
  ```

  The app's files are under `/proc/1/root/` there, for example
  `/proc/1/root/etc/ldaplens/config/connections.yaml`.
