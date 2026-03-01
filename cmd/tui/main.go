package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"immich-uploader/internal/uploader"
)

type uploaderEventMsg struct{ ev uploader.Event }
type uploaderDoneMsg struct{ err error }
type preflightLoadedMsg struct {
	id   int
	plan uploader.UploadPlan
	err  error
}
type tickMsg time.Time

type uiMode string

const (
	modeWizard  uiMode = "wizard"
	modeReady   uiMode = "ready"
	modeRunning uiMode = "running"
)

type appConfig struct {
	BaseURL         string `json:"base_url"`
	APIKey          string `json:"api_key"`
	Root            string `json:"root"`
	Deep            bool   `json:"deep"`
	Checksum        bool   `json:"checksum"`
	BatchSize       int    `json:"batch_size"`
	Workers         int    `json:"workers"`
	SmallestFirst   bool   `json:"smallest_first"`
	DedupeAdd       bool   `json:"dedupe_add"`
	DeleteOnSuccess bool   `json:"delete_on_success"`
	IgnoreDir       string `json:"ignore_dir"`
	Timeout         string `json:"timeout"`
}

func defaultConfig() appConfig {
	return appConfig{
		BaseURL:         "http://localhost:2283/api",
		APIKey:          "",
		Root:            ".",
		Deep:            true,
		Checksum:        true,
		BatchSize:       200,
		Workers:         4,
		SmallestFirst:   true,
		DedupeAdd:       true,
		DeleteOnSuccess: false,
		IgnoreDir:       "ignore",
		Timeout:         (5 * time.Hour).String(),
	}
}

func defaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "immich-uploader-tui.json"
	}
	return filepath.Join(dir, "immich-uploader", "tui-config.json")
}

func loadConfig(path string) (appConfig, bool, error) {
	cfg := defaultConfig()
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, false, nil
		}
		return cfg, false, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, false, err
	}
	return cfg, true, nil
}

func saveConfig(path string, cfg appConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func argValue(name, fallback string) string {
	prefix := name + "="
	for i := 1; i < len(os.Args); i++ {
		a := os.Args[i]
		if strings.HasPrefix(a, prefix) {
			return strings.TrimPrefix(a, prefix)
		}
		if a == name && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}
	return fallback
}

type keyMap struct {
	Start      key.Binding
	Wizard     key.Binding
	Refresh    key.Binding
	Quit       key.Binding
	ToggleLog  key.Binding
	ToggleHelp key.Binding
	Up         key.Binding
	Down       key.Binding
	PgUp       key.Binding
	PgDown     key.Binding
	Top        key.Binding
	Bottom     key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Start:      key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "start")),
		Wizard:     key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "wizard")),
		Refresh:    key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh plan")),
		Quit:       key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		ToggleLog:  key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "toggle logs")),
		ToggleHelp: key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Up:         key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "log up")),
		Down:       key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "log down")),
		PgUp:       key.NewBinding(key.WithKeys("pgup", "u"), key.WithHelp("pgup/u", "page up")),
		PgDown:     key.NewBinding(key.WithKeys("pgdown", "d"), key.WithHelp("pgdn/d", "page down")),
		Top:        key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "top")),
		Bottom:     key.NewBinding(key.WithKeys("G"), key.WithHelp("G", "bottom")),
	}
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Start, k.Wizard, k.Refresh, k.ToggleHelp, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Start, k.Wizard, k.Refresh, k.ToggleLog, k.ToggleHelp, k.Quit},
		{k.Up, k.Down, k.PgUp, k.PgDown, k.Top, k.Bottom},
	}
}

type model struct {
	start    time.Time
	cancel   context.CancelFunc
	events   <-chan uploader.Event
	done     <-chan error
	width    int
	height   int
	running  bool
	finished bool
	err      error
	mode     uiMode

	verbose  bool
	showHelp bool
	keys     keyMap
	help     help.Model

	configPath  string
	cfg         appConfig
	planReqID   int
	plan        uploader.UploadPlan
	planLoaded  bool
	planLoading bool
	planErr     string

	wizardInputs []textinput.Model
	wizardFocus  int
	wizardErr    string

	albumName       string
	albumStart      time.Time
	albumIndex      int
	albumTotal      int
	albumDone       int
	albumFiles      int
	albumBytes      int64
	albumTotalBytes int64
	lastEvent       string

	globalFiles     int
	globalDup       int
	globalFailed    int
	globalMovedFail int
	globalBytes     int64

	eventLines []string
	viewport   viewport.Model
	spinner    spinner.Model
	fileProg   progress.Model
	byteProg   progress.Model
}

