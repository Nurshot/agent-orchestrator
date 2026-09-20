import { Cloud, FolderOpen, GitFork } from "lucide-react";
import { useCallback, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { aoBridge } from "../lib/bridge";
import { useCloudGate } from "../hooks/useCloudGate";
import {
	CreateProjectFlow,
	importValidationMessage,
	type PreparedProjectInput,
} from "./CreateProjectFlow";
import { OnboardingCloudDialog } from "./OnboardingCloudDialog";
import { SetupRow } from "./SetupList";
import type { components } from "../../api/schema";

type ProjectMode = "folder" | "git";
type ImportValidationResult = components["schemas"]["ImportValidationResult"];

/** The daemon says why it rejected a folder, and the create project flow
 *  already turns those codes into sentences. Reuse them here so onboarding
 *  reports the same reason the rest of the app does, rather than a shrug. */
function folderRejectionMessage(result: ImportValidationResult): string {
	if (result.nextStep === "choose_import_kind") {
		return "That folder holds more than one repository. Open a single repository, or add the folder as a workspace from the board.";
	}
	return importValidationMessage(result);
}

export function OnboardingProjectSetup({
	mode,
	onModeChange,
	onPrepared,
	onCloudProjectCreated,
	preparedProject,
}: {
	mode: ProjectMode;
	onModeChange: (mode: ProjectMode) => void;
	onPrepared: (input: PreparedProjectInput | null) => void;
	onCloudProjectCreated: () => void;
	preparedProject: PreparedProjectInput | null;
}) {
	const { t } = useTranslation();
	const { cloudEnabled } = useCloudGate();
	const [triggerNonce, setTriggerNonce] = useState(0);
	const [folderError, setFolderError] = useState<string | null>(null);
	const [isSelectingFolder, setIsSelectingFolder] = useState(false);
	const [showCloud, setShowCloud] = useState(false);
	const lastPreparedPath = useRef<string | null>(preparedProject?.path ?? null);

	const resetPrepared = useCallback(() => {
		lastPreparedPath.current = null;
		onPrepared(null);
	}, [onPrepared]);

	const startCloud = useCallback(() => {
		resetPrepared();
		setFolderError(null);
		setShowCloud(true);
	}, [resetPrepared]);

	const startImport = useCallback(
		async (next: ProjectMode) => {
			resetPrepared();
			setFolderError(null);
			onModeChange(next);
			if (next === "folder") {
				setIsSelectingFolder(true);
				try {
					const path = await aoBridge.app.chooseDirectory("Choose a project repository");
					if (!path) return;
					const { data, error } = await apiClient.POST("/api/v1/imports/validate", {
						body: { importKind: "project", path },
					});
					if (error || !data) throw new Error(apiErrorMessage(error, "AO could not check that folder. Try again, or pick another one."));
					if (!data.isValid || data.nextStep === "error" || data.nextStep === "choose_import_kind") {
						throw new Error(folderRejectionMessage(data));
					}
					let defaultBranch: string | undefined;
					try {
						defaultBranch = (await aoBridge.app.getRepositoryBranch(path)) ?? undefined;
					} catch {
						defaultBranch = undefined;
					}
					onPrepared({
						path,
						defaultBranch,
						repositorySetup: !data.root.isRepo
							? "NOT_A_GIT_REPO"
							: !data.root.hasCommit
								? "PROJECT_UNBORN"
								: null,
					});
				} catch (error) {
					setFolderError(error instanceof Error ? error.message : "AO could not use that folder. Pick another one.");
				} finally {
					setIsSelectingFolder(false);
				}
				return;
			}
			setTriggerNonce((nonce) => nonce + 1);
		},
		[onModeChange, onPrepared, resetPrepared],
	);

	return (
		<>
			<div className="flex w-full max-w-[520px] flex-col gap-3">
				<SetupRow
					variant="card"
					disabled={isSelectingFolder}
					icon={<GitFork aria-hidden="true" />}
					label="Clone from Git"
					description="Start from a remote repository using an HTTPS or SSH URL"
					onClick={() => void startImport("git")}
				/>
				<SetupRow
					variant="card"
					disabled={isSelectingFolder}
					icon={<FolderOpen aria-hidden="true" />}
					label="Open local folder"
					description="Bring in a repository that is already on this machine"
					onClick={() => void startImport("folder")}
				/>
				{cloudEnabled ? (
					<SetupRow
						variant="card"
						disabled={isSelectingFolder}
						icon={<Cloud aria-hidden="true" />}
						label={t("onboarding.createCloudProject")}
						description={t("onboarding.createCloudProjectDetail")}
						onClick={startCloud}
					/>
				) : null}
				{folderError ? <p className="col-span-full text-center text-xs text-destructive">{folderError}</p> : null}
			</div>
			{cloudEnabled && showCloud ? (
				<OnboardingCloudDialog onClose={() => setShowCloud(false)} onCreated={onCloudProjectCreated} />
			) : null}
			<CreateProjectFlow
				mode="choose"
				variant="onboarding"
				onCloneProject={async () => undefined}
				onCreateProject={async () => undefined}
				onInitializeProject={async (path) => {
					const { error } = await apiClient.POST("/api/v1/projects/initialize", { body: { path } });
					if (error) throw new Error(apiErrorMessage(error));
				}}
				onboardingTrigger={{
					kind: mode === "folder" ? "folder" : "clone",
					nonce: triggerNonce,
				}}
				prepareOnly={{
					onPrepared: (input) => {
						lastPreparedPath.current = input.path;
						onPrepared(input);
					},
				}}
			/>
		</>
	);
}
