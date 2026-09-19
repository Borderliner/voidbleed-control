package system

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DesktopApp is an installed application that can open files: one .desktop
// entry, from anywhere the desktop file specification says to look -- Flatpak
// exports included, which is where half of a modern machine's applications
// live.
type DesktopApp struct {
	ID      string // "org.gnome.gThumb.desktop"
	Name    string
	Comment string
	Types   []string // the MIME types it declares
	Flatpak bool
	Hidden  bool // NoDisplay or Hidden: a handler, but not one to offer
}

// Declares reports whether the application says it opens this type.
func (a DesktopApp) Declares(mime string) bool {
	for _, t := range a.Types {
		if t == mime {
			return true
		}
	}
	return false
}

// FileKind is one row of the page: a familiar name for the bundle of MIME
// types people think of as one thing. Nobody wants to set image/x-tga; they
// want pictures to open in the picture viewer.
type FileKind struct {
	Group string
	Label string
	Types []string
}

// Origin says where a default came from, which is the difference between a
// choice someone made and whatever the machine fell back to.
type Origin int

const (
	OriginNone   Origin = iota // nothing is set: the first match would win
	OriginUser                 // ~/.config/mimeapps.list -- this program's doing
	OriginSystem               // /etc/xdg, or a package's own list
)

func (o Origin) String() string {
	switch o {
	case OriginUser:
		return "yours"
	case OriginSystem:
		return "system"
	}
	return "unset"
}

// TypeHandler is what opens one MIME type, which is what a kind is made of.
type TypeHandler struct {
	Type   string
	App    DesktopApp
	Origin Origin
}

// Default is a kind of file together with what opens it today.
type Default struct {
	FileKind
	App     DesktopApp // what opens the kind; the zero value when nothing can
	Origin  Origin
	Mixed   bool // the types in this kind do not all open in the same thing
	Each    []TypeHandler
	Choices []DesktopApp
}

// Kinds is what the page lists: the file kinds a desktop actually has an
// opinion about, in the order they are shown. Every type here is one some
// installed application is likely to claim -- the list is deliberately short,
// because the point is to be read at a glance, not to be exhaustive. The
// whole set of types on the machine is one keypress away.
var Kinds = []FileKind{
	{"Internet", "Web pages", []string{"text/html", "x-scheme-handler/http", "x-scheme-handler/https"}},
	{"Internet", "Email links", []string{"x-scheme-handler/mailto"}},
	{"Internet", "Terminal", []string{"x-scheme-handler/terminal"}},
	{"Files", "Folders", []string{"inode/directory"}},
	{"Files", "Archives", []string{
		"application/zip", "application/x-7z-compressed", "application/vnd.rar",
		"application/x-rar-compressed", "application/x-tar", "application/gzip",
		"application/x-compressed-tar", "application/x-xz-compressed-tar",
		"application/x-bzip-compressed-tar",
	}},
	{"Documents", "PDF", []string{"application/pdf"}},
	{"Documents", "Plain text", []string{"text/plain"}},
	{"Documents", "Markdown", []string{"text/markdown"}},
	{"Documents", "Word documents", []string{
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/msword", "application/vnd.oasis.opendocument.text", "application/rtf",
	}},
	{"Documents", "Spreadsheets", []string{
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/vnd.ms-excel", "application/vnd.oasis.opendocument.spreadsheet", "text/csv",
	}},
	{"Documents", "Presentations", []string{
		"application/vnd.openxmlformats-officedocument.presentationml.presentation",
		"application/vnd.ms-powerpoint", "application/vnd.oasis.opendocument.presentation",
	}},
	{"Documents", "E-books", []string{"application/epub+zip", "application/x-mobipocket-ebook"}},
	{"Media", "Pictures", []string{
		"image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp",
		"image/tiff", "image/x-png", "image/x-bmp", "image/avif",
	}},
	{"Media", "Drawings", []string{"image/svg+xml"}},
	{"Media", "Music", []string{
		"audio/mpeg", "audio/flac", "audio/ogg", "audio/x-vorbis+ogg",
		"audio/x-wav", "audio/mp4", "audio/aac", "audio/x-opus+ogg",
	}},
	{"Media", "Video", []string{
		"video/mp4", "video/x-matroska", "video/webm", "video/quicktime",
		"video/x-msvideo", "video/mpeg",
	}},
	{"Code", "Code and config", []string{
		"application/json", "application/xml", "text/xml",
		"text/x-shellscript", "text/x-python", "text/x-csrc",
	}},
}

