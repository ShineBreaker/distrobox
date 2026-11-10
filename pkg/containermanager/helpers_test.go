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

package containermanager_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/89luca89/distrobox/pkg/containermanager"
)

// TestPathExistsAndIsSymlink covers the symlink edge cases that drive mount
// decisions in makeCreateCommand (e.g. the /dev/shm symlink branch, selinux,
// /var/home). PathExists uses Stat (follows links); IsSymlink uses Lstat.
func TestPathExistsAndIsSymlink(t *testing.T) {
	assert.False(t, containermanager.PathExists("/does/not/exist"))

	dir := t.TempDir()
	assert.True(t, containermanager.PathExists(dir))
	assert.False(t, containermanager.IsSymlink(dir))

	// Broken symlink: Stat fails (false), Lstat sees the link (true).
	broken := filepath.Join(dir, "broken")
	require.NoError(t, os.Symlink("/nope/nope", broken))
	assert.False(t, containermanager.PathExists(broken))
	assert.True(t, containermanager.IsSymlink(broken))

	// Symlink to an existing target: both true.
	good := filepath.Join(dir, "good")
	require.NoError(t, os.Symlink(dir, good))
	assert.True(t, containermanager.PathExists(good))
	assert.True(t, containermanager.IsSymlink(good))
}

// TestFilterEnvVars pins the enter env allow/deny logic: excluded prefixes, the
// XDG_*_DIRS pattern, and values containing shell-special characters are dropped.
func TestFilterEnvVars(t *testing.T) {
	for _, k := range []string{"HOME", "PATH", "SHELL", "HOSTNAME", "XDG_SEAT", "XDG_CONFIG_DIRS", "_UNDERSCORE"} {
		t.Setenv(k, "x")
	}
	t.Setenv("DBX_TEST_KEEP", "keep")
	t.Setenv("DBX_TEST_BAD", "has$dollar") // '$' -> excluded

	has := func(prefix string) bool {
		for _, e := range containermanager.FilterEnvVars() {
			if strings.HasPrefix(e, prefix) {
				return true
			}
		}
		return false
	}

	assert.True(t, has("DBX_TEST_KEEP="))
	for _, dropped := range []string{
		"HOME=", "PATH=", "SHELL=", "HOSTNAME=", "XDG_SEAT=", "XDG_CONFIG_DIRS=", "_UNDERSCORE=", "DBX_TEST_BAD=",
	} {
		assert.False(t, has(dropped), dropped)
	}
}

// TestGetWorkDir covers the /run/host prefixing and the noWorkDir branch.
func TestGetWorkDir(t *testing.T) {
	wd, err := os.Getwd()
	require.NoError(t, err)

	got, err := containermanager.GetWorkDir("/home/tester", true) // noWorkDir -> container home
	require.NoError(t, err)
	assert.Equal(t, "/home/tester", got)

	got, err = containermanager.GetWorkDir(wd, false) // cwd under home -> as-is
	require.NoError(t, err)
	assert.Equal(t, wd, got)

	got, err = containermanager.GetWorkDir("/definitely/not/a/prefix/xyzzy", false) // outside -> /run/host
	require.NoError(t, err)
	assert.Equal(t, "/run/host"+wd, got)
}

// TestBuildXDGPaths covers dedup + append of standard paths onto the env value.
func TestBuildXDGPaths(t *testing.T) {
	t.Setenv("DBX_TEST_XDG", "/a:/b")
	assert.Equal(t, "/a:/b:/c", containermanager.BuildXDGPaths("DBX_TEST_XDG", []string{"/b", "/c"}))

	assert.Equal(t, "/usr/local/share:/usr/share",
		containermanager.BuildXDGPaths("DBX_TEST_XDG_UNSET_UNIQUE_9421", []string{"/usr/local/share", "/usr/share"}))
}

// TestBuildXDGPathsDropsHostOnlySegments pins that a Guix/Nix host's
// XDG_*_DIRS entries (which point at the store or a home profile) are removed
// segment by segment, leaving the container-side standard paths behind.
func TestBuildXDGPathsDropsHostOnlySegments(t *testing.T) {
	t.Setenv("DBX_TEST_XDG_STORE", "/gnu/store/abc-share:/home/u/.guix-home/profile/share:/home/u/.local/state/nix/profile/share:/usr/local/share")
	assert.Equal(t, "/usr/local/share:/usr/share",
		containermanager.BuildXDGPaths("DBX_TEST_XDG_STORE", []string{"/usr/local/share", "/usr/share"}))
}

