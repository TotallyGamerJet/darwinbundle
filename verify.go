package macbundle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Quill signs; Apple's own tools verify. Using the platform's verifier is the
// point — it is the thing that will actually decide whether macOS loads a
// bundle, so checking against anything else would only prove that the signer
// agrees with itself.

// ErrCodesignUnavailable is returned, wrapped, by Verify and Inspect when
// Apple's codesign is not on the PATH. It ships with macOS and exists nowhere
// else.
//
// That asymmetry is deliberate rather than an omission. Signing is portable:
// Sign runs anywhere. Checking a signature is not, because only codesign can
// say whether macOS will accept it. The alternative to this error is
// `exec: "codesign": executable file not found in $PATH` from a few frames
// down, which reads as a broken CI image rather than as a step that was never
// going to work on this machine.
var ErrCodesignUnavailable = errors.New("codesign is not available; it ships with macOS, " +
	"so sign here and verify on a Mac")

// VerifyOptions tunes Verify.
type VerifyOptions struct {
	// Deep checks the contents of a bundle as well as the bundle: nested code
	// such as an app extension, and every resource the seal covers. It also
	// applies codesign's --strict checks. Use it for an application; there is
	// nothing to look inside for a lone binary.
	Deep bool
}

// VerifyResult reports what codesign said about one path.
type VerifyResult struct {
	Path string

	// Valid is whether codesign accepted the signature.
	Valid bool

	// Output is codesign's own account, verbose, with surrounding whitespace
	// trimmed. It is the thing to show someone when Valid is false.
	Output string

	// Entitlements is the XML plist of entitlements the signature carries, if
	// it carries any.
	Entitlements string
}

// Verify asks codesign whether the signature on path is valid.
//
// The error is non-nil exactly when the signature is not valid, or codesign
// could not be run; in the first case the VerifyResult still holds what
// codesign printed.
func Verify(ctx context.Context, path string, opts VerifyOptions) (VerifyResult, error) {
	res := VerifyResult{Path: path}
	codesign, err := lookCodesign()
	if err != nil {
		return res, err
	}

	args := []string{"--verify", "--verbose=4"}
	if opts.Deep {
		args = append(args, "--deep", "--strict")
	}
	args = append(args, path)

	out, runErr := exec.CommandContext(ctx, codesign, args...).CombinedOutput()
	res.Output = strings.TrimSpace(string(out))
	res.Valid = runErr == nil

	// Entitlements are informational and do not decide validity, so a failure to
	// read them is not an error.
	if ents, err := exec.CommandContext(ctx, codesign, "-d", "--entitlements", "-", "--xml", path).Output(); err == nil {
		res.Entitlements = strings.TrimSpace(string(ents))
	}

	if runErr != nil {
		if ctx.Err() != nil {
			return res, fmt.Errorf("macbundle: verifying %s: %w", path, ctx.Err())
		}
		return res, fmt.Errorf("macbundle: codesign rejected %s: %s", path, res.Output)
	}
	return res, nil
}

// Signature describes how a binary or bundle is signed.
type Signature struct {
	// Signed is false for code that carries no signature at all. The other
	// fields are then zero.
	Signed bool

	// Identifier is the identifier in the code directory.
	Identifier string

	// TeamID is the Team ID the signing certificate carries, or empty if there is
	// none, which is the case for ad hoc signatures and for Apple's own platform
	// binaries.
	TeamID string

	// AdHoc reports a signature made without a certificate.
	AdHoc bool

	// HardenedRuntime reports whether the runtime flag is set in the code
	// directory. Notarisation requires it.
	HardenedRuntime bool
}

// Inspect reads how the code at path is signed.
//
// The distinction it exists to draw is the one that decides whether a build
// works, and it is invisible until it fails. A File Provider extension reaches
// its shared configuration through an App Group whose identifier is prefixed
// with the Team ID, and the sandbox grants that group only to code signed by
// that team. Ad hoc code belongs to no team, so the grant is refused and every
// read of the group container returns EPERM. Nothing about that is loud: the
// extension launches, resolves the container path, and is denied the file.
//
// An unsigned file is not an error; it is a Signature with Signed false.
func Inspect(ctx context.Context, path string) (Signature, error) {
	codesign, err := lookCodesign()
	if err != nil {
		return Signature{}, err
	}
	// -d -vv writes the code directory summary to stderr.
	out, runErr := exec.CommandContext(ctx, codesign, "-d", "-vv", path).CombinedOutput()
	if ctx.Err() != nil {
		return Signature{}, fmt.Errorf("macbundle: reading the signature of %s: %w", path, ctx.Err())
	}
	if runErr != nil {
		if bytes.Contains(out, []byte("not signed at all")) {
			return Signature{}, nil
		}
		return Signature{}, fmt.Errorf("macbundle: reading the signature of %s: %s",
			path, strings.TrimSpace(string(out)))
	}
	return parseSignature(string(out)), nil
}

// parseSignature reads codesign -d -vv output.
func parseSignature(out string) Signature {
	sig := Signature{Signed: true}
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Identifier="):
			sig.Identifier = strings.TrimPrefix(line, "Identifier=")

		case strings.HasPrefix(line, "TeamIdentifier="):
			sig.TeamID = strings.TrimPrefix(line, "TeamIdentifier=")
			// codesign spells the absent case "not set" rather than omitting the
			// line, which would otherwise read as a team called "not set".
			if sig.TeamID == "not set" {
				sig.TeamID = ""
			}

		case strings.HasPrefix(line, "Signature="):
			// Present only for ad hoc code. A signature made with a certificate
			// prints "Signature size=N" instead.
			sig.AdHoc = strings.TrimPrefix(line, "Signature=") == "adhoc"

		case strings.HasPrefix(line, "CodeDirectory "):
			sig.HardenedRuntime = codeDirectoryFlags(line)["runtime"]
		}
	}
	return sig
}

// codeDirectoryFlags extracts the named flags from a CodeDirectory line, as in
// "CodeDirectory v=20500 size=13888 flags=0x10000(runtime) hashes=...".
func codeDirectoryFlags(line string) map[string]bool {
	flags := map[string]bool{}
	_, rest, ok := strings.Cut(line, "flags=")
	if !ok {
		return flags
	}
	rest, _, _ = strings.Cut(rest, " ")
	_, names, ok := strings.Cut(rest, "(")
	if !ok {
		return flags
	}
	names, _, _ = strings.Cut(names, ")")
	for name := range strings.SplitSeq(names, ",") {
		flags[strings.TrimSpace(name)] = true
	}
	return flags
}

func lookCodesign() (string, error) {
	path, err := exec.LookPath("codesign")
	if err != nil {
		return "", fmt.Errorf("macbundle: %w", ErrCodesignUnavailable)
	}
	return path, nil
}