func listenEvent(events <-chan uploader.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-events
		if !ok {
			return nil
		}
		return uploaderEventMsg{ev: ev}
	}
}

func waitDone(done <-chan error) tea.Cmd {
	return func() tea.Msg {
		err, ok := <-done
		if !ok {
			return uploaderDoneMsg{}
		}
		return uploaderDoneMsg{err: err}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func loadPreflightCmd(id int, cfg appConfig) tea.Cmd {
	return func() tea.Msg {
		opt := buildOptions(cfg, nil)
		opt.Timeout = 20 * time.Second
		plan, err := uploader.BuildUploadPlan(context.Background(), opt)
		return preflightLoadedMsg{id: id, plan: plan, err: err}
	}
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spinner.Tick}
	if m.mode == modeReady && m.planLoading {
		cmds = append(cmds, loadPreflightCmd(m.planReqID, m.cfg))
	}
	if m.running {
		cmds = append(cmds, listenEvent(m.events), waitDone(m.done), tickCmd())
	}
	return tea.Batch(cmds...)
}

func isTruthy(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "1" || s == "true" || s == "y" || s == "yes" || s == "on"
}

func mustAtoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func buildOptions(cfg appConfig, onEvent uploader.EventFunc) uploader.Options {
	timeout := 5 * time.Minute
	if d, err := time.ParseDuration(cfg.Timeout); err == nil && d > 0 {
		timeout = d
	}
	return uploader.Options{
		BaseURL:         cfg.BaseURL,
		APIKey:          cfg.APIKey,
		Root:            cfg.Root,
		Deep:            cfg.Deep,
		Checksum:        cfg.Checksum,
		BatchSize:       cfg.BatchSize,
		Workers:         cfg.Workers,
		SmallestFirst:   cfg.SmallestFirst,
		IgnoreDir:       cfg.IgnoreDir,
		Timeout:         timeout,
		DedupeAdd:       cfg.DedupeAdd,
		DeleteOnSuccess: cfg.DeleteOnSuccess,
		TUI:             false,
		TUIAuto:         false,
		NoANSI:          false,
		OnEvent:         onEvent,
	}
}

func (m model) startRunWithConfig(cfg appConfig) (model, tea.Cmd) {
	if cfg.BaseURL == "" || cfg.APIKey == "" || cfg.Root == "" {
		m.wizardErr = "Immich URL, API key, and root folder are required"
		return m, nil
	}
	if st, err := os.Stat(cfg.Root); err != nil || !st.IsDir() {
		m.wizardErr = "Root folder does not exist or is not a directory"
		return m, nil
	}
	if _, err := time.ParseDuration(cfg.Timeout); err != nil {
		m.wizardErr = "Timeout must be valid (example: 5m, 90s)"
		return m, nil
	}

	if err := saveConfig(m.configPath, cfg); err != nil {
		m.wizardErr = "failed to save config: " + err.Error()
		return m, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan uploader.Event, 256)
	done := make(chan error, 1)
	opt := buildOptions(cfg, func(ev uploader.Event) {
		select {
		case events <- ev:
		default:
		}
	})

	go func() {
		err := uploader.Run(ctx, opt, func(string, ...any) {})
		done <- err
		close(done)
		close(events)
	}()

	m.cancel = cancel
	m.events = events
	m.done = done
	m.mode = modeRunning
	m.running = true
	m.finished = false
	m.err = nil
	m.cfg = cfg
	m.start = time.Now()
	m.wizardErr = ""
	m.lastEvent = "Starting upload"
	m.albumStart = time.Time{}
	m.eventLines = nil
	m.viewport.SetContent("")
	m.viewport.GotoBottom()

	return m, tea.Batch(listenEvent(m.events), waitDone(m.done), tickCmd(), m.spinner.Tick)
}

func (m model) startRunFromWizard() (model, tea.Cmd) {
	cfg := m.cfg
	cfg.BaseURL = strings.TrimSpace(m.wizardInputs[0].Value())
	cfg.APIKey = strings.TrimSpace(m.wizardInputs[1].Value())
	cfg.Root = strings.TrimSpace(m.wizardInputs[2].Value())
	cfg.Workers = maxInt(1, mustAtoiDefault(strings.TrimSpace(m.wizardInputs[3].Value()), cfg.Workers))
	cfg.BatchSize = maxInt(1, mustAtoiDefault(strings.TrimSpace(m.wizardInputs[4].Value()), cfg.BatchSize))
	cfg.Timeout = strings.TrimSpace(m.wizardInputs[5].Value())
	cfg.Deep = isTruthy(m.wizardInputs[6].Value())
	cfg.Checksum = isTruthy(m.wizardInputs[7].Value())
	cfg.SmallestFirst = isTruthy(m.wizardInputs[8].Value())
	cfg.DedupeAdd = isTruthy(m.wizardInputs[9].Value())
	cfg.DeleteOnSuccess = isTruthy(m.wizardInputs[10].Value())
	cfg.IgnoreDir = strings.TrimSpace(m.wizardInputs[11].Value())
	return m.startRunWithConfig(cfg)
}

func (m model) startRunFromConfig() (model, tea.Cmd) {
	return m.startRunWithConfig(m.cfg)
}

func (m model) startPreflightLoad() (model, tea.Cmd) {
	m.planReqID++
	m.planLoading = true
	m.planErr = ""
	return m, loadPreflightCmd(m.planReqID, m.cfg)
}

func (m model) appendEvent(line string) model {
	m.eventLines = append(m.eventLines, line)
	if len(m.eventLines) > 600 {
		m.eventLines = m.eventLines[len(m.eventLines)-600:]
	}
	m.viewport.SetContent(strings.Join(m.eventLines, "\n"))
	m.viewport.GotoBottom()
	return m
}

func (m model) updateProgressBars() {
	f := ratio(m.albumDone, m.albumFiles)
	b := ratioBytes(m.albumBytes, m.albumTotalBytes)
	_ = m.fileProg.SetPercent(f)
	_ = m.byteProg.SetPercent(b)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.width < 60 {
			m.width = 60
		}
		vpW := m.width - 8
		if vpW < 20 {
			vpW = 20
		}
		vpH := 10
		if m.height > 28 {
			vpH = 12
		}
		m.viewport.Width = vpW
		m.viewport.Height = vpH
		m.viewport.SetContent(strings.Join(m.eventLines, "\n"))
		m.viewport.GotoBottom()
		m.fileProg.Width = 26
		m.byteProg.Width = 26
		return m, nil
	case tickMsg:
		if m.running {
			return m, tickCmd()
		}
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)
	case tea.MouseMsg:
		if m.mode == modeRunning && m.verbose {
			switch {
			case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonWheelUp:
				m.viewport.LineUp(3)
			case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonWheelDown:
				m.viewport.LineDown(3)
			}
		}
	case tea.KeyMsg:
		if key.Matches(msg, m.keys.ToggleHelp) {
			m.showHelp = !m.showHelp
			return m, nil
		}

		if m.mode == modeWizard {
			switch {
			case key.Matches(msg, m.keys.Quit):
				return m, tea.Quit
			case msg.String() == "tab" || msg.String() == "down" || msg.String() == "enter":
				m.wizardFocus = (m.wizardFocus + 1) % len(m.wizardInputs)
			case msg.String() == "shift+tab" || msg.String() == "up":
				m.wizardFocus--
				if m.wizardFocus < 0 {
					m.wizardFocus = len(m.wizardInputs) - 1
				}
			case msg.String() == "ctrl+s":
				return m.startRunFromWizard()
			}
			for i := range m.wizardInputs {
				if i == m.wizardFocus {
					m.wizardInputs[i].Focus()
				} else {
					m.wizardInputs[i].Blur()
				}
				m.wizardInputs[i], _ = m.wizardInputs[i].Update(msg)
			}
			m.wizardErr = ""
			return m, nil
		}

		if m.mode == modeReady {
			switch {
			case key.Matches(msg, m.keys.Quit):
				return m, tea.Quit
			case key.Matches(msg, m.keys.Start):
				return m.startRunFromConfig()
			case key.Matches(msg, m.keys.Refresh):
				return m.startPreflightLoad()
			case key.Matches(msg, m.keys.Wizard):
				m.mode = modeWizard
				m.wizardInputs = buildWizardInputs(m.cfg)
				m.wizardFocus = 0
				m.wizardErr = ""
				return m, nil
			}
			return m, nil
		}

		if m.mode == modeRunning {
			switch {
			case key.Matches(msg, m.keys.Quit):
				if m.running && m.cancel != nil {
					m.cancel()
				}
				return m, tea.Quit
			case key.Matches(msg, m.keys.ToggleLog):
				m.verbose = !m.verbose
				return m, nil
			case key.Matches(msg, m.keys.Wizard):
				if m.finished {
					m.mode = modeWizard
					m.wizardInputs = buildWizardInputs(m.cfg)
					m.wizardFocus = 0
					m.wizardErr = ""
					return m, nil
				}
			case key.Matches(msg, m.keys.Start):
				if m.finished {
					m.mode = modeReady
					return m.startPreflightLoad()
				}
			}

			if m.verbose {
				switch {
				case key.Matches(msg, m.keys.Up):
					m.viewport.LineUp(1)
				case key.Matches(msg, m.keys.Down):
					m.viewport.LineDown(1)
				case key.Matches(msg, m.keys.PgUp):
					m.viewport.HalfViewUp()
				case key.Matches(msg, m.keys.PgDown):
					m.viewport.HalfViewDown()
				case key.Matches(msg, m.keys.Top):
					m.viewport.GotoTop()
				case key.Matches(msg, m.keys.Bottom):
					m.viewport.GotoBottom()
				}
			}
		}
	case uploaderEventMsg:
		ev := msg.ev
		m.albumTotal = ev.AlbumTotal
		if ev.AlbumIndex > 0 {
			m.albumIndex = ev.AlbumIndex
		}
		if ev.AlbumName != "" {
			m.albumName = ev.AlbumName
		}
		if ev.AlbumFileCount > 0 {
			m.albumFiles = ev.AlbumFileCount
		}
		if ev.AlbumDone > 0 || ev.Kind == uploader.EventAlbumStarted {
			m.albumDone = ev.AlbumDone
		}
		if ev.AlbumTotalBytes > 0 {
			m.albumTotalBytes = ev.AlbumTotalBytes
		}
		if ev.AlbumBytes > 0 || ev.Kind == uploader.EventAlbumStarted {
			m.albumBytes = ev.AlbumBytes
		}
		m.globalFiles = ev.GlobalFiles
		m.globalDup = ev.GlobalDup
		m.globalFailed = ev.GlobalFailed
		m.globalMovedFail = ev.GlobalMovedFail
		m.globalBytes = ev.GlobalBytes

		switch ev.Kind {
		case uploader.EventRunStarted:
			m.running = true
			m.lastEvent = fmt.Sprintf("Starting upload (%d albums)", ev.AlbumTotal)
		case uploader.EventAlbumStarted:
			m.albumStart = ev.Time
			m.lastEvent = fmt.Sprintf("Album %d/%d: %s", ev.AlbumIndex, ev.AlbumTotal, ev.AlbumName)
		case uploader.EventFileUploaded:
			base := ev.FileName
			if strings.TrimSpace(base) == "" {
				base = filepath.Base(ev.FilePath)
			}
			m.lastEvent = fmt.Sprintf("Uploaded %s (%s)", base, ev.AssetStatus)
		case uploader.EventFileFailed:
			m.lastEvent = fmt.Sprintf("Failed %s", ev.FileName)
		case uploader.EventMoveFailed:
			m.lastEvent = fmt.Sprintf("Post-upload action failed %s", ev.FileName)
		case uploader.EventAlbumSkipped:
			m.lastEvent = fmt.Sprintf("Skipped album %s", ev.AlbumName)
		case uploader.EventAlbumError:
			m.lastEvent = fmt.Sprintf("Album error: %s", ev.AlbumName)
		case uploader.EventAlbumFinished:
			m.lastEvent = fmt.Sprintf("Finished %s", ev.AlbumName)
		case uploader.EventRunCompleted:
			m.lastEvent = "Upload completed"
		}

		m.updateProgressBars()
		line := fmt.Sprintf("%s | %s", string(ev.Kind), m.lastEvent)
		if ev.Error != "" {
			line += " | " + ev.Error
		}
		m = m.appendEvent(line)
		cmds = append(cmds, listenEvent(m.events))
	case uploaderDoneMsg:
		m.running = false
		m.finished = true
		m.err = msg.err
		if msg.err != nil {
			m.lastEvent = "Upload failed"
			m = m.appendEvent("error | " + msg.err.Error())
		} else {
			m.lastEvent = "Upload finished"
		}
	case preflightLoadedMsg:
		if msg.id != m.planReqID {
			return m, nil
		}
		m.plan = msg.plan
		m.planLoaded = true
		m.planLoading = false
		if msg.err != nil {
			m.planErr = msg.err.Error()
		} else {
			m.planErr = ""
		}
	}

	return m, tea.Batch(cmds...)
}

