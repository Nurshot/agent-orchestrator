import { COMPANY } from "@ao/shared/constants";
import { ArrowRight, ExternalLink, Rss } from "lucide-react";
import type { Metadata } from "next";
import Link from "next/link";
import { FaGithub } from "react-icons/fa";
import { GridCross } from "@/app/blog/components/GridCross";
import { getWeeklyUpdates } from "@/lib/changelog";
import { ChangelogEntry } from "./components/ChangelogEntry";

export const metadata: Metadata = {
  title: "Changelog",
  description:
    "The latest updates, improvements, and new features in Agent Orchestrator.",
  alternates: {
    canonical: "/changelog",
    types: {
      "application/rss+xml": "/changelog.xml",
    },
  },
  openGraph: {
    title: "Changelog | Agent Orchestrator",
    description:
      "The latest updates, improvements, and new features in Agent Orchestrator.",
    url: "/changelog",
    images: ["/og-image.png"],
  },
  twitter: {
    card: "summary_large_image",
    title: "Changelog | Agent Orchestrator",
    description:
      "The latest updates, improvements, and new features in Agent Orchestrator.",
    images: ["/og-image.png"],
  },
};

export default function ChangelogPage() {
  const entries = getWeeklyUpdates();

  return (
    <main className="relative min-h-screen">
      <div
        className="absolute inset-0 pointer-events-none"
        style={{
          backgroundImage: `
            linear-gradient(to right, transparent 0%, transparent calc(50% - 384px), rgba(255,255,255,0.06) calc(50% - 384px), rgba(255,255,255,0.06) calc(50% - 383px), transparent calc(50% - 383px), transparent calc(50% + 383px), rgba(255,255,255,0.06) calc(50% + 383px), rgba(255,255,255,0.06) calc(50% + 384px), transparent calc(50% + 384px))
          `,
        }}
      />

      <header className="relative border-b border-border">
        <div className="max-w-3xl mx-auto px-6 pt-16 pb-12 md:pt-24 md:pb-16 relative">
          <GridCross className="top-0 left-0" />
          <GridCross className="top-0 right-0" />

          <span className="text-sm font-mono text-muted-foreground tracking-[0.5px]">
            Changelog
          </span>
          <h1 className="text-5xl md:text-7xl font-medium tracking-[-0.05em] leading-[0.94] text-foreground mt-5 text-balance">
            What&apos;s new
          </h1>
          <p className="text-base md:text-lg text-muted-foreground mt-5 max-w-2xl leading-relaxed text-pretty">
            New workflows, meaningful improvements, and fixes you will notice.
            Updated weekly. For technical release notes and downloads, use the
            release archive.
          </p>
          <div className="flex flex-wrap items-center gap-x-5 gap-y-3 mt-7">
            <Link
              href="/changelog/releases"
              className="inline-flex items-center gap-1.5 text-sm text-foreground hover:text-orange-400 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-orange-500/70 transition-colors"
            >
              <FaGithub className="size-4" />
              Release archive
              <ArrowRight className="size-3.5" />
            </Link>
            {/* RSS is a document endpoint, so it intentionally uses full navigation. */}
            <a
              href="/changelog.xml"
              className="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-orange-500/70 transition-colors"
            >
              <Rss className="size-3.5" />
              RSS feed
            </a>
            <a
              href={`${COMPANY.GITHUB_URL}/releases`}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-orange-500/70 transition-colors"
            >
              All downloads
              <ExternalLink className="size-3.5" />
            </a>
          </div>

          <GridCross className="bottom-0 left-0" />
          <GridCross className="bottom-0 right-0" />
        </div>
      </header>

      <div className="relative max-w-3xl mx-auto px-6 py-14 md:py-20">
        {entries.length === 0 ? (
          <p className="text-muted-foreground">No updates yet.</p>
        ) : (
          <section aria-label="Weekly product updates" className="space-y-16 md:space-y-24">
            {entries.map((entry) => (
              <ChangelogEntry key={entry.url} entry={entry} />
            ))}
          </section>
        )}
      </div>
    </main>
  );
}
