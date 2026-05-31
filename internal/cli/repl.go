package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"grove/internal/grove"
)

// replState is the mutable session config a REPL question is asked with.
type replState struct {
	mode         string
	source       string // "" = all sources/trees
	model        string // query model; "" = resolve from config
	indexModel   string // build/index model for a future /build; "" = config
	retrieveOnly bool
}

func newReplCmd() *cobra.Command {
	st := replState{mode: "balanced"}
	var plain bool

	cmd := &cobra.Command{
		Use:   "repl",
		Short: "Interactive question session over the forest",
		Long: "Opens an interactive session so you can ask questions without re-running " +
			"`grove ask` each time. Pick a source, switch modes/model between questions " +
			"with : commands, and get cited answers inline. `:help` lists commands.\n\n" +
			"On a terminal this launches a full-screen TUI; --plain (or a piped stdin) " +
			"uses a simple line-based prompt instead.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			g, err := openGrove(ctx)
			if err != nil {
				return err
			}
			// The runners own g's lifecycle (the TUI may swap it via /workspace).
			sources, err := g.ListSources(ctx)
			if err != nil {
				return err
			}
			names := make([]string, len(sources))
			for i, s := range sources {
				names[i] = s.Name
			}
			sort.Strings(names)

			// Full-screen TUI on an interactive terminal; line-reader otherwise
			// (pipes, --plain) so the REPL stays scriptable and testable.
			if !plain && isTerminal(os.Stdin) && isTerminal(os.Stdout) {
				return runTUI(ctx, g, &st, names)
			}
			return runRepl(cmd, g, &st, names)
		},
	}
	cmd.Flags().StringVar(&st.model, "model", "", "query model as provider/name (default: config [query].model)")
	cmd.Flags().StringVar(&st.mode, "mode", "balanced", "initial retrieval mode: fast, balanced, quality, deep")
	cmd.Flags().StringVar(&st.source, "source", "", "initial source restriction (default: all)")
	cmd.Flags().BoolVar(&plain, "plain", false, "use the line-based prompt instead of the full-screen TUI")
	return cmd
}

