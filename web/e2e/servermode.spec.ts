import { connectDialog, profileButton } from './helpers'
import { expect, test } from './test'

// The server-mode instance (playwright.config.ts): root path, no token, the profiles in
// deploy/e2e/connections.server.yaml. Its failed sign-in limits are its own, so the wrong
// password here locks nobody out of the local instance.
test.use({ baseURL: 'http://127.0.0.1:18092/', extraHTTPHeaders: {} })

test('server mode shows no saving and no trust', async ({ page }) => {
  // The checks for absent controls below mean something only once the UI knows its mode.
  const mode = page.waitForResponse('**/api/server')
  await page.goto('./')
  expect((await mode).ok()).toBe(true)
  await expect(profileButton(page, 'people')).toBeVisible()
  // The operator's notice (playwright.config.ts) is text: the markup shows as typed and the
  // newline breaks the line.
  const notice = page.getByRole('note', { name: 'Notice' })
  const text = 'Public demo <b>x</b>\nsecond'
  await expect(notice).toHaveJSProperty('innerText', text)
  // Nothing about where a profile binds before a sign-in.
  await expect(page.getByText('127.0.0.1')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'New connection' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: /^Edit / })).toHaveCount(0)
  await expect(page.getByText('yaml', { exact: true })).toHaveCount(0)

  // A bind DN the UI is not told: the field is just "Password", and password managers key it by
  // the profile name.
  let dialog = await connectDialog(page, 'fixed')
  await expect(dialog.getByLabel('Password', { exact: true })).toBeVisible()
  await expect(dialog.getByLabel('Username')).toHaveCount(0)
  await expect(dialog.locator('input[autocomplete=username]')).toHaveValue('fixed')

  // An untrusted certificate: the generic message, no chain and no trust.
  dialog = await connectDialog(page, 'ad-untrusted')
  await dialog.getByLabel('Username').fill('user001')
  await dialog.getByLabel('Password', { exact: true }).fill('Demo-Passw0rd!')
  await dialog.getByLabel('Password', { exact: true }).press('Enter')
  await expect(dialog.getByRole('alert')).toHaveText(
    "the directory server's certificate is not trusted",
  )
  await expect(dialog.getByText('Server certificate', { exact: true })).toHaveCount(0)
  await expect(dialog.getByRole('button', { name: 'Trust this CA and connect' })).toHaveCount(0)

  dialog = await connectDialog(page, 'people')
  const username = dialog.getByLabel('Username')
  const password = dialog.getByLabel('Password', { exact: true })
  await expect(username).toHaveAttribute('autocomplete', 'username')
  await expect(dialog.getByText('127.0.0.1')).toHaveCount(0)
  await username.fill('a b')
  await password.fill('secret001')
  await password.press('Enter')
  await expect(dialog.getByRole('alert')).toHaveText(
    'A username has only letters, digits, ".", "_" and "-", starts with a letter or digit and is at most 64 characters long.',
  )
  // A wrong password says no more than that, whatever the directory said.
  await username.fill('user001')
  await password.fill('wrong')
  await password.press('Enter')
  await expect(dialog.getByRole('alert')).toHaveText('invalid username or password')

  await password.fill('secret001')
  await password.press('Enter')
  await expect(page).toHaveURL('http://127.0.0.1:18092/c/people')
  await expect(
    page.locator('header').getByText('Signed in as uid=user001,ou=people,dc=example,dc=org', {
      exact: true,
    }),
  ).toBeVisible()
  await expect(page.getByRole('contentinfo', { name: 'Connection status' })).toContainText(
    'ldap://127.0.0.1:1390',
  )
  // Still there, above the header, and the page still fits the window.
  await expect(notice).toHaveJSProperty('innerText', text)
  expect((await notice.boundingBox())!.y).toBe(0)
  expect(await page.evaluate(() => document.scrollingElement!.scrollHeight)).toBe(
    page.viewportSize()!.height,
  )
})

test('a profile label stands in for its name, which stays in the URL', async ({ page }) => {
  await page.goto('./')
  const label = 'Example Org (fictional catalog)'
  const button = page.getByRole('button', {
    name: new RegExp(`^${label.replace(/[()]/g, '\\$&')}`),
  })
  // The name is still there, small, under the label.
  await expect(button).toContainText('labeled')
  await button.click()
  const dialog = page.getByRole('dialog', { name: `Connect to ${label}` })
  await dialog.getByLabel('Username').fill('user001')
  await dialog.getByLabel('Password', { exact: true }).fill('secret001')
  await dialog.getByLabel('Password', { exact: true }).press('Enter')
  await expect(page).toHaveURL('http://127.0.0.1:18092/c/labeled')
  await expect(page.locator('header').getByTitle('All connections', { exact: true })).toHaveText(
    label,
  )
})
