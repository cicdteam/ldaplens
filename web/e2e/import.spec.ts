import { BASE, E2E_OU, e2eDN } from './fixture'
import { attrRow, blur, openEntry, palette, paletteItem, postJSON } from './helpers'
import { expect, test, type Page } from './test'

const dialog = (page: Page) => page.getByRole('dialog', { name: /^Import/ })

/** The palette's Import LDIF… (no open entry needed): the import dialog. */
async function openImport(page: Page) {
  await palette(page, 'Import LDIF')
  await paletteItem(page, 'Import LDIF…').click()
  await expect(dialog(page)).toBeVisible()
  return dialog(page)
}

// Unique per run, so the spec runs again against the same directory.
const run = Date.now().toString(36)

test('import from another base: the base swap, what is left out, the report, then again', async ({
  page,
}) => {
  const ou = `ou=Imp${run}`
  const corp = (rdns: string) => `${rdns},dc=corp,dc=com`
  const ldif = `version: 1

# Export of dc=corp,dc=com
dn: ${corp(`cn=Ops,${ou}`)}
objectClass: groupOfNames
cn: Ops
member: ${corp(`uid=mkovacs,${ou}`)}
member: ${corp(`uid=apetrov,${ou}`)}

dn: ${corp(ou)}
objectClass: organizationalUnit
ou: Imp${run}

dn: ${corp(`uid=mkovacs,${ou}`)}
objectClass: inetOrgPerson
uid: mkovacs
cn: Marta Kovacs
sn: Kovacs
entryUUID: 6f1c0e44-2b1e-103f-9a51-cb1d4b6f2a10

dn: ${corp(`uid=apetrov,${ou}`)}
objectClass: inetOrgPerson
uid: apetrov
cn:: ${Buffer.from('Анна Петрова').toString('base64')}
sn: Petrova
userPassword: {SSHA}x2Gm9q0rKp4lR1o0aT5eB3lW8yQnJcVd
`
  const moved = (rdns: string) => `${rdns},${BASE}`
  // Children first, for the cleanup.
  const created = [`cn=Ops,${ou}`, `uid=mkovacs,${ou}`, `uid=apetrov,${ou}`, ou].map(moved)
  await openEntry(page, 'demo', e2eDN('read'))
  try {
    let d = await openImport(page)
    await d.locator('input[type=file]').setInputFiles({
      name: 'corp.ldif',
      mimeType: 'text/plain',
      buffer: Buffer.from(ldif),
    })
    await expect(d.getByLabel('LDIF', { exact: true })).toHaveValue(ldif)
    await d.getByRole('button', { name: 'Preview', exact: true }).click()
    await expect(d.getByLabel('Their base')).toHaveValue('dc=corp,dc=com')
    await expect(d.getByLabel('Becomes')).toHaveValue(BASE)
    await expect(d).toContainText('1 server-managed attribute left out: entryUUID')
    await expect(d).toContainText('1 password value left out')
    // Parents first: the OU leads, under the new base.
    await expect(d.getByRole('listitem').first()).toContainText(moved(ou))
    await d.getByRole('button', { name: 'Import 4 entries', exact: true }).click()

    d = page.getByRole('dialog', { name: 'Import finished' })
    await expect(d).toContainText('4 created')
    await expect(d).toContainText('0 failed')
    await d.getByRole('button', { name: `Show ${ou}`, exact: true }).click()
    await expect(d).toBeHidden()
    await expect(page.getByRole('treeitem', { name: new RegExp(`^${ou}`) })).toHaveAttribute(
      'aria-selected',
      'true',
    )

    // The members point at the new base; the password stayed out.
    await page.goto(`c/demo/entry?dn=${encodeURIComponent(moved(`cn=Ops,${ou}`))}`)
    await expect(attrRow(page, 'member')).toContainText(moved(`uid=mkovacs,${ou}`))
    await expect(attrRow(page, 'member')).toContainText(moved(`uid=apetrov,${ou}`))
    await expect(attrRow(page, 'member')).not.toContainText('dc=corp')
    await page.goto(`c/demo/entry?dn=${encodeURIComponent(moved(`uid=apetrov,${ou}`))}`)
    await expect(attrRow(page, 'cn')).toContainText('Анна Петрова')
    await expect(attrRow(page, 'userPassword')).toHaveCount(0)

    // The same file again: everything is there already, nothing is changed.
    d = await openImport(page)
    await d.getByLabel('LDIF', { exact: true }).fill(ldif)
    await d.getByRole('button', { name: 'Preview', exact: true }).click()
    await d.getByRole('button', { name: 'Import 4 entries', exact: true }).click()
    d = page.getByRole('dialog', { name: 'Import finished' })
    await expect(d).toContainText('4 already existed')
    await expect(d).toContainText('0 created')
    await expect(d.getByRole('button', { name: /^Show / })).toHaveCount(0)
  } finally {
    for (const dn of created) await postJSON(page, 'api/c/demo/entries/delete', { dn })
  }
})

