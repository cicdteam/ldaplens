import { e2eDN, e2eGroup } from './fixture'
import { attrRow, openEntry, palette, paletteItem, review } from './helpers'
import { expect, test, type Page } from './test'

const editButton = { name: 'Edit', exact: true }
const applyButton = { name: 'Apply to demo', exact: true }
type List = 'Member of' | 'Members'

const list = (page: Page, name: List) => page.getByRole('region', { name, exact: true })
const row = (page: Page, name: List, dn: string) => list(page, name).locator(`[data-ref="${dn}"]`)

/** Edit on, then the Membership tab. */
async function editMembership(page: Page) {
  await page.getByRole('button', editButton).click()
  await page.getByRole('tab', { name: 'Membership' }).click()
}

test('join a groupOfNames from Member of through the palette; Members lists the new member', async ({
  page,
}) => {
  const group = e2eGroup('join-team')
  await openEntry(page, 'demo', e2eDN('joiner'))
  await palette(page, 'Add to group')
  await paletteItem(page, 'Add to group…').click()
  const field = page.getByLabel('Add to group', { exact: true })
  await expect(field).toBeFocused()
  await expect(page.getByRole('button', editButton)).toHaveAttribute('aria-pressed', 'true')
  await field.fill(group)
  await field.press('Enter')
  await expect(row(page, 'Member of', group).getByRole('img', { name: 'added' })).toBeVisible()

  const dialog = await review(page)
  await expect(dialog).toContainText(`dn: ${group}`)
  await expect(dialog).toContainText('add: member')
  await expect(dialog).toContainText(`member: ${e2eDN('joiner')}`)
  await dialog.getByRole('button', applyButton).click()
  await expect(dialog).toBeHidden()
  await expect(row(page, 'Member of', group)).toBeVisible()
  await expect(row(page, 'Member of', group).getByRole('img', { name: 'added' })).toHaveCount(0)

  await page.goto(`c/demo/entry?dn=${encodeURIComponent(group)}`)
  await page.getByRole('tab', { name: 'Membership' }).click()
  await expect(row(page, 'Members', e2eDN('joiner'))).toBeVisible()
})

test('remove a member in Members: struck, restorable, gone after Apply', async ({ page }) => {
  const dropped = e2eDN('dropped')
  await openEntry(page, 'demo', e2eGroup('drop-team'))
  await editMembership(page)
  const r = row(page, 'Members', dropped)
  await r.getByRole('button', { name: `Remove ${dropped}`, exact: true }).click()
  await expect(r.getByRole('img', { name: 'deleted' })).toBeVisible()
  await r.getByRole('button', { name: `Restore ${dropped}`, exact: true }).click()
  await expect(r.getByRole('img', { name: 'deleted' })).toHaveCount(0)
  await r.getByRole('button', { name: `Remove ${dropped}`, exact: true }).click()

  const dialog = await review(page)
  await expect(dialog).toContainText('delete: member')
  await expect(dialog).toContainText(`member: ${dropped}`)
  await dialog.getByRole('button', applyButton).click()
  await expect(dialog).toBeHidden()
  await expect(row(page, 'Members', dropped)).toHaveCount(0)
  await expect(row(page, 'Members', e2eDN('keeper'))).toBeVisible()
})

test('join a posixGroup: the batch adds memberUid, and Member of keeps the group', async ({
  page,
}) => {
  const group = e2eGroup('posix-team')
  await openEntry(page, 'demo', e2eDN('posixer'))
  await editMembership(page)
  await list(page, 'Member of').getByRole('button', { name: 'Add to group…' }).click()
  const field = page.getByLabel('Add to group', { exact: true })
  await field.fill(group)
  await field.press('Enter')
  await expect(row(page, 'Member of', group).getByRole('img', { name: 'added' })).toBeVisible()

  const dialog = await review(page)
  await expect(dialog).toContainText('add: memberUid')
  await expect(dialog).toContainText('memberUid: posixer')
  await dialog.getByRole('button', applyButton).click()
  await expect(dialog).toBeHidden()
  await expect(row(page, 'Member of', group)).toBeVisible()
  await expect(row(page, 'Member of', group).getByRole('img', { name: 'added' })).toHaveCount(0)
})

