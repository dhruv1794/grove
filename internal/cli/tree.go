package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"grove/internal/grove"
)

func newTreeCmd() *cobra.Command {
	var depth int
	cmd := &cobra.Command{
		Use:   "tree <doc-id|path>",
		Short: "Show where a document sits in the knowledge forest",
		Long: "Tree resolves a document by ID or source path and renders the " +
			"tree-of-contents it belongs to as ASCII, marking the document's node.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			g, err := openGrove(ctx)
			if err != nil {
				return err
			}
			defer g.Close()

			views, err := g.Tree(ctx, args[0], depth)
			if err != nil {
				return err
			}
			if gflags.JSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(views)
			}
			renderTrees(cmd.OutOrStdout(), views)
			return nil
		},
	}
	cmd.Flags().IntVar(&depth, "depth", 0, "limit rendered tree depth (0 = full tree)")
	return cmd
}

func renderTrees(out io.Writer, views []grove.TreeView) {
	for i, v := range views {
		if i > 0 {
			fmt.Fprintln(out)
		}
		title := v.DocTitle
		if title == "" {
			title = v.SourceRef
		}
		fmt.Fprintf(out, "%s — %s/%s\n", title, v.Source, v.SourceRef)
		fmt.Fprintf(out, "tree: %s\n\n", v.Tree)
		renderTreeNode(out, v.Root, "", true, true)
	}
}

// renderTreeNode prints one node and recurses into its children. prefix is
// the accumulated indent for descendant lines; isLast marks the final child
// of its parent; isRoot suppresses the branch connector for the tree root.
func renderTreeNode(out io.Writer, n *grove.TreeNode, prefix string, isLast, isRoot bool) {
	marker := ""
	if n.IsTarget {
		marker = "  ◀"
	}
	if isRoot {
		fmt.Fprintf(out, "%s%s\n", n.Title, marker)
	} else {
		connector := "├── "
		if isLast {
			connector = "└── "
		}
		fmt.Fprintf(out, "%s%s%s%s\n", prefix, connector, n.Title, marker)
	}
	childPrefix := prefix
	if !isRoot {
		if isLast {
			childPrefix += "    "
		} else {
			childPrefix += "│   "
		}
	}
	for i, c := range n.Children {
		renderTreeNode(out, c, childPrefix, i == len(n.Children)-1, false)
	}
}
