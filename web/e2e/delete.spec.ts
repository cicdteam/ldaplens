import { E2E_OU, e2eDN, e2eGroup, e2eOU } from './fixture'
import { blur, openEntry } from './helpers'
import { expect, test } from './test'

const deleteButton = { name: 'Delete', exact: true }

test('delete: a container with children stays; a leaf goes after typing its RDN', async ({
  page,
}) => {
  await openEntry(page, 'demo', e2eOU('full'))
  await page.getByRole('button', { name: 'More', exact: true }).click()
  await page.getByRole('button', deleteButton).click()
  const dialog = page.getByRole('dialog', { name: 'Delete entry' })
  await expect(dialog).toContainText('Has 1 child. Delete it first.')
  await expect(dialog.getByRole('button', deleteButton)).toBeDisabled()
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
  // Mod+Backspace, the Finder's key, opens it too.
  await blur(page)
  await page.keyboard.press('ControlOrMeta+Backspace')
  await expect(dialog).toContainText('Has 1 child. Delete it first.')
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()

  await page.goto(`c/demo/entry?dn=${encodeURIComponent(e2eDN('deleteme'))}`)
  await expect(page.locator('[data-attr]').first()).toBeVisible()
  await blur(page)
  await page.keyboard.press('Delete')
  const del = dialog.getByRole('button', deleteButton)
  const typed = dialog.getByLabel('Type uid=deleteme to confirm')
  await expect(typed).toBeFocused()
  // Again from the menu, with the count cached: the field opens with the dialog and keeps focus.
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: 'More', exact: true }).click()
  await page.getByRole('button', deleteButton).click()
  await expect(typed).toBeFocused()
  await typed.fill('uid=deleteme2')
  await expect(del).toBeDisabled()
  await typed.fill(' uid=deleteme ')
  await expect(del).toBeEnabled()
  await del.click()
  await expect(dialog).toBeHidden()
  await expect(page).toHaveURL(new RegExp(`dn=${encodeURIComponent(E2E_OU)}(&|$)`))
  await page.goto(`c/demo/entry?dn=${encodeURIComponent(e2eDN('deleteme'))}`)
  await expect(page.getByText('No entry at this DN.')).toBeVisible()
})

test('delete: an entry in groups names them, since the server may leave it there', async ({
  page,
}) => {
  await page.goto(`c/demo/entry?dn=${encodeURIComponent(e2eDN('grouped'))}`)
  await expect(page.locator('[data-attr]').first()).toBeVisible()
  await blur(page)
  await page.keyboard.press('Delete')
  const dialog = page.getByRole('dialog', { name: 'Delete entry' })
  const note = dialog.getByRole('note', { name: 'Group membership' })
  await expect(note).toContainText('A member of 2 groups')
  await expect(note).toContainText(e2eGroup('grouped-names'))
  await expect(note).toContainText(e2eGroup('grouped-posix'))
  await expect(note).toContainText('remove it from them yourself')
  // Only a reminder: the delete goes ahead.
  await dialog.getByLabel('Type uid=grouped to confirm').fill('uid=grouped')
  await dialog.getByRole('button', deleteButton).click()
  await expect(dialog).toBeHidden()
})

test('delete: an entry in no group gets no reminder', async ({ page }) => {
  await page.goto(`c/demo/entry?dn=${encodeURIComponent(e2eDN('read'))}`)
  await expect(page.locator('[data-attr]').first()).toBeVisible()
  await blur(page)
  await page.keyboard.press('Delete')
  const dialog = page.getByRole('dialog', { name: 'Delete entry' })
  await expect(dialog.getByLabel('Type uid=read to confirm')).toBeVisible()
  await expect(dialog.getByRole('note', { name: 'Group membership' })).toHaveCount(0)
  await page.keyboard.press('Escape')
})
