package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"grove/internal/core"
	"grove/internal/grove"
	"grove/internal/llm"
)

// grove's forest palette. The vars default to the dark-terminal tuning;
// applyTheme rebinds them to a light palette when the terminal background is
// light (opencode-style), so a single detection at startup flips the whole TUI.
var (
	colLeaf  = lipgloss.Color("#7CB342") // accent green
	colSky   = lipgloss.Color("#4FC3F7") // cyan
	colSun   = lipgloss.Color("#FFB74D") // amber
	colPeach = lipgloss.Color("#FFAB70") // palette selection
	colMuted = lipgloss.Color("#6E7681") // dim gray
	colFaint = lipgloss.Color("#484F58") // fainter gray
	colRed   = lipgloss.Color("#E57373")
	colInk   = lipgloss.Color("#0D1117") // near-black, for text on a light bg
	colPaper = lipgloss.Color("#E6EDF3") // body text

	colBg      = lipgloss.Color("#0F140F") // app background — painted across the whole screen
	colBgInput = lipgloss.Color("#070907") // input area fill — darkest (near-black green)
	colBgUser  = lipgloss.Color("#2A2014") // user question panel — bark brown
	colBgGrove = lipgloss.Color("#16271A") // grove answer panel — forest green
	colBgSide  = lipgloss.Color("#171F14") // sidebar panel — dim moss (just above app bg)
)

// applyTheme rebinds the palette for the detected terminal background. Dark is
// the default tuning (no-op); light swaps to near-white surfaces with dark text
// and accents darkened enough to stay legible on a light background.
func applyTheme(dark bool) {
	if dark {
		return // vars already hold the dark palette
	}
	colLeaf = lipgloss.Color("#4F8A2E")  // accent green, darkened for white bg
	colSky = lipgloss.Color("#1D6FD6")   // blue
	colSun = lipgloss.Color("#B45309")   // amber/orange
	colPeach = lipgloss.Color("#FFAB70") // selection fill — pairs with colInk text
	colMuted = lipgloss.Color("#6B7280") // dim gray, legible on white
	colFaint = lipgloss.Color("#9CA3AF") // fainter gray
	colRed = lipgloss.Color("#DC2626")
	colInk = lipgloss.Color("#0D1117")   // dark text on the peach selection
	colPaper = lipgloss.Color("#1A1A1A") // body text — now dark

	colBg = lipgloss.Color("#FAFAFA")      // app background — near white
	colBgInput = lipgloss.Color("#EFEFEF") // input area fill
	colBgUser = lipgloss.Color("#F2F2F2")  // user question panel — light gray
	colBgGrove = lipgloss.Color("#FFFFFF") // grove answer panel — white
	colBgSide = lipgloss.Color("#F2F2F2")  // sidebar panel
}

type tuiStyles struct {
	logo                  lipgloss.Style
	vpBox, inputBox       lipgloss.Style
	palBox, palSel        lipgloss.Style
	palName, palArg       lipgloss.Style
	footer, footerKey     lipgloss.Style
	stKey, stVal          lipgloss.Style
	prompt, placeholder   lipgloss.Style
	qbar, question        lipgloss.Style
	userBlock, groveBlock lipgloss.Style
	answer, meta, metaDot lipgloss.Style
	cite, citePath        lipgloss.Style
	side, sideTitle       lipgloss.Style
	sideKey, sideVal      lipgloss.Style
	sideOn, sideOff       lipgloss.Style
	dim, errln, status    lipgloss.Style
}

func newTUIStyles() tuiStyles {
	return tuiStyles{
		logo:        lipgloss.NewStyle().Bold(true).Foreground(colLeaf).Background(colBg),
		vpBox:       lipgloss.NewStyle().Background(colBg).Padding(0, 1),
		inputBox:    lipgloss.NewStyle().Border(lipgloss.ThickBorder(), false, false, false, true).BorderForeground(colLeaf).BorderBackground(colBg).Background(colBgInput).Padding(1, 2),
		palBox:      lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colFaint).BorderBackground(colBg).Background(colBg).Padding(0, 1),
		palSel:      lipgloss.NewStyle().Background(colPeach).Foreground(colInk),
		palName:     lipgloss.NewStyle().Foreground(colPaper).Background(colBg),
		palArg:      lipgloss.NewStyle().Foreground(colMuted).Background(colBg),
		footer:      lipgloss.NewStyle().Foreground(colMuted).Background(colBg),
		footerKey:   lipgloss.NewStyle().Foreground(colSky).Background(colBg).Bold(true),
		stKey:       lipgloss.NewStyle().Foreground(colFaint).Background(colBg),
		stVal:       lipgloss.NewStyle().Foreground(colPaper).Background(colBg),
		prompt:      lipgloss.NewStyle().Bold(true).Foreground(colLeaf).Background(colBgInput),
		placeholder: lipgloss.NewStyle().Foreground(colFaint),
		qbar:        lipgloss.NewStyle().Foreground(colSun).Background(colBg),
		question:    lipgloss.NewStyle().Bold(true).Foreground(colSun),
		userBlock:   lipgloss.NewStyle().Border(lipgloss.ThickBorder(), false, false, false, true).BorderForeground(colLeaf).BorderBackground(colBg).Background(colBgUser).Foreground(colPaper).Padding(1, 2),
		groveBlock:  lipgloss.NewStyle().Border(lipgloss.ThickBorder(), false, false, false, true).BorderForeground(colSky).BorderBackground(colBg).Background(colBgGrove).Foreground(colPaper).Padding(1, 2),
		answer:      lipgloss.NewStyle().Foreground(colPaper).Background(colBg),
		meta:        lipgloss.NewStyle().Foreground(colMuted).Background(colBg),
		metaDot:     lipgloss.NewStyle().Foreground(colLeaf).Background(colBg),
		cite:        lipgloss.NewStyle().Bold(true).Foreground(colSky).Background(colBg),
		citePath:    lipgloss.NewStyle().Foreground(colMuted).Background(colBg).Underline(true),
		side:        lipgloss.NewStyle().Background(colBgSide).Padding(0, 2),
		sideTitle:   lipgloss.NewStyle().Bold(true).Foreground(colLeaf).Background(colBgSide),
		sideKey:     lipgloss.NewStyle().Foreground(colMuted).Background(colBgSide),
		sideVal:     lipgloss.NewStyle().Foreground(colPaper).Background(colBgSide),
		sideOn:      lipgloss.NewStyle().Foreground(colLeaf).Background(colBgSide),
		sideOff:     lipgloss.NewStyle().Foreground(colFaint).Background(colBgSide),
		dim:         lipgloss.NewStyle().Foreground(colMuted).Background(colBg),
		errln:       lipgloss.NewStyle().Foreground(colRed).Background(colBg),
		status:      lipgloss.NewStyle().Foreground(colSun).Background(colBg),
	}
}

