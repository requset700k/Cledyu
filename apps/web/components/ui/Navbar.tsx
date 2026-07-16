'use client';

import Link from 'next/link';
import { usePathname } from 'next/navigation';
import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { api } from '@/lib/api';
import type { UserRole } from '@/lib/types';

const NAV_LINKS = [
  { href: '/', label: 'Home' },
  { href: '/labs', label: 'Labs' },
  { href: '/billing', label: '요금제' },
];

const DASHBOARD_LINK = { href: '/dashboard', label: '내 학습' };
const INSTRUCTOR_LINK = { href: '/instructor', label: '강사 모드' };

function isActive(pathname: string, href: string) {
  return href === '/' ? pathname === '/' : pathname.startsWith(href);
}

function pillClass(active: boolean) {
  return `whitespace-nowrap rounded-full px-5 py-2 text-sm font-semibold tracking-tight transition-colors ${
    active ? 'bg-white text-black' : 'text-white/65 hover:text-white'
  }`;
}

export function Navbar() {
  const pathname = usePathname();
  // 모바일(xs) 햄버거 메뉴 토글 상태. sm 이상에서는 항상 데스크톱 필 네비를 노출.
  const [mobileOpen, setMobileOpen] = useState(false);
  const { data: me } = useQuery({
    queryKey: ['me'],
    queryFn: () => api.auth.optionalMe(),
    retry: false,
  });

  const links = [
    ...NAV_LINKS,
    ...(me ? [DASHBOARD_LINK] : []),
    ...(canAccessInstructor(me?.role) ? [INSTRUCTOR_LINK] : []),
  ];

  function handleLogout() {
    // 백엔드 GET /auth/logout 으로 전체 페이지 이동 → 쿠키 삭제 + Keycloak SSO 로그아웃.
    api.auth.logout();
  }

  return (
    <>
      <Link
        href="/"
        className="fixed left-6 top-6 z-[70] select-none font-michroma text-[15px] tracking-[0.08em] text-white sm:left-10 sm:top-[34px]"
      >
        CLEDYU
      </Link>

      <nav className="fixed right-10 top-6 z-[70] hidden items-center gap-1 rounded-full border border-white/30 bg-black/70 p-1.5 backdrop-blur-md sm:flex">
        {links.map((link) => (
          <Link
            key={link.href}
            href={link.href}
            className={pillClass(isActive(pathname, link.href))}
          >
            {link.label}
          </Link>
        ))}
        {me ? (
          <button
            onClick={handleLogout}
            className={`${pillClass(false)} ml-1 border-l border-white/20 pl-5`}
          >
            로그아웃
          </button>
        ) : (
          <Link
            href="/login"
            className="ml-1 rounded-full border border-white/40 px-5 py-[7px] text-sm font-semibold text-white transition-colors hover:bg-white hover:text-black"
          >
            로그인
          </Link>
        )}
      </nav>

      {/* 모바일 전용 햄버거 토글 — sm 미만에서만 노출 */}
      <button
        type="button"
        onClick={() => setMobileOpen((open) => !open)}
        className="fixed right-6 top-6 z-[70] rounded-full border border-white/30 bg-black/70 p-2 text-white backdrop-blur-md sm:hidden"
        aria-label="네비게이션 메뉴 토글"
        aria-controls="mobile-nav"
        aria-expanded={mobileOpen}
      >
        <MenuIcon open={mobileOpen} />
      </button>

      {/* 모바일 드롭다운 — 햄버거 클릭 시 nav 링크를 세로로 펼침 */}
      {mobileOpen && (
        <div
          id="mobile-nav"
          className="fixed inset-x-4 top-20 z-[70] space-y-1 rounded-2xl border border-white/20 bg-black/90 p-3 backdrop-blur-md sm:hidden"
        >
          {links.map((link) => (
            <Link
              key={link.href}
              href={link.href}
              onClick={() => setMobileOpen(false)}
              className={`block rounded-xl px-4 py-2.5 text-sm font-semibold transition-colors ${
                isActive(pathname, link.href)
                  ? 'bg-white text-black'
                  : 'text-white/70 hover:text-white'
              }`}
            >
              {link.label}
            </Link>
          ))}
          {me ? (
            <button
              onClick={handleLogout}
              className="block w-full rounded-xl px-4 py-2.5 text-left text-sm font-semibold text-white/70 hover:text-white"
            >
              로그아웃
            </button>
          ) : (
            <Link
              href="/login"
              onClick={() => setMobileOpen(false)}
              className="block rounded-xl px-4 py-2.5 text-sm font-semibold text-white/70 hover:text-white"
            >
              로그인
            </Link>
          )}
        </div>
      )}
    </>
  );
}

function canAccessInstructor(role?: UserRole) {
  return role === 'instructor' || role === 'admin';
}

function MenuIcon({ open }: { open: boolean }) {
  return (
    <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
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