// Defaults is every kind with what opens it, ready to be listed.
func (c *Client) Defaults(ctx context.Context) []Default {
	apps := c.DesktopApps()
	set := c.mimeapps()
	out := make([]Default, 0, len(Kinds))
	for _, kind := range Kinds {
		out = append(out, resolveKind(kind, apps, set))
	}
	return out
}

// EveryType is the same list drawn from the machine instead of from Kinds:
// one row per MIME type some installed application declares. It is what the
// page shows when the curated list does not go far enough.
func (c *Client) EveryType(ctx context.Context) []Default {
	apps := c.DesktopApps()
	set := c.mimeapps()
	seen := map[string]bool{}
	var types []string
	for _, app := range apps {
		for _, t := range app.Types {
			if !seen[t] {
				seen[t] = true
				types = append(types, t)
			}
		}
	}
	sort.Strings(types)
	out := make([]Default, 0, len(types))
	for _, t := range types {
		out = append(out, resolveKind(FileKind{Group: group(t), Label: t, Types: []string{t}}, apps, set))
	}
	return out
}

// group is the part of a MIME type before the slash, which is as much of a
// grouping as an arbitrary type gives us.
func group(mime string) string {
	head, _, _ := strings.Cut(mime, "/")
	return head
}

func resolveKind(kind FileKind, apps []DesktopApp, set mimeapps) Default {
	d := Default{FileKind: kind}
	byID := map[string]DesktopApp{}
	for _, app := range apps {
		byID[app.ID] = app
	}
	// What opens each type in the kind, and whether they agree. A kind whose
	// types disagree is worth saying out loud rather than papering over: it
	// is exactly the state a machine ends up in after years of "open with".
	first := true
	for _, t := range kind.Types {
		id, origin := set.defaultFor(t, byID)
		app, known := byID[id], true
		if id == "" {
			app, known = firstMatch(apps, t)
			origin = OriginNone
		}
		if !known {
			continue // nothing installed opens this type
		}
		d.Each = append(d.Each, TypeHandler{Type: t, App: app, Origin: origin})
		switch {
		case first:
			// The first type in a kind is the one it is named after, so its
			// handler is the one the row shows.
			d.App, d.Origin, first = app, origin, false
		case app.ID != d.App.ID:
			d.Mixed = true
		}
	}
	for _, app := range apps {
		if app.Hidden {
			continue
		}
		for _, t := range kind.Types {
			if app.Declares(t) {
				d.Choices = append(d.Choices, app)
				break
			}
		}
	}
	sort.Slice(d.Choices, func(i, j int) bool { return d.Choices[i].Name < d.Choices[j].Name })
	return d
}

// firstMatch is what happens with no default set: some application that
// claims the type opens it, and which one is not something anybody decided.
func firstMatch(apps []DesktopApp, mime string) (DesktopApp, bool) {
	var found []DesktopApp
	for _, app := range apps {
		if !app.Hidden && app.Declares(mime) {
			found = append(found, app)
		}
	}
	if len(found) == 0 {
		return DesktopApp{}, false
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Name < found[j].Name })
	return found[0], true
}

// SetDefault makes an application the one that opens every type in a kind.
// It writes only ~/.config/mimeapps.list: a user's own choice, no password,
// and nothing that changes the machine for anybody else.
func (c *Client) SetDefault(kind FileKind, app DesktopApp) error {
	if c.Demo {
		return nil
	}
	path := MimeappsPath()
	ini := readINI(path)
	defaults := copyOf(ini["Default Applications"])
	added := copyOf(ini["Added Associations"])
	for _, t := range kind.Types {
		defaults[t] = app.ID
		// A type the entry does not declare still opens with it once the
		// association is written down; without this, some file managers
		// refuse the default they were just given.
		if !app.Declares(t) {
			added[t] = prependID(added[t], app.ID)
		}
	}
	if err := writeINI(path, "Default Applications", defaults); err != nil {
		return err
	}
	return writeINI(path, "Added Associations", added)
}

