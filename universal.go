package darwinbundle

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/konoui/lipo/pkg/lipo"
)

// Universal binaries are assembled with konoui/lipo as a library rather than by
// invoking Apple's lipo(1).
//
// A tool on the PATH is whatever version the machine happens to have, and lipo
// in particular exists only on a Mac with the command line tools installed. As
// a dependency this is pinned, and works wherever Go does.
//
// Order matters against signing: sign a universal binary, never merge signed
// slices. Sign detects a universal binary, signs each slice separately and
// repacks them, so the only requirement is that MakeUniversal runs first.

// MakeUniversal merges thin Mach-O binaries into one universal binary at out.
//
// The inputs must be distinct architectures; lipo rejects duplicates, which is
// the error worth getting rather than a fat file with two identical slices. The
// output is made executable, which lipo's own is not.
func MakeUniversal(out string, inputs ...string) error {
	if len(inputs) < 2 {
		return fmt.Errorf("darwinbundle: a universal binary needs at least two inputs, got %d", len(inputs))
	}
	for _, in := range inputs {
		if _, err := os.Stat(in); err != nil {
			return fmt.Errorf("darwinbundle: universal input %s: %w", in, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return fmt.Errorf("darwinbundle: creating the directory for %s: %w", out, err)
	}
	// lipo writes the output itself; a stale file of the wrong shape left in
	// place would otherwise be what gets signed.
	if err := os.Remove(out); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("darwinbundle: clearing %s: %w", out, err)
	}

	l := lipo.New(lipo.WithInputs(inputs...), lipo.WithOutput(out))
	if err := l.Create(); err != nil {
		return fmt.Errorf("darwinbundle: merging %v into %s: %w", inputs, out, err)
	}

	// The binaries are executable; lipo's output is not, by default.
	if err := os.Chmod(out, 0o755); err != nil {
		return fmt.Errorf("darwinbundle: making %s executable: %w", out, err)
	}
	return nil
}

// Architectures reports the architectures present in a Mach-O file, thin or
// universal. It is how a build verifies it produced what it meant to.
func Architectures(path string) ([]string, error) {
	arches, err := lipo.New(lipo.WithInputs(path)).Archs()
	if err != nil {
		return nil, fmt.Errorf("darwinbundle: reading the architectures of %s: %w", path, err)
	}
	return arches, nil
}
