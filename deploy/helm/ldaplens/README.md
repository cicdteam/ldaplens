# ldaplens Helm chart

[ldaplens](https://github.com/cicdteam/ldaplens) is a web UI for browsing
and editing LDAP directories. This chart runs it as `ldaplens server` for
directories of the OpenLDAP family, with per-user sign-in only: every
profile has a `bindDNTemplate`, everyone signs in with their own username
and password, and the chart holds no Secret, password or token. For a
shared profile, one that binds with a configured password or anonymously,
run ldaplens with [Docker](https://github.com/cicdteam/ldaplens/blob/main/docs/deployment.md#run-with-docker)
or [systemd](https://github.com/cicdteam/ldaplens/blob/main/docs/deployment.md#systemd) instead.

The chart installs:

- a Deployment of two pods under the `restricted` Pod Security Standard
  (non-root, read-only root filesystem, all capabilities dropped,
  `seccompProfile: RuntimeDefault`, no service account token), which
  starts a new pod before it stops an old one;
- a ConfigMap with `connections.yaml` and the CA certificates;
- a Service, port 80, named `http`;
- a PodDisruptionBudget and a NetworkPolicy that lets in only the ingress
  controller;
- an Ingress, with `ingress.enabled: true`.

Server mode writes nothing, so there is no volume.

## Prerequisites

- A namespace labeled for the `restricted` Pod Security Standard:

  ```sh
  kubectl create namespace ldaplens
  kubectl label namespace ldaplens \
    pod-security.kubernetes.io/enforce=restricted \
    pod-security.kubernetes.io/enforce-version=latest
  ```

- An ingress controller with cookie affinity: a per-user connection lives
  in the pod where it was made. The defaults are for ingress-nginx in the
  namespace `ingress-nginx`; for another controller, see [Ingress
  controller](#ingress-controller).
- The LDAP server reachable from the pods. The chart's NetworkPolicy limits
  only what comes in; a policy of your own that limits egress must let the
  pods reach the directory. A directory certificate from a private CA needs
  that CA in `caCerts`.

## Install

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
ingress:
  enabled: true
  tls:
    - hosts: [ldaplens.corp.example]
      secretName: ldaplens-tls
```

```sh
helm install ldaplens oci://ghcr.io/cicdteam/charts/ldaplens --version 0.1.0 -n ldaplens -f values.yaml
```

The notes printed after the install give the URL to open. `helm show
values` lists every value with its comment, and `helm template` prints the
manifests as plain YAML, for those who apply them another way.

**Upgrade**: `helm upgrade` with the same arguments applies a change.
ldaplens reads `connections.yaml` at startup, so a change to `connections`
or `caCerts` rolls the pods. Per-user sessions end with their pod: after a
rollout, everyone signs in again.

**Pin the image by digest** in production: set `image.digest` to the
`sha256:...` from [Verify the
image](https://github.com/cicdteam/ldaplens/blob/main/docs/deployment.md#verify-the-image); it wins over
`image.tag`.

### Verify the chart

Like the image, each published chart carries a signed build provenance.
`helm show chart` prints the chart's digest; check with the [GitHub
CLI](https://cli.github.com) that it came from the ldaplens repository's
workflow, and install that digest:

```sh
digest=$(helm show chart oci://ghcr.io/cicdteam/charts/ldaplens --version 0.1.0 2>&1 >/dev/null | sed -n 's/^Digest: //p')
gh attestation verify "oci://ghcr.io/cicdteam/charts/ldaplens@$digest" \
  --repo cicdteam/ldaplens \
  --signer-workflow cicdteam/ldaplens/.github/workflows/ci.yml &&
  helm install ldaplens "oci://ghcr.io/cicdteam/charts/ldaplens@$digest" -n ldaplens -f values.yaml
```

## Values

| Key | Default | Description |
|---|---|---|
| `image.repository` | `ghcr.io/cicdteam/ldaplens` | The image. |
| `image.tag` | `""` | Empty means the chart's appVersion. |
| `image.digest` | `""` | `sha256:...`, which wins over the tag. |
| `image.pullPolicy` | `IfNotPresent` | The image pull policy. |
| `imagePullSecrets` | `[]` | Secrets for a private registry, such as `[{name: corp-registry}]`. |
| `replicaCount` | `2` | The number of pods. Per-user sessions live in the pod that holds them; the Ingress affinity cookie keeps a browser on one pod. |
| `host` | `""` | Required. The host name users open (`--allowed-hosts`); ldaplens answers 403 to any other Host, except localhost (`kubectl port-forward`). Also the Ingress host. |
| `basePath` | `""` | A URL prefix such as `/ldap` (`--base-path`), kept by the proxy (no rewrite-target). The probes and the Ingress path follow it. |
| `trustedProxies` | `[]` | CIDRs or IPs of the proxies in front of ldaplens (`--trusted-proxies`). Required with `ingress.enabled`, see [trustedProxies](#trustedproxies). |
| `notice` | `""` | Plain text shown above the header on every page (`--notice`), at most 300 characters. |
| `noticeColor` | `""` | The notice strip's background (`--notice-color`), `#rgb` or `#rrggbb`; the text turns black or white to read on it. Empty keeps the default look. |
| `session.idle` | `""` | Close LDAP connections after this much inactivity (`--session-idle`). Empty means `30m`. |
| `session.maxAge` | `""` | Close each LDAP connection this long after it was connected (`--session-max-age`). Empty means `8h`. |
| `connections` | `[]` | Required. Profiles in the [connections.yaml](https://github.com/cicdteam/ldaplens/blob/main/docs/configuration.md#connectionsyaml) shape, each with `bindDNTemplate` (see [Per-user login](https://github.com/cicdteam/ldaplens/blob/main/docs/configuration.md#per-user-login)). `caFile` names a key of `caCerts`. |
| `caCerts` | `{}` | CA certificates (PEM) by file name, for the profiles' `caFile`. |
| `goMemLimit` | `400MiB` | `GOMEMLIMIT`, about 80% of the memory limit: Go does not read the limit. Empty sets none. |
| `resources` | `{requests: {cpu: 50m, memory: 64Mi}, limits: {memory: 512Mi}}` | The container's resources. |
| `podAnnotations` | `{}` | Extra pod annotations. |
| `podLabels` | `{}` | Extra pod labels. |
| `nodeSelector` | `{}` | The pods' node selector. |
| `tolerations` | `[]` | The pods' tolerations. |
| `affinity` | `{}` | The pods' affinity. |
| `topologySpreadConstraints` | `[]` | The pods' topology spread constraints. |
| `pdb.enabled` | `true` | A PodDisruptionBudget. |
| `pdb.minAvailable` | `1` | The pods a node drain or another voluntary eviction leaves running. |
| `networkPolicy.enabled` | `true` | A NetworkPolicy that lets only `networkPolicy.from` reach the pods. The kubelet's probes come from the node, which it does not block. |
| `networkPolicy.from` | ingress-nginx, see [Ingress controller](#ingress-controller) | The peers let in. Empty is refused while the policy is enabled. |
| `service.port` | `80` | The Service port. |
| `service.annotations` | `{}` | Service annotations, such as Traefik's affinity cookie. |
| `ingress.enabled` | `false` | An Ingress for `host` and `basePath`. Needs `trustedProxies`. |
| `ingress.className` | `nginx` | The IngressClass. Empty leaves it to the cluster's default. |
| `ingress.annotations` | ingress-nginx, see [Ingress controller](#ingress-controller) | The Ingress annotations. |
| `ingress.tls` | `[]` | The Ingress TLS, such as `[{hosts: [ldaplens.corp.example], secretName: ldaplens-tls}]`. |

## Ingress controller

The defaults are for ingress-nginx:

```yaml
ingress:
  className: nginx
  annotations:
    # Apply, New child and Clone send up to 8 MiB; the default is 1m.
    nginx.ingress.kubernetes.io/proxy-body-size: 8m
    # An Apply may run 4 minutes; the default is 60 seconds.
    nginx.ingress.kubernetes.io/proxy-read-timeout: "300"
    # Keeps each browser on the pod that holds its connections; persistent:
    # a scale-up, as in a rollout, moves nobody.
    nginx.ingress.kubernetes.io/affinity: cookie
    nginx.ingress.kubernetes.io/affinity-mode: persistent
networkPolicy:
  from:
    - namespaceSelector:
        matchLabels: {kubernetes.io/metadata.name: ingress-nginx}
      podSelector:
        matchLabels: {app.kubernetes.io/name: ingress-nginx}
```

ingress-nginx logs the query string by default, and it holds DNs and search
filters: log the path only with its `log-format-upstream` (`$uri` in place
of `$request`).

Traefik sets no body size limit or backend timeout, and takes its affinity
cookie on the Service. Set `ingress.className: traefik`, `networkPolicy.from`
to Traefik's pods (adjust the labels to your install), and:

```yaml
service:
  annotations:
    traefik.ingress.kubernetes.io/service.sticky.cookie: "true"
```

Helm merges `ingress.annotations` with the defaults: set a default
annotation to `null` to drop it.

For another controller, or a route of your own, leave `ingress.enabled`
false and route `host` and `basePath` to the Service's `http` port,
keeping the path and the Host header, with cookie affinity, 8 MiB bodies
and a 300 second read timeout; set `networkPolicy.from` to the proxy's
pods. [Behind a reverse
proxy](https://github.com/cicdteam/ldaplens/blob/main/docs/deployment.md#behind-a-reverse-proxy) has
the details.

## trustedProxies

ldaplens counts wrong passwords per client address: twenty in a minute
from one address refuse every sign-in from it for the rest of the minute.
Behind an ingress, every request comes from the controller's pods. Unless
their addresses are in `trustedProxies`, ldaplens takes the controller for
the client: all users share one count, twenty wrong passwords from anyone
lock everyone out, and the logs carry the controller's address instead of
the user's. With them listed, ldaplens reads the client from
`X-Forwarded-For`. So the chart refuses `ingress.enabled` without
`trustedProxies`; without the chart's Ingress, the notes warn about an
empty one.

Set it to the pod network the ingress controller runs in, such as
`10.244.0.0/16` (it differs between clusters), or to the nodes' addresses
for a controller on `hostNetwork`. Whatever reaches ldaplens from a listed
address can name any client, so list only proxies: the NetworkPolicy keeps
the other pods of that network out. The controller must see the client
address itself: behind a load balancer that hides it, such as a Service
with `externalTrafficPolicy: Cluster`, clients share the nodes' addresses.
See [Running for a team](https://github.com/cicdteam/ldaplens/blob/main/docs/deployment.md#running-for-a-team)
and [Flags](https://github.com/cicdteam/ldaplens/blob/main/docs/configuration.md#flags) for the limits.

## What the chart refuses

`helm install`, `helm upgrade` and `helm template` fail, before anything
reaches the cluster, on:

| Values | Fix |
|---|---|
| `host` empty | Set it to the host name users open. |
| `connections` empty | Add a profile with `bindDNTemplate`. |
| A profile with `bindDN`, `password`, `passwordFile` or `passwordEnv` | Remove it and set `bindDNTemplate`. For a shared profile, run ldaplens with Docker or systemd. |
| A profile without `bindDNTemplate` | Set it, such as `uid={user},ou=people,dc=corp,dc=example`. |
| A `caFile` that is not a key of `caCerts` | Add the CA certificate as `caCerts.<caFile>`. |
| A `caCerts` key `connections.yaml`, which would replace the generated profile file | Give the certificate another name, such as `corp-ca.pem`. |
| `notice` over 300 characters, after trimming | Shorten it. |
| `ingress.enabled` with an empty `trustedProxies` | Set `trustedProxies`, see [trustedProxies](#trustedproxies). |
| `networkPolicy.enabled` with an empty `networkPolicy.from`, which would let every source in | List the ingress controller's pods, or set `networkPolicy.enabled: false`. |

The values schema also refuses a key the chart does not know, at the top
level, in a profile or in `image`, `session`, `pdb`, `networkPolicy`,
`service` and `ingress` (a typo such as `baseDn` fails), and values of the
wrong shape: a profile `url` other than `ldap://` or `ldaps://`, a
`bindDNTemplate` without `{user}`, a duration that is not like `30m` or
`1m30s`, an `image.digest` other than `sha256:` and 64 hex digits, or a
`basePath` other than a path such as `/ldap`, without a trailing `/`.