test('a failed entry: its child is not imported, Copy failed as LDIF holds both', async ({
  page,
  context,
}) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const person = `uid=nosn${run},${E2E_OU}`
  const child = `cn=child,${person}`
  await openEntry(page, 'demo', e2eDN('read'))
  const d = await openImport(page)
  await d.getByLabel('LDIF', { exact: true }).fill(`dn: ${child}
objectClass: organizationalRole
cn: child

dn: ${person}
objectClass: inetOrgPerson
uid: nosn${run}
cn: No Surname
`)
  await d.getByRole('button', { name: 'Preview', exact: true }).click()
  // Already under this directory: no swap.
  await expect(d.getByLabel('Their base')).toHaveValue(E2E_OU)
  await expect(d.getByLabel('Becomes')).toHaveValue(E2E_OU)
  await expect(d).toContainText('1 with warnings')
  await expect(d).toContainText('missing sn')
  await d.getByRole('button', { name: 'Import 2 entries', exact: true }).click()

  const report = page.getByRole('dialog', { name: 'Import finished' })
  await expect(report).toContainText('0 created')
  await expect(report).toContainText('1 failed')
  await expect(report).toContainText('1 not imported')
  await expect(report).toContainText('not imported: parent not created')
  await report.getByRole('button', { name: 'Copy failed as LDIF', exact: true }).click()
  await expect
    .poll(() => page.evaluate(() => navigator.clipboard.readText()))
    .toMatch(new RegExp(`dn: ${person}\\n[^]*dn: ${child}\\n`))
})

