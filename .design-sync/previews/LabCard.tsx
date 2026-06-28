import { LabCard } from 'cledyu-web';

// LabCard is the lab-catalog tile. The difficulty badge color is the primary
// variant axis (입문 green / 중급 amber / 고급 red), so each story sweeps it.
const Wrap = ({ children }: { children: React.ReactNode }) => (
  <div className="bg-slate-950 p-6" style={{ width: 360 }}>
    {children}
  </div>
);

export const Beginner = () => (
  <Wrap>
    <LabCard
      lab={{
        id: 'linux-basics',
        title: 'Linux 기초 명령어',
        description: '셸에서 파일 탐색, 권한, 파이프라인을 직접 다루며 리눅스 기본기를 익힙니다.',
        difficulty: 'beginner',
        duration_min: 30,
        tags: ['linux', 'bash', 'cli'],
        step_count: 5,
        vm_type: 'kt-lab-small',
      }}
    />
  </Wrap>
);

export const Intermediate = () => (
  <Wrap>
    <LabCard
      lab={{
        id: 'k8s-pod-deploy',
        title: 'Kubernetes Pod 배포하기',
        description:
          'kubectl로 디플로이먼트를 생성하고 서비스로 노출한 뒤 롤아웃 상태를 확인합니다.',
        difficulty: 'intermediate',
        duration_min: 45,
        tags: ['kubernetes', 'kubectl', 'deployment'],
        step_count: 8,
        vm_type: 'kt-lab-medium',
      }}
    />
  </Wrap>
);

export const Advanced = () => (
  <Wrap>
    <LabCard
      lab={{
        id: 'terraform-vpc',
        title: 'Terraform으로 VPC 구성',
        description:
          'IaC로 VPC·서브넷·라우팅을 선언하고 plan/apply 워크플로로 인프라를 프로비저닝합니다.',
        difficulty: 'advanced',
        duration_min: 60,
        tags: ['terraform', 'aws', 'iac'],
        step_count: 12,
        vm_type: 'kt-lab-medium',
      }}
    />
  </Wrap>
);
