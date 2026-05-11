// 루트 경로(/) 접근 시 /login으로 바로 이동. 별도 랜딩 페이지 없음.
import { redirect } from 'next/navigation';

export default function HomePage() {
  redirect('/login');
}
