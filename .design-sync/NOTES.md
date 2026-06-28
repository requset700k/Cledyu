# design-sync notes — cledyu-web

Synced to claude.ai/design project **Cledyu Web** (`projectId` in config.json). This is
the `apps/web` **Next.js 15 + Tailwind app** synced as the `package` shape — there is no
packaged component library, no Storybook, no `dist/`. Everything is wired through config +
shims so the components bundle and render standalone.

## Components (12)
- 9 lab/platform components (group `lab` + `general`).
- `LoginPage` (group `login`) — the REAL login launcher, re-exported from
  `app/(auth)/login/page.tsx` (default export → named in `.ds-entry.mjs`). Reads
  `CLEDYU_SOCIAL_LOGIN_PROVIDERS` env at render; previews set it per story.
- `KeycloakLogin`, `KeycloakRegister` (group `mockups`) — DESIGN MOCKUPS in
  `.design-sync/mockups/`, NOT shipped code. The real auth pages are Keycloak FTL + the
  `cledyu.css` theme (`infra/keycloak-theme/cledyu/login/`), which Claude Design (React-only)
  can't import. These React components reproduce that skin for design iteration.

## How the build is wired (read before re-syncing)
- **Synth entry**: `apps/web/.ds-entry.mjs` (generated, gitignored) re-exports the 9
  components + `Providers`, plus `LoginPage` (default re-export from the app page) and the two
  mockups (from `.design-sync/mockups/`). `cfg.entry` points at it. It lives *inside* `apps/web`
  so `PKG_DIR` resolves to the app (package.json name `cledyu-web`). Component list is
  pinned explicitly via `cfg.componentSrcMap` (synth mode finds no `.d.ts` exports).
