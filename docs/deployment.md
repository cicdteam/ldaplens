# Deployment

Docker, running ldaplens for a team, reverse proxies, Kubernetes and systemd.

## Run with Docker

Release images for amd64 and arm64 are published as
`ghcr.io/cicdteam/ldaplens:<version>` (for example `1.0.0`). `:latest` follows
the newest final release; a pre-release such as `1.2.0-rc.1` gets only its
version tag.
The image listens on `0.0.0.0:8080` and runs `server` when given no arguments.

The quickest start runs local mode with its data in a volume:

```sh
docker run --rm -p 127.0.0.1:8080:8080 -v ldaplens:/data ghcr.io/cicdteam/ldaplens local
```

Open the login link from the log, then add your directories with **New
connection**. The volume keeps them and the token; without it, both are lost
when the container is removed. Set `LDAPLENS_AUTH_TOKEN` to choose the token
(at least 16 characters from `A-Z a-z 0-9 . _ ~ -`). The image runs as UID
65532, so a bind mount used in place of the volume must be owned by it
(`chown 65532:65532 <dir>`), and `--user` needs a directory that user owns.

Connections come from two places, and you can use either or both: the ones
you create in the UI (kept in the data dir, passwords never stored) and the
fixed ones in a [`connections.yaml`](configuration.md#connectionsyaml) you mount. A local run
that also loads a `connections.yaml`:

```sh
docker run --rm -p 127.0.0.1:8080:8080 \
  -v ldaplens:/data \
  -v "$PWD/connections.yaml:/etc/ldaplens/connections.yaml:ro" \
  -e LDAPLENS_CONFIG=/etc/ldaplens/connections.yaml \
  ghcr.io/cicdteam/ldaplens local
```

With no arguments, the image runs `server`, which needs `LDAPLENS_CONFIG`
(or `--config`) and writes nothing, so it runs with a read-only root
filesystem and no volume. Arguments replace that default, so start them with
`server` or `local`: `--config=...` alone runs local mode.

Both modes also work hardened, with these flags added to `docker run`:
`--read-only --cap-drop ALL --security-opt no-new-privileges:true` (local
mode writes only to `/data`).

To run your own build instead, `make image` builds `ldaplens:dev`; use that name
in place of the published one.

The UI's hashed assets are served gzipped and cached for a year, so no ingress compression or caching setup is needed.
`/api` responses are never cached (`Cache-Control: no-store`), and the UI refuses to be framed by other pages.

`make image-multiarch` builds `ldaplens:dev-amd64` and `ldaplens:dev-arm64` with
buildx and checks each (architecture, unpacked size under 40 MiB, `--version`,
and, with the flags above, `/healthz` of server mode and the login link of
local mode with a volume); set `IMAGE` to tag them for a registry. On a host without amd64
(or arm64) emulation, pass `PLATFORMS=linux/arm64` (or `linux/amd64`) to check
one platform.

Demo with a sample OpenLDAP and Samba AD, in local mode: `make demo-start`, then open
<http://127.0.0.1:8080/?token=demo-token-not-secret>. It has two profiles on the
OpenLDAP directory, `demo` (editable) and `demo-readonly` (read-only),
and `demo-ad` (Samba AD over LDAPS, editable, bound as Administrator; demo
people 001-040 bind with `Demo-Passw0rd!`, 041-045 are disabled). The
directories reset on every start. `make demo-stop` stops it; `make demo-start DEMO_PORT=8090` picks another port.
`make help` lists all targets.

### Verify the image

Release images are built only from `v*` tags on `main`, and each carries a
signed build provenance (a GitHub artifact attestation) and an SBOM. Tags can
be moved, digests cannot, so resolve the tag to its digest once, check with
the [GitHub CLI](https://cli.github.com) that this digest came from this
repository's workflow and release tag, and run that digest:

```sh
digest=$(docker buildx imagetools inspect ghcr.io/cicdteam/ldaplens:<version> --format '{{print .Manifest.Digest}}')
gh attestation verify "oci://ghcr.io/cicdteam/ldaplens@$digest" \
  --repo cicdteam/ldaplens \
  --signer-workflow cicdteam/ldaplens/.github/workflows/ci.yml \
  --source-ref refs/tags/v<version> &&
  docker run --rm -p 127.0.0.1:8080:8080 -v ldaplens:/data "ghcr.io/cicdteam/ldaplens@$digest" local
```

In Kubernetes, set the chart's `image.digest` to the same `sha256:...`, and
verify the chart itself as in [Verify the chart](#verify-the-chart). The SBOM
(SPDX) is attached to the image:

```sh
docker buildx imagetools inspect "ghcr.io/cicdteam/ldaplens@$digest" --format '{{json .SBOM}}'
```
## Running for a team

For a team, run `ldaplens server`, for example in Kubernetes (see
[Kubernetes](#kubernetes) for the Helm chart). It serves only the profiles in
`connections.yaml`, keeps nothing on disk and runs as several replicas
behind one ingress. Server mode is built and tested for the OpenLDAP
family: OpenLDAP and directories like it, such as 389 Directory Server.
Active Directory is supported in local mode: its admin setups, such as
Protected Users and tiered admin accounts, do not fit a shared web
service.

- **Per-user login** is the recommended setup: give every profile a
  `bindDNTemplate` (see [Per-user login](#per-user-login)). Everyone then
  works as themselves, no service account password is stored in the
  deployment, and server mode needs no token. The UI offers no saving, and a
  save fails with 403 `saving_disabled`; connect opens only the profiles in
  `connections.yaml`, so nobody can make the server open connections to
  other hosts and ports it reaches, or change a profile so that the next
  person's password goes to a server of their choosing.
- **A shared profile**, one that binds with a configured password
  (`passwordFile` or `passwordEnv`) or anonymously, serves anyone who
  reaches ldaplens, so server mode then needs a token, through
  `LDAPLENS_AUTH_TOKEN_FILE` (see [Two modes](configuration.md#two-modes)). Server mode
  never makes one. The token file is read at startup: restart after
  rotating it. Run a shared profile with Docker or systemd: the Helm chart
  runs per-user profiles only.
- **Two replicas**, a rolling update that starts a new pod before it stops an
  old one (`maxSurge: 1`, `maxUnavailable: 0`) and a PodDisruptionBudget with
  `minAvailable: 1` keep the UI up through a rollout or a node drain. Each
  replica dials its own connection for a shared profile, so any replica
  answers for it (see [Flags](configuration.md#flags)), and shared profiles need no affinity.
  A per-user or typed-password connection lives in the browser's session on
  the pod where it was made, so the ingress must keep each browser on one
  pod with an affinity cookie (see [Behind a reverse
  proxy](#behind-a-reverse-proxy) for ingress-nginx and Traefik). When that
  pod goes away, in a rollout, a drain or a crash, its sessions go with it,
  and their users sign in again. A rollout replaces every pod, so after one
  every per-user and typed-password user signs in again.
- **TLS ends at the ingress**, and ldaplens speaks plain HTTP behind it. The
  ingress sends `X-Forwarded-Proto: https`, which makes the cookies Secure:
  no flag is needed. `kubectl port-forward` over plain HTTP works as is, for
  example `kubectl -n ldaplens port-forward svc/ldaplens 8080:80` and then
  `http://localhost:8080`; it reaches one pod, so it needs no affinity.
- **The client address**: set `--trusted-proxies` to the pod network where
  the ingress controller runs, for example `10.244.0.0/16` (use yours), or
  to the nodes' addresses for an ingress controller on `hostNetwork`, so
  that the logs and the per-client limit see the client behind the ingress
  (see [Behind a reverse proxy](#behind-a-reverse-proxy)). The ingress
  controller must see it too: behind a load balancer that hides it, such as
  a Service with `externalTrafficPolicy: Cluster`, clients share the nodes'
  addresses, and their wrong passwords add up.
- **The failed sign-in limits are per replica.** Each replica keeps its own
  counts (see [Flags](configuration.md#flags)) and forgets them when it restarts, and a
  client that sends no affinity cookie spreads its tries over all of them:
  with two replicas, up to 10 wrong passwords a minute for one DN and 40 from
  one address. The directory's own lockout policy is the real limit (in
  OpenLDAP, the ppolicy overlay with `pwdLockout`); the limits of ldaplens
  only slow guessing down.
- **Memory**: ldaplens uses about 10 MiB, plus about 55 MiB for each Apply of
  4 MiB of photos in flight. A large read holds up to 64 MiB of values per
  response, several times over while it is converted and encoded. Set a memory
  limit and `GOMEMLIMIT` to about 80% of it, for example 512Mi and 400MiB.
  Go does not read the container's limit by itself. `GOMEMLIMIT` makes the
  garbage collector work harder near the limit, which makes an OOM kill
  less likely, but it does not prevent one when live memory exceeds the
  limit. An OOM kill ends the sessions on that pod.
- **A notice** for everyone, such as
  `--notice "Public demo: changes are reset every 15 minutes."`, shows as a
  strip above the header on every page, the sign-in page included (with an
  access token, from the first page after the login link). To make it hard
  to miss, `--notice-color "#f59e0b"` fills the strip with that color.

What is read at startup and what at every connect, the probes and the
shutdown drain are described under [Flags](configuration.md#flags), who did what under [Logs
and accountability](operations.md#logs-and-accountability).

### Per-user login

A profile with `bindDNTemplate` (see [Signing in with your own
username](configuration.md#signing-in-with-your-own-username)) lets everyone sign in to the
directory as themselves: one profile serves the whole team, and each person
types their own username and password. It is the recommended mode for a
team. The directory's access rules apply to each person rather than to a
shared account, the directory's audit log and ldaplens's logs name the person
who made each change, and no service account password has to be stored in
the deployment.

A connect to such a profile goes like this:

1. The browser sends the profile, the username and the password.
2. ldaplens checks, in this order, that there is a username; that the
   username matches
   `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`; and that there is a password. Nothing
   reaches the directory before every check has passed. An empty password
   would be an unauthenticated bind (RFC 4513), which many servers accept as
   anonymous, so it is refused here.
3. ldaplens binds as the template with the username filled in, for example
   `uid=alice,ou=people,dc=example,dc=org`. A wrong password is 401
   `invalid_credentials`, as on any profile. Five of them in 60 seconds
   refuse that DN, and twenty from one client address refuse that address,
   with 429 `too_many_attempts` until the 60 seconds are over (see
   [Flags](configuration.md#flags)).
4. The connection's lines in the log (`connected`, `connect failed`, every
   write and `disconnected`) carry that DN as `bindDN`.

The connect dialog of such a profile has a visible Username field above the
password, marked `autocomplete="username"` so that a password manager offers
the accounts it saved; ldaplens itself does not remember the username. Once connected, the
workspace header shows `Signed in as` and the DN the connection bound as,
for example `Signed in as uid=alice,ou=people,dc=example,dc=org`, or the UPN
for a template like `{user}@corp.example`. Profiles with a fixed `bindDN`
do not show this line. Two people signed in to the same profile from two
browsers each have their own connection: one's disconnect or wrong password
does not touch the other's.

| Code | Status | When | The connect dialog says |
|---|---|---|---|
| `username_required` | 400 | no username | Enter your username. |
| `invalid_username` | 400 | the username does not match the pattern above | A username has only letters, digits, ".", "_" and "-", starts with a letter or digit and is at most 64 characters long. |
| `password_required` | 400 | no password | this profile needs a password |
| `too_many_attempts` | 429 | five wrong passwords for this DN, or twenty from this client address, in 60 seconds, counting connects that are still under way; `Retry-After` gives the seconds to wait | Too many failed sign-ins. Wait a minute and try again. |

The password a person typed stays in the server's memory, with their
connection, so that ldaplens can reconnect after a dropped connection without
asking again. It is forgotten at disconnect, when the session expires after
`--session-idle` without use, or `--session-max-age` after that connection
was connected. If the directory stops accepting it (changed or reset),
ldaplens closes the connection at the next reconnect instead of retrying, and
asks to sign in again.

### Behind a reverse proxy

Set up the proxy and ldaplens together:

- **A token**, from `LDAPLENS_AUTH_TOKEN_FILE` or `LDAPLENS_AUTH_TOKEN`.
  Local mode otherwise uses the one it keeps in its data dir, also behind a
  proxy on the same host; server mode needs one for a profile with a
  configured password or an anonymous bind (see [Two modes](configuration.md#two-modes)).
- **The Host header**: set `--allowed-hosts` to the host name users open,
  and have the proxy pass the browser's `Host` on (nginx: `proxy_set_header
  Host $host`; Traefik and ingress-nginx do so by default). Any other Host
  gets 403 `host_not_allowed`.
- **The client address**: set `--trusted-proxies` to the proxy's addresses
  and have the proxy append the client to `X-Forwarded-For` (nginx:
  `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for`; Traefik and
  ingress-nginx send it by default). For a request from a listed address,
  the client is the right-most `X-Forwarded-For` address that is not itself
  listed, or the left-most when all are; an entry that does not parse ends
  the search there. Logs carry the client as `remote` and the proxy as
  `proxy`, and the failed sign-in limit counts per client (see
  [Flags](configuration.md#flags)). Without it, every client has the proxy's address: their
  wrong passwords add up to one count, and twenty in a minute refuse
  everyone for the rest of it. List only proxies: whatever reaches ldaplens
  from a listed address can name any client.
- **Secure cookies**: ldaplens marks its cookies `Secure` when the request
  came over TLS or its first `X-Forwarded-Proto` value is `https` (in any
  case), which ingress-nginx and Traefik send by default. nginx does not:
  add `proxy_set_header X-Forwarded-Proto $scheme`, or set
  `--secure-cookies`, which marks every cookie `Secure`, as the example below
  does. The header is taken from any client: forging it can only add
  `Secure`, which makes a browser on plain HTTP drop the cookie, never weaken
  it. Over plain HTTP, such as `kubectl port-forward`, `docker run` or the
  demo, the cookies work in every browser.
- **Body size and read timeout**: Apply, New child and Clone send up to
  8 MiB, and an Apply may run 4 minutes; ldaplens cuts off any request after
  5 minutes. nginx and ingress-nginx allow 1 MiB and wait 60 seconds by default: a
  bigger body gets 413, and a longer Apply gets 504 from the proxy. When the
  proxy closes the connection, ldaplens stops before the next entry, so some
  entries are written and the browser does not learn which.
- **Error pages**: ldaplens never answers 502 or 504 itself, since a proxy
  or CDN may put its own error page in place of those (Cloudflare does). A
  proxy that replaces other error responses hides ldaplens's messages, and
  the UI then shows only the status: `The ldaplens server did not answer
  properly (HTTP 503). Try again in a moment, or reload the page.`
- **`Origin` and `Sec-Fetch-Site`** reach ldaplens as the browser sent them.
  Its cross-origin check reads them, and a proxy that clears or rewrites
  them turns the check off.
- **A sub-path**: run ldaplens with `--base-path /ldap` and keep the prefix at
  the proxy: no `rewrite-target` (ingress-nginx), `StripPrefix` (Traefik)
  or URI after the address in `proxy_pass` (nginx). A prefix that only one
  side knows serves a blank page.
- **Only the app**: the probes `/healthz` and `/readyz` skip the token and
  the Host check, for the kubelet. The nginx and Traefik examples do not
  route them; an Ingress cannot leave a path out, and through it they tell
  only that an ldaplens runs there (they answer `ok`).
- **Only through the proxy**: listen on loopback
  (`LDAPLENS_LISTEN=127.0.0.1:8080`) with the proxy on the same host, or let
  only the ingress controller in with a NetworkPolicy (see
  [Kubernetes](#kubernetes)). Otherwise a client that reaches the port
  skips the proxy and what it checks, such as a single sign-on.
- **The login link** (`/?token=...`) is meant for a local quick start:
  through a proxy, the token lands in the proxy's access log, and it does
  not expire. Before you give people the link, keep the query string out of
  the access log (the nginx example does), or have an authenticating proxy
  add `Authorization: Bearer <token>` to the requests it lets through, so
  that nobody handles the token. With a token set, ldaplens prints no link.

nginx, terminating TLS, with ldaplens on `127.0.0.1:8080`
(`LDAPLENS_ALLOWED_HOSTS=ldaplens.corp.example`, `LDAPLENS_SECURE_COOKIES=true`,
`LDAPLENS_TRUSTED_PROXIES=127.0.0.1` and a token):

```nginx
# The query string holds the login token, DNs and filters: log the path only.
log_format ldaplens '$remote_addr - [$time_local] "$request_method $uri $server_protocol" '
                  '$status $body_bytes_sent';

server {
    listen 443 ssl;
    server_name ldaplens.corp.example;
    ssl_certificate     /etc/ssl/ldaplens.pem;
    ssl_certificate_key /etc/ssl/ldaplens.key;
    access_log /var/log/nginx/ldaplens.log ldaplens;

    client_max_body_size 8m;

    location / {
        proxy_pass http://127.0.0.1:8080;   # no URI after the address: keeps the path
        proxy_set_header Host $host;        # not the default $proxy_host
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_read_timeout 300s;
        # Leave Origin and Sec-Fetch-Site alone, and do not cache.
    }
    location ~ ^/(healthz|readyz)$ {
        return 404;
    }
}
```

Traefik (file provider) keeps the Host header, sends `X-Forwarded-For` and
sets no body size limit or backend timeout by default; run ldaplens as for
nginx, with `LDAPLENS_TRUSTED_PROXIES=127.0.0.1`:

```yaml
http:
  routers:
    ldaplens:
      rule: "Host(`ldaplens.corp.example`) && !Path(`/healthz`) && !Path(`/readyz`)"
      entryPoints: [websecure]
      service: ldaplens
      tls: {}
  services:
    ldaplens:
      loadBalancer:
        servers:
          - url: "http://127.0.0.1:8080"
```

For a sub-path, match `PathPrefix(`/ldap`)` without a `StripPrefix`
middleware and run ldaplens with `--base-path /ldap`.

ingress-nginx, for the Service of the chart in [Kubernetes](#kubernetes),
which makes this Ingress with `ingress.enabled: true`:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: ldaplens
  namespace: ldaplens
  annotations:
    # Apply, New child and Clone send up to 8 MiB; the default is 1m.
    nginx.ingress.kubernetes.io/proxy-body-size: 8m
    # An Apply may run 4 minutes; the default is 60 seconds.
    nginx.ingress.kubernetes.io/proxy-read-timeout: "300"
    # No rewrite-target. For a sub-path, set LDAPLENS_BASE_PATH to it.
    # Keeps each browser on the pod that holds its per-user or typed-password
    # connections; persistent: a scale-up, as in a rollout, moves nobody.
    nginx.ingress.kubernetes.io/affinity: cookie
    nginx.ingress.kubernetes.io/affinity-mode: persistent
spec:
  ingressClassName: nginx
  tls:
    - hosts: [ldaplens.corp.example]
      secretName: ldaplens-tls
  rules:
    - host: ldaplens.corp.example
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service: {name: ldaplens, port: {name: http}}
```

Traefik as the ingress controller sets no body size limit or backend timeout
by default, so its Ingress needs no annotations. Its affinity cookie is set
on the Service instead: with the chart in [Kubernetes](#kubernetes), set
`ingress.className: traefik`, `networkPolicy.from` to Traefik's pods, and
this in `values.yaml`:

```yaml
service:
  annotations:
    traefik.ingress.kubernetes.io/service.sticky.cookie: "true"
```

A Traefik IngressRoute takes `sticky: {cookie: {}}` on its service.

### Kubernetes

The Helm chart `oci://ghcr.io/cicdteam/charts/ldaplens` runs `ldaplens
server` in two replicas under the `restricted` Pod Security Standard, with a
NetworkPolicy that lets in only the ingress controller. It runs per-user
profiles only and holds no Secret, password or token; run a shared profile
with Docker or systemd. [Its README](../deploy/helm/ldaplens/README.md), also
shown by `helm show readme`, lists every value, what the chart refuses and
how to set it up for another ingress controller.

Create the namespace and label it for `restricted`:

```sh
kubectl create namespace ldaplens
kubectl label namespace ldaplens \
  pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/enforce-version=latest
```

A `values.yaml` for one per-user profile over LDAPS, behind ingress-nginx:

```yaml
# The host name users open; ldaplens answers 403 to any other Host.
host: ldaplens.corp.example
# The pod network, where the ingress controller runs, so that its
# X-Forwarded-For names the client; it differs between clusters: use yours.
trustedProxies: [10.244.0.0/16]
connections:
  # Everyone signs in as themselves: no token, no stored password.
  - name: people
    url: ldaps://ldap.corp.example:636
    baseDN: dc=corp,dc=example
    bindDNTemplate: uid={user},ou=people,dc=corp,dc=example
    caFile: corp-ca.pem # a key of caCerts
caCerts:
  corp-ca.pem: |
    -----BEGIN CERTIFICATE-----
    (the directory's CA certificate)
    -----END CERTIFICATE-----
# The Ingress, with the affinity cookie, body size and read timeout of
# ingress-nginx set (see Behind a reverse proxy).
ingress:
  enabled: true
  tls:
    - hosts: [ldaplens.corp.example]
      secretName: ldaplens-tls
```

Install it:

```sh
helm install ldaplens oci://ghcr.io/cicdteam/charts/ldaplens --version 0.2.0 -n ldaplens -f values.yaml
```

ingress-nginx logs the query string by default, and it holds DNs and search
filters: log the path only with its `log-format-upstream` (`$uri` in place
of `$request`).

#### Verify the chart

Like the image, each published chart carries a signed build provenance.
`helm show chart` prints the chart's digest; check with the [GitHub
CLI](https://cli.github.com) that it came from this repository's workflow,
and install that digest:

```sh
digest=$(helm show chart oci://ghcr.io/cicdteam/charts/ldaplens --version 0.2.0 2>&1 >/dev/null | sed -n 's/^Digest: //p')
gh attestation verify "oci://ghcr.io/cicdteam/charts/ldaplens@$digest" \
  --repo cicdteam/ldaplens \
  --signer-workflow cicdteam/ldaplens/.github/workflows/ci.yml &&
  helm install ldaplens "oci://ghcr.io/cicdteam/charts/ldaplens@$digest" -n ldaplens -f values.yaml
```

### systemd

On a Linux server, run ldaplens as a service on loopback behind a reverse
proxy on the same host, such as the nginx example above. The binary is
static: `make build` builds `bin/ldaplens`, or copy `/ldaplens` out of the
image. The unit runs it in local mode as a dynamic user in a sandbox, with
saved connections in `/var/lib/ldaplens` (0700); for fixed profiles only, run
server mode: put `server` before `--config` in `ExecStart` and drop the
`LDAPLENS_DATA_DIR` line.

```ini
# /etc/systemd/system/ldaplens.service
# Setup:
#   install -m 0755 ldaplens /usr/local/bin/ldaplens
#   groupadd --system ldaplens-secrets
#   install -d -m 0755 /etc/ldaplens
#   install -m 0644 connections.yaml /etc/ldaplens/      # no secrets in it
#   install -m 0600 ldaplens.env /etc/ldaplens/            # root only; systemd reads it
#   install -d -m 0750 -g ldaplens-secrets /etc/ldaplens/secrets
#   install -m 0640 -g ldaplens-secrets corp-ad /etc/ldaplens/secrets/   # passwordFile
[Unit]
Description=ldaplens, a web UI for LDAP directories
Documentation=https://github.com/cicdteam/ldaplens
Wants=network-online.target
After=network-online.target

[Service]
Type=exec
ExecStart=/usr/local/bin/ldaplens --config=/etc/ldaplens/connections.yaml
Environment=LDAPLENS_LISTEN=127.0.0.1:8080
Environment=LDAPLENS_DATA_DIR=%S/ldaplens
Environment=LDAPLENS_SECURE_COOKIES=true
# The reverse proxy on this host, whose X-Forwarded-For names the client.
Environment=LDAPLENS_TRUSTED_PROXIES=127.0.0.1
# About 80% of MemoryMax: the Go runtime does not read MemoryMax by itself.
Environment=GOMEMLIMIT=400MiB
# LDAPLENS_AUTH_TOKEN, LDAPLENS_ALLOWED_HOSTS and the passwordEnv variables.
EnvironmentFile=/etc/ldaplens/ldaplens.env
# Lets the service read the passwordFile files under /etc/ldaplens/secrets.
SupplementaryGroups=ldaplens-secrets

DynamicUser=yes
StateDirectory=ldaplens
StateDirectoryMode=0700
UMask=0077
Restart=on-failure
RestartSec=2s
# ldaplens waits up to 25 s for running requests after SIGTERM.
TimeoutStopSec=30s
MemoryMax=512M
TasksMax=256
# Set here: the syscall filter below stops Go from raising the soft limit.
LimitNOFILE=8192

NoNewPrivileges=yes
CapabilityBoundingSet=
AmbientCapabilities=
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
PrivateUsers=yes
PrivateIPC=yes
ProtectHostname=yes
ProtectClock=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectProc=invisible
ProcSubset=pid
RestrictAddressFamilies=AF_INET AF_INET6
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
RemoveIPC=yes
DevicePolicy=closed
KeyringMode=private
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallFilter=~@privileged @resources
SystemCallErrorNumber=EPERM

[Install]
WantedBy=multi-user.target
```

`/etc/ldaplens/ldaplens.env` (root, 0600):

```sh
LDAPLENS_AUTH_TOKEN=<openssl rand -hex 32>
LDAPLENS_ALLOWED_HOSTS=ldaplens.corp.example
```

Without the token, ldaplens keeps one in `/var/lib/ldaplens/token`, and
journald keeps the login link it logs at every start. Passwords for
`passwordFile` profiles go in `/etc/ldaplens/secrets/`
(`root:ldaplens-secrets`, 0640).
