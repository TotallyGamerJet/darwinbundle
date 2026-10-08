package macbundle_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/TotallyGamerJet/macbundle"
	"github.com/TotallyGamerJet/macbundle/appiconset"
)

// app returns a valid application with one valid extension; tests change what
// they are about.
func app(t testing.TB) *macbundle.Bundle {
	t.Helper()
	return &macbundle.Bundle{
		Kind:          macbundle.KindApp,
		Name:          "Test App",
		Identifier:    "com.example.TestApp",
		Version:       "1.2.3",
		BuildNumber:   "42",
		MinimumSystem: "13.0",
		Executable:    fakeBinary(t, "app"),
		PlugIns: []*macbundle.Bundle{{
			Kind:        macbundle.KindExtension,
			Name:        "TestExt",
			DisplayName: "Test Extension",
			Identifier:  "com.example.TestApp.Ext",
			Version:     "1.2.3",
			BuildNumber: "42",
			Executable:  fakeBinary(t, "ext"),
			Info: map[string]any{
				"NSExtension": map[string]any{
					"NSExtensionPointIdentifier": "com.apple.fileprovider-nonui",
					"NSExtensionPrincipalClass":  "TestExtension",
				},
			},
		}},
	}
}

func build(t testing.TB, b *macbundle.Bundle, dir string) string {
	t.Helper()
	path, err := b.Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return path
}

func TestBuildProducesAppleBundleHierarchy(t *testing.T) {
	b := app(t)
	dir := t.TempDir()
	path := build(t, b, dir)

	if want := filepath.Join(dir, "Test App.app"); path != want {
		t.Errorf("Build returned %s, want %s", path, want)
	}
	// The exact hierarchy code signing expects.
	for _, rel := range []string{
		"Contents/Info.plist",
		"Contents/PkgInfo",
		"Contents/MacOS/Test App",
		"Contents/Resources",
		"Contents/PlugIns/TestExt.appex/Contents/Info.plist",
		"Contents/PlugIns/TestExt.appex/Contents/MacOS/TestExt",
		"Contents/PlugIns/TestExt.appex/Contents/Resources",
	} {
		if _, err := os.Stat(filepath.Join(path, rel)); err != nil {
			t.Errorf("missing %s: %v", rel, err)
		}
	}
	// PkgInfo is for applications; an extension has none.
	if _, err := os.Stat(filepath.Join(path, "Contents/PlugIns/TestExt.appex/Contents/PkgInfo")); err == nil {
		t.Error("the extension has a PkgInfo")
	}
}

func TestPathHelpersAgreeWithWhatIsBuilt(t *testing.T) {
	b := app(t)
	dir := t.TempDir()
	build(t, b, dir)

	ext := b.PlugIns[0]
	for name, p := range map[string]string{
		"app":            b.Path(dir),
		"app executable": b.ExecutablePath(dir),
		"extension":      ext.Path(b.PlugInsDir(dir)),
		"ext executable": ext.ExecutablePath(b.PlugInsDir(dir)),
		"plug-ins":       b.PlugInsDir(dir),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestExecutablesAreExecutable(t *testing.T) {
	b := app(t)
	dir := t.TempDir()
	build(t, b, dir)
	for _, p := range []string{b.ExecutablePath(dir), b.PlugIns[0].ExecutablePath(b.PlugInsDir(dir))} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s is not executable (mode %v)", p, fi.Mode())
		}
	}
}

