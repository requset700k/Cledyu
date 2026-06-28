// design-sync shim for `next/navigation`.
// Standalone previews have no Next router context. Cledyu only reads usePathname()
// (Navbar highlights the active link). The shim returns a stable path and no-op
// router/search stubs so the components render their default (non-active) state.
export function usePathname(): string {
  return '/';
}

export function useRouter() {
  const noop = () => {};
  return { push: noop, replace: noop, back: noop, forward: noop, refresh: noop, prefetch: noop };
}

export function useSearchParams(): URLSearchParams {
  return new URLSearchParams();
}

export function useParams<
  T extends Record<string, string | string[]> = Record<string, string | string[]>,
>(): T {
  return {} as T;
}

export function redirect(_url: string): never {
  throw new Error('redirect() is a no-op in design-sync previews');
}

export function notFound(): never {
  throw new Error('notFound() is a no-op in design-sync previews');
}
