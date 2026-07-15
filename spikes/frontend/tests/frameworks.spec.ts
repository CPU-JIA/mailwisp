import { expect, test } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

for (const framework of ['native', 'vue', 'react', 'svelte']) {
  test(`${framework}完成统一交互切片`, async ({ page }) => {
    const errors: string[] = []
    page.on('console', message => {
      if (message.type() === 'error') errors.push(message.text())
    })

    await page.goto(`/${framework}/`)
    await expect(page.getByRole('heading', { name: 'MailWisp' })).toBeVisible()
    await expect(page.getByText('来信即现，过时即逝。')).toBeVisible()

    const selects = page.locator('select')
    await selects.nth(0).selectOption('en-US')
    await expect(page.getByText('Fast mail. Zero trace.')).toBeVisible()
    await expect(page.locator('html')).toHaveAttribute('lang', 'en-US')

    await selects.nth(1).selectOption('mist')
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'mist')

    await page.getByPlaceholder('Paste a temporary inbox access token').fill('test-token')
    await page.getByRole('button', { name: 'Open inbox' }).click()
    await expect(page.getByRole('heading', { name: 'Inbox' })).toBeVisible()
    await page.getByRole('button', { name: /Your verification code/ }).click()
    await expect(page.getByText('Use 482913 to finish signing in.')).toBeVisible()
    await page.getByRole('button', { name: /Back to inbox/ }).click()
    await expect(page.getByRole('heading', { name: 'Inbox' })).toBeVisible()

    const accessibility = await new AxeBuilder({ page }).analyze()
    expect(accessibility.violations).toEqual([])

    expect(errors).toEqual([])
  })
}
