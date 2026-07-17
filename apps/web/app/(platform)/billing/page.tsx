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
      return '팀 단위 학습 현황과 운영 리포트를 제공하는 조직용 플랜입니다.';
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
  // 추천 플랜은 흰 배경 반전 카드로 강조 (v2 디자인의 PRO 카드)
  const inverted = plan.recommended;

  return (
    <div
      className={`flex flex-col p-9 ${
        inverted
          ? 'border border-white bg-white text-black'
          : `border bg-white/[0.02] text-[#F2F2F2] ${
              selected ? 'border-white/70' : 'border-white/20'
            }`
      }`}
    >
      <div className="flex items-center justify-between gap-3">
        <div className="font-michroma text-lg tracking-[0.08em]">{plan.name}</div>
        {plan.recommended && (
          <span className="whitespace-nowrap rounded-full border border-current px-3 py-1 font-jbmono text-[11px] tracking-[0.12em]">
            RECOMMENDED
          </span>
        )}
      </div>

      <div className="mt-7 font-chakra text-4xl font-bold leading-none">{formatPrice(plan)}</div>

      <ul
        className={`mt-9 flex-1 space-y-3 text-sm leading-[1.5] ${inverted ? '' : 'text-white/80'}`}
      >
        {plan.features.map((feature) => (
          <li key={feature} className="flex gap-3">
            <span className="opacity-50">—</span>
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
        className={`mt-9 rounded-full py-3.5 text-sm font-bold tracking-[-0.01em] transition-colors disabled:cursor-not-allowed ${
          inverted
            ? 'bg-black text-white hover:bg-black/80 disabled:bg-black/30 disabled:text-white/70'
            : 'border border-white/50 text-white hover:bg-white hover:text-black disabled:border-white/20 disabled:text-white/35 disabled:hover:bg-transparent'
        }`}
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
    <div className="fixed inset-0 z-[80] flex items-center justify-center bg-black/80 p-4 backdrop-blur-sm">
      <div className="w-full max-w-lg border border-white/25 bg-black shadow-2xl">
        <div className="border-b border-white/15 p-6">
          <div className="flex items-start justify-between gap-4">
            <div>
              <p className="font-jbmono text-xs tracking-[0.12em] text-white/50">CLEDYU CHECKOUT</p>
              <h2 className="mt-2 font-chakra text-xl font-bold text-white">
                {plan.name} 플랜 결제
              </h2>
            </div>
            <button
              type="button"
              onClick={onClose}
              className="rounded-full px-3 py-1 text-sm text-white/55 transition-colors hover:text-white"
            >
              닫기
            </button>
          </div>
        </div>

        <div className="space-y-6 p-6">
          <section className="border border-white/15 bg-white/[0.03] p-5">
            <div className="flex items-center justify-between text-sm">
              <span className="text-white/50">상품</span>
              <span className="font-medium text-white">{plan.name} 월간 구독</span>
            </div>
            <div className="mt-3 flex items-center justify-between text-sm">
              <span className="text-white/50">결제 금액</span>
              <span className="font-chakra text-lg font-bold text-white">
                {formatAmount(plan.price_krw)}
              </span>
            </div>
          </section>

          <section>
            <p className="text-sm font-medium text-white/80">결제수단</p>
            <div className="mt-3 grid grid-cols-3 gap-2">
              {methodLabels.map((item) => (
                <button
                  key={item.id}
                  type="button"
                  onClick={() => setMethod(item.id)}
                  className={`border px-3 py-2.5 text-sm transition-colors ${
                    method === item.id
                      ? 'border-white bg-white text-black'
                      : 'border-white/25 text-white/55 hover:border-white/60 hover:text-white'
                  }`}
                >
                  {item.label}
                </button>
              ))}
            </div>
          </section>

          <section className="border border-white/15 bg-white/[0.03] p-5">
            {method === 'card' && (
              <div className="space-y-3 text-sm">
                <div className="border border-white/20 bg-black px-3 py-2.5 font-jbmono text-white/80">
                  1234 · 5678 · **** · ****
                </div>
                <div className="grid grid-cols-2 gap-2">
                  <div className="border border-white/20 bg-black px-3 py-2.5 font-jbmono text-white/40">
                    MM / YY
                  </div>
                  <div className="border border-white/20 bg-black px-3 py-2.5 font-jbmono text-white/40">
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
                    className="border border-white/20 bg-black px-3 py-3 text-center text-white/80"
                  >
                    {label}
                  </div>
                ))}
              </div>
            )}
            {method === 'transfer' && (
              <div className="border border-white/20 bg-black px-3 py-3 text-sm text-white/80">
                은행 선택 후 계좌 인증을 진행합니다.
              </div>
            )}
          </section>

          <button
            type="button"
            disabled={pending}
            onClick={onConfirm}
            className="w-full rounded-full bg-white px-4 py-3.5 text-sm font-bold text-black transition-colors hover:bg-white/85 disabled:cursor-not-allowed disabled:bg-white/30 disabled:text-black/50"
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
    <Suspense fallback={<p className="text-white/50">불러오는 중...</p>}>
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
    return <p className="text-white/50">불러오는 중...</p>;
  }
  if (plans.isError || subscription.isError || !plans.data || !subscription.data) {
    return <p className="text-red-400">결제 정보를 불러오지 못했습니다.</p>;
  }

  const selectedPlan =
    plans.data.items.find((plan) => plan.id === selectedPlanID) ??
    plans.data.items.find((plan) => plan.id === subscription.data.plan_id) ??
    plans.data.items[0];
  const isSelectedPlanCurrent =
    selectedPlan.id === subscription.data.plan_id && subscription.data.status === 'active';
  const paymentPlan = paymentPlanID
    ? plans.data.items.find((plan) => plan.id === paymentPlanID)
    : null;
  const isPaymentPlanCurrent =
    paymentPlan?.id === subscription.data.plan_id && subscription.data.status === 'active';

  return (
    <div>
      <h1 className="font-michroma text-[clamp(34px,4.2vw,60px)] leading-none tracking-[0.05em] text-white">
        PLANS
      </h1>
      <p className="mt-5 text-base tracking-[-0.02em] text-white/55">
        플랜을 선택하고 결제하면 해당 기능 권한이 바로 활성화됩니다
      </p>

      {/* 현재 구독 밴드 */}
      <div className="mt-12 flex flex-wrap items-baseline gap-x-6 gap-y-2 border-y border-white/25 px-3 py-7">
        <span className="font-jbmono text-xs tracking-[0.12em] text-white/45">
          CURRENT SUBSCRIPTION
        </span>
        <span className="font-chakra text-2xl font-bold uppercase text-white">
          {subscription.data.plan_id}
        </span>
        <span className="font-jbmono text-xs uppercase tracking-[0.1em] text-white/45">
          [ {subscription.data.status} ]
        </span>
      </div>

      {/* 선택한 요금제 상세 */}
      <section className="mt-10 border border-white/20 bg-white/[0.02] p-8">
        <div className="flex flex-col gap-4 md:flex-row md:items-start md:justify-between">
          <div>
            <p className="font-jbmono text-xs tracking-[0.12em] text-white/50">SELECTED PLAN</p>
            <h2 className="mt-2 font-chakra text-2xl font-bold text-white">{selectedPlan.name}</h2>
            <p className="mt-3 max-w-2xl break-keep text-sm leading-[1.7] text-white/60">
              {planAccessDescription(selectedPlan.id)}
            </p>
          </div>
          <span className="whitespace-nowrap rounded-full border border-white/25 px-4 py-2 text-sm text-white/80">
            {formatPrice(selectedPlan)}
          </span>
        </div>

        <div className="mt-6 grid gap-2.5 md:grid-cols-2">
          {selectedPlan.features.map((feature) => (
            <div key={feature} className="border border-white/15 bg-black/40 p-3.5">
              <p className="text-sm font-medium text-white/90">{feature}</p>
            </div>
          ))}
        </div>

        <p className="mt-6 text-xs leading-relaxed text-white/35">
          운영 결제와 정산은 PG 상점 계약, 사업자/정산 계좌 등록, 라이브 키 발급 이후 실제 승인
          흐름으로 전환합니다.
        </p>

        {paymentSimulationEnabled && selectedPlan.price_krw > 0 && !isSelectedPlanCurrent && (
          <div className="mt-6">
            <button
              type="button"
              disabled={activatePlan.isPending}
              onClick={() => setPaymentPlanID(selectedPlan.id)}
              className="rounded-full bg-white px-7 py-3 text-sm font-bold text-black transition-colors hover:bg-white/85 disabled:cursor-not-allowed disabled:bg-white/30"
            >
              선택한 요금제 결제하기
            </button>
            <p className="mt-3 text-xs text-white/40">
              결제가 완료되면 선택한 요금제 권한이 계정에 반영됩니다.
            </p>
          </div>
        )}

        {isSelectedPlanCurrent && (
          <p className="mt-6 text-sm text-white/80">✔ 현재 이용 중인 요금제입니다.</p>
        )}

        {activationMessage && <p className="mt-4 text-sm text-white/80">✔ {activationMessage}</p>}
        {activationError && <p className="mt-4 text-sm text-red-400">{activationError}</p>}
        {completeReturnedCheckout.isPending && (
          <p className="mt-4 text-sm text-white/60">결제 결과를 반영하는 중입니다...</p>
        )}
      </section>

      <section className="mt-10 grid grid-cols-1 gap-3.5 md:grid-cols-3">
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

      {paymentPlan && !isPaymentPlanCurrent && (
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
