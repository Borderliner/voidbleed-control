package system

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Borderliner/voidbleed-control/internal/sys"
)

// fakeRunner answers from a table of canned output and records what it was
// asked to run.
type fakeRunner struct {
	out  map[string]string
	seen []string
}

func (f *fakeRunner) Run(ctx context.Context, cmd sys.Cmd) error {
	f.seen = append(f.seen, cmd.String())
	return nil
}

func (f *fakeRunner) Output(ctx context.Context, cmd sys.Cmd) (string, error) {
	line := cmd.String()
	f.seen = append(f.seen, line)
	for match, out := range f.out {
		if strings.Contains(line, match) {
			return out, nil
		}
	}
	return "", nil
}

func (f *fakeRunner) WriteFile(string, []byte, fs.FileMode) error { return nil }
func (f *fakeRunner) AppendFile(string, []byte) error             { return nil }
func (f *fakeRunner) ReadFile(string) ([]byte, error)             { return nil, nil }
func (f *fakeRunner) MkdirAll(string, fs.FileMode) error          { return nil }
func (f *fakeRunner) Symlink(string, string) error                { return nil }
func (f *fakeRunner) RemoveAll(string) error                      { return nil }
func (f *fakeRunner) Exists(string) bool                          { return false }
func (f *fakeRunner) Glob(string) ([]string, error)               { return nil, nil }

func TestDisableStopsBeforeUnlinking(t *testing.T) {
	cmd := DisableCmd("iptables").String()
	down, unlink := strings.Index(cmd, "sv down"), strings.Index(cmd, "rm -f")
	if down < 0 || unlink < 0 {
		t.Fatalf("disable does not both stop and unlink: %s", cmd)
	}
	if down > unlink {
		t.Errorf("disable unlinks before stopping, which cannot work:\n%s", cmd)
	}
	if !strings.Contains(cmd, "|| true") {
		t.Errorf("stopping a service that is already down must not fail the action:\n%s", cmd)
	}
}

func TestParseServiceStatus(t *testing.T) {
	for _, tc := range []struct {
		line       string
		name, want string
		pid        string
	}{
		{"run: /var/service/dbus: (pid 1204) 84231s", "dbus", "run", "1204"},
		{"down: /var/service/cupsd: 12s, normally up", "cupsd", "down", ""},
	} {
		got, ok := parseStatus(tc.line)
		if !ok {
			t.Fatalf("could not read %q", tc.line)
		}
		if got.Name != tc.name || got.State != tc.want || got.PID != tc.pid {
			t.Errorf("%q read as %+v", tc.line, got)
		}
	}
}

func TestFirewallWithoutUfwOffersNothingToRun(t *testing.T) {
	restore := Have
	Have = func(name string) bool { return name == "iptables" }
	defer func() { Have = restore }()

	runner := &fakeRunner{}
	fw := NewWith(runner, 0).FirewallState(context.Background())
	if fw.Installed {
		t.Error("ufw is not installed, so the page must not think it can drive one")
	}
	if len(fw.Others) != 1 || fw.Others[0] != "iptables" {
		t.Errorf("iptables should be reported: %+v", fw.Others)
	}
	for _, line := range runner.seen {
		if strings.Contains(line, "ufw") {
			t.Errorf("ran a ufw command on a machine without ufw: %s", line)
		}
	}
}
func TestParseUfwStatus(t *testing.T) {
	var fw Firewall
	parseUfw(`Status: active
Default: deny (incoming), allow (outgoing), disabled (routed)

To                         Action      From
--                         ------      ----
[ 1] 22/tcp                ALLOW IN    Anywhere
`, &fw)
	if !fw.Active {
		t.Error("an active firewall was read as inactive")
	}
	if len(fw.Rules) != 1 || fw.Rules[0].Number != "1" || fw.Rules[0].To != "22/tcp" {
		t.Errorf("rules read as %+v", fw.Rules)
	}
}

