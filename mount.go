package monty

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

// MountMode controls whether sandbox writes are rejected, persisted, or held
// in a per-feed in-memory overlay.
type MountMode string

const (
	MountOverlay   MountMode = "overlay"
	MountReadOnly  MountMode = "read-only"
	MountReadWrite MountMode = "read-write"
)
const DefaultMountMemoryLimit uint64 = 100 * 1024 * 1024

// MountOptions configures a host directory mounted at a virtual POSIX path.
type MountOptions struct {
	HostPath, VirtualPath string
	Mode                  MountMode
	WriteBytesLimit       *uint64
	MemoryUsageLimit      uint64
}

// MountDir safely anchors operations to an opened host directory. Close it
// when it is no longer used, especially on Windows.
type MountDir struct {
	HostPath, VirtualPath string
	Mode                  MountMode
	WriteBytesLimit       *uint64
	MemoryUsageLimit      uint64
	root                  *os.Root
}

func NewMountDir(options MountOptions) (*MountDir, error) {
	if options.Mode == "" {
		options.Mode = MountOverlay
	}
	if options.Mode != MountOverlay && options.Mode != MountReadOnly && options.Mode != MountReadWrite {
		return nil, fmt.Errorf("invalid mount mode %q", options.Mode)
	}
	virtual := path.Clean(options.VirtualPath)
	if !strings.HasPrefix(virtual, "/") || virtual == "/" {
		return nil, fmt.Errorf("VirtualPath must be an absolute non-root POSIX path")
	}
	if options.MemoryUsageLimit == 0 {
		options.MemoryUsageLimit = DefaultMountMemoryLimit
	}
	root, err := os.OpenRoot(options.HostPath)
	if err != nil {
		return nil, fmt.Errorf("open mount host directory: %w", err)
	}
	return &MountDir{options.HostPath, virtual, options.Mode, options.WriteBytesLimit, options.MemoryUsageLimit, root}, nil
}
func (m *MountDir) Close() error {
	if m == nil || m.root == nil {
		return nil
	}
	err := m.root.Close()
	m.root = nil
	return err
}
func (m *MountDir) String() string {
	return fmt.Sprintf("MountDir(host_path=%q, virtual_path=%q, mode=%q)", m.HostPath, m.VirtualPath, m.Mode)
}

type overlayEntry struct {
	data         []byte
	deleted, dir bool
}
type mountState struct {
	mount   *MountDir
	overlay map[string]overlayEntry
	used    uint64
	written uint64
}

func newRunState(session *Session, options FeedOptions) *runState {
	r := &runState{session: session, options: options, futures: make(map[uint32]<-chan futureOutcome)}
	mounts := options.Mounts
	if options.Mount != nil {
		mounts = append([]*MountDir{options.Mount}, mounts...)
	}
	for _, m := range mounts {
		if m != nil {
			r.mounts = append(r.mounts, &mountState{mount: m, overlay: make(map[string]overlayEntry)})
		}
	}
	return r
}

func (m *mountState) relative(value Value) (string, bool) {
	p, ok := value.(string)
	if !ok {
		if x, yes := value.(Path); yes {
			p = string(x)
			ok = true
		}
	}
	if !ok {
		return "", false
	}
	clean := path.Clean(p)
	if clean == m.mount.VirtualPath {
		return ".", true
	}
	prefix := m.mount.VirtualPath + "/"
	if !strings.HasPrefix(clean, prefix) {
		return "", false
	}
	rel := strings.TrimPrefix(clean, prefix)
	if rel == "" || rel == "." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}
