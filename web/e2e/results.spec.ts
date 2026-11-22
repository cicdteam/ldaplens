import { connect, exact, results } from './helpers'
import { expect, test } from './test'

for (const [width, height] of [
  [1280, 720],
  [1440, 900],
]) {
  test(`at ${width}x${height} the header cells line up with the rows and none is clipped`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height })
    await connect(page, 'demo')
    await page.goto('c/demo/search?filter=(objectClass=inetOrgPerson)')
    const grid = results(page)
    const heads = grid.getByRole('columnheader')
    const cells = grid.getByRole('row').nth(1).getByRole('gridcell')
    await expect(heads).toHaveText(['Name', 'uid', 'mail'])
    await expect(cells).toHaveCount(3)
    for (let i = 0; i < 3; i++) {
      const head = (await heads.nth(i).boundingBox())!
      const cell = (await cells.nth(i).boundingBox())!
      expect(Math.abs(cell.x - head.x), `column ${i} x`).toBeLessThanOrEqual(1)
      expect(Math.abs(cell.width - head.width), `column ${i} width`).toBeLessThanOrEqual(1)
    }
    // Nothing scrolls sideways, and the last column ends inside the grid.
    expect(await grid.evaluate((el) => el.scrollWidth - el.clientWidth)).toBe(0)
    const box = (await grid.boundingBox())!
    const last = (await heads.nth(2).boundingBox())!
    expect(last.x + last.width).toBeLessThanOrEqual(box.x + box.width)
  })
}

test('rows lead with the name; the DN is the row title and Mod+C copies it', async ({
  page,
  context,
}) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const dn = 'uid=user001,ou=people,dc=example,dc=org'
  await connect(page, 'demo')
  await page.goto('c/demo/search?filter=(uid=user001)')
  const row = results(page).getByRole('row').nth(1)
  // cn, since the demo people have no displayName; the RDN is uid=user001.
  await expect(row.getByRole('gridcell')).toHaveText(['User 001', 'user001', 'user001@example.org'])
  await expect(row).toHaveAttribute('title', dn)
  await row.click()
  await expect(row).toHaveAttribute('aria-selected', 'true')
  await expect(results(page)).toBeFocused()
  await page.keyboard.press('ControlOrMeta+C')
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(dn)
})

test('Name sorts by the name, not the DN', async ({ page }) => {
  await connect(page, 'demo')
  // By DN, cn=Jürgen Müller comes before uid=read; by name, E2E read comes first.
  await page.goto('c/demo/search?filter=(|(uid=read)(uid=jmueller))')
  const grid = results(page)
  const first = grid.getByRole('row').nth(1).getByRole('gridcell').first()
  await grid.getByRole('columnheader', exact('Name')).click()
  await expect(first).toHaveText('E2E read')
  await grid.getByRole('columnheader', exact('Name')).click()
  await expect(first).toHaveText('Jürgen Müller')
})

test('the rows stay virtual; End shows the last row below the header', async ({ page }) => {
  await connect(page, 'demo')
  await page.goto('c/demo/search?filter=(objectClass=inetOrgPerson)')
  const grid = results(page)
  await grid.getByRole('row').nth(1).click()
  await expect(grid.getByRole('row').nth(1)).toHaveAttribute('aria-selected', 'true')
  const total = Number(await grid.getAttribute('aria-rowcount')) - 1
  expect(total).toBeGreaterThan(250)
  expect(await grid.getByRole('row').count()).toBeLessThan(100)
  await page.keyboard.press('End')
  const last = grid.locator(`[id="result-${total - 1}"]`)
  await expect(last).toHaveAttribute('aria-selected', 'true')
  await expect(last).toBeInViewport()
  const head = (await grid.getByRole('columnheader').first().boundingBox())!
  const row = (await last.boundingBox())!
  const box = (await grid.boundingBox())!
  expect(head.y).toBe(box.y)
  expect(row.y).toBeGreaterThanOrEqual(head.y + head.height)
  expect(row.y + row.height).toBeLessThanOrEqual(box.y + box.height)
  expect(row.x).toBe(head.x)
})

test('a search the server answered only in part says which parts it refused', async ({ page }) => {
  // OpenDJ and 389 DS refuse an OR with an unindexed part with result 50; the backend then
  // searches part by part and names the attributes of the parts refused (Go tests cover
  // that). Here the answer is made to carry them.
  await page.route(
    (url) => url.pathname.includes('/api/') && url.pathname.endsWith('/search'),
    async (route) => {
      const res = await route.fetch()
      const body = await res.json()
      await route.fulfill({
        response: res,
        json: { ...body, skipped: ['displayName', 'description'] },
      })
    },
  )
  await connect(page, 'demo')
  await page.goto('c/demo/search?filter=(uid=user001)')
  await expect(
    page.getByText(
      'Not searched by displayName, description: the server refuses unindexed searches.',
    ),
  ).toBeVisible()
  await expect(results(page).getByRole('row').nth(1)).toBeVisible()
})
