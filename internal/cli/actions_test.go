package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func writeFakeVoxBinary(t *testing.T, path, version string) {
	t.Helper()
	script := fmt.Sprintf("#!/usr/bin/env bash\nif [[ \"${1:-}\" == \"--version\" ]]; then\n  echo 'vox version %s (commit abcdef123456), built 2026-11-27T00:00:00Z'\n  exit 0\nfi\necho \"unexpected invocation: $*\" >&2\nexit 1\n", version)
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
}

func writeTarGzArchive(t *testing.T, archivePath, binaryPath string) string {
	t.Helper()
	data, err := os.ReadFile(binaryPath)
	require.NoError(t, err)

	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	require.NoError(t, tarWriter.WriteHeader(&tar.Header{
		Name: "vox",
		Mode: 0o755,
		Size: int64(len(data)),
	}))
	_, err = tarWriter.Write(data)
	require.NoError(t, err)
	require.NoError(t, tarWriter.Close())
	require.NoError(t, gzipWriter.Close())

	require.NoError(t, os.WriteFile(archivePath, buffer.Bytes(), 0o600))
	sum := sha256.Sum256(buffer.Bytes())
	return hex.EncodeToString(sum[:])
}

func TestActionsSetupInstallCachingAndPreseededBinaries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("setup action targets Linux and macOS runners")
	}

	root := repoRoot(t)
	installScript := filepath.Join(root, "actions", "setup", "install.sh")

	fixtureDir := t.TempDir()
	fakeVoxSource := filepath.Join(fixtureDir, "vox")
	writeFakeVoxBinary(t, fakeVoxSource, "0.2.0")

	archiveName := "vox_0.2.0_Linux_x86_64.tar.gz"
	archivePath := filepath.Join(fixtureDir, archiveName)
	archiveSHA := writeTarGzArchive(t, archivePath, fakeVoxSource)
	checksumsPath := filepath.Join(fixtureDir, "checksums.txt")
	require.NoError(t, os.WriteFile(checksumsPath, fmt.Appendf(nil, "%s  %s\n", archiveSHA, archiveName), 0o600))

	// Mock curl that serves the local fixture files and counts invocations.
	curlLog := filepath.Join(fixtureDir, "curl.log")
	mockBinDir := filepath.Join(fixtureDir, "mock-bin")
	require.NoError(t, os.MkdirAll(mockBinDir, 0o755))
	mockCurl := filepath.Join(mockBinDir, "curl")
	mockCurlScript := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
echo "$@" >> %q
out=""
url=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output) out="$2"; shift 2 ;;
    --retry|--connect-timeout|--max-time|--proto|--proto-redir) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
case "$url" in
  */checksums.txt) cp %q "$out" ;;
  */%s) cp %q "$out" ;;
  *) echo "unexpected curl url: $url" >&2; exit 22 ;;
