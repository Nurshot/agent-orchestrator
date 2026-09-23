import { useLocalSearchParams, useRouter } from "expo-router";
import { useEffect } from "react";
import { ConversationActionsSheet } from "../../lib/chat/ConversationActionsSheet";
import { readChatSheet, releaseChatSheet } from "../../lib/chat/chatSheetRegistry";
import { useSheetEntryPresent } from "../../lib/chat/useSheetEntryPresent";
import { backOr } from "../../lib/backNavigation";
import { useMobileConversation } from "../../lib/chat/useConversation";
import { useApp } from "../../lib/store";

export default function ConversationActionsRoute() {
	const router = useRouter();
	const { sheetKey } = useLocalSearchParams<{ sheetKey?: string }>();
	const entry = readChatSheet(sheetKey);
	const { config } = useApp();
	const live = useMobileConversation(entry?.kind === "conversation-actions" ? config : null, entry?.kind === "conversation-actions" ? entry.sessionId : "");
	useEffect(() => () => releaseChatSheet(sheetKey), [sheetKey]);
	// Dismiss rather than draw an empty sheet when the hand-off is gone.
	useSheetEntryPresent(entry?.kind === "conversation-actions");
	if (entry?.kind !== "conversation-actions") return null;
	const closeThen = (action: () => void) => {
		backOr(router);
		setTimeout(action, 220);
	};
	return <ConversationActionsSheet entry={entry} snapshot={live.snapshot ?? entry.snapshot} onAction={closeThen} />;
}

export { SheetErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";
