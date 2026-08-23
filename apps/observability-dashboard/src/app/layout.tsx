import type { Metadata } from "next";
import { AppShell } from "@/components/AppShell";
import "./globals.css";

export const metadata: Metadata = {
  title: "klaro 상시 관측",
  description: "메트릭·트레이스·로그 탐색, 라이브 대시보드, 알림 룰 — klaro observability",
};

/**
 * The theme has to be applied before first paint or the page flashes light on
 * a dark-themed browser. That means an inline script: any React effect runs
 * after hydration, which is several frames too late.
 */
const THEME_BOOTSTRAP = `
try {
  var t = localStorage.getItem("klaro-obs-theme");
  if (t === "light" || t === "dark") document.documentElement.setAttribute("data-theme", t);
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
