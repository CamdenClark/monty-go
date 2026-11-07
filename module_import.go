package monty

import (
	"context"
	"fmt"
	"path"
	"strings"
)

// importFile reads only virtual paths explicitly served by the feed's OS handler.
// Packages precede plain modules; missing or inaccessible candidates are skipped.
func importFile(ctx context.Context, handler OSHandler, name string, searchPath List) (Value, error) {
	if handler == nil {
		return NotHandled, nil
	}
	for _, part := range strings.Split(name, ".") {
		if !pythonIdentifier(part) {
			return NotHandled, nil
		}
	}
	leaf := name[strings.LastIndexByte(name, '.')+1:]
	searched := 0
	for _, entry := range searchPath {
		dir, ok := entry.(string)
		if !ok || !path.IsAbs(dir) || strings.ContainsRune(dir, '\x00') {
			continue
		}
		if searched == 32 {
			break
		}
		searched++
		for _, suffix := range []string{leaf + "/__init__.py", leaf + ".py"} {
			candidate := path.Join(dir, suffix)
			value, err := handler(ctx, "Path.read_text", []Value{candidate}, Kwargs{})
			if err != nil {
				switch raisedFromError(err).Type {
				case "FileNotFoundError", "NotADirectoryError", "IsADirectoryError", "PermissionError":
					continue
				default:
					return nil, err
				}
			}
			if _, ok := value.(notHandled); ok {
				continue
			}
			source, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("invalid return type: Path.read_text for an import must return a str")
			}
			return ModuleFile{Path: candidate, Source: source}, nil
		}
	}
	return NotHandled, nil
}
