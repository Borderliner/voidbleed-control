package control

import (
	"context"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Borderliner/voidbleed-control/internal/system"
)

// defaultsPage is what opens what: the file kinds a desktop has an opinion
// about, and the application each one goes to. It has two lists and shows one
// at a time -- the kinds, and, once a kind is chosen, the applications that
// can open it -- because a chooser built out of the same table is a chooser
// that scrolls, filters and looks like everything else here.
type defaultsPage struct {
	table   Table
	rows    []system.Default
	all     bool // every MIME type on the machine, not the curated kinds
	loading bool
	picking *system.Default
	last    string // the kind to come back to after choosing
	// notice is what a change had to say, kept across the reload that
	// follows it: the confirmation is the point, and the summary can wait.
	notice string
}

type defaultsLoadedMsg struct{ rows []system.Default }
type defaultsAppliedMsg struct {
	status string
	err    error
}

func newDefaultsPage() *defaultsPage {
	return &defaultsPage{table: Table{
		Headers: []string{"opens", "with", ""},
		Widths:  []int{0, 26, 7},
	}}
}

// Typing reports whether the page has the keyboard: while a filter is open,
// and while an application is being chosen, where enter and the letters mean
// something else than they do on the page.
func (p *defaultsPage) Typing() bool { return p.table.Typing() || p.picking != nil }

func (p *defaultsPage) Label() string { return "Defaults" }

func (p *defaultsPage) Title() (string, string) {
	if p.picking != nil {
		return "Default applications", "what opens " + strings.ToLower(p.picking.Label)
	}
	if p.all {
		return "Default applications", "every type this machine knows"
	}
	return "Default applications", "what opens what"
}

func (p *defaultsPage) Load(m *Model) tea.Cmd {
	p.loading = true
	client, all := m.Client, p.all
	return func() tea.Msg {
		ctx := context.Background()
		if all {
			return defaultsLoadedMsg{rows: client.EveryType(ctx)}
		}
		return defaultsLoadedMsg{rows: client.Defaults(ctx)}
	}
}

func (p *defaultsPage) Update(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case defaultsLoadedMsg:
		p.loading = false
		p.rows = msg.rows
		// A reload after a change lands back on the kinds, where the change
		// can be seen.
		p.picking = nil
		p.fill()
		if p.last != "" {
			p.table.Focus(p.last)
		}
		if p.notice != "" {
			m.Status(p.notice)
			p.notice = ""
			return nil
		}
		m.Status(p.summary())
		return nil
	case defaultsAppliedMsg:
		if msg.err != nil {
			m.Fail(msg.err)
			return nil
		}
		p.notice = msg.status
		return p.Load(m)
	case tea.KeyPressMsg:
		return p.key(m, msg.String())
	}
	return nil
}

func (p *defaultsPage) key(m *Model, key string) tea.Cmd {
	if p.table.Typing() {
		p.table.Key(key, 10)
		return nil
	}
	row, has := p.table.Current()

	if p.picking != nil {
		switch key {
		case "esc", "q":
			p.stopPicking()
			return nil
		case "enter":
			if !has {
				return nil
			}
			kind, app := p.picking.FileKind, p.app(row.ID)
			client := m.Client
			p.stopPicking()
			m.Status("writing " + system.MimeappsPath() + "…")
			return func() tea.Msg {
				if err := client.SetDefault(kind, app); err != nil {
					return defaultsAppliedMsg{err: err}
				}
				return defaultsAppliedMsg{status: kind.Label + " now opens with " + app.Name}
			}
		}
		p.table.Key(key, 10)
		return nil
	}

	switch key {
	case "a":
		p.all = !p.all
		p.last = ""
		p.table.ClearFilter()
		return p.Load(m)
	case "enter", "c":
		if !has {
			return nil
		}
		if !p.startPicking(row.ID) {
			m.Status(row.ID + ": nothing installed says it opens this")
		}
		return nil
	case "x":
		if !has {
			return nil
		}
		kind := p.row(row.ID).FileKind
		client := m.Client
		p.last = row.ID
		return func() tea.Msg {
			if err := client.ClearDefault(kind); err != nil {
				return defaultsAppliedMsg{err: err}
			}
			return defaultsAppliedMsg{status: kind.Label + ": your choice dropped, back to the system default"}
		}
	}
	p.table.Key(key, 10)
	return nil
}

// startPicking opens the chooser, and reports whether there was anything to
// choose from: a kind no installed application claims has nothing to offer.
func (p *defaultsPage) startPicking(label string) bool {
	row := p.row(label)
	if len(row.Choices) == 0 {
		return false
	}
	p.picking, p.last = &row, label
	p.table.ClearFilter()
	p.fill()
	p.table.Focus(row.App.ID)
	return true
}

func (p *defaultsPage) stopPicking() {
	p.picking = nil
	p.table.ClearFilter()
	p.fill()
	p.table.Focus(p.last)
}

// row is the kind with this label, or the zero value.
func (p *defaultsPage) row(label string) system.Default {
	for _, r := range p.rows {
		if r.Label == label {
			return r
		}
	}
	return system.Default{}
}

// app is one of the applications offered for the kind being chosen for.
func (p *defaultsPage) app(id string) system.DesktopApp {
	if p.picking == nil {
		return system.DesktopApp{}
	}
	for _, a := range p.picking.Choices {
		if a.ID == id {
			return a
		}
	}
	return system.DesktopApp{}
}

