import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ArtifactFileView } from "./ArtifactFileView";
import { TooltipProvider } from "./ui/tooltip";

vi.mock("../lib/api-client", () => ({ getApiBaseUrl: () => "http://127.0.0.1:3001" }));
vi.mock("../hooks/usePierreFileHighlight", () => ({ usePierreFileHighlightReady: () => true }));
vi.mock("./ReadOnlyFileView", () => ({
	ReadOnlyFileView: ({ detail }: { detail: { content: string } }) => <code>{detail.content}</code>,
}));

const fetchMock = vi.fn();

function renderWithQuery(ui: ReactNode) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={queryClient}>
			<TooltipProvider>{ui}</TooltipProvider>
		</QueryClientProvider>,
	);
}

function scrollContainer(): HTMLElement {
	const element = document.querySelector(".board-scrollbar");
	if (!(element instanceof HTMLElement)) throw new Error("expected artifact scroll container");
	return element;
}

describe("ArtifactFileView", () => {
	beforeEach(() => {
		vi.stubGlobal("fetch", fetchMock);
	});

	afterEach(() => {
		fetchMock.mockReset();
		vi.unstubAllGlobals();
	});

	it("fetches a .txt artifact through the artifact-scoped preview route and renders its content", async () => {
		fetchMock.mockResolvedValue(new Response("hello world", { status: 200 }));

		renderWithQuery(<ArtifactFileView artifactName="notes.txt" path="notes.txt" sessionId="sess-1" />);

		await waitFor(() => expect(screen.getByText("hello world")).toBeInTheDocument());
		expect(scrollContainer()).toHaveClass("overflow-y-auto", "min-h-0");

		expect(fetchMock).toHaveBeenCalledWith(
			"http://127.0.0.1:3001/api/v1/sessions/sess-1/preview/files/__ao_artifacts__/notes.txt?raw=1",
		);
	});

	it("opens a .md artifact already rendered, not as raw markdown source", async () => {
		fetchMock.mockResolvedValue(new Response("# Notes\n\nhello", { status: 200 }));

		renderWithQuery(<ArtifactFileView artifactName="notes.md" path="notes.md" sessionId="sess-1" />);

		await waitFor(() => expect(screen.getByRole("heading", { name: "Notes" })).toBeInTheDocument());
		expect(fetchMock).toHaveBeenCalledWith(
			"http://127.0.0.1:3001/api/v1/sessions/sess-1/preview/files/__ao_artifacts__/notes.md?raw=1",
		);
		expect(screen.getByText("hello")).toBeInTheDocument();
		expect(screen.queryByText(/^# Notes/)).not.toBeInTheDocument();
	});

	it("URL-encodes nested artifact paths per segment", async () => {
		fetchMock.mockResolvedValue(new Response("content", { status: 200 }));

		renderWithQuery(<ArtifactFileView artifactName="report.txt" path="sub dir/report.txt" sessionId="sess-1" />);

		await waitFor(() => expect(fetchMock).toHaveBeenCalled());
		expect(fetchMock).toHaveBeenCalledWith(
			"http://127.0.0.1:3001/api/v1/sessions/sess-1/preview/files/__ao_artifacts__/sub%20dir/report.txt?raw=1",
		);
	});

	it("does not show an edit affordance (no write endpoint for artifacts yet)", async () => {
		fetchMock.mockResolvedValue(new Response("content", { status: 200 }));

		renderWithQuery(<ArtifactFileView artifactName="notes.txt" path="notes.txt" sessionId="sess-1" />);

		await waitFor(() => expect(screen.getByText("content")).toBeInTheDocument());
		expect(screen.queryByRole("button", { name: "Edit file" })).not.toBeInTheDocument();
	});

	it("shows a retry option when the fetch fails", async () => {
		fetchMock.mockResolvedValue(new Response("nope", { status: 404 }));

		renderWithQuery(<ArtifactFileView artifactName="notes.txt" path="notes.txt" sessionId="sess-1" />);

		await waitFor(() => expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument());
	});
});
