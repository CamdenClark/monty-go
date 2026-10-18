package monty_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	monty "github.com/camdenclark/monty-go"
)

func TestMonty12LanguageAndStandardLibrary(t *testing.T) {
	pool := integrationPool(t)
	for _, tc := range []struct{ name, code string }{
		{"patterns", `from dataclasses import dataclass
from collections import namedtuple
@dataclass
class Point:
    x: int
    y: int
Pair = namedtuple('Pair', ['x', 'y'])
def classify(value):
    match value:
        case None: return 'none'
        case {'kind': 'sum', 'values': [first, *rest], **extra} if first > 0:
            return first + sum(rest) + extra['bonus']
        case Point(0, y) | Pair(0, y): return y
        case [1, 2] as pair: return sum(pair)
        case int(n) if n > 0: return n
        case 'a' | 'b': return 'letter'
        case _: return 'other'
assert classify(None) == 'none'
assert classify({'kind': 'sum', 'values': [10, 20], 'bonus': 12}) == 42
assert classify(Point(0, 42)) == 42
assert classify(Pair(0, 42)) == 42
assert classify([1, 2]) == 3
assert classify(42) == 42
assert classify('a') == 'letter'
assert classify(-1) == 'other'
True`},
		{"pattern binding rollback", `x = 'original'
match [1, 3]:
    case [x, 2]: pass
assert x == 'original'
match [1, 2]:
    case [x, y] if False: pass
assert (x, y) == (1, 2)
True`},
		{"slice assignment", `items = [0, 1, 2, 3, 4]
items[1:3] = [8, 9, 10]
assert items == [0, 8, 9, 10, 3, 4]
items[::2] = [20, 21, 22]
assert items == [20, 8, 21, 10, 22, 4]
items[::-2] = [30, 31, 32]
assert items == [20, 32, 21, 31, 22, 30]
items[:] = items
assert len(items) == 6
try:
    items[::2] = [1]
except ValueError:
    pass
else:
    raise AssertionError('extended slice accepted wrong length')
True`},
		{"dataclass fields", `from dataclasses import dataclass, field
@dataclass
class Basket:
    items: list = field(default_factory=list)
    total: int = field(default=0)
    label: str = field(default='basket')
    def __post_init__(self):
        self.total = len(self.items)
a = Basket([1, 2])
b = Basket()
c = Basket()
b.items.append(3)
assert a.total == 2 and c.items == []
assert a.label == 'basket'
True`},
		{"hashlib", `import hashlib
h = hashlib.sha256(b'abc')
assert h.hexdigest() == 'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad'
c = h.copy()
c.update(b'd')
assert c.digest() == hashlib.sha256(b'abcd').digest()
assert len(hashlib.blake2b(b'abc').digest()) == 64
True`},
		{"os.path", `import os.path
assert os.path.join('/a', 'b', '..', 'c') == '/a/b/../c'
assert os.path.normpath('/a/b/../c') == '/a/c'
assert os.path.splitext('/a/file.csv') == ('/a/file', '.csv')
assert os.path.basename('/a/file.csv') == 'file.csv'
True`},
		{"bisect", `import bisect
values = [1, 3, 3, 7]
assert bisect.bisect_left(values, 3) == 1
assert bisect.bisect_right(values, 3) == 3
bisect.insort(values, 4)
assert values == [1, 3, 3, 4, 7]
True`},
		{"csv", `import csv
assert list(csv.reader(['name,value', '"a,b",42'])) == [['name', 'value'], ['a,b', '42']]
assert list(csv.DictReader(['name,value', 'Ada,42'])) == [{'name': 'Ada', 'value': '42'}]
class Sink:
    def __init__(self): self.text = ''
    def write(self, text):
        self.text += text
        return len(text)
sink = Sink()
csv.writer(sink).writerow(['a,b', 42])
assert sink.text == '"a,b",42\r\n'
True`},
		{"tomllib", `import tomllib
from datetime import date
data = tomllib.loads('name = "Ada"\n[project]\nvalues = [1, 2]\ndate = 2026-10-18\n')
assert data == {'name': 'Ada', 'project': {'values': [1, 2], 'date': date(2026, 10, 18)}}
True`},
		{"set ordering", `assert {1} < {1, 2}
assert frozenset([1, 2]) >= {2}
assert not ({1, 3} <= {1, 2})
True`},
		{"user dunders", `class Number:
    def __init__(self, n): self.n = n
    def __add__(self, other): return self.n + other
    def __radd__(self, other): return other + self.n
    def __neg__(self): return -self.n
    def __getitem__(self, index): return self.n + index
n = Number(40)
assert n + 2 == 42 and 2 + n == 42
assert -n == -40 and n[2] == 42
True`},
		{"regex split groups", `import re
assert re.split(r'(\d+)', 'a12b34') == ['a', '12', 'b', '34', '']
True`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := integrationSession(t, pool)
			if got := run(t, session, tc.code); got != true {
				t.Fatalf("got %#v", got)
			}
		})
	}
}

