// design-sync shim for `next/link`.
// The real next/link needs Next's App Router context, which doesn't exist in a
// standalone claude.ai/design bundle, so it throws at render. This shim renders
// a plain anchor with the same surface the Cledyu components use (href + className
// + children), keeping every Link-using component (Navbar, LabCard) visually true.
import * as React from 'react';

type LinkProps = {
  href: string | { pathname?: string };
  children?: React.ReactNode;
  className?: string;
  onClick?: (e: React.MouseEvent<HTMLAnchorElement>) => void;
  [key: string]: unknown;
};

export default function Link({ href, children, ...rest }: LinkProps) {
  const h = typeof href === 'string' ? href : (href?.pathname ?? '#');
  return React.createElement('a', { href: h, ...rest }, children);
}
