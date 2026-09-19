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
	// Categories is what kind of program it says it is. Terminals are the
	// reason it is read: not one of them declares x-scheme-handler/terminal,
	// so the only way to offer a terminal is to know one when we see it.
	Categories []string
	Flatpak    bool
	Hidden     bool // NoDisplay or Hidden: a handler, but not one to offer
}

// InCategory reports whether the entry puts itself in this category.
func (a DesktopApp) InCategory(name string) bool {
	for _, c := range a.Categories {
		if c == name {
			return true
		}
	}
	return false
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
	// Category widens the list of applications offered for this kind to a
	// class of program, for the types nothing declares.
	Category string
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
	{Group: "Internet", Label: "Web pages", Types: []string{"text/html", "x-scheme-handler/http", "x-scheme-handler/https"}},
	{Group: "Internet", Label: "Email links", Types: []string{"x-scheme-handler/mailto"}},
	// No terminal declares the scheme it is supposed to answer to, so the
	// list of terminals is where this one's choices come from.
	{Group: "Internet", Label: "Terminal", Types: []string{"x-scheme-handler/terminal"}, Category: "TerminalEmulator"},
	{Group: "Files", Label: "Folders", Types: []string{"inode/directory"}, Category: "FileManager"},
	{Group: "Files", Label: "Archives", Types: []string{
		"application/zip", "application/x-7z-compressed", "application/vnd.rar",
		"application/x-rar-compressed", "application/x-tar", "application/gzip",
		"application/x-compressed-tar", "application/x-xz-compressed-tar",
		"application/x-bzip-compressed-tar",
	}},
	{Group: "Documents", Label: "PDF", Types: []string{"application/pdf"}},
	{Group: "Documents", Label: "Plain text", Types: []string{"text/plain"}},
	{Group: "Documents", Label: "Markdown", Types: []string{"text/markdown"}},
	{Group: "Documents", Label: "Word documents", Types: []string{
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/msword", "application/vnd.oasis.opendocument.text", "application/rtf",
	}},
	{Group: "Documents", Label: "Spreadsheets", Types: []string{
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/vnd.ms-excel", "application/vnd.oasis.opendocument.spreadsheet", "text/csv",
	}},
	{Group: "Documents", Label: "Presentations", Types: []string{
		"application/vnd.openxmlformats-officedocument.presentationml.presentation",
		"application/vnd.ms-powerpoint", "application/vnd.oasis.opendocument.presentation",
	}},
	{Group: "Documents", Label: "E-books", Types: []string{"application/epub+zip", "application/x-mobipocket-ebook"}},
	{Group: "Media", Label: "Pictures", Types: []string{
		"image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp",
		"image/tiff", "image/x-png", "image/x-bmp", "image/avif",
	}},
	{Group: "Media", Label: "Drawings", Types: []string{"image/svg+xml"}},
	{Group: "Media", Label: "Music", Types: []string{
		"audio/mpeg", "audio/flac", "audio/ogg", "audio/x-vorbis+ogg",
		"audio/x-wav", "audio/mp4", "audio/aac", "audio/x-opus+ogg",
	}},
	{Group: "Media", Label: "Video", Types: []string{
		"video/mp4", "video/x-matroska", "video/webm", "video/quicktime",
		"video/x-msvideo", "video/mpeg",
	}},
	{Group: "Code", Label: "Code and config", Types: []string{
		"application/json", "application/xml", "text/xml",
		"text/x-shellscript", "text/x-python", "text/x-csrc",
	}},
}

// Defaults is every kind with what opens it, ready to be listed.
func (c *Client) Defaults(ctx context.Context) []Default {
	apps, set, registered := c.DesktopApps(), c.mimeapps(), c.registered()
	out := make([]Default, 0, len(Kinds))
	for _, kind := range Kinds {
		out = append(out, resolveKind(kind, apps, set, registered))
	}
	return out
}

// EveryType is the same list drawn from the machine instead of from Kinds:
// one row per MIME type some installed application declares. It is what the
// page shows when the curated list does not go far enough.
func (c *Client) EveryType(ctx context.Context) []Default {
	apps, set, registered := c.DesktopApps(), c.mimeapps(), c.registered()
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
		out = append(out, resolveKind(FileKind{Group: group(t), Label: t, Types: []string{t}}, apps, set, registered))
	}
	return out
}

// group is the part of a MIME type before the slash, which is as much of a
// grouping as an arbitrary type gives us.
func group(mime string) string {
	head, _, _ := strings.Cut(mime, "/")
	return head
}