func (p *defaultsPage) fill() {
	if p.picking != nil {
		p.fillChoices()
		return
	}
	rows := make([]Row, 0, len(p.rows))
	for _, d := range p.rows {
		name, badge := d.App.Name, d.Origin.String()
		if name == "" {
			name, badge = "nothing opens it", ""
		} else if d.Mixed {
			badge = "mixed"
		}
		rows = append(rows, Row{
			ID:    d.Label,
			Cols:  []string{d.Label, name, ""},
			Badge: badge,
			Mark:  d.Origin == system.OriginUser,
			Muted: d.App.Name == "",
		})
	}
	p.table.Headers = []string{"opens", "with", ""}
	// A MIME type needs the room a kind's name does not: "Pictures" is eight
	// characters, "application/vnd.oasis.opendocument.text" is thirty-nine.
	if p.all {
		p.table.Widths = []int{0, 18, 7}
	} else {
		p.table.Widths = []int{0, 26, 7}
	}
	p.table.SetRows(rows)
}

func (p *defaultsPage) fillChoices() {
	rows := make([]Row, 0, len(p.picking.Choices))
	for _, app := range p.picking.Choices {
		badge := ""
		switch {
		case app.ID == p.picking.App.ID:
			badge = "current"
		case app.Flatpak:
			badge = "flatpak"
		}
		rows = append(rows, Row{
			ID:    app.ID,
			Cols:  []string{app.Name, strings.TrimSuffix(app.ID, ".desktop"), ""},
			Badge: badge,
			Mark:  app.ID == p.picking.App.ID,
		})
	}
	p.table.Headers = []string{"application", "desktop entry", ""}
	p.table.Widths = []int{0, 24, 8}
	p.table.SetRows(rows)
}

// summary is the line under the heading: how much of this machine somebody
// has actually decided.
func (p *defaultsPage) summary() string {
	mine, none := 0, 0
	for _, d := range p.rows {
		switch {
		case d.Origin == system.OriginUser:
			mine++
		case d.App.Name == "" || d.Origin == system.OriginNone:
			none++
		}
	}
	out := itoa(mine) + " of " + itoa(len(p.rows)) + " set by you"
	if none > 0 {
		out += ", " + itoa(none) + " left to whatever claims the type first"
	}
	return out
}

func (p *defaultsPage) View(m *Model, width, height int) string {
	if p.loading {
		return m.spinner() + m.Styles.Dim.Render(" reading desktop entries…")
	}
	list := p.table.View(m.Styles, m.Glyphs, m.listWidth(width), height)
	return m.split(width, height, list, p.detail(m))
}

func (p *defaultsPage) detail(m *Model) string {
	row, ok := p.table.Current()
	if !ok {
		return ""
	}
	if p.picking != nil {
		app := p.app(row.ID)
		out := app.Name + "\n" + strings.TrimSuffix(app.ID, ".desktop") + "\n\n"
		if app.Comment != "" {
			out += app.Comment + "\n\n"
		}
		if app.Flatpak {
			out += "Installed as a Flatpak.\n\n"
		}
		var declared []string
		for _, t := range p.picking.Types {
			if app.Declares(t) {
				declared = append(declared, t)
			}
		}
		out += "Opens " + plural(len(declared), "type", "types") + " of the " +
			itoa(len(p.picking.Types)) + " in " + p.picking.Label
		if len(declared) > 0 {
			out += ": " + strings.Join(declared, ", ")
		}
		out += ".\n\nenter makes it the default for all of them; esc goes back."
		return out
	}

	d := p.row(row.ID)
	out := d.Label + "\n" + strings.Join(d.Types, ", ") + "\n\n"
	switch {
	case d.App.Name == "":
		out += "Nothing installed says it opens this.\n\n"
	case d.Origin == system.OriginUser:
		out += "Opens with " + d.App.Name + ", because you said so.\nWritten in " + short(system.MimeappsPath()) + "\n\n"
	case d.Origin == system.OriginSystem:
		out += "Opens with " + d.App.Name + ", from the system list.\nNo choice of your own is set for it.\n\n"
	default:
		out += "Opens with " + d.App.Name + " -- but nothing decided that: no " +
			"default is set, so the first application that claims the type wins, " +
			"and that order can change when packages do.\n\n"
	}
	if d.Mixed {
		out += "These types do not agree with each other:\n"
		for _, each := range d.Each {
			out += "  " + each.Type + " -> " + each.App.Name + "\n"
		}
		out += "\nChoosing one application settles all of them.\n\n"
	}
	out += plural(len(d.Choices), "application", "applications") + " can open it.\n\n"
	out += "Choices are written to " + short(system.MimeappsPath()) + " -- your own file, " +
		"so nothing here needs a password and nothing changes for anybody else on " +
		"the machine. A type the application does not declare is added to its " +
		"associations as well, so the choice holds."
	return out
}

func (p *defaultsPage) Help(m *Model) (nav, actions []Binding) {
	if p.picking != nil {
		return []Binding{{m.Glyphs.UpDown, "application"}, {"/", "filter"}, {"esc", "back"}},
			[]Binding{{"enter", "make default"}}
	}
	all := "all types"
	if p.all {
		all = "common kinds"
	}
	return []Binding{{m.Glyphs.UpDown, "kind"}, {"/", "filter"}, {"a", all}},
		[]Binding{{"enter", "choose"}, {"x", "clear"}}
}

// short writes a path under the home directory the way people say it.
func short(path string) string {
	if home := os.Getenv("HOME"); home != "" && strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}