func TestInstalledPackages(t *testing.T) {
	runner := &fakeRunner{out: map[string]string{
		"xbps-query -l": "ii btop-1.4.7_1   Monitor of resources\nii niri-26.04_1   Compositor\n",
		"xbps-query -m": "btop-1.4.7_1\n",
		"xbps-query -O": "niri-26.04_1\n",
	}}
	pkgs, err := NewWith(runner, 1000).Installed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("got %d packages", len(pkgs))
	}
	btop := pkgs[0]
	if btop.Name != "btop" || btop.Version != "1.4.7_1" || !btop.Manual || btop.Orphan {
		t.Errorf("btop read as %+v", btop)
	}
	if !pkgs[1].Orphan {
		t.Errorf("niri should be marked an orphan: %+v", pkgs[1])
	}
}
func TestUpdateCheckDoesNotNeedRoot(t *testing.T) {
	runner := &fakeRunner{out: map[string]string{
		"xbps-install": "niri-26.05_1 update x86_64 https://repo 9437184 3145728\n",
	}}
	c := NewWith(runner, 1000)
	updates, err := c.Updates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || updates[0].Name != "niri" || updates[0].NewVersion != "26.05_1" {
		t.Errorf("updates read as %+v", updates)
	}
	for _, line := range runner.seen {
		if strings.Contains(line, "xbps-install") && !strings.Contains(line, "-M") {
			t.Errorf("update check would write to /var/db/xbps: %s", line)
		}
		if strings.Contains(line, "sudo") {
			t.Errorf("update check asked for root: %s", line)
		}
	}
}

// "Keep this" is a flag in the package database, not an install.
func TestKeepIsAFlagNotAnInstall(t *testing.T) {
	cmd := MarkManualCmd("dejavu-fonts-ttf").String()
	if cmd != "xbps-pkgdb -m manual dejavu-fonts-ttf" {
		t.Errorf("keep runs %q", cmd)
	}
	if MarkAutoCmd("x").String() != "xbps-pkgdb -m auto x" {
		t.Errorf("the other direction runs %q", MarkAutoCmd("x").String())
	}
}

// Kernel series are numbers: 6.6 is older than 6.18, which sorting them as
// words gets backwards.
func TestKernelSeriesSortAsNumbers(t *testing.T) {
	kernels := []Kernel{{Series: "6.6"}, {Series: "6.18"}, {Series: "5.15"}, {Series: "6.1"}}
	sortKernels(kernels)
	var got []string
	for _, k := range kernels {
		got = append(got, k.Series)
	}
	want := []string{"6.18", "6.6", "6.1", "5.15"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("sorted %v, want %v", got, want)
	}
}

func TestKernelInstallTakesHeadersWhenAsked(t *testing.T) {
	k := Kernel{Package: "linux6.6"}
	if got := KernelInstallCmd(k, false).String(); got != "xbps-install -Sy linux6.6" {
		t.Errorf("install runs %q", got)
	}
	if got := KernelInstallCmd(k, true).String(); got != "xbps-install -Sy linux6.6 linux6.6-headers" {
		t.Errorf("install with headers runs %q", got)
	}
}

// A machine with a choice of its own keeps it; a type nobody decided is
// reported as undecided, however confidently something opens it.
func TestDefaultsSayWhoDecided(t *testing.T) {
	home := fakeDesktop(t)
	os.WriteFile(filepath.Join(home, "config/mimeapps.list"),
		[]byte("[Default Applications]\napplication/pdf=papers.desktop\n"), 0o644)
	os.WriteFile(filepath.Join(home, "xdg/mimeapps.list"),
		[]byte("[Default Applications]\ninode/directory=thunar.desktop\n"), 0o644)

	byLabel := map[string]Default{}
	for _, d := range (&Client{}).Defaults(context.Background()) {
		byLabel[d.Label] = d
	}
	if got := byLabel["PDF"]; got.App.Name != "Papers" || got.Origin != OriginUser {
		t.Errorf("PDF: got %q from %v, want Papers from the user's file", got.App.Name, got.Origin)
	}
	if got := byLabel["Folders"]; got.App.Name != "Thunar" || got.Origin != OriginSystem {
		t.Errorf("Folders: got %q from %v, want Thunar from the system list", got.App.Name, got.Origin)
	}
	// Nothing pins pictures, so something opens them and nothing decided it.
	if got := byLabel["Pictures"]; got.Origin != OriginNone || got.App.Name == "" {
		t.Errorf("Pictures: got %q from %v, want an application and no decision", got.App.Name, got.Origin)
	}
}

