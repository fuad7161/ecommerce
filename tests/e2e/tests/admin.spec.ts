import { test, expect, Page } from '@playwright/test';

const email = process.env.ADMIN_EMAIL || 'admin@example.com';
const password = process.env.ADMIN_PASSWORD || 'change-me-please';

async function login(page: Page) {
  await page.goto('/admin/login');
  await page.getByLabel('Email').fill(email);
  await page.getByLabel('Password').fill(password);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();
}

test('admin pages require login', async ({ page }) => {
  await page.goto('/admin/orders');
  await expect(page).toHaveURL(/\/admin\/login/);
});

test('wrong password is rejected', async ({ page }) => {
  await page.goto('/admin/login');
  await page.getByLabel('Email').fill(email);
  await page.getByLabel('Password').fill('definitely-wrong');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByText('invalid email or password')).toBeVisible();
});

test('admin fulfils an order end to end', async ({ page }) => {
  await login(page);
  await expect(page.getByText('Total revenue')).toBeVisible();

  await page.getByRole('link', { name: /Orders/ }).first().click();
  await page.getByLabel('Status').selectOption('pending');
  const first = page.locator('#results a[href^="/admin/orders/"]').first();
  await expect(first).toBeVisible();
  await first.click();

  page.on('dialog', (d) => d.accept());
  for (const next of ['Confirmed', 'Processing', 'Ready for shipment', 'Shipped', 'Delivered']) {
    await page.getByLabel('New status').selectOption({ label: next });
    await page.getByRole('button', { name: 'Update status' }).click();
    await expect(page.getByText(`Order status updated to ${next}.`)).toBeVisible();
  }
  // Delivered orders stay open only for a possible return; COD is now marked paid.
  await expect(page.getByLabel('New status')).toHaveValue('returned');
  await expect(page.getByText('cod · paid')).toBeVisible();
});

test('admin creates a draft product and previews it', async ({ page }) => {
  await login(page);
  await page.goto('/admin/products/new');
  const name = `E2E Draft ${Date.now()}`;
  await page.getByLabel('Product name').fill(name);
  await page.getByLabel('Regular price (৳)').fill('1000');
  await page.getByLabel('Selling price (৳)').fill('800');
  await page.getByLabel('Initial stock').fill('3');
  await page.getByRole('button', { name: 'Create product' }).click();
  await expect(page.getByText('Product saved.')).toBeVisible();

  const [preview] = await Promise.all([
    page.waitForEvent('popup'),
    page.getByRole('link', { name: 'Preview ↗' }).click(),
  ]);
  await expect(preview.getByText('Admin preview')).toBeVisible();
  await expect(preview.getByRole('heading', { name })).toBeVisible();

  // Drafts are not visible on the public storefront.
  const slug = new URL(preview.url()).pathname; // /admin/products/{id}/preview
  expect(slug).toContain('/preview');
  const res = await page.request.get(`/products?q=${encodeURIComponent(name)}`);
  expect(await res.text()).toContain('No products match');
});
