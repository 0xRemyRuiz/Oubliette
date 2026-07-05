// Package transfer copies CRIU checkpoint images into a guest VM.
//
// The destination directory is visible on both host and guest through a
// virtiofs share configured on the domain, so "transfer" is a local file
// copy on the host into the shared directory, not a network operation.
package transfer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// CopyDump copies the contents of localDir into destDir, creating destDir
// (and the shared-directory tree above it) if it does not already exist.
// destDir must be a path under the host side of a virtiofs share so that
// the guest sees the same files at its corresponding mount point.
func CopyDump(localDir, destDir string) error {
	if _, err := os.Stat(localDir); err != nil {
		return fmt.Errorf("stat local dump dir %q: %w", localDir, err)
	}
	if err := os.MkdirAll(destDir, 0700); err != nil {
		return fmt.Errorf("create shared dump dir %q: %w", destDir, err)
	}
	// localDir+"/." copies the directory contents (not the directory name itself).
	out, err := exec.Command("cp", "-a", localDir+"/.", destDir).CombinedOutput()
	if err != nil {
		return fmt.Errorf("copy dump %q to %q: %s: %w", localDir, destDir, out, err)
	}
	return nil
}

// StageHelper copies the restore helper binary at localPath into destDir
// (the host side of a virtiofs share) under its base name, skipping the copy
// if a file already there is byte-for-byte identical -- so repeated
// migrations against an unchanged oubliette build don't recopy the binary
// every time.
func StageHelper(localPath, destDir string) error {
	if _, err := os.Stat(localPath); err != nil {
		return fmt.Errorf("stat helper binary %q: %w", localPath, err)
	}
	if err := os.MkdirAll(destDir, 0700); err != nil {
		return fmt.Errorf("create shared dir %q: %w", destDir, err)
	}
	destPath := filepath.Join(destDir, filepath.Base(localPath))

	localHash, err := fileSHA256(localPath)
	if err != nil {
		return fmt.Errorf("hash helper binary %q: %w", localPath, err)
	}
	destHash, err := fileSHA256(destPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("hash staged helper %q: %w", destPath, err)
	}
	if err == nil && destHash == localHash {
		return nil
	}

	out, err := exec.Command("cp", localPath, destPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("copy helper %q to %q: %s: %w", localPath, destPath, out, err)
	}
	if err := os.Chmod(destPath, 0755); err != nil {
		return fmt.Errorf("chmod helper %q: %w", destPath, err)
	}
	return nil
}

// fileSHA256 returns the hex-encoded SHA-256 digest of the file at path.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
