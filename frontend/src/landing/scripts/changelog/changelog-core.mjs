const INTERNAL_TYPES = new Set(["build", "chore", "ci", "docs", "refactor", "test"]);

function labelNames(pullRequest) {
	return new Set(
		(pullRequest.labels ?? []).map((label) =>
			(typeof label === "string" ? label : label.name).toLowerCase(),
		),
	);
}

function conventionalTitle(title) {
	const match = title.match(/^([a-z]+)(?:\(([^)]+)\))?!?:\s*(.+)$/i);
	return match
		? { type: match[1].toLowerCase(), scope: match[2]?.toLowerCase(), subject: match[3] }
		: { type: undefined, scope: undefined, subject: title };
}

export function classifyPullRequest(pullRequest) {
	const labels = labelNames(pullRequest);
	if (labels.has("changelog:skip")) return "skip";

	const include = labels.has("changelog:include");
	const { type } = conventionalTitle(pullRequest.title);

	if (!include && type && INTERNAL_TYPES.has(type)) return "skip";
	if (type === "fix" || labels.has("bug")) return "fix";
	if (type === "feat" || labels.has("feature")) return "feature";
	if (type === "perf" || labels.has("enhancement") || include) return "improvement";

	return "skip";
}

export function cleanPullRequestTitle(title) {
	const { subject } = conventionalTitle(title);
	const cleaned = subject.replace(/\s*\(#\d+\)\s*$/, "").trim();
	return cleaned ? cleaned[0].toUpperCase() + cleaned.slice(1) : title;
}

export function escapeMdxText(value) {
	return value
		.replaceAll("&", "&amp;")
		.replaceAll("<", "&lt;")
		.replaceAll(">", "&gt;")
		.replaceAll("{", "&#123;")
		.replaceAll("}", "&#125;")
		.replaceAll("[", "\\[")
		.replaceAll("]", "\\]");
}

export function extractPullRequestNumbers(content) {
	const numbers = new Set();
	for (const match of content.matchAll(
		/github\.com\/[^/]+\/[^/]+\/pull\/(\d+)|(?:^|[\s(])#(\d+)/gm,
	)) {
		numbers.add(Number(match[1] ?? match[2]));
	}
	return numbers;
}

function formatDate(date) {
	return new Intl.DateTimeFormat("en-US", {
		month: "short",
		day: "numeric",
		year: "numeric",
		timeZone: "UTC",
	}).format(new Date(`${date}T12:00:00Z`));
}

function badge(pullRequest) {
	return `<PRBadge url="${pullRequest.url}" />`;
}

function featureSection(pullRequest) {
	const title = escapeMdxText(cleanPullRequestTitle(pullRequest.title));
	return [
		`## ${title} ${badge(pullRequest)}`,
		"",
		`<!-- Editor: replace this line with what users can do now and why it matters. -->`,
		`${title}.`,
	].join("\n");
}

function bullet(pullRequest) {
	const title = escapeMdxText(cleanPullRequestTitle(pullRequest.title));
	return `- **${title}** ${badge(pullRequest)}`;
}

export function renderWeeklyDraft({ pullRequests, startDate, endDate }) {
	const categorized = pullRequests
		.map((pullRequest) => ({
			...pullRequest,
			category: classifyPullRequest(pullRequest),
		}))
		.filter((pullRequest) => pullRequest.category !== "skip");

	if (categorized.length === 0) return null;

	const allFeatures = categorized.filter((pullRequest) => pullRequest.category === "feature");
	const highlights = allFeatures.slice(0, 4);
	const improvements = [
		...allFeatures.slice(4),
		...categorized.filter((pullRequest) => pullRequest.category === "improvement"),
	];
	const fixes = categorized.filter((pullRequest) => pullRequest.category === "fix");
	const title = `Weekly update — ${formatDate(startDate)} to ${formatDate(endDate)}`;
	const description = [
		highlights.length ? `${highlights.length} highlights` : null,
		improvements.length ? `${improvements.length} improvements` : null,
		fixes.length ? `${fixes.length} fixes` : null,
	]
		.filter(Boolean)
		.join(", ");

	const sections = [
		"---",
		`title: ${JSON.stringify(title)}`,
		`description: ${JSON.stringify(`What changed in Agent Orchestrator this week: ${description}.`)}`,
		`date: ${JSON.stringify(endDate)}`,
		`rangeStart: ${JSON.stringify(startDate)}`,
		`rangeEnd: ${JSON.stringify(endDate)}`,
		"---",
		"",
		"{/*",
		"Review before merge:",
		"- Replace the working title and generated highlight copy.",
		"- Confirm every item is available to users and not behind an internal flag.",
		"- Add a real product image for the strongest visual change when possible.",
		"- Remove this checklist after the editorial pass.",
		"*/}",
	];

	if (highlights.length > 0) {
		sections.push("", ...highlights.flatMap((pullRequest) => ["", featureSection(pullRequest)]));
	}

	if (improvements.length > 0) {
		sections.push("", "## Improvements", "", ...improvements.map(bullet));
	}

	if (fixes.length > 0) {
		sections.push("", "---", "", "**Bug fixes**", "", ...fixes.map(bullet));
	}

	return {
		content: `${sections.join("\n").trim()}\n`,
		counts: {
			included: categorized.length,
			highlights: highlights.length,
			improvements: improvements.length,
			fixes: fixes.length,
			skipped: pullRequests.length - categorized.length,
		},
	};
}

export function renderPullRequestBody({ counts, startDate, endDate, entryPath }) {
	return `## What and why

Prepare the weekly product update for ${startDate} through ${endDate}.

- ${counts.included} merged pull requests included
- ${counts.highlights} suggested highlights
- ${counts.improvements} additional improvements
- ${counts.fixes} fixes
- ${counts.skipped} internal or excluded changes skipped

Draft: \`${entryPath}\`

## Human review

- [ ] Confirm the included changes are available to users
- [ ] Remove internal, duplicated, reverted, or feature-flagged work
- [ ] Rewrite the title, summary, and highlight copy in plain language
- [ ] Add real screenshots or a short recording where they improve understanding
- [ ] Check every pull request reference
- [ ] Review the desktop and mobile changelog preview
- [ ] Remove the editorial checklist from the MDX file

## Publishing

Merging this pull request publishes the update through the existing landing-site deployment. This workflow never merges or publishes directly.
`;
}