// forest-flavored words shown while a query is being answered.
var thinkingWords = []string{
	"Foraging", "Rooting around", "Tracing roots", "Sifting leaves",
	"Wandering the grove", "Following trails", "Gathering", "Branching out",
	"Reading the rings", "Combing the canopy", "Rustling", "Sprouting",
	"Germinating", "Photosynthesizing", "Pollinating", "Budding", "Blooming",
	"Leafing through", "Pruning", "Grafting", "Composting", "Mulching",
	"Digging", "Burrowing", "Unearthing", "Excavating", "Sniffing out",
	"Tracking", "Trailing", "Stalking", "Scouting", "Surveying", "Mapping",
	"Charting", "Navigating", "Roaming", "Rambling", "Meandering", "Trekking",
	"Wading in", "Climbing", "Scaling", "Ascending", "Descending", "Spelunking",
	"Rummaging", "Ransacking", "Scavenging", "Hunting", "Tracking down",
	"Chasing leads", "Connecting dots", "Weaving threads", "Untangling",
	"Unraveling", "Knitting", "Stitching", "Threading", "Splicing",
	"Cross-referencing", "Triangulating", "Corroborating", "Cross-checking",
	"Fact-finding", "Sleuthing", "Investigating", "Probing", "Excavating leads",
	"Distilling", "Steeping", "Brewing", "Simmering", "Marinating",
	"Percolating", "Fermenting", "Crystallizing", "Synthesizing", "Coalescing",
	"Assembling", "Piecing together", "Mosaicking", "Collating", "Curating",
	"Harvesting", "Reaping", "Threshing", "Winnowing", "Panning", "Sieving",
	"Filtering", "Distilling sap", "Tapping", "Counting rings", "Dendro-reading",
	"Echolocating", "Divining", "Dowsing", "Scrying", "Pondering", "Ruminating",
	"Mulling", "Cogitating", "Deliberating", "Contemplating", "Reflecting",
	"Musing", "Noodling", "Chewing on it", "Turning it over", "Wondering",
	"Considering", "Weighing", "Sizing up", "Puzzling", "Untwisting roots",
	"Listening to the soil", "Whispering to trees", "Consulting the canopy",
}

// tips rotated in the splash + sidebar.
var groveTips = []string{
	"/config shows which model keys are set",
	"/workspace switches between forests",
	"fast mode is model-free and instant; deep walks the tree",
	"/mode quality reranks candidates — best on a strong model",
	"type / for commands, then ↑↓ to pick a value",
	"/build reindexes after your notes change",
	"/source focuses one tree; 'all' searches the whole forest",
	"⌘-click a cited path to open it (ctrl-click on Linux)",
}

func pick(ss []string) string { return ss[rand.Intn(len(ss))] }

// groveBanner is the block-letter wordmark shown big on the splash; it "scales
// down" to the small 🌳 grove in the status line once a conversation starts.
var groveBanner = []string{
	" ███  ████   ███  █   █ █████",
	"█     █   █ █   █ █   █ █    ",
	"█  ██ ████  █   █ █   █ ███  ",
	"█   █ █  █  █   █  █ █  █    ",
	" ███  █   █  ███    █   █████",
}

type askResultMsg struct {
	res *grove.AskResult
	err error
}

type buildProgressMsg grove.BuildProgress

type buildDoneMsg struct {
	res *grove.BuildResult
	err error
}

type editorClosedMsg struct{ err error }
type connectDoneMsg struct{ err error }

type tuiModel struct {
	ctx     context.Context
	g       *grove.Grove
	sources []string
	st      *replState

	input      textinput.Model
	vp         viewport.Model
	spin       spinner.Model
	prog       progress.Model
	started    bool // false until the first submit — drives the centered splash
	thinking   bool
	building   bool
	buildProg  map[string]grove.BuildProgress // per-source node progress
	buildCh    chan grove.BuildProgress
	shouldQuit bool
	body       string // accumulated transcript (string, not Builder — the model is copied by value each Update)
	ready      bool
	width      int
	height     int
	mainW      int // width of the left column (terminal minus sidebar)

	thinkWord string    // playful processing word for the current query
	askStart  time.Time // when the current query started, for elapsed
	tip       string    // current rotating tip

	// session usage, accumulated across queries for the sidebar.
	qCount       int
	totCalls     int
	totPrompt    int
	totCompl     int
	totUSD       float64
	totEstimated bool

	palette []replCommand // currently-matching commands ("/" palette); empty = hidden
	palIdx  int

	submenu    []string // second-level value picker (e.g. /mode → fast…); empty = hidden
	submenuCmd string   // the command the submenu is choosing a value for
	subIdx     int
	models     []string // installed models, for /model & /index-model pickers

	// resolved config snapshot for the sidebar (refreshed on start, /config
	// open, and /workspace switch). cfgQuery/cfgIndex include env overrides.
	cfgQuery, cfgIndex, cfgEmbed                        string
	provOllama, provOpenAI, provAnthropic, provDeepSeek bool

	styles tuiStyles
}

