package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupOptimizedGitEnv(t *testing.T) {
	t.Helper()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(cwd, "testdata", "gitconfig"))
}

func createOptimizedLsRemoteRepo(t *testing.T) (string, string) {
	t.Helper()
	setupOptimizedGitEnv(t)

	ctx := t.Context()
	repoPath := t.TempDir()
	require.NoError(t, runCmd(ctx, repoPath, "git", "init"))
	require.NoError(t, runCmd(ctx, repoPath, "git", "config", "tag.gpgSign", "false"))
	require.NoError(t, runCmd(ctx, repoPath, "git", "checkout", "-b", "main"))
	require.NoError(t, runCmd(ctx, repoPath, "git", "commit", "-m", "main", "--allow-empty"))

	shaBytes, err := outputCmd(ctx, repoPath, "git", "rev-parse", "HEAD")
	require.NoError(t, err)
	sha := strings.TrimSpace(string(shaBytes))
	require.NoError(t, runCmd(ctx, repoPath, "git", "tag", "v1.0.0"))
	require.NoError(t, runCmd(ctx, repoPath, "git", "tag", "-a", "annotated", "-m", "annotated"))
	require.NoError(t, runCmd(ctx, repoPath, "git", "update-ref", "refs/pull/123/head", sha))
	return repoPath, sha
}

func TestOptimizedLsRemoteRefPrefixPlanBackport(t *testing.T) {
	repoURL := "https://example.com/repo.git"
	for _, tc := range []struct {
		name        string
		prefixes    []string
		wantArgs    []string
		wantCache   []string
		wantPlanned bool
	}{
		{
			name:        "heads and tags",
			prefixes:    []string{"refs/heads/", "refs/tags/"},
			wantArgs:    []string{"ls-remote", "--heads", "--tags", repoURL},
			wantCache:   []string{"HEAD", "heads", "tags"},
			wantPlanned: true,
		},
		{
			name:        "normalizes prefixes",
			prefixes:    []string{" refs/heads ", "refs/tags"},
			wantArgs:    []string{"ls-remote", "--heads", "--tags", repoURL},
			wantCache:   []string{"HEAD", "heads", "tags"},
			wantPlanned: true,
		},
		{
			name:        "unsupported prefixes",
			prefixes:    []string{"refs/pull/", "refs/changes/"},
			wantPlanned: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := nativeGitClient{
				repoURL:                      repoURL,
				optimizedLsRemoteRefPrefixes: normalizeOptimizedLsRemoteRefPrefixes(tc.prefixes),
			}

			args, cacheParts, planned := client.optimizedLsRemoteRefPrefixPlan()
			assert.Equal(t, tc.wantPlanned, planned)
			assert.Equal(t, tc.wantArgs, args)
			assert.Equal(t, tc.wantCache, cacheParts)
		})
	}
}

func TestOptimizedLsRemoteBackport(t *testing.T) {
	repoPath, expectedSHA := createOptimizedLsRemoteRepo(t)
	client, err := NewClientExt(
		"file://"+repoPath,
		filepath.Join(t.TempDir(), "client"),
		NopCreds{},
		true,
		false,
		"",
		"",
		WithOptimizedLsRemote(true, []string{"refs/heads/", "refs/tags/"}),
	)
	require.NoError(t, err)

	for _, revision := range []string{
		"",
		"HEAD",
		"main",
		"refs/heads/main",
		"v1.0.0",
		"refs/tags/v1.0.0",
		"v1.*",
		"annotated",
		"refs/pull/123/head",
	} {
		t.Run(revision, func(t *testing.T) {
			sha, err := client.LsRemote(revision)
			require.NoError(t, err)
			assert.Equal(t, expectedSHA, sha)
		})
	}
}

func TestOptimizedLsRemoteIgnoresUnusableClientRootBackport(t *testing.T) {
	repoPath, expectedSHA := createOptimizedLsRemoteRepo(t)
	clientRoot := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(clientRoot, nil, 0o600))

	client, err := NewClientExt(
		"file://"+repoPath,
		clientRoot,
		NopCreds{},
		true,
		false,
		"",
		"",
		WithOptimizedLsRemote(true, []string{"refs/heads/", "refs/tags/"}),
	)
	require.NoError(t, err)

	sha, handled, err := client.(*nativeGitClient).lsRemoteOptimized("HEAD")
	require.NoError(t, err)
	assert.True(t, handled)
	assert.Equal(t, expectedSHA, sha)
}

func TestOptimizedLsRemoteHexLookingTagFallsBackBackport(t *testing.T) {
	repoPath, expectedSHA := createOptimizedLsRemoteRepo(t)
	require.NoError(t, runCmd(t.Context(), repoPath, "git", "tag", "20240101"))

	client, err := NewClientExt(
		"file://"+repoPath,
		filepath.Join(t.TempDir(), "client"),
		NopCreds{},
		true,
		false,
		"",
		"",
		WithOptimizedLsRemote(true, []string{"refs/heads/"}),
	)
	require.NoError(t, err)

	sha, err := client.LsRemote("20240101")
	require.NoError(t, err)
	assert.Equal(t, expectedSHA, sha)
}

func TestOptimizedLsRemoteCoveredMissDoesNotFallbackBackport(t *testing.T) {
	repoPath, _ := createOptimizedLsRemoteRepo(t)
	lsRemoteCalls := 0
	client, err := NewClientExt(
		"file://"+repoPath,
		filepath.Join(t.TempDir(), "client"),
		NopCreds{},
		true,
		false,
		"",
		"",
		WithOptimizedLsRemote(true, []string{"refs/heads/", "refs/tags/"}),
		WithEventHandlers(EventHandlers{
			OnLsRemote: func(string) func() {
				lsRemoteCalls++
				return func() {}
			},
		}),
	)
	require.NoError(t, err)

	_, err = client.LsRemote("refs/heads/missing")
	require.ErrorContains(t, err, "unable to resolve 'refs/heads/missing'")
	assert.Equal(t, 2, lsRemoteCalls)
}
