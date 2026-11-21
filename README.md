# ldaplens - LDAP UI

A web UI to view, search and manage LDAP data: an LDAP browser and editor for
OpenLDAP and Active Directory. One binary or one container, nothing to install
next to it.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/ldaplens-dark.png">
  <img alt="ldaplens: the directory tree, a search for engineers, and a person's entry with a staged change waiting for Review" src="docs/images/ldaplens-light.png">
</picture>

## Try it online

[demo.ldaplens.com](https://demo.ldaplens.com) runs ldaplens in server mode on
the directory of CICD Team, a made-up company: about 300 people, their
departments, teams and access groups, nested groups included. Sign in as
`demo` with the password `demo`. Almost everything is yours to change: browse
the tree, search with filters, edit people, move them between groups, create,
rename and delete entries, import LDIF. Every 15 minutes (at :00, :15, :30 and
:45) the directory goes back to its original state, so nothing you do there
lasts, and others may be editing it at the same time. Passwords and binary
values such as photos and certificates are read-only there, so the demo stays
a demo.

## Quick start

On your machine, in local mode, with the saved connections and the login
token in a volume:

```sh
docker run --rm -p 127.0.0.1:8080:8080 -v ldaplens:/data ghcr.io/cicdteam/ldaplens local
```

Open the login link from the log and add a directory with **New
connection**. The image is for amd64 and arm64; [verify
it](docs/deployment.md#verify-the-image) before you run it.

For a team, in Kubernetes, with the Helm chart (every user signs in with
their own uid and password):

```sh
helm install ldaplens oci://ghcr.io/cicdteam/charts/ldaplens -n ldaplens -f values.yaml
```

See [Kubernetes](docs/deployment.md#kubernetes) for a `values.yaml`, and
[the chart's README](deploy/helm/ldaplens/README.md) for every value.

## What it does

- **Browse** a lazy tree, and see an entry as a card with its attributes
  grouped by the schema, readable AD values (SIDs, GUIDs, times,
  `userAccountControl`) and photos.
- **Search** by name, with an RFC 4515 filter (completed from the schema) or
  by DN; keep recent and saved searches; export the results as CSV or LDIF.
- **Edit** attributes, create, clone, rename, move and delete entries, set
  passwords. Changes are staged and written only on Review/Apply, with the
  LDIF shown first.
- **Group membership**, direct and nested, both ways: the groups of an entry
  and the members of a group, editable.
- **Import LDIF** from another directory, under a new base.
- **OpenLDAP and Active Directory**, over LDAP, StartTLS or LDAPS.
- **Safe for a team**: per-user sign-in with each person's own uid, read-only
  profiles, an operator notice, no passwords stored, and logs of who changed
  what.

## Two modes

- **`ldaplens local`** is for one person, on a laptop or a jump host. It
  keeps the connections you save in the UI and a login token.
- **`ldaplens server`** is for a team. It serves only the profiles in its
  `connections.yaml` and keeps nothing on disk.

[Two modes](docs/configuration.md#two-modes) has the details.

## Documentation

- [Using ldaplens](docs/ui.md): the UI, editing, Review/Apply, LDIF import.
- [Configuration](docs/configuration.md): the two modes, `connections.yaml`,
  saved connections, every flag.
- [Deployment](docs/deployment.md): Docker, running for a team, reverse
  proxies, Kubernetes, systemd.
- [Logs and operations](docs/operations.md): what is logged, and what to do
  when something goes wrong.
- [Development](docs/development.md): building, testing, Samba AD vs Active
  Directory.
- [Security](SECURITY.md): reporting a vulnerability.

## License

[Apache License 2.0](LICENSE).
