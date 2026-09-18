import { renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useLinkPreview } from "./useLinkPreview";

const { getMock } = vi.hoisted(() => ({ getMock: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock },
	apiErrorMessage: () => "request failed",
}));

// The hook caches per URL in the shared module-level queryClient, so each test
// uses distinct URLs to stay isolated.

afterEach(() => {
	getMock.mockReset();
});

describe("useLinkPreview", () => {
	it("does not fetch until enabled", async () => {
		renderHook(() => useLinkPreview("https://idle.example.test/", false));
		await new Promise((resolve) => setTimeout(resolve, 50));
		expect(getMock).not.toHaveBeenCalled();
	});

	it("refetches when the url changes on the same hook instance", async () => {
		// Regression: XtermTerminal reuses one hook instance across hovered links;
		// a previous URL's preview must never be returned for the current URL.
		const urlA = "https://videos.example.test/never-gonna";
		const urlB = "https://social.example.test/home";
		getMock.mockImplementation((_path: string, opts: { params: { query: { url: string } } }) =>
			Promise.resolve({ data: { url: opts.params.query.url, title: `Title for ${opts.params.query.url}` }, error: undefined }),
		);

		const { result, rerender } = renderHook(({ url }) => useLinkPreview(url, true), {
			initialProps: { url: urlA },
		});
		await waitFor(() => expect(result.current.data?.url).toBe(urlA));

		rerender({ url: urlB });
		expect(result.current.data?.url).not.toBe(urlA);
		await waitFor(() => expect(result.current.data?.url).toBe(urlB));
		expect(getMock).toHaveBeenCalledTimes(2);

		// Re-hovering the first URL serves the cache without another request.
		rerender({ url: urlA });
		await waitFor(() => expect(result.current.data?.url).toBe(urlA));
		expect(getMock).toHaveBeenCalledTimes(2);
	});

	it("surfaces fetch failure as isError", async () => {
		getMock.mockResolvedValue({ data: undefined, error: { message: "upstream down" } });
		const { result } = renderHook(() => useLinkPreview("https://down.example.test/", true));
		await waitFor(() => expect(result.current.isError).toBe(true));
		expect(result.current.data).toBeUndefined();
	});
});