func runRepl(cmd *cobra.Command, g *grove.Grove, st *replState, sources []string) error {
	ctx := context.Background()
	out := cmd.OutOrStdout()
	defer func() { g.Close() }() // closes whatever g points to after a /workspace swap

	fmt.Fprintf(out, "grove repl — %d source(s): %s\n", len(sources), strings.Join(sources, ", "))
	if len(sources) > 1 && st.source == "" {
		fmt.Fprintln(out, "querying all sources; `/source <name>` to focus one.")
	}
	fmt.Fprintln(out, "type a question, or `/help` for commands, `/quit` to exit.")

	scanner := bufio.NewScanner(cmd.InOrStdin())
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // allow long questions
	for {
		fmt.Fprint(out, replPrompt(st))
		if !scanner.Scan() {
			fmt.Fprintln(out)
			return scanner.Err()
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if path, ok := parseWorkspaceSwitch(line); ok {
			ng, names, err := switchWorkspace(ctx, path)
			if err != nil {
				fmt.Fprintf(out, "workspace: %v\n", err)
				continue
			}
			g.Close()
			g, sources = ng, names
			st.source = "" // a focused source likely won't exist in the new forest
			fmt.Fprintf(out, "workspace: %s (%d source(s): %s)\n", path, len(names), strings.Join(names, ", "))
			continue
		}
		if a, ok, perr := isSyncCmd(line); ok {
			if perr != nil {
				fmt.Fprintf(out, "sync: %v\n", perr)
				continue
			}
			fmt.Fprintf(out, "syncing %s …\n", orAll(a.Source))
			onProg, finishProg := newBuildProgress(false)
			res, err := runSync(ctx, g, a, onProg)
			finishProg()
			if err != nil {
				fmt.Fprintf(out, "sync failed: %v\n", err)
			} else {
				renderSyncResult(out, res)
			}
			continue
		}
		if a, ok, perr := isEmbedCmd(line); ok {
			if perr != nil {
				fmt.Fprintf(out, "embed: %v\n", perr)
				continue
			}
			onProg, finishProg := newEmbedProgress()
			res, err := runEmbed(ctx, g, a, onProg)
			finishProg()
			if err != nil {
				fmt.Fprintf(out, "embed failed: %v\n", err)
			} else {
				renderEmbedResult(out, res)
			}
			continue
		}
		if a, ok, perr := isTreeCmd(line); ok {
			if perr != nil {
				fmt.Fprintf(out, "tree: %v\n", perr)
				continue
			}
			views, err := g.Tree(ctx, a.Ref, a.Depth)
			if err != nil {
				fmt.Fprintf(out, "tree: %v\n", err)
			} else {
				renderTrees(out, views)
			}
			continue
		}
		if isStatusCmd(line) {
			st, err := g.Status(ctx)
			if err != nil {
				fmt.Fprintf(out, "status: %v\n", err)
			} else {
				renderStatusBrief(out, st)
			}
			continue
		}
		if name, ok, perr := isDisconnectCmd(line); ok {
			if perr != nil {
				fmt.Fprintf(out, "disconnect: %v\n", perr)
				continue
			}
			res, err := g.Disconnect(ctx, name)
			if err != nil {
				fmt.Fprintf(out, "disconnect: %v\n", err)
			} else {
				fmt.Fprintf(out, "disconnected %q (%d docs removed)\n", res.Name, res.DocsRemoved)
				// Refresh the cached source list.
				if srcs, err := g.ListSources(ctx); err == nil {
					sources = sources[:0]
					for _, s := range srcs {
						sources = append(sources, s.Name)
					}
				}
			}
			continue
		}
		if srcType, ok := isSetupCmd(line); ok {
			if srcType == "" {
				fmt.Fprintln(out, "usage: /setup <gdrive|confluence>")
				continue
			}
			status, err := g.SetupSource(srcType)
			if err != nil {
				fmt.Fprintf(out, "setup: %v\n", err)
				continue
			}
			renderSetupStatus(out, status)
			// Open the global config file in $EDITOR so the user can fill in
			// client_id / client_secret right now (mirrors /config open).
			if c := configEditorCommand(status.Path); c != nil {
				c.Stdin, c.Stdout, c.Stderr = os.Stdin, out, os.Stderr
				if err := c.Run(); err != nil {
					fmt.Fprintf(out, "open editor: %v\n", err)
				}
				// Re-read so the printed status reflects the saved file.
				if refreshed, err := g.SetupSource(srcType); err == nil {
					fmt.Fprintln(out, "—— saved ——")
					renderSetupStatus(out, refreshed)
				}
			} else {
				fmt.Fprintf(out, "no editor: set $EDITOR or edit %s manually\n", status.Path)
			}
			continue
		}
		if args, ok, perr := isConnectCmd(line); ok {
			if perr != nil {
				fmt.Fprintf(out, "connect: %v\n", perr)
				continue
			}
			fmt.Fprintf(out, "connecting %s …\n", args.Type)
			onProgress, finishProgress := newConnectProgress()
			res, err := runConnect(ctx, g, args, onProgress)
			finishProgress()
			if err != nil {
				fmt.Fprintf(out, "connect failed: %v\n", err)
			} else {
				renderConnectResult(out, args.Type, res)
				// Refresh the cached source list so /sources, /source picker etc see the new one.
				if srcs, err := g.ListSources(ctx); err == nil {
					sources = sources[:0]
					for _, s := range srcs {
						sources = append(sources, s.Name)
					}
				}
			}
			continue
		}
		if src, ok := isBuildCmd(line); ok {
			fmt.Fprintf(out, "building %s · index model %s …\n", orAll(src), orDefault(st.indexModel))
			res, err := g.Build(ctx, grove.BuildOpts{Model: st.indexModel, Source: src, Concurrency: 4})
			if err != nil {
				fmt.Fprintf(out, "build failed: %v\n", err)
			} else {
				renderBuildResult(cmd, res, false)
			}
			continue
		}
		if sub, ok := isConfigCmd(line); ok {
			if sub == "open" || sub == "edit" {
				if c := configEditorCommand(g.ConfigPath()); c != nil {
					c.Stdin, c.Stdout, c.Stderr = os.Stdin, out, os.Stderr
					if err := c.Run(); err != nil {
						fmt.Fprintf(out, "config open: %v\n", err)
					}
				} else {
					fmt.Fprintf(out, "no editor: set $EDITOR or open %s manually\n", g.ConfigPath())
				}
			} else {
				fmt.Fprintln(out, configReport(g))
			}
			continue
		}
		if isReplCommand(line) {
			reply, quit := handleReplCommand(st, sources, line)
			if reply != "" {
				fmt.Fprint(out, reply)
			}
			if quit {
				return nil
			}
			if err := persistModelChange(g, line); err != nil {
				fmt.Fprintf(out, "warning: could not save model to config: %v\n", err)
			}
			continue
		}
		askInRepl(ctx, cmd, g, st, line)
	}
}

func replPrompt(st *replState) string {
	src := st.source
	if src == "" {
		src = "all"
	}
	return fmt.Sprintf("grove(%s·%s)> ", st.mode, src)
}

// isReplCommand reports whether a line is a `/` (or legacy `:`) command.
func isReplCommand(line string) bool {
	return strings.HasPrefix(line, "/") || strings.HasPrefix(line, ":")
}

// replCommand describes one `/` command for the help text and the TUI palette.
type replCommand struct {
	name string // canonical, with leading slash, e.g. "/mode"
	args string // argument hint, e.g. "<fast|balanced|quality|deep>"
	desc string
}

var replCommands = []replCommand{
	{"/mode", "<fast|balanced|quality|deep>", "set retrieval mode"},
	{"/source", "<name|all>", "focus one source/tree, or all"},
	{"/query-model", "<provider/name>", "set query model (no arg: config default)"},
	{"/index-model", "<provider/name>", "set build/index model for /build"},
	{"/workspace", "<path>", "switch to a different forest on disk"},
	{"/build", "[source]", "(re)index the forest with the index model"},
	{"/connect", "<local|obsidian|gdrive|confluence> [args]", "add a new source to this forest"},
	{"/setup", "<gdrive|confluence>", "prep credentials in the global config file + open in $EDITOR"},
	{"/sync", "[source] [--force]", "reconcile sources + rebuild only what changed"},
	{"/embed", "[--source X] [--chunks]", "compute semantic embeddings for the docs"},
	{"/tree", "<doc-id|path> [--depth N]", "show where a document sits in the forest"},
	{"/status", "", "workspace + sources summary"},
	{"/disconnect", "<source>", "remove a source and all its indexed documents"},
	{"/config", "[open]", "show config + which keys are set; 'open' edits it"},
	{"/sources", "", "list connected sources"},
	{"/retrieve-only", "", "toggle answer synthesis on/off"},
	{"/help", "", "show this list"},
	{"/quit", "", "exit the session"},
}

// isBuildCmd reports whether line is a /build command, and the optional source
// argument. Like /workspace, building reaches the Grove handle, so adapters
// handle it rather than the pure handleReplCommand.
func isBuildCmd(line string) (source string, ok bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", false
	}
	if c := "/" + strings.TrimLeft(fields[0], "/:"); c == "/build" || c == "/reindex" {
		return strings.Join(fields[1:], " "), true
	}
	return "", false
}