// Setting a default sets every type the kind stands for, not just the one it
// is named after, and says so in the file the desktop actually reads.
func TestSetDefaultCoversTheWholeKind(t *testing.T) {
	home := fakeDesktop(t)
	c := &Client{}
	pictures := kindNamed(t, "Pictures")
	if err := c.SetDefault(pictures, DesktopApp{ID: "gimp.desktop", Name: "GIMP",
		Types: []string{"image/png"}}); err != nil {
		t.Fatal(err)
	}
	ini := readINI(filepath.Join(home, "config/mimeapps.list"))
	for _, mime := range pictures.Types {
		if got := ini["Default Applications"][mime]; got != "gimp.desktop" {
			t.Errorf("%s=%q, want gimp.desktop", mime, got)
		}
	}
	// image/png is declared, image/jpeg is not: the one that is not has to be
	// associated as well, or the choice does not hold.
	if got := ini["Added Associations"]["image/jpeg"]; !strings.Contains(got, "gimp.desktop") {
		t.Errorf("image/jpeg association %q does not mention gimp.desktop", got)
	}
	if _, ok := ini["Added Associations"]["image/png"]; ok {
		t.Error("image/png was associated again, though GIMP already declares it")
	}

	if err := c.ClearDefault(pictures); err != nil {
		t.Fatal(err)
	}
	ini = readINI(filepath.Join(home, "config/mimeapps.list"))
	if got := ini["Default Applications"]["image/png"]; got != "" {
		t.Errorf("image/png is still pinned to %q after clearing", got)
	}
}

// Choices are the applications that declare the type, and the ones nobody is
// meant to see stay out of the list.
func TestChoicesLeaveOutHiddenEntries(t *testing.T) {
	fakeDesktop(t)
	for _, d := range (&Client{}).Defaults(context.Background()) {
		if d.Label != "Pictures" {
			continue
		}
		var names []string
		for _, a := range d.Choices {
			names = append(names, a.Name)
		}
		want := []string{"GIMP", "gThumb"}
		if strings.Join(names, ",") != strings.Join(want, ",") {
			t.Errorf("choices %v, want %v", names, want)
		}
	}
}

func kindNamed(t *testing.T, label string) FileKind {
	t.Helper()
	for _, k := range Kinds {
		if k.Label == label {
			return k
		}
	}
	t.Fatalf("no kind called %q", label)
	return FileKind{}
}

