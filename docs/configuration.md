# Configuration

The two modes, profiles (`connections.yaml` and saved ones) and every flag.

## Two modes

ldaplens runs in one of two modes, picked by its first argument:

- **`ldaplens`**, or **`ldaplens local`**, is for one person: on a laptop, a
  jump host or in a quick Docker try. It keeps the connections you save in
  the UI and a login token in a data dir: `--data-dir`, or else `ldaplens` in
  the user config dir, created with mode 0700 (Linux:
  `$XDG_CONFIG_HOME/ldaplens` or `~/.config/ldaplens`; macOS: `~/Library/Application
  Support/ldaplens`; Windows: `%AppData%\ldaplens`; the image: `/data/ldaplens`).
- **`ldaplens server`** is for a team, for example in Kubernetes. It serves
  only the profiles in `--config` and keeps nothing on disk.

Any other first argument that does not start with `-` fails with `unknown
command "x": want local or server`, and so does a mode after the flags.

**Local mode always needs a token.** Unless `--auth-token` or
`--auth-token-file` sets one, the first start makes one, keeps it in `token`
in the data dir (0600), and every start logs a login link:

```text
level=INFO msg="open the UI with this login link (adjust the host and port to how you reach it)" url="http://localhost:8080/?token=..."
```

The link sets a cookie for 30 days, and the token survives restarts, so a
bookmark keeps working. To change it and log out every browser, delete the
file and restart. Without a user config dir (no `$HOME`), there is no data
dir: ldaplens logs `saving profiles disabled (no data dir)` and makes a new
token at every start.

**Server mode needs `--config`**, refuses `--data-dir` and never makes a
token. Without `--auth-token` or `--auth-token-file`, every profile must ask
each person for a password (a `bindDNTemplate`, or a `bindDN` without
`passwordEnv`/`passwordFile`); a profile that binds with a configured
password or anonymously fails startup with `profile <name> binds with a
configured password or anonymously; server mode needs --auth-token or
--auth-token-file`.

**What the token is for.** It guards ldaplens itself, before any LDAP bind.
A profile that asks each person for a password needs no guard: the
directory checks that password. One that binds with a configured password or
anonymously binds as the server, so without the token anyone who reaches the
URL would work as that account. The token is one secret for the instance,
not a login: who you are, in the logs and in the directory, comes from the
bind.

**Server mode shows only what it takes to connect.** Local mode shows all of
it. In server mode:

- the profile list gives each profile's `name`, `label`, `color`,
  `readOnly`, `connected`, `userLogin` and `needsPassword` only: no URL,
  base DN, bind DN, CA or TLS settings. The server address shows in the
  status bar once a connect has succeeded;
- there is no "New connection", no profile editing and no "Trust this CA and
  connect"; a save answers 403 `saving_disabled`;
- a profile with a bind DN and no configured password asks for a "Password",
  without naming the DN;
- a failed connect tells nothing about the directory. Every LDAP result
  (not only 49, but also 389-DS's 19 and 53 for a locked or inactivated
  account) answers 401 `invalid_credentials` with `invalid username or
  password` and no diagnostic: the directory's message, such as Active
  Directory's `account locked out`, would tell anyone that the account
  exists. An unreachable server answers 503 `network` and an untrusted
  certificate 422 `tls_untrusted`, without host names, error text or the
  certificate chain. The server log keeps the full error in `connect failed`.

`GET /api/server` tells the UI the mode, and carries `notice` and
`noticeColor` when they are set.

## connections.yaml

```yaml
connections:
  - name: corp-ad
    label: Corp Active Directory
    url: ldaps://dc1.corp.example:636
    baseDN: dc=corp,dc=example
    bindDN: cn=reader,ou=svc,dc=corp,dc=example
    passwordEnv: CORP_AD_PASSWORD # or passwordFile, or type it in the UI
    caFile: /etc/ldaplens/corp-ca.pem
    readOnly: true
    color: "#d33"
```

