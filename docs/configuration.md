# Configuration

The two modes, profiles (`connections.yaml` and saved ones) and every flag.

## Two modes

ldaplens runs in one of two modes, picked by its first argument:

- **`ldaplens`**, or **`ldaplens local`**, is for one person: on a laptop, a
  jump host or in a quick Docker try. It keeps the connections you save in
  the UI and a login token in a data dir: `--data-dir`, or else `ldaplens` in
  the user config dir, created with mode 0700:
  - Linux: `$XDG_CONFIG_HOME/ldaplens`, or `~/.config/ldaplens`;
  - macOS: `~/Library/Application Support/ldaplens`;
  - Windows: `%AppData%\ldaplens`;
  - the image: `/data/ldaplens`.
- **`ldaplens server`** is for a team, for example in Kubernetes. It serves
  only the profiles in `--config` and keeps nothing on disk.

Any other first argument that does not start with `-` fails with `unknown
command "x": want local or server`, and a mode after the flags fails too.
`--version` and `-h` work in both modes.

**Local mode always needs a token.** Unless `--auth-token` or
`--auth-token-file` sets one, the first start makes one and keeps it in
`token` in the data dir (mode 0600), and every start logs a login link with
it:

```text
level=INFO msg="open the UI with this login link (adjust the host and port to how you reach it)" url="http://localhost:8080/?token=..."
```

Open the link once: it sets a cookie that lasts 30 days, and the token stays
the same across restarts, so a bookmark of the UI keeps working. To change
the token, and log out every browser, delete the file and restart. Without a
user config dir (no `$HOME`, for example), there is no data dir: ldaplens
logs `saving profiles disabled (no data dir)` and makes a new token at every
start.

**Server mode needs `--config`**; without it, startup fails with `server mode
needs --config`. It refuses `--data-dir` and `LDAPLENS_DATA_DIR` with `server
mode keeps no data; --data-dir is for local mode`, and never makes a token.
Without `--auth-token` or `--auth-token-file`, every profile must ask each
person for a password: a `bindDNTemplate` profile, or a `bindDN` without
`passwordEnv` or `passwordFile`. A profile that binds with a configured
password or anonymously would serve anyone who reaches ldaplens, so it fails
startup with `profile <name> binds with a configured password or
anonymously; server mode needs --auth-token or --auth-token-file`.

**What the token is for.** The token guards ldaplens itself, before any
LDAP bind. A profile that asks each person for a password needs no guard:
the directory checks that password. A profile that binds with a configured
password or anonymously binds as the server, so without the token anyone
who reaches the URL would work as that account. The token is one secret for
the whole instance, not a per-person login: who you are, in the logs and in
the directory, comes from the bind.

**Server mode shows only what it takes to connect.** The UI learns the mode
from `GET /api/server` (`{"mode":"server"}` or `{"mode":"local"}`), which
also carries `notice` when `--notice` is set and leaves it out when it is
empty, and `noticeColor` with it when `--notice-color` is set. In server mode:

- the profile list (`GET /api/profiles`) gives each profile's `name`,
  `label`, `color`, `readOnly`, `connected`, `userLogin` and `needsPassword` only,
  nothing about where or how it binds: no URL, base DN, bind DN, CA or TLS
  settings. The server address and StartTLS show in the status bar once a
  connect has succeeded;
- the UI has no "New connection", no profile editing and no "Trust this CA
  and connect"; a save answers 403 `saving_disabled` with `this server keeps
  no saved connections`;
- the connect dialog of a profile with a bind DN and no configured password
  asks for a "Password", without naming the DN;
- a failed connect tells nothing about the directory. Every LDAP result,
  not only 49 (invalid credentials) but also, for example, 389-DS's 19 and
  53 for a locked or inactivated account, answers 401 `invalid_credentials`
  with `invalid username or password`, and no `diagnostic`, `reason`,
  `matchedDN` or `ldapResultCode`: the directory's message, and the reason
  decoded from it, such as Active Directory's `account locked out`, would
  tell anyone who tries a name that the account exists. A server that
  cannot be reached answers 503 `network` with `cannot reach the directory
  server. Try again in a moment.`, and an untrusted certificate 422 `tls_untrusted` with `the
  directory server's certificate is not trusted`, without host names, the
  error text or the certificate chain. The server log keeps the full error
  in `connect failed`.

Local mode shows all of it.
## connections.yaml

```yaml
connections:
  - name: corp-ad
    label: Corp Active Directory # optional; shown instead of the name
    url: ldaps://dc1.corp.example:636
    baseDN: dc=corp,dc=example
    bindDN: cn=reader,ou=svc,dc=corp,dc=example
    passwordEnv: CORP_AD_PASSWORD # or passwordFile, or type it in the UI
    caFile: /etc/ldaplens/corp-ca.pem
    readOnly: true
    color: "#d33"
```

