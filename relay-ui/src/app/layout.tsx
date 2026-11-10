import type { Metadata } from "next";
import "./globals.css";
import BrandBar from "@/components/brand/BrandBar";
import BrandFooter from "@/components/brand/BrandFooter";

export const metadata: Metadata = {
  title: "RELAY // LLM GATEWAY INTERFACE",
  description: "Next-gen sharp-edged plasma dashboard for Relay LLM Gateway",
  icons: { icon: "/techmines-icon.png", apple: "/techmines-icon.png" },
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en" className="dark bg-black">
      {/* Full-viewport app: brand bar, gateway console, footer — no page scroll */}
      <body className="h-dvh flex flex-col overflow-hidden bg-black text-neutral-100 antialiased selection:bg-red-600 selection:text-black">
        <BrandBar project="Relay — Fault-Tolerant LLM Gateway" />
        <div className="flex-1 min-h-0">{children}</div>
        <BrandFooter className="shrink-0 py-2" />
      </body>
    </html>
  );
}
