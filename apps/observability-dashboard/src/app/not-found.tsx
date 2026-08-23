import Link from "next/link";
import { EmptyState } from "@/components/States";

export default function NotFound() {
  return (
    <div className="view">
      <div className="page-head">
        <h1>페이지를 찾을 수 없습니다</h1>
      </div>
      <EmptyState
        title="주소를 확인하세요"
        description="요청한 화면이 없습니다. 라이브 대시보드에서 다시 시작할 수 있습니다."
        action={
          <Link href="/live" className="btn btn-primary">
            라이브 대시보드로
          </Link>
        }
      />
    </div>
  );
}