func ratio(done, total int) float64 {
	if total <= 0 {
		return 0
	}
	r := float64(done) / float64(total)
	if r < 0 {
		return 0
	}
	if r > 1 {
		return 1
	}
	return r
}

func ratioBytes(done, total int64) float64 {
	if total <= 0 {
		return 0
	}
	r := float64(done) / float64(total)
	if r < 0 {
		return 0
	}
	if r > 1 {
		return 1
	}
	return r
}

func formatBytes(n int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case n >= GB:
		return fmt.Sprintf("%.2f GiB", float64(n)/float64(GB))
	case n >= MB:
		return fmt.Sprintf("%.2f MiB", float64(n)/float64(MB))
	case n >= KB:
		return fmt.Sprintf("%.2f KiB", float64(n)/float64(KB))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func formatMBps(bytes int64, d time.Duration) string {
	if bytes <= 0 || d <= 0 {
		return "0.00 MB/s"
	}
	mb := float64(bytes) / (1024.0 * 1024.0)
	return fmt.Sprintf("%.2f MB/s", mb/d.Seconds())
}

func statusChip(text string, fg, bg lipgloss.Color) string {
	return lipgloss.NewStyle().
		Foreground(fg).
		Background(bg).
		Bold(true).
		Padding(0, 1).
		Render(text)
}

func (m model) wizardView() string {
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("33")).Padding(0, 1)
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	label := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("160")).Padding(0, 1)
	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("39")).
		Background(lipgloss.Color("235")).
		Padding(1, 2)

	labels := []string{
		"Immich URL", "API Key", "Root Folder", "Workers", "Batch Size", "Timeout",
		"Deep", "Checksum", "Smallest First", "Dedupe Add", "Delete On Success", "Ignore Dir",
	}

	lines := make([]string, 0, len(m.wizardInputs))
	for i := range m.wizardInputs {
		prefix := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("  ")
		rowStyle := lipgloss.NewStyle()
		if i == m.wizardFocus {
			prefix = lipgloss.NewStyle().Foreground(lipgloss.Color("226")).Bold(true).Render(">>")
			rowStyle = rowStyle.Background(lipgloss.Color("238"))
		}
		lines = append(lines, rowStyle.Render(fmt.Sprintf("%s %s %s", prefix, label.Width(15).Render(labels[i]+":"), m.wizardInputs[i].View())))
	}

	content := []string{
		headerStyle.Render("Setup Wizard") + " " + statusChip("EDIT MODE", lipgloss.Color("230"), lipgloss.Color("62")),
		muted.Render("Config file: " + m.configPath),
		"",
		strings.Join(lines, "\n"),
		"",
		muted.Render("Tab/Shift+Tab move | type edit | Ctrl+S save+start | q quit"),
	}
	if m.wizardErr != "" {
		content = append(content, errStyle.Render(m.wizardErr))
	}
	return card.Render(strings.Join(content, "\n")) + "\n"
}

