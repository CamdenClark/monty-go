package monty

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// RuntimeVersion is the version of the Monty worker installed by this wrapper.
const RuntimeVersion = "1.2.0"

// CacheDirEnv overrides the directory used for installed Monty runtimes.
const CacheDirEnv = "MONTY_CACHE_DIR"

const (
	maxRuntimeArchiveSize  = 64 << 20
	maxRuntimeUnpackedSize = 128 << 20
	maxRuntimeBinarySize   = 64 << 20
	installLockStaleAfter  = 5 * time.Minute
)

// InstallOptions configures installation of the Monty worker for the current
// operating system and architecture.
type InstallOptions struct {
	// CacheDir overrides MONTY_CACHE_DIR and the operating system's user cache.
	CacheDir string
	// Force downloads and verifies the runtime even when a valid cached copy exists.
	Force bool
}

type runtimeArtifact struct {
	goos          string
	goarch        string
	url           string
	archiveSHA256 string
	binarySHA256  string
	member        string
}

var runtimeArtifacts = map[string]runtimeArtifact{
	"darwin/arm64": {
		goos:          "darwin",
		goarch:        "arm64",
		url:           "https://registry.npmjs.org/@pydantic/monty-darwin-arm64/-/monty-darwin-arm64-1.2.0.tgz",
		archiveSHA256: "5f2e4aa0b7177dfbb9527988b64f4cc3f33e2ca4188134f274201309a1b2c3ff",
		binarySHA256:  "bbff17e025871b6e6f5ac464c757ac5717ab32e328381c6a914190b80eec5c24",
		member:        "package/monty",
	},
	"darwin/amd64": {
		goos:          "darwin",
		goarch:        "amd64",
		url:           "https://registry.npmjs.org/@pydantic/monty-darwin-x64/-/monty-darwin-x64-1.2.0.tgz",
		archiveSHA256: "d20d2181f18a31c2d5c316a8fdd29bd4b5ca0c57260379d658f4f295c1696823",
		binarySHA256:  "a2dd4831bec846a559d4e1a4148dad1a937820abf8d3129ec3ec91d0b4633b29",
		member:        "package/monty",
	},
	"linux/arm64": {
		goos:          "linux",
		goarch:        "arm64",
		url:           "https://registry.npmjs.org/@pydantic/monty-linux-arm64-gnu/-/monty-linux-arm64-gnu-1.2.0.tgz",
		archiveSHA256: "8fb2314c955f1cf92fe12e0a23391d4059f4c2ff7de29745be2e09a60cc29773",
		binarySHA256:  "24691f2bdbf6eb6f60862adf801b619ca5c10d35147de37833dbceab9ac2ea6d",
		member:        "package/monty",
	},
	"linux/amd64": {
		goos:          "linux",
		goarch:        "amd64",
		url:           "https://registry.npmjs.org/@pydantic/monty-linux-x64-gnu/-/monty-linux-x64-gnu-1.2.0.tgz",
		archiveSHA256: "6f791fcf4dcb45230970d67f78a12d9a1e53198e6118d0354a54ffb0126f0ac6",
		binarySHA256:  "d36ff64238568cd0fa6e152f5359512b7cd78acc000a0659fe1823ebf54b85b5",
		member:        "package/monty",
	},
	"windows/amd64": {
		goos:          "windows",
		goarch:        "amd64",
		url:           "https://registry.npmjs.org/@pydantic/monty-win32-x64-msvc/-/monty-win32-x64-msvc-1.2.0.tgz",
		archiveSHA256: "819a2c007060ba2942edaf6170706ae90f29fab83af92b8ecfa32bb31f25889e",
		binarySHA256:  "f35610bf02a9050d9bd5bbe0b9cf7d2ce43c4905a51db79b074291195dff34cd",
		member:        "package/monty.exe",
	},
}

// RuntimeCacheDir returns the default root used for installed runtimes. It
// honors MONTY_CACHE_DIR.
func RuntimeCacheDir() (string, error) {
	return resolveRuntimeCacheDir("")
}

