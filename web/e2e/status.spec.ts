import { e2eDN } from './fixture'
import { attrRow, connect, openEntry, pendingBar } from './helpers'
import { expect, test, type Page } from './test'

const bar = (page: Page) => page.getByRole('contentinfo', { name: 'Connection status' })

test('status bar: address, transport, server type and the keys of the focused pane', async ({
  page,
}) => {
  await openEntry(page, 'demo', e2eDN('read'))
  const status = bar(page)
  await expect(status).toContainText('ldap://127.0.0.1:1390')
  await expect(status).toContainText('not encrypted')
  await expect(status.getByText('LDAP', { exact: true })).toBeVisible()
  // The e2e binary is built without a version, so it reports dev.
  await expect(status.getByText('ldaplens dev', { exact: true })).toBeVisible()

  await page.getByRole('tree', { name: 'Directory tree' }).focus()
  await expect(status).toContainText('expand')
  await page.locator('#card-heading').focus()
  await expect(status).toContainText('undo')
  await page.getByRole('button', { name: 'Edit', exact: true }).click()
  const desc = attrRow(page, 'description').locator('input').first()
  await desc.focus()
  await expect(status).toContainText('stage value')
  await desc.fill('status bar edit')
  await desc.press('Enter')
  await expect(pendingBar(page)).toContainText('1 change in 1 entry staged for demo')

  // Staged only: discarding leaves the directory as it was. Discard all asks first.
  await page.keyboard.press('ControlOrMeta+S')
  const review = page.getByRole('dialog', { name: 'Review changes' })
  page.once('dialog', (d) => {
    expect(d.message()).toBe('Discard 1 unapplied change?')
    void d.dismiss()
  })
  await review.getByRole('button', { name: 'Discard all' }).click()
  await expect(review).toContainText('status bar edit')
  page.once('dialog', (d) => void d.accept())
  await review.getByRole('button', { name: 'Discard all' }).click()
  await page.keyboard.press('Escape')
  await expect(pendingBar(page)).toHaveCount(0)
})

test('status bar on Active Directory over ldaps', async ({ page }) => {
  await connect(page, 'ad')
  const status = bar(page)
  await expect(status).toContainText('ldaps://127.0.0.1:1637')
  await expect(status.getByText('TLS', { exact: true })).toBeVisible()
  await expect(status).toContainText('Active Directory')
  await expect(status).not.toContainText('certificate not verified')
})
