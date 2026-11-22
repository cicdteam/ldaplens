# Developing ldaplens

Building, testing and the Samba AD stand-in for Active Directory.

## Development

`make check` runs Go fmt, vet, lint, vuln and unit tests plus the web checks
(`make web-check`: typecheck, ESLint, Prettier, Vitest, npm audit of the
production dependencies).
`make test-integration` needs Docker; it runs OpenLDAP and Samba AD, and
OpenDJ as a plain user with rights to edit groups (no `unindexed-search`
privilege, with its `isMemberOf`): membership, paged listing, search, entry
and schema reads, group edits, adds, renames and deletes. `make
web` builds the UI into `web/dist`, which the Go binary embeds (`make build`
does both).

`make e2e` runs the Playwright suite against a fresh OpenLDAP and Samba AD
(Docker) and the built binary, in local mode on 127.0.0.1:18091 and in server
mode on 127.0.0.1:18092, using the locally installed Google Chrome; it is not
part of `make check`.

Both suites (`make test-integration` and `make e2e`) reach Samba AD
(`smblds/smblds`) over LDAPS with the test CA in `deploy/ad-test/tls/` (test
fixtures, not secrets; the CA key was discarded).

UI development with hot reload:

```sh
docker compose -f deploy/docker-compose.yml up -d --build openldap
DEMO_LDAP_PASSWORD=adminpassword go run ./cmd/ldaplens --config deploy/connections.dev.yaml
cd web && npm ci && npm run dev   # http://localhost:5173, /api is proxied to :8080
```

Open the login link that ldaplens logs once: browsers send its cookie to
every port of `localhost`, so the Vite server on 5173 then works too.

The demo directory (`deploy/demo-ldif/gen.sh`) has 250 people, nested groups
with a cycle, DNs with escaped commas and non-ASCII characters, photos on the
first 20 people, a certificate on `user001`, and names and descriptions with
language options on the first five. The AD demo, under `OU=Demo`, has 50
people with titles and departments (001-040 enabled with `Demo-Passw0rd!`,
041-045 disabled, 046-050 without a password), nested groups (`user001` is in
Everyone via Staff via Engineering), and All Bulk, a group of 1600 users,
above AD's 1500-value range limit.

### Images and the demo

`make image` builds `ldaplens:dev`, to run in place of the published image.

`make image-multiarch` builds `ldaplens:dev-amd64` and `ldaplens:dev-arm64` with
buildx and checks each (architecture, unpacked size under 40 MiB, `--version`,
and, with the hardening flags, `/healthz` of server mode and the login link of
local mode with a volume); set `IMAGE` to tag them for a registry. On a host without amd64
(or arm64) emulation, pass `PLATFORMS=linux/arm64` (or `linux/amd64`) to check
one platform.

Demo with a sample OpenLDAP and Samba AD, in local mode: `make demo-start`, then open
<http://127.0.0.1:8080/?token=demo-token-not-secret>. It has three profiles:
`demo` (editable) and `demo-readonly` (read-only) on OpenLDAP, and `demo-ad` (Samba AD over LDAPS, editable, bound as Administrator; demo
people 001-040 bind with `Demo-Passw0rd!`, 041-045 are disabled). The
directories reset on every start. `make demo-stop` stops it; `make demo-start DEMO_PORT=8090` picks another port.
`make help` lists all targets.

### Samba AD vs Active Directory

The AD tests run against Samba, which differs from Windows AD in ways they
cannot cover:

- Samba returns all values of a plain read without ranging. The follow-up
  range requests are tested against Samba from a ranged first request, but a
  range the server forces on a plain read is covered by unit tests only.
- No Kerberos or NTLM binds; ldaplens uses simple binds only.
- Password policy errors carry the same `0000052D` code, but the reason after
  it may be worded differently.
- No global catalog.
