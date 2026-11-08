import { api, type Template } from '#lib/api.js';

// What the manifest editor needs for one declared object or library template:
// the version it was read at, the stage that sends that version back (the
// server refuses an edit git has moved past), and the re-read of the current
// version to make the edit again on.

type Edit = {
	baseBlob?: string;
	onsubmit: (yaml: string, baseBlob?: string) => Promise<unknown>;
	onreload: () => Promise<{ yaml: string; baseBlob?: string }>;
};

export function objectManifestEdit(o: {
	resource: string;
	namespace: string;
	name: string;
	baseBlob?: string;
}): Edit {
	return {
		baseBlob: o.baseBlob,
		onsubmit: (yaml, baseBlob) =>
			api.updateObjectManifest(o.resource, o.namespace, o.name, yaml, baseBlob),
		onreload: () =>
			api
				.objectSpec(o.resource, o.namespace, o.name)
				.then((s) => ({ yaml: s.manifest, baseBlob: s.baseBlob })),
	};
}

export function templateManifestEdit(t: Pick<Template, 'library' | 'name' | 'baseBlob'>): Edit {
	return {
		baseBlob: t.baseBlob,
		onsubmit: (yaml, baseBlob) =>
			api.updateTemplate({ library: t.library, name: t.name, yaml, baseBlob }),
		onreload: async () => {
			const { templates } = await api.templates();
			const now = templates.find((x) => x.library === t.library && x.name === t.name);
			if (!now) throw new Error(`${t.name} is no longer in ${t.library}`);
			return { yaml: now.yaml, baseBlob: now.baseBlob };
		},
	};
}