// fetchOllamaModels lists locally-installed Ollama models as provider-prefixed
// ids ("ollama/llama3.1:8b"), best-effort — the model pickers degrade to
// free-text entry if Ollama isn't reachable.
func fetchOllamaModels() []string {
	host := os.Getenv("GROVE_OLLAMA_HOST")
	if host == "" {
		host = "http://localhost:11434"
	}
	c := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := c.Get(strings.TrimRight(host, "/") + "/api/tags")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil
	}
	out := make([]string, 0, len(body.Models))
	for _, m := range body.Models {
		out = append(out, "ollama/"+m.Name)
	}
	sort.Strings(out)
	return out
}

func newTUIModel(ctx context.Context, g *grove.Grove, st *replState, sources []string) tuiModel {
	in := textinput.New()
	in.Placeholder = `ask anything, or / for commands`
	in.Prompt = ""
	in.Focus()
	// Carry the input-box fill behind the typed text/placeholder/cursor; without
	// these the textinput renders runes on the terminal default bg, which shows as
	// a mismatched block inside the box (visible especially in light terminals).
	in.TextStyle = lipgloss.NewStyle().Background(colBgInput).Foreground(colPaper)
	in.PlaceholderStyle = lipgloss.NewStyle().Background(colBgInput).Foreground(colFaint)
	in.Cursor.TextStyle = lipgloss.NewStyle().Background(colBgInput).Foreground(colPaper)

	sp := spinner.New()
	sp.Spinner = spinner.Points
	sp.Style = lipgloss.NewStyle().Foreground(colLeaf)

	pg := progress.New(progress.WithoutPercentage(), progress.WithSolidFill(string(colLeaf)))

	m := tuiModel{ctx: ctx, g: g, sources: sources, st: st, input: in, spin: sp, prog: pg, models: fetchOllamaModels(), tip: pick(groveTips), styles: newTUIStyles()}
	m.reloadConfig()
	m.appendLine(m.styles.dim.Render(fmt.Sprintf("Connected to %d source(s): %s.", len(sources), strings.Join(sources, ", "))))
	m.appendLine(m.styles.dim.Render("Ask a question, or type / for commands."))
	return m
}

// reloadConfig refreshes the resolved models and provider availability shown in
// the sidebar — called on start, after /config open, and after /workspace.
// cfgQuery/cfgIndex come from core.LoadConfigFile, which applies env overrides.
func (m *tuiModel) reloadConfig() {
	cfg, _ := core.LoadMergedConfig(m.g.ConfigPath())
	m.cfgQuery = cfg.Query.Model
	m.cfgIndex = cfg.Build.Model
	m.cfgEmbed = os.Getenv("GROVE_EMBED_MODEL")
	if m.cfgEmbed == "" {
		m.cfgEmbed = "ollama/bge-m3"
	}
	m.provOllama = true // local, always available
	m.provOpenAI = os.Getenv("GROVE_OPENAI_API_KEY") != ""
	m.provAnthropic = os.Getenv("GROVE_ANTHROPIC_API_KEY") != ""
	m.provDeepSeek = os.Getenv("GROVE_DEEPSEEK_API_KEY") != ""
}

func (m *tuiModel) appendLine(s string) { m.body += s + "\n" }

// fileLink wraps an absolute path in an OSC 8 terminal hyperlink so supporting
// terminals make it clickable (cmd/ctrl-click → open in the default app). label
// is the already-styled display text. Non-absolute locations (no file:// target
// to resolve) are returned unwrapped.
func fileLink(path, label string) string {
	if !strings.HasPrefix(path, "/") {
		return label
	}
	return "\x1b]8;;file://" + path + "\x1b\\" + label + "\x1b]8;;\x1b\\"
}

// echoCommand records a /command the user ran, styled consistently with the TUI
// (not the line-reader's grove(…)> prompt).
func (m *tuiModel) echoCommand(val string) {
	m.appendLine("")
	m.appendLine(m.styles.metaDot.Render("› ") + m.styles.dim.Render(val)) // leaf chevron on app bg
}

func (m tuiModel) Init() tea.Cmd { return textinput.Blink }

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()

	case tea.KeyMsg:
		return m.onKey(msg)

	case askResultMsg:
		m.thinking = false
		m.renderResult(msg)
		m.refreshViewport()

	case buildProgressMsg:
		m.buildProg[msg.Source] = grove.BuildProgress(msg)
		cmds = append(cmds, waitBuildProgress(m.buildCh)) // wait for the next one

	case buildDoneMsg:
		m.building = false
		m.renderBuildDone(msg)
		m.refreshViewport()

	case editorClosedMsg:
		m.reloadConfig() // pick up any model/key edits made in the editor
		if msg.err != nil {
			m.appendLine(m.styles.errln.Render("config open: " + msg.err.Error()))
		} else {
			m.appendLine(m.styles.dim.Render("config reloaded."))
		}
		m.refreshViewport()

	case connectDoneMsg:
		// Subprocess wrote to its own stdout/stderr while suspended; report
		// outcome here and refresh the cached source list for /sources etc.
		if msg.err != nil {
			m.appendLine(m.styles.errln.Render("connect: " + msg.err.Error()))
		} else {
			m.appendLine(m.styles.dim.Render("connect finished."))
			if srcs, err := m.g.ListSources(m.ctx); err == nil {
				m.sources = m.sources[:0]
				for _, s := range srcs {
					m.sources = append(m.sources, s.Name)
				}
			}
		}
		m.refreshViewport()

	case spinner.TickMsg:
		if m.thinking || m.building {
			var cmd tea.Cmd
			m.spin, cmd = m.spin.Update(msg)
			cmds = append(cmds, cmd)
		}
	}

	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

// mouseSeqRe matches SGR mouse-report sequences (e.g. "[<65;126;31M"). On some
// terminals wheel/motion events leak through as KeyRunes instead of being
// parsed as a MouseMsg; without this guard they'd be inserted into the input as
// literal text while scrolling. Legitimate scrolling arrives as tea.MouseMsg
// (handled by the viewport), so dropping these key messages is safe.
var mouseSeqRe = regexp.MustCompile(`\[<\d+;\d+;\d+[Mm]`)

