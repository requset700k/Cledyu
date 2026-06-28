## Cledyu Web — how to build with these components

Cledyu is a Korean-language hands-on lab platform (Kubernetes/Linux/Terraform practice
in browser VMs). These are its real shipped React components. UI text is Korean.

### Setup & wrapping
- **Dark theme is mandatory.** Every component is designed for a dark page. Wrap your
  screens in a dark surface or text is unreadable: `<div className="bg-slate-950 min-h-screen">…</div>`.
  The body-level dark default (incl. slate-100 text) ships in `styles.css`, but set the
  background on your own containers too.
- **Data components need the query provider.** `LabSession` and `AiTutorPanel` use
  TanStack Query. Wrap them (or the whole app) in `Providers` (exported from the bundle —
  it is the `QueryClientProvider`): `<Providers><LabSession … /></Providers>`.
- `Navbar` and `LabCard` use `next/link`/`next/navigation` internally — render them inside
  a Next.js App Router app. (In these previews those are shimmed to plain anchors.)

### Styling idiom — Tailwind utility classes
There are **no style props and no CSS-class exports**. Style your own layout glue with
Tailwind utilities, matching this palette so it reads as Cledyu:

| Role | Classes |
|---|---|
| Brand accent (buttons, active tabs, links, progress) | `bg-brand-500` `hover:bg-brand-600` `text-brand-400` `border-brand-500/50` (scale `brand-50…900`, sky-based) |
| Surfaces | page `bg-slate-950`; cards `bg-slate-800/50`; bars `bg-slate-900`; insets `bg-slate-950` |
| Borders | `border-slate-700` `border-slate-800` |
| Text | primary `text-white` (page default is slate-100 via `styles.css`); body `text-slate-300`; muted `text-slate-400`/`text-slate-500` |
| Status — success/passed | `text-emerald-400` `bg-emerald-500/10` `border-emerald-500/30` |
| Status — warning/validating | `text-amber-400` `bg-amber-500/10` |
| Status — error/failed/expired | `text-red-400` `bg-red-500/10` |
| AI tutor accent | `text-indigo-300` `bg-indigo-500/5` `border-indigo-500/30` |
| Shape | cards `rounded-xl p-6`; controls `rounded-lg`/`rounded-md`; `gap-*`, `space-y-*` |

Difficulty badges: 입문=emerald, 중급=amber, 고급=red.

**The shipped `styles.css` is a compiled Tailwind subset** — only utilities Cledyu's own
code uses are present. Stay within the classes above (and what you see in `styles.css`);
arbitrary Tailwind classes outside that subset will not resolve.

### Where the truth lives
Read `styles.css` (and its `@import`s, incl. `_ds_bundle.css`) for the full compiled class
set and tokens. Each component has a `<Name>.d.ts` (prop contract) and `<Name>.prompt.md`
(usage) — read those before composing.

### Example
```tsx
<Providers>
  <div className="bg-slate-950 min-h-screen">
    <Navbar />
    <main className="max-w-7xl mx-auto px-4 py-8 grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-6">
      {labs.map((lab) => <LabCard key={lab.id} lab={lab} />)}
    </main>
  </div>
</Providers>
```
