// The create sheet's slice of the fixture: the extra base data its defaults
// read (an overlapping pair of networks, a project network with no range,
// templates) and the create routes it posts to, each with the backend's
// ?dryRun=true twin that answers what staging would write.

// ── base data ─────────────────────────────────────────────────────────────────

// Two project networks on one range (the overlap the network form names) and
// one without addresses.
export const createNetworks = [
	{
		name: 'web-internal',
		kind: 'internal',
		scope: 'project',
		namespace: 'web-prod',
		attachRef: 'web-prod/web-internal',
		backing: 'UserDefinedNetwork',
		topology: 'Layer2',
		sourceFile: 'web-prod/networks/web-internal.yaml',
		sync: 'Synced',
	},
	{
		name: 'udn-frontend',
		kind: 'internal',
		scope: 'project',
		namespace: 'web-prod',
		subnets: ['10.200.0.0/16'],
		attachRef: 'web-prod/udn-frontend',
		backing: 'UserDefinedNetwork',
		topology: 'Layer2',
		sync: 'Synced',
	},
	{
		name: 'udn-tcp-frontend',
		kind: 'internal',
		scope: 'project',
		namespace: 'batch-prod',
		subnets: ['10.200.0.0/16'],
		attachRef: 'batch-prod/udn-tcp-frontend',
		backing: 'UserDefinedNetwork',
		topology: 'Layer2',
		sync: 'Synced',
	},
];

const TEMPLATE_YAML = (name) =>
	`apiVersion: template.kubevirt.io/v1beta1\nkind: VirtualMachineTemplate\nmetadata:\n  name: ${name}\n`;

export const createTemplates = {
	templates: [
		{
			name: 'fedora-server',
			library: 'platform',
			description: 'Fedora 42 with the web stack, 2 vCPU 4 GiB',
			sourceFile: 'templates/fedora-server.yaml',
			instancetype: 'u1.medium',
			preference: 'fedora',
			parameters: [
				{ name: 'NAME', generate: 'expression', from: 'fedora-[a-z0-9]{5}' },
				{
					name: 'SSH_KEY',
					displayName: 'SSH public key',
					description: 'Added to the default user',
				},
			],
			yaml: TEMPLATE_YAML('fedora-server'),
		},
		{
			name: 'web-node',
			library: 'team-web',
			description: 'A web-prod node on web-net',
			sourceFile: 'templates/web-node.yaml',
			instancetype: 'u1.medium',
			preference: 'fedora',
			parameters: [
				{ name: 'NAME', value: 'web-node' },
				{ name: 'ROLE', displayName: 'Role', required: true },
			],
			yaml: TEMPLATE_YAML('web-node'),
		},
	],
};

// ── manifests: a JS twin of vmgen/netgen, close enough to read ─────────────────

function yaml(v, indent = '') {
	if (Array.isArray(v)) {
		if (!v.length) return ' []';
		return v
			.map((x) => {
				const body = yaml(x, indent + '  ');
				return `\n${indent}-${typeof x === 'object' && x ? body.replace(/^\n\s*/, ' ') : body}`;
			})
			.join('');
	}
	if (v && typeof v === 'object') {
		const keys = Object.keys(v).filter((k) => v[k] !== undefined);
		if (!keys.length) return ' {}';
		return keys.map((k) => `\n${indent}${k}:${yaml(v[k], indent + '  ')}`).join('');
	}
	if (typeof v === 'string' && v.includes('\n'))
		return ` |\n${v
			.replace(/\n$/, '')
			.split('\n')
			.map((l) => indent + l)
			.join('\n')}`;
	return ` ${typeof v === 'string' && /^[\d$:{}[\],&*#?|<>=!%@`'"-]|: /.test(v) ? JSON.stringify(v) : v}`;
}
const doc = (o) => yaml(o).replace(/^\n/, '') + '\n';

const DNS1123 = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;
const CIDR = /^(\d{1,3}\.){3}\d{1,3}\/\d{1,2}$|^[0-9a-fA-F:]+\/\d{1,3}$/;

class Refused extends Error {
	constructor(status, msg) {
		super(msg);
		this.status = status;
	}
}
const need = (ok, msg) => {
	if (!ok) throw new Refused(400, msg);
};
const named = (what, v) =>
	need(DNS1123.test(v ?? '') && v.length <= 63, `invalid ${what} "${v ?? ''}"`);