func (m tuiModel) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyRunes && mouseSeqRe.MatchString(string(msg.Runes)) {
		return m, nil // swallow leaked mouse-report sequences
	}

	// Second-level value picker (e.g. /mode → fast|balanced|…).
	if len(m.submenu) > 0 {
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			m.closeSubmenu()
			return m, nil
		case "up", "ctrl+p":
			m.subIdx = (m.subIdx - 1 + len(m.submenu)) % len(m.submenu)
			return m, nil
		case "down", "ctrl+n":
			m.subIdx = (m.subIdx + 1) % len(m.submenu)
			return m, nil
		case "enter", "tab":
			val := m.submenu[m.subIdx]
			cmdName := m.submenuCmd
			m.closeSubmenu()
			m.input.SetValue(cmdName + " " + val)
			cmd := m.submit()
			if m.shouldQuit {
				return m, tea.Quit
			}
			return m, cmd
		}
		return m, nil // swallow other keys while the picker is open
	}

	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		if len(m.palette) > 0 { // close palette, keep typing
			m.input.Reset()
			m.syncPalette()
			return m, nil
		}
		return m, tea.Quit
	case "up", "ctrl+p":
		if len(m.palette) > 0 {
			m.palIdx = (m.palIdx - 1 + len(m.palette)) % len(m.palette)
			return m, nil
		}
	case "down", "ctrl+n":
		if len(m.palette) > 0 {
			m.palIdx = (m.palIdx + 1) % len(m.palette)
			return m, nil
		}
	case "tab":
		if len(m.palette) > 0 {
			sel := m.palette[m.palIdx]
			if !m.openSubmenuIfAny(sel.name) {
				m.complete()
			}
			return m, nil
		}
	case "enter":
		if len(m.palette) > 0 {
			sel := m.palette[m.palIdx]
			if m.openSubmenuIfAny(sel.name) {
				return m, nil // picker opened; don't submit
			}
			if sel.args == "" { // no-arg command: run it now
				m.input.SetValue(sel.name)
			} else { // free-text arg (e.g. /workspace): fill and wait
				m.complete()
				return m, nil
			}
		}
		cmd := m.submit()
		if m.shouldQuit {
			return m, tea.Quit
		}
		return m, cmd
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	// Mouse-report sequences can arrive split across several key messages, so
	// the per-message guard above misses them; they reassemble in the input
	// value. Strip any complete sequence that has accumulated here.
	if v := m.input.Value(); mouseSeqRe.MatchString(v) {
		m.input.SetValue(mouseSeqRe.ReplaceAllString(v, ""))
		m.input.CursorEnd()
	}
	m.syncPalette()
	return m, cmd
}

// openSubmenuIfAny opens the value picker for a command with enumerable options
// (returns true), or reports false when the argument is free-text or absent.
func (m *tuiModel) openSubmenuIfAny(name string) bool {
	opts := commandOptions(name, m.sources, m.models)
	if len(opts) == 0 {
		return false
	}
	m.submenu = opts
	m.submenuCmd = name
	m.subIdx = 0
	m.palette = nil
	m.resize()
	return true
}

func (m *tuiModel) closeSubmenu() {
	m.submenu = nil
	m.submenuCmd = ""
	m.subIdx = 0
	m.input.Reset()
	m.syncPalette()
}

// syncPalette recomputes the command palette from the current input. It shows
// while the input is a single "/"-prefixed token (no space yet) — once an
// argument is being typed, the command is chosen and the palette hides.
func (m *tuiModel) syncPalette() {
	val := m.input.Value()
	if strings.HasPrefix(val, "/") && !strings.Contains(val, " ") {
		m.palette = matchCommands(val)
	} else {
		m.palette = nil
	}
	if m.palIdx >= len(m.palette) {
		m.palIdx = 0
	}
	m.resize()
}

func (m *tuiModel) complete() {
	sel := m.palette[m.palIdx]
	v := sel.name
	if sel.args != "" {
		v += " "
	}
	m.input.SetValue(v)
	m.input.CursorEnd()
	m.syncPalette()
}

// resize sizes the viewport for the current terminal. The left column reserves
// rows for the input box (3), status line (1), footer (1), plus the open picker;
// width is the terminal minus the right sidebar.
func (m *tuiModel) resize() {
	if m.width == 0 {
		return
	}
	m.mainW = m.width - m.sidebarWidth()
	chrome := 6 // input box (3) + activity line (1) + status (1) + footer (1)
	if h := m.overlayHeight(); h > 0 {
		chrome += h
	}
	vpH := max(m.height-chrome, 3)
	vpW := m.mainW - 2 // vpBox horizontal padding
	if !m.ready {
		m.vp = viewport.New(vpW, vpH)
		m.vp.Style = lipgloss.NewStyle().Background(colBg) // paint the empty transcript area
		m.ready = true
	} else {
		m.vp.Width = vpW
		m.vp.Height = vpH
	}
	m.input.Width = m.mainW - 8
	if w := m.vp.Width / 2; w > 10 {
		m.prog.Width = w
	}
	m.refreshViewport()
}

// sidebarWidth is the right info-panel width, or 0 on a narrow terminal. It
// scales to ~a quarter of the terminal (opencode-style) within [36,48] so wide
// values like "ollama/nomic-embed-text" and "anthropic — no key" fit on one line.
func (m *tuiModel) sidebarWidth() int {
	if m.width < 90 {
		return 0
	}
	return min(max(m.width/4, 36), 48)
}

// overlayHeight is the rows consumed by whichever picker is open (command
// palette or value submenu), including its border. 0 when neither is shown.
func (m *tuiModel) overlayHeight() int {
	switch {
	case len(m.submenu) > 0:
		return len(m.submenu) + 1 + 2 // values + title row + border
	case len(m.palette) > 0:
		return len(m.palette) + 2 // rows + border
	}
	return 0
}

