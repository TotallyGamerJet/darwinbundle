package macbundle

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/TotallyGamerJet/macbundle/appiconset"
	"github.com/TotallyGamerJet/macbundle/internal/plist"
)

// Kind is the type of a bundle.
type Kind int

const (
	// KindApp is an application: Name.app.
	KindApp Kind = iota + 1

	// KindExtension is an app extension: Name.appex. It is launched by the
	// system as an XPC service rather than by the user, and lives inside the
	// application that contains it, in Contents/PlugIns.
	KindExtension
)

func (k Kind) valid() bool { return k == KindApp || k == KindExtension }

// suffix is the directory suffix of a bundle of this kind.
func (k Kind) suffix() string {
	if k == KindExtension {
		return ".appex"
	}
	return ".app"
}

// packageType is the four-character CFBundlePackageType.
func (k Kind) packageType() string {
	if k == KindExtension {
		return "XPC!"
	}
	return "APPL"
}

func (k Kind) String() string {
	switch k {
	case KindApp:
		return "app"
	case KindExtension:
		return "extension"
	default:
		return fmt.Sprintf("Kind(%d)", int(k))
	}
}

// Bundle describes a bundle to assemble: an application, optionally with app
// extensions nested inside it.
//
//	Name.app/
//	└── Contents/
//	    ├── Info.plist
//	    ├── PkgInfo                       (applications only)
//	    ├── MacOS/Name
//	    ├── Resources/
//	    │   └── Assets.car                (when IconSet is set)
//	    └── PlugIns/
//	        └── Ext.appex/
//	            └── Contents/ ...         (the same shape, recursively)
//
// The zero value is not usable; Kind, Name, Identifier and Executable are
// required. Build checks everything before it writes anything.
type Bundle struct {
	// Kind says whether this is an application or an extension. Required.
	Kind Kind

	// Name is the bundle's file name without its suffix: "Notes" for Notes.app.
	// It is also the executable's name unless ExecutableName says otherwise, so
	// it must be safe as a single path element.
	Name string

	// DisplayName is the name shown to the user, written as CFBundleDisplayName
	// and CFBundleName. It defaults to Name.
	//
	// For an extension this matters more than it looks. The system labels what
	// an extension provides with the extension's own display name and never
	// consults the containing application's, so an extension left at its file
	// name is what the user sees in their own sidebar.
	DisplayName string

	// Identifier is the CFBundleIdentifier, in reverse-DNS form. Apple permits
	// only letters, digits, hyphens and periods. An extension's identifier must
	// be prefixed by its containing application's followed by a period. Required.
	Identifier string

	// Version is the user-visible version, CFBundleShortVersionString.
	// BuildNumber is CFBundleVersion. MinimumSystem is the oldest macOS that can
	// run the bundle, LSMinimumSystemVersion.
	//
	// Each is left out of Info.plist when empty, rather than written as an
	// empty string the system would take literally.
	Version, BuildNumber, MinimumSystem string

	// Executable is the path of the compiled binary to copy into the bundle.
	// It is copied rather than linked, so that signing the bundle never
	// modifies the build output. Required.
	Executable string

	// ExecutableName is the name the binary is given inside Contents/MacOS, and
	// CFBundleExecutable. It defaults to Name.
	ExecutableName string

	// IconSet is the path of an .appiconset directory to compile into
	// Contents/Resources/Assets.car, and CFBundleIconName is set to match.
	//
	// Empty means no icon. That is a real choice, not an omission: the bundle
	// gets no Assets.car and no CFBundleIconName, rather than an empty catalog
	// and a plist entry pointing at nothing.
	IconSet string

	// Resources maps a path inside Contents/Resources to the file or directory
	// to copy there. Directories are copied recursively; anything other than a
	// regular file or directory, a symbolic link included, is an error.
	Resources map[string]string

	// Info holds Info.plist entries beyond the ones the fields above generate.
	// Where a key is in both, Info wins: it is the escape hatch for anything a
	// field does not cover (NSExtension, LSUIElement, CFBundleDocumentTypes).
	//
	// Values may be strings, booleans, integers, floats, []byte, time.Time,
	// string-keyed maps, and slices of those. The one key that cannot be
	// changed is CFBundleExecutable, which must name the file this build
	// creates; setting it to anything else is an error.
	Info map[string]any

	// PlugIns are the extensions nested inside an application. Each is built
	// into Contents/PlugIns, and each must have Kind KindExtension.
	PlugIns []*Bundle
}

