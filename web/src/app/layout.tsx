import type { Metadata, Viewport } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import "./globals.css";

const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
  maximumScale: 5,
  userScalable: true,
};

export const metadata: Metadata = {
  title: "Boulez - Orchestrate Multiple AI Coding Agents",
  description: "A kernel and daemon that orchestrates multiple AI coding agents (Claude Code, Codex, Pi, Aider, …) in isolated git worktrees, local or over SSH.",
  keywords: ["boulez", "ai", "code assistant", "terminal", "tmux", "orchestration", "claude code", "codex", "aider"],
  authors: [{ name: "yro7" }],
  openGraph: {
    title: "Boulez",
    description: "A kernel and daemon that orchestrates multiple AI coding agents in isolated git worktrees, local or over SSH.",
    url: "https://github.com/yro7/boulez",
    type: "website",
  },
  twitter: {
    card: "summary_large_image",
    title: "Boulez",
    description: "A kernel and daemon that orchestrates multiple AI coding agents in isolated git worktrees, local or over SSH.",
  },
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en">
      <body className={`${geistSans.variable} ${geistMono.variable}`}>
        {children}
      </body>
    </html>
  );
}