| Field | Meaning |
|---|---|
| `name` | Required. The identifier, in the workspace URL (`/c/corp-ad`) and the API: letters, digits, `.`, `_`, `-`, at most 64. |
| `label` | What the UI shows instead of the name, up to 80 characters: the connection list (with the name under it), the connect dialog, the header and the command palette. |
| `url` | Required. `ldap://` or `ldaps://`, with a host, without credentials. |
| `startTLS` | StartTLS on an `ldap://` URL; not with `ldaps://`. |
| `baseDN` | Where the tree starts; empty: the server's naming context. |
| `bindDN` | Bind as this DN. Without a configured password, connect asks for one. Empty, and no template: anonymous. |
| `bindDNTemplate` | Each person signs in as themselves, see [Per-user login](#per-user-login). |
| `passwordEnv`, `passwordFile` | Where the server reads the bind password: an environment variable, or a file (one trailing newline dropped). Makes the profile shared. |
| `caFile`, `caPEM` | CA certificates to trust, on top of the system ones: a file, or PEM inline. |
| `insecureSkipVerify` | Do not verify the server's certificate. The status bar shows "certificate not verified". |
| `readOnly` | The UI offers no Edit or entry actions, and the server refuses every write with 403 `read_only`. |
| `color` | A dot next to the profile in the list and the header: a hex color such as `#d33`, or a CSS color name. |
| `connectTimeout`, `searchTimeout` | Durations such as `10s`: dial and bind (default 10 s), each operation (default 30 s). |

`readOnly` and `color` are the only safety and marking options.

### Per-user login

A profile with `bindDNTemplate` instead of `bindDN` lets everyone sign in as
themselves: one profile serves the whole team, each person types their own
username and password, and ldaplens binds as the template with `{user}`
replaced. The directory's access rules apply to each person, its audit log
and ldaplens's logs name who made each change, and no service account
password is stored. It is the recommended setup for a team.

The template contains `{user}` exactly once and cannot be combined with
`bindDN`, `passwordEnv` or `passwordFile`. It is read from
`connections.yaml` only; profiles saved in the UI cannot have it.

```yaml
connections:
  - name: people # OpenLDAP: the full DN
    url: ldap://ldap.example.org:389
    startTLS: true
    baseDN: dc=example,dc=org
    bindDNTemplate: uid={user},ou=people,dc=example,dc=org
  - name: corp-ad # Active Directory: a UPN (or a down-level name, CORP\{user})
    url: ldaps://dc1.corp.example:636
    baseDN: dc=corp,dc=example
    bindDNTemplate: "{user}@corp.example"
```

The username must match `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`, so it cannot
change the structure of the DN (in a template with `=` it is also escaped as
a DN value), and the password must not be empty, since an empty one would be
an anonymous bind on many servers. Nothing reaches the directory before both
checks pass. A wrong password is 401 `invalid_credentials`; five in 60
seconds for one DN, or twenty from one client address, refuse it until the
minute is over ([Flags](#flags)).

The connect dialog has a Username field marked `autocomplete="username"`, so
a password manager offers the accounts it saved. Once connected, the header
shows `Signed in as` and the DN the connection bound as (or the UPN), and the
log lines of the connection carry it as `bindDN`. Two people on the same
profile each have their own connection.

| Code | Status | When | The connect dialog says |
|---|---|---|---|
| `username_required` | 400 | no username | Enter your username. |
| `invalid_username` | 400 | the username does not match the pattern | A username has only letters, digits, ".", "_" and "-", starts with a letter or digit and is at most 64 characters long. |
| `password_required` | 400 | no password | this profile needs a password |
| `too_many_attempts` | 429 | five wrong passwords for this DN, or twenty from this client address, in 60 seconds; `Retry-After` gives the seconds to wait | Too many failed sign-ins. Wait a minute and try again. |

The typed password stays in the server's memory with the connection, so
that ldaplens can reconnect after a drop without asking again. It is
forgotten at disconnect, after `--session-idle` without use, or
`--session-max-age` after connecting. If the directory stops accepting it,
ldaplens closes the connection at the next reconnect and asks to sign in
again.

## Saved connections

Profiles created in the UI are kept, without passwords, in `profiles.json`
in the data dir of local mode; without a data dir, as in server mode, a save
fails with 403 `saving_disabled`. At most 200 profiles, each at most 4 KiB as
JSON plus up to 16 KiB of `caPEM`; more fails with 409 `profile_limit`.
Saves are written to a temporary file, synced and renamed, so a crash never
leaves a partial file.

A `profiles.json` that does not parse or holds an invalid profile is moved to
`profiles.json.bad`, and the server starts without saved profiles and logs
`profiles file was invalid and was moved aside`. On a read-only data dir, or
for a file the server cannot read, that is a startup error instead.

Run one ldaplens per data dir: it locks `.lock` there, and a second process
exits with `data dir ... is in use by another ldaplens process`. A
`--data-dir` that does not exist is a startup error (the default one is
created). A read-only data dir still works if it holds the token file or a
token is set: saved profiles are listed, saving fails, and the server logs
`data dir is not writable; saving profiles will fail`.

Back up `profiles.json` (it holds no passwords), and always before an
upgrade: after a rollback, the older version drops the settings it does not
know at its next save.

## Flags

| Flag | Env | Mode | Default |
|---|---|---|---|
| `--listen` | `LDAPLENS_LISTEN` | both | `127.0.0.1:8080` (`0.0.0.0:8080` in the image) |
| `--base-path` | `LDAPLENS_BASE_PATH` | both | none |
| `--config` | `LDAPLENS_CONFIG` | both, required in server | none |
| `--data-dir` | `LDAPLENS_DATA_DIR` | local | `ldaplens` in the user config dir (see [Two modes](#two-modes)) |
| `--auth-token` | `LDAPLENS_AUTH_TOKEN` | both | local: kept in `token` in the data dir; server: none, which allows only profiles that ask for a password |
| `--auth-token-file` | `LDAPLENS_AUTH_TOKEN_FILE` | both | none; a file holding the token, such as a mounted Secret; one trailing newline (`\n` or `\r\n`) is dropped |
| `--allowed-hosts` | `LDAPLENS_ALLOWED_HOSTS` | both | none; `localhost`, `127.0.0.1` and `::1` are always allowed; comma-separated extras |
| `--session-idle` | `LDAPLENS_SESSION_IDLE` | both | `30m` |
| `--session-max-age` | `LDAPLENS_SESSION_MAX_AGE` | both | `8h` |
| `--secure-cookies` | `LDAPLENS_SECURE_COOKIES` | both | `false`; TLS and `X-Forwarded-Proto: https` make cookies Secure without it (see [Behind a reverse proxy](deployment.md#behind-a-reverse-proxy)) |
| `--trusted-proxies` | `LDAPLENS_TRUSTED_PROXIES` | both | none; comma-separated CIDRs or IPs of reverse proxies whose `X-Forwarded-For` is trusted for the client address (see [Behind a reverse proxy](deployment.md#behind-a-reverse-proxy)) |
| `--notice` | `LDAPLENS_NOTICE` | both | none; a plain-text message shown above the header on every page once the access token is accepted (markup shows as typed, a newline breaks the line), trimmed, at most 300 characters (a longer one is a startup error) |
| `--notice-color` | `LDAPLENS_NOTICE_COLOR` | both | none; the notice strip's background, `#rgb` or `#rrggbb` (anything else is a startup error), with black or white text, whichever reads better |
| `--version` | | both | prints `ldaplens <version>` and exits |

A flag beats its variable, and an empty variable counts as unset. A variable
that does not parse (`LDAPLENS_SECURE_COOKIES=yes`, `LDAPLENS_SESSION_IDLE=30`)
is a startup error even when its flag is set. Both token flags, or both
token variables, at once are a startup error, and so is a token file that
is missing or holds an invalid token (at least 16 characters from
`A-Z a-z 0-9 . _ ~ -`); the error names the file, never the token.

### Sessions

A browser's connections live in a session on the server, closed after
`--session-idle` without use. Each connection also closes `--session-max-age`
after it was connected, however active (an automatic reconnect does not
restart the count). A session holds at most 8 connections (409
`too_many_connections`), and the server at most 1000 sessions, closing the
least recently used past that. Every successful connect moves the browser to
a new session ID, so an ID seen before a connect is worthless after it.

### Shared profiles

A profile that binds with its configured password or anonymously is shared:
one connection per ldaplens process serves every browser with the token,
outside any session, so any replica serves it. It is dialed at the first
request that needs it, closes after `--session-idle` without use or
`--session-max-age` after the dial, and stays open when one browser
disconnects; saving the profile in the UI closes it, so the next request
uses the new settings. A typed password or a `bindDNTemplate` gives the browser a
connection of its own, on the process that holds its session: that needs one
replica or sticky sessions ([Running for a
team](deployment.md#running-for-a-team)).

### Failed sign-ins

Two limits apply to every connect with a typed password, each over 60
seconds, with 429 `too_many_attempts` and `Retry-After`:

- **Five wrong passwords for one bind DN** refuse that DN on that server.
- **Twenty from one client address**, whatever the DNs, refuse that address,
  so that a few passwords tried on many accounts are caught too.

A success clears the DN's count, not the address's. Connects still in flight
count, so a burst gets no more tries than the limit. Other failures, such as
an unreachable server, do not count. A connect with the configured password,
or an anonymous one, is never refused, so a wrong service password cannot
lock its DN for everyone; in return, anyone who reaches ldaplens can keep
one account from connecting through it, a minute at a time. The directory's
own lockout policy is separate. Behind a proxy, set `--trusted-proxies`, or
every client shares the proxy's count.

The DN is normalized for spacing, case and escapes
(`UID=Alice, ou=People,...` and `uid=\61lice,ou=people,...` count as
`uid=alice,ou=people,...`), and the server is its host and port (`ldap://Dc1`
is `ldap://dc1:389`). Attribute OIDs, Active Directory's other names for one
account (UPN, `DOMAIN\user`, DN) and a server's IP address still count
apart, and a saved connection can use any of them: where guessing passwords
matters, run `ldaplens server` with profiles that each name one form. The
counts are in memory, per process, for at most 10,000 DNs and 10,000
addresses.

### Busy, timeouts and shutdown

At most 64 API requests run at once, and 4 per session; a request that waits
more than 10 seconds gets 503 `busy` with `Retry-After: 1`, which the UI
retries for reads (3 times) and never for writes. A running request has 2
minutes before 503 `timeout`. An Apply has 4 minutes and stops between
entries: the entries before the stop stay applied, and the 200 response
lists them and names the next one as failed with `timeout` (`canceled` when
the browser went away).

The probes `GET /healthz` and `GET /readyz` answer `ok` without the token or
the Host check, at the root and under `--base-path`.

On SIGTERM or Ctrl-C the server stops accepting connections and waits up to
25 seconds for running requests, then logs `shutdown drain timed out` and
exits 0; a second Ctrl-C exits at once. That fits a pod's default 30-second
grace period.

### What is read when

`connections.yaml`, the `passwordEnv` variables, the token and every flag
are read at startup: restart after changing them. `passwordFile` and
`caFile` are read at every dial, so a rotated Secret is used at the next
connect, or for a shared profile once its connection has closed. Mount
ConfigMaps and Secrets as directories, not with `subPath`, which never sees
an update.