// fakeDesktop builds a machine out of a temporary directory: a few desktop
// entries and nowhere else to look, so the test does not depend on what the
// machine running it happens to have installed.
func fakeDesktop(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	apps := filepath.Join(home, "data/applications")
	if err := os.MkdirAll(apps, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"config", "xdg"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	entries := map[string]string{
		"papers.desktop": "[Desktop Entry]\nType=Application\nName=Papers\nMimeType=application/pdf;\n",
		"thunar.desktop": "[Desktop Entry]\nType=Application\nName=Thunar\nMimeType=inode/directory;\n",
		"gthumb.desktop": "[Desktop Entry]\nType=Application\nName=gThumb\nMimeType=image/png;image/jpeg;image/gif;\n",
		"gimp.desktop":   "[Desktop Entry]\nType=Application\nName=GIMP\nMimeType=image/png;\n",
		"import.desktop": "[Desktop Entry]\nType=Application\nName=Import\nNoDisplay=true\nMimeType=image/png;\n",
		"broken.desktop": "[Desktop Entry]\nType=Link\nName=Not an application\n",
		// A terminal, which declares no MIME type at all -- none of them do.
		"ghostty.desktop": "[Desktop Entry]\nType=Application\nName=Ghostty\nCategories=System;TerminalEmulator;\n",
		// update-desktop-database's index, in an order the alphabet does not
		// give: gThumb before GIMP.
		"mimeinfo.cache": "[MIME Cache]\nimage/png=gthumb.desktop;gimp.desktop;\nimage/jpeg=gthumb.desktop;\n",
	}
	for name, body := range entries {
		if err := os.WriteFile(filepath.Join(apps, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_DIRS", filepath.Join(home, "xdg"))
	t.Setenv("XDG_DATA_DIRS", filepath.Join(home, "empty"))
	dirs := applicationDirs
	applicationDirs = func() []string { return []string{apps} }
	t.Cleanup(func() { applicationDirs = dirs })
	return home
}

// The bug that made this whole page useless: every write appended the group
// heading again, and a key file with the same group in it twice is one glib
// refuses to read -- so the desktop saw no defaults at all.
func TestWritingTwiceLeavesOneGroupOfEach(t *testing.T) {
	home := fakeDesktop(t)
	c := &Client{}
	gimp := DesktopApp{ID: "gimp.desktop", Name: "GIMP", Types: []string{"image/png"}}
	for i := 0; i < 3; i++ {
		if err := c.SetDefault(kindNamed(t, "Pictures"), gimp); err != nil {
			t.Fatal(err)
		}
		if err := c.ClearDefault(kindNamed(t, "Markdown")); err != nil {
			t.Fatal(err)
		}
	}
	for group, count := range MimeappsGroups(filepath.Join(home, "config/mimeapps.list")) {
		if count != 1 {
			t.Errorf("[%s] appears %d times, want once", group, count)
		}
	}
}

// A file already in that state is put right, and nothing in it is lost.
func TestRepairCollapsesDuplicatedGroups(t *testing.T) {
	home := fakeDesktop(t)
	path := filepath.Join(home, "config/mimeapps.list")
	os.WriteFile(path, []byte("[Default Applications]\n"+
		"application/pdf=papers.desktop\n"+
		"[Added Associations]\nimage/png=gimp.desktop;\n"+
		"[Default Applications]\n[Added Associations]\n[Default Applications]\n"), 0o644)

	if !MimeappsBroken() {
		t.Fatal("a file with three [Default Applications] is not reported as broken")
	}
	if err := (&Client{}).RepairMimeapps(); err != nil {
		t.Fatal(err)
	}
	if MimeappsBroken() {
		t.Error("still broken after the repair")
	}
	ini := readINI(path)
	if got := ini["Default Applications"]["application/pdf"]; got != "papers.desktop" {
		t.Errorf("the repair lost the PDF default: %q", got)
	}
	if got := ini["Added Associations"]["image/png"]; got != "gimp.desktop;" {
		t.Errorf("the repair lost the association: %q", got)
	}
}

// With nothing set, what opens a type is the first application in
// mimeinfo.cache -- the list the desktop itself walks. Naming the
// alphabetically first one instead is a page that says one thing while the
// machine does another.
func TestUnsetTypeFollowsTheRegisteredOrder(t *testing.T) {
	fakeDesktop(t)
	for _, d := range (&Client{}).Defaults(context.Background()) {
		if d.Label != "Pictures" {
			continue
		}
		if d.Origin != OriginNone {
			t.Fatalf("Pictures came from %v, want nobody", d.Origin)
		}
		if d.App.Name != "gThumb" {
			t.Errorf("Pictures would open in %q, but the cache names gThumb first", d.App.Name)
		}
	}
}

// Terminals declare no MIME type, so offering one means knowing a terminal
// when we see it.
func TestTerminalOffersTerminalEmulators(t *testing.T) {
	fakeDesktop(t)
	for _, d := range (&Client{}).Defaults(context.Background()) {
		if d.Label != "Terminal" {
			continue
		}
		if len(d.Choices) != 1 || d.Choices[0].Name != "Ghostty" {
			t.Errorf("the terminal kind offers %v, want Ghostty", d.Choices)
		}
		// And choosing it has to stick, which means writing the association
		// as well as the default.
		if err := (&Client{}).SetDefault(d.FileKind, d.Choices[0]); err != nil {
			t.Fatal(err)
		}
		ini := readINI(MimeappsPath())
		if got := ini["Default Applications"]["x-scheme-handler/terminal"]; got != "ghostty.desktop" {
			t.Errorf("terminal default is %q", got)
		}
		if got := ini["Added Associations"]["x-scheme-handler/terminal"]; !strings.Contains(got, "ghostty.desktop") {
			t.Errorf("terminal association is %q", got)
		}
	}
}
