// SPDX-License-Identifier: GPL-3.0-only
//
// This file is part of the distrobox project:
//    https://github.com/89luca89/distrobox
//
// Copyright (C) 2021 distrobox contributors
//
// distrobox is free software; you can redistribute it and/or modify it
// under the terms of the GNU General Public License version 3
// as published by the Free Software Foundation.
//
// distrobox is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with distrobox; if not, see <http://www.gnu.org/licenses/>.

package insidedistrobox

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed assets/distrobox-host-exec
var hostExecScript string

//go:embed assets/distrobox-init
var initScript string

//go:embed assets/distrobox-export
var exportScripts string

// scriptFile is one in-container helper script and its embedded content.
type scriptFile struct {
	name    string
	content string
}

// ProvisionScripts ensures the helper scripts exist on disk and returns their
// directory: scriptsDir when they are already there and current (or read-only),
// else a per-user data dir (so a system-wide install still works when run
// rootless).
func ProvisionScripts(scriptsDir string) (string, error) {
	// A content-addressed store directory cannot serve as a container bind
	// source: its path embeds a content hash, so it changes with every package
	// upgrade and the previous one is eventually reclaimed by garbage
	// collection. Containers created against it would then fail to start, so
	// bind from a stable per-user directory instead.
	if isContentAddressedStore(scriptsDir) {
		scriptsDir = UserScriptsDir()
	}

	scripts := []scriptFile{
		{"distrobox-host-exec", hostExecScript},
		{"distrobox-init", initScript},
		{"distrobox-export", exportScripts},
	}

	// Reuse scripts already present (e.g. packaged), even if read-only, when
	// they are not stale relative to the embedded copies. A read-only dir is
	// always reusable: its scripts cannot be refreshed anyway.
	if allPresent(scriptsDir, scripts) {
		if !dirWritable(scriptsDir) || allCurrent(scriptsDir, scripts) {
			return scriptsDir, nil
		}
	}

	// Extract into scriptsDir, or a per-user dir when it is not writable.
	dir := scriptsDir
	//nolint:gosec // 0755 is the same as from distrobox v1, let's keep it for compatibility
	if err := os.MkdirAll(dir, 0755); err != nil || !dirWritable(dir) {
		dir = UserScriptsDir()
	}
	//nolint:gosec // 0755 is the same as from distrobox v1, let's keep it for compatibility
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create scripts directory %s: %w", dir, err)
	}

	for _, script := range scripts {
		destFilePath := filepath.Join(dir, script.name)
		// Skip only when the copy on disk already matches the embedded one;
		// a stale copy left by an older distrobox must be refreshed, or the
		// container would keep running an outdated entrypoint.
		if fileHasContent(destFilePath, script.content) {
			continue
		}
		//nolint:gosec // 0755 is the same as from distrobox v1, let's keep it for compatibility
		if err := os.WriteFile(destFilePath, []byte(script.content), 0755); err != nil {
			return "", fmt.Errorf("failed to write script %s: %w", script.name, err)
		}
	}

	return dir, nil
}

// allPresent reports whether every helper script exists in dir.
func allPresent(dir string, scripts []scriptFile) bool {
	for _, script := range scripts {
		if _, err := os.Stat(filepath.Join(dir, script.name)); err != nil {
			return false
		}
	}

	return true
}

// allCurrent reports whether every helper script in dir is byte-identical to
// its embedded copy.
func allCurrent(dir string, scripts []scriptFile) bool {
	for _, script := range scripts {
		if !fileHasContent(filepath.Join(dir, script.name), script.content) {
			return false
		}
	}

	return true
}

// fileHasContent reports whether path exists and holds exactly content.
func fileHasContent(path, content string) bool {
	b, err := os.ReadFile(path)

	return err == nil && string(b) == content
}

// isContentAddressedStore reports whether dir lives inside a content-addressed
// package store such as Guix's /gnu/store or Nix's /nix/store.
func isContentAddressedStore(dir string) bool {
	return strings.HasPrefix(dir, "/gnu/store/") ||
		strings.HasPrefix(dir, "/nix/store/")
}

// dirWritable reports whether a file can be created in dir (probes with a temp file).
func dirWritable(dir string) bool {
	probe, err := os.CreateTemp(dir, ".dbx-write-probe-")
	if err != nil {
		return false
	}
	defer func() {
		_ = probe.Close()
		_ = os.Remove(probe.Name())
	}()

	return true
}

// UserScriptsDir is the per-user fallback for extracted scripts.
func UserScriptsDir() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "distrobox")
	}
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".local", "share", "distrobox")
	}

	return filepath.Join(os.TempDir(), "distrobox")
}