func (m *tuiModel) submit() tea.Cmd {
	val := strings.TrimSpace(m.input.Value())
	if val == "" {
		return nil
	}
	m.started = true // leave the centered splash for the normal layout
	m.input.Reset()
	m.palette = nil
	if path, ok := parseWorkspaceSwitch(val); ok {
		m.echoCommand(val)
		ng, names, err := switchWorkspace(m.ctx, path)
		if err != nil {
			m.appendLine(m.styles.errln.Render("workspace: " + err.Error()))
		} else {
			m.g.Close()
			m.g, m.sources = ng, names
			m.st.source = "" // a focused source likely won't exist in the new forest
			m.reloadConfig() // new forest may have its own config.toml
			m.appendLine(m.styles.status.Render(fmt.Sprintf("workspace: %s — %d source(s): %s", path, len(names), strings.Join(names, ", "))))
		}
		m.refreshViewport()
		return nil
	}
	if a, ok, perr := isSyncCmd(val); ok {
		m.echoCommand(val)
		if perr != nil {
			m.appendLine(m.styles.errln.Render("sync: " + perr.Error()))
			m.refreshViewport()
			return nil
		}
		c := subprocess(m.g.Layout().Root, m.g.ConfigPath(), syncCmdArgs(a)...)
		m.refreshViewport()
		return tea.ExecProcess(c, func(err error) tea.Msg { return editorClosedMsg{err} })
	}
	if a, ok, perr := isEmbedCmd(val); ok {
		m.echoCommand(val)
		if perr != nil {
			m.appendLine(m.styles.errln.Render("embed: " + perr.Error()))
			m.refreshViewport()
			return nil
		}
		c := subprocess(m.g.Layout().Root, m.g.ConfigPath(), embedCmdArgs(a)...)
		m.refreshViewport()
		return tea.ExecProcess(c, func(err error) tea.Msg { return editorClosedMsg{err} })
	}
	if a, ok, perr := isTreeCmd(val); ok {
		m.echoCommand(val)
		if perr != nil {
			m.appendLine(m.styles.errln.Render("tree: " + perr.Error()))
			m.refreshViewport()
			return nil
		}
		views, err := m.g.Tree(m.ctx, a.Ref, a.Depth)
		if err != nil {
			m.appendLine(m.styles.errln.Render("tree: " + err.Error()))
		} else {
			var sb strings.Builder
			renderTrees(&sb, views)
			m.appendLine(m.styles.status.Render(strings.TrimRight(sb.String(), "\n")))
		}
		m.refreshViewport()
		return nil
	}
	if isStatusCmd(val) {
		m.echoCommand(val)
		st, err := m.g.Status(m.ctx)
		if err != nil {
			m.appendLine(m.styles.errln.Render("status: " + err.Error()))
		} else {
			var sb strings.Builder
			renderStatusBrief(&sb, st)
			m.appendLine(m.styles.status.Render(strings.TrimRight(sb.String(), "\n")))
		}
		m.refreshViewport()
		return nil
	}
	if name, ok, perr := isDisconnectCmd(val); ok {
		m.echoCommand(val)
		if perr != nil {
			m.appendLine(m.styles.errln.Render("disconnect: " + perr.Error()))
			m.refreshViewport()
			return nil
		}
		res, err := m.g.Disconnect(m.ctx, name)
		if err != nil {
			m.appendLine(m.styles.errln.Render("disconnect: " + err.Error()))
		} else {
			m.appendLine(m.styles.status.Render(fmt.Sprintf("disconnected %q (%d docs removed)", res.Name, res.DocsRemoved)))
			if srcs, err := m.g.ListSources(m.ctx); err == nil {
				m.sources = m.sources[:0]
				for _, s := range srcs {
					m.sources = append(m.sources, s.Name)
				}
			}
		}
		m.refreshViewport()
		return nil
	}
	if srcType, ok := isSetupCmd(val); ok {
		m.echoCommand(val)
		if srcType == "" {
			m.appendLine(m.styles.status.Render("usage: /setup <gdrive|confluence>"))
			m.refreshViewport()
			return nil
		}
		status, err := m.g.SetupSource(srcType)
		if err != nil {
			m.appendLine(m.styles.errln.Render("setup: " + err.Error()))
			m.refreshViewport()
			return nil
		}
		var sb strings.Builder
		renderSetupStatus(&sb, status)
		m.appendLine(m.styles.status.Render(strings.TrimRight(sb.String(), "\n")))
		// Open the config in $EDITOR so creds can be filled in now.
		if c := configEditorCommand(status.Path); c != nil {
			m.refreshViewport()
			return tea.ExecProcess(c, func(err error) tea.Msg { return editorClosedMsg{err} })
		}
		m.appendLine(m.styles.errln.Render("no editor: set $EDITOR or edit " + status.Path + " manually"))
		m.refreshViewport()
		return nil
	}
	if args, ok, perr := isConnectCmd(val); ok {
		m.echoCommand(val)
		if perr != nil {
			m.appendLine(m.styles.errln.Render("connect: " + perr.Error()))
			m.refreshViewport()
			return nil
		}
		// Re-exec grove in a subprocess so the TUI suspends and the connect's
		// progress bar + OAuth browser flow get a clean TTY. SQLite (WAL) +
		// the file-based token store are both concurrency-safe.
		c := connectSubprocess(m.g.Layout().Root, m.g.ConfigPath(), args)
		m.refreshViewport()
		return tea.ExecProcess(c, func(err error) tea.Msg {
			return connectDoneMsg{err: err}
		})
	}
	if src, ok := isBuildCmd(val); ok {
		return m.startBuild(src)
	}
	if sub, ok := isConfigCmd(val); ok {
		m.echoCommand(val)
		if sub == "open" || sub == "edit" {
			c := configEditorCommand(m.g.ConfigPath())
			if c == nil {
				m.appendLine(m.styles.errln.Render("no editor: set $EDITOR or open " + m.g.ConfigPath()))
				m.refreshViewport()
				return nil
			}
			m.refreshViewport()
			return tea.ExecProcess(c, func(err error) tea.Msg { return editorClosedMsg{err} })
		}
		m.appendLine(m.styles.status.Render(configReport(m.g)))
		m.refreshViewport()
		return nil
	}
	if isReplCommand(val) {
		reply, quit := handleReplCommand(m.st, m.sources, val)
		if quit {
			m.shouldQuit = true
			return nil
		}
		m.echoCommand(val)
		if reply != "" {
			m.appendLine(m.styles.status.Render(strings.TrimRight(reply, "\n")))
		}
		if err := persistModelChange(m.g, val); err != nil {
			m.appendLine(m.styles.errln.Render("could not save model to config: " + err.Error()))
		} else {
			m.reloadConfig() // refresh the sidebar's resolved model from the saved config
		}
		m.refreshViewport()
		return nil
	}
	m.appendLine("")
	// Green left accent bar + vertically-padded text (opencode-style); the border
	// adds one column, so the content width is one less than the viewport.
	m.appendLine(m.styles.userBlock.Width(m.vp.Width - 1).Render(val))
	stg, err := grove.StagesForMode(m.st.mode)
	if err != nil {
		m.appendLine(m.styles.errln.Render("error: " + err.Error()))
		m.refreshViewport()
		return nil
	}
	m.thinking = true
	m.thinkWord = pick(thinkingWords)
	m.askStart = time.Now()
	m.refreshViewport()
	return tea.Batch(m.spin.Tick, askCmd(m.ctx, m.g, m.st, stg, val))
}

