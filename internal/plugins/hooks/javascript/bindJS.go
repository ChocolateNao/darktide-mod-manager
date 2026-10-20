package javascript

import (
	"context"
	"encoding/json"

	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/hooks/host"
	"github.com/dop251/goja"
)

// install exposes the restricted `dmm` surface plus console/print sugar; no
// require, fetch, process or filesystem. The context goes through JSON because
// goja exposes struct methods, never fields, to scripts.
func install(vm *goja.Runtime, h *host.Host) error {
	dmm := vm.NewObject()

	if err := setContext(vm, dmm, h); err != nil {
		return err
	}

	if err := setLog(vm, dmm, h); err != nil {
		return err
	}

	if err := setPaths(vm, dmm, h); err != nil {
		return err
	}

	if err := setFS(vm, dmm, h); err != nil {
		return err
	}

	if err := setConfig(vm, dmm, h); err != nil {
		return err
	}

	if err := setStorage(vm, dmm, h); err != nil {
		return err
	}

	if err := setEnv(vm, dmm, h); err != nil {
		return err
	}

	if err := vm.Set("dmm", dmm); err != nil {
		return wrap(err, "expose dmm to the script")
	}

	return setSugar(vm, h)
}

func setContext(vm *goja.Runtime, dmm *goja.Object, h *host.Host) error {
	raw, err := json.Marshal(h.ContextValue())
	if err != nil {
		return wrap(err, "encode the hook context")
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return wrap(err, "decode the hook context")
	}

	if err := dmm.Set("context", decoded); err != nil {
		return wrap(err, "expose dmm.context")
	}

	return nil
}

func setLog(vm *goja.Runtime, dmm *goja.Object, h *host.Host) error {
	log := vm.NewObject()

	for _, name := range []string{"info", "warn", "error", "debug"} {
		level := name

		if err := log.Set(name, func(call goja.FunctionCall) goja.Value {
			h.Log(level, joinArgs(call))

			return goja.Undefined()
		}); err != nil {
			return wrap(err, "expose dmm.log.%s", name)
		}
	}

	if err := dmm.Set("log", log); err != nil {
		return wrap(err, "expose dmm.log")
	}

	limits := vm.NewObject()
	if err := limits.Set("maxLogLines", host.MaxLogLines); err != nil {
		return wrap(err, "expose dmm.limits.maxLogLines")
	}

	if err := limits.Set("maxLogLineLength", host.MaxLogLineLength); err != nil {
		return wrap(err, "expose dmm.limits.maxLogLineLength")
	}

	if err := dmm.Set("limits", limits); err != nil {
		return wrap(err, "expose dmm.limits")
	}

	return nil
}

func setPaths(vm *goja.Runtime, dmm *goja.Object, h *host.Host) error {
	paths := vm.NewObject()

	for _, name := range []string{"game", "mods", "mod", "cache", "temp", "storage"} {
		value, _ := h.Path(name)

		if err := paths.Set(name, value); err != nil {
			return wrap(err, "expose dmm.paths.%s", name)
		}
	}

	if err := dmm.Set("paths", paths); err != nil {
		return wrap(err, "expose dmm.paths")
	}

	return nil
}

func setFS(vm *goja.Runtime, dmm *goja.Object, h *host.Host) error {
	fsObj := vm.NewObject()

	readFile := func(call goja.FunctionCall) goja.Value {
		out, err := h.ReadFile(arg(call, 0))
		if err != nil {
			panic(vm.NewGoError(err))
		}

		return vm.ToValue(out)
	}

	list := func(call goja.FunctionCall) goja.Value {
		out, err := h.List(arg(call, 0))
		if err != nil {
			panic(vm.NewGoError(err))
		}

		return vm.ToValue(out)
	}

	exists := func(call goja.FunctionCall) goja.Value {
		out, err := h.Exists(arg(call, 0))
		if err != nil {
			panic(vm.NewGoError(err))
		}

		return vm.ToValue(out)
	}

	stat := func(call goja.FunctionCall) goja.Value {
		out, err := h.Stat(arg(call, 0))
		if err != nil {
			panic(vm.NewGoError(err))
		}

		return vm.ToValue(out)
	}

	writeTemp := func(call goja.FunctionCall) goja.Value {
		out, err := h.WriteTemp(arg(call, 0), arg(call, 1))
		if err != nil {
			panic(vm.NewGoError(err))
		}

		return vm.ToValue(out)
	}

	entries := []struct {
		value any
		name  string
	}{
		{name: "readFile", value: readFile},
		{name: "list", value: list},
		{name: "exists", value: exists},
		{name: "stat", value: stat},
		{name: "writeTemp", value: writeTemp},
	}
	for _, entry := range entries {
		if err := fsObj.Set(entry.name, entry.value); err != nil {
			return wrap(err, "expose dmm.fs.%s", entry.name)
		}
	}

	if err := dmm.Set("fs", fsObj); err != nil {
		return wrap(err, "expose dmm.fs")
	}

	return nil
}

