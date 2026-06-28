import { Navbar } from 'cledyu-web';

// Navbar is the sticky platform top bar: brand wordmark, primary nav links, and a
// logout button (mobile collapses to a hamburger). Full-width by design.
export const Default = () => (
  <div className="bg-slate-950" style={{ width: 900 }}>
    <Navbar />
    <div className="px-6 py-10 text-slate-500 text-sm">페이지 콘텐츠 영역</div>
  </div>
);