function vmManifest(s) {
	named('name', s.name);
	named('namespace', s.namespace);
	need(s.instancetype, 'instancetype is required');
	need(s.preference, 'preference is required');
	need(s.osImage?.name, 'osImage is required');
	const ci = s.cloudInit;
	const userData = ci
		? [
				'#cloud-config',
				ci.user && `user: ${ci.user}`,
				ci.password && 'password: $2a$10$fixture.hash.of.the.typed.password',
				ci.password && 'chpasswd: { expire: False }',
				ci.password && !ci.sshKey && 'ssh_pwauth: True',
				ci.sshKey && `ssh_authorized_keys:\n  - ${ci.sshKey}`,
			]
				.filter(Boolean)
				.join('\n') + '\n'
		: undefined;
	const extra = s.extraDisks ?? [];
	const nets = s.networks ?? [];
	const primary = s.primaryNetwork !== false;
	const iface = (ref) => ref.slice(ref.lastIndexOf('/') + 1);
	const storage = (size, cls) => ({
		resources: { requests: { storage: size } },
		storageClassName: cls || undefined,
	});
	return doc({
		apiVersion: 'kubevirt.io/v1',
		kind: 'VirtualMachine',
		metadata: { name: s.name, namespace: s.namespace, labels: s.labels },
		spec: {
			runStrategy: s.running ? 'Always' : 'Halted',
			instancetype: { name: s.instancetype },
			preference: { name: s.preference },
			dataVolumeTemplates: [
				{
					metadata: { name: `${s.name}-rootdisk` },
					spec: {
						sourceRef: { kind: 'DataSource', name: s.osImage.name, namespace: s.osImage.namespace },
						storage: storage(s.diskSize || '30Gi', s.storageClass),
					},
				},
				...extra.map((d) => ({
					metadata: { name: `${s.name}-${d.name}` },
					spec: { source: { blank: {} }, storage: storage(d.size || '10Gi', d.storageClass) },
				})),
			],
			template: {
				spec: {
					domain: {
						devices: {
							disks: [
								{ name: 'rootdisk', disk: { bus: 'virtio' } },
								...extra.map((d) => ({ name: d.name, disk: { bus: 'virtio' } })),
								...(userData ? [{ name: 'cloudinitdisk', disk: { bus: 'virtio' } }] : []),
							],
							interfaces: [
								...(primary ? [{ name: 'default', masquerade: {} }] : []),
								...nets.map((n) => ({ name: iface(n.name), bridge: {} })),
							],
						},
					},
					networks: [
						...(primary ? [{ name: 'default', pod: {} }] : []),
						...nets.map((n) => ({ name: iface(n.name), multus: { networkName: n.name } })),
					],
					volumes: [
						{ name: 'rootdisk', dataVolume: { name: `${s.name}-rootdisk` } },
						...extra.map((d) => ({ name: d.name, dataVolume: { name: `${s.name}-${d.name}` } })),
						...(userData ? [{ name: 'cloudinitdisk', cloudInitNoCloud: { userData } }] : []),
					],
				},
			},
		},
	});
}

function networkManifest(s) {
	named('network name', s.name);
	for (const c of s.subnets ?? [])
		need(CIDR.test(c), `subnet "${c}" must be a CIDR (e.g. 10.0.0.0/24)`);
	const layer2 = s.subnets?.length
		? { role: 'Secondary', subnets: s.subnets }
		: { role: 'Secondary', ipam: { mode: 'Disabled' } };
	const selector = {
		matchExpressions: [
			{ key: 'kubernetes.io/metadata.name', operator: 'In', values: s.namespaces ?? [] },
		],
	};
	if ((s.scope ?? 'project') === 'project') {
		named('namespace', s.namespace);
		return [
			`${s.namespace}/networks/${s.name}.yaml`,
			doc({
				apiVersion: 'k8s.ovn.org/v1',
				kind: 'UserDefinedNetwork',
				metadata: { name: s.name, namespace: s.namespace },
				spec: { topology: 'Layer2', layer2 },
			}),
		];
	}
	need(s.namespaces?.length, 'at least one namespace must be selected');
	const network =
		s.scope === 'vlan'
			? {
					topology: 'Localnet',
					localnet: {
						role: 'Secondary',
						physicalNetworkName: s.physicalNetwork,
						vlan: { mode: 'Access', access: { id: s.vlan } },
						subnets: s.subnets?.length ? s.subnets : undefined,
						ipam: s.subnets?.length ? undefined : { mode: 'Disabled' },
					},
				}
			: { topology: 'Layer2', layer2 };
	if (s.scope === 'vlan') {
		need(s.physicalNetwork, 'an uplink (physical network) is required for a VLAN network');
		need(s.vlan >= 1 && s.vlan <= 4094, 'a VLAN id in 1..4094 is required');
	}
	return [
		`networks/${s.name}.yaml`,
		doc({
			apiVersion: 'k8s.ovn.org/v1',
			kind: 'ClusterUserDefinedNetwork',
			metadata: { name: s.name },
			spec: { namespaceSelector: selector, network },
		}),
	];
}

