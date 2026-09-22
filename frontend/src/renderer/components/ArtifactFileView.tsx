import { useFileAnnotation } from "../hooks/useFileAnnotation";
import { FileContentPane, type FileViewMode } from "./FileContentPane";

const ARTIFACT_SOURCE = { kind: "artifact" as const };

// Mirrors FileContentPane's own canRenderMarkdown extension check: a markdown
// artifact should open already rendered, the same way GitHub opens a README,
// rather than making the reader click over to the "Rendered" tab themselves.
function initialModeFor(path: string): FileViewMode {
	return /\.(md|markdown)$/i.test(path) ? "rendered" : "file";
}

/**
 * Content view for one file in a session's artifact directory, reusing the
 * same `FileContentPane` the workspace/PR Files flow uses (source-agnostic:
 * dispatches on `source.kind`). Artifacts fetch raw content straight from the
 * preview-files route rather than the workspace-diff machinery, since they
 * live outside the git workspace and have no diff/status.
 *
 * Read-only for now: `WorkspaceFileDetail.editable`/`fileFingerprint` are
 * unset for the artifact source (see `fetchSessionArtifactFile`), which is
 * exactly the signal `FileContentPane` already uses to hide its edit
 * affordance for any source. Wiring up editing later is then a matter of
 * adding a write endpoint and setting those two fields — no new UI.
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
	const annotation = useFileAnnotation(sessionId, { source: artifactName });

	return (
		<div className="h-full min-h-0">
			<FileContentPane annotation={annotation} initialMode={initialModeFor(path)} path={path} sessionId={sessionId} source={ARTIFACT_SOURCE} split={false} />
		</div>
	);
}
