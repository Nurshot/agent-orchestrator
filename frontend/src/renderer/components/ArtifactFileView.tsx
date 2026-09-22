import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { sessionArtifactFileQueryOptions } from "../hooks/useSessionWorkspaceFiles";
import { useFileAnnotation } from "../hooks/useFileAnnotation";
import { ReadOnlyFileView } from "./ReadOnlyFileView";
import { PanelMessage, RetryButton } from "./WorkspaceDiffView";

/**
 * Read-only content view for one file in a session's artifact directory.
 * Artifacts live outside the git workspace (no diff, no status), so this
 * fetches raw content straight from the preview-files route instead of
 * going through the workspace-diff machinery `SessionFileExplorer` uses.
 */
export function ArtifactFileView({
	artifactName,
	path,
	sessionId,
}: {
	artifactName: string;
	path: string;
	sessionId: string;
}) {
	const { t } = useTranslation();
	const annotation = useFileAnnotation(sessionId, { source: artifactName });
	const query = useQuery(sessionArtifactFileQueryOptions(sessionId, path, t("files.error.loadArtifact")));

	return (
		<div className="flex h-full min-h-0 flex-col">
			<div className="shrink-0 truncate border-b border-(--color-border-settings-input) px-3 py-2 text-sm font-semibold" title={artifactName}>
				{artifactName}
			</div>
			<div className="min-h-0 flex-1 overflow-auto">
				{query.isPending ? (
					<PanelMessage>{t("files.loading")}</PanelMessage>
				) : query.isError ? (
					<PanelMessage action={<RetryButton onClick={() => void query.refetch()} />}>
						{t("files.error.loadArtifact")}
					</PanelMessage>
				) : (
					<ReadOnlyFileView annotation={annotation} detail={query.data} sessionId={sessionId} />
				)}
			</div>
		</div>
	);
}
