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

package insidedistrobox_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	insidedistrobox "github.com/89luca89/distrobox/internal/inside-distrobox"
)

// expectedScripts is the canonical triad of in-container helper scripts.
// ProvisionScripts must always end with all three present in the returned
// directory, regardless of which resolution branch produced it.
//
//nolint:gochecknoglobals // shared fixture across the suite, behaves like a constant
var expectedScripts = []string{
	"distrobox-host-exec",
	"distrobox-init",
	"distrobox-export",
}

func assertAllScripts(t *testing.T, dir string) {
	t.Helper()
	for _, name := range expectedScripts {
		assert.FileExists(t, filepath.Join(dir, name), "expected %s in %s", name, dir)
	}
}

// writes the scripts into a writable dir and returns it unchanged.
func TestProvisionScripts_CustomDir(t *testing.T) {
	tmpDir := t.TempDir()

	dir, err := insidedistrobox.ProvisionScripts(tmpDir)
	require.NoError(t, err)
	require.Equal(t, tmpDir, dir)
	assertAllScripts(t, dir)
}

// a stale copy in a writable dir is refreshed from the embedded content, so an
// upgraded distrobox does not leave an outdated entrypoint behind for new
// containers to bind.
func TestProvisionScripts_RefreshesStaleScripts(t *testing.T) {
	dir := t.TempDir()
	marker := "#!/bin/sh\n# pre-existing-marker\n"
	for _, name := range expectedScripts {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(marker), 0755))
	}

	got, err := insidedistrobox.ProvisionScripts(dir)
	require.NoError(t, err)
	require.Equal(t, dir, got)

	for _, name := range expectedScripts {
		b, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		assert.NotContains(t, string(b), "pre-existing-marker",
			"%s kept a stale copy", name)
		assert.NotEmpty(t, string(b), "%s ended up empty", name)
	}
}

// scripts already matching the embedded content are left untouched, so the
// common repeated-invocation path performs no writes.
func TestProvisionScripts_KeepsCurrentScripts(t *testing.T) {
	dir := t.TempDir()
	first, err := insidedistrobox.ProvisionScripts(dir)
	require.NoError(t, err)

	before := make(map[string]time.Time, len(expectedScripts))
	for _, name := range expectedScripts {
		info, err := os.Stat(filepath.Join(first, name))
		require.NoError(t, err)
		before[name] = info.ModTime()
	}

	time.Sleep(50 * time.Millisecond)

	second, err := insidedistrobox.ProvisionScripts(dir)
	require.NoError(t, err)
	require.Equal(t, first, second)

	for _, name := range expectedScripts {
		info, err := os.Stat(filepath.Join(second, name))
		require.NoError(t, err)
		assert.Equal(t, before[name], info.ModTime(),
			"%s was rewritten although already current", name)
	}
}

// A content-addressed store directory is never used as the scripts source: its
// path embeds a content hash, so it changes with every package upgrade and the
// old one is eventually reclaimed, which would leave every container created
// against it unstartable.
func TestProvisionScripts_RedirectsContentAddressedStore(t *testing.T) {
	storeDirs := []string{
		"/gnu/store/1cf89hacl99j85lflmbiv9dw2h9crqvq-distrobox-se/bin",
		"/nix/store/1cf89hacl99j85lflmbiv9dw2h9crqvq-distrobox-se/bin",
	}

	for _, storeDir := range storeDirs {
		home := t.TempDir()
		t.Setenv("XDG_DATA_HOME", "")
		t.Setenv("HOME", home)

		dir, err := insidedistrobox.ProvisionScripts(storeDir)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(home, ".local", "share", "distrobox"), dir,
			"%s must not be used as the scripts source", storeDir)
		assertAllScripts(t, dir)
	}
}

// A read-only install (e.g. a distro-packaged tree) is reused as-is even when
// its scripts carry distribution patches and differ from the embedded copies:
// they cannot be refreshed there anyway, and rewriting them is not our call.
func TestProvisionScripts_ReusesReadOnlyPackagedScripts(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write into any directory; cannot exercise the read-only path")
	}
	roDir := filepath.Join(t.TempDir(), "install")
	require.NoError(t, os.Mkdir(roDir, 0o755))
	marker := "#!/bin/sh\n# distro-patched\n"
	for _, name := range expectedScripts {
		require.NoError(t, os.WriteFile(filepath.Join(roDir, name), []byte(marker), 0755))
	}
	// Lock the dir only after the scripts are in place.
	require.NoError(t, os.Chmod(roDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o755) })

	dir, err := insidedistrobox.ProvisionScripts(roDir)
	require.NoError(t, err)
	require.Equal(t, roDir, dir)

	for _, name := range expectedScripts {
		b, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		assert.Equal(t, marker, string(b), "%s was rewritten in a read-only install", name)
	}
}