const nsSelector = (namespaces) => ({
	matchExpressions: [{ key: 'kubernetes.io/metadata.name', operator: 'In', values: namespaces }],
});

// Every create route: where it lands and what it renders. tier is 'tenant'
// (the project owning body.namespace) or 'platform'.
const ROUTES = {
	'/api/vms': {
		tier: () => 'tenant',
		render: (b) => [{ path: `${b.namespace}/${b.name}.yaml`, yaml: vmManifest(b), name: b.name }],
	},
	'/api/templates/deploy': {
		tier: () => 'tenant',
		render: (b, s) => {
			const t = (s.templates?.templates ?? []).find(
				(x) => x.name === b.template && (!b.library || x.library === b.library),
			);
			if (!t)
				throw new Refused(404, `template "${b.template}" not in library "${b.library ?? ''}"`);
			const name = b.name || `${b.template}-${Math.random().toString(36).slice(2, 7)}`;
			named('rendered VM name', name);
			const yamlText = doc({
				apiVersion: 'kubevirt.io/v1',
				kind: 'VirtualMachine',
				metadata: { name, namespace: b.namespace },
				spec: {
					runStrategy: b.powerOn ? 'Always' : 'Halted',
					instancetype: t.instancetype ? { name: t.instancetype } : undefined,
					preference: t.preference ? { name: t.preference } : undefined,
				},
			});
			return [{ path: `${b.namespace}/${name}.yaml`, yaml: yamlText, name }];
		},
	},
	'/api/networks': {
		tier: (b) => ((b.scope ?? 'project') === 'project' ? 'tenant' : 'platform'),
		render: (b) => {
			const [path, y] = networkManifest(b);
			return [{ path, yaml: y, name: b.name, resource: 'network' }];
		},
	},
	'/api/networkpolicies': {
		tier: () => 'tenant',
		render: (b) => {
			named('name', b.name);
			return [
				{
					path: `${b.namespace}/networkpolicies/${b.name}.yaml`,
					name: b.name,
					resource: 'networkpolicy',
					yaml: doc({
						apiVersion: 'networking.k8s.io/v1',
						kind: 'NetworkPolicy',
						metadata: { name: b.name, namespace: b.namespace },
						spec: {
							podSelector: b.appliedTo ? { matchLabels: b.appliedTo } : {},
							policyTypes: ['Ingress'],
							ingress: (b.ingress ?? []).map((r) => ({
								from: r.from?.map((l) => ({ podSelector: { matchLabels: l } })),
								ports: r.ports,
							})),
						},
					}),
				},
			];
		},
	},
	'/api/egressfirewalls': {
		tier: () => 'tenant',
		render: (b) => {
			need(b.rules?.length, 'at least one rule is required');
			return [
				{
					path: `${b.namespace}/egressfirewalls/default.yaml`,
					name: 'default',
					resource: 'egressfirewall',
					yaml: doc({
						apiVersion: 'k8s.ovn.org/v1',
						kind: 'EgressFirewall',
						metadata: { name: 'default', namespace: b.namespace },
						spec: {
							egress: b.rules.map((r) => ({
								type: r.action,
								to: r.cidr ? { cidrSelector: r.cidr } : { dnsName: r.dnsName },
								ports: r.ports,
							})),
						},
					}),
				},
			];
		},
	},
	'/api/adminnetworkpolicies': {
		tier: () => 'platform',
		render: (b) => {
			if (!b.baseline) named('name', b.name);
			const kind = b.baseline ? 'BaselineAdminNetworkPolicy' : 'AdminNetworkPolicy';
			const name = b.baseline ? 'default' : b.name;
			return [
				{
					path: `${kind.toLowerCase()}s/${name}.yaml`,
					name,
					resource: b.baseline ? 'baselineadminnetworkpolicy' : 'adminnetworkpolicy',
					yaml: doc({
						apiVersion: 'policy.networking.k8s.io/v1alpha1',
						kind,
						metadata: { name },
						spec: {
							priority: b.baseline ? undefined : b.priority,
							subject: { namespaces: b.subject ? { matchLabels: b.subject } : {} },
							ingress: (b.ingress ?? []).map((r, i) => ({
								name: `rule-${i + 1}`,
								action: r.action,
								from: r.peers.map((p) => ({ namespaces: { matchLabels: p } })),
							})),
						},
					}),
				},
			];
		},
	},
	'/api/egressips': {
		tier: () => 'platform',
		render: (b) => {
			named('name', b.name);
			return [
				{
					path: `egressips/${b.name}.yaml`,
					name: b.name,
					resource: 'egressip',
					yaml: doc({
						apiVersion: 'k8s.ovn.org/v1',
						kind: 'EgressIP',
						metadata: { name: b.name },
						spec: { egressIPs: b.egressIPs, namespaceSelector: nsSelector(b.namespaces) },
					}),
				},
			];
		},
	},
	'/api/externalroutes': {
		tier: () => 'platform',
		render: (b) => {
			named('name', b.name);
			return [
				{
					path: `externalroutes/${b.name}.yaml`,
					name: b.name,
					resource: 'externalroute',
					yaml: doc({
						apiVersion: 'k8s.ovn.org/v1',
						kind: 'AdminPolicyBasedExternalRoute',
						metadata: { name: b.name },
						spec: {
							from: { namespaceSelector: nsSelector(b.namespaces) },
							nextHops: { static: b.nextHops.map((ip) => ({ ip })) },
						},
					}),
				},
			];
		},
	},
	'/api/uplinks': {
		tier: () => 'platform',
		render: (b) => {
			named('uplink name', b.name);
			return [
				{
					path: `uplinks/${b.name}.yaml`,
					name: `${b.name}-bridge`,
					resource: 'uplink',
					yaml: doc({
						apiVersion: 'nmstate.io/v1',
						kind: 'NodeNetworkConfigurationPolicy',
						metadata: { name: `${b.name}-bridge` },
					}),
				},
			];
		},
	},
	'/api/namespaces': {
		tier: () => 'platform',
		render: (b) => [namespaceFile(b.name, b.project, b.vmNetwork)],
	},
	'/api/projects': {
		tier: () => 'platform',
		render: (b, s) => {
			named('project name', b.name);
			if (s.inventory.projects.some((p) => p.name === b.name))
				throw new Refused(409, 'that project already exists; adopt it instead of creating it');
			const ns = b.namespace || b.name;
			const files = [namespaceFile(ns, b.name, b.vmNetwork)];
			if (b.owners?.length)
				files.push({
					path: `rbac/${ns}.yaml`,
					name: `${ns}-admins`,
					resource: 'rolebinding',
					yaml: doc({
						apiVersion: 'rbac.authorization.k8s.io/v1',
						kind: 'RoleBinding',
						metadata: { name: `${ns}-admins`, namespace: ns },
						roleRef: { kind: 'ClusterRole', name: 'admin' },
						subjects: b.owners.map((name) => ({ kind: 'User', name })),
					}),
				});
			return files;
		},
	},
};

