'use client';

import Link from 'next/link';
import { usePathname } from 'next/navigation';
import { api } from '@/lib/api';

const NAV_LINKS = [
  { href: '/labs', label: 'Labs' },
  { href: '/leaderboard', label: '리더보드' },
  { href: '/instructor', label: '강사 모드' },
];

export function Navbar() {
  const pathname = usePathname();

  async function handleLogout() {
    await api.auth.logout().catch(() => {});
    window.location.href = '/login';
  }

  return (
    <nav className="sticky top-0 z-50 border-b border-slate-800 bg-slate-900/80 backdrop-blur">
      <div className="max-w-7xl mx-auto px-4 sm:px-6 h-14 flex items-center justify-between">
        <div className="flex items-center gap-6">
          <Link href="/labs" className="text-white font-bold text-base tracking-tight">
            Cledyu
          </Link>
          <div className="hidden sm:flex items-center gap-1">
            {NAV_LINKS.map((link) => (
              <Link
                key={link.href}
                href={link.href}
                className={`px-3 py-1.5 rounded-md text-sm transition-colors ${
                  pathname.startsWith(link.href)
                    ? 'text-white bg-slate-800'
                    : 'text-slate-400 hover:text-white hover:bg-slate-800/50'
                }`}
              >
                {link.label}
              </Link>
            ))}
          </div>
        </div>

        <button
          onClick={handleLogout}
          className="text-slate-400 hover:text-white text-sm transition-colors px-3 py-1.5 rounded-md hover:bg-slate-800/50"
        >
          로그아웃
        </button>
      </div>
    </nav>
  );
}
