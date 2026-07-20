package scanner

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// maxExtractBytes caps total uncompressed archive size written to tmpfs, guarding
// against zip/tar bombs exhausting RAM (2g tmpfs). [SC-07] resource bound.
const maxExtractBytes = 1 << 30 // 1 GiB

// Acquire materializes the SAST source tree under a per-job tmpfs directory and
// returns cleanup that removes it. [EPHEM-01]: the caller MUST `defer cleanup()`
// immediately so the source is destroyed on every exit path (success/error/
// timeout/ctx-cancel/panic). cleanup is always non-nil and idempotent.
//
//	workRoot        e.g. /scan-work (tmpfs, per design #2)
//	srcStagingRoot  e.g. /scan-src  (shared tmpfs volume; upload staging)
func Acquire(ctx context.Context, workRoot, srcStagingRoot string, s Source) (dir string, cleanup func(), err error) {
	noop := func() {}
	dir, err = os.MkdirTemp(workRoot, "job-*")
	if err != nil {
		return "", noop, fmt.Errorf("mkdir tmpfs workdir: %w", err)
	}
	rm := func() { _ = os.RemoveAll(dir) }

	switch s.Type {
	case "repo":
		if err = gitClone(ctx, s.RepoURL, s.Ref, dir); err != nil {
			rm()
			return "", noop, err
		}
		return dir, rm, nil

	case "upload":
		staging, ferr := stagingPath(srcStagingRoot, s.Token)
		if ferr != nil {
			rm()
			return "", noop, ferr
		}
		if err = extractArchive(staging, dir); err != nil {
			rm()
			return "", noop, err
		}
		_ = os.Remove(staging) // consume staging as soon as extracted
		cleanup = func() { rm(); _ = os.Remove(staging) }
		return dir, cleanup, nil

	default:
		rm()
		return "", noop, fmt.Errorf("unknown source type %q", s.Type)
	}
}

// gitClone performs a hardened shallow clone into an existing empty dir (C-1/H-1).
// repoURL/ref are validated (https-only, SSRF gate, no option-looking values); the
// dangerous ext/file/ftp transports are disabled; "--" separates options from the
// URL so a crafted value can't inject git arguments; credential prompts are off.
func gitClone(ctx context.Context, repoURL, ref, dir string) error {
	if err := ValidateRepoURL(repoURL); err != nil {
		return err
	}
	if err := ValidateRef(ref); err != nil {
		return err
	}
	args := []string{
		"-c", "protocol.ext.allow=never",
		"-c", "protocol.file.allow=never",
		"-c", "protocol.ftp.allow=never",
		"-c", "protocol.ftps.allow=never",
		"clone", "--depth", "1", "--single-branch", "--no-tags",
	}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	// "--" ends option parsing: repoURL/dir are always treated as operands.
	args = append(args, "--", repoURL, dir)
	cmd := exec.CommandContext(ctx, "git", args...)
	// No credential prompts, no system/global git config influence.
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/true",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone: %w: %s", err, truncate(string(out), 300))
	}
	return nil
}

// stagingPath resolves the uploaded archive under srcStagingRoot by token. The
// upload handler writes "<token><ext>"; we locate the single matching file.
func stagingPath(root, token string) (string, error) {
	if token == "" {
		return "", fmt.Errorf("upload token required")
	}
	if strings.ContainsAny(token, "/\\.") {
		return "", fmt.Errorf("invalid upload token")
	}
	matches, _ := filepath.Glob(filepath.Join(root, token+".*"))
	if len(matches) == 0 {
		return "", fmt.Errorf("upload staging for token not found (expired?)")
	}
	return matches[0], nil
}

// extractArchive extracts a .zip or .tar[.gz] into destDir with path-traversal
// (zip-slip) protection and a total-size cap.
func extractArchive(archivePath, destDir string) error {
	lower := strings.ToLower(archivePath)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return extractZip(archivePath, destDir)
	case strings.HasSuffix(lower, ".tar"), strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return extractTar(archivePath, destDir)
	default:
		return fmt.Errorf("unsupported archive extension: %s", filepath.Ext(archivePath))
	}
}

// safeJoin rejects archive entries that would escape destDir (zip-slip). Absolute
// paths and ".." traversal are refused rather than silently neutralized.
func safeJoin(destDir, name string) (string, error) {
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "\\") {
		return "", fmt.Errorf("archive entry has absolute path: %s", name)
	}
	target := filepath.Join(destDir, name)
	if target != destDir && !strings.HasPrefix(target, destDir+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive entry escapes destination: %s", name)
	}
	return target, nil
}

func extractZip(archivePath, destDir string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer zr.Close()
	var total int64
	for _, f := range zr.File {
		target, err := safeJoin(destDir, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		n, err := writeCapped(target, rc, &total)
		rc.Close()
		if err != nil {
			return err
		}
		_ = n
	}
	return nil
}

func extractTar(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open tar: %w", err)
	}
	defer f.Close()
	var src io.Reader = f
	if strings.HasSuffix(strings.ToLower(archivePath), ".gz") || strings.HasSuffix(strings.ToLower(archivePath), ".tgz") {
		gz, gerr := gzip.NewReader(f)
		if gerr != nil {
			return fmt.Errorf("gzip: %w", gerr)
		}
		defer gz.Close()
		src = gz
	}
	tr := tar.NewReader(src)
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar next: %w", err)
		}
		target, err := safeJoin(destDir, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if _, err := writeCapped(target, tr, &total); err != nil {
				return err
			}
			// symlinks and other special types are skipped intentionally.
		}
	}
	return nil
}

func writeCapped(target string, r io.Reader, total *int64) (int64, error) {
	out, err := os.Create(target)
	if err != nil {
		return 0, err
	}
	defer out.Close()
	limit := maxExtractBytes - *total
	if limit <= 0 {
		return 0, fmt.Errorf("archive exceeds %d byte extraction cap", maxExtractBytes)
	}
	n, err := io.Copy(out, io.LimitReader(r, limit+1))
	*total += n
	if n > limit {
		return n, fmt.Errorf("archive exceeds %d byte extraction cap", maxExtractBytes)
	}
	return n, err
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