func (m model) readyView() string {
	header := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("27")).Padding(0, 1)
	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("44")).
		Background(lipgloss.Color("236")).
		Padding(1, 2)
	label := lipgloss.NewStyle().Foreground(lipgloss.Color("81")).Bold(true)
	value := lipgloss.NewStyle().Foreground(lipgloss.Color("229"))
	hint := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	warn := lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
	startBtn := statusChip(" [S] Start Upload ", lipgloss.Color("230"), lipgloss.Color("35"))
	wizardBtn := statusChip(" [W] Open Wizard ", lipgloss.Color("230"), lipgloss.Color("62"))
	refreshBtn := statusChip(" [R] Refresh Plan ", lipgloss.Color("230"), lipgloss.Color("69"))
	quitBtn := statusChip(" [Q] Quit ", lipgloss.Color("230"), lipgloss.Color("160"))

	planStatus := muted.Render("Plan not loaded yet")
	if m.planLoading {
		planStatus = muted.Render(m.spinner.View() + " calculating upload plan...")
	} else if m.planLoaded {
		planStatus = muted.Render("Upload plan is ready")
	}

	createCount := "unknown"
	if m.plan.ServerChecked {
		createCount = strconv.Itoa(m.plan.AlbumsToCreate)
	}

	lines := []string{
		header.Render("Immich Uploader TUI"),
		"",
		fmt.Sprintf("%s %s", label.Render("Server:"), value.Render(m.cfg.BaseURL)),
		fmt.Sprintf("%s %s", label.Render("Root:"), value.Render(m.cfg.Root)),
		fmt.Sprintf("%s %s", label.Render("Workers:"), value.Render(strconv.Itoa(m.cfg.Workers))),
		fmt.Sprintf("%s %s", label.Render("Delete On Success:"), value.Render(fmt.Sprintf("%t", m.cfg.DeleteOnSuccess))),
		"",
		fmt.Sprintf("%s %s", label.Render("Plan Albums:"), value.Render(strconv.Itoa(m.plan.AlbumsTotal))),
		fmt.Sprintf("%s %s", label.Render("Plan Files:"), value.Render(strconv.Itoa(m.plan.MediaFiles))),
		fmt.Sprintf("%s %s", label.Render("Plan Size:"), value.Render(formatBytes(m.plan.TotalBytes))),
		fmt.Sprintf("%s %s", label.Render("Will Create:"), value.Render(createCount)),
		planStatus,
		"",
		startBtn + "  " + wizardBtn + "  " + refreshBtn + "  " + quitBtn,
		"",
		hint.Render("Press S to start with current config, W to edit settings, or R to recalculate plan."),
	}
	if m.planErr != "" {
		lines = append(lines, warn.Render("Plan warning: "+m.planErr))
	}
	return card.Render(strings.Join(lines, "\n")) + "\n"
}

