import { exact, profileButton } from './helpers'
import { expect, test, type Page } from './test'

// Saved connections live in the local instance's data dir, which e2e/serve.sh empties at every
// start, and there is no delete: each test saves its profile from scratch, so a second run of it
// starts the same way.

const AD_BASE = 'DC=corp,DC=example,DC=test'

/** The connections page's row of profile: its button and its Edit button or yaml mark. */
const row = (page: Page, profile: string) =>
  page.locator('main li').filter({ has: profileButton(page, profile) })

test('a saved connection: create, edit to read-only, name clash, connect', async ({ page }) => {
  await page.goto('./')
  await page.getByRole('button', exact('New connection')).click()
  let form = page.getByRole('dialog', exact('New connection'))
  await form.getByLabel('Name').fill('lab')
  await form.getByLabel('URL').fill('ldap://127.0.0.1:1390')
  await form.getByLabel(/^Base DN/).fill('dc=example,dc=org')
  await form.getByLabel(/^Bind DN/).fill('cn=admin,dc=example,dc=org')
  await form.getByRole('button', exact('Save')).click()
  await expect(form).toHaveCount(0)
  const lab = row(page, 'lab')
  await expect(lab).toContainText('ldap://127.0.0.1:1390')
  await expect(lab.getByText('yaml', { exact: true })).toHaveCount(0)
  await expect(lab.getByText('READ-ONLY', { exact: true })).toHaveCount(0)

  await lab.getByRole('button', exact('Edit lab')).click()
  form = page.getByRole('dialog', exact('Edit lab'))
  await expect(form.getByLabel('Name')).toHaveValue('lab')
  await expect(form.getByLabel('Name')).toHaveAttribute('readonly', '')
  await form.getByLabel('Read-only').check()
  await form.getByRole('button', exact('Save')).click()
  await expect(form).toHaveCount(0)
  await expect(lab.getByText('READ-ONLY', { exact: true })).toBeVisible()

  // A profile from connections.yaml keeps its name: the server refuses, the form stays open.
  await expect(row(page, 'demo').getByText('yaml', { exact: true })).toBeVisible()
  await page.getByRole('button', exact('New connection')).click()
  form = page.getByRole('dialog', exact('New connection'))
  await form.getByLabel('Name').fill('demo')
  await form.getByLabel('URL').fill('ldap://127.0.0.1:1390')
  await form.getByRole('button', exact('Save')).click()
  await expect(form.getByRole('alert')).toHaveText(
    'profile is defined in the config file and cannot be changed here: demo',
  )
  await form.getByRole('button', exact('Cancel')).click()
  await expect(form).toHaveCount(0)

  // The password is never saved: the connect asks for it.
  await profileButton(page, 'lab').click()
  const password = page.getByRole('dialog').getByLabel('Password for cn=admin,dc=example,dc=org')
  await password.fill('adminpassword')
  await password.press('Enter')
  await expect(page).toHaveURL(/\/c\/lab(?:[/?#]|$)/)
  await expect(page.locator('header').getByText('READ-ONLY', { exact: true })).toBeVisible()
  await expect(page.getByRole('treeitem', { name: /^ou=people/ })).toBeVisible()
})

test('an untrusted certificate shows its chain; Trust this CA and connect; the next connect raises no warning', async ({
  page,
}) => {
  // Samba's LDAPS without its CA, read-only: the test writes nothing. Saved through the API, as
  // the form would.
  const saved = await page.request.post('api/profiles', {
    data: {
      name: 'ad-trust',
      url: 'ldaps://127.0.0.1:1637',
      startTLS: false,
      insecureSkipVerify: false,
      readOnly: true,
      baseDN: AD_BASE,
      bindDN: `CN=Administrator,CN=Users,${AD_BASE}`,
    },
  })
  expect(saved.status()).toBe(200)

  const connect = async () => {
    await page.goto('./')
    await profileButton(page, 'ad-trust').click()
    const dialog = page.getByRole('dialog')
    const password = dialog.getByLabel(/^Password/)
    await password.fill('Adm1nPassw0rd!')
    await password.press('Enter')
    return dialog
  }
  const dialog = await connect()
  await expect(dialog.getByText('Server certificate', { exact: true })).toBeVisible()
  await expect(dialog.getByText('CN=localhost', { exact: true })).toBeVisible()
  await expect(dialog.getByText(/^SHA-256 ([0-9a-f]{2}:){31}[0-9a-f]{2}$/)).toBeVisible()
  await dialog.getByRole('button', exact('Trust this CA and connect')).click()
  await expect(page).toHaveURL(/\/c\/ad-trust(?:[/?#]|$)/)
  await expect(page.getByRole('treeitem', exact(AD_BASE))).toBeVisible()

  // The next connect asks for the password, which is never saved, and nothing about the
  // certificate: the dialog closes on success.
  await page.getByRole('button', exact('Disconnect')).click()
  await expect(profileButton(page, 'ad-trust')).toBeVisible()
  await connect()
  await expect(page).toHaveURL(/\/c\/ad-trust(?:[/?#]|$)/)
  await expect(page.getByRole('treeitem', exact(AD_BASE))).toBeVisible()
})

test('a saved connection with a label shows the label, the name under it', async ({ page }) => {
  await page.goto('./')
  await page.getByRole('button', exact('New connection')).click()
  let form = page.getByRole('dialog', exact('New connection'))
  await form.getByLabel('Name').fill('labeled-lab')
  await form.getByLabel(/^Label/).fill('  My test lab  ')
  await form.getByLabel('URL').fill('ldap://127.0.0.1:1390')
  await form.getByRole('button', exact('Save')).click()
  await expect(form).toHaveCount(0)
  const item = page.getByRole('listitem').filter({ hasText: 'My test lab' })
  await expect(item).toContainText('labeled-lab · ldap://127.0.0.1:1390')

  // The server trimmed it; clearing it brings the name back.
  await item.getByRole('button', exact('Edit My test lab')).click()
  form = page.getByRole('dialog', exact('Edit My test lab'))
  await expect(form.getByLabel(/^Label/)).toHaveValue('My test lab')
  await form.getByLabel(/^Label/).fill('')
  await form.getByRole('button', exact('Save')).click()
  await expect(form).toHaveCount(0)
  await expect(page.getByRole('button', exact('Edit labeled-lab'))).toBeVisible()
  await expect(page.getByText('My test lab')).toHaveCount(0)
})