// ClearDefault drops this machine's own choice for a kind, which hands the
// decision back to the system list, or to whatever claims the type first.
func (c *Client) ClearDefault(kind FileKind) error {
	if c.Demo {
		return nil
	}
	path := MimeappsPath()
	ini := readINI(path)
	defaults := copyOf(ini["Default Applications"])
	for _, t := range kind.Types {
		// writeINI drops a key whose value is empty.
		defaults[t] = ""
	}
	return writeINI(path, "Default Applications", defaults)
}

// MimeappsPath is the file the choices are written to.
func MimeappsPath() string { return filepath.Join(configHome(), "mimeapps.list") }

func copyOf(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

// prependID puts an application at the front of a semicolon list, without
// letting it appear twice.
func prependID(list, id string) string {
	out := []string{id}
	for _, part := range strings.Split(list, ";") {
		if part = strings.TrimSpace(part); part != "" && part != id {
			out = append(out, part)
		}
	}
	return strings.Join(out, ";") + ";"
}

// mimeapps is the chain of mimeapps.list files, nearest first.
type mimeapps []struct {
	defaults map[string]string
	origin   Origin
}

// defaultFor walks the chain the way the specification says to: the first
// file that names an application which is actually installed decides it.
func (set mimeapps) defaultFor(mime string, installed map[string]DesktopApp) (string, Origin) {
	for _, file := range set {
		for _, id := range strings.Split(file.defaults[mime], ";") {
			if id = strings.TrimSpace(id); id == "" {
				continue
			}
			if _, ok := installed[id]; ok {
				return id, file.origin
			}
		}
	}
	return "", OriginNone
}

// mimeapps is the chain this machine resolves with -- or, in demo mode, a
// made-up one, so the demo reads no file of yours at all.
func (c *Client) mimeapps() mimeapps {
	if c.Demo {
		return demoMimeapps()
	}
	return readMimeapps()
}

func readMimeapps() mimeapps {
	var set mimeapps
	add := func(path string, origin Origin) {
		ini := readINI(path)
		if len(ini["Default Applications"]) == 0 {
			return
		}
		set = append(set, struct {
			defaults map[string]string
			origin   Origin
		}{ini["Default Applications"], origin})
	}
	add(MimeappsPath(), OriginUser)
	for _, dir := range configDirs() {
		add(filepath.Join(dir, "mimeapps.list"), OriginSystem)
	}
	add(filepath.Join(dataHome(), "applications/mimeapps.list"), OriginUser)
	for _, dir := range dataDirs() {
		add(filepath.Join(dir, "applications/mimeapps.list"), OriginSystem)
	}
	return set
}

// DesktopApps reads every application entry on the machine, nearest
// directory first so a user's own copy of an entry wins.
func (c *Client) DesktopApps() []DesktopApp {
	if c.Demo {
		return demoApps()
	}
	var apps []DesktopApp
	seen := map[string]bool{}
	for _, dir := range applicationDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".desktop") || seen[name] {
				continue
			}
			app, ok := readDesktopEntry(filepath.Join(dir, name))
			if !ok {
				continue
			}
			seen[name] = true
			app.Flatpak = strings.Contains(dir, "/flatpak/")
			apps = append(apps, app)
		}
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	return apps
}

// applicationDirs is where desktop entries live, in the order the
// specification gives them. Flatpak's export directories are named outright:
// they are usually in XDG_DATA_DIRS, but a terminal started before the first
// Flatpak was installed does not have them. It is a variable so tests can
// describe a machine they are not running on.
var applicationDirs = func() []string {
	dirs := []string{filepath.Join(dataHome(), "applications")}
	for _, dir := range dataDirs() {
		dirs = append(dirs, filepath.Join(dir, "applications"))
	}
	dirs = append(dirs,
		filepath.Join(dataHome(), "flatpak/exports/share/applications"),
		"/var/lib/flatpak/exports/share/applications",
	)
	return unique(dirs)
}

func configDirs() []string {
	if dirs := os.Getenv("XDG_CONFIG_DIRS"); dirs != "" {
		return strings.Split(dirs, ":")
	}
	return []string{"/etc/xdg"}
}