test('Stop ends the run after the request in flight, a lost connection or session at once', async ({
  page,
}) => {
  // Answered here, so nothing is written.
  const ldif = [1, 2]
    .map((n) => `dn: uid=stop${n}${run},${E2E_OU}\nobjectClass: inetOrgPerson\ncn: s\nsn: s\n`)
    .join('\n')
  let release!: () => void
  const held = new Promise<void>((r) => (release = r))
  await page.route('**/entries/add', async (route) => {
    await held
    await route.fulfill({
      status: 409,
      json: { code: 'conflict', message: 'Entry Already Exists', ldapResultCode: 68 },
    })
  })
  await openEntry(page, 'demo', e2eDN('read'))
  let d = await openImport(page)
  await d.getByLabel('LDIF', { exact: true }).fill(ldif)
  await d.getByRole('button', { name: 'Preview', exact: true }).click()
  await d.getByRole('button', { name: 'Import 2 entries', exact: true }).click()
  const running = page.getByRole('dialog', { name: 'Importing' })
  await expect(running).toContainText('creating…')
  await page.keyboard.press('Escape')
  await expect(running).toBeVisible()
  // The palette works over the dialog; picking another action keeps the run.
  await palette(page, 'New child')
  await paletteItem(page, 'New child entry').click()
  await expect(running).toContainText('creating…')
  await palette(page, 'Import LDIF')
  await paletteItem(page, 'Import LDIF…').click()
  await expect(running).toContainText('creating…')
  await running.getByRole('button', { name: 'Stop after this entry', exact: true }).click()
  release()
  const report = page.getByRole('dialog', { name: 'Import finished' })
  await expect(report).toContainText('1 already existed')
  await expect(report).toContainText('1 not imported')
  await expect(report).toContainText('not imported: stopped')
  await page.keyboard.press('Escape')
  await expect(report).toBeHidden()

  await page.unroute('**/entries/add')
  await page.route('**/entries/add', (route) =>
    route.fulfill({
      status: 503,
      json: { code: 'network', message: 'LDAP Result Code 200 "Network Error": EOF' },
    }),
  )
  d = await openImport(page)
  await d.getByLabel('LDIF', { exact: true }).fill(ldif)
  await d.getByRole('button', { name: 'Preview', exact: true }).click()
  await d.getByRole('button', { name: 'Import 2 entries', exact: true }).click()
  await expect(report).toContainText('The import stopped: LDAP Result Code 200')
  await expect(report).toContainText('1 failed')
  await expect(report).toContainText('not imported: stopped')
  await page.keyboard.press('Escape')
  await expect(report).toBeHidden()

  // A lost session: the report stays, with no refetch to send the page to reconnect.
  d = await openImport(page)
  await d.getByLabel('LDIF', { exact: true }).fill(ldif)
  await d.getByRole('button', { name: 'Preview', exact: true }).click()
  await page.unroute('**/entries/add')
  const refetched: string[] = []
  await page.route('**/api/c/demo/**', (route) => {
    if (!route.request().url().endsWith('/entries/add')) refetched.push(route.request().url())
    return route.fulfill({
      status: 401,
      json: { code: 'not_connected', message: 'connect to this profile first' },
    })
  })
  await d.getByRole('button', { name: 'Import 2 entries', exact: true }).click()
  await expect(report).toContainText('The import stopped: connect to this profile first')
  await expect(report.getByRole('button', { name: 'Copy failed as LDIF' })).toBeVisible()
  expect(refetched).toEqual([])
  await expect(page).not.toHaveURL(/[?&]connect=/)
})

test('read-only profile: no Import LDIF in the palette or the actions menu', async ({ page }) => {
  await openEntry(page, 'demo-ro', e2eDN('read'))
  await palette(page, 'Import')
  await expect(paletteItem(page, 'Import LDIF')).toHaveCount(0)
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: 'More', exact: true }).click()
  const menu = page.getByRole('dialog', { name: 'Entry actions' })
  await expect(menu.getByRole('button', { name: 'Reload entry' })).toBeVisible()
  await expect(menu.getByRole('button', { name: 'Import LDIF here…' })).toHaveCount(0)
})

test('Import LDIF here… and I fill Becomes with the open entry', async ({ page }) => {
  const people = `ou=people,${BASE}`
  await openEntry(page, 'demo', people)
  await page.getByRole('button', { name: 'More', exact: true }).click()
  await page.getByRole('button', { name: 'Import LDIF here…', exact: true }).click()
  const d = dialog(page)
  await d
    .getByLabel('LDIF', { exact: true })
    .fill('dn: uid=x,dc=corp,dc=com\nobjectClass: inetOrgPerson\nuid: x\ncn: x\nsn: x\n')
  await d.getByRole('button', { name: 'Preview', exact: true }).click()
  await expect(d.getByLabel('Their base')).toHaveValue('dc=corp,dc=com')
  await expect(d.getByLabel('Becomes')).toHaveValue(people)
  await expect(d.getByRole('listitem')).toContainText(`uid=x,${people}`)
  await d.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(d).toBeHidden()

  // The key opens it too.
  await blur(page)
  await page.keyboard.press('i')
  await expect(dialog(page)).toBeVisible()
})