// Path returns where the bundle lives when built into dir: dir/Name.app, or
// dir/Name.appex.
func (b *Bundle) Path(dir string) string {
	return filepath.Join(dir, b.Name+b.Kind.suffix())
}

// ExecutablePath returns where the bundle's binary ends up when it is built
// into dir.
func (b *Bundle) ExecutablePath(dir string) string {
	return filepath.Join(b.Path(dir), "Contents", "MacOS", b.executableName())
}

// PlugInsDir returns the directory that holds the bundle's nested extensions
// when it is built into dir. Pass it to an extension's Path or ExecutablePath to
// find that extension inside its application.
func (b *Bundle) PlugInsDir(dir string) string {
	return filepath.Join(b.Path(dir), "Contents", "PlugIns")
}

func (b *Bundle) executableName() string {
	if b.ExecutableName != "" {
		return b.ExecutableName
	}
	return b.Name
}

func (b *Bundle) displayName() string {
	if b.DisplayName != "" {
		return b.DisplayName
	}
	return b.Name
}

// Build assembles the bundle, and the extensions nested in it, into dir, and
// returns the path of the bundle it made.
//
// Code signing seals a bundle's entire contents, so a file left over from an
// earlier build with different settings would be sealed into the signature and
// shipped. Build therefore replaces any bundle already at the destination
// rather than adding to it. It assembles in a temporary directory beside the
// destination and moves the result into place only once everything has
// succeeded, so a build that fails leaves the previous bundle as it was.
func (b *Bundle) Build(dir string) (path string, err error) {
	if err := b.validate(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("macbundle: creating %s: %w", dir, err)
	}

	stage, err := os.MkdirTemp(dir, ".macbundle-")
	if err != nil {
		return "", fmt.Errorf("macbundle: creating a staging directory in %s: %w", dir, err)
	}
	defer func() {
		// Whatever is left in the staging directory is a failed build, or the
		// empty directory a successful one has been moved out of.
		err = errors.Join(err, os.RemoveAll(stage))
	}()

	if err := b.assemble(stage); err != nil {
		return "", err
	}

	dest := b.Path(dir)
	if err := os.RemoveAll(dest); err != nil {
		return "", fmt.Errorf("macbundle: clearing the previous bundle: %w", err)
	}
	if err := os.Rename(b.Path(stage), dest); err != nil {
		return "", fmt.Errorf("macbundle: moving the bundle into place: %w", err)
	}
	return dest, nil
}

// assemble writes the bundle and its plug-ins into dir. It does no validation;
// Build has already done it for the whole tree.
func (b *Bundle) assemble(dir string) error {
	root := b.Path(dir)
	contents := filepath.Join(root, "Contents")

	for _, d := range []string{
		filepath.Join(contents, "MacOS"),
		filepath.Join(contents, "Resources"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("macbundle: creating %s: %w", d, err)
		}
	}

	if err := copyExecutable(b.Executable, b.ExecutablePath(dir)); err != nil {
		return err
	}

	info, err := plist.Marshal(b.infoPlist())
	if err != nil {
		return fmt.Errorf("macbundle: %s Info.plist: %w", b.Name, err)
	}
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), info, 0o644); err != nil {
		return fmt.Errorf("macbundle: writing the Info.plist of %s: %w", b.Name, err)
	}

	if b.IconSet != "" {
		out := filepath.Join(contents, "Resources", "Assets.car")
		if err := appiconset.Compile(b.IconSet, out); err != nil {
			return fmt.Errorf("macbundle: %s: %w", b.Name, err)
		}
	}

	for _, name := range sortedKeys(b.Resources) {
		dst := filepath.Join(contents, "Resources", filepath.FromSlash(name))
		if err := copyTree(b.Resources[name], dst); err != nil {
			return err
		}
	}

	if b.Kind == KindApp {
		// PkgInfo is legacy but is still what some Launch Services paths look at
		// first to classify a bundle.
		if err := os.WriteFile(filepath.Join(contents, "PkgInfo"), []byte("APPL????"), 0o644); err != nil {
			return fmt.Errorf("macbundle: writing PkgInfo: %w", err)
		}
	}

	for _, p := range b.PlugIns {
		if err := p.assemble(b.PlugInsDir(dir)); err != nil {
			return err
		}
	}
	return nil
}