// Copying a binary that is not itself executable must still yield one that is.
func TestTheCopiedBinaryIsExecutableWhateverTheSourceWas(t *testing.T) {
	b := app(t)
	if err := os.Chmod(b.Executable, 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	build(t, b, dir)
	fi, err := os.Stat(b.ExecutablePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("mode %v", fi.Mode())
	}
}

func TestAppInfoPlist(t *testing.T) {
	b := app(t)
	dir := t.TempDir()
	path := build(t, b, dir)
	info := readPlist(t, filepath.Join(path, "Contents/Info.plist"))

	want := map[string]any{
		"CFBundleName":               "Test App",
		"CFBundleDisplayName":        "Test App",
		"CFBundleExecutable":         "Test App",
		"CFBundleIdentifier":         "com.example.TestApp",
		"CFBundlePackageType":        "APPL",
		"CFBundleShortVersionString": "1.2.3",
		"CFBundleVersion":            "42",
		"LSMinimumSystemVersion":     "13.0",
		"NSPrincipalClass":           "NSApplication",
		"LSUIElement":                false,
		"DTPlatformName":             "macosx",
	}
	for k, v := range want {
		if got := info[k]; got != v {
			t.Errorf("%s = %#v, want %#v", k, got, v)
		}
	}
	if got := info["CFBundleSupportedPlatforms"]; !reflect.DeepEqual(got, []any{"MacOSX"}) {
		t.Errorf("CFBundleSupportedPlatforms = %#v", got)
	}
	if _, ok := info["CFBundleIconName"]; ok {
		t.Error("an app with no icon set names an icon")
	}
}

func TestExtensionInfoPlist(t *testing.T) {
	b := app(t)
	dir := t.TempDir()
	build(t, b, dir)
	ext := b.PlugIns[0]
	info := readPlist(t, filepath.Join(ext.Path(b.PlugInsDir(dir)), "Contents/Info.plist"))

	// The system launches an extension as an XPC service, not as an app.
	if info["CFBundlePackageType"] != "XPC!" {
		t.Errorf("CFBundlePackageType = %v, want XPC!", info["CFBundlePackageType"])
	}
	// The executable keeps the file-safe name; the other two are what the user
	// reads.
	if info["CFBundleExecutable"] != "TestExt" {
		t.Errorf("CFBundleExecutable = %v", info["CFBundleExecutable"])
	}
	for _, k := range []string{"CFBundleName", "CFBundleDisplayName"} {
		if info[k] != "Test Extension" {
			t.Errorf("%s = %v, want the display name", k, info[k])
		}
	}
	nsext, _ := info["NSExtension"].(map[string]any)
	if nsext["NSExtensionPointIdentifier"] != "com.apple.fileprovider-nonui" ||
		nsext["NSExtensionPrincipalClass"] != "TestExtension" {
		t.Errorf("NSExtension = %#v", nsext)
	}
	if _, ok := info["NSPrincipalClass"]; ok {
		t.Error("an extension declares NSPrincipalClass, which is an application's")
	}
}

// A key that is not set is left out. The original wrote an empty string, and an
// empty LSMinimumSystemVersion is a value, not an absence.
func TestEmptyFieldsAreOmittedRatherThanWrittenEmpty(t *testing.T) {
	b := app(t)
	b.Version, b.BuildNumber, b.MinimumSystem = "", "", ""
	dir := t.TempDir()
	info := readPlist(t, filepath.Join(build(t, b, dir), "Contents/Info.plist"))
	for _, k := range []string{"CFBundleShortVersionString", "CFBundleVersion", "LSMinimumSystemVersion"} {
		if v, ok := info[k]; ok {
			t.Errorf("%s was written as %#v", k, v)
		}
	}
}

func TestInfoOverridesAndExtendsTheGeneratedEntries(t *testing.T) {
	b := app(t)
	b.Info = map[string]any{
		"LSUIElement":              true, // overrides a default
		"NSHumanReadableCopyright": "© Someone",
		"CFBundleDocumentTypes": []map[string]any{
			{"CFBundleTypeName": "Thing", "CFBundleTypeExtensions": []string{"thing"}},
		},
		"ElectricBlue": 7,
	}
	info := readPlist(t, filepath.Join(build(t, b, t.TempDir()), "Contents/Info.plist"))

	if info["LSUIElement"] != true {
		t.Error("Info did not override the default")
	}
	if info["NSHumanReadableCopyright"] != "© Someone" || info["ElectricBlue"] != 7 {
		t.Errorf("Info entries were not written: %#v", info)
	}
	docs, _ := info["CFBundleDocumentTypes"].([]any)
	if len(docs) != 1 {
		t.Fatalf("CFBundleDocumentTypes = %#v", info["CFBundleDocumentTypes"])
	}
	if doc, _ := docs[0].(map[string]any); doc["CFBundleTypeName"] != "Thing" {
		t.Errorf("document type = %#v", docs[0])
	}
	// What was not overridden is still generated.
	if info["CFBundleIdentifier"] != "com.example.TestApp" {
		t.Error("generated entries were lost")
	}
}

func TestDisplayNameAndExecutableNameCanDiffer(t *testing.T) {
	b := app(t)
	b.DisplayName = "Pretty Name"
	b.ExecutableName = "prettyd"
	dir := t.TempDir()
	path := build(t, b, dir)

	if _, err := os.Stat(filepath.Join(path, "Contents/MacOS/prettyd")); err != nil {
		t.Errorf("the executable is not under ExecutableName: %v", err)
	}
	info := readPlist(t, filepath.Join(path, "Contents/Info.plist"))
	if info["CFBundleExecutable"] != "prettyd" || info["CFBundleDisplayName"] != "Pretty Name" {
		t.Errorf("Info.plist = %#v", info)
	}
	if b.ExecutablePath(dir) != filepath.Join(path, "Contents/MacOS/prettyd") {
		t.Errorf("ExecutablePath = %s", b.ExecutablePath(dir))
	}
}

func TestIconSetIsCompiledAndNamed(t *testing.T) {
	b := app(t)
	b.IconSet = iconSet(t)
	path := build(t, b, t.TempDir())

	if fi, err := os.Stat(filepath.Join(path, "Contents/Resources/Assets.car")); err != nil || fi.Size() == 0 {
		t.Errorf("no compiled icon catalog: %v", err)
	}
	info := readPlist(t, filepath.Join(path, "Contents/Info.plist"))
	if info["CFBundleIconName"] != appiconset.FacetName {
		t.Errorf("CFBundleIconName = %v, want %s", info["CFBundleIconName"], appiconset.FacetName)
	}
}

func TestResourcesAreCopiedIntoPlace(t *testing.T) {
	src := t.TempDir()
	write := func(name, body string, mode os.FileMode) string {
		p := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	single := write("one.txt", "one", 0o644)
	write("tree/a.txt", "a", 0o644)
	write("tree/deep/b.sh", "b", 0o755)

	b := app(t)
	b.Resources = map[string]string{
		"one.txt":              single,
		"Support/tree":         filepath.Join(src, "tree"),
		"en.lproj/Strings.txt": single,
	}
	path := build(t, b, t.TempDir())

	for rel, want := range map[string]string{
		"one.txt":                "one",
		"Support/tree/a.txt":     "a",
		"Support/tree/deep/b.sh": "b",
		"en.lproj/Strings.txt":   "one",
	} {
		got, err := os.ReadFile(filepath.Join(path, "Contents/Resources", rel))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", rel, got, err, want)
		}
	}
	fi, err := os.Stat(filepath.Join(path, "Contents/Resources/Support/tree/deep/b.sh"))
	if err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("a copied script lost its executable bit: %v %v", fi, err)
	}
}