func (m *mountState) virtual(rel string) Path {
	if rel == "." {
		return Path(m.mount.VirtualPath)
	}
	return Path(path.Join(m.mount.VirtualPath, rel))
}
func (m *mountState) write(rel string, data []byte, appendData bool) (int, error) {
	if m.mount.Mode == MountReadOnly {
		return 0, &HostError{Type: "PermissionError", Message: "mount is read-only"}
	}
	incoming := uint64(len(data))
	if m.mount.WriteBytesLimit != nil && m.written+incoming > *m.mount.WriteBytesLimit {
		return 0, &HostError{Type: "MemoryError", Message: "mount write limit exceeded"}
	}
	if m.mount.Mode == MountOverlay {
		old := m.overlay[rel]
		if appendData {
			base, err := m.read(rel)
			if err == nil {
				data = append(base, data...)
			} else {
				var host *HostError
				if !errors.As(err, &host) || host.Type != "FileNotFoundError" {
					return 0, err
				}
			}
		}
		newUsed := m.used - min(m.used, uint64(len(old.data))) + uint64(len(data))
		if newUsed > m.mount.MemoryUsageLimit {
			return 0, &HostError{Type: "MemoryError", Message: "mount memory limit exceeded"}
		}
		m.used = newUsed
		m.written += incoming
		m.overlay[rel] = overlayEntry{data: append([]byte(nil), data...)}
		return int(incoming), nil
	}
	if appendData {
		f, err := m.mount.root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o666)
		if err != nil {
			return 0, m.hostError(err)
		}
		defer f.Close()
		n, err := f.Write(data)
		if err != nil {
			return 0, m.hostError(err)
		}
		m.written += incoming
		return n, nil
	}
	if err := m.mount.root.WriteFile(rel, data, 0o666); err != nil {
		return 0, m.hostError(err)
	}
	m.written += incoming
	return len(data), nil
}
func (m *mountState) read(rel string) ([]byte, error) {
	if x, ok := m.overlay[rel]; ok {
		if x.deleted {
			return nil, &HostError{Type: "FileNotFoundError", Message: m.mount.VirtualPath + "/" + rel}
		}
		if x.dir {
			return nil, &HostError{Type: "IsADirectoryError", Message: m.mount.VirtualPath + "/" + rel}
		}
		return append([]byte(nil), x.data...), nil
	}
	b, err := m.mount.root.ReadFile(rel)
	if err != nil {
		return nil, m.hostError(err)
	}
	return b, nil
}
func (m *mountState) hostError(err error) error {
	typ := "OSError"
	switch {
	case os.IsNotExist(err):
		typ = "FileNotFoundError"
	case os.IsPermission(err):
		typ = "PermissionError"
	}
	return &HostError{Type: typ, Message: err.Error()}
}
func (m *mountState) stat(rel string) (fs.FileInfo, error) {
	if x, ok := m.overlay[rel]; ok {
		if x.deleted {
			return nil, &HostError{Type: "FileNotFoundError", Message: rel}
		}
		return overlayInfo{path.Base(rel), int64(len(x.data)), x.dir}, nil
	}
	info, err := m.mount.root.Lstat(rel)
	if err != nil {
		return nil, m.hostError(err)
	}
	return info, nil
}

func statValue(info fs.FileInfo) NamedTuple {
	t := float64(info.ModTime().UnixNano()) / 1e9
	return NamedTuple{
		TypeName:   "os.stat_result",
		FieldNames: []string{"st_mode", "st_ino", "st_dev", "st_nlink", "st_uid", "st_gid", "st_size", "st_atime", "st_mtime", "st_ctime"},
		Values:     []Value{int64(info.Mode()), int64(0), int64(0), int64(1), int64(0), int64(0), info.Size(), t, t, t},
	}
}

