export const POST_LOGIN_RETURN_KEY = 'cledyu.postLoginReturn';

export function pathWithSearch(locationLike) {
  const pathname = locationLike?.pathname || '/';
  const search = locationLike?.search || '';
  return `${pathname}${search}`;
}

export function normalizeReturnPath(raw, fallback = '/labs') {
  if (typeof raw !== 'string') return fallback;

  const value = raw.trim();
  if (!value || !value.startsWith('/') || value.startsWith('//')) {
    return fallback;
  }

  try {
    const url = new URL(value, 'https://app.cledyu.local');
    return `${url.pathname}${url.search}${url.hash}`;
  } catch {
    return fallback;
  }
}
