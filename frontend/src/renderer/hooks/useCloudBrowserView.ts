import { useCallback, useEffect, useMemo, useSyncExternalStore } from "react";
import type { BrowserViewModel, CloudBrowserSurfaceModel } from "./useBrowserView";
import {
	CloudBrowserStream,
	type CloudBrowserControl,
	type CloudBrowserSnapshot,
} from "../lib/cloud-browser-stream";
import { useCloudCp } from "./useCloudCp";

const streams = new Map<string, CloudBrowserStream>();

export function resetCloudBrowserStreamsForTest(): void {
	for (const stream of streams.values()) stream.dispose();
	streams.clear();
}

export function useCloudBrowserView(options: {
	orgId?: string;
	sessionId: string;
	active: boolean;
}): BrowserViewModel {
	const { baseUrl, client, ready } = useCloudCp();
	const key = options.orgId && baseUrl ? `${baseUrl}|${options.orgId}|${options.sessionId}` : "";
	const stream = useMemo(() => {
		if (!key || !options.orgId) return null;
		let current = streams.get(key);
		if (!current) {
			const created = new CloudBrowserStream({
				baseUrl,
				orgId: options.orgId,
				sessionId: options.sessionId,
				client,
				onIdle: () => {
					if (streams.get(key) === created) streams.delete(key);
				},
			});
			current = created;
			streams.set(key, current);
		}
		return current;
	}, [baseUrl, client, key, options.orgId, options.sessionId]);
	const empty = useMemo<CloudBrowserSnapshot>(() => ({
		status: "idle",
		frameUrl: "",
		frameWidth: 0,
		frameHeight: 0,
		frameSequence: 0,
		streamEpoch: 0,
		url: "",
		title: "",
		tabs: [],
		activeTabId: "",
		owner: "idle",
		canOperate: false,
		canGoBack: false,
		canGoForward: false,
		isLoading: false,
		dialogOpen: false,
		dialogType: "",
		dialogText: "",
		dialogPrompt: "",
		viewportPending: true,
		error: "",
		errorRequestId: "",
	}), []);
	const snapshot = useSyncExternalStore(
		stream?.subscribe ?? (() => () => undefined),
		stream?.getSnapshot ?? (() => empty),
		() => empty,
	);

	useEffect(() => {
		if (!stream || !ready || !options.active) return;
		stream.retain();
		return () => stream.release();
	}, [options.active, ready, stream]);

	const send = useCallback(
		(control: Omit<CloudBrowserControl, "version" | "streamEpoch">) => stream?.send(control) ?? false,
		[stream],
	);
	const setViewport = useCallback((width: number, height: number) => stream?.setViewport(width, height), [stream]);
	const reportPaint = useCallback(
		(frameSequence: number, decodeMs: number, paintMs: number) => stream?.reportPaint(frameSequence, decodeMs, paintMs),
		[stream],
	);
	const retry = useCallback(() => stream?.retryNow(), [stream]);
	const surface = useMemo<CloudBrowserSurfaceModel>(() => ({
		snapshot,
		send,
		setViewport,
		reportPaint,
		retry,
	}), [reportPaint, retry, send, setViewport, snapshot]);
	const viewId = `cloud-browser-${options.sessionId}`;
	const navigate = useCallback(async (url: string) => {
		send({ type: "navigate", operation: "open", url });
	}, [send]);
	const tab = useCallback(async (operation: string, tabId?: string, url?: string) => {
		send({ type: "tab", operation, tabId, url });
	}, [send]);

	return {
		viewId,
		navState: {
			viewId,
			url: snapshot.url,
			title: snapshot.title,
			canGoBack: snapshot.canGoBack,
			canGoForward: snapshot.canGoForward,
			isLoading: snapshot.isLoading,
			error: snapshot.status === "fatal" ? snapshot.error : undefined,
		},
		slotRef: () => undefined,
		navigate,
		goBack: async () => { send({ type: "navigate", operation: "back" }); },
		goForward: async () => { send({ type: "navigate", operation: "forward" }); },
		reload: async () => { send({ type: "navigate", operation: "reload" }); },
		stop: async () => undefined,
		tabs: snapshot.tabs,
		activeTabId: snapshot.activeTabId,
		tabNotice: snapshot.status === "reconnecting" ? "Reconnecting" : "",
		selectTab: async (tabId) => tab("select", tabId),
		closeTab: async (tabId) => tab("close", tabId),
		openTab: async (url) => tab("new", undefined, url),
		openLink: navigate,
		reorderTabs: () => undefined,
		closedTabs: [],
		reopenClosedTab: async () => undefined,
		devtoolsState: { viewId, open: false, activeTabId: snapshot.activeTabId, placement: "undocked" },
		profileState: { viewId, profileId: null, temporary: true },
		openDevTools: async () => undefined,
		closeDevTools: async () => undefined,
		setDevToolsPlacement: async () => undefined,
		agentBrowserActive: snapshot.owner === "agent",
		agentBrowserActivity: snapshot.owner === "agent" ? {
			viewId,
			active: true,
			action: "browser",
			phase: "started",
		} : null,
		destroy: () => stream?.release(),
		annotationMode: false,
		annotationState: { count: 0, screenshotCount: 0, hasDraft: false },
		setAnnotationMode: async () => undefined,
		annotationAction: async () => undefined,
		cloudSurface: surface,
	};
}