func TestMonty12MatchSuspensionAndRestore(t *testing.T) {
	pool := integrationPool(t)
	session := integrationSession(t, pool)
	ctx := context.Background()
	p, err := session.FeedStart(ctx, `match payload:
    case {'value': n} if accept(n):
        result = n + 2
    case _:
        result = 0
result`, monty.FeedOptions{Inputs: map[string]any{"payload": map[string]any{"value": 40}}, ExternalLookup: monty.ExternalLookup{"accept": func(int) bool { return true }}})
	if err != nil {
		t.Fatal(err)
	}
	// Persist the external call in the guard before supplying its result.
	call, ok := p.(*monty.FunctionSnapshot)
	if !ok || call.FunctionName != "accept" || call.Position.End <= call.Position.Start {
		t.Fatalf("guard suspension: %#v", p)
	}
	dump, err := call.Dump(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restored := integrationSession(t, pool)
	snap, err := restored.LoadSnapshot(ctx, dump)
	if err != nil {
		t.Fatal(err)
	}
	p, err = snap.(*monty.FunctionSnapshot).Resume(ctx, true)
	if err != nil || p.(*monty.Complete).Output != int64(42) {
		t.Fatalf("restored match: %#v, %v", p, err)
	}
	if got := run(t, restored, "result"); got != int64(42) {
		t.Fatal(got)
	}
}

func TestMonty12PatternErrorsAndTOMLData(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	for _, code := range []string{
		"match [1, 2]:\n    case [x, x]: pass",
		"match 1:\n    case x: pass\n    case 1: pass",
		"match [1]:\n    case [x] | [y]: pass",
	} {
		_, err := session.FeedRun(context.Background(), code)
		var syntax *monty.SyntaxError
		if !errors.As(err, &syntax) {
			t.Fatalf("pattern error: %T %v", err, err)
		}
		if got := run(t, session, "42"); got != int64(42) {
			t.Fatal(got)
		}
	}
	_, err := session.FeedRun(context.Background(), "import tomllib\ntomllib.loads('x = @')")
	var runtimeErr *monty.RuntimeError
	if !errors.As(err, &runtimeErr) || runtimeErr.Exception.Type != "tomllib.TOMLDecodeError" {
		t.Fatal(err)
	}
	data, ok := runtimeErr.Exception.Data.(*monty.JSONErrorData)
	if !ok || data.Document == nil || *data.Document != "x = @" || data.Line != 1 || data.Column == 0 {
		t.Fatalf("TOML data: %#v", runtimeErr.Exception.Data)
	}
	_, err = session.FeedRun(context.Background(), "key = 'x' * 70000\ntomllib.loads('[' + key + ']\\n[' + key + ']\\n')")
	if !errors.As(err, &runtimeErr) || runtimeErr.Exception.Type != "tomllib.TOMLDecodeError" {
		t.Fatal(err)
	}
	data, ok = runtimeErr.Exception.Data.(*monty.JSONErrorData)
	if !ok || len(data.Message) <= 64<<10 || data.Document != nil {
		t.Fatal("long TOML error lost its structured data")
	}
}

func TestMonty12FilesAndScanning(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	files := map[string]string{"/data/lines": "first\r\nsecond\rthird\n", "/data/input.json": `{"value":42}`, "/data/input.toml": "value = 42"}
	scans := 0
	os := func(ctx context.Context, name string, args []monty.Value, kw monty.Kwargs) (monty.Value, error) {
		switch name {
		case "open":
			return monty.NewFileHandle(args[0].(string), args[1].(string), 0)
		case "Path.read_text":
			return files[args[0].(string)], nil
		case "Path.read_bytes":
			return []byte(files[args[0].(string)]), nil
		case "Path.write_text", "Path.append_text":
			path, text := args[0].(string), args[1].(string)
			if name == "Path.append_text" {
				files[path] += text
			} else {
				files[path] = text
			}
			return len(text), nil
		case "Path.rename":
			src, dst := args[0].(string), args[1].(string)
			files[dst] = files[src]
			delete(files, src)
			return nil, nil
		case "Path.unlink":
			return nil, &monty.HostError{Type: "FileNotFoundError", Message: "missing file"}
		case "Path.scan":
			scans++
			if args[0] != "/data" || len(args) != 6 {
				t.Fatalf("scan arguments: %#v", args)
			}
			return monty.List{monty.Tuple{"", true, false, false}, monty.Tuple{"sub", true, false, false}, monty.Tuple{"sub/a.py", false, true, false}, monty.Tuple{"b.txt", false, true, false}}, nil
		}
		return monty.NotHandled, nil
	}
	got := run(t, session, `import json, tomllib, os
from pathlib import Path
with open('/data/lines') as f:
    lines = [line for line in f]
assert lines == ['first\n', 'second\n', 'third\n']
with open('/data/lines', newline='') as f:
    assert f.read() == 'first\r\nsecond\rthird\n'
with open('/data/input.json') as f:
    assert json.load(f) == {'value': 42}
with open('/data/input.toml', 'rb') as f:
    assert tomllib.load(f) == {'value': 42}
with open('/data/output.json', 'w', newline='\r\n') as f:
    json.dump({'value': 42}, f)
with open('/data/output.txt', 'w', newline='\r\n') as f:
    f.write('first\nsecond\n')
assert Path('/data/output.txt').rename('renamed.txt') == Path('renamed.txt')
assert Path('/data/missing').unlink(missing_ok=True) is None
assert sorted(str(p) for p in Path('/data').rglob('*.py')) == ['/data/sub/a.py']
assert sorted(entry.name for entry in os.scandir('/data')) == ['b.txt', 'sub']
assert list(os.walk('/data')) == [('/data', ['sub'], ['b.txt']), ('/data/sub', [], ['a.py'])]
assert list(Path('/data').walk()) == [(Path('/data'), ['sub'], ['b.txt']), (Path('/data/sub'), [], ['a.py'])]
True`, monty.FeedOptions{OS: os, Cwd: "/data"})
	if got != true || files["/data/output.json"] != `{"value": 42}` || files["/data/renamed.txt"] != "first\r\nsecond\r\n" || scans != 4 {
		t.Fatalf("file behavior: %#v, %#v, scans=%d", got, files, scans)
	}
	_, err := session.FeedRun(context.Background(), "list(os.scandir('/denied'))")
	var runtimeErr *monty.RuntimeError
	if !errors.As(err, &runtimeErr) || runtimeErr.Exception.Type != "PermissionError" || !strings.Contains(err.Error(), "/denied") {
		t.Fatalf("scan default denial: %v", err)
	}
}

func TestMonty12RestoreCapsDestinationLimits(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	source := integrationSession(t, pool, monty.CheckoutOptions{Limits: monty.ResourceLimits{MaxSuspensions: 20, MaxFeedDuration: time.Second}})
	p, err := source.FeedStart(ctx, "a + b + c")
	if err != nil {
		t.Fatal(err)
	}
	dump, err := p.(monty.Snapshot).Dump(ctx)
	if err != nil {
		t.Fatal(err)
	}
	destination := integrationSession(t, pool, monty.CheckoutOptions{Limits: monty.ResourceLimits{MaxSuspensions: 2}})
	snap, err := destination.LoadSnapshot(ctx, dump)
	if err != nil {
		t.Fatal(err)
	}
	p, err = snap.(*monty.NameLookupSnapshot).Resume(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.(*monty.NameLookupSnapshot).Resume(ctx, 1)
	if err == nil {
		t.Fatal("destination suspension limit was lost")
	}
	if got := run(t, destination, "42"); !reflect.DeepEqual(got, int64(42)) {
		t.Fatal(got)
	}
}

func TestMonty12RestoreCapsHostDuration(t *testing.T) {
	for _, sourceBudget := range []time.Duration{0, 2 * time.Second} {
		t.Run(sourceBudget.String(), func(t *testing.T) {
			pool := integrationPool(t, monty.Options{MaxProcesses: 2, DurationLimitGrace: 10 * time.Millisecond})
			source := integrationSession(t, pool, monty.CheckoutOptions{Limits: monty.ResourceLimits{MaxDuration: sourceBudget}})
			run(t, source, "value = 42")
			dump, err := source.Dump(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			destination := integrationSession(t, pool, monty.CheckoutOptions{Limits: monty.ResourceLimits{MaxDuration: 5 * time.Millisecond}})
			if err := destination.LoadSession(context.Background(), dump); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			started := time.Now()
			_, err = destination.FeedRun(ctx, "while True: pass")
			var crashed *monty.CrashedError
			if !errors.As(err, &crashed) || !crashed.TimedOut || time.Since(started) >= time.Second {
				t.Fatalf("destination duration lost: %v", err)
			}
		})
	}
}
