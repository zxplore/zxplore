// model.go — the bubbletea TUI.
//
// Three modes so far:
//
//	browse    — the F1 filesystem browser: dataset list + live dossier.
//	favorites — saved quick-connects (WinSCP-style saved sessions): pick + jump.
//	connect   — a text input to dial a NEW [user@]host:pool and save it.
//
// zexplore is a terminal UI in the spirit of k9s — rich, keyboard first (mouse
// enabled) — running on any ZFS system. Browsing a favorite that points at a
// remote host re-homes the whole browser there over SSH.
package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── Styles ───────────────────────────────────────────────────────────────────
var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0b1220")).Background(lipgloss.Color("#5fc4bc")).Padding(0, 1)
	badgeStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0b1220")).Background(lipgloss.Color("#4cb98a")).Padding(0, 1)
	hostStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#7cd0c9"))
	footerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	paneStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240"))
	paneFocus   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#5ab0ff"))
	cursorStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0b1220")).Background(lipgloss.Color("#5ab0ff"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#4cb98a"))
)

type uiMode int

const (
	modeBrowse uiMode = iota
	modeFavorites
	modeConnect
)

type model struct {
	host      Host
	datasets  []Dataset
	cursor    int
	dossier   string
	width     int
	height    int
	err       string
	status    string
	kldload   bool
	mode      uiMode
	favorites []Favorite
	favCursor int
	input     textinput.Model
}

func newModel() model {
	ti := textinput.New()
	ti.Placeholder = "user@host:pool/path   (blank host = local)"
	ti.CharLimit = 256
	ti.Width = 48
	m := model{host: LocalHost(), kldload: IsKldload(), input: ti}
	m.reload()
	return m
}

func (m *model) reload() {
	ds, err := ListDatasets(m.host)
	m.datasets = ds
	if err != nil {
		m.err = err.Error()
	} else {
		m.err = ""
	}
	if m.cursor >= len(m.datasets) {
		m.cursor = len(m.datasets) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	m.refreshDossier()
}

func (m *model) refreshDossier() {
	if m.cursor >= 0 && m.cursor < len(m.datasets) {
		m.dossier = Dossier(m.host, m.datasets[m.cursor].Name)
	} else {
		m.dossier = "(no datasets)"
	}
}

// connect re-homes the browser at a favorite's host + lands on its path.
func (m model) connect(f Favorite) model {
	m.host = f.Host()
	m.cursor = 0
	m.reload()
	if f.Path != "" {
		for i, d := range m.datasets {
			if d.Name == f.Path {
				m.cursor = i
				break
			}
		}
		m.refreshDossier()
	}
	if m.err != "" {
		m.status = "✗ " + m.host.Label() + ": " + m.err
	} else {
		m.status = "connected: " + f.Target()
	}
	return m
}

func (m *model) bookmarkCurrent() {
	if len(m.datasets) == 0 {
		return
	}
	ds := m.datasets[m.cursor].Name
	f := Favorite{Name: (Favorite{SSH: m.host.SSH, Path: ds}).Target(), SSH: m.host.SSH, Path: ds}
	m.favorites = AddFavorite(LoadFavorites(), f)
	_ = SaveFavorites(m.favorites)
	m.status = "bookmarked: " + f.Name
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		switch m.mode {
		case modeFavorites:
			return m.updateFavorites(msg), nil
		case modeConnect:
			return m.updateConnect(msg)
		default:
			if s := msg.String(); s == "q" || s == "esc" {
				return m, tea.Quit
			}
			return m.updateBrowse(msg), nil
		}
	}
	return m, nil
}

func (m model) updateBrowse(msg tea.KeyMsg) model {
	switch msg.String() {
	case "down", "j":
		if m.cursor < len(m.datasets)-1 {
			m.cursor++
			m.refreshDossier()
		}
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.refreshDossier()
		}
	case "g", "home":
		m.cursor = 0
		m.refreshDossier()
	case "G", "end":
		if len(m.datasets) > 0 {
			m.cursor = len(m.datasets) - 1
			m.refreshDossier()
		}
	case "r":
		m.reload()
		m.status = "reloaded"
	case "c":
		m.favorites = LoadFavorites()
		m.favCursor = 0
		m.mode = modeFavorites
	case "b":
		m.bookmarkCurrent()
	}
	return m
}

func (m model) updateFavorites(msg tea.KeyMsg) model {
	switch msg.String() {
	case "esc", "c", "q":
		m.mode = modeBrowse
	case "down", "j":
		if m.favCursor < len(m.favorites)-1 {
			m.favCursor++
		}
	case "up", "k":
		if m.favCursor > 0 {
			m.favCursor--
		}
	case "n":
		m.input.SetValue("")
		m.input.Focus()
		m.mode = modeConnect
	case "d":
		if m.favCursor < len(m.favorites) {
			m.favorites = append(m.favorites[:m.favCursor], m.favorites[m.favCursor+1:]...)
			_ = SaveFavorites(m.favorites)
			if m.favCursor >= len(m.favorites) && m.favCursor > 0 {
				m.favCursor--
			}
		}
	case "enter":
		if m.favCursor >= 0 && m.favCursor < len(m.favorites) {
			f := m.favorites[m.favCursor]
			m.mode = modeBrowse
			return m.connect(f)
		}
	}
	return m
}

