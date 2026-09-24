import { Check, ChevronDown, Lock, Search } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { cn } from "../lib/utils";
import { Popover, PopoverContent, PopoverTrigger } from "./ui/popover";

export interface SearchablePickerOption {
	value: string;
	label: string;
	description?: string;
	private?: boolean;
}

export function SearchablePicker({
	ariaLabel,
	placeholder,
	searchPlaceholder,
	value,
	onChange,
	options,
	disabled = false,
	fixedScroll = false,
	className,
}: {
	ariaLabel: string;
	placeholder: string;
	searchPlaceholder: string;
	value: string;
	onChange: (value: string) => void;
	options: SearchablePickerOption[];
	disabled?: boolean;
	fixedScroll?: boolean;
	className?: string;
}) {
	const { t } = useTranslation();
	const [open, setOpen] = useState(false);
	const [search, setSearch] = useState("");
	const selected = options.find((option) => option.value === value);
	const filtered = options.filter((option) =>
		`${option.label} ${option.description ?? ""}`.toLowerCase().includes(search.trim().toLowerCase()),
	);

	return (
		<Popover open={open} onOpenChange={(next) => { setOpen(next); if (!next) setSearch(""); }}>
			<PopoverTrigger asChild>
				<button
					type="button"
					role="combobox"
					aria-label={ariaLabel}
					aria-expanded={open}
					aria-controls={open ? `${ariaLabel.replace(/\W+/g, "-").toLowerCase()}-options` : undefined}
					disabled={disabled}
					className={cn(
						"flex h-control-form w-full items-center gap-2 rounded-md border border-border bg-[var(--color-bg-import-card)] px-3 text-left text-[13px] text-foreground outline-none transition-colors hover:border-foreground/30 focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50",
						className,
					)}
				>
					{selected?.private ? <Lock className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" /> : null}
					<span className={cn("min-w-0 flex-1 truncate", !selected && "text-muted-foreground")}>{selected?.label ?? placeholder}</span>
					<ChevronDown className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
				</button>
			</PopoverTrigger>
			<PopoverContent align="start" className="w-[var(--radix-popover-trigger-width)] min-w-56 overflow-hidden p-0 shadow-xl">
				<div className="flex h-10 items-center gap-2 border-b border-border px-3">
					<Search className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
					<input
						autoFocus
						aria-label={searchPlaceholder}
						placeholder={searchPlaceholder}
						value={search}
						onChange={(event) => setSearch(event.target.value)}
						className="h-full min-w-0 flex-1 bg-transparent text-[13px] text-foreground outline-none placeholder:text-muted-foreground"
					/>
				</div>
				<div
					id={`${ariaLabel.replace(/\W+/g, "-").toLowerCase()}-options`}
					role="listbox"
					aria-label={ariaLabel}
					className={cn(
						"overscroll-contain p-1",
						fixedScroll ? "repository-picker-scrollbar h-56 overflow-y-scroll" : "settings-thin-scrollbar max-h-56 overflow-y-auto",
					)}
				>
					{filtered.length === 0 ? <p className="px-3 py-5 text-center text-xs text-muted-foreground">{t("common.noMatches", { defaultValue: "No matches" })}</p> : null}
					{filtered.map((option) => (
						<button
							key={option.value}
							type="button"
							role="option"
							aria-selected={option.value === value}
							aria-label={option.label}
							onClick={() => { onChange(option.value); setOpen(false); }}
							className="flex w-full items-center gap-2 rounded-md px-2 py-2 text-left text-[13px] text-foreground outline-none hover:bg-accent focus-visible:bg-accent"
						>
							{option.private ? <Lock className="lucide-lock size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" /> : <span className="w-3.5 shrink-0" />}
							<span className="min-w-0 flex-1">
								<span className="block truncate">{option.label}</span>
								{option.description ? <span className="block truncate text-[11px] text-muted-foreground">{option.description}</span> : null}
							</span>
							{option.value === value ? <Check className="size-3.5 shrink-0 text-primary" aria-hidden="true" /> : null}
						</button>
					))}
				</div>
			</PopoverContent>
		</Popover>
	);
}