func dataDirs() []string {
	if dirs := os.Getenv("XDG_DATA_DIRS"); dirs != "" {
		return strings.Split(dirs, ":")
	}
	return []string{"/usr/local/share", "/usr/share"}
}

func readDesktopEntry(path string) (DesktopApp, bool) {
	ini := readINI(path)
	entry, ok := ini["Desktop Entry"]
	if !ok || entry["Type"] != "Application" {
		return DesktopApp{}, false
	}
	app := DesktopApp{
		ID:      filepath.Base(path),
		Name:    entry["Name"],
		Comment: entry["Comment"],
		Hidden:  entry["NoDisplay"] == "true" || entry["Hidden"] == "true",
	}
	if app.Name == "" {
		app.Name = strings.TrimSuffix(app.ID, ".desktop")
	}
	for _, t := range strings.Split(entry["MimeType"], ";") {
		if t = strings.TrimSpace(t); t != "" {
			app.Types = append(app.Types, t)
		}
	}
	return app, true
}

// demoMimeapps is the demo machine's idea of what has been decided: a couple
// of choices of its own, a couple from the system, and the rest left open --
// the three states the page has something to say about.
func demoMimeapps() mimeapps {
	return mimeapps{
		{defaults: map[string]string{
			"text/plain":    "org.xfce.mousepad.desktop",
			"text/markdown": "org.xfce.mousepad.desktop",
			"image/png":     "org.gnome.gThumb.desktop",
			"image/jpeg":    "org.gimp.GIMP.desktop",
		}, origin: OriginUser},
		{defaults: map[string]string{
			"inode/directory":           "thunar.desktop",
			"text/html":                 "firefox.desktop",
			"x-scheme-handler/http":     "firefox.desktop",
			"x-scheme-handler/https":    "firefox.desktop",
			"application/pdf":           "org.gnome.Papers.desktop",
			"x-scheme-handler/terminal": "com.mitchellh.ghostty.desktop",
		}, origin: OriginSystem},
	}
}

// demoApps is the machine the demo pretends to be: enough applications for
// the choices to be worth making, and not one of them real.
func demoApps() []DesktopApp {
	return []DesktopApp{
		{ID: "firefox.desktop", Name: "Firefox", Comment: "Browse the web",
			Types: []string{"text/html", "x-scheme-handler/http", "x-scheme-handler/https", "application/pdf", "image/svg+xml"}},
		{ID: "org.gnome.Papers.desktop", Name: "Papers", Comment: "Read documents", Flatpak: true,
			Types: []string{"application/pdf", "application/epub+zip"}},
		{ID: "org.gnome.gThumb.desktop", Name: "gThumb", Comment: "View and organise images", Flatpak: true,
			Types: []string{"image/png", "image/jpeg", "image/gif", "image/webp", "image/svg+xml"}},
		{ID: "org.gimp.GIMP.desktop", Name: "GNU Image Manipulation Program", Comment: "Edit images", Flatpak: true,
			Types: []string{"image/png", "image/jpeg", "image/tiff", "image/x-xcf"}},
		{ID: "mpv.desktop", Name: "mpv", Comment: "Play media",
			Types: []string{"video/mp4", "video/x-matroska", "video/webm", "audio/mpeg", "audio/flac"}},
		{ID: "org.xfce.mousepad.desktop", Name: "Mousepad", Comment: "Edit text",
			Types: []string{"text/plain", "text/markdown", "application/json", "application/xml"}},
		{ID: "thunar.desktop", Name: "Thunar", Comment: "Browse the file system",
			Types: []string{"inode/directory"}},
		{ID: "engrampa.desktop", Name: "Engrampa", Comment: "Open and unpack archives",
			Types: []string{"application/zip", "application/x-7z-compressed", "application/x-tar", "application/gzip"}},
		{ID: "com.mitchellh.ghostty.desktop", Name: "Ghostty", Comment: "A terminal",
			Types: []string{"x-scheme-handler/terminal"}},
		{ID: "org.onlyoffice.desktopeditors.desktop", Name: "ONLYOFFICE Desktop Editors", Flatpak: true,
			Comment: "Edit documents, spreadsheets and presentations",
			Types: []string{
				"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
				"application/msword", "application/pdf", "text/csv",
				"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			}},
	}
}