func resolveRuntimeCacheDir(explicit string) (string, error) {
	dir := explicit
	if dir == "" {
		dir = os.Getenv(CacheDirEnv)
	}
	if dir == "" {
		userCache, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("resolve Monty runtime cache: %w", err)
		}
		dir = filepath.Join(userCache, "monty-go")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve Monty runtime cache %q: %w", dir, err)
	}
	return abs, nil
}

func currentRuntimeArtifact() (runtimeArtifact, error) {
	return runtimeArtifactFor(runtime.GOOS, runtime.GOARCH)
}

func runtimeArtifactFor(goos, goarch string) (runtimeArtifact, error) {
	key := goos + "/" + goarch
	artifact, ok := runtimeArtifacts[key]
	if !ok {
		return runtimeArtifact{}, fmt.Errorf("Monty %s has no runtime for %s", RuntimeVersion, key)
	}
	return artifact, nil
}

// Install downloads, verifies, and caches the Monty worker for the current
// platform. It is safe to call repeatedly and concurrently. The returned path
// is absolute and can be passed to Options.BinaryPath.
func Install(ctx context.Context, options ...InstallOptions) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("install Monty runtime: %w", err)
	}
	if len(options) > 1 {
		return "", errors.New("Install accepts at most one InstallOptions value")
	}
	var option InstallOptions
	if len(options) == 1 {
		option = options[0]
	}
	artifact, err := currentRuntimeArtifact()
	if err != nil {
		return "", err
	}
	return installRuntime(ctx, option, artifact, http.DefaultClient)
}

func runtimeBinaryPath(cacheDir string, artifact runtimeArtifact) string {
	exe := "monty"
	if artifact.goos == "windows" {
		exe += ".exe"
	}
	return filepath.Join(
		cacheDir,
		"runtime",
		"v"+RuntimeVersion,
		artifact.goos+"-"+artifact.goarch,
		artifact.binarySHA256[:16],
		exe,
	)
}

func cachedRuntimeBinary(cacheDir string, artifact runtimeArtifact) (string, error) {
	root, err := resolveRuntimeCacheDir(cacheDir)
	if err != nil {
		return "", err
	}
	path := runtimeBinaryPath(root, artifact)
	if err := validateRuntimeBinary(path, artifact); err != nil {
		return "", err
	}
	return filepath.Abs(path)
}

func validateRuntimeBinary(path string, artifact runtimeArtifact) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("cached Monty runtime: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect cached Monty runtime: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("cached Monty runtime is not a regular file: %s", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("cached Monty runtime is not executable: %s", path)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("hash cached Monty runtime: %w", err)
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), artifact.binarySHA256) {
		return fmt.Errorf("cached Monty runtime failed SHA-256 verification: %s", path)
	}
	return nil
}

