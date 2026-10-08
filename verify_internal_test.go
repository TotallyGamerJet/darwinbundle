package darwinbundle

import (
	"os"
	"path/filepath"
	"testing"
)

// The fixtures are what codesign -d -vv printed for files signed by this
// package, with the temporary path replaced. They are real output because the
// parser's whole difficulty is the shapes codesign actually uses: it spells an
// absent team "not set", prints an ad hoc signature as "Signature=adhoc" and a
// certificate-backed one as "Signature size=N", and says nothing parseable for
// unsigned code.
func TestParseSignature(t *testing.T) {
	tests := []struct {
		file string
		want Signature
	}{
		{"adhoc", Signature{Signed: true, Identifier: "probe-arm64", AdHoc: true}},
		{"linker", Signature{Signed: true, Identifier: "a.out", AdHoc: true}},
		{"team", Signature{Signed: true, Identifier: "com.example.captured", TeamID: "TEAM123456", HardenedRuntime: true}},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "codesign", tc.file+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			if got := parseSignature(string(raw)); got != tc.want {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// "not set" must not become a team called "not set": that would make every
// ad hoc build look like it belonged to one.
func TestAnAbsentTeamIsEmptyNotTheWordsNotSet(t *testing.T) {
	if got := parseSignature("TeamIdentifier=not set\n").TeamID; got != "" {
		t.Errorf("TeamID = %q", got)
	}
}

func TestCodeDirectoryFlags(t *testing.T) {
	tests := []struct {
		line string
		want []string
	}{
		{"CodeDirectory v=20500 size=1 flags=0x10000(runtime) hashes=4+2", []string{"runtime"}},
		{"CodeDirectory v=20400 size=1 flags=0x20002(adhoc,linker-signed) hashes=4+0", []string{"adhoc", "linker-signed"}},
		{"CodeDirectory v=20500 size=1 flags=0x0(none) hashes=4+2", []string{"none"}},
		{"CodeDirectory v=20500 size=1 flags=0x0 hashes=4+2", nil},
		{"CodeDirectory v=20500 size=1 hashes=4+2", nil},
		{"", nil},
	}
	for _, tc := range tests {
		got := codeDirectoryFlags(tc.line)
		if len(got) != len(tc.want) {
			t.Errorf("%q: got %v, want %v", tc.line, got, tc.want)
			continue
		}
		for _, name := range tc.want {
			if !got[name] {
				t.Errorf("%q: missing flag %q in %v", tc.line, name, got)
			}
		}
	}
}