func resolveKind(kind FileKind, apps []DesktopApp, set mimeapps, registered map[string][]string) Default {
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
			app, known = firstMatch(apps, byID, registered[t], t)
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
		if kind.Category != "" && app.InCategory(kind.Category) {
			d.Choices = append(d.Choices, app)
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

// firstMatch is what happens with no default set: the first application in
// the registered list opens it, and nobody decided that. The order comes from
// mimeinfo.cache, which is the order the desktop itself walks -- guessing
// alphabetically instead is how a page ends up naming an application that
// never opens anything.
func firstMatch(apps []DesktopApp, byID map[string]DesktopApp, registered []string, mime string) (DesktopApp, bool) {
	for _, id := range registered {
		if app, ok := byID[id]; ok && !app.Hidden {
			return app, true
		}
	}
	// Nothing in the cache: fall back to whatever declares it, in a stable
	// order, so the page still has something to say.
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

// registered reads the mimeinfo.cache files that update-desktop-database
// keeps beside the desktop entries: type to applications, in the order that
// decides what opens a type nobody has chosen for.
func (c *Client) registered() map[string][]string {
	if c.Demo {
		return map[string][]string{}
	}
	out := map[string][]string{}
	for _, dir := range applicationDirs() {
		for mime, list := range readINI(filepath.Join(dir, "mimeinfo.cache"))["MIME Cache"] {
			for _, id := range strings.Split(list, ";") {
				if id = strings.TrimSpace(id); id != "" {
					out[mime] = append(out[mime], id)
				}
			}
		}
	}
	return out
}

// SetDefault makes an application the one that opens every type in a kind.
// It writes only ~/.config/mimeapps.list: a user's own choice, no password,
// and nothing that changes the machine for anybody else.
func (c *Client) SetDefault(kind FileKind, app DesktopApp) error {
	if c.Demo {
		return nil
	}
	sections := readINI(MimeappsPath())
	defaults, added := section(sections, "Default Applications"), section(sections, "Added Associations")
	for _, t := range kind.Types {
		defaults[t] = app.ID
		// A type the entry does not declare still opens with it once the
		// association is written down; without this, some file managers
		// refuse the default they were just given.
		if !app.Declares(t) {
			added[t] = prependID(added[t], app.ID)
		}
	}
	return writeMimeapps(MimeappsPath(), sections)
}

// ClearDefault drops this machine's own choice for a kind, which hands the
// decision back to the system list, or to whatever claims the type first.
func (c *Client) ClearDefault(kind FileKind) error {
	if c.Demo {
		return nil
	}
	sections := readINI(MimeappsPath())
	defaults := section(sections, "Default Applications")
	for _, t := range kind.Types {
		delete(defaults, t)
	}
	return writeMimeapps(MimeappsPath(), sections)
}

// MimeappsGroups counts how often each group heading appears in the file. A
// key file with a group in it twice is one glib refuses to read, and a
// machine whose mimeapps.list is in that state has no defaults at all as far
// as the desktop is concerned -- worth saying out loud, and worth a key that
// puts it right.
func MimeappsGroups(path string) map[string]int {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	out := map[string]int{}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			out[strings.Trim(line, "[]")]++
		}
	}
	return out
}

// MimeappsBroken reports whether the file has a group in it more than once.
func MimeappsBroken() bool {
	for _, n := range MimeappsGroups(MimeappsPath()) {
		if n > 1 {
			return true
		}
	}
	return false
}

// RepairMimeapps writes the file back as one group per heading, keeping every
// association in it.
func (c *Client) RepairMimeapps() error {
	if c.Demo {
		return nil
	}
	return writeMimeapps(MimeappsPath(), readINI(MimeappsPath()))
}

// writeMimeapps writes the whole file from the sections it was given, rather
// than editing lines in place: it is the only way to leave a file that
// already has a group in it twice in a state glib will read.
func writeMimeapps(path string, sections map[string]map[string]string) error {
	order := []string{"Default Applications", "Added Associations", "Removed Associations"}
	var rest []string
	for name := range sections {
		if name != "" && !contains(order, name) {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)

	var b strings.Builder
	for _, name := range append(order, rest...) {
		values := sections[name]
		if len(values) == 0 && name != "Default Applications" {
			continue // an empty group is noise, and one glib need not read
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("[" + name + "]\n")
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			b.WriteString(key + "=" + values[key] + "\n")
		}
	}
	return writeFile(path, b.String())
}

func section(sections map[string]map[string]string, name string) map[string]string {
	if sections[name] == nil {
		sections[name] = map[string]string{}
	}
	return sections[name]
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// MimeappsPath is the file the choices are written to.
func MimeappsPath() string { return filepath.Join(configHome(), "mimeapps.list") }

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
	for _, c := range strings.Split(entry["Categories"], ";") {
		if c = strings.TrimSpace(c); c != "" {
			app.Categories = append(app.Categories, c)
		}
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
			"inode/directory":        "thunar.desktop",
			"text/html":              "firefox.desktop",
			"x-scheme-handler/http":  "firefox.desktop",
			"x-scheme-handler/https": "firefox.desktop",
			"application/pdf":        "org.gnome.Papers.desktop",
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
		// No terminal declares the scheme it is meant to answer to; the
		// category is the only thing that says what it is.
		{ID: "com.mitchellh.ghostty.desktop", Name: "Ghostty", Comment: "A terminal",
			Categories: []string{"System", "TerminalEmulator"}},
		{ID: "org.onlyoffice.desktopeditors.desktop", Name: "ONLYOFFICE Desktop Editors", Flatpak: true,
			Comment: "Edit documents, spreadsheets and presentations",
			Types: []string{
				"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
				"application/msword", "application/pdf", "text/csv",
				"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			}},
	}
}
