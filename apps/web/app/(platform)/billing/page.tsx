'use client';

import { useMutation, useQuery } from '@tanstack/react-query';
import { useRouter, useSearchParams } from 'next/navigation';
import { Suspense, useEffect, useRef, useState } from 'react';
import { api } from '@/lib/api';
import type { BillingPlan, Subscription } from '@/lib/types';

function formatPrice(plan: BillingPlan) {
  if (plan.price_krw === 0) return '무료';
  return `${plan.price_krw.toLocaleString('ko-KR')}원 / 월`;
}

function formatAmount(amount: number) {
  return `${amount.toLocaleString('ko-KR')}원`;
}

function planAccessDescription(planID: string) {
  switch (planID) {
    case 'pro-monthly':
      return '중급/고급 Lab, AI 힌트 확장, 수료 이력까지 개인 학습자에게 열리는 플랜입니다.';
    case 'team-monthly':
      return '팀 단위 학습 현황과 강사 모드까지 포함하는 조직/교육 운영자용 플랜입니다.';
    default:
      return '무료 공개 Lab과 기본 학습 현황을 확인할 수 있는 기본 플랜입니다.';
  }
}

function PlanCard({
  plan,
  currentPlanID,
  currentSubscriptionStatus,
  selected,
  paymentEnabled,
  onSelect,
  onPay,
}: {
  plan: BillingPlan;
  currentPlanID: string;
  currentSubscriptionStatus: Subscription['status'];
  selected: boolean;
  paymentEnabled: boolean;
  onSelect: (planID: string) => void;
  onPay: (planID: string) => void;
}) {
  const isCurrent = plan.id === currentPlanID && currentSubscriptionStatus === 'active';
  const isPaid = plan.price_krw > 0;
  const canPay = paymentEnabled && isPaid && !isCurrent;
  const buttonLabel = isCurrent ? '현재 이용 중' : isPaid ? '결제하기' : '기능 보기';

  return (
    <div
      className={`rounded-lg border bg-slate-900/50 p-5 ${
        selected
          ? 'border-brand-400 ring-1 ring-brand-500/40'
          : plan.recommended
            ? 'border-brand-500/70'
            : 'border-slate-800'
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
        onClick={() => {
          onSelect(plan.id);
          if (canPay) {
            onPay(plan.id);
          }
        }}
        disabled={isCurrent || (isPaid && !paymentEnabled)}
        className="mt-6 w-full rounded-md bg-brand-500 px-3 py-2 text-sm font-medium text-white transition-colors hover:bg-brand-400 disabled:cursor-not-allowed disabled:bg-slate-800 disabled:text-slate-500"
      >
        {selected && !isPaid ? '선택됨' : buttonLabel}
      </button>
    </div>
  );
}

function PaymentModal({
  plan,
  pending,
  onClose,
  onConfirm,
}: {
  plan: BillingPlan;
  pending: boolean;
  onClose: () => void;
  onConfirm: () => void;
}) {
  const [method, setMethod] = useState<'card' | 'easy' | 'transfer'>('card');
  const methodLabels = [
    { id: 'card', label: '카드' },
    { id: 'easy', label: '간편결제' },
    { id: 'transfer', label: '계좌이체' },
  ] as const;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-slate-950/75 p-4">
      <div className="w-full max-w-lg rounded-xl border border-slate-700 bg-slate-950 shadow-2xl">
        <div className="border-slate-800 border-b p-5">
          <div className="flex items-start justify-between gap-4">
            <div>
              <p className="text-brand-300 text-sm font-semibold">Cledyu 결제</p>
              <h2 className="mt-1 text-xl font-bold text-white">{plan.name} 플랜 결제</h2>
            </div>
            <button
              type="button"
              onClick={onClose}
              className="rounded-md px-2 py-1 text-slate-400 text-sm hover:bg-slate-800 hover:text-white"
            >
              닫기
            </button>
          </div>
        </div>

        <div className="space-y-5 p-5">
          <section className="rounded-lg border border-slate-800 bg-slate-900/70 p-4">
            <div className="flex items-center justify-between text-sm">
              <span className="text-slate-400">상품</span>
              <span className="font-medium text-white">{plan.name} 월간 구독</span>
            </div>
            <div className="mt-3 flex items-center justify-between text-sm">
              <span className="text-slate-400">결제 금액</span>
              <span className="font-bold text-brand-300 text-lg">
                {formatAmount(plan.price_krw)}
              </span>
            </div>
          </section>

          <section>
            <p className="text-slate-300 text-sm font-medium">결제수단</p>
            <div className="mt-3 grid grid-cols-3 gap-2">
              {methodLabels.map((item) => (
                <button
                  key={item.id}
                  type="button"
                  onClick={() => setMethod(item.id)}
                  className={`rounded-md border px-3 py-2 text-sm ${
                    method === item.id
                      ? 'border-brand-400 bg-brand-500/20 text-brand-200'
                      : 'border-slate-700 bg-slate-900 text-slate-400 hover:border-slate-500'
                  }`}
                >
                  {item.label}
                </button>
              ))}
            </div>
          </section>

          <section className="rounded-lg border border-slate-800 bg-slate-900/70 p-4">
            {method === 'card' && (
              <div className="space-y-3 text-sm">
                <div className="rounded-md border border-slate-700 bg-slate-950 px-3 py-2 text-slate-300">
                  1234 · 5678 · **** · ****
                </div>
                <div className="grid grid-cols-2 gap-2">
                  <div className="rounded-md border border-slate-700 bg-slate-950 px-3 py-2 text-slate-500">
                    MM / YY
                  </div>
                  <div className="rounded-md border border-slate-700 bg-slate-950 px-3 py-2 text-slate-500">
                    CVC
                  </div>
                </div>
              </div>
            )}
            {method === 'easy' && (
              <div className="grid grid-cols-3 gap-2 text-sm">
                {['Toss Pay', 'Kakao Pay', 'Naver Pay'].map((label) => (
                  <div
                    key={label}
                    className="rounded-md border border-slate-700 bg-slate-950 px-3 py-3 text-center text-slate-300"
                  >
                    {label}
                  </div>
                ))}
              </div>
            )}
            {method === 'transfer' && (
              <div className="rounded-md border border-slate-700 bg-slate-950 px-3 py-3 text-slate-300 text-sm">
                은행 선택 후 계좌 인증을 진행합니다.
              </div>
            )}
          </section>

          <button
            type="button"
            disabled={pending}
            onClick={onConfirm}
            className="w-full rounded-md bg-brand-500 px-4 py-3 text-sm font-semibold text-white transition-colors hover:bg-brand-400 disabled:cursor-not-allowed disabled:bg-slate-700 disabled:text-slate-400"
          >
            {pending ? '결제 처리 중...' : `${formatAmount(plan.price_krw)} 결제하기`}
          </button>
        </div>
      </div>
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
  const router = useRouter();
  const searchParams = useSearchParams();
  const paymentSimulationEnabled = process.env.NODE_ENV !== 'production';
  const [activationMessage, setActivationMessage] = useState<string | null>(null);
  const [activationError, setActivationError] = useState<string | null>(null);
  const [selectedPlanID, setSelectedPlanID] = useState<string | null>(null);
  const [paymentPlanID, setPaymentPlanID] = useState<string | null>(null);
  const consumedCheckoutID = useRef<string | null>(null);
  const plans = useQuery({
    queryKey: ['billing-plans'],
    queryFn: () => api.billing.plans(),
  });
  const subscription = useQuery({
    queryKey: ['my-subscription'],
    queryFn: () => api.billing.subscription(),
  });
  const completeReturnedCheckout = useMutation({
    mutationFn: (checkoutID: string) => api.billing.completeCheckout(checkoutID),
    onMutate: () => {
      setActivationMessage(null);
      setActivationError(null);
    },
    onSuccess: async () => {
      await subscription.refetch();
      setPaymentPlanID(null);
      setActivationMessage('결제가 완료되었습니다. 선택한 요금제 권한이 활성화되었습니다.');
      router.replace('/billing', { scroll: false });
    },
    onError: () => {
      setActivationError('결제 처리에 실패했습니다. 잠시 후 다시 시도하세요.');
      router.replace('/billing', { scroll: false });
    },
  });
  const activatePlan = useMutation({
    mutationFn: async (planID: string) => {
      const checkout = await api.billing.checkout(planID);
      if (checkout.provider !== 'mock') {
        throw new Error('payment activation requires mock provider');
      }
      return api.billing.completeCheckout(checkout.id);
    },
    onMutate: () => {
      setActivationMessage(null);
      setActivationError(null);
    },
    onSuccess: async () => {
      await subscription.refetch();
      setPaymentPlanID(null);
      setActivationMessage('결제가 완료되었습니다. 선택한 요금제 권한이 활성화되었습니다.');
    },
    onError: () => {
      setActivationError('결제 처리에 실패했습니다. 잠시 후 다시 시도하세요.');
    },
  });
  const returnedCheckoutID = searchParams.get('checkout_session_id');
  const returnedProvider = searchParams.get('provider');

  useEffect(() => {
    if (!paymentSimulationEnabled || !returnedCheckoutID) return;
    if (returnedProvider && returnedProvider !== 'mock') return;
    if (consumedCheckoutID.current === returnedCheckoutID) return;

    consumedCheckoutID.current = returnedCheckoutID;
    completeReturnedCheckout.mutate(returnedCheckoutID);
  }, [completeReturnedCheckout, paymentSimulationEnabled, returnedCheckoutID, returnedProvider]);

  if (plans.isLoading || subscription.isLoading) {
    return <p className="text-slate-400">불러오는 중...</p>;
  }
  if (plans.isError || subscription.isError || !plans.data || !subscription.data) {
    return <p className="text-red-400">결제 정보를 불러오지 못했습니다.</p>;
  }

  const selectedPlan =
    plans.data.items.find((plan) => plan.id === selectedPlanID) ??
    plans.data.items.find((plan) => plan.id === subscription.data.plan_id) ??
    plans.data.items[0];
  const paymentPlan = paymentPlanID
    ? plans.data.items.find((plan) => plan.id === paymentPlanID)
    : null;

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-bold text-white">요금제</h1>
        <p className="mt-1 text-slate-400 text-sm">
          플랜을 선택하고 결제하면 해당 기능 권한이 바로 활성화됩니다.
        </p>
      </div>

      <section className="rounded-lg border border-slate-800 bg-slate-900/50 p-5">
        <div className="text-slate-400 text-sm">현재 구독</div>
        <div className="mt-1 text-white text-xl font-bold">{subscription.data.plan_id}</div>
        <p className="mt-1 text-slate-500 text-xs">상태: {subscription.data.status}</p>
      </section>

      <section className="rounded-lg border border-sky-500/30 bg-sky-500/10 p-5">
        <div className="flex flex-col gap-4 md:flex-row md:items-start md:justify-between">
          <div>
            <p className="text-sky-200 text-sm font-semibold">선택한 요금제</p>
            <h2 className="mt-1 text-xl font-bold text-white">{selectedPlan.name}</h2>
            <p className="mt-2 max-w-2xl text-slate-300 text-sm">
              {planAccessDescription(selectedPlan.id)}
            </p>
          </div>
          <span className="rounded-md bg-slate-950/60 px-3 py-2 text-slate-300 text-sm">
            {formatPrice(selectedPlan)}
          </span>
        </div>

        <div className="mt-5 grid gap-3 md:grid-cols-2">
          {selectedPlan.features.map((feature) => (
            <div key={feature} className="rounded-md border border-slate-700 bg-slate-950/40 p-3">
              <p className="text-white text-sm font-medium">{feature}</p>
            </div>
          ))}
        </div>

        <p className="mt-5 text-slate-500 text-xs">
          운영 결제와 정산은 PG 상점 계약, 사업자/정산 계좌 등록, 라이브 키 발급 이후 실제 승인
          흐름으로 전환합니다.
        </p>

        {paymentSimulationEnabled && selectedPlan.price_krw > 0 && (
          <div className="mt-5">
            <button
              type="button"
              disabled={activatePlan.isPending}
              onClick={() => setPaymentPlanID(selectedPlan.id)}
              className="rounded-md bg-brand-500 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-brand-400 disabled:cursor-not-allowed disabled:bg-slate-700 disabled:text-slate-400"
            >
              선택한 요금제 결제하기
            </button>
            <p className="mt-2 text-slate-500 text-xs">
              결제가 완료되면 선택한 요금제 권한이 계정에 반영됩니다.
            </p>
          </div>
        )}

        {activationMessage && <p className="mt-3 text-emerald-300 text-sm">{activationMessage}</p>}
        {activationError && <p className="mt-3 text-red-300 text-sm">{activationError}</p>}
        {completeReturnedCheckout.isPending && (
          <p className="mt-3 text-brand-300 text-sm">결제 결과를 반영하는 중입니다...</p>
        )}
      </section>

      <section className="grid grid-cols-1 gap-4 md:grid-cols-3">
        {plans.data.items.map((plan) => (
          <PlanCard
            key={plan.id}
            plan={plan}
            currentPlanID={subscription.data.plan_id}
            currentSubscriptionStatus={subscription.data.status}
            selected={plan.id === selectedPlan.id}
            paymentEnabled={paymentSimulationEnabled}
            onSelect={setSelectedPlanID}
            onPay={setPaymentPlanID}
          />
        ))}
      </section>

      {paymentPlan && (
        <PaymentModal
          plan={paymentPlan}
          pending={activatePlan.isPending}
          onClose={() => setPaymentPlanID(null)}
          onConfirm={() => activatePlan.mutate(paymentPlan.id)}
        />
      )}
    </div>
  );
}