func (m *mountState) readDir(rel string) (List, error) {
	f, err := m.mount.root.Open(rel)
	var entries []os.DirEntry
	if err == nil {
		entries, err = f.ReadDir(-1)
		_ = f.Close()
		if err != nil {
			return nil, m.hostError(err)
		}
	} else {
		overlay, ok := m.overlay[rel]
		if !ok || !overlay.dir || overlay.deleted {
			return nil, m.hostError(err)
		}
	}
	names := make(map[string]bool, len(entries))
	for _, entry := range entries {
		names[entry.Name()] = true
	}
	if m.mount.Mode == MountOverlay {
		prefix := ""
		if rel != "." {
			prefix = rel + "/"
		}
		for name, entry := range m.overlay {
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			rest := strings.TrimPrefix(name, prefix)
			if strings.Contains(rest, "/") {
				rest = strings.SplitN(rest, "/", 2)[0]
			}
			if rest == "" {
				continue
			}
			if entry.deleted && name == prefix+rest {
				delete(names, rest)
			} else {
				names[rest] = true
			}
		}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	result := make(List, 0, len(ordered))
	for _, name := range ordered {
		child := name
		if rel != "." {
			child = path.Join(rel, name)
		}
		result = append(result, m.virtual(child))
	}
	return result, nil
}

type overlayInfo struct {
	name string
	size int64
	dir  bool
}

func (x overlayInfo) Name() string { return x.name }
func (x overlayInfo) Size() int64  { return x.size }
func (x overlayInfo) Mode() fs.FileMode {
	if x.dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (x overlayInfo) ModTime() time.Time { return time.Time{} }
func (x overlayInfo) IsDir() bool        { return x.dir }
func (x overlayInfo) Sys() any           { return nil }

func (m *mountState) handle(name string, args []Value) (Value, bool, error) {
	if len(args) == 0 {
		return nil, false, nil
	}
	rel, ok := m.relative(args[0])
	if !ok {
		return nil, false, nil
	}
	switch name {
	case "Path.exists":
		_, err := m.stat(rel)
		if err != nil {
			var h *HostError
			if errors.As(err, &h) && h.Type == "FileNotFoundError" {
				return false, true, nil
			}
			return nil, true, err
		}
		return true, true, nil
	case "Path.is_file", "Path.is_dir", "Path.is_symlink":
		info, err := m.stat(rel)
		if err != nil {
			var h *HostError
			if errors.As(err, &h) && h.Type == "FileNotFoundError" {
				return false, true, nil
			}
			return nil, true, err
		}
		if name == "Path.is_file" {
			return !info.IsDir(), true, nil
		}
		if name == "Path.is_dir" {
			return info.IsDir(), true, nil
		}
		return info.Mode()&fs.ModeSymlink != 0, true, nil
	case "Path.read_text":
		b, err := m.read(rel)
		return string(b), true, err
	case "Path.read_bytes":
		b, err := m.read(rel)
		return b, true, err
	case "Path.stat":
		info, err := m.stat(rel)
		if err != nil {
			return nil, true, err
		}
		return statValue(info), true, nil
	case "Path.iterdir":
		items, err := m.readDir(rel)
		return items, true, err
	case "Path.write_text", "Path.append_text":
		if len(args) < 2 {
			return nil, true, &HostError{Type: "TypeError", Message: "missing write data"}
		}
		text, ok := args[1].(string)
		if !ok {
			return nil, true, &HostError{Type: "TypeError", Message: "text write requires str"}
		}
		n, err := m.write(rel, []byte(text), name == "Path.append_text")
		return int64(n), true, err
	case "Path.write_bytes", "Path.append_bytes":
		if len(args) < 2 {
			return nil, true, &HostError{Type: "TypeError", Message: "missing write data"}
		}
		data, ok := args[1].([]byte)
		if !ok {
			return nil, true, &HostError{Type: "TypeError", Message: "binary write requires bytes"}
		}
		n, err := m.write(rel, data, name == "Path.append_bytes")
		return int64(n), true, err
	case "open":
		if len(args) < 2 {
			return nil, true, &HostError{Type: "TypeError", Message: "missing file mode"}
		}
		mode, _ := args[1].(string)
		h, err := NewFileHandle(string(m.virtual(rel)), mode, 0)
		if err != nil {
			return nil, true, &HostError{Type: "ValueError", Message: err.Error()}
		}
		if strings.HasPrefix(h.Mode, "r") {
			if _, err = m.stat(rel); err != nil {
				return nil, true, err
			}
		}
		return h, true, nil
	case "Path.unlink", "Path.rmdir":
		if m.mount.Mode == MountReadOnly {
			return nil, true, &HostError{Type: "PermissionError", Message: "mount is read-only"}
		}
		if m.mount.Mode == MountOverlay {
			info, err := m.stat(rel)
			if err != nil {
				return nil, true, err
			}
			if name == "Path.rmdir" && !info.IsDir() {
				return nil, true, &HostError{Type: "NotADirectoryError", Message: string(m.virtual(rel))}
			}
			if name == "Path.unlink" && info.IsDir() {
				return nil, true, &HostError{Type: "IsADirectoryError", Message: string(m.virtual(rel))}
			}
			old := m.overlay[rel]
			m.used -= min(m.used, uint64(len(old.data)))
			m.overlay[rel] = overlayEntry{deleted: true}
			return nil, true, nil
		}
		if err := m.mount.root.Remove(rel); err != nil {
			return nil, true, m.hostError(err)
		}
		return nil, true, nil
	case "Path.mkdir":
		if m.mount.Mode == MountReadOnly {
			return nil, true, &HostError{Type: "PermissionError", Message: "mount is read-only"}
		}
		parents, exist := false, false
		if len(args) > 1 {
			parents, _ = args[1].(bool)
		}
		if len(args) > 2 {
			exist, _ = args[2].(bool)
		}
		if m.mount.Mode == MountOverlay {
			if _, err := m.stat(rel); err == nil {
				if exist {
					return nil, true, nil
				}
				return nil, true, &HostError{Type: "FileExistsError", Message: string(m.virtual(rel))}
			} else {
				var host *HostError
				if !errors.As(err, &host) || host.Type != "FileNotFoundError" {
					return nil, true, err
				}
			}
			m.overlay[rel] = overlayEntry{dir: true}
			return nil, true, nil
		}
		var err error
		if parents {
			err = m.mount.root.MkdirAll(rel, 0o777)
		} else {
			err = m.mount.root.Mkdir(rel, 0o777)
		}
		if err != nil {
			if exist && errors.Is(err, fs.ErrExist) {
				return nil, true, nil
			}
			return nil, true, m.hostError(err)
		}
		return nil, true, nil
	case "Path.rename":
		if len(args) < 2 {
			return nil, true, &HostError{Type: "TypeError", Message: "missing destination"}
		}
		dst, inside := m.relative(args[1])
		if !inside {
			return nil, false, nil
		}
		if m.mount.Mode == MountReadOnly {
			return nil, true, &HostError{Type: "PermissionError", Message: "mount is read-only"}
		}
		if m.mount.Mode == MountOverlay {
			b, err := m.read(rel)
			if err != nil {
				return nil, true, err
			}
			m.overlay[dst] = overlayEntry{data: b}
			m.overlay[rel] = overlayEntry{deleted: true}
			return m.virtual(dst), true, nil
		}
		if err := m.mount.root.Rename(rel, dst); err != nil {
			return nil, true, m.hostError(err)
		}
		return m.virtual(dst), true, nil
	case "Path.absolute", "Path.resolve":
		return m.virtual(rel), true, nil
	}
	return nil, false, nil
}