func setConfig(vm *goja.Runtime, dmm *goja.Object, h *host.Host) error {
	configObj := vm.NewObject()

	get := func(call goja.FunctionCall) goja.Value {
		out, err := h.ConfigGet(arg(call, 0))
		if err != nil {
			panic(vm.NewGoError(err))
		}

		return vm.ToValue(out)
	}

	getAll := func(goja.FunctionCall) goja.Value {
		out, err := h.ConfigGetAll()
		if err != nil {
			panic(vm.NewGoError(err))
		}

		return vm.ToValue(out)
	}

	set := func(call goja.FunctionCall) goja.Value {
		if err := h.ConfigSet(arg(call, 0), arg(call, 1)); err != nil {
			panic(vm.NewGoError(err))
		}

		return goja.Undefined()
	}

	entries := []struct {
		value any
		name  string
	}{
		{name: "get", value: get},
		{name: "getAll", value: getAll},
		{name: "set", value: set},
	}
	for _, entry := range entries {
		if err := configObj.Set(entry.name, entry.value); err != nil {
			return wrap(err, "expose dmm.config.%s", entry.name)
		}
	}

	if err := dmm.Set("config", configObj); err != nil {
		return wrap(err, "expose dmm.config")
	}

	return nil
}

func setStorage(vm *goja.Runtime, dmm *goja.Object, h *host.Host) error {
	storageObj := vm.NewObject()

	list := func(goja.FunctionCall) goja.Value {
		out, err := h.StorageList(context.Background())
		if err != nil {
			panic(vm.NewGoError(err))
		}

		return vm.ToValue(out)
	}

	path := func(call goja.FunctionCall) goja.Value {
		out, err := h.StoragePath(context.Background(), arg(call, 0), arg(call, 1), arg(call, 2))
		if err != nil {
			panic(vm.NewGoError(err))
		}

		return vm.ToValue(out)
	}

	if err := storageObj.Set("list", list); err != nil {
		return wrap(err, "expose dmm.storage.list")
	}

	if err := storageObj.Set("path", path); err != nil {
		return wrap(err, "expose dmm.storage.path")
	}

	if err := dmm.Set("storage", storageObj); err != nil {
		return wrap(err, "expose dmm.storage")
	}

	return nil
}

func setEnv(vm *goja.Runtime, dmm *goja.Object, h *host.Host) error {
	envObj := vm.NewObject()

	get := func(call goja.FunctionCall) goja.Value {
		out, err := h.EnvGet(arg(call, 0))
		if err != nil {
			panic(vm.NewGoError(err))
		}

		return vm.ToValue(out)
	}

	if err := envObj.Set("get", get); err != nil {
		return wrap(err, "expose dmm.env.get")
	}

	if err := dmm.Set("env", envObj); err != nil {
		return wrap(err, "expose dmm.env")
	}

	return nil
}

// setSugar keeps console.log/print working: they are the surface existing
// scripts know.
func setSugar(vm *goja.Runtime, h *host.Host) error {
	console := vm.NewObject()

	for _, name := range []string{"log", "warn", "error", "info"} {
		level := name
		if level == "log" {
			level = "info"
		}

		if err := console.Set(name, func(call goja.FunctionCall) goja.Value {
			h.Log(level, joinArgs(call))

			return goja.Undefined()
		}); err != nil {
			return wrap(err, "expose console.%s", name)
		}
	}

	if err := vm.Set("console", console); err != nil {
		return wrap(err, "expose console to the script")
	}

	if err := vm.Set("print", func(call goja.FunctionCall) goja.Value {
		h.Log("info", joinArgs(call))

		return goja.Undefined()
	}); err != nil {
		return wrap(err, "expose print to the script")
	}

	return nil
}

func arg(call goja.FunctionCall, i int) string {
	if i >= len(call.Arguments) {
		return ""
	}

	return call.Arguments[i].String()
}

func joinArgs(call goja.FunctionCall) string {
	out := ""

	for i, value := range call.Arguments {
		if i > 0 {
			out += " "
		}

		out += value.String()
	}

	return out
}
