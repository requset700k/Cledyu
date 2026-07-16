import type { DetailedHTMLProps, HTMLAttributes } from 'react';

declare module '*.css';

// particle-fx: components/ui/ParticleFx.tsx 에서 정의하는 커스텀 엘리먼트 JSX 타입.
declare global {
  namespace JSX {
    interface IntrinsicElements {
      'particle-fx': DetailedHTMLProps<HTMLAttributes<HTMLElement>, HTMLElement> & { kind?: string };
    }
  }
}