esac
`, curlLog, checksumsPath, archiveName, archivePath)
	require.NoError(t, os.WriteFile(mockCurl, []byte(mockCurlScript), 0o755))

	toolCache := filepath.Join(t.TempDir(), "tool-cache")
	runnerTemp := t.TempDir()

	runInstall := func(t *testing.T, extraEnv map[string]string, pathPrefix string) (string, string, string) {
		t.Helper()
		githubPath := filepath.Join(t.TempDir(), "github_path")
		githubOutput := filepath.Join(t.TempDir(), "github_output")
		require.NoError(t, os.WriteFile(githubPath, nil, 0o600))
		require.NoError(t, os.WriteFile(githubOutput, nil, 0o600))

		cmd := exec.Command("bash", installScript)
		env := make([]string, 0, 8+len(extraEnv))
		env = append(env,
			"PATH="+pathPrefix+string(os.PathListSeparator)+"/usr/bin:/bin",
			"VOX_VERSION=v0.2.0",
			"RUNNER_OS=Linux",
			"RUNNER_ARCH=X64",
			"RUNNER_TEMP="+runnerTemp,
			"RUNNER_TOOL_CACHE="+toolCache,
			"GITHUB_PATH="+githubPath,
			"GITHUB_OUTPUT="+githubOutput,
		)
		for key, value := range extraEnv {
			env = append(env, key+"="+value)
		}
		cmd.Env = env
		combined, err := cmd.CombinedOutput()
		require.NoError(t, err, "install.sh failed:\n%s", string(combined))

		outBytes, err := os.ReadFile(githubOutput)
		require.NoError(t, err)
		pathBytes, err := os.ReadFile(githubPath)
		require.NoError(t, err)
		return string(combined), string(outBytes), string(pathBytes)
	}

	// 1. Cold install downloads once and populates the tool cache.
	_, outputs, addedPath := runInstall(t, nil, mockBinDir)
	expectedCachedBin := filepath.Join(toolCache, "vox", "0.2.0", "x86_64", "vox")
	assert.Contains(t, outputs, "path="+expectedCachedBin+"\n")
	assert.Contains(t, outputs, "cache-hit=false\n")
	assert.Contains(t, addedPath, filepath.Dir(expectedCachedBin)+"\n")
	_, err := os.Stat(filepath.Join(filepath.Dir(expectedCachedBin), "vox.sha256"))
	require.NoError(t, err)

	// 2. Warm tool-cache hit skips curl completely (replace curl with a failing stub).
	noCurlDir := filepath.Join(t.TempDir(), "no-curl")
	require.NoError(t, os.MkdirAll(noCurlDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(noCurlDir, "curl"), []byte("#!/usr/bin/env bash\necho 'curl should not run' >&2\nexit 99\n"), 0o755))

	_, outputs, _ = runInstall(t, nil, noCurlDir)
	assert.Contains(t, outputs, "path="+expectedCachedBin+"\n")
	assert.Contains(t, outputs, "cache-hit=true\n")

	// 3. Tampered cached binary fails SHA-256 verification and triggers fresh download.
	require.NoError(t, os.WriteFile(expectedCachedBin, []byte("#!/usr/bin/env bash\necho corrupted\n"), 0o755))
	stdout, outputs, _ := runInstall(t, nil, mockBinDir)
	assert.Contains(t, stdout, "::warning::Cached Vox binary")
	assert.Contains(t, outputs, "path="+expectedCachedBin+"\n")
	assert.Contains(t, outputs, "cache-hit=false\n")

	// 4. Pre-seeded VOX_BIN matching requested version skips download and reports cache-hit=true.
	preseededBin := filepath.Join(t.TempDir(), "vox")
	writeFakeVoxBinary(t, preseededBin, "0.2.0")
	_, outputs, addedPath = runInstall(t, map[string]string{"VOX_BIN": preseededBin}, noCurlDir)
	assert.Contains(t, outputs, "path="+preseededBin+"\n")
	assert.Contains(t, outputs, "cache-hit=true\n")
	assert.Contains(t, addedPath, filepath.Dir(preseededBin)+"\n")

	// 5. Existing vox on PATH matching requested version skips download and reports cache-hit=true.
	pathVoxDir := t.TempDir()
	pathVoxBin := filepath.Join(pathVoxDir, "vox")
	writeFakeVoxBinary(t, pathVoxBin, "0.2.0")
	_, outputs, addedPath = runInstall(t, nil, pathVoxDir+string(os.PathListSeparator)+noCurlDir)
	assert.Contains(t, outputs, "path="+pathVoxBin+"\n")
	assert.Contains(t, outputs, "cache-hit=true\n")
	assert.Empty(t, strings.TrimSpace(addedPath))

	// 6. Wrong version on PATH does not satisfy v0.2.0 and falls back to cache/download.
	oldVoxDir := t.TempDir()
	writeFakeVoxBinary(t, filepath.Join(oldVoxDir, "vox"), "0.1.0")
	_, outputs, _ = runInstall(t, nil, oldVoxDir+string(os.PathListSeparator)+noCurlDir)
	assert.Contains(t, outputs, "path="+expectedCachedBin+"\n")
	assert.Contains(t, outputs, "cache-hit=true\n")
}

func TestActionsNotifyPostWithoutJQAndNewInputs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("notify action targets Linux and macOS runners")
	}

	root := repoRoot(t)
	postScript := filepath.Join(root, "actions", "notify", "post.sh")

	// Build a minimal PATH directory containing only core POSIX utilities (explicitly no jq).
	noJQDir := t.TempDir()
	for _, tool := range []string{"bash", "cat", "grep", "mktemp", "rm", "sed", "tr"} {
		resolved, err := exec.LookPath(tool)
		require.NoError(t, err)
		require.NoError(t, os.Symlink(resolved, filepath.Join(noJQDir, tool)))
	}

	// Stub vox binary that records argv and simulates --output github-output behavior.
	argsFile := filepath.Join(t.TempDir(), "vox-args.txt")
	stubVox := filepath.Join(t.TempDir(), "vox")
	stubScript := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
printf '%%s\n' "$@" > %q
case "${VOX_STUB_MODE:-single}" in
  single)
    printf 'message-id=789\nmessage-ids=["789"]\ndelivered=true\n'
    ;;
  batch)
    printf 'message-id=\nmessage-ids=["101","102"]\ndelivered=true\n'
    ;;
  best-effort-fail)
    echo 'warning: Discord did not confirm the webhook message before the timeout.' >&2
    printf 'message-id=\nmessage-ids=[]\ndelivered=false\n'
    ;;
  hard-fail)
    echo 'Error: Discord webhook delivery failed (HTTP 500, Discord code 0).' >&2
    exit 1
    ;;
esac
`, argsFile)
	require.NoError(t, os.WriteFile(stubVox, []byte(stubScript), 0o755))

	runPost := func(t *testing.T, extraEnv map[string]string) (string, string, []string, error) {
		t.Helper()
		_ = os.Remove(argsFile)
		githubOutput := filepath.Join(t.TempDir(), "github_output")
		require.NoError(t, os.WriteFile(githubOutput, nil, 0o600))

		cmd := exec.Command("bash", postScript)
		env := make([]string, 0, 4+len(extraEnv))
		env = append(env,
			"PATH="+noJQDir,
			"RUNNER_TEMP="+t.TempDir(),
			"GITHUB_OUTPUT="+githubOutput,
			"VOX_BIN="+stubVox,
		)
		for key, value := range extraEnv {
			env = append(env, key+"="+value)
		}
		cmd.Env = env
		combined, err := cmd.CombinedOutput()
		outBytes, readErr := os.ReadFile(githubOutput)
		require.NoError(t, readErr)

		var recordedArgs []string
		if argBytes, argErr := os.ReadFile(argsFile); argErr == nil {
			recordedArgs = strings.Split(strings.TrimSuffix(string(argBytes), "\n"), "\n")
		}
		return string(combined), string(outBytes), recordedArgs, err
	}

	t.Run("works without jq and sets message-id, message-ids, delivered", func(t *testing.T) {
		_, outputs, recordedArgs, err := runPost(t, map[string]string{
			"DISCORD_WEBHOOK_URL":      "https://discord.com/api/webhooks/123/secret",
			"VOX_NOTIFY_MESSAGE":       "Build passed",
			"VOX_NOTIFY_TITLE":         "CI",
			"VOX_NOTIFY_STATUS":        "cancelled",
			"VOX_NOTIFY_TRUNCATE":      "true",
			"VOX_NOTIFY_ESCAPE_FIELDS": "true",
		})
		require.NoError(t, err)
		assert.Equal(t, "message-id=789\nmessage-ids=[\"789\"]\ndelivered=true\n", outputs)
		assert.Contains(t, recordedArgs, "--output")
		assert.Contains(t, recordedArgs, "github-output")
		assert.Contains(t, recordedArgs, "--truncate")
		assert.Contains(t, recordedArgs, "--escape-fields")
		// Cancelled uses its own slate color (607D8B), distinct from warning (F39C12).
		assert.Contains(t, recordedArgs, "607D8B")
	})

	t.Run("raw hex color input forwards to --embed-color and rejects status+color", func(t *testing.T) {
		_, outputs, recordedArgs, err := runPost(t, map[string]string{
			"DISCORD_WEBHOOK_URL": "https://discord.com/api/webhooks/123/secret",
			"VOX_NOTIFY_MESSAGE":  "Disk pressure",
			"VOX_NOTIFY_COLOR":    "#E74C3C",
		})
		require.NoError(t, err)
		assert.Contains(t, outputs, "delivered=true")
		assert.Contains(t, recordedArgs, "--embed-color")
		assert.Contains(t, recordedArgs, "#E74C3C")

		combined, _, _, err := runPost(t, map[string]string{
			"DISCORD_WEBHOOK_URL": "https://discord.com/api/webhooks/123/secret",
			"VOX_NOTIFY_MESSAGE":  "Conflict",
			"VOX_NOTIFY_STATUS":   "warning",
			"VOX_NOTIFY_COLOR":    "E74C3C",
		})
		require.Error(t, err)
		assert.Contains(t, combined, "::error::Use either status or color, not both.")
	})

	t.Run("required false skips empty webhook cleanly", func(t *testing.T) {
		combined, outputs, recordedArgs, err := runPost(t, map[string]string{
			"DISCORD_WEBHOOK_URL": "",
			"VOX_NOTIFY_MESSAGE":  "Release published",
			"VOX_NOTIFY_REQUIRED": "false",
		})
		require.NoError(t, err)
		assert.Empty(t, recordedArgs)
		assert.Contains(t, combined, "::warning::DISCORD_WEBHOOK_URL is not configured")
		assert.Equal(t, "message-id=\nmessage-ids=[]\ndelivered=false\n", outputs)
	})

	t.Run("fail-on-delivery-error false passes --best-effort and emits warning annotation", func(t *testing.T) {
		combined, outputs, recordedArgs, err := runPost(t, map[string]string{
			"DISCORD_WEBHOOK_URL":               "https://discord.com/api/webhooks/123/secret",
			"VOX_NOTIFY_MESSAGE":                "Check alert",
			"VOX_NOTIFY_FAIL_ON_DELIVERY_ERROR": "false",
			"VOX_STUB_MODE":                     "best-effort-fail",
		})
		require.NoError(t, err)
		assert.Contains(t, recordedArgs, "--best-effort")
		assert.Contains(t, combined, "::warning::Discord did not confirm the webhook message before the timeout.")
		assert.Equal(t, "message-id=\nmessage-ids=[]\ndelivered=false\n", outputs)
	})

	t.Run("multi-payload batch writes message-ids JSON array", func(t *testing.T) {
		_, outputs, recordedArgs, err := runPost(t, map[string]string{
			"DISCORD_WEBHOOK_URL": "https://discord.com/api/webhooks/123/secret",
			"VOX_NOTIFY_PAYLOAD":  "./alerts.jsonl",
			"VOX_STUB_MODE":       "batch",
		})
		require.NoError(t, err)
		assert.Contains(t, recordedArgs, "--payload-file")
		assert.Contains(t, recordedArgs, "./alerts.jsonl")
		assert.Equal(t, "message-id=\nmessage-ids=[\"101\",\"102\"]\ndelivered=true\n", outputs)
	})
}