func (m model) runningView() string {
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("27")).Padding(0, 1)
	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("50"))
	errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("33")).
		Background(lipgloss.Color("236")).
		Padding(0, 1)
	albumCardStyle := card.Copy().BorderForeground(lipgloss.Color("44"))
	statsCardStyle := card.Copy().BorderForeground(lipgloss.Color("99"))
	eventsCardStyle := card.Copy().BorderForeground(lipgloss.Color("172"))
	label := lipgloss.NewStyle().Foreground(lipgloss.Color("81")).Bold(true)

	status := "RUNNING"
	statusChipView := statusChip(status, lipgloss.Color("230"), lipgloss.Color("35"))
	if m.finished && m.err == nil {
		status = "DONE"
		statusChipView = statusChip(status, lipgloss.Color("230"), lipgloss.Color("28"))
	}
	if m.err != nil {
		statusChipView = statusChip("ERROR", lipgloss.Color("230"), lipgloss.Color("160"))
	} else if m.globalFailed > 0 || m.globalMovedFail > 0 {
		statusChipView = statusChip("WARN", lipgloss.Color("232"), lipgloss.Color("220"))
	}

	elapsed := time.Since(m.start).Round(time.Second)
	spin := m.spinner.View()
	title := headerStyle.Render("Immich Uploader TUI") + "  " + statusChipView + "  " + lipgloss.NewStyle().Foreground(lipgloss.Color("81")).Render(spin)
	meta := muted.Render(fmt.Sprintf("elapsed %s | q quit | v logs | ? help | mouse wheel scroll", elapsed))

	albumElapsed := time.Since(m.albumStart)
	if m.albumStart.IsZero() {
		albumElapsed = 0
	}
	fileBar := m.fileProg.ViewAs(ratio(m.albumDone, m.albumFiles))
	byteBar := m.byteProg.ViewAs(ratioBytes(m.albumBytes, m.albumTotalBytes))

	albumCard := albumCardStyle.Render(strings.Join([]string{
		label.Render(fmt.Sprintf("Album %d/%d", m.albumIndex, m.albumTotal)) + " " + lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Render(m.albumName),
		fmt.Sprintf("%s %d/%d %s", label.Render("Files:"), m.albumDone, m.albumFiles, fileBar),
		fmt.Sprintf("%s %s / %s %s", label.Render("Bytes:"), formatBytes(m.albumBytes), formatBytes(m.albumTotalBytes), byteBar),
		fmt.Sprintf("%s %s", label.Render("Album speed:"), okStyle.Render(formatMBps(m.albumBytes, albumElapsed))),
		fmt.Sprintf("%s %s", label.Render("Last:"), lipgloss.NewStyle().Foreground(lipgloss.Color("229")).Render(m.lastEvent)),
	}, "\n"))

	failColor := lipgloss.Color("50")
	if m.globalFailed > 0 || m.globalMovedFail > 0 {
		failColor = lipgloss.Color("196")
	}
	dupColor := lipgloss.Color("39")
	if m.globalDup > 0 {
		dupColor = lipgloss.Color("220")
	}

	stats := []string{
		fmt.Sprintf("%s %s", label.Render("uploaded"), lipgloss.NewStyle().Foreground(lipgloss.Color("50")).Render(fmt.Sprintf("%d", m.globalFiles))),
		fmt.Sprintf("%s %s", label.Render("duplicates"), lipgloss.NewStyle().Foreground(dupColor).Render(fmt.Sprintf("%d", m.globalDup))),
		fmt.Sprintf("%s %s", label.Render("failed"), lipgloss.NewStyle().Foreground(failColor).Render(fmt.Sprintf("%d", m.globalFailed))),
		fmt.Sprintf("%s %s", label.Render("move-failed"), lipgloss.NewStyle().Foreground(failColor).Render(fmt.Sprintf("%d", m.globalMovedFail))),
		fmt.Sprintf("%s %s", label.Render("bytes"), lipgloss.NewStyle().Foreground(lipgloss.Color("87")).Render(formatBytes(m.globalBytes))),
		fmt.Sprintf("%s %s", label.Render("global speed"), okStyle.Render(formatMBps(m.globalBytes, elapsed))),
	}
	statsCard := statsCardStyle.Render(label.Render("Global") + "\n" + strings.Join(stats, "\n"))

	body := ""
	if m.width > 118 {
		// Wide terminals: two-column dashboard.
		leftW := (m.width - 8) * 2 / 3
		rightW := (m.width - 8) - leftW
		if leftW < 48 {
			leftW = 48
		}
		if rightW < 24 {
			rightW = 24
		}
		albumCard = albumCardStyle.Width(leftW).Render(strings.Join([]string{
			label.Render(fmt.Sprintf("Album %d/%d", m.albumIndex, m.albumTotal)) + " " + lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Render(m.albumName),
			fmt.Sprintf("%s %d/%d %s", label.Render("Files:"), m.albumDone, m.albumFiles, fileBar),
			fmt.Sprintf("%s %s / %s %s", label.Render("Bytes:"), formatBytes(m.albumBytes), formatBytes(m.albumTotalBytes), byteBar),
			fmt.Sprintf("%s %s", label.Render("Album speed:"), okStyle.Render(formatMBps(m.albumBytes, albumElapsed))),
			fmt.Sprintf("%s %s", label.Render("Last:"), lipgloss.NewStyle().Foreground(lipgloss.Color("229")).Render(m.lastEvent)),
		}, "\n"))
		statsCard = statsCardStyle.Width(rightW).Render(label.Render("Global") + "\n" + strings.Join(stats, "\n"))
		body = lipgloss.JoinHorizontal(lipgloss.Top, albumCard, "  ", statsCard)
	} else {
		// Narrow terminals: stack panels for readability.
		stackW := m.width - 6
		if stackW < 40 {
			stackW = 40
		}
		albumCard = albumCardStyle.Width(stackW).Render(strings.Join([]string{
			label.Render(fmt.Sprintf("Album %d/%d", m.albumIndex, m.albumTotal)) + " " + lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Render(m.albumName),
			fmt.Sprintf("%s %d/%d %s", label.Render("Files:"), m.albumDone, m.albumFiles, fileBar),
			fmt.Sprintf("%s %s / %s %s", label.Render("Bytes:"), formatBytes(m.albumBytes), formatBytes(m.albumTotalBytes), byteBar),
			fmt.Sprintf("%s %s", label.Render("Album speed:"), okStyle.Render(formatMBps(m.albumBytes, albumElapsed))),
			fmt.Sprintf("%s %s", label.Render("Last:"), lipgloss.NewStyle().Foreground(lipgloss.Color("229")).Render(m.lastEvent)),
		}, "\n"))
		statsCard = statsCardStyle.Width(stackW).Render(label.Render("Global") + "\n" + strings.Join(stats, "\n"))
		body = lipgloss.JoinVertical(lipgloss.Left, albumCard, "", statsCard)
	}
	out := []string{title, meta, "", body}

	if m.verbose {
		out = append(out, "", eventsCardStyle.Render(label.Render("Recent Events")+"\n"+m.viewport.View()))
	}

	if m.finished {
		if m.err != nil {
			out = append(out, "", errStyle.Render("Run finished with error: "+m.err.Error()))
			out = append(out, muted.Render("Press W for wizard or Q to quit."))
		} else {
			out = append(out, "", okStyle.Render("Run finished successfully."))
			out = append(out, muted.Render("Press S to return to ready screen, or W to edit config."))
		}
	}

	if m.showHelp {
		out = append(out, "", m.help.View(m.keys))
	}

	return strings.Join(out, "\n") + "\n"
}