// TestFilterEnvVarsWithBlacklist pins the user-facing env blacklist: extra
// names (including comma/colon-separated lists) are dropped on top of the
// built-in exclusions, without affecting other variables.
func TestFilterEnvVarsWithBlacklist(t *testing.T) {
	t.Setenv("SSL_CERT_FILE", "/gnu/store/does-not-exist.crt")
	t.Setenv("GUILE_LOAD_PATH", "/gnu/store/nowhere")
	t.Setenv("DBX_TEST_STILL_KEEP", "keep")

	has := func(prefix string) bool {
		for _, e := range containermanager.FilterEnvVarsWithBlacklist([]string{"SSL_CERT_FILE,GULE_LOAD_PATH:GUILE_LOAD_PATH"}) {
			if strings.HasPrefix(e, prefix) {
				return true
			}
		}
		return false
	}

	assert.False(t, has("SSL_CERT_FILE="), "blacklisted var must be dropped")
	assert.False(t, has("GUILE_LOAD_PATH="), "blacklisted var must be dropped (colon list)")
	assert.True(t, has("DBX_TEST_STILL_KEEP="), "non-blacklisted var must survive")
}

// TestFilterEnvVarsDropsHostOnlyStorePaths pins the value-based rule: any
// variable whose value references /gnu/, /nix/ or /run/current-system is
// dropped automatically, regardless of its name; variables with normal
// FHS values survive.
func TestFilterEnvVarsDropsHostOnlyStorePaths(t *testing.T) {
	t.Setenv("SSL_CERT_FILE", "/gnu/store/aaa-ca-certificates.crt")
	t.Setenv("GUILE_LOAD_PATH", "/gnu/store/bbb-guile/share")
	t.Setenv("SOMETHING_NIXY", "/nix/store/xxx-profile/lib")
	t.Setenv("SYSTEM_WIDE", "/run/current-system/profile/bin")
	t.Setenv("DBX_TEST_NORMAL", "/usr/share/data")
	t.Setenv("NOTES_DIR", "/home/user/gnu-project-notes") // contains "gnu" but not "/gnu/"

	has := func(prefix string) bool {
		for _, e := range containermanager.FilterEnvVars() {
			if strings.HasPrefix(e, prefix) {
				return true
			}
		}
		return false
	}

	assert.False(t, has("SSL_CERT_FILE="), "value in /gnu/store must be dropped")
	assert.False(t, has("GUILE_LOAD_PATH="), "value in /gnu/store must be dropped")
	assert.False(t, has("SOMETHING_NIXY="), "value in /nix/store must be dropped")
	assert.False(t, has("SYSTEM_WIDE="), "value in /run/current-system must be dropped")
	assert.True(t, has("DBX_TEST_NORMAL="), "normal FHS value must survive")
	assert.True(t, has("NOTES_DIR="), "substring 'gnu' outside a path marker must survive")
}

// TestFilterEnvVarsDropsHostOnlyProfilePaths pins the profile half of the
// value-based rule: Guix home/profile and Nix per-user profile paths are
// convenience symlinks into the store, so a value naming them is just as dead
// inside a container as one naming /gnu/store directly. This is what keeps
// SSL_CERT_FILE, XDG_*_DIRS and the GUIX_* family from leaking on a Guix host.
func TestFilterEnvVarsDropsHostOnlyProfilePaths(t *testing.T) {
	t.Setenv("SSL_CERT_FILE", "/home/user/.guix-home/profile/etc/ssl/certs/ca-certificates.crt")
	t.Setenv("GUIX_PROFILE", "/home/user/.guix-profile")
	t.Setenv("GUIX_LOCPATH", "/home/u/.guix-home/profile/lib/locale")
	t.Setenv("NIX_PROFILE_BIN", "/home/u/.local/state/nix/profile/bin")
	t.Setenv("DBX_TEST_NORMAL_PROFILE", "/home/u/.config/app/config.ini")

	has := func(prefix string) bool {
		for _, e := range containermanager.FilterEnvVars() {
			if strings.HasPrefix(e, prefix) {
				return true
			}
		}
		return false
	}

	assert.False(t, has("SSL_CERT_FILE="), "value in ~/.guix-home/profile must be dropped")
	assert.False(t, has("GUIX_PROFILE="), "value in ~/.guix-profile must be dropped")
	assert.False(t, has("GUIX_LOCPATH="), "value in ~/.guix-home/profile must be dropped")
	assert.False(t, has("NIX_PROFILE_BIN="), "value in ~/.local/state/nix/profile must be dropped")
	assert.True(t, has("DBX_TEST_NORMAL_PROFILE="), "an ordinary config path must survive")
}
