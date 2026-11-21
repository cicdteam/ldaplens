import { connectDialog, signIn } from './helpers'
import { expect, test } from './test'

test('stored password rejected: field, hint, focus, typed password connects', async ({ page }) => {
  const dialog = await connectDialog(page, 'readonly-badpw')
  const pw = dialog.locator('input[type=password]')
  await expect(pw).toBeVisible()
  await expect(pw).toBeFocused()
  await expect(
    dialog.getByText('The stored password did not work; enter one to try.'),
  ).toBeVisible()
  await expect(pw).toHaveAttribute('autocomplete', 'current-password')
  const user = dialog.locator('input[autocomplete=username]')
  await expect(user).toHaveValue('cn=admin,dc=example,dc=org')
  await expect(user).toHaveAttribute('tabindex', '-1')
  await pw.fill('wrong2')
  // Enter during a pending connect is ignored: let the typed attempt settle.
  const rejected = page.waitForResponse((r) => r.url().endsWith('/api/connect'))
  await pw.press('Enter')
  expect((await rejected).status()).toBe(401)
  await expect(dialog.getByRole('button', { name: 'Connect' })).toBeEnabled()
  await expect(
    dialog.getByText('The stored password did not work; enter one to try.'),
  ).toBeVisible()
  await expect(pw).toHaveAttribute('aria-describedby', /.+/)
  await pw.fill('adminpassword')
  await pw.press('Enter')
  await expect(page).toHaveURL(/\/c\/readonly-badpw/)
})

test('prompt-only profile shows no stored-password hint', async ({ page }) => {
  const dialog = await connectDialog(page, 'demo-pw')
  await dialog.locator('input[type=password]').fill('nope')
  await dialog.locator('input[type=password]').press('Enter')
  await expect(dialog.getByText(/Invalid Credentials/)).toBeVisible()
  await expect(dialog.getByText(/stored password/)).toHaveCount(0)
})

test('badges and color come with info even when /profiles fails', async ({ page }) => {
  await signIn(page, 'readonly-badpw', { password: 'adminpassword' })
  await page.route('**/api/profiles', (r) => r.abort())
  await page.goto('c/readonly-badpw')
  const header = page.locator('header')
  await expect(header.getByText('READ-ONLY', { exact: true })).toBeVisible()
  // The color dot, the one mark in the link to the connections.
  const dot = header
    .getByTitle('All connections', { exact: true })
    .locator('span[aria-hidden="true"]')
  await expect(dot).toHaveCSS('background-color', 'rgb(221, 51, 51)')
})

test('the connections page scrolls on its own when it is taller than the window', async ({
  page,
}) => {
  await page.setViewportSize({ width: 900, height: 300 })
  await page.goto('./')
  const view = page.locator('main').locator('..')
  await expect.poll(() => view.evaluate((e) => e.scrollHeight > e.clientHeight)).toBe(true)
  await view.evaluate((e) => e.scrollTo(0, e.scrollHeight))
  // The last row, not a named one: the list grows with every profile in connections.yaml.
  await expect(page.locator('main li').last()).toBeInViewport()
})

test('no notice without --notice', async ({ page }) => {
  await page.goto('./')
  // Only once the mode is known: the notice comes in the same answer.
  await expect(page.getByRole('button', { name: 'New connection' })).toBeVisible()
  await expect(page.getByRole('note', { name: 'Notice' })).toHaveCount(0)
})
