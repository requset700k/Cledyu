import { NextRequest } from 'next/server';

type RouteContext = {
  params: Promise<{ path: string[] }>;
};

const HOP_BY_HOP_HEADERS = new Set([
  'accept-encoding',
  'connection',
  'content-length',
  'host',
  'keep-alive',
  'proxy-authenticate',
  'proxy-authorization',
  'te',
  'trailer',
  'transfer-encoding',
  'upgrade',
  'x-forwarded-for',
]);

function backendURL(request: NextRequest, path: string[]) {
  const base = process.env.CLEDYU_BACKEND_URL;
  if (!base) {
    throw new Error('CLEDYU_BACKEND_URL is not set');
  }

  const url = new URL(`/api/${path.map(encodeURIComponent).join('/')}`, base);
  url.search = request.nextUrl.search;
  return url;
}

function requestHeaders(request: NextRequest) {
  const headers = new Headers(request.headers);
  for (const header of HOP_BY_HOP_HEADERS) {
    headers.delete(header);
  }
  return headers;
}

function responseHeaders(upstream: Response) {
  const headers = new Headers();
  for (const [key, value] of upstream.headers.entries()) {
    if (!HOP_BY_HOP_HEADERS.has(key.toLowerCase())) {
      headers.append(key, value);
    }
  }
  return headers;
}

async function proxy(request: NextRequest, context: RouteContext) {
  const { path } = await context.params;
  let url: URL;
  try {
    url = backendURL(request, path);
  } catch {
    return Response.json({ error: 'backend url is not configured' }, { status: 500 });
  }

  const hasBody = request.method !== 'GET' && request.method !== 'HEAD' && request.body !== null;
  let upstream: Response;
  try {
    upstream = await fetch(url, {
      method: request.method,
      headers: requestHeaders(request),
      body: hasBody ? request.body : undefined,
      duplex: hasBody ? 'half' : undefined,
      redirect: 'manual',
    } as RequestInit & { duplex?: 'half' });
  } catch {
    return Response.json({ error: 'backend request failed' }, { status: 502 });
  }

  return new Response(upstream.body, {
    status: upstream.status,
    statusText: upstream.statusText,
    headers: responseHeaders(upstream),
  });
}

export async function GET(request: NextRequest, context: RouteContext) {
  return proxy(request, context);
}

export async function POST(request: NextRequest, context: RouteContext) {
  return proxy(request, context);
}

export async function PUT(request: NextRequest, context: RouteContext) {
  return proxy(request, context);
}

export async function PATCH(request: NextRequest, context: RouteContext) {
  return proxy(request, context);
}

export async function DELETE(request: NextRequest, context: RouteContext) {
  return proxy(request, context);
}

export async function OPTIONS(request: NextRequest, context: RouteContext) {
  return proxy(request, context);
}
