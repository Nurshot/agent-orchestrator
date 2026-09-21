package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

type processSnapshot struct {
	Processes []struct {
		PID       int32  `json:"pid"`
		RSSBytes  uint64 `json:"rssBytes"`
		Group     string `json:"group"`
		SessionID string `json:"sessionId,omitempty"`
	} `json:"processes"`
	TotalRSSBytes uint64 `json:"totalRssBytes"`
}

func newPSCommand(ctx *commandContext) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{Use: "ps", Short: "Show AO-owned processes and memory use", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var snapshot processSnapshot
		if err := ctx.getJSON(cmd.Context(), "system/processes", &snapshot); err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(cmd.OutOrStdout(), snapshot)
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "GROUP\tSESSION\tPID\tRSS")
		for _, p := range snapshot.Processes {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", p.Group, p.SessionID, p.PID, formatBytes(p.RSSBytes))
		}
		_, _ = fmt.Fprintf(w, "total\t\t\t%s\n", formatBytes(snapshot.TotalRSSBytes))
		return w.Flush()
	}}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output process snapshot as JSON")
	return cmd
}

func formatBytes(n uint64) string {
	const mib = 1024 * 1024
	if n < mib {
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MiB", float64(n)/mib)
}
