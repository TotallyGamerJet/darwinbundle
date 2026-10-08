# Security policy

## Reporting a vulnerability

Please report it privately through GitHub's *Report a vulnerability* button on
this repository's **Security** tab, rather than a public issue.

## What is in scope

- **The parsers** (`bom`, and anything built on it) read binary input that may
  come from an untrusted source. A panic, hang, unbounded allocation or
  out-of-range read on malformed input is a vulnerability.
- **Path handling** in `Bundle`, `appiconset` and `Zip`: anything that writes
  or reads outside the directories it was given.
- **Signing**: anything that causes a bundle to be signed with a different
  identity than the one configured, or that leaves key material somewhere it
  should not be.

## What is not

- Weaknesses in code *you* sign. This module puts a signature on what it is
  handed; what the code does is yours.
- Behaviour of Apple's tools, quill, or other dependencies. Report those to
  their maintainers; `govulncheck` runs here daily to tell us when one needs an
  update.
