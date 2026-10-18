package monty

import (
	"crypto/rand"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"google.golang.org/protobuf/encoding/protowire"
)

// Module is a host module crossing the sandbox boundary. It can be returned
// from an OS handler's "import" call; Function attributes resolve through
// ExternalLookup. Automatic imports use CheckoutOptions.ExternalModules.
type Module struct {
	Name  string
	ID    [16]byte
	Attrs Dict
}

func decodeModule(data []byte, decodeAttrs func([]byte) (Dict, error)) (Module, error) {
	var module Module
	var seen [4]bool
	err := parseFields(data, func(f wireField) error {
		if f.tag < 1 || f.tag > 3 {
			return nil
		}
		if seen[f.tag] || f.type_ != protowire.BytesType {
			return fmt.Errorf("invalid module field %d", f.tag)
		}
		seen[f.tag] = true
		var err error
		switch f.tag {
		case 1:
			module.Name = string(f.bytes)
		case 2:
			module.ID, err = decodeUUID(f.bytes)
		case 3:
			module.Attrs, err = decodeAttrs(f.bytes)
		}
		return err
	})
	if err == nil && (!seen[2] || !seen[3]) {
		err = fmt.Errorf("module requires identity and attributes")
	}
	return module, err
}

// ExternalModule exposes Go values, functions and nested modules to Python.
// Construct one with NewExternalModule. Attributes are copied at construction.
type ExternalModule struct {
	id    [16]byte
	attrs map[string]Value
	stubs string
}

// ExternalModuleOptions supplies optional .pyi source and a stable identity.
// A zero ID generates a fresh UUID. Retain the ID when restoring module state.
type ExternalModuleOptions struct {
	ID    [16]byte
	Stubs string
}

// ExternalModules maps top-level import names to host modules. Nested modules
// are attributes of their parent. Do not mutate this map during a feed.
type ExternalModules map[string]*ExternalModule

// NewExternalModule copies public attributes, excluding names beginning with
// an underscore. Functions may use the same signatures as ExternalLookup.
func NewExternalModule(attrs map[string]Value, options ...ExternalModuleOptions) (*ExternalModule, error) {
	if len(options) > 1 {
		return nil, fmt.Errorf("NewExternalModule accepts at most one ExternalModuleOptions value")
	}
	var o ExternalModuleOptions
	if len(options) == 1 {
		o = options[0]
	}
	m := &ExternalModule{id: o.ID, stubs: o.Stubs, attrs: make(map[string]Value, len(attrs))}
	if m.id == ([16]byte{}) {
		rand.Read(m.id[:])
		m.id[6] = m.id[6]&0x0f | 0x40
		m.id[8] = m.id[8]&0x3f | 0x80
	}
	for name, value := range attrs {
		if strings.HasPrefix(name, "_") {
			continue
		}
		if !pythonIdentifier(name) {
			return nil, fmt.Errorf("invalid module attribute %q", name)
		}
		if nested, ok := value.(*ExternalModule); ok && nested == nil {
			return nil, fmt.Errorf("nil submodule %q", name)
		}
		m.attrs[name] = value
	}
	return m, nil
}

// ID returns the module's UUID identity.
func (m *ExternalModule) ID() [16]byte { return m.id }

func pythonIdentifier(name string) bool {
	// Python's hard keywords; match/case/type are soft keywords and valid names.
	if strings.Contains(" False None True and as assert async await break class continue def del elif else except finally for from global if import in is lambda nonlocal not or pass raise return try while with yield ", " "+name+" ") {
		return false
	}
	for i, r := range name {
		if r != '_' && !unicode.IsLetter(r) && !(i > 0 && (unicode.IsDigit(r) || unicode.IsMark(r))) {
			return false
		}
	}
	return name != ""
}

func (m *ExternalModule) value(name string, depth int) (Module, error) {
	if m == nil || depth > 64 {
		return Module{}, fmt.Errorf("nil or excessively nested external module %q", name)
	}
	attrs := Dict{}
	keys := make([]string, 0, len(m.attrs))
	for key := range m.attrs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := m.attrs[key]
		if child, ok := value.(*ExternalModule); ok {
			var err error
			value, err = child.value(name+"."+key, depth+1)
			if err != nil {
				return Module{}, err
			}
		} else if isHostFunction(value) {
			value = Function{Name: name + "." + key, Docstring: functionDoc(value)}
		}
		attrs = append(attrs, Pair{key, value})
	}
	return Module{Name: name, ID: m.id, Attrs: attrs}, nil
}

func (m *ExternalModule) collectStubs(name string, stubs map[string]Value) {
	var source Value
	if m.stubs != "" {
		source = m.stubs
	}
	stubs[name] = source
	for key, value := range m.attrs {
		if child, ok := value.(*ExternalModule); ok {
			child.collectStubs(name+"."+key, stubs)
		}
	}
}

func (modules ExternalModules) function(name string) (Value, bool) {
	parts := strings.Split(name, ".")
	if len(parts) < 2 {
		return nil, false
	}
	m := modules[parts[0]]
	for _, part := range parts[1 : len(parts)-1] {
		if m == nil || strings.HasPrefix(part, "_") {
			return nil, false
		}
		m, _ = m.attrs[part].(*ExternalModule)
	}
	if m == nil || strings.HasPrefix(parts[len(parts)-1], "_") {
		return nil, false
	}
	fn, ok := m.attrs[parts[len(parts)-1]]
	return fn, ok && isHostFunction(fn)
}
