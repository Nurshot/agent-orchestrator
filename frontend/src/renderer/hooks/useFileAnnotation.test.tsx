import { act, renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { useFileAnnotation } from "./useFileAnnotation";

describe("useFileAnnotation", () => {
	it("closes feedback when the same trigger is clicked again", () => {
		const { result } = renderHook(() => useFileAnnotation("sess-1"));
		const target = {
			path: "src/App.tsx",
			side: "new" as const,
			line: 12,
			scope: "unstaged",
			surface: "focused" as const,
		};

		act(() => result.current.begin(target));
		expect(result.current.target).toEqual(target);

		act(() => result.current.begin({ ...target }));
		expect(result.current.target).toBeNull();
	});

	it("cancels an open composer when the file source changes", () => {
		const { result, rerender } = renderHook(({ source }) => useFileAnnotation("sess-1", source), { initialProps: { source: "Workspace" } });
		act(() => result.current.begin({ path: "src/App.tsx", side: "new", line: 12, scope: "unstaged", surface: "focused" }));
		act(() => result.current.setDraft("stale feedback"));
		rerender({ source: "PR #42" });
		expect(result.current.target).toBeNull();
		expect(result.current.draft).toBe("");
	});
});
