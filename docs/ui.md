# Using ldaplens

How the UI behaves: browsing, search, the inspector, editing, Review/Apply and LDIF import.

## Using the UI

- **Connections** (`/`): profiles from `connections.yaml` (marked `yaml`) and
  ones saved in the UI (local mode; server mode shows less, see [Two
  modes](configuration.md#two-modes)). Passwords are never stored: a profile with a bind DN
  but no `passwordEnv`/`passwordFile` asks for one at connect time, and so
  does one whose configured password the server rejects or cannot read. A
  `bindDNTemplate` profile asks for a username and a password (see [Per-user
  login](deployment.md#per-user-login)). An
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