// infoPlist is the Info.plist content: what the fields generate, with Info laid
// over it.
func (b *Bundle) infoPlist() map[string]any {
	name := b.displayName()
	entries := map[string]any{
		"CFBundleName":                  name,
		"CFBundleDisplayName":           name,
		"CFBundleExecutable":            b.executableName(),
		"CFBundleIdentifier":            b.Identifier,
		"CFBundleInfoDictionaryVersion": "6.0",
		"CFBundlePackageType":           b.Kind.packageType(),
		// Xcode injects these; the loader consults them when deciding whether a
		// bundle is loadable on this platform.
		"CFBundleSupportedPlatforms": []string{"MacOSX"},
		"DTPlatformName":             "macosx",
	}
	for key, value := range map[string]string{
		"CFBundleShortVersionString": b.Version,
		"CFBundleVersion":            b.BuildNumber,
		"LSMinimumSystemVersion":     b.MinimumSystem,
	} {
		if value != "" {
			entries[key] = value
		}
	}
	if b.Kind == KindApp {
		entries["NSPrincipalClass"] = "NSApplication"
		// A normal foreground application with a window, not an agent. Info can
		// override this for one that is.
		entries["LSUIElement"] = false
	}
	if b.IconSet != "" {
		// The name of a facet in Assets.car, not a file.
		entries["CFBundleIconName"] = appiconset.FacetName
	}
	for k, v := range b.Info {
		entries[k] = v
	}
	return entries
}

// validate checks the whole tree, so that nothing is written for a bundle that
// could not be completed.
func (b *Bundle) validate() error {
	return b.validateIn(nil)
}

func (b *Bundle) validateIn(parent *Bundle) error {
	label := b.Name
	if label == "" {
		label = "bundle"
	}

	if !b.Kind.valid() {
		return fmt.Errorf("macbundle: %s: Kind must be KindApp or KindExtension", label)
	}
	if err := validName(b.Name); err != nil {
		return fmt.Errorf("macbundle: Name: %w", err)
	}
	if b.ExecutableName != "" {
		if err := validName(b.ExecutableName); err != nil {
			return fmt.Errorf("macbundle: %s: ExecutableName: %w", label, err)
		}
	}
	if err := validIdentifier(b.Identifier); err != nil {
		return fmt.Errorf("macbundle: %s: Identifier: %w", label, err)
	}
	if b.Executable == "" {
		return fmt.Errorf("macbundle: %s: Executable is required", label)
	}
	if fi, err := os.Stat(b.Executable); err != nil {
		return fmt.Errorf("macbundle: %s: Executable: %w", label, err)
	} else if !fi.Mode().IsRegular() {
		return fmt.Errorf("macbundle: %s: Executable %s is not a regular file", label, b.Executable)
	}

	if got, ok := b.Info["CFBundleExecutable"]; ok && got != b.executableName() {
		return fmt.Errorf("macbundle: %s: Info sets CFBundleExecutable to %v, but the file this build creates is %q",
			label, got, b.executableName())
	}

	if b.Kind == KindExtension {
		ext, ok := b.Info["NSExtension"]
		if !ok || isEmptyDict(ext) {
			return fmt.Errorf("macbundle: %s: an extension needs an NSExtension entry in Info, "+
				"naming its extension point and principal class", label)
		}
		if len(b.PlugIns) > 0 {
			return fmt.Errorf("macbundle: %s: an extension cannot contain extensions", label)
		}
	}

	if parent != nil && !strings.HasPrefix(b.Identifier, parent.Identifier+".") {
		// The system finds an extension through its containing app, and refuses
		// to load one whose identifier is not prefixed by the app's.
		return fmt.Errorf("macbundle: the extension identifier %q must be prefixed by the application's (%q)",
			b.Identifier, parent.Identifier+".")
	}

	for name, src := range b.Resources {
		if !filepath.IsLocal(filepath.FromSlash(name)) {
			return fmt.Errorf("macbundle: %s: resource %q is not a path inside Contents/Resources", label, name)
		}
		if b.IconSet != "" && filepath.ToSlash(filepath.Clean(name)) == "Assets.car" {
			return fmt.Errorf("macbundle: %s: resource %q would overwrite the compiled icon set", label, name)
		}
		if _, err := os.Stat(src); err != nil {
			return fmt.Errorf("macbundle: %s: resource %q: %w", label, name, err)
		}
	}
	if b.IconSet != "" {
		if _, err := os.Stat(filepath.Join(b.IconSet, "Contents.json")); err != nil {
			return fmt.Errorf("macbundle: %s: IconSet: %w", label, err)
		}
	}

	if len(b.PlugIns) > 0 && b.Kind != KindApp {
		return fmt.Errorf("macbundle: %s: only an application can contain extensions", label)
	}
	seen := map[string]bool{}
	for _, p := range b.PlugIns {
		if p == nil {
			return fmt.Errorf("macbundle: %s: a nil entry in PlugIns", label)
		}
		if p.Kind != KindExtension {
			return fmt.Errorf("macbundle: %s: plug-in %q is a %s, not an extension", label, p.Name, p.Kind)
		}
		if seen[p.Name] {
			return fmt.Errorf("macbundle: %s: two plug-ins are named %q", label, p.Name)
		}
		seen[p.Name] = true
		if err := p.validateIn(b); err != nil {
			return err
		}
	}
	return nil
}

