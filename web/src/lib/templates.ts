import type { Template } from './api';

// The platform repo's library, offered to every project.
export const libraryLabel = (lib: string) => (lib === 'platform' ? 'Shared library' : lib);

export const templateKey = (t: Template) => `${t.library}/${t.name}`;

// What a project deploys from: its own library and the shared one. Without a
// project, every library the caller reads.
export const templatesFor = (templates: Template[], project?: string): Template[] =>
	project ? templates.filter((t) => t.library === project || t.library === 'platform') : templates;
