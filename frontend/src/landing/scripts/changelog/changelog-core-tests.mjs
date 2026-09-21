import assert from "node:assert/strict";
import test from "node:test";
import {
	classifyPullRequest,
	extractPullRequestNumbers,
	renderHistoricalWeek,
	renderWeeklyDraft,
} from "./changelog-core.mjs";

const pullRequest = (overrides = {}) => ({
	number: 42,
	title: "feat(chat): keep queued messages after restart",
	url: "https://github.com/Untrivial-ai/agent-orchestrator/pull/42",
	labels: [],
	mergedAt: "2026-09-20T12:00:00Z",
	...overrides,
});

test("classifies conventional user-facing changes", () => {
	assert.equal(classifyPullRequest(pullRequest()), "feature");
	assert.equal(
		classifyPullRequest(pullRequest({ title: "fix: restore terminal focus" })),
		"fix",
	);
	assert.equal(
		classifyPullRequest(pullRequest({ title: "chore: update dependencies" })),
		"skip",
	);
});

test("explicit labels override the conventional type", () => {
	assert.equal(
		classifyPullRequest(
			pullRequest({ title: "chore: ship a visible migration", labels: ["changelog:include"] }),
		),
		"improvement",
	);
	assert.equal(
		classifyPullRequest(pullRequest({ labels: ["changelog:skip"] })),
		"skip",
	);
});

test("extracts unique pull request references", () => {
	const content = [
		"https://github.com/Untrivial-ai/agent-orchestrator/pull/42",
		"https://github.com/Untrivial-ai/agent-orchestrator/pull/42",
		"Shipped in (#99)",
	].join("\n");
	assert.deepEqual([...extractPullRequestNumbers(content)], [42, 99]);
});

test("renders a reviewable weekly MDX draft", () => {
	const result = renderWeeklyDraft({
		pullRequests: [
			pullRequest(),
			pullRequest({ number: 43, title: "fix(files): render large diffs", url: "https://github.com/Untrivial-ai/agent-orchestrator/pull/43" }),
		],
		startDate: "2026-09-14",
		endDate: "2026-09-20",
	});
	assert.ok(result);
	assert.match(result.content, /rangeStart: "2026-09-14"/);
	assert.match(result.content, /<PRBadge url=".*\/pull\/42" \/>/);
	assert.match(result.content, /\*\*Bug fixes\*\*/);
	assert.equal(result.counts.included, 2);
});

test("renders historical weeks without an editorial checklist", () => {
	const result = renderHistoricalWeek({
		changes: [pullRequest()],
		startDate: "2026-09-14",
		endDate: "2026-09-20",
		totalCommits: 3,
	});
	assert.match(result.content, /historical: true/);
	assert.match(result.content, /## Features/);
	assert.doesNotMatch(result.content, /Review before merge/);
});