func (m model) updateConnect(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeFavorites
		return m, nil
	case "enter":
		v := strings.TrimSpace(m.input.Value())
		m.mode = modeBrowse
		if v != "" {
			f := ParseTarget(v)
			m.favorites = AddFavorite(LoadFavorites(), f)
			_ = SaveFavorites(m.favorites)
			return m.connect(f), nil
		}
		return m, nil
	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
}

// ── View ─────────────────────────────────────────────────────────────────────
func (m model) View() string {
	if m.width == 0 {
		return "loading…"
	}
	switch m.mode {
	case modeFavorites:
		return m.viewFavorites()
	case modeConnect:
		return m.viewConnect()
	default:
		return m.viewBrowse()
	}
}

func (m model) viewBrowse() string {
	title := titleStyle.Render("zexplore")
	host := hostStyle.Render("  " + m.host.Label())
	badge := ""
	if m.kldload {
		badge = "  " + badgeStyle.Render("kldload")
	}
	header := lipgloss.JoinHorizontal(lipgloss.Top, title, host, badge)

	bodyH := m.height - 4
	if bodyH < 3 {
		bodyH = 3
	}
	leftW := m.width * 45 / 100
	if leftW < 24 {
		leftW = 24
	}
	rightW := m.width - leftW - 4
	if rightW < 12 {
		rightW = 12
	}
	left := paneFocus.Width(leftW).Height(bodyH).Render(m.renderList(leftW, bodyH))
	right := paneStyle.Width(rightW).Height(bodyH).Render(m.renderDossier(bodyH))
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	foot := footerStyle.Render(" ↑/↓ move  c connect  b bookmark  r reload  q quit    [F1 files] F2 transfer F3 restore F4 pools (coming)")
	if m.status != "" {
		foot = okStyle.Render(" "+m.status) + "\n" + foot
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, foot)
}

func (m model) viewFavorites() string {
	title := titleStyle.Render("zexplore") + hostStyle.Render("  quick connect")
	var b strings.Builder
	if len(m.favorites) == 0 {
		b.WriteString(dimStyle.Render("  no saved connections yet — press n to add one\n"))
	}
	for i, f := range m.favorites {
		label := fmt.Sprintf("  %-40s %s", f.Name, dimStyle.Render(f.Target()))
		if i == m.favCursor {
			label = cursorStyle.Render(fmt.Sprintf(" %-*s", m.width-2, f.Name+"   "+f.Target()))
		}
		b.WriteString(label + "\n")
	}
	foot := footerStyle.Render(" ↵ connect   n new   d delete   esc back")
	box := paneFocus.Width(m.width - 2).Height(m.height - 4).Render(b.String())
	return lipgloss.JoinVertical(lipgloss.Left, title, box, foot)
}

func (m model) viewConnect() string {
	title := titleStyle.Render("zexplore") + hostStyle.Render("  new connection")
	prompt := "Dial a target — [user@]host:pool/path  (or a local pool/path):\n\n" + m.input.View()
	box := paneFocus.Width(m.width - 2).Height(m.height - 4).Render(prompt)
	foot := footerStyle.Render(" ↵ connect + save   esc cancel")
	return lipgloss.JoinVertical(lipgloss.Left, title, box, foot)
}

func (m model) renderList(w, h int) string {
	if m.err != "" {
		return dimStyle.Render(truncate("error: "+m.err, w))
	}
	if len(m.datasets) == 0 {
		return dimStyle.Render("(no datasets)")
	}
	top := 0
	if m.cursor >= h {
		top = m.cursor - h + 1
	}
	end := top + h
	if end > len(m.datasets) {
		end = len(m.datasets)
	}
	var b strings.Builder
	for i := top; i < end; i++ {
		d := m.datasets[i]
		meta := fmt.Sprintf("%s/%s ×%d", d.Used, d.Refer, d.Snaps)
		nameW := w - lipgloss.Width(meta) - 1
		if nameW < 4 {
			nameW = 4
		}
		line := padRight(truncate(d.Name, nameW), nameW) + " " + meta
		line = truncate(line, w)
		if i == m.cursor {
			line = cursorStyle.Render(padRight(line, w))
		}
		b.WriteString(line)
		if i < end-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (m model) renderDossier(h int) string {
	lines := strings.Split(m.dossier, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	return strings.Join(lines, "\n")
}

func truncate(s string, n int) string {
	if n < 1 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	for len(s) > 0 && lipgloss.Width(s)+1 > n {
		s = s[:len(s)-1]
	}
	return s + "…"
}

func padRight(s string, n int) string {
	w := lipgloss.Width(s)
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}