func (m model) View() string {
	switch m.mode {
	case modeWizard:
		return m.wizardView()
	case modeReady:
		return m.readyView()
	default:
		return m.runningView()
	}
}

func buildWizardInputs(cfg appConfig) []textinput.Model {
	vals := []struct {
		v      string
		secret bool
	}{
		{cfg.BaseURL, false},
		{cfg.APIKey, true},
		{cfg.Root, false},
		{strconv.Itoa(cfg.Workers), false},
		{strconv.Itoa(cfg.BatchSize), false},
		{cfg.Timeout, false},
		{fmt.Sprintf("%t", cfg.Deep), false},
		{fmt.Sprintf("%t", cfg.Checksum), false},
		{fmt.Sprintf("%t", cfg.SmallestFirst), false},
		{fmt.Sprintf("%t", cfg.DedupeAdd), false},
		{fmt.Sprintf("%t", cfg.DeleteOnSuccess), false},
		{cfg.IgnoreDir, false},
	}
	inputs := make([]textinput.Model, 0, len(vals))
	for i, it := range vals {
		ti := textinput.New()
		ti.SetValue(it.v)
		ti.CharLimit = 256
		ti.Width = 64
		if it.secret {
			ti.EchoMode = textinput.EchoPassword
			ti.EchoCharacter = '•'
		}
		if i == 0 {
			ti.Focus()
		}
		inputs = append(inputs, ti)
	}
	return inputs
}