function namespaceFile(name, project, vmNetwork) {
	named('namespace name', name);
	const docs = [
		doc({
			apiVersion: 'v1',
			kind: 'Namespace',
			metadata: {
				name,
				labels: {
					'dotvirt.io/project': project,
					'k8s.ovn.org/primary-user-defined-network': vmNetwork ? '' : undefined,
				},
				annotations: { 'dotvirt.io/repo': `dotvirt/${project}.git` },
			},
		}),
	];
	if (vmNetwork)
		docs.push(
			doc({
				apiVersion: 'k8s.ovn.org/v1',
				kind: 'UserDefinedNetwork',
				metadata: { name: vmNetwork.name, namespace: name },
				spec: { topology: 'Layer2', layer2: { role: 'Primary', subnets: [vmNetwork.subnet] } },
			}),
		);
	return {
		path: `namespaces/${name}.yaml`,
		name,
		resource: 'namespace',
		yaml: docs.join('---\n'),
	};
}

// ── the routes ────────────────────────────────────────────────────────────────

// Answers a create route (or its dry run) when path is one; false otherwise.
// ctx carries the server's helpers so this module owns no state of its own.
export async function handleCreate(req, res, url, ctx) {
	const route = req.method === 'POST' && ROUTES[url.pathname];
	if (!route) return false;
	const { state, sendJSON, sendText, readBody, projectOf, stageInto } = ctx;
	const body = await readBody(req);
	if (!body) {
		sendText(res, 400, 'invalid JSON body');
		return true;
	}
	try {
		let project = 'platform';
		if (route.tier(body) === 'tenant') {
			need(body.namespace, 'spec namespace is required');
			const p = projectOf(body.namespace);
			if (!p) throw new Refused(404, 'namespace not found in any visible project');
			project = p.name;
		}
		const files = route.render(body, state.scenario);
		const declared = new Set(
			(state.scenario.networks?.networks ?? []).map((n) => n.sourceFile).filter(Boolean),
		);
		const vmTaken =
			url.pathname === '/api/vms' || url.pathname === '/api/templates/deploy'
				? state.scenario.inventory.projects
						.flatMap((p) => p.namespaces)
						.flatMap((n) => n.vms)
						.some((v) => v.namespace === body.namespace && v.name === files[0].name)
				: false;
		if (vmTaken) throw new Refused(409, `${body.namespace}/${files[0].name} already exists in git`);
		const kind = (f) => (declared.has(f.path) ? 'edit' : 'create');
		if (url.searchParams.get('dryRun') === 'true') {
			sendJSON(res, 200, {
				project,
				files: files.map((f) => ({ kind: kind(f), path: f.path, yaml: f.yaml })),
			});
			return true;
		}
		for (const f of files)
			stageInto(project, {
				kind: kind(f),
				resource: f.resource,
				namespace: route.tier(body) === 'tenant' ? body.namespace : 'cluster',
				name: f.name,
				changes: [{ field: 'manifest', action: 'add', to: f.path }],
				yaml: f.yaml,
			});
		sendJSON(res, 200, ctx.draftFor(project));
	} catch (e) {
		if (!(e instanceof Refused)) throw e;
		sendText(res, e.status, e.message);
	}
	return true;
}

