import { test, expect } from '@playwright/test';

// Full guest journey: browse → search → product → cart (persisted) → checkout → confirmation → tracking.
test('guest can buy with cash on delivery and track the order', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible();

  // Search through the backend-driven listing.
  await page.goto('/products?q=earbuds');
  await expect(page.getByText(/product(s)? found/)).toBeVisible();
  await page.getByRole('link', { name: /Wireless Earbuds Pro/ }).first().click();
  await expect(page.getByRole('heading', { name: 'Wireless Earbuds Pro' })).toBeVisible();

  await page.getByRole('button', { name: 'Increase quantity' }).click();
  await page.getByRole('button', { name: 'Add to cart' }).click();
  await expect(page.getByText('Added to cart')).toBeVisible();

  // Cart survives a reload (localStorage) and is priced by the server.
  await page.goto('/cart');
  await page.reload();
  await expect(page.getByText('Wireless Earbuds Pro')).toBeVisible();
  await expect(page.getByText(/Subtotal \(2 items\)/)).toBeVisible();

  await page.getByRole('link', { name: 'Proceed to checkout' }).click();
  await expect(page.getByRole('heading', { name: 'Checkout' })).toBeVisible();

  // Invalid phone is rejected server-side.
  await page.getByLabel('Full name').fill('Playwright Buyer');
  await page.getByLabel('Mobile number').fill('12345');
  await page.getByLabel('Delivery area').selectOption({ index: 1 });
  await page.getByLabel('District / City').fill('Dhaka');
  await page.getByLabel('Area / Thana').fill('Mirpur');
  await page.getByLabel('Full address').fill('House 10, Road 2, Section 6');
  await page.getByLabel(/I confirm my details/).check();
  await page.getByRole('button', { name: 'Place order' }).click();
  await expect(page.getByText('Enter a valid Bangladeshi mobile number')).toBeVisible();

  await page.getByLabel('Mobile number').fill('01755555555');
  // The form is re-rendered with the previous answers; make sure the confirmation is ticked.
  await page.getByLabel(/I confirm my details/).check();
  await expect(page.locator('#cart-summary')).toContainText('Total payable');
  await page.getByRole('button', { name: 'Place order' }).click();

  await expect(page.getByRole('heading', { name: /Thank you, Playwright Buyer/ })).toBeVisible();
  const orderNumber = (await page.locator('text=/^\\d{6}-\\d{5}$/').first().textContent())!.trim();
  expect(orderNumber).toMatch(/^\d{6}-\d{5}$/);

  // The cart is emptied after a successful order.
  await expect(page.locator('[data-cart-count]').first()).toBeHidden();

  // Tracking needs both order number and phone.
  await page.goto('/track');
  await page.getByLabel('Order number').fill(orderNumber);
  await page.getByLabel('Mobile number').fill('01799999999');
  await page.getByRole('button', { name: /Track/ }).click();
  await expect(page.getByText("We couldn't find an order")).toBeVisible();

  await page.getByLabel('Mobile number').fill('01755555555');
  await page.getByRole('button', { name: /Track/ }).click();
  await expect(page.locator('#track-result')).toContainText('Pending confirmation');
});

test('out-of-stock products cannot be added to the cart', async ({ page }) => {
  await page.goto('/product/smart-watch-s8');
  await expect(page.getByText('Out of stock').first()).toBeVisible();
  await expect(page.getByRole('button', { name: 'Add to cart' })).toBeDisabled();
});
