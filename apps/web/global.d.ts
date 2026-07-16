declare module '*.css';

// particle-fx: components/ui/ParticleFx.tsx 에서 정의하는 커스텀 엘리먼트 JSX 타입.
declare namespace JSX {
  interface IntrinsicElements {
    'particle-fx': import('react').DetailedHTMLProps<
      import('react').HTMLAttributes<HTMLElement>,
      HTMLElement
    > & {
      class?: string;
      kind?: string;
    };
  }
}