// validName checks a name is usable as one path element and as an executable
// name.
func validName(s string) error {
	switch {
	case strings.TrimSpace(s) == "":
		return errors.New("is required")
	case s == "." || s == "..":
		return fmt.Errorf("%q is not a usable name", s)
	case strings.ContainsAny(s, "/\\\x00"):
		return fmt.Errorf("%q must not contain a path separator or NUL", s)
	}
	return nil
}

// validIdentifier applies the rule Apple documents for CFBundleIdentifier:
// letters, digits, hyphens and periods.
func validIdentifier(s string) error {
	if s == "" {
		return errors.New("is required")
	}
	for _, r := range s {
		ok := r == '-' || r == '.' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return fmt.Errorf("%q contains %q; only letters, digits, hyphens and periods are allowed", s, r)
		}
	}
	return nil
}

func isEmptyDict(v any) bool {
	m, ok := v.(map[string]any)
	return ok && len(m) == 0
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// copyExecutable copies a binary and marks it executable. It copies rather than
// links so that signing the bundle never modifies the build output.
func copyExecutable(src, dst string) (err error) {
	in, oerr := os.Open(src)
	if oerr != nil {
		return fmt.Errorf("macbundle: opening %s: %w", src, oerr)
	}
	defer func() { err = errors.Join(err, in.Close()) }()

	out, cerr := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if cerr != nil {
		return fmt.Errorf("macbundle: creating %s: %w", dst, cerr)
	}
	defer func() { err = errors.Join(err, closeUnlessDone(out)) }()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("macbundle: copying %s: %w", src, err)
	}
	// Closed here rather than only in the defer: a write is not durable until
	// close reports success, and that has to be part of the copy's own result
	// rather than something reported after Chmod has already run.
	if err := out.Close(); err != nil {
		return fmt.Errorf("macbundle: closing %s: %w", dst, err)
	}
	// The mode passed to OpenFile is filtered by the umask; the bundle's binary
	// must be executable whatever that is.
	return os.Chmod(dst, 0o755)
}

// closeUnlessDone closes f and reports the error, treating an already-closed
// file as success. It lets a write path close explicitly — so the error joins
// the operation it belongs to — while still being closed on every early return.
func closeUnlessDone(f *os.File) error {
	if err := f.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		return err
	}
	return nil
}

// copyTree copies a file or a directory tree to dst, keeping each file's
// permission bits. Anything that is not a regular file or a directory is an
// error: a symbolic link copied as a file would change meaning, and a bundle
// quietly missing one is worse than a build that refuses.
func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("macbundle: %w", err)
	}
	if !info.IsDir() {
		return copyFile(src, dst, info.Mode().Perm())
	}
	root := filepath.Clean(src)
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type().IsRegular():
			fi, err := d.Info()
			if err != nil {
				return err
			}
			return copyFile(p, target, fi.Mode().Perm())
		default:
			return fmt.Errorf("macbundle: %s is a %s, which cannot be copied into a bundle", p, d.Type().Type())
		}
	})
}

func copyFile(src, dst string, perm fs.FileMode) (err error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("macbundle: %w", err)
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("macbundle: opening %s: %w", src, err)
	}
	defer func() { err = errors.Join(err, in.Close()) }()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("macbundle: creating %s: %w", dst, err)
	}
	defer func() { err = errors.Join(err, closeUnlessDone(out)) }()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("macbundle: copying %s: %w", src, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("macbundle: closing %s: %w", dst, err)
	}
	return nil
}
