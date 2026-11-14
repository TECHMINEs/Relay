import type { Metadata } from "next";
import localFont from "next/font/local";
import "./globals.css";
import BrandBar from "@/components/brand/BrandBar";
import BrandFooter from "@/components/brand/BrandFooter";

// Same licensed brand fonts as the portfolio, bundled for a reliable static build.
const fontDisplay = localFont({ src: [
  { path: "./fonts/chakra-500.ttf", weight: "500" },
  { path: "./fonts/chakra-600.ttf", weight: "600" },
  { path: "./fonts/chakra-700.ttf", weight: "700" },
], variable: "--font-chakra", display: "swap" });
const fontBody = localFont({ src: [
  { path: "./fonts/plex-400.ttf", weight: "400" },
  { path: "./fonts/plex-500.ttf", weight: "500" },
  { path: "./fonts/plex-600.ttf", weight: "600" },
], variable: "--font-plex", display: "swap" });
const fontMono = localFont({ src: [
  { path: "./fonts/jetbrains-400.ttf", weight: "400" },
  { path: "./fonts/jetbrains-500.ttf", weight: "500" },
  { path: "./fonts/jetbrains-700.ttf", weight: "700" },
], variable: "--font-jetbrains", display: "swap" });

export const metadata: Metadata = {
  title: "Relay — LLM Gateway Console",
  description: "Interactive browser walkthrough of Relay's request routing and provider fallback.",
  icons: { icon: { url: "/Relay/logo-mark.svg", type: "image/svg+xml" }, apple: "/Relay/apple-touch-icon.png" },
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en" className={`dark ${fontDisplay.variable} ${fontBody.variable} ${fontMono.variable}`}>
      <body className="min-h-dvh flex flex-col overflow-x-hidden text-white antialiased">
        <BrandBar project="Relay — Fault-Tolerant LLM Gateway" />
        <div className="flex-1">{children}</div>
        <BrandFooter className="shrink-0 py-2" />
      </body>
    </html>
  );
}