- **WORKTREE BUILDS**: when building from a `git worktree` (e.g. to avoid disturbing another
  branch), symlink `node_modules` INTO the worktree (`ln -sfn <main>/apps/web/node_modules
  <wt>/apps/web/node_modules`) and pass `--node-modules <wt>/apps/web/node_modules`. If you
  point `--node-modules` at the main checkout instead, the workspace-containment root becomes
  the main repo and every worktree-relative cfg path (tsconfig shim, process polyfill, docsMap,
  readmeHeader) is silently rejected as "outside workspace" → next/* leaks back in, polyfill
  drops, docs/groups break. Also regenerate `.ds-entry.mjs` + `.ds-tailwind.css` (gitignored).
- **Tailwind CSS must be regenerated before every build.** This is a Tailwind app, so the
  utility classes are compiled into `apps/web/.ds-tailwind.css` (gitignored) and shipped via
  `cfg.cssEntry`. Regenerate with:
  ```sh
  cd apps/web && ./node_modules/.bin/tailwindcss -c tailwind.config.ts -i app/globals.css \
    -o .ds-tailwind.css \
    --content "./app/**/*.{ts,tsx},./components/**/*.{ts,tsx},../../.design-sync/previews/**/*.{ts,tsx},../../.design-sync/mockups/**/*.{ts,tsx}" --minify
  ```
  The shipped stylesheet is therefore a **compiled subset** — only classes used by the app +
  the authored previews are present (the conventions header tells the design agent this).
- **next/link + next/navigation are shimmed** to browser-safe versions via
  `.design-sync/tsconfig.dssync.json` `paths` → `.design-sync/shims/next-*`. The esbuild
  `tsconfigPathsPlugin` `onResolve` intercepts them before node resolution.
  - GOTCHA: do NOT put a `"//"` comment key in `tsconfig.dssync.json`. The plugin's
    comment-stripping regex mangles `"//": "..."` → JSON.parse fails → plugin returns NULL →
    esbuild silently falls back to `apps/web/tsconfig.json` (which has no Next mapping) →
    the REAL Next gets bundled → `process is not defined` / `__NEXT_*` errors and
    `[BUNDLE_EXPORT]` failures. Keep that tsconfig pure JSON.
- **process polyfill**: `.design-sync/shims/process-polyfill.mjs` (via `cfg.extraEntries`)
  defines `globalThis.process.env` so components reading `process.env.NEXT_PUBLIC_*` (esbuild
  only substitutes `NODE_ENV`) don't crash. Loads before the components.
- **Provider**: `cfg.provider = Providers` (the app's `QueryClientProvider`) so
  `LabSession`/`AiTutorPanel` (TanStack Query) render.
- **dtsPropsFor**: all 9 prop contracts are hand-written in config (no `.d.ts` in synth mode).
  Keep them in sync if the component props change.

## Toolchain (fresh clone)
- `.ds-sync/` deps: `npm i esbuild ts-morph @types/react playwright@1.58.0 playwright-core@1.58.0 typescript`
- Playwright **1.58.0** matches the cached chromium build **1208** (`~/.cache/ms-playwright`).
  If the cache build changes, find the matching playwright version (its `browsers.json`
  `chromium.revision` must equal the cached `chromium-<N>`).
- `typescript` must be installed in `.ds-sync/` for validate's `.d.ts` check (ts-morph
  vendors its own; the bare `typescript` package is a separate require).

## Build / validate / upload
```sh
# regenerate tailwind (above), then:
node .ds-sync/package-build.mjs --config .design-sync/config.json --node-modules apps/web/node_modules --out ./ds-bundle
node .ds-sync/package-validate.mjs ./ds-bundle
# re-sync driver (fetch project _ds_sync.json -> .design-sync/.cache/remote-sync.json first):
node .ds-sync/resync.mjs --config .design-sync/config.json --node-modules apps/web/node_modules --out ./ds-bundle --remote .design-sync/.cache/remote-sync.json
```

## Known render notes (triaged, not failures)
- `LabTerminal`/`LabWorkspace`/`LabSession` attempt WebSocket/fetch to `/api/*` at render;
  with no backend these silently fail and the components show their connecting/placeholder
  state. That IS the correct standalone preview.
- `overrides`: Navbar/LabSession/LabWorkspace/LabTerminal use `cardMode: single`;
  LabCard/StepList/TerminalPlaceholder use `cardMode: column` (resolved GRID_OVERFLOW on
  their multi-story wide cards).

## Re-sync risks (what can silently go stale)
- **Keycloak mockups drift**: `KeycloakLogin`/`KeycloakRegister` are hand-built React
  reproductions of the Keycloak `cledyu.css` theme. They are NOT linked to the real theme — if
  `infra/keycloak-theme/cledyu/login/resources/css/cledyu.css` or the Keycloak templates
  change, the mockups must be updated by hand. They will not auto-track.
- **LoginPage env**: `LoginPage` reads `CLEDYU_SOCIAL_LOGIN_PROVIDERS`; the process polyfill
  makes it default to empty (no social). Previews set the env per story to show variants.
- **`.ds-tailwind.css` is generated, not committed.** If a future run forgets to regenerate
  it (or `apps/web/tailwind.config.ts` changes), the shipped CSS drifts from the components.
  Always regenerate before building. If you add a NEW class in a preview, regenerate or it
  won't ship.
- **Generated inputs are gitignored** (`apps/web/.ds-entry.mjs`, `apps/web/.ds-tailwind.css`):
  they must be recreated on a fresh clone before building (entry is deterministic — re-write
  it from the component list; tailwind via the command above).
- **dtsPropsFor is hand-maintained** — it will not track real prop changes in the components.
  Diff component signatures on re-sync.
- **Next shims are pinned to current usage**: components only use `next/link` default +
  `usePathname`. If a component starts using `useRouter`/`useSearchParams` meaningfully (the
  shims return no-op/empty), the preview may misrender — extend the shim.
- **App-coupling**: these are app components, not a stable library. Any refactor of
  `apps/web/components` or `apps/web/lib` can change what bundles; re-grade after big changes.
