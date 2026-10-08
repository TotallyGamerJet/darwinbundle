package darwinbundle_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"

	"github.com/TotallyGamerJet/darwinbundle"
	"github.com/TotallyGamerJet/darwinbundle/keychain"
)

// A release build, from compiled binaries to a signed archive: universal
// binaries, an application with an extension and an icon, signed from the inside
// out, checked, and zipped.
//
// The examples in this file are compiled by go test, so they cannot drift from
// the API, but they are not run: they need real Mach-O binaries and a signing
// identity.
func Example() {
	const out = "dist"

	// Merge the per-architecture builds. Do this before signing: Sign signs each
	// slice of a universal binary, but cannot merge signed slices.
	if err := darwinbundle.MakeUniversal("build/notes", "build/notes-arm64", "build/notes-amd64"); err != nil {
		log.Fatal(err)
	}
	if err := darwinbundle.MakeUniversal("build/sync", "build/sync-arm64", "build/sync-amd64"); err != nil {
		log.Fatal(err)
	}

	app := &darwinbundle.Bundle{
		Kind:          darwinbundle.KindApp,
		Name:          "Notes",
		Identifier:    "com.example.Notes",
		Version:       "1.4.0",
		BuildNumber:   "212",
		MinimumSystem: "13.0",
		Executable:    "build/notes",
		IconSet:       "assets/AppIcon.appiconset",
		PlugIns: []*darwinbundle.Bundle{{
			Kind:        darwinbundle.KindExtension,
			Name:        "NotesSync",
			DisplayName: "Notes", // what the user sees, not the file name
			// An extension's identifier must be prefixed by its application's.
			Identifier:  "com.example.Notes.Sync",
			Version:     "1.4.0",
			BuildNumber: "212",
			Executable:  "build/sync",
			// Everything specific to this kind of extension is data, not part
			// of the library.
			Info: map[string]any{
				"NSExtension": map[string]any{
					"NSExtensionPointIdentifier": "com.apple.fileprovider-nonui",
					"NSExtensionPrincipalClass":  "SyncExtension",
				},
			},
		}},
	}
	appPath, err := app.Build(out)
	if err != nil {
		log.Fatal(err)
	}

	// The key stays in the Keychain; darwinbundle never sees it.
	signer, err := keychain.Find("Developer ID Application")
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := signer.Close(); err != nil {
			log.Print(err)
		}
	}()

	// From the inside out: the application's signature seals the extension's.
	ext := app.PlugIns[0]
	for _, target := range []struct{ path, entitlements string }{
		{ext.Path(app.PlugInsDir(out)), "entitlements/sync.plist"},
		{appPath, "entitlements/notes.plist"},
	} {
		err := darwinbundle.Sign(target.path, darwinbundle.SignConfig{
			Signer:          signer,
			Entitlements:    target.entitlements,
			TimestampServer: "http://timestamp.apple.com/ts01",
		})
		if err != nil {
			log.Fatal(err)
		}
	}

	// Signing is portable; checking is not. This needs a Mac.
	if res, err := darwinbundle.Verify(context.Background(), appPath, darwinbundle.VerifyOptions{Deep: true}); err != nil {
		log.Fatalf("%v\n%s", err, res.Output)
	}

	if err := darwinbundle.Zip(filepath.Join(out, "Notes.zip"), appPath); err != nil {
		log.Fatal(err)
	}
}

func ExampleBundle() {
	b := &darwinbundle.Bundle{
		Kind:       darwinbundle.KindApp,
		Name:       "Hello",
		Identifier: "com.example.Hello",
		Version:    "1.0",
		Executable: "build/hello", // already compiled
		Info: map[string]any{
			// Anything the fields do not cover goes in Info, and wins over what
			// they generate.
			"LSUIElement":              true, // a menu bar agent with no Dock icon
			"NSHumanReadableCopyright": "© 2026 Example",
		},
	}
	path, err := b.Build("dist")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("built", path)
}

func ExampleSign_p12() {
	// For a CI runner that has the identity as a secret rather than in a
	// Keychain.
	err := darwinbundle.Sign("dist/Hello.app", darwinbundle.SignConfig{
		P12Path:     "secrets/developer-id.p12",
		P12Password: "from the environment, not from source",
	})
	if err != nil {
		log.Fatal(err)
	}
}

func ExampleSign_adHoc() {
	// Ad hoc must be chosen: a SignConfig that names no identity is an error.
	// An ad hoc signature belongs to no team, so it is not enough for code that
	// needs a team-prefixed App Group.
	if err := darwinbundle.Sign("dist/Hello.app", darwinbundle.SignConfig{AdHoc: true}); err != nil {
		log.Fatal(err)
	}
}

func ExampleSign_nestedBundles() {
	err := darwinbundle.Sign("dist/Notes.app", darwinbundle.SignConfig{AdHoc: true})
	if errors.Is(err, darwinbundle.ErrNestedBundlesUnsupported) {
		// Upstream quill cannot yet seal an application that contains an
		// extension. The README says what to add to go.mod.
		log.Fatal("this build cannot sign applications containing extensions: ", err)
	}
}

func ExampleInspect() {
	sig, err := darwinbundle.Inspect(context.Background(), "dist/Hello.app")
	if errors.Is(err, darwinbundle.ErrCodesignUnavailable) {
		log.Fatal("checking a signature needs a Mac")
	}
	if err != nil {
		log.Fatal(err)
	}
	switch {
	case !sig.Signed:
		fmt.Println("not signed")
	case sig.AdHoc:
		fmt.Println("ad hoc: belongs to no team")
	default:
		fmt.Println("signed by team", sig.TeamID)
	}
}
