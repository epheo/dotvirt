import type { Template, TemplateSeed } from './api';

// The platform repo's library, offered to every project.
export const libraryLabel = (lib: string) => (lib === 'platform' ? 'Shared library' : lib);

export const templateKey = (t: Template) => `${t.library}/${t.name}`;

// What a project deploys from: its own library and the shared one. Without a
// project, every library the caller reads.
export const templatesFor = (templates: Template[], project?: string): Template[] =>
	project ? templates.filter((t) => t.library === project || t.library === 'platform') : templates;

// The starter-library offers pending in the libraries templatesFor lists; the
// shared library is never offered one.
export const seedsFor = (seeds: TemplateSeed[], project?: string): TemplateSeed[] =>
	project ? seeds.filter((s) => s.library === project) : seeds;