// persistModelChange writes a /query-model or /index-model selection to the
// workspace config.toml so it survives across sessions. A no-arg form (which
// only clears the session override back to the config default) and any other
// command are no-ops.
func persistModelChange(g *grove.Grove, line string) error {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil
	}
	cmd := "/" + strings.TrimLeft(fields[0], "/:")
	arg := strings.Join(fields[1:], " ")
	if arg == "" {
		return nil
	}
	switch cmd {
	case "/model", "/query-model":
		return g.SetQueryModel(arg)
	case "/index-model", "/build-model":
		return g.SetBuildModel(arg)
	}
	return nil
}

// parseWorkspaceSwitch returns the target path when line is a /workspace
// command. Switching forests reopens the Grove handle, so it's handled by the
// adapters (which own that lifecycle), not by the pure handleReplCommand.
func parseWorkspaceSwitch(line string) (path string, ok bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", false
	}
	if c := "/" + strings.TrimLeft(fields[0], "/:"); c == "/workspace" || c == "/forest" {
		return strings.Join(fields[1:], " "), true
	}
	return "", false
}

// commandOptions returns the enumerable argument values for a command, or nil
// when its argument is free-text (e.g. /workspace) or it takes none. The TUI
// opens a second-level picker when this is non-empty — like Claude's /model
// submenu. sources/models are the session's known trees and installed models.
func commandOptions(name string, sources, models []string) []string {
	switch name {
	case "/mode":
		return []string{"fast", "balanced", "quality", "deep"}
	case "/source", "/tree":
		return append([]string{"all"}, sources...)
	case "/build", "/reindex":
		return sources // pick a tree to (re)build; bare /build still does all
	case "/model", "/query-model", "/index-model", "/build-model":
		return models
	}
	return nil
}

