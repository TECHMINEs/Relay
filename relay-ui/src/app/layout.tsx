import type { Metadata } from "next";
import { Chakra_Petch, IBM_Plex_Sans, JetBrains_Mono } from "next/font/google";
import "./globals.css";
import BrandBar from "@/components/brand/BrandBar";
import BrandFooter from "@/components/brand/BrandFooter";

// Same three typefaces as techmines.github.io, self-hosted by next/font
const fontDisplay = Chakra_Petch({ subsets: ["latin"], weight: ["500", "600", "700"], variable: "--font-chakra", display: "swap" });
const fontBody = IBM_Plex_Sans({ subsets: ["latin"], weight: ["400", "500", "600"], variable: "--font-plex", display: "swap" });
const fontMono = JetBrains_Mono({ subsets: ["latin"], weight: ["400", "500", "700"], variable: "--font-jetbrains", display: "swap" });

export const metadata: Metadata = {
  title: "Relay — LLM Gateway Console",
  description: "Console for Relay, a fault-tolerant OpenAI-compatible LLM gateway by TECHMINES.",
  icons: { icon: { url: "/logo-mark.svg", type: "image/svg+xml" }, apple: "/apple-touch-icon.png" },
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en" className={`dark ${fontDisplay.variable} ${fontBody.variable} ${fontMono.variable}`}>
      {/* Full-viewport app: brand bar, gateway console, footer — no page scroll */}
      <body className="h-dvh flex flex-col overflow-hidden text-white antialiased">
        <BrandBar project="Relay — Fault-Tolerant LLM Gateway" />
        <div className="flex-1 min-h-0">{children}</div>
        <BrandFooter className="shrink-0 py-2" />
      </body>
    </html>
  );
}
