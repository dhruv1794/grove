package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"grove/internal/core"
)

type sourceStatus struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	DocCount   int    `json:"doc_count"`
	LastSyncAt int64  `json:"last_sync_at,omitempty"`
}

type statusReport struct {
	Workspace string         `json:"workspace"`
	Config    core.Config    `json:"config"`
	Sources   []sourceStatus `json:"sources"`
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show workspace status",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			s, layout, err := openStore(ctx)
			if err != nil {
				return err
			}
			defer s.Close()

			sources, err := s.ListSources(ctx)
			if err != nil {
				return err
			}
			counts, err := s.CountDocumentsBySource(ctx)
			if err != nil {
				return err
			}
			cfg, err := core.LoadConfigFile(configPath(layout))
			if err != nil {
				return err
			}
			report := statusReport{Workspace: layout.Root, Config: cfg}
			for _, src := range sources {
				ss := sourceStatus{Name: src.Name, Type: string(src.Type), DocCount: counts[src.Name]}
				if !src.LastSyncAt.IsZero() {
					ss.LastSyncAt = src.LastSyncAt.Unix()
				}
				report.Sources = append(report.Sources, ss)
			}

			if gflags.JSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
			}
			return renderStatusText(cmd, report, layout)
		},
	}
}

func renderStatusText(cmd *cobra.Command, r statusReport, layout core.Layout) error {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "workspace:   %s\n", layout.Root)
	fmt.Fprintf(out, "build model: %s\n", modelOrUnset(r.Config.Build.Model))
	fmt.Fprintf(out, "query model: %s\n", modelOrUnset(r.Config.Query.Model))
	if len(r.Sources) == 0 {
		fmt.Fprintln(out, "sources:   (none — run `grove connect <type> <args>`)")
		return nil
	}
	fmt.Fprintln(out, "sources:")
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  NAME\tTYPE\tDOCS\tLAST SYNC")
	for _, s := range r.Sources {
		last := "(never)"
		if s.LastSyncAt > 0 {
			last = fmt.Sprintf("%d", s.LastSyncAt)
		}
		fmt.Fprintf(tw, "  %s\t%s\t%d\t%s\n", s.Name, s.Type, s.DocCount, last)
	}
	return tw.Flush()
}

func modelOrUnset(m string) string {
	if m == "" {
		return "(not set)"
	}
	return m
}
