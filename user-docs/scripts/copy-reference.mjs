import { cp } from 'node:fs/promises';

// Keep the standalone data-model viewer independent of the article app.
await cp(new URL('../data-model/', import.meta.url), new URL('../static/data-model/', import.meta.url), { recursive: true });
