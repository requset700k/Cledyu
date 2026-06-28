// design-sync shim: provide a minimal `process.env` in the browser bundle.
// The Cledyu components read Next-injected env vars (e.g. NEXT_PUBLIC_WS_URL) at
// runtime. esbuild only substitutes process.env.NODE_ENV, so any other access
// throws "process is not defined" in a standalone preview. Defining an empty env
// makes those reads return undefined (the components' own fallback path) instead
// of crashing. Imported first via cfg.extraEntries so it runs before any component.
globalThis.process ??= { env: {} };
globalThis.process.env ??= {};
