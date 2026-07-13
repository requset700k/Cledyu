'use client';

import Link from 'next/link';
import { usePathname } from 'next/navigation';
import { useState } from 'react';
import { api } from '@/lib/api';

const DASHBOARD_LINK = { href: '/dashboard', label: '내 학습' };

const NAV_LINKS = [
  { href: '/labs', label: 'Labs' },
  { href: '/leaderboard', label: '리더보드' },
  { href: '/instructor', label: '강사 모드' },
];

const MOBILE_NAV_LINKS = [DASHBOARD_LINK, ...NAV_LINKS];

function navLinkClass(pathname: string, href: string) {
  return `px-3 py-1.5 rounded-md text-sm transition-colors ${
    pathname.startsWith(href)
      ? 'text-white bg-slate-800'
      : 'text-slate-400 hover:text-white hover:bg-slate-800/50'
  }`;
}

export function Navbar() {
  const pathname = usePathname();
  // 모바일(xs) 햄버거 메뉴 토글 상태. sm 이상에서는 항상 데스크톱 네비를 노출.
  const [mobileOpen, setMobileOpen] = useState(false);

  function handleLogout() {
    // 백엔드 GET /auth/logout 으로 전체 페이지 이동 → 쿠키 삭제 + Keycloak SSO 로그아웃.
    api.auth.logout();
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
                className={navLinkClass(pathname, link.href)}
              >
                {link.label}
              </Link>
            ))}
          </div>
        </div>

        <div className="flex items-center gap-2">
          <Link
            href={DASHBOARD_LINK.href}
            className={`hidden sm:inline-flex ${navLinkClass(pathname, DASHBOARD_LINK.href)}`}
          >
            {DASHBOARD_LINK.label}
          </Link>
          <button
            onClick={handleLogout}
            className="text-slate-400 hover:text-white text-sm transition-colors px-3 py-1.5 rounded-md hover:bg-slate-800/50"
          >
            로그아웃
          </button>
          {/* 모바일 전용 햄버거 토글 — sm 미만에서만 노출 */}
          <button
            type="button"
            onClick={() => setMobileOpen((open) => !open)}
            className="sm:hidden text-slate-400 hover:text-white p-1.5 rounded-md hover:bg-slate-800/50 transition-colors"
            aria-label="네비게이션 메뉴 토글"
            aria-controls="mobile-nav"
            aria-expanded={mobileOpen}
          >
            <MenuIcon open={mobileOpen} />
          </button>
        </div>
      </div>

      {/* 모바일 드롭다운 — 햄버거 클릭 시 nav 링크를 세로로 펼침 */}
      {mobileOpen && (
        <div id="mobile-nav" className="sm:hidden border-t border-slate-800 px-4 py-2 space-y-1">
          {MOBILE_NAV_LINKS.map((link) => (
            <Link
              key={link.href}
              href={link.href}
              onClick={() => setMobileOpen(false)}
              className={`block px-3 py-2 rounded-md text-sm transition-colors ${
                pathname.startsWith(link.href)
                  ? 'text-white bg-slate-800'
                  : 'text-slate-400 hover:text-white hover:bg-slate-800/50'
              }`}
            >
              {link.label}
            </Link>
          ))}
        </div>
      )}
    </nav>
  );
}

function MenuIcon({ open }: { open: boolean }) {
  return (
    <svg className="w-5 h-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
      {open ? (
        <path
          strokeLinecap="round"
          strokeLinejoin="round"
          strokeWidth={2}
          d="M6 18L18 6M6 6l12 12"
        />
      ) : (
        <path
          strokeLinecap="round"
          strokeLinejoin="round"
          strokeWidth={2}
          d="M4 6h16M4 12h16M4 18h16"
        />
      )}
    </svg>
  );
}
