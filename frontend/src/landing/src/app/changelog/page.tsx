import { COMPANY } from "@ao/shared/constants";
import { ArrowRight, ExternalLink, Rss } from "lucide-react";
import type { Metadata } from "next";
import Link from "next/link";
import { FaGithub } from "react-icons/fa";
import { GridCross } from "@/app/blog/components/GridCross";
import { formatChangelogDate, getWeeklyUpdates } from "@/lib/changelog";
import { ChangelogCard } from "./components/ChangelogCard";

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
  const [latestEntry, ...previousEntries] = entries;

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
        <div className="max-w-5xl mx-auto px-6 pt-16 pb-12 md:pt-24 md:pb-16 relative">
          <GridCross className="top-0 left-0" />
          <GridCross className="top-0 right-0" />

          <span className="text-sm font-mono text-muted-foreground tracking-[0.5px]">
            Changelog
          </span>
          <h1 className="text-4xl md:text-6xl font-medium tracking-[-0.04em] leading-[0.98] text-foreground mt-5 text-balance">
            What shipped this week
          </h1>
          <p className="text-base md:text-lg text-muted-foreground mt-5 max-w-2xl leading-relaxed text-pretty">
            A weekly record of the product changes that matter: new workflows,
            meaningful improvements, and fixes you will notice.
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

      <div className="relative max-w-5xl mx-auto px-6 py-14 md:py-20">
        {!latestEntry ? (
          <p className="text-muted-foreground">No updates yet.</p>
        ) : (
          <>
            <section aria-labelledby="latest-update-heading">
              <div className="flex items-center gap-3 mb-6">
                <span className="h-px flex-1 bg-border" />
                <span className="text-xs font-mono uppercase tracking-[0.16em] text-muted-foreground">
                  Latest update
                </span>
              </div>
              <Link
                href={latestEntry.url}
                className="group relative block overflow-hidden border border-border bg-muted/20 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-orange-500/70"
              >
                <article
                  className={
                    latestEntry.image
                      ? "grid md:grid-cols-[1.05fr_0.95fr]"
                      : "relative overflow-hidden"
                  }
                >
                  <div className="relative z-10 flex min-h-[22rem] flex-col justify-between p-7 md:p-10">
                    <div>
                      <time
                        dateTime={latestEntry.date}
                        className="font-mono text-sm text-orange-400 tabular-nums"
                      >
                        {formatChangelogDate(latestEntry.date)}
                      </time>
                      <h2
                        id="latest-update-heading"
                        className="mt-5 max-w-2xl text-3xl md:text-5xl font-medium tracking-[-0.035em] leading-[1.02] text-foreground text-balance"
                      >
                        {latestEntry.title}
                      </h2>
                      {latestEntry.description && (
                        <p className="mt-5 max-w-2xl text-base md:text-lg leading-relaxed text-muted-foreground text-pretty">
                          {latestEntry.description}
                        </p>
                      )}
                    </div>
                    <span className="mt-9 inline-flex items-center gap-2 text-sm font-medium text-foreground">
                      Read the update
                      <ArrowRight className="size-4 transition-transform duration-200 group-hover:translate-x-1" />
                    </span>
                  </div>
                  {latestEntry.image ? (
                    <div className="relative min-h-64 border-t border-border md:border-t-0 md:border-l">
                      {/* biome-ignore lint/performance/noImgElement: Changelog media keeps its natural dimensions. */}
                      <img
                        src={latestEntry.image}
                        alt={`${latestEntry.title} product preview`}
                        className="absolute inset-0 size-full object-cover transition-transform duration-300 group-hover:scale-[1.015]"
                      />
                    </div>
                  ) : (
                    <div
                      aria-hidden="true"
                      className="pointer-events-none absolute -right-20 -top-24 size-72 rounded-full bg-orange-500/10 blur-3xl"
                    />
                  )}
                </article>
              </Link>
            </section>

            {previousEntries.length > 0 && (
              <section aria-labelledby="earlier-updates-heading" className="mt-20">
                <div className="flex items-end justify-between gap-6 border-b border-border pb-5">
                  <div>
                    <span className="text-xs font-mono uppercase tracking-[0.16em] text-muted-foreground">
                      Archive
                    </span>
                    <h2
                      id="earlier-updates-heading"
                      className="mt-2 text-2xl md:text-3xl font-medium tracking-[-0.025em] text-foreground"
                    >
                      Earlier updates
                    </h2>
                  </div>
                  <span className="hidden sm:block text-sm text-muted-foreground tabular-nums">
                    {previousEntries.length} published
                  </span>
                </div>
                <div>
                  {previousEntries.map((entry) => (
                    <ChangelogCard key={entry.url} entry={entry} />
                  ))}
                </div>
              </section>
            )}
          </>
        )}
      </div>
    </main>
  );
}