func main() {
	cfgPath := argValue("--config", defaultConfigPath())
	cfg, exists, err := loadConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config %s: %v\n", cfgPath, err)
		os.Exit(1)
	}

	wizardDefault := !exists

	var (
		configPath = flag.String("config", cfgPath, "Config file path")
		wizard     = flag.Bool("wizard", wizardDefault, "Open setup wizard before running")
		verbose    = flag.Bool("verbose", true, "Show recent event log panel")

		baseURL       = flag.String("immich", cfg.BaseURL, "Immich base API URL (include /api)")
		apiKey        = flag.String("key", cfg.APIKey, "Immich API key (x-api-key)")
		root          = flag.String("root", cfg.Root, "Root folder containing album folders")
		deep          = flag.Bool("deep", cfg.Deep, "Upload nested subfolders")
		checksum      = flag.Bool("checksum", cfg.Checksum, "Compute sha1 and send x-immich-checksum")
		batchSize     = flag.Int("batch", cfg.BatchSize, "Assets per album add request")
		workers       = flag.Int("workers", cfg.Workers, "Parallel upload workers per album")
		smallestFirst = flag.Bool("smallest-first", cfg.SmallestFirst, "Upload smaller files first")
		dedupeAdd     = flag.Bool("dedupe-add", cfg.DedupeAdd, "Allow duplicate detection/add")
		deleteSuccess = flag.Bool("delete-on-success", cfg.DeleteOnSuccess, "Verify checksum and delete local file")
		timeout       = flag.String("timeout", cfg.Timeout, "HTTP timeout (example: 5m, 90s)")
		ignoreDir     = flag.String("ignore-dir", cfg.IgnoreDir, "Ignore folder name")
	)
	flag.Parse()

	cfg.BaseURL = strings.TrimSpace(*baseURL)
	cfg.APIKey = strings.TrimSpace(*apiKey)
	cfg.Root = strings.TrimSpace(*root)
	cfg.Deep = *deep
	cfg.Checksum = *checksum
	cfg.BatchSize = *batchSize
	cfg.Workers = *workers
	cfg.SmallestFirst = *smallestFirst
	cfg.DedupeAdd = *dedupeAdd
	cfg.DeleteOnSuccess = *deleteSuccess
	cfg.Timeout = strings.TrimSpace(*timeout)
	cfg.IgnoreDir = strings.TrimSpace(*ignoreDir)

	spin := spinner.New()
	spin.Spinner = spinner.Dot
	spin.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("81"))

	fileProg := progress.New(
		progress.WithDefaultGradient(),
		progress.WithoutPercentage(),
	)
	byteProg := progress.New(
		progress.WithGradient("#6EE7B7", "#34D399"),
		progress.WithoutPercentage(),
	)
	vp := viewport.New(80, 10)

	m := model{
		mode:         modeWizard,
		verbose:      *verbose,
		showHelp:     false,
		keys:         newKeyMap(),
		help:         help.New(),
		configPath:   *configPath,
		cfg:          cfg,
		planReqID:    1,
		planLoading:  true,
		wizardInputs: buildWizardInputs(cfg),
		spinner:      spin,
		fileProg:     fileProg,
		byteProg:     byteProg,
		viewport:     vp,
	}
	m.help.ShowAll = false

	if !*wizard {
		m.mode = modeReady
	}

	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
