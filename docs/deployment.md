# Deployment

The binary, Docker, running ldaplens for a team, reverse proxies, Kubernetes and systemd.

## Run the binary

Each release has static binaries for Linux (amd64, arm64) and macOS (Apple
silicon), as `ldaplens_Linux_x86_64.tar.gz`, `ldaplens_Linux_aarch64.tar.gz`
and `ldaplens_Darwin_arm64.tar.gz` (named as `uname -s` and `uname -m`
print), each with `LICENSE` and `README.md`,
and their SHA-256 sums in `ldaplens_checksums.txt`. The names carry no
version, so `releases/latest/download/<name>` always gives the newest
release, and `releases/download/v<version>/<name>` a given one:

```sh
curl -fsSL "https://github.com/cicdteam/ldaplens/releases/latest/download/ldaplens_$(uname -s)_$(uname -m).tar.gz" | tar -xz ldaplens
./ldaplens
```

There is no build for Intel Macs or Windows: there `curl` fails with 404;
use the [Docker image](#run-with-docker).

With no arguments it runs `local`, on `127.0.0.1:8080` (`--listen` changes
it), and keeps the saved connections and the token in the user config
directory (`~/.config/ldaplens` on Linux, `~/Library/Application
Support/ldaplens` on macOS; `--data-dir` changes it). On a desktop it opens
its login link in the browser; over SSH, or with `--no-browser`, it prints
the link to open. The link carries the token, which is in the command line
of `open` or `xdg-open` while that starts the browser. `./ldaplens server --config connections.yaml` runs server mode.

The macOS binary is not signed by Apple. `curl` leaves it runnable; one
downloaded with a browser is held in quarantine, and macOS refuses to open
it until you clear that:

```sh
xattr -d com.apple.quarantine ldaplens
```

### Verify the binary

Each archive carries a signed build provenance, like the image. Check it with
the [GitHub CLI](https://cli.github.com) before you unpack it:

```sh
curl -fsSLO "https://github.com/cicdteam/ldaplens/releases/latest/download/ldaplens_$(uname -s)_$(uname -m).tar.gz"
gh attestation verify "ldaplens_$(uname -s)_$(uname -m).tar.gz" \
  --repo cicdteam/ldaplens \
  --signer-workflow cicdteam/ldaplens/.github/workflows/ci.yml &&
  tar -xzf "ldaplens_$(uname -s)_$(uname -m).tar.gz" ldaplens
```

Add `--source-ref refs/tags/v<version>` to pin the release, or compare the
archive with `ldaplens_checksums.txt` (`sha256sum -c --ignore-missing
ldaplens_checksums.txt`, `shasum -a 256 -c` on macOS).

## Run with Docker

Release images for amd64 and arm64 are published as
`ghcr.io/cicdteam/ldaplens:<version>`; `:latest` follows the newest final
release. The image listens on `0.0.0.0:8080` and runs as UID 65532.

Local mode with its data (saved connections and the token) in a volume:

```sh
docker run --rm -p 127.0.0.1:8080:8080 -v ldaplens:/data ghcr.io/cicdteam/ldaplens local
```

Open the login link from the log, then add your directories with **New
connection**. Without the volume, the connections and the token are lost
with the container; a bind mount in its place must be owned by 65532
(`chown 65532:65532 <dir>`). `LDAPLENS_AUTH_TOKEN` sets the token (at least
16 characters from `A-Z a-z 0-9 . _ ~ -`).

Fixed profiles come from a [`connections.yaml`](configuration.md#connectionsyaml)
you mount; local mode shows them next to the saved ones:

```sh
docker run --rm -p 127.0.0.1:8080:8080 \
  -v ldaplens:/data \
  -v "$PWD/connections.yaml:/etc/ldaplens/connections.yaml:ro" \
  -e LDAPLENS_CONFIG=/etc/ldaplens/connections.yaml \
  ghcr.io/cicdteam/ldaplens local
```

With no arguments the image runs `server`, which needs `LDAPLENS_CONFIG` and
writes nothing. Arguments replace that default, so start them with `server`
or `local`. Both modes run hardened with `--read-only --cap-drop ALL
--security-opt no-new-privileges:true` added (local mode writes only to
`/data`).

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

For a team, run `ldaplens server`, for example in Kubernetes with the
[Helm chart](#kubernetes). It serves only the profiles in `connections.yaml`,
keeps nothing on disk and runs as several replicas behind one ingress.
Server mode is built and tested for the OpenLDAP family (OpenLDAP, 389
Directory Server and the like). Active Directory is supported in local mode:
its admin setups, such as Protected Users and tiered admin accounts, do not
fit a shared web service.

- **Per-user login** is the recommended setup: give every profile a
  `bindDNTemplate` ([Per-user login](configuration.md#per-user-login)).
  Everyone works as themselves, no service account password is stored, and
  server mode needs no token. Nobody can save a profile or point ldaplens at
  another host: connect opens only the profiles in `connections.yaml`.
- **A shared profile**, one that binds with a configured password
  (`passwordFile`, `passwordEnv`) or anonymously, serves anyone who reaches
  ldaplens, so server mode then needs a token from `LDAPLENS_AUTH_TOKEN_FILE`
  ([Two modes](configuration.md#two-modes)), read at startup. Run it with
  Docker or systemd: the Helm chart runs per-user profiles only.
- **Replicas and sessions**: two replicas, a rolling update that starts a
  new pod first and a PodDisruptionBudget keep the UI up through a rollout
  or a drain. A shared profile works on any replica. A per-user or
  typed-password connection lives on the pod where it was made, so the
  ingress needs an affinity cookie ([Behind a reverse
  proxy](#behind-a-reverse-proxy)); when that pod goes away, in a rollout
  too, its users sign in again.
- **TLS ends at the ingress**, and ldaplens speaks plain HTTP behind it. Set
  `--trusted-proxies` to the ingress controller's pod network so that logs
  and limits see the real client ([Behind a reverse
  proxy](#behind-a-reverse-proxy)).
- **Failed sign-in limits are per replica** and reset on restart ([Flags](configuration.md#flags)):
  they only slow guessing down. The directory's own lockout policy (in
  OpenLDAP, ppolicy with `pwdLockout`) is the real limit.
- **Memory**: about 10 MiB idle, plus about 55 MiB for each Apply of 4 MiB
  of photos in flight; a large read holds up to 64 MiB of values. Set a
  memory limit and `GOMEMLIMIT` to about 80% of it, for example 512Mi and
  400MiB: Go does not read the container's limit by itself.

What is read at startup and at every connect, the probes and the shutdown
drain are under [Flags](configuration.md#flags); who did what is under [Logs
and accountability](operations.md#logs-and-accountability).

### Behind a reverse proxy

Set up the proxy and ldaplens together:

- **A token**, from `LDAPLENS_AUTH_TOKEN_FILE` or `LDAPLENS_AUTH_TOKEN`, for
  local mode and for server mode with a shared profile ([Two
  modes](configuration.md#two-modes)).
- **The Host header**: set `--allowed-hosts` to the host name users open and
  have the proxy pass the browser's `Host` on (nginx: `proxy_set_header Host
  $host`; Traefik and ingress-nginx do by default). Any other Host gets 403
  `host_not_allowed`.
- **The client address**: set `--trusted-proxies` to the proxy's addresses
  and have the proxy append the client to `X-Forwarded-For` (Traefik and
  ingress-nginx do by default). Logs then carry the client as `remote` and
  the proxy as `proxy`, and the failed sign-in limit counts per client.
  Without it every client has the proxy's address, and twenty wrong
  passwords in a minute from anyone refuse everyone. List only proxies:
  whatever comes from a listed address can name any client.
- **Secure cookies**: cookies are `Secure` when the request came over TLS or
  `X-Forwarded-Proto` says `https`, which ingress-nginx and Traefik send.
  nginx does not: add `proxy_set_header X-Forwarded-Proto $scheme` or set
  `--secure-cookies`.
- **Body size and read timeout**: Apply, New child and Clone send up to
  8 MiB, and an Apply may run 4 minutes. nginx and ingress-nginx allow 1 MiB
  and 60 seconds by default. A proxy that cuts an Apply short leaves some
  entries written, and the browser does not learn which.
- **Error pages**: ldaplens never answers 502 or 504 itself, since a proxy
  or CDN may put its own page in their place (Cloudflare does). A proxy that
  replaces other error responses hides ldaplens's messages, and the UI then
  shows only the status.
- **`Origin` and `Sec-Fetch-Site`** must reach ldaplens as the browser sent
  them: its cross-origin check reads them.
- **A sub-path**: run ldaplens with `--base-path /ldap` and keep the prefix
  at the proxy (no `rewrite-target`, `StripPrefix` or URI after the address
  in `proxy_pass`).
- **Only the app**: the probes `/healthz` and `/readyz` skip the token and
  the Host check, for the kubelet; the examples below do not route them.
- **Only through the proxy**: listen on loopback
  (`LDAPLENS_LISTEN=127.0.0.1:8080`), or let only the ingress controller in
  with a NetworkPolicy ([Kubernetes](#kubernetes)).
- **The login link** (`/?token=...`) is for a local quick start: through a
  proxy the token lands in the access log and never expires. Keep the query
  string out of the log (the nginx example does), or have an authenticating
  proxy add `Authorization: Bearer <token>`.

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

In Kubernetes, the [chart](#kubernetes)'s Ingress (`ingress.enabled: true`)
sets the ingress-nginx annotations for all of this: an 8 MiB body, a
300-second read timeout and a persistent affinity cookie.

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
proxy on the same host, such as the nginx example above, with the
[release binary](#run-the-binary) (or `bin/ldaplens` from `make build`). The unit runs it in local mode as a dynamic user in a sandbox, with
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