func installRuntime(ctx context.Context, option InstallOptions, artifact runtimeArtifact, client *http.Client) (string, error) {
	cacheDir, err := resolveRuntimeCacheDir(option.CacheDir)
	if err != nil {
		return "", err
	}
	target := runtimeBinaryPath(cacheDir, artifact)
	if !option.Force {
		if path, err := cachedRuntimeBinary(cacheDir, artifact); err == nil {
			return path, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", fmt.Errorf("create Monty runtime cache: %w", err)
	}
	release, err := acquireInstallLock(ctx, target+".lock")
	if err != nil {
		return "", err
	}
	defer release()
	if !option.Force {
		if path, err := cachedRuntimeBinary(cacheDir, artifact); err == nil {
			return path, nil
		}
	}
	if client == nil {
		client = http.DefaultClient
	}
	if err := downloadAndExtractRuntime(ctx, client, artifact, target); err != nil {
		return "", err
	}
	path, err := cachedRuntimeBinary(cacheDir, artifact)
	if err != nil {
		return "", fmt.Errorf("verify installed Monty runtime: %w", err)
	}
	return path, nil
}

func acquireInstallLock(ctx context.Context, path string) (func(), error) {
	for {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			_, _ = fmt.Fprintf(file, "pid=%d time=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))
			return func() {
				_ = file.Close()
				_ = os.Remove(path)
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("lock Monty runtime installation: %w", err)
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > installLockStaleAfter {
			if removeErr := os.Remove(path); removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
				continue
			}
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("wait for Monty runtime installation: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func downloadAndExtractRuntime(ctx context.Context, client *http.Client, artifact runtimeArtifact, target string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.url, nil)
	if err != nil {
		return fmt.Errorf("create Monty runtime request: %w", err)
	}
	request.Header.Set("User-Agent", "monty-go/"+Version)
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download Monty runtime: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		return fmt.Errorf("download Monty runtime: %s returned %s", artifact.url, response.Status)
	}

	archive, err := os.CreateTemp(filepath.Dir(target), ".monty-*.tgz")
	if err != nil {
		return fmt.Errorf("create Monty runtime download: %w", err)
	}
	archivePath := archive.Name()
	defer os.Remove(archivePath)
	archiveHash := sha256.New()
	limited := &io.LimitedReader{R: response.Body, N: maxRuntimeArchiveSize + 1}
	written, copyErr := io.Copy(io.MultiWriter(archive, archiveHash), limited)
	closeErr := archive.Close()
	if copyErr != nil {
		return fmt.Errorf("download Monty runtime: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("save Monty runtime archive: %w", closeErr)
	}
	if written > maxRuntimeArchiveSize {
		return fmt.Errorf("download Monty runtime: archive exceeds %d bytes", maxRuntimeArchiveSize)
	}
	if !strings.EqualFold(hex.EncodeToString(archiveHash.Sum(nil)), artifact.archiveSHA256) {
		return errors.New("download Monty runtime: archive failed SHA-256 verification")
	}
	if err := extractRuntimeArchive(archivePath, target, artifact); err != nil {
		return err
	}
	return nil
}

func extractRuntimeArchive(archivePath, target string, artifact runtimeArtifact) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open Monty runtime archive: %w", err)
	}
	defer archive.Close()
	return extractRuntimeArchiveReader(archive, target, artifact)
}

func extractRuntimeArchiveReader(archive io.Reader, target string, artifact runtimeArtifact) error {
	gzipReader, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("decompress Monty runtime archive: %w", err)
	}
	defer gzipReader.Close()
	unpacked := &io.LimitedReader{R: gzipReader, N: maxRuntimeUnpackedSize + 1}
	tarReader := tar.NewReader(unpacked)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("extract Monty runtime: archive does not contain %s", artifact.member)
		}
		if err != nil {
			return fmt.Errorf("read Monty runtime archive: %w", err)
		}
		if header.Name != artifact.member {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return fmt.Errorf("extract Monty runtime: %s is not a regular file", artifact.member)
		}
		if header.Size < 0 || header.Size > maxRuntimeBinarySize {
			return fmt.Errorf("extract Monty runtime: executable size %d is invalid", header.Size)
		}
		return writeRuntimeBinary(tarReader, header.Size, target, artifact)
	}
}

func writeRuntimeBinary(source io.Reader, expectedSize int64, target string, artifact runtimeArtifact) error {
	temporary, err := os.CreateTemp(filepath.Dir(target), ".monty-runtime-*")
	if err != nil {
		return fmt.Errorf("create Monty runtime executable: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	binaryHash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, binaryHash), io.LimitReader(source, maxRuntimeBinarySize+1))
	if syncErr := temporary.Sync(); copyErr == nil {
		copyErr = syncErr
	}
	if closeErr := temporary.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return fmt.Errorf("extract Monty runtime executable: %w", copyErr)
	}
	if written != expectedSize {
		return fmt.Errorf("extract Monty runtime: executable size is %d, expected %d", written, expectedSize)
	}
	if !strings.EqualFold(hex.EncodeToString(binaryHash.Sum(nil)), artifact.binarySHA256) {
		return errors.New("extract Monty runtime: executable failed SHA-256 verification")
	}
	if err := os.Chmod(temporaryPath, 0755); err != nil {
		return fmt.Errorf("make Monty runtime executable: %w", err)
	}
	// A forced install still preserves an already-valid executable. The newly
	// downloaded archive has been fully verified, so replacing identical bytes
	// would only create a Windows replacement race.
	if err := validateRuntimeBinary(target, artifact); err == nil {
		return nil
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("replace cached Monty runtime: %w", err)
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return fmt.Errorf("install Monty runtime: %w", err)
	}
	return nil
}
