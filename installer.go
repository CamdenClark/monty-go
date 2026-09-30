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
const RuntimeVersion = "1.0.0"

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
		url:           "https://registry.npmjs.org/@pydantic/monty-darwin-arm64/-/monty-darwin-arm64-1.0.0.tgz",
		archiveSHA256: "ffaa848257a454dd84d5a6267af6d3f9ad971c5c848246aa266111ea91ad5d5a",
		binarySHA256:  "96e063ddcb0efa6cab401503671b6209a33d9657e3cab626445de708c5b84cce",
		member:        "package/monty",
	},
	"darwin/amd64": {
		goos:          "darwin",
		goarch:        "amd64",
		url:           "https://registry.npmjs.org/@pydantic/monty-darwin-x64/-/monty-darwin-x64-1.0.0.tgz",
		archiveSHA256: "fa61ffebe87bad0e900e136ce744abefcba8d5a158b824c7f90bdadb68ede4b4",
		binarySHA256:  "a1996e5f24ca6cd1a1107843940cddaf7b296737345961108d02919201cca7e4",
		member:        "package/monty",
	},
	"linux/arm64": {
		goos:          "linux",
		goarch:        "arm64",
		url:           "https://registry.npmjs.org/@pydantic/monty-linux-arm64-gnu/-/monty-linux-arm64-gnu-1.0.0.tgz",
		archiveSHA256: "8f3a3ea853ff2ad5e2ee6bc3af262825eb305b535aa97252e685a4d52da59ec6",
		binarySHA256:  "e779f18e3e114233290ab0d4ad7a8c6b60b7dbbf48f8792fabdeaefca51bf4ca",
		member:        "package/monty",
	},
	"linux/amd64": {
		goos:          "linux",
		goarch:        "amd64",
		url:           "https://registry.npmjs.org/@pydantic/monty-linux-x64-gnu/-/monty-linux-x64-gnu-1.0.0.tgz",
		archiveSHA256: "251d951b42583c9c34235d8fee13b7220e6c5c869e0c648dcfd5c0fe2b6568cc",
		binarySHA256:  "b1eb33f353001f9d6c7dce85addb1ee32a221e0feda2f3e3f1125bedee16c966",
		member:        "package/monty",
	},
	"windows/amd64": {
		goos:          "windows",
		goarch:        "amd64",
		url:           "https://registry.npmjs.org/@pydantic/monty-win32-x64-msvc/-/monty-win32-x64-msvc-1.0.0.tgz",
		archiveSHA256: "ca1c25bdffacc6c83a828cfe9eeaf43e08b8c627d50b9ea3059ec0cff34581f4",
		binarySHA256:  "932aad2c0b416b4799107a6ae825553da6cf38bba3f2bb1e32c84a7055118553",
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