`readOnly` and `color` are the only safety and marking options. A profile is
writable unless it sets `readOnly: true`: then the UI offers no Edit or
Actions and the server refuses its writes with 403 `read_only`. `color` (a
hex color such as `#d33`, or a CSS color name) puts a colored dot next to the
profile in the connection list and in the workspace header.

`name` is the profile's identifier: it is in the workspace URL (`/c/corp-ad`)
and the API, so it has only letters, digits, `.`, `_` and `-`. `label`, up to
80 characters, is what the UI shows instead: in the connection list (with
the name under it), the connect dialog, the workspace header and the command
palette.

### Signing in with your own username

A profile with `bindDNTemplate` instead of `bindDN` asks each person for a
username and password at connect, and binds as the template with `{user}`
replaced by the username. The template contains `{user}` exactly once and
cannot be combined with `bindDN`, `passwordEnv` or `passwordFile`. It is
read from `connections.yaml` only; profiles saved in the UI cannot have it.

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

The username must match `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$` (ASCII letters,
digits, `.`, `_` and `-`, starting with a letter or digit, at most 64
characters), so it cannot change the structure of the DN; in a template that
contains `=` it is also escaped as a DN value. [Per-user
login](deployment.md#per-user-login) has the whole flow and its errors.
## Saved connections

Profiles created in the UI are kept, without passwords, in `profiles.json`
in the data dir of local mode (see [Two modes](#two-modes)); without a data
dir, as in server mode, the UI cannot save profiles, and a save fails with
403 `saving_disabled`. At most 200
profiles can be saved, each at most 4 KiB as JSON not counting `caPEM`, which
has its own limit of 16 KiB; saving one more profile, or a bigger one, fails
with 409 `profile_limit`. Saves are written to a temporary file, synced and
renamed, so a crash never leaves a partial file.

On a writable data dir, a `profiles.json` that does not parse or holds an
invalid profile does not stop the server: it is moved to `profiles.json.bad`
(replacing an older one), the server starts with no saved profiles and logs
the warning `profiles file was invalid and was moved aside` with its `path`
and the parse or validation `error`. On a read-only data dir it cannot be
moved, so it is a startup error, and so is a file the server cannot read
(permissions).

Run one ldaplens process per data dir. The server locks `.lock` in it (Linux,
macOS and the BSDs), and a second process on the same directory exits with
`data dir ... is in use by another ldaplens process`. A `--data-dir` that does
not exist is a startup error; the default one is created. One that is
read-only or not writable for the server's user still starts if it holds the
token file or a token is set: the saved profiles are listed, saving fails,
and the server runs without the lock and logs the warning `data dir is not
writable; saving profiles will fail` with its `dir`. Without either, keeping
a new token there fails startup.

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

A flag beats its variable, and an empty variable counts as unset
(`LDAPLENS_DATA_DIR=""` means the default data dir). A variable that does not
parse (`LDAPLENS_SECURE_COOKIES=yes`, `LDAPLENS_SESSION_IDLE=30`) is a startup
error even when its flag is set. `--auth-token` or `--auth-token-file`
on the command line overrides both token variables. Both flags at once, or
both variables at once, are a startup error, and so is a token file that is
missing or holds an invalid token (at least 16 characters from
`A-Z a-z 0-9 . _ ~ -`); the error names the file, never the token.

A browser's open connections live in a session on the server, closed after
`--session-idle` without use. Each connection is also closed
`--session-max-age` after it was connected, however active; connecting that
profile again starts the count again, an automatic reconnect after a dropped
connection does not. Every successful connect moves the browser to a new
session ID and keeps its other connections, each with its own count, so an
ID seen before a connect is worthless after it. A request from another tab
at the same instant can find its session gone; that tab reconnects. A
session holds at most 8 connections: one more connect fails with 409
`too_many_connections` until one is disconnected. The server holds at most
1000 sessions; one more closes the connections of the least recently used.

A profile that binds with its configured password (`passwordEnv` or
`passwordFile`) or anonymously is shared: one connection in each ldaplens
process serves every browser, outside any session. Connecting to it sets no
session cookie, and every browser with the token can use it without
connecting, so the Connections page marks it connected while the process
holds it. Each process dials its own at the first request that needs it,
one dial at a time, so any replica behind a load balancer serves any
request. The connection is closed after `--session-idle` without use by
anyone, and `--session-max-age` after it was dialed; the next request dials
again. A disconnect leaves it open for the others; saving the profile in the
UI closes it, so the next request uses the new settings. A typed password,
whether the profile has none configured or the configured one failed, and a
`bindDNTemplate` profile give the browser a connection of its own in its
session, which only the process holding that session knows: those need one
replica or sticky sessions (see [Running for a team](deployment.md#running-for-a-team)).

Five wrong passwords (LDAP result 49, invalid credentials) for one bind DN
within 60 seconds refuse further connects as that DN with 429
`too_many_attempts` and a `Retry-After` header in seconds, until 60 seconds
after the first of them. Twenty wrong passwords from one client address
within 60 seconds, whatever the profiles and DNs, refuse further connects
from that address the same way, so that trying a few passwords on many
accounts is caught too. A connect must pass both limits, and a wrong
password counts against both; a connect refused for its DN does not count
against its address. Connects still waiting for the server count too, so a
burst of them at once gets no more tries than the limit: a connect refused
while those in flight fill it gets `Retry-After: 1`. A successful connect
clears the count of its DN but not that of its address, so that one valid
account cannot reset the count between guesses at others; other failures,
such as a server that cannot be reached, do not count. The DN limit is per account on
each server: it is keyed by the server and the bind DN (see below), and a
successful connect as the same DN to another server does not clear it. The
address limit is keyed by the client address the logs carry as `remote`:
behind a reverse proxy, set `--trusted-proxies` (see [Behind a reverse
proxy](deployment.md#behind-a-reverse-proxy)), or every client has the proxy's address
and shares one count. Both apply to every connect with a typed password, on a `bindDN`
profile as on a `bindDNTemplate` one. A connect with the configured password
(`passwordEnv` or `passwordFile`) and an anonymous one are never refused, so a
wrong service password cannot lock its DN for everyone. In return, anyone who
can reach ldaplens can keep one account from connecting through ldaplens, a
minute at a time; the directory's own lockout policy is separate and
unaffected. The counts are kept in memory, per process, like sessions, for at
most 10,000 bind DNs and 10,000 client addresses; past that the oldest are
dropped.

The count is per account as the bind DN names it, after normalizing spacing,
case and escapes: `uid=alice,ou=people,dc=example,dc=org`,
`UID=Alice, ou=People,dc=example,dc=org` and
`uid=\61lice,ou=people,dc=example,dc=org` share one count, and so do
`alice@corp.example` and `ALICE@corp.example`. The server is the URL's host,
ignoring case, and port, 389 for `ldap://` and 636 for `ldaps://` when the
URL has none: `ldap://Dc1` and `ldap://dc1:389` share one count. Attribute
OIDs (`0.9.2342.19200300.100.1.1=alice` for `uid=alice`), Active Directory's
other names for one account (its UPN, `DOMAIN\user` and its DN) and other
names for one server (its IP address) still count separately, and a saved
connection can name an account or a server any of those ways. So
where guessing passwords through ldaplens matters, run `ldaplens server`,
which has no saved connections, with profiles in `connections.yaml` that each
name one form.

The server runs at most 64 API requests at once, and at most 4 of one
session; the others wait their turn. A request without a session cookie,
such as one on a shared profile from a browser that has typed no password,
counts only toward the 64. A request that waits more than 10
seconds gets 503 `busy` with `Retry-After: 1`; the UI retries a read that
gets it up to 3 times, a second apart, and never retries a write. Once
running, an API request has 2 minutes before it ends with 503 `timeout`. An
Apply has 4 minutes and stops between entries instead: when its time runs
out or its browser goes away, a modify already sent to the server finishes,
the entries before the stop stay applied, and the response is a 200 that
lists them and names the next entry as failed with `timeout` (`canceled`
when the browser went away).

The probes `GET /healthz` and `GET /readyz` answer `ok` without the token or
the Host check, so a kubelet can call them by pod IP. With `--base-path
/ldap` they answer at both `/healthz` and `/ldap/healthz` (and the same for
`/readyz`).

On SIGTERM or Ctrl-C the server stops accepting connections and waits up to
25 seconds for running requests. Then it closes the rest, logs `shutdown
drain timed out` and exits 0. A second Ctrl-C exits at once. The drain fits
the 30 second default grace period of a Kubernetes pod.

`connections.yaml`, the `passwordEnv` variables, the token (also from
`--auth-token-file`) and every other flag and variable are read at startup:
restart after changing them. `passwordFile` and `caFile` are read at every
dial, so a rotated Secret is used at the next dial: at a connect, or for a
shared profile at the first request after its connection closed (idle, past
`--session-max-age`, or because the server refused the old password at a
reconnect). Mount ConfigMaps
and Secrets as directories, not with `subPath`, which never sees an update.
