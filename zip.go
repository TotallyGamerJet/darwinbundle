package macbundle

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// Archives are written with archive/zip rather than by invoking ditto(1).
//
// ditto is part of macOS, which makes it unavailable to a Linux CI runner. It is
// what people reach for because a bundle that loses its symlinks or its
// executable bits fails to launch on the far side, and the shell `zip` cannot
// be relied on to keep either.
//
// archive/zip does keep both. The mode goes into the external attributes with
// the Unix creator marker, which is what `unzip`, Archive Utility and ditto -x
// all read back, and a symlink is stored the way every Unix zip stores one: an
// entry whose mode says S_IFLNK and whose content is the target path.
//
// Two things ditto does that this deliberately does not:
//
//   - Extended attributes and resource forks. ditto carries them in a parallel
//     __MACOSX tree. A signed bundle does not need them: the signature lives
//     inside the Mach-O binaries and in _CodeSignature/CodeResources, both
//     ordinary files. The only xattrs a freshly built bundle carries are
//     com.apple.provenance and com.apple.macl, which the OS writes for its own
//     bookkeeping and which mean nothing on another machine.
//   - Choosing not to compress a file it decides is already compressed. Every
//     file here is deflated.
//
// Frameworks are the usual source of symlinked Versions/Current links, and the
// symlink handling below is what keeps them intact.

// Zip writes the directory tree at src into a zip archive at out.
//
// Entries are prefixed with src's own directory name, which is what
// `ditto -c -k --keepParent` does and what makes unpacking recreate Name.app
// rather than spilling Contents/ into the current directory.
//
// Empty directories are recorded, permissions and modification times are
// preserved, and anything that is not a regular file, a directory or a symlink
// is an error rather than a silent omission: an archive missing a file is a
// worse outcome than one that was never written.
func Zip(out, src string) (err error) {
	root := filepath.Clean(src)
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("macbundle: archiving %s: %w", src, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("macbundle: archiving %s: not a directory", src)
	}

	// An archive written inside the tree it is archiving would be walked into
	// itself, growing as it is read.
	if rel, rerr := filepath.Rel(root, filepath.Clean(out)); rerr == nil && filepath.IsLocal(rel) {
		return fmt.Errorf("macbundle: the archive %s is inside %s, which it would try to archive", out, src)
	}

	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return fmt.Errorf("macbundle: creating the directory for %s: %w", out, err)
	}
	f, err := os.Create(out)
	if err != nil {
		return fmt.Errorf("macbundle: creating %s: %w", out, err)
	}
	zw := zip.NewWriter(f)
	defer func() {
		// The central directory is written by the zip writer's Close, into the
		// file closed by the second one, so the order of these two matters.
		// Arguments are evaluated left to right, which is what fixes it.
		err = errors.Join(err, zw.Close(), f.Close())
		if err != nil {
			// A truncated archive that looks like a finished one is worse than
			// none: it unpacks to a bundle that is quietly missing files.
			err = errors.Join(err, removeIfExists(out))
		}
	}()

	parent := filepath.Base(root)
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		name := parent
		if rel != "." {
			name = path.Join(parent, filepath.ToSlash(rel))
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		return writeZipEntry(zw, p, name, fi)
	})
}

// writeZipEntry adds one filesystem node to the archive under name.
func writeZipEntry(zw *zip.Writer, p, name string, fi os.FileInfo) (err error) {
	h, err := zip.FileInfoHeader(fi)
	if err != nil {
		return fmt.Errorf("macbundle: describing %s: %w", p, err)
	}
	// FileInfoHeader names the entry after the file alone; the path within the
	// archive is ours to set. SetMode has already put the permission bits and
	// the file type into the external attributes, which is what carries the
	// executable bit across.
	h.Name = name

	switch {
	case fi.IsDir():
		// A directory entry is distinguished by the trailing slash, and this is
		// what keeps an empty Resources/ in the bundle.
		h.Name += "/"
		if _, err := zw.CreateHeader(h); err != nil {
			return fmt.Errorf("macbundle: adding %s: %w", p, err)
		}
		return nil

	case fi.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(p)
		if err != nil {
			return fmt.Errorf("macbundle: reading the link %s: %w", p, err)
		}
		// A link target is a few bytes and compresses to more than it saves.
		h.Method = zip.Store
		w, err := zw.CreateHeader(h)
		if err != nil {
			return fmt.Errorf("macbundle: adding %s: %w", p, err)
		}
		if _, err := io.WriteString(w, target); err != nil {
			return fmt.Errorf("macbundle: writing the link %s: %w", p, err)
		}
		return nil

	case fi.Mode().IsRegular():
		h.Method = zip.Deflate
		w, err := zw.CreateHeader(h)
		if err != nil {
			return fmt.Errorf("macbundle: adding %s: %w", p, err)
		}
		src, err := os.Open(p)
		if err != nil {
			return fmt.Errorf("macbundle: reading %s: %w", p, err)
		}
		defer func() { err = errors.Join(err, src.Close()) }()
		if _, err := io.Copy(w, src); err != nil {
			return fmt.Errorf("macbundle: copying %s: %w", p, err)
		}
		return nil

	default:
		// A socket, device or fifo in a bundle is a mistake somewhere upstream.
		// Skipping it would produce an archive that is quietly not the tree it
		// claims to be.
		return fmt.Errorf("macbundle: %s is a %s, which cannot be archived", p, fi.Mode().Type())
	}
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
