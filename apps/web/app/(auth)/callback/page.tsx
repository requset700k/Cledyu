// Keycloak OIDC 콜백 페이지.
// 인증 완료 후 백엔드가 access_token 쿠키를 설정하고 이 페이지로 리다이렉트함.
// 쿠키가 브라우저에 저장될 시간(500ms)을 준 뒤 /labs로 이동.

'use client';

import { useEffect } from 'react';
import { useRouter } from 'next/navigation';

export default function CallbackPage() {
  const router = useRouter();

  useEffect(() => {
    // 백엔드가 설정한 쿠키가 브라우저에 저장될 시간을 준 뒤 이동.
    const timer = setTimeout(() => router.replace('/labs'), 500);
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
