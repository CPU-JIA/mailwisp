import { expect, test } from '@playwright/test'

test('renders the Chinese welcome screen and switches language/theme', async ({ page }) => {
  await page.goto('/')
  await expect(page).toHaveTitle('MailWisp')
  await expect(page.getByRole('heading', { level: 1 })).toContainText('给下一封陌生来信')
  await page.locator('select').first().selectOption('en-US')
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Give the next unknown message')
  await page.locator('select').nth(1).selectOption('mist')
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'mist')
  await expect(page.locator('body')).not.toContainText('undefined')
})
