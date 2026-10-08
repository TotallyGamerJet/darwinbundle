package darwinbundle_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// Real Mach-O files are the only useful fixture for universal binaries and for
// signing: the point is what lipo and the signer do with the genuine article, and
// a hand-assembled header is not accepted by the second.
//
// A minimal Go program is cross-compiled for darwin, once per architecture per
// test run, in a directory TestMain removes at the end. Go is guaranteed to be
// present because the tests are being run by it.

var (
	fixtureDir string
	machoOnce  sync.Map // arch -> func() (string, error)
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "darwinbundle-fixtures-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "creating the fixture directory:", err)
		os.Exit(1)
	}
	fixtureDir = dir

	teardown, err := platformSetup()
	if err != nil {
		fmt.Fprintln(os.Stderr, "setting up the platform fixtures:", err)
		if rerr := os.RemoveAll(dir); rerr != nil {
			fmt.Fprintln(os.Stderr, "and removing the fixture directory:", rerr)
		}
		os.Exit(1)
	}
	code := m.Run()
	if err := teardown(); err != nil {
		fmt.Fprintln(os.Stderr, "tearing down the platform fixtures:", err)
		if code == 0 {
			code = 1
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintln(os.Stderr, "removing the fixture directory:", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// thinBinary returns the path of a darwin executable for arch ("arm64" or
// "amd64"). The file is shared between tests, so a test that signs or modifies
// it must copy it first; use copyOf.
func thinBinary(t testing.TB, arch string) string {
	t.Helper()
	once, _ := machoOnce.LoadOrStore(arch, sync.OnceValues(func() (string, error) {
		return buildMachO(arch)
	}))
	path, err := once.(func() (string, error))()
	if err != nil {
		t.Skipf("could not build a darwin/%s binary: %v", arch, err)
	}
	return path
}

func buildMachO(arch string) (string, error) {
	dir := filepath.Join(fixtureDir, "src-"+arch)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		return "", err
	}
	// 1.24 is this module's floor; naming it keeps the probe from asking for a
	// newer toolchain than the one running the tests.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module probe\n\ngo 1.24\n"), 0o644); err != nil {
		return "", err
	}
	out := filepath.Join(fixtureDir, "probe-"+arch)
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0", "GOOS=darwin", "GOARCH="+arch, "GOTOOLCHAIN=local", "GOFLAGS=-mod=mod")
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%w\n%s", err, b)
	}
	return out, nil
}

// copyOf returns a private copy of a file that the test may modify.
func copyOf(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), filepath.Base(path))
	if err := os.WriteFile(dst, b, info.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	return dst
}
