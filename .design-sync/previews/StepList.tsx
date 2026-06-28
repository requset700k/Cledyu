import { StepList } from 'cledyu-web';

// StepList shows lab steps with a status dot per step (pending/active/validating/
// passed/failed) and strikes through passed steps. The status mix is the axis.
const STEPS = [
  { id: 1, title: 'VM 접속 확인', description: '' },
  { id: 2, title: 'Nginx 설치', description: '' },
  { id: 3, title: '서비스 기동 및 포트 확인', description: '' },
  { id: 4, title: '헬스체크 통과', description: '' },
];

const Wrap = ({ children }: { children: React.ReactNode }) => (
  <div className="bg-slate-950 p-6" style={{ width: 360 }}>
    {children}
  </div>
);

export const InProgress = () => {
  const status: Record<number, 'pending' | 'active' | 'validating' | 'passed' | 'failed'> = {
    1: 'passed',
    2: 'active',
    3: 'pending',
    4: 'pending',
  };
  return (
    <Wrap>
      <StepList steps={STEPS} statusOf={(id) => status[id]} currentId={2} onSelect={() => {}} />
    </Wrap>
  );
};

export const ValidatingAndFailed = () => {
  const status: Record<number, 'pending' | 'active' | 'validating' | 'passed' | 'failed'> = {
    1: 'passed',
    2: 'passed',
    3: 'validating',
    4: 'failed',
  };
  return (
    <Wrap>
      <StepList steps={STEPS} statusOf={(id) => status[id]} currentId={3} onSelect={() => {}} />
    </Wrap>
  );
};
