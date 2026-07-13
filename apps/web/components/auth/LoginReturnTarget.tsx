'use client';

import { useEffect } from 'react';
import { useSearchParams } from 'next/navigation';
import { normalizeReturnPath, POST_LOGIN_RETURN_KEY } from '@/lib/auth-return.mjs';

export function LoginReturnTarget() {
  const searchParams = useSearchParams();
  const from = searchParams.get('from');

  useEffect(() => {
    if (!from) return;
    window.sessionStorage.setItem(POST_LOGIN_RETURN_KEY, normalizeReturnPath(from));
  }, [from]);

  return null;
}
