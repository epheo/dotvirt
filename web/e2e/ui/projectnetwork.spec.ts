import { expect, login, setScenario, test } from './fx';

// A project with a VM Network its namespaces share: a new namespace joins it
// unless told otherwise, and the request says so.

test("a new namespace joins its project's VM Network by default", async ({ page }) => {
	await setScenario(page, 'projectnetwork');
	await login(page);
	await page.goto('/compute');
	await page.locator('aside').getByText('team-web', { exact: true }).click({ button: 'right' });
	await page.getByRole('button', { name: 'New Namespace here…' }).click();
	const dialog = page.getByRole('dialog');
	await expect(dialog.getByRole('combobox', { name: 'VM Network' })).toHaveValue('project');
	await dialog.getByRole('textbox').first().fill('web-edge');
	const sent = page.waitForRequest(
		(r) => r.url().endsWith('/api/namespaces') && r.method() === 'POST',
	);
	await dialog.getByRole('button', { name: 'Stage namespace' }).click();
	const body = (await sent).postDataJSON();
	expect(body).toMatchObject({ name: 'web-edge', project: 'team-web', projectNetwork: true });
	expect(body.vmNetwork).toBeUndefined();
});