func askCmd(ctx context.Context, g *grove.Grove, st *replState, stg grove.Stages, q string) tea.Cmd {
	return func() tea.Msg {
		res, err := g.Ask(ctx, grove.AskOpts{
			Query:        q,
			Model:        st.model,
			Source:       st.source,
			RetrieveOnly: st.retrieveOnly,
			Fast:         stg.Fast,
			Decompose:    stg.Decompose,
			Rerank:       stg.Rerank,
		})
		return askResultMsg{res: res, err: err}
	}
}

// startBuild kicks off an async (re)index. Progress flows back over a channel
// drained by waitBuildProgress; the goroutine reports completion via buildDoneMsg.
func (m *tuiModel) startBuild(source string) tea.Cmd {
	if m.building {
		m.appendLine(m.styles.status.Render("a build is already running…"))
		m.refreshViewport()
		return nil
	}
	m.building = true
	m.buildProg = map[string]grove.BuildProgress{}
	m.buildCh = make(chan grove.BuildProgress, 256)
	scope := orAll(source)
	m.appendLine("")
	m.appendLine(m.styles.status.Render(fmt.Sprintf("building %s · index model %s …", scope, orDefault(m.st.indexModel))))
	m.refreshViewport()
	return tea.Batch(m.spin.Tick, buildCmd(m.ctx, m.g, m.st.indexModel, source, m.buildCh), waitBuildProgress(m.buildCh))
}

func buildCmd(ctx context.Context, g *grove.Grove, model, source string, ch chan grove.BuildProgress) tea.Cmd {
	return func() tea.Msg {
		res, err := g.Build(ctx, grove.BuildOpts{
			Model:       model,
			Source:      source,
			Concurrency: 4,
			OnProgress:  func(p grove.BuildProgress) { ch <- p },
		})
		close(ch)
		return buildDoneMsg{res: res, err: err}
	}
}

func waitBuildProgress(ch chan grove.BuildProgress) tea.Cmd {
	return func() tea.Msg {
		p, ok := <-ch
		if !ok {
			return nil // channel closed; stop the progress loop
		}
		return buildProgressMsg(p)
	}
}

func (m *tuiModel) buildFraction() float64 {
	var done, total int
	for _, p := range m.buildProg {
		done += p.Done
		total += p.Total
	}
	if total == 0 {
		return 0
	}
	return float64(done) / float64(total)
}

func (m *tuiModel) renderBuildDone(msg buildDoneMsg) {
	if msg.err != nil {
		m.appendLine(m.styles.errln.Render("build failed: " + msg.err.Error()))
		return
	}
	r := msg.res
	m.appendLine(m.styles.status.Render(fmt.Sprintf(
		"built %d tree(s), %d nodes (%d cached, %d generated) · %d cross-links · %s · %s",
		r.Trees, r.Nodes, r.CacheHits, r.CacheMiss, r.CrossLinks, r.Elapsed, costStr(r.Tally))))
}

func (m *tuiModel) renderResult(msg askResultMsg) {
	if msg.err != nil {
		m.appendLine(m.styles.errln.Render("error: " + msg.err.Error()))
		return
	}
	r := msg.res

	// session accumulators for the sidebar.
	m.qCount++
	m.totCalls += r.Cost.Calls
	m.totPrompt += r.Cost.Usage.PromptTokens
	m.totCompl += r.Cost.Usage.CompletionTokens
	m.totUSD += r.Cost.USD
	if r.Cost.Estimated {
		m.totEstimated = true
	}

	// Assemble the whole assistant turn (answer + sources + meta) into one block
	// so its left accent bar runs continuously down every line, wrapped lines
	// included (opencode-style). Inner styled segments are bound to the block's
	// background so none bleed through as app-bg patches.
	bg := colBgGrove
	on := func(s lipgloss.Style) lipgloss.Style { return s.Background(bg) }
	var b strings.Builder
	if r.Answer != "" {
		b.WriteString(r.Answer)
	}
	cited, showAll, suppress := grove.CitationDisplay(r.Answer, m.st.retrieveOnly, r.Abstained)
	if len(r.Citations) > 0 && !suppress {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(on(m.styles.dim).Render("sources") + on(m.styles.meta).Render("   ⌘-click to open"))
		for _, c := range r.Citations {
			if !showAll && !cited[c.N] {
				continue
			}
			title := c.Title
			if title == "" {
				title = c.SourceRef
			}
			loc := c.Location
			if loc == "" {
				loc = c.Source + "/" + c.SourceRef
			}
			b.WriteString("\n  " + on(m.styles.cite).Render(fmt.Sprintf("[%d]", c.N)) + " " + on(m.styles.answer).Render(title))
			b.WriteString("\n      " + fileLink(loc, on(m.styles.citePath).Render(loc)))
		}
	}

	elapsed := time.Since(m.askStart).Seconds()
	meta := fmt.Sprintf("%s · %s · %.1fs · %d tokens · %s",
		m.st.mode, orDefault(m.st.model), elapsed,
		r.Cost.Usage.PromptTokens+r.Cost.Usage.CompletionTokens, costStr(r.Cost))
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString(on(m.styles.metaDot).Render("● ") + on(m.styles.meta).Render(meta))

	m.appendLine("")
	m.appendLine(m.styles.groveBlock.Width(m.vp.Width - 1).Render(b.String()))
}

