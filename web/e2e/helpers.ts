import { expect, type Page } from '@playwright/test';

const TOKEN = process.env.OC_TOKEN ?? '';

// login authenticates with the OpenShift token and waits for the inventory shell.
export async function login(page: Page) {
	if (!TOKEN) throw new Error('OC_TOKEN env var is required for the e2e tests');
	await page.goto('/');
	await page.waitForSelector('textarea');
	await page.fill('textarea', TOKEN);
	await page.click('button[type="submit"]');
	// "New VM" in the header is unambiguous and appears once authenticated ("All VMs"
	// shows in both the tree and the breadcrumb, so it's not a unique anchor).
	await expect(page.getByRole('button', { name: /New VM/ })).toBeVisible();
}

// openFirstVM switches to the VMs tab and opens the first VM's detail view.
export async function openFirstVM(page: Page) {
	await page.getByRole('button', { name: 'VMs', exact: true }).click();
	const row = page.locator('tbody tr').first();
	await expect(row).toBeVisible();
	await row.click();
	await expect(page.getByRole('button', { name: /Edit Settings/ })).toBeVisible();
}
