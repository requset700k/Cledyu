'use client';

import { useMutation, useQuery } from '@tanstack/react-query';
import { useSearchParams } from 'next/navigation';
import { Suspense } from 'react';
import { api } from '@/lib/api';
import type { BillingPlan, CheckoutSession, Subscription } from '@/lib/types';

function formatPrice(plan: BillingPlan) {
  if (plan.price_krw === 0) return '무료';
  return `${plan.price_krw.toLocaleString('ko-KR')}원 / 월`;
}

function PlanCard({
  plan,
  currentPlanID,
  currentSubscriptionStatus,
  onCheckout,
  pending,
}: {
  plan: BillingPlan;
  currentPlanID: string;
  currentSubscriptionStatus: Subscription['status'];
  onCheckout: (planID: string) => void;
  pending: boolean;
}) {
  const isCurrent = plan.id === currentPlanID && currentSubscriptionStatus === 'active';
  const isPaid = plan.price_krw > 0;

  return (
    <div
      className={`rounded-lg border bg-slate-900/50 p-5 ${
        plan.recommended ? 'border-brand-500/70' : 'border-slate-800'
      }`}
    >
      <div className="flex items-start justify-between gap-3">
        <div>
          <h2 className="text-white text-lg font-semibold">{plan.name}</h2>
          <p className="mt-1 text-slate-400 text-sm">{formatPrice(plan)}</p>
        </div>
        {plan.recommended && (
          <span className="rounded-md bg-brand-500/15 px-2 py-1 text-brand-300 text-xs">추천</span>
        )}
      </div>

      <ul className="mt-5 space-y-2 text-sm text-slate-300">
        {plan.features.map((feature) => (
          <li key={feature} className="flex gap-2">
            <span className="text-brand-300">✓</span>
            <span>{feature}</span>
          </li>
        ))}
      </ul>

      <button
        type="button"
        disabled={isCurrent || !isPaid || pending}
        onClick={() => onCheckout(plan.id)}
        className="mt-6 w-full rounded-md bg-brand-500 px-3 py-2 text-sm font-medium text-white transition-colors hover:bg-brand-400 disabled:cursor-not-allowed disabled:bg-slate-800 disabled:text-slate-500"
      >
        {isCurrent ? '현재 플랜' : isPaid ? 'Checkout 시작' : '기본 제공'}
      </button>
    </div>
  );
}

export default function BillingPage() {
  return (
    <Suspense fallback={<p className="text-slate-400">불러오는 중...</p>}>
      <BillingPageContent />
    </Suspense>
  );
}

function BillingPageContent() {
  const mockCheckoutEnabled = process.env.NODE_ENV !== 'production';
  const searchParams = useSearchParams();
  const checkoutSessionID = searchParams.get('checkout_session_id');
  const checkoutProvider = searchParams.get('provider');
  const plans = useQuery({
    queryKey: ['billing-plans'],
    queryFn: () => api.billing.plans(),
  });
  const subscription = useQuery({
    queryKey: ['my-subscription'],
    queryFn: () => api.billing.subscription(),
  });
  const checkout = useMutation({
    mutationFn: (planID: string) => api.billing.checkout(planID),
  });
  const completeCheckout = useMutation({
    mutationFn: (checkoutID: string) => api.billing.completeCheckout(checkoutID),
    onSuccess: () => {
      void subscription.refetch();
    },
  });

  if (plans.isLoading || subscription.isLoading) {
    return <p className="text-slate-400">불러오는 중...</p>;
  }
  if (plans.isError || subscription.isError || !plans.data || !subscription.data) {
    return <p className="text-red-400">결제 정보를 불러오지 못했습니다.</p>;
  }

  const created = checkout.data as CheckoutSession | undefined;
  const visibleCheckout = created
    ? { id: created.id, provider: created.provider }
    : checkoutSessionID
      ? { id: checkoutSessionID, provider: checkoutProvider ?? 'mock' }
      : null;

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-bold text-white">요금제</h1>
        <p className="mt-1 text-slate-400 text-sm">
          현재 플랜을 확인하고 checkout 흐름을 시작합니다.
        </p>
      </div>

      <section className="rounded-lg border border-slate-800 bg-slate-900/50 p-5">
        <div className="text-slate-400 text-sm">현재 구독</div>
        <div className="mt-1 text-white text-xl font-bold">{subscription.data.plan_id}</div>
        <p className="mt-1 text-slate-500 text-xs">상태: {subscription.data.status}</p>
      </section>

      {visibleCheckout && (
        <section className="rounded-lg border border-sky-500/40 bg-sky-500/10 p-4">
          <h2 className="text-sky-200 text-sm font-semibold">Mock checkout session 생성됨</h2>
          <p className="mt-1 text-sky-100/80 text-xs">
            실제 PG 승인/웹훅은 후속 PR에서 연결합니다. provider: {visibleCheckout.provider} ·
            session: {visibleCheckout.id}
          </p>
          {mockCheckoutEnabled && (
            <>
              <button
                type="button"
                disabled={completeCheckout.isPending}
                onClick={() => completeCheckout.mutate(visibleCheckout.id)}
                className="mt-3 rounded-md bg-sky-500 px-3 py-2 text-sm font-medium text-white transition-colors hover:bg-sky-400 disabled:cursor-not-allowed disabled:bg-slate-700 disabled:text-slate-400"
              >
                {completeCheckout.isPending ? '승인 처리 중...' : 'Mock 승인 완료'}
              </button>
              {completeCheckout.isSuccess && (
                <p className="mt-2 text-emerald-300 text-xs">
                  구독 상태를 active 로 반영했습니다.
                </p>
              )}
              {completeCheckout.isError && (
                <p className="mt-2 text-red-300 text-xs">Mock checkout 완료 처리에 실패했습니다.</p>
              )}
            </>
          )}
        </section>
      )}
      {checkout.isError && (
        <section className="rounded-lg border border-red-500/40 bg-red-500/10 p-4 text-red-200 text-sm">
          Checkout 세션을 만들지 못했습니다.
        </section>
      )}

      <section className="grid grid-cols-1 gap-4 md:grid-cols-3">
        {plans.data.items.map((plan) => (
          <PlanCard
            key={plan.id}
            plan={plan}
            currentPlanID={subscription.data.plan_id}
            currentSubscriptionStatus={subscription.data.status}
            pending={checkout.isPending}
            onCheckout={(planID) => checkout.mutate(planID)}
          />
        ))}
      </section>
    </div>
  );
}