// TestExportBinaryProtectsExistingFiles runs the POSIX distrobox-export script
// through dash to ensure an existing host file is never replaced unless it is
// an export created by distrobox itself.
func TestExportBinaryProtectsExistingFiles(t *testing.T) {
	dashPath, err := exec.LookPath("dash")
	require.NoError(t, err)

	scriptsDir := t.TempDir()
	_, err = insidedistrobox.ProvisionScripts(scriptsDir)
	require.NoError(t, err)

	tmpDir := t.TempDir()
	guestBin := filepath.Join(tmpDir, "guest", "demo-tool")
	existingPath := filepath.Join(tmpDir, "existing")
	wrapperPath := filepath.Join(tmpDir, "wrapper")
	require.NoError(t, os.MkdirAll(filepath.Dir(guestBin), 0755))
	require.NoError(t, os.WriteFile(guestBin, []byte("#!/bin/sh\necho guest binary\n"), 0755))
	require.NoError(t, os.MkdirAll(existingPath, 0755))
	existingFile := filepath.Join(existingPath, "demo-tool")
	original := []byte("#!/bin/sh\necho host original\n")
	require.NoError(t, os.WriteFile(existingFile, original, 0755))

	t.Setenv("CONTAINER_ID", "first")
	t.Setenv("container", "1")
	t.Setenv("HOME", tmpDir)
	t.Setenv("DISTROBOX_HOST_HOME", tmpDir)
	t.Setenv("DISTROBOX_PATH", "distrobox")

	runExport := func(containerName, exportPath string, force bool) ([]byte, error) {
		t.Setenv("CONTAINER_ID", containerName)
		t.Setenv("DISTROBOX_EXPORT_PATH", exportPath)
		args := []string{"--bin", guestBin}
		if force {
			args = append(args, "--force")
		}
		cmd := exec.Command(dashPath, append([]string{filepath.Join(scriptsDir, "distrobox-export")}, args...)...)
		return cmd.CombinedOutput()
	}

	output, err := runExport("first", existingPath, false)
	require.Error(t, err)
	assert.Contains(t, string(output), "already exists")
	got, err := os.ReadFile(existingFile)
	require.NoError(t, err)
	assert.Equal(t, original, got)

	output, err = runExport("forced", existingPath, true)
	require.NoError(t, err, string(output))
	forced, err := os.ReadFile(existingFile)
	require.NoError(t, err)
	assert.Contains(t, string(forced), "# distrobox_binary")
	assert.Contains(t, string(forced), "# name: forced")

	output, err = runExport("first", wrapperPath, false)
	require.NoError(t, err, string(output))
	wrapperFile := filepath.Join(wrapperPath, "demo-tool")
	wrapper, err := os.ReadFile(wrapperFile)
	require.NoError(t, err)
	assert.Contains(t, string(wrapper), "# distrobox_binary")

	output, err = runExport("updated", wrapperPath, false)
	require.NoError(t, err, string(output))
	updated, err := os.ReadFile(wrapperFile)
	require.NoError(t, err)
	assert.Contains(t, string(updated), "# distrobox_binary")
	assert.Contains(t, string(updated), "# name: updated")
}

// TestProvisionScripts_ExtractsAdjacentToBinary verifies that ProvisionScripts
// writes to the given directory.
func TestProvisionScripts_ExtractsAdjacentToBinary(t *testing.T) {
	exe, err := os.Executable()
	require.NoError(t, err)
	exeDir := filepath.Dir(exe)
	if !isDirWritable(exeDir) {
		t.Skipf("binary-adjacent dir %s is not writable; cannot exercise this branch here", exeDir)
	}
	t.Cleanup(func() {
		for _, name := range expectedScripts {
			_ = os.Remove(filepath.Join(exeDir, name))
		}
	})

	dir, err := insidedistrobox.ProvisionScripts(exeDir)
	require.NoError(t, err)
	require.Equal(t, exeDir, dir)
	assertAllScripts(t, dir)
}

// an unwritable target dir falls back to the per-user data dir.
func TestProvisionScripts_FallsBackWhenUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write into any directory; cannot exercise the fallback")
	}
	roDir := filepath.Join(t.TempDir(), "install")
	require.NoError(t, os.Mkdir(roDir, 0o500))

	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", home)

	dir, err := insidedistrobox.ProvisionScripts(roDir)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, ".local", "share", "distrobox"), dir)
	assertAllScripts(t, dir)
}

// isDirWritable does a probing-write into dir to determine whether a
// non-root user can create files there. Used by the extraction-adjacent
// test as a defensive skip when the binary's directory isn't writable
// (e.g. read-only test harness, distros with hardened tmpfs).
func isDirWritable(dir string) bool {
	probe, err := os.CreateTemp(dir, ".dbx-write-probe-")
	if err != nil {
		return false
	}
	defer os.Remove(probe.Name())
	defer probe.Close()
	return true
}