func (m *tuiModel) refreshViewport() {
	if !m.ready {
		return
	}
	// Pad every line to the full viewport width with the app bg so no terminal
	// background shows behind short lines (e.g. command echoes, intro text).
	m.vp.SetContent(lipgloss.NewStyle().Background(colBg).Width(m.vp.Width).Render(strings.TrimRight(m.body, "\n")))
	m.vp.GotoBottom()
}

func (m tuiModel) View() string {
	if !m.ready {
		return "starting grove…"
	}
	if !m.started {
		return m.splashView()
	}
	parts := []string{m.styles.vpBox.Render(m.vp.View())}
	switch {
	case len(m.submenu) > 0:
		parts = append(parts, m.submenuView())
	case len(m.palette) > 0:
		parts = append(parts, m.paletteView())
	}
	parts = append(parts, m.inputView())
	parts = append(parts, m.activityView())
	if m.sidebarWidth() == 0 {
		parts = append(parts, m.statusView()) // sidebar shows this when visible
	} else {
		parts = append(parts, "")
	}
	parts = append(parts, m.footerView())
	left := lipgloss.NewStyle().Background(colBg).Width(m.mainW).Height(m.height).Render(lipgloss.JoinVertical(lipgloss.Left, parts...))
	var frame string
	if m.sidebarWidth() == 0 {
		frame = left
	} else {
		frame = lipgloss.JoinHorizontal(lipgloss.Top, left, m.sidebarView())
	}
	return m.appBg(frame)
}

// appBg paints the whole terminal with the app background so no terminal
// background bleeds through (opencode-style full-screen fill).
func (m tuiModel) appBg(s string) string {
	return lipgloss.NewStyle().Background(colBg).Width(m.width).Height(m.height).Render(s)
}

// sidebarView is the right info panel: forest, session config, and usage —
// opencode-style. Hidden on a narrow terminal (sidebarWidth 0).
func (m tuiModel) sidebarView() string {
	w := m.sidebarWidth()
	if w == 0 {
		return ""
	}
	s := m.styles
	inner := w - 4 // horizontal padding (2+2)
	// Keep the separator space inside a backgrounded span so the panel fill has
	// no gaps between key and value.
	kv := func(k, v string) string {
		return s.sideKey.Render(k+" ") + s.sideVal.Render(v)
	}
	tipStyle := s.dim.Background(colBgSide)
	// provider availability line: green "ready" if the key/endpoint is present.
	prov := func(name string, ok bool) string {
		if ok {
			return s.sideKey.Render(name+" ") + s.sideOn.Render("ready")
		}
		return s.sideKey.Render(name+" ") + s.sideOff.Render("— no key")
	}

	// One flat list of moss-backgrounded lines; the outer side style fills the
	// remaining height with the same background so the panel is uniform.
	lines := []string{
		s.sideTitle.Render("Forest"),
		kv("path", truncLeft(tildePath(m.g.Layout().Root), inner-5)),
		kv("trees", fmt.Sprintf("%d", len(m.sources))),
		kv("focus", orAll(m.st.source)),
		"",
		s.sideTitle.Render("Session"),
		kv("mode", m.st.mode),
		kv("query", m.resolvedModel(m.st.model, m.cfgQuery)),
		kv("index", m.resolvedModel(m.st.indexModel, m.cfgIndex)),
		kv("embed", m.cfgEmbed),
		"",
		s.sideTitle.Render("Providers"),
		prov("ollama   ", m.provOllama),
		prov("deepseek ", m.provDeepSeek),
		prov("openai   ", m.provOpenAI),
		prov("anthropic", m.provAnthropic),
		"",
		s.sideTitle.Render("Usage"),
		kv("queries", fmt.Sprintf("%d", m.qCount)),
		kv("tokens", fmt.Sprintf("%d", m.totPrompt+m.totCompl)),
		kv("cost", costStr(sessionTally(m))),
		"",
		s.sideTitle.Render("Tip"),
		tipStyle.Render(m.tip),
	}
	return s.side.Width(inner).Height(m.height).Render(strings.Join(lines, "\n"))
}

// resolvedModel shows the model that will actually run: the session override if
// set, else the config value, else an actionable "not set" hint — never the
// opaque "(config default)".
func (m tuiModel) resolvedModel(override, cfg string) string {
	switch {
	case override != "":
		return override
	case cfg != "":
		return cfg
	default:
		return "— not set"
	}
}

func sessionTally(m tuiModel) llm.Tally {
	return llm.Tally{Calls: m.totCalls, USD: m.totUSD, Estimated: m.totEstimated}
}

// tildePath abbreviates the home directory to ~ for display.
func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// truncLeft keeps the tail of a path (the informative end), eliding the head
// with … when it's wider than max.
func truncLeft(s string, max int) string {
	if max < 4 || len(s) <= max {
		return s
	}
	return "…" + s[len(s)-(max-1):]
}