// matchCommands returns the commands whose name has the given prefix. A bare
// "/" matches everything. Used by the TUI command palette.
func matchCommands(prefix string) []replCommand {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	var out []replCommand
	for _, c := range replCommands {
		if strings.HasPrefix(c.name, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// handleReplCommand runs a `/` command (legacy `:` accepted), mutating st. It
// returns a reply to show the user (may be empty) and quit=true when the
// session should end. Shared by the line-reader REPL and the TUI.
func handleReplCommand(st *replState, sources []string, line string) (reply string, quit bool) {
	fields := strings.Fields(line)
	cmd := "/" + strings.TrimLeft(fields[0], "/:") // normalize :foo and /foo → /foo
	arg := ""
	if len(fields) > 1 {
		arg = strings.Join(fields[1:], " ")
	}
	switch cmd {
	case "/quit", "/exit", "/q":
		return "", true
	case "/help", "/h":
		return replHelp, false
	case "/mode":
		if arg == "" {
			return fmt.Sprintf("mode: %s\n", st.mode), false
		}
		if _, err := grove.StagesForMode(arg); err != nil {
			return fmt.Sprintf("unknown mode %q (fast, balanced, quality, deep)\n", arg), false
		}
		st.mode = arg
	case "/source", "/tree":
		switch {
		case arg == "":
			return fmt.Sprintf("source: %s\n", orAll(st.source)), false
		case arg == "all":
			st.source = ""
		case slices.Contains(sources, arg):
			st.source = arg
		default:
			return fmt.Sprintf("unknown source %q (%s)\n", arg, strings.Join(sources, ", ")), false
		}
	case "/model", "/query-model":
		st.model = arg // empty clears back to config default
		return fmt.Sprintf("query model: %s\n", orDefault(st.model)), false
	case "/index-model", "/build-model":
		st.indexModel = arg
		return fmt.Sprintf("index model: %s (used by /build)\n", orDefault(st.indexModel)), false
	case "/sources", "/trees":
		return fmt.Sprintf("sources: %s\n", strings.Join(sources, ", ")), false
	case "/retrieve-only":
		st.retrieveOnly = !st.retrieveOnly
		return fmt.Sprintf("retrieve-only: %v\n", st.retrieveOnly), false
	default:
		return fmt.Sprintf("unknown command %q — `/help` for the list\n", cmd), false
	}
	return "", false
}

func askInRepl(ctx context.Context, cmd *cobra.Command, g *grove.Grove, st *replState, q string) {
	out := cmd.OutOrStdout()
	stg, err := grove.StagesForMode(st.mode)
	if err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return
	}
	res, err := g.Ask(ctx, grove.AskOpts{
		Query:        q,
		Model:        st.model,
		Source:       st.source,
		RetrieveOnly: st.retrieveOnly,
		Fast:         stg.Fast,
		Decompose:    stg.Decompose,
		Rerank:       stg.Rerank,
	})
	if err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return
	}
	renderAskResult(cmd, res, st.retrieveOnly)
}

// switchWorkspace opens a forest at path and returns its handle and sorted
// source names. The caller closes the previous handle on success. Shared by the
// line-reader REPL and the TUI.
func switchWorkspace(ctx context.Context, path string) (*grove.Grove, []string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil, fmt.Errorf("usage: /workspace <path>")
	}
	ng, err := openGroveAt(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	srcs, err := ng.ListSources(ctx)
	if err != nil {
		ng.Close()
		return nil, nil, err
	}
	names := make([]string, len(srcs))
	for i, s := range srcs {
		names[i] = s.Name
	}
	sort.Strings(names)
	return ng, names, nil
}

func orAll(s string) string {
	if s == "" {
		return "all"
	}
	return s
}

func orDefault(s string) string {
	if s == "" {
		return "(config default)"
	}
	return s
}

const replHelp = `commands:
  /mode <fast|balanced|quality|deep>   set retrieval mode (no arg: show current)
  /source <name|all>                   focus one source, or all (alias /tree)
  /query-model <provider/name>         set query model (no arg: clear to config)
  /index-model <provider/name>         set build/index model for /build
  /workspace <path>                    switch to a different forest (alias /forest)
  /build [source]                      (re)index the forest with the index model
  /config [open]                       show config + keys set; 'open' edits the file
  /sources                             list sources
  /retrieve-only                       toggle synthesis on/off
  /help                                this list
  /quit                                exit (also Ctrl-D)
anything else is asked as a question.
`
