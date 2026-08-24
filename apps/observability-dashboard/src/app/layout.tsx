import type { Metadata } from "next";
import { AppShell } from "@/components/AppShell";
import "./globals.css";

export const metadata: Metadata = {
  title: "klaro 상시 관측",
  description: "메트릭·트레이스·로그 탐색, 라이브 대시보드, 알림 룰 — klaro observability",
};

/**
 * Dark is the CSS default now, so the only flash to prevent is dark-then-light
 * for a visitor who previously chose light. That means an inline script: any
 * React effect runs after hydration, which is several frames too late.
 */
const THEME_BOOTSTRAP = `
try {
  if (localStorage.getItem("klaro-obs-theme") === "light") {
    document.documentElement.setAttribute("data-theme", "light");
  }
} catch (e) {}
`;

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="ko">
      <body>
        <script dangerouslySetInnerHTML={{ __html: THEME_BOOTSTRAP }} />
        <AppShell>{children}</AppShell>
      </body>
    </html>
  );
}
