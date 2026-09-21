import { type ChangelogEntry, slugify } from "./changelog-utils";
import { normalizeContentDate } from "./content-utils";

const RELEASES_REPO = "Untrivial-ai/agent-orchestrator";
const STABLE_TAG = /^v\d+\.\d+\.\d+$/;

interface GitHubRelease {
	tag_name: string;
	name: string | null;
	body: string | null;
	published_at: string;
	html_url: string;
	draft: boolean;
	prerelease: boolean;
}

function isStableRelease(release: GitHubRelease): boolean {
	return (
		!release.draft &&
		!release.prerelease &&
		STABLE_TAG.test(release.tag_name)
	);
}

function releaseToEntry(release: GitHubRelease): ChangelogEntry {
	const title = release.name?.trim() || release.tag_name;
	return {
		slug: slugify(release.tag_name),
		url: `/changelog/${slugify(release.tag_name)}`,
		title,
		description: `Technical release notes for ${title}.`,
		date: normalizeContentDate(release.published_at) as string,
		content:
			release.body?.trim() ||
			`See the complete ${title} release notes and downloads on GitHub.`,
		releaseUrl: release.html_url,
		source: "release",
		draft: false,
	};
}

let stableReleaseEntriesPromise: Promise<ChangelogEntry[]> | undefined;

async function loadStableReleaseEntries(): Promise<ChangelogEntry[]> {
	try {
		const token = process.env.GITHUB_TOKEN;
		const pages = await Promise.all(
			[1, 2, 3, 4].map(async (page) => {
				const response = await fetch(
					`https://api.github.com/repos/${RELEASES_REPO}/releases?per_page=25&page=${page}`,
					{
						headers: {
							Accept: "application/vnd.github+json",
							"X-GitHub-Api-Version": "2022-11-28",
							...(token ? { Authorization: `Bearer ${token}` } : {}),
						},
						next: { revalidate: 3600 },
					},
				);

				if (!response.ok) {
					throw new Error(`GitHub releases request failed with ${response.status}`);
				}
				return (await response.json()) as GitHubRelease[];
			}),
		);

		return pages
			.flat()
			.filter(isStableRelease)
			.map(releaseToEntry)
			.sort(
				(a, b) => new Date(b.date).getTime() - new Date(a.date).getTime(),
			);
	} catch (error) {
		console.warn("Unable to load GitHub releases for the changelog archive", error);
		return [];
	}
}

export function getStableReleaseEntries(): Promise<ChangelogEntry[]> {
	stableReleaseEntriesPromise ??= loadStableReleaseEntries();
	return stableReleaseEntriesPromise;
}

export async function getStableReleaseEntry(
	slug: string,
): Promise<ChangelogEntry | undefined> {
	const releases = await getStableReleaseEntries();
	return releases.find((release) => release.slug === slug);
}