func TestAResourceThatIsASymlinkIsRefused(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "real"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(src, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	b := app(t)
	b.Resources = map[string]string{"tree": src}
	if _, err := b.Build(t.TempDir()); err == nil {
		t.Error("a symbolic link inside a resource directory was copied")
	}
}

// Code signing seals the whole bundle, so a file left behind from an earlier
// build would be sealed into the signature and shipped.
func TestRebuildClearsStaleFiles(t *testing.T) {
	b := app(t)
	dir := t.TempDir()
	path := build(t, b, dir)

	stale := filepath.Join(path, "Contents/Resources/stale.txt")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	build(t, b, dir)
	if _, err := os.Stat(stale); err == nil {
		t.Error("a file from the previous build survived a rebuild")
	}
}

// A build that fails must not take the last good bundle with it.
func TestAFailedRebuildLeavesThePreviousBundleAlone(t *testing.T) {
	b := app(t)
	dir := t.TempDir()
	path := build(t, b, dir)
	marker := filepath.Join(path, "Contents/Resources/keep.txt")
	if err := os.WriteFile(marker, []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Valid by the checks that run first, and fails part-way through assembly:
	// the icon set's Contents.json exists but names nothing.
	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, "Contents.json"), []byte(`{"images":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	b.IconSet = bad
	if _, err := b.Build(dir); err == nil {
		t.Fatal("the build was expected to fail")
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "kept" {
		t.Errorf("the previous bundle was damaged by a failed build: %q, %v", got, err)
	}
	// And nothing is left lying about in the output directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the output directory holds %v, want only the bundle", names)
	}
}

func TestInfoPlistIsReproducible(t *testing.T) {
	b := app(t)
	read := func() string {
		p := build(t, b, t.TempDir())
		raw, err := os.ReadFile(filepath.Join(p, "Contents/Info.plist"))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	first := read()
	for range 5 {
		if read() != first {
			t.Fatal("two builds of one bundle produced different Info.plist files")
		}
	}
}

func TestBuildCreatesTheOutputDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does", "not", "exist")
	build(t, app(t), dir)
}

func TestBuildRejectsWhatCannotBeBuilt(t *testing.T) {
	tests := []struct {
		name   string
		change func(*macbundle.Bundle)
		want   string
	}{
		{"no kind", func(b *macbundle.Bundle) { b.Kind = 0 }, "Kind"},
		{"an unknown kind", func(b *macbundle.Bundle) { b.Kind = 99 }, "Kind"},
		{"no name", func(b *macbundle.Bundle) { b.Name = "" }, "Name"},
		{"a blank name", func(b *macbundle.Bundle) { b.Name = "  " }, "Name"},
		{"a name with a slash", func(b *macbundle.Bundle) { b.Name = "a/b" }, "path separator"},
		{"a name that climbs", func(b *macbundle.Bundle) { b.Name = ".." }, "Name"},
		{"an executable name with a slash", func(b *macbundle.Bundle) { b.ExecutableName = "../x" }, "ExecutableName"},
		{"no identifier", func(b *macbundle.Bundle) { b.Identifier = "" }, "Identifier"},
		{"an identifier with an underscore", func(b *macbundle.Bundle) { b.Identifier = "com.example.my_app" }, "only letters, digits"},
		{"an identifier with a space", func(b *macbundle.Bundle) { b.Identifier = "com.example.my app" }, "only letters, digits"},
		{"no executable", func(b *macbundle.Bundle) { b.Executable = "" }, "Executable is required"},
		{"a missing executable", func(b *macbundle.Bundle) { b.Executable = "/nonexistent/binary" }, "Executable"},
		{"a directory as the executable", func(b *macbundle.Bundle) { b.Executable = os.TempDir() }, "not a regular file"},
		{"CFBundleExecutable contradicting the file", func(b *macbundle.Bundle) {
			b.Info = map[string]any{"CFBundleExecutable": "other"}
		}, "CFBundleExecutable"},
		{"an Info value a plist cannot hold", func(b *macbundle.Bundle) {
			b.Info = map[string]any{"Bad": make(chan int)}
		}, "Bad"},
		{"a resource that escapes", func(b *macbundle.Bundle) {
			b.Resources = map[string]string{"../outside": b.Executable}
		}, "not a path inside"},
		{"an absolute resource path", func(b *macbundle.Bundle) {
			b.Resources = map[string]string{"/etc/x": b.Executable}
		}, "not a path inside"},
		{"a resource that does not exist", func(b *macbundle.Bundle) {
			b.Resources = map[string]string{"x": "/nonexistent/file"}
		}, "resource"},
		{"an icon set that does not exist", func(b *macbundle.Bundle) { b.IconSet = "/nonexistent/set" }, "IconSet"},
		{"a nil plug-in", func(b *macbundle.Bundle) { b.PlugIns = append(b.PlugIns, nil) }, "nil"},
		{"an application as a plug-in", func(b *macbundle.Bundle) { b.PlugIns[0].Kind = macbundle.KindApp }, "not an extension"},
		{"two plug-ins of one name", func(b *macbundle.Bundle) {
			dup := *b.PlugIns[0]
			b.PlugIns = append(b.PlugIns, &dup)
		}, "two plug-ins"},
		{"an extension with the wrong identifier prefix", func(b *macbundle.Bundle) {
			b.PlugIns[0].Identifier = "com.other.Ext"
		}, "must be prefixed"},
		{"an extension whose identifier merely starts with the app's", func(b *macbundle.Bundle) {
			// com.example.TestAppExtra begins with com.example.TestApp but is
			// not inside its namespace.
			b.PlugIns[0].Identifier = "com.example.TestAppExtra"
		}, "must be prefixed"},
		{"an extension with no NSExtension", func(b *macbundle.Bundle) { b.PlugIns[0].Info = nil }, "NSExtension"},
		{"an extension with an empty NSExtension", func(b *macbundle.Bundle) {
			b.PlugIns[0].Info = map[string]any{"NSExtension": map[string]any{}}
		}, "NSExtension"},
		{"an extension containing an extension", func(b *macbundle.Bundle) {
			inner := *b.PlugIns[0]
			inner.Name = "Inner"
			inner.Identifier = "com.example.TestApp.Ext.Inner"
			b.PlugIns[0].PlugIns = []*macbundle.Bundle{&inner}
		}, "cannot contain extensions"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := app(t)
			tc.change(b)
			dir := t.TempDir()
			_, err := b.Build(dir)
			if err == nil {
				t.Fatal("no error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
			// Validation happens before anything is written.
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Errorf("a rejected build left %d entries in the output directory", len(entries))
			}
		})
	}
}

// A standalone extension, or an application with no extensions, is a valid
// thing to build.
func TestStandaloneBundles(t *testing.T) {
	a := app(t)
	a.PlugIns = nil
	build(t, a, t.TempDir())

	ext := app(t).PlugIns[0]
	path := build(t, ext, t.TempDir())
	if !strings.HasSuffix(path, "TestExt.appex") {
		t.Errorf("path = %s", path)
	}
}

func TestOnlyAnApplicationMayContainExtensions(t *testing.T) {
	b := app(t)
	ext := b.PlugIns[0]
	other := *ext
	other.Name = "Other"
	other.Identifier = ext.Identifier + ".Other"
	ext.PlugIns = []*macbundle.Bundle{&other}
	if _, err := ext.Build(t.TempDir()); err == nil {
		t.Error("an extension was allowed to contain an extension")
	}
}
