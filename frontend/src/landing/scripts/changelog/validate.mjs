import fs from "node:fs";
import path from "node:path";
import matter from "gray-matter";
import { extractPullRequestNumbers } from "./changelog-core.mjs";

const root = process.cwd();
const changelogDirectory = path.join(root, "content/changelog");
const publicDirectory = path.join(root, "public");
const errors = [];

for (const file of fs.readdirSync(changelogDirectory).filter((name) => name.endsWith(".mdx"))) {
	const filePath = path.join(changelogDirectory, file);
	const raw = fs.readFileSync(filePath, "utf8");
	const { data } = matter(raw);

	for (const property of ["title", "description", "date"]) {
		if (!data[property]) errors.push(`${file}: missing ${property} frontmatter`);
	}

	if (data.date && Number.isNaN(new Date(data.date).getTime())) {
		errors.push(`${file}: invalid date frontmatter`);
	}

	if (data.image) {
		const imagePath = path.join(publicDirectory, String(data.image).replace(/^\//, ""));
		if (!fs.existsSync(imagePath)) errors.push(`${file}: image does not exist: ${data.image}`);
	}

	if (data.rangeStart || data.rangeEnd) {
		const references = [
			...raw.matchAll(
				/github\.com\/[^/]+\/[^/]+\/pull\/(\d+)|(?:^|[\s(])#(\d+)/gm,
			),
		].map((match) => Number(match[1] ?? match[2]));
		const uniqueReferences = extractPullRequestNumbers(raw);
		if (references.length !== uniqueReferences.size) {
			errors.push(`${file}: contains a duplicate pull request reference`);
		}
	}
}

if (errors.length > 0) {
	console.error(errors.map((error) => `- ${error}`).join("\n"));
	process.exit(1);
}

console.log("Changelog content is valid.");
