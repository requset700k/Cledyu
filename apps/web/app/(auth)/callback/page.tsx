'use client';

import { useEffect } from 'react';
import { useRouter } from 'next/navigation';
import { normalizeReturnPath, POST_LOGIN_RETURN_KEY } from '@/lib/auth-return.mjs';

export default function CallbackPage() {
  const router = useRouter();

  useEffect(() => {
    // 백엔드가 설정한 쿠키가 브라우저에 저장될 시간을 준 뒤 이동.
    const timer = setTimeout(() => {
      let target = '/labs';
      try {
        const stored = window.sessionStorage.getItem(POST_LOGIN_RETURN_KEY);
        if (stored) {
          target = normalizeReturnPath(stored);
          window.sessionStorage.removeItem(POST_LOGIN_RETURN_KEY);
        }
      } catch {
        target = '/labs';
      }
      router.replace(target);
    }, 500);
    return () => clearTimeout(timer);
  }, [router]);

  return (
    <div className="min-h-screen bg-slate-950 flex items-center justify-center">
      <div className="text-center">
        <div className="inline-block w-8 h-8 border-2 border-brand-500 border-t-transparent rounded-full animate-spin mb-4" />
        <p className="text-slate-400 text-sm">로그인 처리 중...</p>
      </div>
    </div>
  );
}
