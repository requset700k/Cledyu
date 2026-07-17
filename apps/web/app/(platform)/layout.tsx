import { Navbar } from '@/components/ui/Navbar';

export const dynamic = 'force-dynamic';

export default function PlatformLayout({ children }: { children: React.ReactNode }) {
  return (
    <div className="min-h-screen bg-black text-[#F2F2F2]">
      <Navbar authEnabled={process.env.AUTH_ENABLED === 'true'} />
      <main className="mx-auto max-w-[1280px] px-6 pb-16 pt-28 sm:px-10">{children}</main>
    </div>
  );
}
