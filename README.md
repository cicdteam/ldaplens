# ldaplens - LDAP UI

A web UI to view, search and manage LDAP data: an LDAP browser and editor for
OpenLDAP and Active Directory. One binary or one container, nothing to install
next to it.

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
empty. In server mode:

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
fixed ones in a [`connections.yaml`](#connectionsyaml) you mount. A local run
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

## Using the UI

- **Connections** (`/`): profiles from `connections.yaml` (marked `yaml`) and
  ones saved in the UI (local mode; server mode shows less, see [Two
  modes](#two-modes)). Passwords are never stored: a profile with a bind DN
  but no `passwordEnv`/`passwordFile` asks for one at connect time, and so
  does one whose configured password the server rejects or cannot read. A
  `bindDNTemplate` profile asks for a username and a password (see [Per-user
  login](#per-user-login)). An
  untrusted TLS certificate shows its chain and fingerprints; "Trust this CA
  and connect" stores it in a UI profile (for a yaml profile, copy the PEM into `caPEM` or
  point `caFile` at it).
- **Layout**: the tree on the left and the entry card on the right. A search,
  or List children, opens a list pane between them, and the card stays
  beside it. Up and Down in the list move the card through the rows; Down in
  the search bar moves into the list, on its selected row or else the first.
  The list's × button, or Esc while focus is in the list, closes it: the entry
  in the card, its Edit mode and the staged changes stay, the URL keeps only
  the entry, and focus moves to the card's heading (to the tree with no entry
  open). Pane sizes are
  remembered separately with the list and without it. With no entry
  selected, the card says so. Invisible and direction-changing characters in
  names and DNs (a right-to-left override, a zero-width space) show as
  `<U+XXXX>`, such as `<U+202E>`; Copy DN, LDIF and writes use the real value.
- **List children** (the list icon on a container in the tree, or "List
  children of the current entry" in the command palette): lists the entries
  one level under it, with the default columns. The search bar keeps its own
  text, base and scope, so a name typed next still searches under its base.
- **Tree** (left): lazy, keyboard-driven (arrows, Home/End, `*`, type-ahead).
  Each row shows its RDN; an account row (`uid=` or `sAMAccountName=`) also
  shows the person's name, muted: `displayName`, else `cn`. A click on a
  container selects it and expands or collapses it; the chevron only expands
  or collapses it, and so do Right and Left. Collapsing a parent of the
  selected entry marks that parent instead and keeps the card. A container shows its List
  children icon while the pointer is over its row, and on the row with
  keyboard focus; Shift+Enter on the row does the same. "Filter loaded
  entries" above the tree narrows the rows of open branches to the ones whose
  RDN value or name holds the text (any case), with their parents; the
  selected entry always stays. It never asks the server (the search bar
  does). Next to it, Reload the tree from the server and Collapse the tree
  (the header's tree button opens it again). A container shows its first
  2000 children; a bigger one ends with "More entries…", which lists its
  children like List children.
- **Search bar** (header): one line that takes three kinds of input; Enter
  runs it.
  - Plain text (`doe`, `John Doe`) searches by name: `cn` and `displayName`
    containing it, `uid`, `sAMAccountName` and `mail` starting with it. The
    bar, Recent and Saved show the name, not the filter it runs.
  - Text with `=` or `(` is an RFC 4515 filter (`uid=jdoe`,
    `(&(objectClass=person)(cn=a*))`, `memberOf=cn=devs,ou=groups,dc=example,dc=org`),
    with highlighting, completion from the schema inside parentheses and inline
    errors. Empty text matches everything. A filter longer than one line opens
    down over the panels while the bar has focus.
  - A DN under the connection's base DN, in any letter case, opens that entry.
    A value with an unescaped `=` (the `memberOf=...` above) keeps it a filter.

  The chip with the base DN and the scope (subtree, one level or base) opens
  the search base, the scope, a button that uses the selected entry as the
  base, and the result columns, picked from the schema's attribute names. Next
  to it, the filter builder and the recent and saved searches.
- **Search results** (list pane): the toolbar shows the number of entries,
  the filter that ran (its tooltip adds the scope and the base), Columns,
  Export and Close. Columns picks the columns from the schema's attribute
  names; closing it runs the search again with them. Export saves the loaded
  rows as CSV or LDIF. Each row leads with the entry's name:
  `displayName`, else `cn`, else the value of its RDN. The other columns are
  `uid` (`sAMAccountName` on Active Directory) and `mail` until you pick
  others. The header and the rows share one column layout, so they always
  line up; more columns than the pane fits scroll sideways together. A row's
  tooltip is its DN, and ⌘C / Ctrl+C in the list copies the DN of the
  selected row (unless text is selected). A search also asks for
  `displayName` and `cn` to show the name; an LDIF export holds only the DN
  and the picked columns.

  A search returns at most a limit of entries in one request: 500, or the
  search API's `sizeLimit`, up to 5000. The response says `more` when the search matches more. Nothing is kept on the
  server between requests, so "Load more" runs the search again with the
  next limit: 1000, then 2000, then 5000; the rows shown stay until the
  bigger answer arrives. If it fails, the error offers Retry and "Show the
  first N", which goes back to the rows shown before. Running a search
  again starts over at 500. A search that matches more than 5000 entries shows
  the first 5000 with the note "Showing the first 5,000 entries; refine the
  search to see the rest": narrow the filter or the base DN. Sorting by
  column and export to CSV or LDIF act on the rows loaded. A search the
  server cut with its own size or time
  limit, or one past ldaplens's cap of 64 MiB of attribute values, shows
  what arrived and is marked truncated ("The search stopped early"). One
  server message (an entry) over 64 MiB fails the request instead. Recent and saved
  searches are kept per connection in the browser; the same popover lists
  Examples (people, groups, OUs, or disabled accounts on Active Directory). A filter can test a
  password or secret attribute for presence (`(userPassword=*)`) but not
  match on its values: equality, substring, ordering, approximate and
  extensible matches on one, and extensible matches without an attribute
  type (`(:octetStringMatch:=x)`, which test every attribute), are refused
  with 400 `secret_filter`.
- **Entry card** (right): the header says who the entry is. A person shows
  their initials, other entries the icon of their kind (group, unit, domain).
  The title is the entry's name (`displayName`, else `cn`, else the value of
  its RDN), followed by the RDN when it differs. Under it: the DN as
  breadcrumbs (each opens that entry) with a copy button, the object classes,
  status chips, and "Modified <time> by <modifiersName>" when the entry has
  `modifyTimestamp` (and `modifiersName`). There are two status chips, both
  from reliable signals: "Read-only connection" for a read-only profile, and
  "Disabled" for an Active Directory account whose `userAccountControl` has
  the ACCOUNTDISABLE flag. The actions are Edit, Add to group, Copy DN and
  More (raw values, Reload entry, and the entry actions below); a read-only
  profile has Copy DN and More with raw values and Reload entry only. A
  narrow card keeps the action icons and drops their words.

  The tabs are Attributes, Membership and LDIF. Attributes starts with
  **Pinned**, the key attributes for the kind of entry: for a person `cn`,
  `uid` (`sAMAccountName` on Active Directory), `mail`, `title`, `manager`
  and `telephoneNumber`; for a group or any other entry `cn` and
  `description`. Pinned attributes the entry has are listed in that order and
  are not repeated below. Then come Required (MUST), Optional (MAY) and
  Operational, which starts closed. A click on a section's title closes or
  opens it; a section opens by itself while one of its rows has a problem to
  fix or a server error, and Operational also while the attribute filter is
  set. A row with one value is 30 px
  tall; hovering a value shows its copy button, and DN values open their
  entry. The attribute filter sits in the tab bar. Membership loads only while
  its tab is open, with Direct or Nested in the tab bar. The tab, Direct or
  Nested, whether Operational is open, and the attribute filter stay as you
  move to another entry (Up and Down in the list, the tree, a link); Edit
  ends when you leave the entry.

  Values come with decoded AD values (SIDs, GUIDs, dates, `userAccountControl`
  flags), photos and a Download button on binary values (on any profile);
  Raw values in More shows them as stored. Membership lists direct and nested
  groups with the "via" group; LDIF shows the
  entry as LDIF (non-ASCII text such as Cyrillic names is shown decoded, marked
  `utf-8`, here and in the Review and entry action dialogs; values with edge
  spaces, line breaks, control, invisible, private-use or unassigned characters
  stay base64; Copy LDIF and downloads keep base64, as RFC 2849 requires).
  The Members list appears for groups that use `member` or
  `uniqueMember` (not for a posixGroup's `memberUid`); Member of also lists
  the posixGroups whose `memberUid` holds the entry's `uid`. Values of
  password and secret attributes never leave the server (their names and a
  count of hidden values are shown). Long value lists show the first 100
  values and grow by 1000 on request; the attribute filter also narrows the
  values it matches. An attribute with more than 10 000 values (the `member`
  list of a huge group) arrives truncated to its first 10 000, noted
  "Showing the first 10,000 values"; LDIF and CSV exports of such an entry
  also hold only the first 10 000. Edit still adds and deletes single
  values, and Apply touches only those. Member of searches `member`,
  `uniqueMember` and `memberUid`; an equality index on each keeps it fast.
  Member of and Members list at most 10 000 groups or members, and Nested
  follows groups 10 levels deep: a longer list is truncated there, a deeper
  one stops at the tenth level, and either is marked "The list may be
  incomplete" (Member of marks a chain exactly 10 levels deep too: it
  cannot tell that the tenth group is in no other; nor can Members tell that
  a group at the tenth level has no members).
- **Pending changes** (above the status bar, only while changes are staged):
  "2 changes in 1 entry staged for demo" with the server address, Discard
  and Review (⌘S / Ctrl+S). Discard asks first; ⌘Z / Ctrl+Z brings the
  changes back. "Some changes fail validation" shows on the bar while a
  staged change breaks the schema.
- **Status bar** (bottom): the server address, whether the connection is
  encrypted (TLS, StartTLS, not encrypted, certificate not verified), the
  server type, and the keys that act in the focused pane (the tree, the
  list, the card, or a value being edited).
- Every view has a URL (`/c/<profile>/entry?dn=...`,
  `/c/<profile>/search?filter=...`), so links and reloads work.

| Shortcut | Action |
|---|---|
| ⌘K / Ctrl+K | Command palette: go to a DN, find an entry by name, saved searches, switch connection, actions |
| `/` | Focus the search bar (outside text fields) |
| ↵ / Enter, ⌘↵ / Ctrl+Enter | Run the search or open the DN (in the search bar) |
| ⇧⌥F / Shift+Alt+F | Format the filter (in the search bar) |
| ↑ / ↓, Page Up / Page Down, Home / End | Move through the rows; the card follows (in the results list) |
| ↓ / Down | Move into the open list (in the search bar, on its last line) |
| Esc | Close the list (with focus in the list); the entry in the card and staged changes stay |
| ⇧↵ / Shift+Enter | List the children of the focused container (in the tree) |
| ⌘C / Ctrl+C | Copy the DN of the selected row (in the results list, with no text selected) |
| `E` | Edit mode on the entry card (ignored while focus is in a text field, the tree, the results list or a dialog) |
| ⌘S / Ctrl+S | Review the staged changes |
| ⌘Z / Ctrl+Z, ⌘⇧Z / Ctrl+Shift+Z | Undo, redo a staged change (outside text fields) |
| `N`, `C`, `I`, `R` or F2, `P`, `Delete` or ⌘⌫ / Ctrl+Backspace | New child, Clone, Import LDIF here, Rename/Move, Set password, Delete the open entry (ignored while focus is in a text field, the tree, the results list or a dialog) |

⌘K / Ctrl+K and ⇧⌥F / Shift+Alt+F match the physical key, so they also work
on non-Latin keyboard layouts.

### Editing

- **Edit** (`E`, or Edit on the card) turns attribute values into fields; a value
  commits on Enter. Changes are staged, not written: added, changed, deleted
  and stale values are marked on the card, the pending changes bar shows
  how many, and staged entries get a dot in the tree and in the results.
  The batch is kept per connection for the browser tab and survives a reload.
- **Review** (⌘S / Ctrl+S, Review on the pending changes bar, or the command
  palette) names the profile and the server the batch goes to and shows it
  as LDIF to read or copy; **Apply to** and the profile's name (Apply to
  demo) writes it. Discard all asks first, like Discard on the bar. A
  read-only profile (`readOnly: true`) has no Edit, and
  the server refuses its writes.
- If an entry changed since you read it, the apply stops at that entry:
  "Reload entry" marks the staged changes that no longer fit as stale,
  "Drop stale changes" removes them. A value the server rejects shows its
  error at the field. Entries that applied leave the batch.
- ⌘Z / Ctrl+Z undoes a staged change and ⌘⇧Z / Ctrl+Shift+Z redoes it
  (outside text fields). Disconnecting or closing the tab with unapplied
  changes asks first.
- **Entry actions** (in the card's More, or the command palette): **New child** (`N`),
  **Clone** (`C`), **Rename/Move** (`R` or F2) and **Delete** (`Delete` or
  ⌘⌫ / Ctrl+Backspace). Each opens a dialog with the DN and the LDIF it
  writes, and writes at once; it is not staged. More also holds **Import LDIF
  here…** (`I`, see [Importing LDIF](#importing-ldif)). A read-only profile
  has no entry actions, and the server refuses these writes too.
- **New child** picks a structural class (common ones first), auxiliary
  classes, the RDN attribute and value, and asks for the MUST attributes.
  **Clone** starts from the entry, staged changes included, binary values
  (photos) and attributes with options too, without the RDN value, operational
  and secret attributes, the attributes that identify one entry (`uid`, `mail`,
  `uidNumber`, `employeeNumber`, `sAMAccountName`, `userPrincipalName`,
  `objectGUID`, `objectSid`, `mS-DS-ConsistencyGuid`), certificates
  (`userCertificate`, `userSMIMECertificate`, any attribute of certificate
  syntax), authentication bindings (`sshPublicKey`, `ipaSshPubKey`,
  `altSecurityIdentities`, `msDS-KeyCredentialLink`, `servicePrincipalName`,
  `krbPrincipalName`, `krbCanonicalName`) and `ipaUniqueID`. Binary fields in
  both list their values with a delete button and take files through
  "+ Upload file…".
- **Rename/Move** changes the RDN, the parent, or both; "Keep old RDN value as
  an attribute" is off by default and not offered on Active Directory.
  **Delete** removes an entry without children after you type its RDN. Both
  wait until staged changes of the entry and the entries below it are applied
  or discarded. When groups list the entry as a member, Delete names them
  (the first 5): OpenLDAP keeps a deleted member's DN in its groups unless
  the refint overlay is on, so you remove it from them yourself. On Active
  Directory, which does that by itself, there is no reminder.
- **Set password…** (`P`, in More and the command palette) is offered for
  entries whose classes allow `userPassword`, which needs the schema (on
  Active Directory, for `user` entries only). It asks for the new password
  twice and sets it at once: it is never staged, kept in the browser or
  logged, and the dialog shows no LDIF. On OpenLDAP the server hashes it by its own policy through the
  Password Modify operation (the default, when the server offers it), or
  ldaplens stores it as SSHA. Active Directory takes it as `unicodePwd`, only
  over ldaps:// or StartTLS.
- **Membership** in Edit, on the Membership tab: "Add to group…" in Member of
  (also the card's Add to group and the command palette, which turn on Edit
  and open it) and "Add member…" in Members suggest groups
  or entries by name; direct memberships have a remove button, nested ones
  say which group they come through. These are staged changes of the group's
  `member`, `uniqueMember` or `memberUid` (posixGroup, by the entry's `uid`),
  reviewed and applied with the rest of the batch. A plain posixGroup keeps
  its members in `memberUid` and has no Members list to edit: it is joined
  and left from its members' Member of. A posixGroup that is also a
  `groupOfNames` (RFC 2307bis) is edited as `member`, in its Members list
  too, like any groupOfNames. Removing the last member of a group whose class
  requires one (`groupOfNames`, `groupOfUniqueNames`) is flagged at that
  attribute on the Attributes tab, and Apply waits; this needs the schema.
- In Edit, `objectClass` takes auxiliary classes ("+ class") and lets you
  remove them; the structural class and its superclasses are locked. The
  required attributes the added classes introduce appear as empty rows to
  fill; attributes a removed class leaves without a class show `Not allowed
  without <class>; delete it`, and Apply waits until you delete them. This
  needs the schema.
- **Binary values** (`jpegPhoto`, certificates, any `;binary` attribute or one
  that holds binary values already): in Edit, "+ Upload file…" stages a file
  of up to 1 MiB (photos show a preview) and each binary value has a delete
  button; replacing one is a delete and an upload. Certificates are written
  as `userCertificate;binary` (RFC 4522). Apply, New child and Clone send at
  most 8 MiB in one request: a reverse proxy in front of ldaplens must allow
  that body size (see [Behind a reverse proxy](#behind-a-reverse-proxy)).
  One Apply also takes at most 1000 entries and 1000 changes per entry, one New child or Clone at most 1000 attributes, and either at
  most 10 000 values in all; a bigger request is refused with 400
  `request_too_large`, naming the limit. Where the server cannot delete or
  add one value (`jpegPhoto` has no equality rule), Apply reads the
  attribute and writes back the values it will hold: a value someone else
  adds between the two is lost.
- **Attributes with options** (`cn;lang-de`, `description;lang-ru`) are
  attributes of their own, edited apart from `cn`. "+ attribute" takes a name
  with options (letters, digits and hyphens; not `range=`), suggests the
  names the entry's classes allow, and opens the file chooser on Enter for a
  binary one. A required attribute is met by a value of any of its variants.
- A batch too large for the tab's session storage (several photos) stays in
  the tab only: Review says it will not survive a reload.
- Not editable here: secrets other than the entry's password. RDN values
  change through Rename/Move, not in place.

#### Importing LDIF

**Import LDIF…** in the command palette, or **Import LDIF here…** (`I`) in
an entry's More, creates the entries of an LDIF file (RFC 2849 content
records) at once; it is not staged. A read-only profile has neither.

- Paste the LDIF, open a file or drop one on the dialog. It is read in the
  browser, up to 5,000 records; nothing is written until you press Import. A
  syntax error names its line.
- The preview places the entries under this directory: "Their base" (the
  common base of the entries' parents, so the top entries keep their names)
  becomes "Becomes" in every DN, and in every
  value of a DN attribute (`member`, `manager`, `seeAlso`...) under that
  base. Import LDIF here… fills Becomes with the open entry; from the palette
  it is their base itself when that already lies under the server's naming
  context (nothing moves), else the naming context. Both fields can be
  edited; equal values move nothing.
- Left out and named in the preview: server-managed attributes
  (`entryUUID`, `createTimestamp` and others the schema marks
  NO-USER-MODIFICATION or operational) and password values. Users are created
  without a password; set one later with Set password.
- The entries list shows them parents first, in the order they are created,
  with warnings from the schema (a missing required attribute, an attribute
  the schema does not know, a DN value under their base that is not in the
  file). Entries with warnings are still tried; the server decides.
- Not supported, and skipped: `changetype: modify`, `delete`, `modrdn` and
  `moddn` records (`changetype: add` is a plain record), `attr:< URL`
  values, and a record whose DN an earlier record has ("repeats line N").
- The import creates one entry at a time. An entry that already exists is
  skipped, never changed. A failure does not stop the run, but the entries
  below a failed one are not imported. "Stop after this entry" ends it after
  the request in flight; a lost session or connection ends it too. The
  dialog stays open while it runs.
- The report counts created, already existed, failed, not imported and not
  supported, and lists every failure and skip with the reason. **Copy failed
  as LDIF** copies the failed and not imported records, already under the new
  base: fix them and import that to finish a partial import, since what was
  created is skipped the second time. **Copy created DNs** and **Show** (the
  topmost created entry) are there too.

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
login](#per-user-login) has the whole flow and its errors.

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
| `--secure-cookies` | `LDAPLENS_SECURE_COOKIES` | both | `false`; TLS and `X-Forwarded-Proto: https` make cookies Secure without it (see [Behind a reverse proxy](#behind-a-reverse-proxy)) |
| `--trusted-proxies` | `LDAPLENS_TRUSTED_PROXIES` | both | none; comma-separated CIDRs or IPs of reverse proxies whose `X-Forwarded-For` is trusted for the client address (see [Behind a reverse proxy](#behind-a-reverse-proxy)) |
| `--notice` | `LDAPLENS_NOTICE` | both | none; a plain-text message shown above the header on every page once the access token is accepted (markup shows as typed, a newline breaks the line), trimmed, at most 300 characters (a longer one is a startup error) |
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
replica or sticky sessions (see [Running for a team](#running-for-a-team)).

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
proxy](#behind-a-reverse-proxy)), or every client has the proxy's address
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
  reverse proxy](#behind-a-reverse-proxy)); behind any other proxy it is the
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
(no data dir)` (see [Two modes](#two-modes)). A shutdown that runs out of
time logs the warning `shutdown drain timed out` with `after`.

The directory's own audit log sees only the bind DN. A profile bound as a
shared service account (`passwordEnv` or `passwordFile`) makes every write
that account's, and with one shared token only `session` and `remote` tell
people apart. For per-person accountability, use [per-user
login](#per-user-login): everyone binds as themselves with their own
password, and both the directory and ldaplens log their DN.

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
  `LDAPLENS_AUTH_TOKEN_FILE` (see [Two modes](#two-modes)). Server mode
  never makes one. The token file is read at startup: restart after
  rotating it. Run a shared profile with Docker or systemd: the Helm chart
  runs per-user profiles only.
- **Two replicas**, a rolling update that starts a new pod before it stops an
  old one (`maxSurge: 1`, `maxUnavailable: 0`) and a PodDisruptionBudget with
  `minAvailable: 1` keep the UI up through a rollout or a node drain. Each
  replica dials its own connection for a shared profile, so any replica
  answers for it (see [Flags](#flags)), and shared profiles need no affinity.
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
  counts (see [Flags](#flags)) and forgets them when it restarts, and a
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
  access token, from the first page after the login link).

What is read at startup and what at every connect, the probes and the
shutdown drain are described under [Flags](#flags), who did what under [Logs
and accountability](#logs-and-accountability).

### Per-user login

A profile with `bindDNTemplate` (see [Signing in with your own
username](#signing-in-with-your-own-username)) lets everyone sign in to the
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
   [Flags](#flags)).
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
  configured password or an anonymous bind (see [Two modes](#two-modes)).
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
  [Flags](#flags)). Without it, every client has the proxy's address: their
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
with Docker or systemd. [Its README](deploy/helm/ldaplens/README.md), also
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
helm install ldaplens oci://ghcr.io/cicdteam/charts/ldaplens --version 0.1.0 -n ldaplens -f values.yaml
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
digest=$(helm show chart oci://ghcr.io/cicdteam/charts/ldaplens --version 0.1.0 2>&1 >/dev/null | sed -n 's/^Digest: //p')
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

## Development

`make check` runs Go fmt, vet, lint, vuln and unit tests plus the web checks
(`make web-check`: typecheck, ESLint, Prettier, Vitest, npm audit of the
production dependencies).
`make test-integration` needs Docker; it runs OpenLDAP and Samba AD. `make
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
