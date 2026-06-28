import { LabSession } from 'cledyu-web';

// LabSession is the full in-session screen: left = step list + current step
// instructions + 검증 button + AI tutor; right = terminal/IDE workspace. With no
// backend it renders against an empty session, so the placeholder terminal shows
// and the first step is focused. skipBootGrace avoids the boot loading card.
// Needs the QueryClientProvider (cfg.provider = Providers).
const LAB = {
  id: 'k8s-pod-deploy',
  title: 'Kubernetes Pod 배포하기',
  difficulty: 'intermediate' as const,
  environment: 'k3s',
  steps: [
    {
      id: 1,
      title: '클러스터 노드 확인',
      description:
        'kubectl로 현재 클러스터의 노드 상태를 확인합니다. 모든 노드가 Ready 여야 합니다.',
      commands: ['kubectl get nodes'],
    },
    {
      id: 2,
      title: 'Deployment 생성',
      description: 'nginx 이미지를 사용하는 디플로이먼트를 생성하고 레플리카 수를 확인합니다.',
      commands: ['kubectl create deployment web --image=nginx', 'kubectl get deploy'],
    },
    {
      id: 3,
      title: '서비스로 노출',
      description: 'Deployment를 NodePort 서비스로 노출하고 접근 가능한 포트를 확인합니다.',
      commands: ['kubectl expose deployment web --type=NodePort --port=80'],
    },
  ],
};

export const Default = () => (
  <div className="bg-slate-950 p-6">
    <LabSession sessionId="demo-session" lab={LAB} skipBootGrace />
  </div>
);