// The image upload: the target is created at once, then reports ready, takes
// the bytes at a fixture URL and finishes importing on the next status read.
export async function handleUpload(req, res, url, ctx) {
	const { sendJSON, readBody } = ctx;
	if (url.pathname === '/api/uploads' && req.method === 'POST') {
		const b = (await readBody(req)) ?? {};
		ctx.state.uploads ??= new Map();
		ctx.state.uploads.set(`${b.namespace}/${b.name}`, 'UploadReady');
		sendJSON(res, 200, { namespace: b.namespace, name: b.name });
		return true;
	}
	let m = url.pathname.match(/^\/api\/uploads\/([^/]+)\/([^/]+)$/);
	if (m && req.method === 'GET') {
		const key = `${m[1]}/${m[2]}`;
		const phase = ctx.state.uploads?.get(key) ?? 'Pending';
		if (phase === 'Uploaded') ctx.state.uploads.set(key, 'Succeeded');
		sendJSON(res, 200, { phase, ready: phase === 'Succeeded', progress: '100.0%' });
		return true;
	}
	m = url.pathname.match(/^\/api\/uploads\/([^/]+)\/([^/]+)\/token$/);
	if (m && req.method === 'POST') {
		sendJSON(res, 200, {
			token: 'fixture-upload-token',
			uploadUrl: `/__fixture/upload/${m[1]}/${m[2]}`,
		});
		return true;
	}
	m = url.pathname.match(/^\/__fixture\/upload\/([^/]+)\/([^/]+)$/);
	if (m && req.method === 'POST') {
		req.resume();
		req.on('end', () => {
			ctx.state.uploads?.set(`${m[1]}/${m[2]}`, 'Uploaded');
			res.writeHead(200);
			res.end();
		});
		return true;
	}
	return false;
}
