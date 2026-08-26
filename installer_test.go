package monty

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func sha256String(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func makeRuntimeArchive(t *testing.T, member string, executable []byte, typeflag byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	if typeflag == 0 {
		typeflag = tar.TypeReg
	}
	header := &tar.Header{
		Name:     member,
		Mode:     0755,
		Size:     int64(len(executable)),
		Typeflag: typeflag,
	}
	if err := tarWriter.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if typeflag == tar.TypeReg || typeflag == tar.TypeRegA {
		if _, err := tarWriter.Write(executable); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func fixtureArtifact(serverURL string, archive, executable []byte) runtimeArtifact {
	member := "package/monty"
	if runtime.GOOS == "windows" {
		member += ".exe"
	}
	return runtimeArtifact{
		goos:          runtime.GOOS,
		goarch:        runtime.GOARCH,
		url:           serverURL,
		archiveSHA256: sha256String(archive),
		binarySHA256:  sha256String(executable),
		member:        member,
	}
}

func TestRuntimeArtifactMatrix(t *testing.T) {
	want := []string{
		"darwin/amd64",
		"darwin/arm64",
		"linux/amd64",
		"linux/arm64",
		"windows/amd64",
	}
	if len(runtimeArtifacts) != len(want) {
		t.Fatalf("got %d artifacts, want %d", len(runtimeArtifacts), len(want))
	}
	for _, key := range want {
		goos, goarch, found := strings.Cut(key, "/")
		if !found {
			t.Fatalf("invalid test platform %q", key)
		}
		artifact, err := runtimeArtifactFor(goos, goarch)
		if err != nil {
			t.Fatal(err)
		}
		if artifact.goos != goos || artifact.goarch != goarch {
			t.Fatalf("artifact for %s is %s/%s", key, artifact.goos, artifact.goarch)
		}
	}
}

func TestRuntimeArtifactMetadata(t *testing.T) {
	for key, artifact := range runtimeArtifacts {
		t.Run(key, func(t *testing.T) {
			if artifact.goos+"/"+artifact.goarch != key {
				t.Fatalf("artifact platform is %s/%s", artifact.goos, artifact.goarch)
			}
			if !strings.HasPrefix(artifact.url, "https://registry.npmjs.org/@pydantic/") || !strings.Contains(artifact.url, RuntimeVersion) {
				t.Fatalf("unexpected artifact URL %q", artifact.url)
			}
			for name, digest := range map[string]string{"archive": artifact.archiveSHA256, "binary": artifact.binarySHA256} {
				decoded, err := hex.DecodeString(digest)
				if err != nil || len(decoded) != sha256.Size {
					t.Fatalf("%s digest %q is not SHA-256", name, digest)
				}
			}
			if artifact.member != "package/monty" && artifact.member != "package/monty.exe" {
				t.Fatalf("unexpected archive member %q", artifact.member)
			}
		})
	}
	if _, err := runtimeArtifactFor("plan9", "amd64"); err == nil || !strings.Contains(err.Error(), "no runtime") {
		t.Fatalf("unsupported platform error = %v", err)
	}
}

func TestInstallRuntimeDownloadsCachesAndForces(t *testing.T) {
	executable := []byte("fixture Monty executable")
	member := "package/monty"
	if runtime.GOOS == "windows" {
		member += ".exe"
	}
	archive := makeRuntimeArchive(t, member, executable, tar.TypeReg)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writer.Header().Set("Content-Type", "application/gzip")
		_, _ = writer.Write(archive)
	}))
	defer server.Close()
	artifact := fixtureArtifact(server.URL, archive, executable)
	cacheDir := t.TempDir()

	path, err := installRuntime(context.Background(), InstallOptions{CacheDir: cacheDir}, artifact, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("installed path is not absolute: %s", path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, executable) {
		t.Fatalf("installed bytes = %q", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0111 == 0 {
			t.Fatalf("installed mode %v is not executable", info.Mode())
		}
	}

	second, err := installRuntime(context.Background(), InstallOptions{CacheDir: cacheDir}, artifact, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if second != path || requests.Load() != 1 {
		t.Fatalf("cached install path=%q requests=%d", second, requests.Load())
	}
	forced, err := installRuntime(context.Background(), InstallOptions{CacheDir: cacheDir, Force: true}, artifact, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if forced != path || requests.Load() != 2 {
		t.Fatalf("forced install path=%q requests=%d", forced, requests.Load())
	}
}

func TestInstallRuntimeRepairsCorruptCache(t *testing.T) {
	executable := []byte("known-good runtime")
	member := "package/monty"
	if runtime.GOOS == "windows" {
		member += ".exe"
	}
	archive := makeRuntimeArchive(t, member, executable, tar.TypeReg)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		_, _ = writer.Write(archive)
	}))
	defer server.Close()
	artifact := fixtureArtifact(server.URL, archive, executable)
	option := InstallOptions{CacheDir: t.TempDir()}
	path, err := installRuntime(context.Background(), option, artifact, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := installRuntime(context.Background(), option, artifact, server.Client()); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2", requests.Load())
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, executable) {
		t.Fatalf("repaired runtime = %q, %v", got, err)
	}
}

func TestInstallRuntimeConcurrentCallersDownloadOnce(t *testing.T) {
	executable := []byte("concurrent runtime")
	member := "package/monty"
	if runtime.GOOS == "windows" {
		member += ".exe"
	}
	archive := makeRuntimeArchive(t, member, executable, tar.TypeReg)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		time.Sleep(75 * time.Millisecond)
		_, _ = writer.Write(archive)
	}))
	defer server.Close()
	artifact := fixtureArtifact(server.URL, archive, executable)
	option := InstallOptions{CacheDir: t.TempDir()}
	const callers = 12
	paths := make([]string, callers)
	errorsFound := make([]error, callers)
	var wait sync.WaitGroup
	for index := range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			paths[index], errorsFound[index] = installRuntime(context.Background(), option, artifact, server.Client())
		}()
	}
	wait.Wait()
	for index, err := range errorsFound {
		if err != nil {
			t.Fatalf("caller %d: %v", index, err)
		}
		if paths[index] != paths[0] {
			t.Fatalf("caller %d path %q, want %q", index, paths[index], paths[0])
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

func TestInstallRuntimeRejectsInvalidDownloads(t *testing.T) {
	executable := []byte("runtime")
	member := "package/monty"
	if runtime.GOOS == "windows" {
		member += ".exe"
	}
	goodArchive := makeRuntimeArchive(t, member, executable, tar.TypeReg)
	cases := []struct {
		name      string
		status    int
		archive   []byte
		configure func(*runtimeArtifact)
		want      string
	}{
		{name: "HTTP status", status: http.StatusBadGateway, archive: []byte("upstream failed"), want: "502 Bad Gateway"},
		{name: "archive checksum", status: http.StatusOK, archive: goodArchive, configure: func(a *runtimeArtifact) { a.archiveSHA256 = strings.Repeat("0", 64) }, want: "archive failed SHA-256"},
		{name: "binary checksum", status: http.StatusOK, archive: goodArchive, configure: func(a *runtimeArtifact) { a.binarySHA256 = strings.Repeat("0", 64) }, want: "executable failed SHA-256"},
		{name: "missing executable", status: http.StatusOK, archive: makeRuntimeArchive(t, "package/not-monty", executable, tar.TypeReg), want: "does not contain"},
		{name: "non-regular executable", status: http.StatusOK, archive: makeRuntimeArchive(t, member, nil, tar.TypeSymlink), want: "not a regular file"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.WriteHeader(test.status)
				_, _ = writer.Write(test.archive)
			}))
			defer server.Close()
			artifact := fixtureArtifact(server.URL, test.archive, executable)
			if test.configure != nil {
				test.configure(&artifact)
			}
			_, err := installRuntime(context.Background(), InstallOptions{CacheDir: t.TempDir()}, artifact, server.Client())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestInstallRuntimeCancellationWhileLocked(t *testing.T) {
	executable := []byte("runtime")
	archive := makeRuntimeArchive(t, "package/monty", executable, tar.TypeReg)
	artifact := fixtureArtifact("http://unused.invalid", archive, executable)
	cacheDir := t.TempDir()
	target := runtimeBinaryPath(cacheDir, artifact)
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target+".lock", []byte("held"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, err := installRuntime(ctx, InstallOptions{CacheDir: cacheDir}, artifact, http.DefaultClient)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}

func TestInstallRuntimeCancellationDuringDownload(t *testing.T) {
	executable := []byte("runtime")
	archive := makeRuntimeArchive(t, "package/monty", executable, tar.TypeReg)
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
	}))
	defer server.Close()
	artifact := fixtureArtifact(server.URL, archive, executable)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, err := installRuntime(ctx, InstallOptions{CacheDir: t.TempDir()}, artifact, server.Client())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	select {
	case <-started:
	default:
		t.Fatal("download request never started")
	}
}

func TestAcquireInstallLockReclaimsStaleLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.lock")
	if err := os.WriteFile(path, []byte("abandoned"), 0600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-installLockStaleAfter - time.Minute)
	if err := os.Chtimes(path, stale, stale); err != nil {
		t.Fatal(err)
	}
	release, err := acquireInstallLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("replacement lock does not exist: %v", err)
	}
	release()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("released lock error = %v", err)
	}
}

func TestRuntimeCacheDirectoryPrecedence(t *testing.T) {
	environmentDir := filepath.Join(t.TempDir(), "environment")
	explicitDir := filepath.Join(t.TempDir(), "explicit")
	t.Setenv(CacheDirEnv, environmentDir)
	got, err := RuntimeCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs(environmentDir)
	if got != want {
		t.Fatalf("RuntimeCacheDir = %q, want %q", got, want)
	}
	got, err = resolveRuntimeCacheDir(explicitDir)
	if err != nil {
		t.Fatal(err)
	}
	want, _ = filepath.Abs(explicitDir)
	if got != want {
		t.Fatalf("explicit cache = %q, want %q", got, want)
	}
}

func TestFindBinaryUsesInstalledRuntimeCache(t *testing.T) {
	executable := []byte("resolver runtime")
	member := "package/monty"
	if runtime.GOOS == "windows" {
		member += ".exe"
	}
	archive := makeRuntimeArchive(t, member, executable, tar.TypeReg)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write(archive)
	}))
	defer server.Close()
	artifact := fixtureArtifact(server.URL, archive, executable)
	key := runtime.GOOS + "/" + runtime.GOARCH
	original, hadOriginal := runtimeArtifacts[key]
	runtimeArtifacts[key] = artifact
	defer func() {
		if hadOriginal {
			runtimeArtifacts[key] = original
		} else {
			delete(runtimeArtifacts, key)
		}
	}()

	cacheDir := t.TempDir()
	path, err := installRuntime(context.Background(), InstallOptions{CacheDir: cacheDir}, artifact, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONTY_BIN", "")
	t.Setenv("PATH", t.TempDir())
	found, err := findBinary("", cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if found != path {
		t.Fatalf("FindBinary = %q, want %q", found, path)
	}
}

func TestFindBinaryPrefersVerifiedRuntimeCacheOverPath(t *testing.T) {
	executable := []byte("verified cached runtime")
	member := "package/monty"
	if runtime.GOOS == "windows" {
		member += ".exe"
	}
	archive := makeRuntimeArchive(t, member, executable, tar.TypeReg)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write(archive)
	}))
	defer server.Close()
	artifact := fixtureArtifact(server.URL, archive, executable)
	key := runtime.GOOS + "/" + runtime.GOARCH
	original, hadOriginal := runtimeArtifacts[key]
	runtimeArtifacts[key] = artifact
	defer func() {
		if hadOriginal {
			runtimeArtifacts[key] = original
		} else {
			delete(runtimeArtifacts, key)
		}
	}()

	cacheDir := t.TempDir()
	cached, err := installRuntime(context.Background(), InstallOptions{CacheDir: cacheDir}, artifact, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	pathDir := t.TempDir()
	pathBinary := filepath.Join(pathDir, "monty")
	if runtime.GOOS == "windows" {
		pathBinary += ".exe"
	}
	if err = os.WriteFile(pathBinary, []byte("arbitrary PATH runtime"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONTY_BIN", "")
	t.Setenv("PATH", pathDir)

	found, err := findBinary("", cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if found != cached {
		t.Fatalf("FindBinary = %q, want verified cache %q", found, cached)
	}
}

func TestInstallPublicValidation(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Install(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Install error = %v", err)
	}
	if _, err := Install(context.Background(), InstallOptions{}, InstallOptions{}); err == nil || !strings.Contains(err.Error(), "at most one") {
		t.Fatalf("multiple options error = %v", err)
	}
}

func TestAutoInstallDoesNotOverrideExplicitBinaryPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-monty")
	_, err := New(context.Background(), Options{
		BinaryPath:  missing,
		AutoInstall: true,
		CacheDir:    t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "BinaryPath") {
		t.Fatalf("error = %v, want explicit BinaryPath failure", err)
	}
}

func TestExtractRuntimeArchiveRejectsMalformedGzip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-gzip")
	if err := os.WriteFile(path, []byte("not gzip"), 0600); err != nil {
		t.Fatal(err)
	}
	artifact := runtimeArtifact{member: "package/monty"}
	err := extractRuntimeArchive(path, filepath.Join(t.TempDir(), "monty"), artifact)
	if err == nil || !strings.Contains(err.Error(), "decompress") {
		t.Fatalf("error = %v", err)
	}
}

func TestInstallRuntimeSendsVersionedUserAgent(t *testing.T) {
	executable := []byte("runtime")
	member := "package/monty"
	if runtime.GOOS == "windows" {
		member += ".exe"
	}
	archive := makeRuntimeArchive(t, member, executable, tar.TypeReg)
	var userAgent string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		userAgent = request.UserAgent()
		_, _ = io.Copy(writer, bytes.NewReader(archive))
	}))
	defer server.Close()
	artifact := fixtureArtifact(server.URL, archive, executable)
	if _, err := installRuntime(context.Background(), InstallOptions{CacheDir: t.TempDir()}, artifact, server.Client()); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("monty-go/%s", Version)
	if userAgent != want {
		t.Fatalf("User-Agent = %q, want %q", userAgent, want)
	}
}
