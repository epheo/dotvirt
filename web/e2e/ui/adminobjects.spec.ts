import { expect, login, setScenario, test } from './fx';

// What the install asks a cluster admin to install: offered only to a caller
// the capability says may install it all, and installed with one click.

test('a cluster admin installs what the install asks for, and the banner clears', async ({
	page,
}) => {
	await setScenario(page, 'adminobjects');
	const calls: string[] = [];
	page.on('request', (r) => {
		if (r.url().includes('/api/admin-objects')) calls.push(r.method());
	});
	await login(page);
	const list = page.getByTestId('admin-objects');
	await expect(list).toBeVisible();
	// Each pending object, and what differs where another admin changed one.
	await expect(list.getByRole('listitem')).toHaveText([
		"Bounds the operator's Secrets (missing)",
		'Bounds the operator\'s Deployments (differs: ValidatingAdmissionPolicy dotvirt-operator-deployments lacks the rule "the operator mounts no host path")',
		"Bounds the operator's RBAC bindings (missing)",
	]);
	await expect(page.getByTestId('admin-pins')).toHaveText(
		'Pinned to system:serviceaccount:dotvirt-operator:dotvirt-operator-controller, namespace dotvirt, ArgoCD openshift-gitops (openshift-gitops-argocd-application-controller), images registry.example/team/dotvirt:custom.',
	);
	// The manifest an admin applies with kubectl instead: the listed objects, as one List.
	const [file] = await Promise.all([
		page.waitForEvent('download'),
		page.getByRole('button', { name: 'Download manifest' }).click(),
	]);
	expect(file.suggestedFilename()).toBe('dotvirt-admin-objects.json');
	const manifest = JSON.parse(
		await (await file.createReadStream()).toArray().then((c) => Buffer.concat(c).toString()),
	);
	expect(manifest.kind).toBe('List');
	expect(manifest.items.map((o: { metadata: { name: string } }) => o.metadata.name)).toEqual([
		'dotvirt-operator-secrets',
		'dotvirt-operator-deployments',
		'dotvirt-operator-bindings',
		'dotvirt-operator-secrets',
	]);
	await expect(
		page.getByText(
			'kubectl apply --server-side --field-manager=dotvirt -f dotvirt-admin-objects.json',
		),
	).toBeVisible();
	await page.getByRole('button', { name: 'Install', exact: true }).click();
	await expect(
		page.getByText('Installed what the install asked a cluster admin for.'),
	).toBeVisible();
	await expect(list).toHaveCount(0);
	expect(calls).toContain('POST');
});

test('a caller who may not install it is neither offered it nor reads it', async ({ page }) => {
	await setScenario(page, 'adminobjectsdenied');
	const calls: string[] = [];
	page.on('request', (r) => {
		if (r.url().includes('/api/admin-objects')) calls.push(r.method());
	});
	await login(page);
	await expect(page.getByRole('main')).toBeVisible();
	await expect(page.getByTestId('admin-objects')).toHaveCount(0);
	await expect(page.getByRole('button', { name: 'Install', exact: true })).toHaveCount(0);
	expect(calls).toEqual([]);
});