func (m tuiModel) paletteView() string {
	s := m.styles
	inner := m.mainW - 4 // box border(2)+padding(2) → total width == input box (mainW)
	var rows []string
	for i, c := range m.palette {
		name := c.name
		if c.args != "" {
			name += " " + c.args
		}
		if i == m.palIdx {
			rows = append(rows, s.palSel.Width(inner).Render(name+"  "+c.desc))
			continue
		}
		// Separator + trailing fill must carry the app bg, else they show
		// through as unstyled (terminal) cells.
		text := s.palName.Render(name) + s.palArg.Render("  "+c.desc)
		rows = append(rows, lipgloss.NewStyle().Background(colBg).Width(inner).Render(text))
	}
	return s.palBox.Render(strings.Join(rows, "\n"))
}

// splashView is the centered welcome shown before the first question — logo,
// input, status, and a tip, vertically centered. After the first submit the
// model flips to the normal top-anchored layout (input drops to the bottom).
func (m tuiModel) splashView() string {
	s := m.styles
	logo := s.logo.Render(strings.Join(groveBanner, "\n"))
	tagline := s.status.Render("🌳 ") + s.dim.Render("ask your forest — local-first, cited answers")

	block := []string{logo, "", tagline, ""}
	switch {
	case len(m.submenu) > 0:
		block = append(block, m.submenuView())
	case len(m.palette) > 0:
		block = append(block, m.paletteView())
	}
	hints := s.footerKey.Render("enter") + s.footer.Render(" ask   ") +
		s.footerKey.Render("/") + s.footer.Render(" commands   ") +
		s.footerKey.Render("ctrl+c") + s.footer.Render(" quit")
	tip := s.status.Render("tip ") + s.dim.Render(m.tip)
	block = append(block, m.inputView(), m.statusView(), "", hints, "", tip)

	// Center each element across the full width with app-bg whitespace, so the
	// side padding is painted (not left as unstyled terminal-bg cells).
	rows := make([]string, len(block))
	for i, ln := range block {
		rows[i] = lipgloss.PlaceHorizontal(m.width, lipgloss.Center, ln, lipgloss.WithWhitespaceBackground(colBg))
	}
	body := lipgloss.JoinVertical(lipgloss.Left, rows...)
	return lipgloss.Place(m.width, m.height, lipgloss.Left, lipgloss.Center, body,
		lipgloss.WithWhitespaceBackground(colBg))
}

func (m tuiModel) submenuView() string {
	s := m.styles
	inner := m.mainW - 4 // match the input box total width (mainW)
	title := s.palArg.Render(fmt.Sprintf("%s — pick a value (↑↓ enter · esc cancel)", m.submenuCmd))
	rows := []string{lipgloss.NewStyle().Background(colBg).Width(inner).Render(title)}
	for i, v := range m.submenu {
		if i == m.subIdx {
			rows = append(rows, s.palSel.Width(inner).Render(v))
		} else {
			rows = append(rows, lipgloss.NewStyle().Background(colBg).Width(inner).Render(s.palName.Render(v)))
		}
	}
	return s.palBox.Render(strings.Join(rows, "\n"))
}

func (m tuiModel) inputView() string {
	inner := m.styles.prompt.Render("❯ ") + m.input.View()
	return m.styles.inputBox.Width(m.mainW - 5).Render(inner)
}

// activityView is the spinner line shown under the input while a query or build
// runs (opencode-style — below the box, not replacing the prompt). Always one
// row (blank when idle) so the layout height stays stable.
func (m tuiModel) activityView() string {
	var s string
	switch {
	case m.building:
		f := m.buildFraction()
		s = m.spin.View() + " " + m.styles.dim.Render("building… ") +
			m.prog.ViewAs(f) + m.styles.dim.Render(fmt.Sprintf(" %.0f%%", f*100))
	case m.thinking:
		word := m.thinkWord
		if word == "" {
			word = "Thinking"
		}
		s = m.spin.View() + " " + m.styles.status.Render(word+"…")
	}
	return lipgloss.NewStyle().Background(colBg).Width(m.mainW).Padding(0, 1).Render(s)
}

// statusView is the opencode-style pill line just under the input.
func (m tuiModel) statusView() string {
	s := m.styles
	seg := func(k, v string) string { return s.stKey.Render(k) + s.stVal.Render(v) }
	left := s.logo.Render("🌳 grove") + s.dim.Render("  ·  ") +
		seg("mode ", m.st.mode) + s.dim.Render("  ·  ") +
		seg("source ", orAll(m.st.source)) + s.dim.Render("  ·  ") +
		seg("model ", orDefault(m.st.model))
	if m.st.retrieveOnly {
		left += s.dim.Render("  ·  ") + s.status.Render("retrieve-only")
	}
	return lipgloss.NewStyle().Background(colBg).Width(m.mainW).Padding(0, 1).Render(left)
}

func (m tuiModel) footerView() string {
	s := m.styles
	hint := s.footerKey.Render("enter") + s.footer.Render(" ask  ") +
		s.footerKey.Render("/") + s.footer.Render(" commands  ") +
		s.footerKey.Render("↑↓") + s.footer.Render(" pick  ") +
		s.footerKey.Render("tab") + s.footer.Render(" complete  ") +
		s.footerKey.Render("ctrl+c") + s.footer.Render(" quit")
	return lipgloss.NewStyle().Background(colBg).Width(m.mainW).Padding(0, 1).Render(hint)
}

// runTUI launches the bubbletea REPL over the alt screen. It owns g's
// lifecycle: /workspace may swap the handle, so the final model's handle is
// closed here rather than by the caller.
func runTUI(ctx context.Context, g *grove.Grove, st *replState, sources []string) error {
	// Detect the terminal background once, before bubbletea takes the screen,
	// so the palette (built inside newTUIModel) matches a light or dark terminal.
	applyTheme(termenv.HasDarkBackground())
	fm, err := tea.NewProgram(newTUIModel(ctx, g, st, sources), tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	if tm, ok := fm.(tuiModel); ok && tm.g != nil {
		tm.g.Close()
	} else {
		g.Close()
	}
	return err
}
