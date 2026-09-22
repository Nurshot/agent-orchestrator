import { create } from "zustand";
import type { CloudCpCreateSessionCoder, CloudCpSessionRepo } from "../lib/cloud-cp/types";

export type CoderSize = "small" | "medium" | "large";

// The per-session Coder picker choices (template + curated form). Kept in memory
// only — these are per-session choices, not a persisted preference, and reset
// when the composer clears. "" templateId means the deployment default template,
// which sends no picker options and preserves the pre-existing behavior.
export interface CoderSessionOptionsState {
	templateId: string;
	size: CoderSize;
	startupScript: string;
	extraRepos: CloudCpSessionRepo[];
	setTemplateId: (templateId: string) => void;
	setSize: (size: CoderSize) => void;
	setStartupScript: (startupScript: string) => void;
	setExtraRepos: (extraRepos: CloudCpSessionRepo[]) => void;
	reset: () => void;
}

const initialState = {
	templateId: "",
	size: "medium" as CoderSize,
	startupScript: "",
	extraRepos: [] as CloudCpSessionRepo[],
};

export const useCoderSessionOptionsStore = create<CoderSessionOptionsState>((set) => ({
	...initialState,
	setTemplateId: (templateId) => set({ templateId }),
	setSize: (size) => set({ size }),
	setStartupScript: (startupScript) => set({ startupScript }),
	setExtraRepos: (extraRepos) => set({ extraRepos }),
	reset: () => set({ ...initialState, extraRepos: [] }),
}));

// buildCoderRequestOptions turns the picker state into the createSession `coder`
// payload, or undefined when the choice is "Default with no extra repos" — in
// which case the request omits `coder` entirely and behaves exactly as before.
// Size and startup are only sent alongside a chosen (non-default) template,
// mirroring the control plane's validation.
export function buildCoderRequestOptions(state: {
	templateId: string;
	size: CoderSize;
	startupScript: string;
	extraRepos: CloudCpSessionRepo[];
}): CloudCpCreateSessionCoder | undefined {
	const templateId = state.templateId.trim();
	const extraRepos = state.extraRepos
		.map((repo) => ({ url: repo.url.trim(), branch: repo.branch?.trim() || undefined }))
		.filter((repo) => repo.url.length > 0);
	if (!templateId && extraRepos.length === 0) return undefined;
	const coder: CloudCpCreateSessionCoder = {};
	if (templateId) {
		coder.templateId = templateId;
		coder.size = state.size;
		if (state.startupScript.trim().length > 0) coder.startupScript = state.startupScript;
	}
	if (extraRepos.length > 0) coder.extraRepos = extraRepos;
	return coder;
}
