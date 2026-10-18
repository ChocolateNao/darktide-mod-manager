package lua

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/hooks/host"
	lua "github.com/yuin/gopher-lua"
)

// bind exposes the restricted `dmm` surface to the script, plus print as sugar
// over dmm.log. Nothing else is added: require and dofile are gone, io/os/package
// are never opened, and every capability goes through the host.
func bind(st *lua.LState, h *host.Host, raised *error) error {
	dmm := st.NewTable()

	if err := bindContext(st, dmm, h); err != nil {
		return err
	}

	bindLog(st, dmm, h)
	bindLimits(st, dmm, h)
	bindPaths(st, dmm, h)
	bindFS(st, dmm, h, raised)
	bindConfig(st, dmm, h, raised)
	bindStorage(st, dmm, h, raised)
	bindEnv(st, dmm, h, raised)

	st.SetGlobal("dmm", dmm)
	bindPrint(st, h)

	return nil
}

func bindContext(st *lua.LState, dmm *lua.LTable, h *host.Host) error {
	raw, err := json.Marshal(h.ContextValue())
	if err != nil {
		return fmt.Errorf("encode the hook context: %w", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return fmt.Errorf("decode the hook context: %w", err)
	}

	st.SetField(dmm, "context", toLValue(st, decoded))

	return nil
}

func bindLog(st *lua.LState, dmm *lua.LTable, h *host.Host) {
	logTable := st.NewTable()

	for _, name := range []string{"info", "warn", "error", "debug"} {
		level := name

		st.SetField(logTable, name, st.NewFunction(func(s *lua.LState) int {
			h.Log(level, joinArgs(s))

			return 0
		}))
	}

	st.SetField(dmm, "log", logTable)
}

func bindLimits(st *lua.LState, dmm *lua.LTable, _ *host.Host) {
	limits := st.NewTable()
	limits.RawSetString("maxLogLines", lua.LNumber(host.MaxLogLines))
	limits.RawSetString("maxLogLineLength", lua.LNumber(host.MaxLogLineLength))
	st.SetField(dmm, "limits", limits)
}

func bindPaths(st *lua.LState, dmm *lua.LTable, h *host.Host) {
	paths := st.NewTable()

	for _, name := range []string{"game", "mods", "mod", "cache", "temp", "storage"} {
		value, _ := h.Path(name)
		paths.RawSetString(name, lua.LString(value))
	}

	st.SetField(dmm, "paths", paths)
}

func bindFS(st *lua.LState, dmm *lua.LTable, h *host.Host, raised *error) {
	fsTable := st.NewTable()

	st.SetField(fsTable, "readFile", st.NewFunction(func(s *lua.LState) int {
		out, err := h.ReadFile(s.CheckString(1))
		if err != nil {
			fail(s, raised, err)
		}

		s.Push(lua.LString(out))

		return 1
	}))

	st.SetField(fsTable, "list", st.NewFunction(func(s *lua.LState) int {
		out, err := h.List(s.CheckString(1))
		if err != nil {
			fail(s, raised, err)
		}

		s.Push(toLValueJSON(s, out))

		return 1
	}))

	st.SetField(fsTable, "exists", st.NewFunction(func(s *lua.LState) int {
		out, err := h.Exists(s.CheckString(1))
		if err != nil {
			fail(s, raised, err)
		}

		s.Push(lua.LBool(out))

		return 1
	}))

	st.SetField(fsTable, "stat", st.NewFunction(func(s *lua.LState) int {
		out, err := h.Stat(s.CheckString(1))
		if err != nil {
			fail(s, raised, err)
		}

		s.Push(toLValueJSON(s, out))

		return 1
	}))

	st.SetField(fsTable, "writeTemp", st.NewFunction(func(s *lua.LState) int {
		out, err := h.WriteTemp(s.CheckString(1), s.CheckString(2))
		if err != nil {
			fail(s, raised, err)
		}

		s.Push(lua.LString(out))

		return 1
	}))

	st.SetField(dmm, "fs", fsTable)
}

func bindConfig(st *lua.LState, dmm *lua.LTable, h *host.Host, raised *error) {
	configTable := st.NewTable()

	st.SetField(configTable, "get", st.NewFunction(func(s *lua.LState) int {
		out, err := h.ConfigGet(s.CheckString(1))
		if err != nil {
			fail(s, raised, err)
		}

		s.Push(lua.LString(out))

		return 1
	}))

	st.SetField(configTable, "getAll", st.NewFunction(func(s *lua.LState) int {
		out, err := h.ConfigGetAll()
		if err != nil {
			fail(s, raised, err)
		}

		s.Push(toLValueJSON(s, out))

		return 1
	}))

	st.SetField(configTable, "set", st.NewFunction(func(s *lua.LState) int {
		if err := h.ConfigSet(s.CheckString(1), s.CheckString(2)); err != nil {
			fail(s, raised, err)
		}

		return 0
	}))

	st.SetField(dmm, "config", configTable)
}

func bindStorage(st *lua.LState, dmm *lua.LTable, h *host.Host, raised *error) {
	storageTable := st.NewTable()

	st.SetField(storageTable, "list", st.NewFunction(func(s *lua.LState) int {
		out, err := h.StorageList(context.Background())
		if err != nil {
			fail(s, raised, err)
		}

		s.Push(toLValueJSON(s, out))

		return 1
	}))

	st.SetField(storageTable, "path", st.NewFunction(func(s *lua.LState) int {
		out, err := h.StoragePath(context.Background(),
			s.CheckString(1), s.CheckString(2), s.CheckString(3))
		if err != nil {
			fail(s, raised, err)
		}

		s.Push(lua.LString(out))

		return 1
	}))

	st.SetField(dmm, "storage", storageTable)
}

func bindEnv(st *lua.LState, dmm *lua.LTable, h *host.Host, raised *error) {
	envTable := st.NewTable()

	st.SetField(envTable, "get", st.NewFunction(func(s *lua.LState) int {
		out, err := h.EnvGet(s.CheckString(1))
		if err != nil {
			fail(s, raised, err)
		}

		s.Push(lua.LString(out))

		return 1
	}))

	st.SetField(dmm, "env", envTable)
}

// bindPrint keeps `print` working: it is the surface existing scripts know, and
// the JavaScript binding offers the same sugar.
func bindPrint(st *lua.LState, h *host.Host) {
	st.SetGlobal("print", st.NewFunction(func(s *lua.LState) int {
		h.Log("info", joinArgs(s))

		return 0
	}))
}

// fail records the Go error and raises it inside the script: pcall can still
// catch the Lua error, but the run reports the original typed code.
func fail(s *lua.LState, raised *error, err error) {
	if raised != nil && *raised == nil {
		*raised = err
	}

	s.RaiseError("%s", err.Error())
}

func joinArgs(s *lua.LState) string {
	parts := make([]string, 0, s.GetTop())
	for i := 1; i <= s.GetTop(); i++ {
		parts = append(parts, s.Get(i).String())
	}

	return strings.Join(parts, " ")
}

// toLValueJSON converts a Go value through its JSON encoding, so host structs
// keep the same field names the JavaScript binding shows.
func toLValueJSON(st *lua.LState, value any) lua.LValue {
	raw, err := json.Marshal(value)
	if err != nil {
		return lua.LNil
	}

	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return lua.LNil
	}

	return toLValue(st, decoded)
}

// toLValue converts a JSON-shaped Go value into Lua values: maps become
// tables, slices become arrays, everything else becomes a scalar.
func toLValue(st *lua.LState, value any) lua.LValue {
	switch typed := value.(type) {
	case nil:
		return lua.LNil
	case string:
		return lua.LString(typed)
	case bool:
		return lua.LBool(typed)
	case float64:
		return lua.LNumber(typed)
	case int:
		return lua.LNumber(typed)
	case int64:
		return lua.LNumber(typed)
	case map[string]any:
		table := st.NewTable()
		for key, item := range typed {
			table.RawSetString(key, toLValue(st, item))
		}

		return table
	case []any:
		table := st.NewTable()
		for _, item := range typed {
			table.Append(toLValue(st, item))
		}

		return table
	case []string:
		table := st.NewTable()
		for _, item := range typed {
			table.Append(lua.LString(item))
		}

		return table
	default:
		// Host structs arrive here through the caller's JSON round trip; this branch
		// only sees unexpected shapes, and formatting them beats panicking.
		return lua.LString(fmt.Sprintf("%v", typed))
	}
}
