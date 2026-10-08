package darwinbundle_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/TotallyGamerJet/darwinbundle"
)

// TestMakeUniversalCarriesBothSlices.
//
// The application ships one binary that has to run on both Apple Silicon and
// Intel. A merge that silently produced a thin file would install, sign, and
// verify perfectly — and then fail to launch on exactly the machines nobody
// developing it is using.
func TestMakeUniversalCarriesBothSlices(t *testing.T) {
	arm := thinBinary(t, "arm64")
	intel := thinBinary(t, "amd64")
	out := filepath.Join(t.TempDir(), "universal")

	if err := darwinbundle.MakeUniversal(out, arm, intel); err != nil {
		t.Fatalf("MakeUniversal: %v", err)
	}

	arches, err := darwinbundle.Architectures(out)
	if err != nil {
		t.Fatalf("Architectures: %v", err)
	}
	// lipo names the Intel slice x86_64, which is what Apple's tooling prints
	// and not the GOARCH the input was built with.
	for _, want := range []string{"arm64", "x86_64"} {
		if !slices.Contains(arches, want) {
			t.Errorf("the universal binary has %v, missing %s", arches, want)
		}
	}

	// It must still be runnable: lipo's own output is not executable, so the
	// mode is set deliberately and a regression there would only show up when
	// the bundle refused to launch.
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("the universal binary is not executable (mode %v)", info.Mode().Perm())
	}
}

// A single input is a thin binary, not a universal one. Accepting it would let
// a build that lost an architecture pass unnoticed.
func TestMakeUniversalRejectsASingleInput(t *testing.T) {
	arm := thinBinary(t, "arm64")
	out := filepath.Join(t.TempDir(), "universal")

	if err := darwinbundle.MakeUniversal(out, arm); err == nil {
		t.Fatal("one input was accepted as a universal binary")
	}
}

// A missing input is named, rather than surfacing as whatever lipo says about
// a file it could not open.
func TestMakeUniversalNamesAMissingInput(t *testing.T) {
	arm := thinBinary(t, "arm64")
	missing := filepath.Join(t.TempDir(), "absent")
	out := filepath.Join(t.TempDir(), "universal")

	err := darwinbundle.MakeUniversal(out, arm, missing)
	if err == nil {
		t.Fatal("a missing input was accepted")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("the error does not name the missing input: %v", err)
	}
}

// Architectures reads a thin file too, which is what makes it usable as a check
// on the build's own output rather than only on merged results.
func TestArchitecturesReadsAThinBinary(t *testing.T) {
	arches, err := darwinbundle.Architectures(thinBinary(t, "arm64"))
	if err != nil {
		t.Fatalf("Architectures: %v", err)
	}
	if len(arches) != 1 || arches[0] != "arm64" {
		t.Errorf("got %v, want [arm64]", arches)
	}
}

// Two slices of one architecture are not a universal binary. lipo's refusal is
// the error worth having, rather than a fat file with two identical slices.
func TestMakeUniversalRejectsDuplicateArchitectures(t *testing.T) {
	arm := thinBinary(t, "arm64")
	out := filepath.Join(t.TempDir(), "universal")
	if err := darwinbundle.MakeUniversal(out, arm, copyOf(t, arm)); err == nil {
		t.Error("two arm64 slices were merged")
	}
}

// A file already at the destination, of any shape, is replaced rather than
// merged into or trusted: whatever it was would otherwise be what gets signed.
func TestMakeUniversalReplacesAStaleOutput(t *testing.T) {
	arm, intel := thinBinary(t, "arm64"), thinBinary(t, "amd64")
	out := filepath.Join(t.TempDir(), "nested", "universal")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, []byte("stale, and not a Mach-O"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := darwinbundle.MakeUniversal(out, arm, intel); err != nil {
		t.Fatalf("MakeUniversal over a stale file: %v", err)
	}
	arches, err := darwinbundle.Architectures(out)
	if err != nil || len(arches) != 2 {
		t.Errorf("Architectures = %v, %v; want both slices", arches, err)
	}
}

func TestMakeUniversalCreatesTheOutputDirectory(t *testing.T) {
	arm, intel := thinBinary(t, "arm64"), thinBinary(t, "amd64")
	out := filepath.Join(t.TempDir(), "a", "b", "universal")
	if err := darwinbundle.MakeUniversal(out, arm, intel); err != nil {
		t.Fatal(err)
	}
}

func TestArchitecturesRefusesAFileThatIsNotMachO(t *testing.T) {
	p := filepath.Join(t.TempDir(), "text")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := darwinbundle.Architectures(p); err == nil {
		t.Error("a text file was reported as having architectures")
	}
}

// Reporting two architectures is not the same as running. On a Mac the merged
// file is executed, which is the check that the slices lipo wrote are the ones
// the kernel will load.
func TestTheUniversalBinaryRuns(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin binaries only run on macOS")
	}
	out := filepath.Join(t.TempDir(), "universal")
	if err := darwinbundle.MakeUniversal(out, thinBinary(t, "arm64"), thinBinary(t, "amd64")); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command(out).CombinedOutput(); err != nil {
		t.Errorf("running the universal binary: %v\n%s", err, b)
	}
}