test("removing a groupOfNames' last member keeps Apply off, with the reason", async ({ page }) => {
  const soloist = e2eDN('soloist')
  await openEntry(page, 'demo', e2eGroup('solo-team'))
  await editMembership(page)
  await row(page, 'Members', soloist)
    .getByRole('button', { name: `Remove ${soloist}`, exact: true })
    .click()
  const dialog = await review(page)
  await expect(dialog).toContainText('1 entry fails validation')
  await expect(dialog.getByRole('button', applyButton)).toBeDisabled()
  await page.keyboard.press('Escape')
  await page.getByRole('tab', { name: 'Attributes' }).click()
  await expect(attrRow(page, 'member').getByRole('alert')).toHaveText(
    'Required by the object class',
  )
})

test('leave a group from Member of: struck, then gone after Apply', async ({ page }) => {
  const group = e2eGroup('leave-team')
  await openEntry(page, 'demo', e2eDN('leaver'))
  await editMembership(page)
  const r = row(page, 'Member of', group)
  await r.getByRole('button', { name: `Remove ${group}`, exact: true }).click()
  await expect(r.getByRole('img', { name: 'deleted' })).toBeVisible()

  const dialog = await review(page)
  await expect(dialog).toContainText(`dn: ${group}`)
  await expect(dialog).toContainText('delete: member')
  await expect(dialog).toContainText(`member: ${e2eDN('leaver')}`)
  await dialog.getByRole('button', applyButton).click()
  await expect(dialog).toBeHidden()
  await expect(row(page, 'Member of', group)).toHaveCount(0)
})

test('an add still reading the group stages once, and Mod+S reviews it once staged', async ({
  page,
}) => {
  const group = e2eGroup('join-team')
  await openEntry(page, 'demo', e2eDN('racer'))
  await editMembership(page)
  // The group read is held, so the add is still pending at the second commit and at Mod+S.
  let asked!: () => void
  const reading = new Promise<void>((r) => (asked = r))
  let release!: () => void
  const held = new Promise<void>((r) => (release = r))
  await page.route(
    (url) => url.pathname.endsWith('/entry') && url.searchParams.get('dn') === group,
    async (route) => {
      asked()
      await held
      await route.continue()
    },
  )
  await list(page, 'Member of').getByRole('button', { name: 'Add to group…' }).click()
  const field = page.getByLabel('Add to group', { exact: true })
  await field.fill(group)
  await field.press('Enter')
  await reading
  // Mod+S blurs the field, which commits it again, then asks for the review.
  await page.keyboard.press('ControlOrMeta+S')
  await page.evaluate(
    () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))),
  )
  const dialog = page.getByRole('dialog', { name: 'Review changes' })
  await expect(dialog).toHaveCount(0)
  release()
  await expect(dialog).toContainText(`member: ${e2eDN('racer')}`)
  // Closed first: the modal review hides the page from the accessibility tree.
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
  await expect(row(page, 'Member of', group).getByRole('img', { name: 'added' })).toBeVisible()
  await expect(list(page, 'Member of').getByRole('alert')).toHaveCount(0)
})

test('a server that refuses an unindexed memberUid, and one that lists isMemberOf: the groups it found, a note, the source', async ({
  page,
}) => {
  const hidden = 'cn=hidden,ou=groups,dc=example,dc=org'
  // OpenDJ and 389 DS answer such a search with result 50; the backend then
  // searches per attribute and reports the one it skipped (Go tests cover
  // that). Here the answer is made to carry it.
  await page.route(
    (url) => url.pathname.endsWith('/memberof'),
    async (route) => {
      const res = await route.fetch()
      const body = await res.json()
      // A group only the server's isMemberOf lists (one the user may not search).
      const refs = [...(body.refs ?? []), { dn: hidden, source: 'isMemberOf' }]
      await route.fulfill({ response: res, json: { ...body, refs, skipped: ['memberUid'] } })
    },
  )
  await openEntry(page, 'demo', 'uid=user001,ou=people,dc=example,dc=org')
  await page.getByRole('tab', { name: 'Membership' }).click()
  await expect(
    page.getByText(
      'The server refused to search memberUid (usually because it is not indexed there), so groups that list this entry only in that attribute are missing.',
    ),
  ).toBeVisible()
  // The groups the server did find are still listed.
  await expect(list(page, 'Member of').locator('[data-ref]').first()).toBeVisible()
  await expect(row(page, 'Member of', hidden)).toContainText('from isMemberOf')
})
