"use client";

/**
 * The topbar + sidebar shell, replicated from docs/klaro/prototype.html.
 *
 * The navigation is deliberately NOT the prototype's org/project tree: 05
 * section 3 (2026-08-23 note) puts 상시 관측 at the top level rather than under
 * a project, because the collection unit is the org observability key, not a
 * deployment-fitness project. So the shell is the same and the nav is the one
 * thing that is not.
 */

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useState, type ReactNode } from "react";
import { config } from "@/lib/config";
import { useLive } from "@/hooks/useLive";

const THEME_KEY = "klaro-obs-theme";
/**
 * Dark-first: "dark" is the unconditional default, not a stand-in for "follow
 * the OS". There is no "system" state - globals.css no longer reacts to
 * prefers-color-scheme, so the only way to reach light is this toggle.
 */
type Theme = "light" | "dark";

function BrandMark() {
  return (
    <span className="brand-mark" aria-hidden="true">
      <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="3"
        strokeLinecap="round" strokeLinejoin="round">
        <path d="M3 12h4l3-8 4 16 3-8h4" />
      </svg>
    </span>
  );
}

interface NavItem {
  href: string;
  label: string;
  icon: ReactNode;
}

const EXPLORER_NAV: NavItem[] = [
  {
    href: "/live",
    label: "라이브",
    icon: (
      <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
        strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <path d="M3 12h4l3-8 4 16 3-8h4" />
      </svg>
    ),
  },
  {
    href: "/metrics",
    label: "메트릭",
    icon: (
      <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
        strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <path d="M3 3v18h18" />
        <path d="M7 15l4-5 3 3 4-6" />
      </svg>
    ),
  },
  {
    href: "/traces",
    label: "트레이스",
    icon: (
      <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
        strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <rect x="3" y="4" width="14" height="4" rx="1" />
        <rect x="6" y="10" width="12" height="4" rx="1" />
        <rect x="9" y="16" width="9" height="4" rx="1" />
      </svg>
    ),
  },
  {
    href: "/logs",
    label: "로그",
    icon: (
      <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
        strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <path d="M4 5h16M4 10h16M4 15h10M4 20h7" />
      </svg>
    ),
  },
];

const OPS_NAV: NavItem[] = [
  {
    href: "/alerts",
    label: "알림",
    icon: (
      <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
        strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <path d="M18 8a6 6 0 1 0-12 0c0 7-3 8-3 8h18s-3-1-3-8" />
        <path d="M13.7 21a2 2 0 0 1-3.4 0" />
      </svg>
    ),
  },
  {
    href: "/dashboards",
    label: "대시보드",
    icon: (
      <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
        strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <rect x="3" y="3" width="8" height="9" rx="1" />
        <rect x="13" y="3" width="8" height="5" rx="1" />
        <rect x="13" y="10" width="8" height="11" rx="1" />
        <rect x="3" y="14" width="8" height="7" rx="1" />
      </svg>
    ),
  },
];

function NavList({ items, pathname }: { items: NavItem[]; pathname: string }) {
  return (
    <>
      {items.map((item) => {
        const active = pathname === item.href || pathname.startsWith(item.href + "/");
        return (
          <Link
            key={item.href}
            href={item.href}
            className="nav-btn"
            aria-current={active ? "page" : undefined}
          >
            {item.icon}
            {item.label}
          </Link>
        );
      })}
    </>
  );
}

/**
 * Live chip in the topbar.
 *
 * It subscribes on every page rather than only on /live so the shell can tell
 * the truth about the stream everywhere - the prototype's "1개 실행 중" chip is
 * a global indicator, and one that only worked on one screen would be worse
 * than none.
 */
function LiveChip() {
  const { status, latest } = useLive("metric", { windowSize: 2 });

  const label =
    status === "open"
      ? (latest ? latest.points.length + "개 시계열 · LIVE" : "연결됨 · LIVE")
      : status === "connecting"
        ? "연결 중…"
        : status === "reconnecting"
          ? "재연결 중…"
          : status === "error"
            ? "스트림 오류"
            : "중지됨";

  const cls =
    status === "open" ? "live-chip" : status === "error" || status === "closed" ? "live-chip stopped" : "live-chip connecting";

  return (
    <Link href="/live" className={cls} role="status">
      <span className="live-dot" aria-hidden="true" />
      <span>{label}</span>
    </Link>
  );
}

function ThemeToggle() {
  const [theme, setTheme] = useState<Theme>("dark");

  // Read the stored choice after mount: the server render cannot know it, and
  // reading it during render would produce a hydration mismatch.
  useEffect(() => {
    const stored = window.localStorage.getItem(THEME_KEY);
    if (stored === "light") setTheme("light");
  }, []);

  useEffect(() => {
    const root = document.documentElement;
    if (theme === "dark") {
      // No attribute needed: :root's own defaults are already dark. Clearing
      // it (and the stored choice) is what makes "dark" the true default
      // rather than just another explicit option.
      root.removeAttribute("data-theme");
      window.localStorage.removeItem(THEME_KEY);
    } else {
      root.setAttribute("data-theme", "light");
      window.localStorage.setItem(THEME_KEY, "light");
    }
  }, [theme]);

  const next: Theme = theme === "dark" ? "light" : "dark";

  return (
    <button
      type="button"
      className="theme-btn"
      onClick={() => setTheme(next)}
      aria-label="라이트/다크 테마 전환"
    >
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
        strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <path d="M21 12.8A9 9 0 1 1 11.2 3 7 7 0 0 0 21 12.8z" />
      </svg>
      <span>테마</span>
    </button>
  );
}

export function AppShell({ children }: { children: ReactNode }) {
  const pathname = usePathname() ?? "/";

  return (
    <>
      <header className="topbar">
        <Link href="/live" className="brand">
          <BrandMark />
          klaro
        </Link>
        <span className="brand-scope">상시 관측</span>
        <span className="org-switch">
          <label className="sr-only" htmlFor="orgSelect">
            조직 선택
          </label>
          {/* Org switching is an auth-layer concern the dashboard does not own
              yet; the control shows which org every request is scoped to. */}
          <select id="orgSelect" className="num" defaultValue="current" disabled>
            <option value="current">org {config.orgId.slice(0, 8)}…</option>
          </select>
        </span>
        <LiveChip />
        <span className="topbar-spacer" />
        {config.mock ? <span className="badge badge-warn">MOCK 데이터</span> : null}
        <ThemeToggle />
        <span className="avatar" aria-hidden="true">
          관
        </span>
      </header>

      <div className="app">
        <nav className="sidebar" aria-label="상시 관측 메뉴">
          <p className="microlabel side-group">탐색</p>
          <NavList items={EXPLORER_NAV} pathname={pathname} />
          <p className="microlabel side-group">운영</p>
          <NavList items={OPS_NAV} pathname={pathname} />
        </nav>
        <main>{children}</main>
      </div>
    </>
  );
}
