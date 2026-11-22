# Using ldaplens

How the UI behaves: browsing, search, the inspector, editing, Review/Apply and LDIF import.

## Using the UI

Every view has a URL (`/c/<profile>/entry?dn=...`,
`/c/<profile>/search?filter=...`), so links and reloads work.

### Connections

The start page (`/`) lists the profiles from `connections.yaml` (marked
`yaml`) and, in local mode, the ones saved in the UI; server mode shows less
([Two modes](configuration.md#two-modes)). Passwords are never stored: a
profile with a bind DN but no `passwordEnv`/`passwordFile` asks for one at
connect, and so does one whose configured password the server rejects or
cannot read. A `bindDNTemplate` profile asks for a username and a password
([Per-user login](configuration.md#per-user-login)). An untrusted TLS
certificate shows its chain and fingerprints; "Trust this CA and connect"
stores it in a UI profile (for a yaml profile, put the PEM in `caPEM` or
point `caFile` at it).

**New connection** (local mode) asks three things in turn, and Edit shows
the same steps, each reachable from the top:

1. **Server**: host and port, and the security (LDAPS, StartTLS or none,
   which warns that the password crosses the network in clear text). A
   pasted `ldap://` or `ldaps://` URL fills all three. Then the base DN
   (empty: the server's), and under TLS certificate options a CA in PEM and
   Skip certificate verification.
2. **Sign in**: **With my username** (the default: connect asks for a
   username and a password, and binds as a `bindDNTemplate`), **As one fixed
   DN** (connect asks for its password) or **Anonymously**. For a username,
   OpenLDAP and others suggests `uid={user},ou=people,<base DN>` and Active
   Directory `{user}@<domain of the base DN>`; the template must hold
   `{user}` exactly once, and Try it shows the DN a sample username gives.
3. **Name**: the name (from the host, in links and logs; fixed once saved),
   a label, a color and Read-only, over a summary of what connect will ask
   for.

### Layout and tree

The tree is on the left and the entry card on the right. A search, or List
children, opens a list pane between them; Up and Down in the list move the
card through the rows, and Esc or × closes it without losing the entry or
its staged changes.

The tree loads lazily and works from the keyboard (arrows, Home/End, `*`,
type-ahead). An account row (`uid=` or `sAMAccountName=`) also shows the
person's name. A container shows its first 2000 children; a bigger one ends
with "More entries…", which lists them in the list pane. **List children**
(the list icon on a container, Shift+Enter on its row, or the command
palette) lists the entries one level under it. "Filter loaded entries"
narrows the open branches without asking the server.

Invisible and direction-changing characters in names and DNs (a
right-to-left override, a zero-width space) show as `<U+XXXX>`, such as
`<U+202E>`; Copy DN, LDIF and writes use the real value.

### Search

The search bar in the header takes three kinds of input; Enter runs it.

- Plain text (`doe`, `John Doe`) searches by name: `cn` and `displayName`
  containing it, `uid`, `sAMAccountName` and `mail` starting with it.
- Text with `=` or `(` is an RFC 4515 filter (`uid=jdoe`,
  `(&(objectClass=person)(cn=a*))`), with highlighting, completion from the
  schema and inline errors. Empty matches everything.
- A DN under the connection's base DN, in any letter case, opens that entry.
  A value with an unescaped `=` (`memberOf=cn=devs,...`) keeps it a filter.

The chip next to it sets the search base, the scope (subtree, one level or
base) and the result columns. Next to it are the filter builder and the
recent and saved searches, kept per connection in the browser, with
examples (people, groups, OUs, disabled accounts on Active Directory).

A filter can test a password or secret attribute for presence
(`(userPassword=*)`) but not match its values: such filters are refused with
400 `secret_filter`.

### Results

Each row leads with the entry's name (`displayName`, else `cn`, else its RDN
value), then `uid` (`sAMAccountName` on Active Directory) and `mail` until you
pick other columns. Sorting and Export (CSV or LDIF) act on the rows loaded;
⌘C / Ctrl+C copies the selected row's DN.

A search shows up to 500 entries; **Load more** runs it again for 1000, 2000,
then 5000. Past 5000, narrow the filter or the base. A search the server cut
with its own size or time limit, or one past ldaplens's cap of 64 MiB of
values, shows what arrived, marked "The search stopped early".

Some servers (OpenDJ, 389 DS) refuse a search they cannot answer from an index
unless the user holds a special privilege, and an OR filter, such as the
header's quick search, is refused as soon as one part is unindexed. ldaplens
then runs each part of the OR alone, lists what the server answers, and says
"Not searched by" and the attributes of the parts it refused (on OpenDJ,
usually `displayName`): entries that match only those are missing. Attributes
the server's schema does not define, such as `sAMAccountName` off Active
Directory, match nothing and are not named.

### Entry card

The header shows the entry's name and kind, its DN as breadcrumbs with a
copy button, the object classes, "Modified <time> by <modifiersName>" when
the entry has them, and chips for a read-only profile and a disabled Active
Directory account. The actions are Edit, Add to group, Copy DN and More (raw
values, Reload entry and the entry actions); a read-only profile has only
Copy DN, raw values and Reload.

The tabs are **Attributes**, **Membership** and **LDIF**. Attributes start
with Pinned (for a person `cn`, `uid`, `mail`, `title`, `manager`,
`telephoneNumber`; otherwise `cn` and `description`), then Required (MUST),
Optional (MAY) and Operational. AD values are decoded (SIDs, GUIDs, dates,
`userAccountControl` flags), photos are shown, and binary values have a
Download button. Values of password and secret attributes never leave the
server: only their names and a count are shown.

Long value lists show the first 100 values and grow by 1000 on request. An
attribute with more than 10 000 values (the `member` list of a huge group)
arrives truncated to its first 10 000, and exports hold only those; Edit
still adds and removes single values.

LDIF shows non-ASCII text such as Cyrillic names decoded, marked `utf-8`;
values with edge spaces, line breaks or control characters stay base64, and
Copy LDIF and downloads keep base64, as RFC 2849 requires.

### Membership

The Membership tab lists **Member of** and, for groups that use `member` or
`uniqueMember`, **Members**, either Direct or Nested (with the "via" group).
Member of searches `member`, `uniqueMember` and `memberUid` (posixGroups, by
the entry's `uid`); an equality index on each keeps it fast. Both lists stop
at 10 000 entries and Nested at 10 levels, and are then marked "The list may
be incomplete".

Some servers refuse a search on an attribute that has no index unless the
user has a special privilege (OpenDJ and 389 DS answer result 50, "unindexed
search"); one unindexed attribute makes the whole search unindexed. Member
of then searches each attribute alone, lists what the server answers, and
names the attributes it refused (often `memberUid`) in a note above the list.
Only when it refuses all of them does the tab show the error.

Member of also reads the groups the server keeps on the entry itself,
`isMemberOf` (OpenDJ) or `memberOf` (389 DS, FreeIPA, Active Directory,
OpenLDAP with the memberof overlay). It lists those the searches did not
reach, such as groups the user may not search, marked "from isMemberOf" (or
memberOf): the server does not say whether such a membership is direct or
nested, so these rows are not removed from here; open the group instead.

### Pending changes and status bar

While changes are staged, a bar above the status bar shows how many, for
which profile and server, with Discard and Review. The status bar shows the
server address, whether the connection is encrypted, the server type, the
keys that act in the focused pane and, at the right end, the ldaplens version
(as `ldaplens --version` prints it).

### Shortcuts

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

- **Edit** turns attribute values into fields; a value
  commits on Enter. Changes are staged, not written: added, changed, deleted
  and stale values are marked on the card, the pending changes bar shows
  how many, and staged entries get a dot in the tree and in the results.
  The batch is kept per connection for the browser tab and survives a reload.
- **Review** names the profile and the server the batch goes to and shows it
  as LDIF to read or copy; **Apply to** and the profile's name (Apply to
  demo) writes it. Discard all asks first, like Discard on the bar. A
  read-only profile (`readOnly: true`) has no Edit, and
  the server refuses its writes.
- If an entry changed since you read it, the apply stops at that entry:
  "Reload entry" marks the staged changes that no longer fit as stale,
  "Drop stale changes" removes them. A value the server rejects shows its
  error at the field. Entries that applied leave the batch.
- Undo and redo work on staged changes. Disconnecting or closing the tab
  with unapplied changes asks first.
- **Entry actions** (in the card's More, or the command palette): **New
  child**, **Clone**, **Rename/Move** and **Delete**. Each opens a dialog with the DN and the LDIF it
  writes, and writes at once; it is not staged. More also holds **Import LDIF
  here…** ([Importing LDIF](#importing-ldif)). A read-only profile
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
- **Set password…** (in More and the command palette) is offered for
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
  that body size (see [Behind a reverse proxy](deployment.md#behind-a-reverse-proxy)).
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
