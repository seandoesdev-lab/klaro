import { redirect } from "next/navigation";

/**
 * The product has no separate home: 상시 관측 is a live view of a running
 * system, so the landing screen is the live dashboard rather than a menu.
 */
export default function Home() {
  redirect("/live");
}
