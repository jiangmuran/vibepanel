package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"

	"github.com/jiangmuran/vibepanel/internal/pages"
)

// One runner for every server.js the panel executes: a share page's and a
// plugin's. The two grew up separately (docs/plugins.md §5, rung 2 is "a
// page's server.js with more hooks") and were the same forty lines twice --
// a fresh runtime per call, the stack cap, the budget as an interrupt, the
// request's end as an interrupt, the hook looked up by name, the result as
// JSON under a cap -- which is exactly the code where a fix to one side
// would not reach the other. The budget and the cap are the sandbox; they
// live in one place now.
//
// What stays with each caller is what differs: where the program comes
// from, what ctx carries, and how the data ops are committed.

// jsCall is one call into a server.js.
type jsCall struct {
	// hook is the function looked up on the program; what names the call in
	// errors and the log ("route GET /digest", "onSchedule").
	hook, what string
	args       []any
	budget     time.Duration
	// log receives compile errors, hook failures and result problems.
	log func(level, text string)
	// ctx builds the hook's last argument. nil is a hook that gets its input
	// and nothing else -- a page's transform, which runs on every read and
	// must not be able to write.
	ctx func(vm *goja.Runtime) (*goja.Object, error)
}

// runJS runs one hook of an already compiled program and returns its result
// as plain JSON values, or errNoHook when the program does not define it.
func runJS(ctx context.Context, prog *goja.Program, c jsCall) (any, error) {
	vm := goja.New()
	vm.SetMaxCallStackSize(256)
	timer := time.AfterFunc(c.budget, func() {
		vm.Interrupt(fmt.Sprintf("%s ran past its %v budget", c.what, c.budget))
	})
	defer timer.Stop()
	stop := context.AfterFunc(ctx, func() { vm.Interrupt("the request ended") })
	defer stop()

	failed := func(err error) error {
		msg := jsError(err)
		c.log("error", c.what+": "+msg)
		return dataErrorf("server.js %s failed: %s", c.what, firstLine(msg))
	}
	if _, err := vm.RunProgram(prog); err != nil {
		return nil, failed(err)
	}
	fn, ok := goja.AssertFunction(vm.Get(c.hook))
	if !ok {
		return nil, errNoHook
	}
	args := make([]goja.Value, 0, len(c.args)+1)
	for _, a := range c.args {
		args = append(args, vm.ToValue(jsonRoundTrip(a)))
	}
	if c.ctx != nil {
		obj, err := c.ctx(vm)
		if err != nil {
			return nil, err
		}
		args = append(args, obj)
	}
	out, err := fn(goja.Undefined(), args...)
	if err != nil {
		return nil, failed(err)
	}
	return jsResult(c, out)
}

// jsResult checks a hook's return value: JSON, at most pages.MaxServerResult
// encoded. The cap is what keeps a hook from handing a wall a megabyte on
// every poll.
func jsResult(c jsCall, out goja.Value) (any, error) {
	if out == nil || goja.IsUndefined(out) || goja.IsNull(out) {
		return nil, nil
	}
	raw, err := json.Marshal(out.Export())
	if err != nil {
		c.log("error", c.what+": its result is not JSON")
		return nil, dataErrorf("server.js %s returned something that is not JSON", c.what)
	}
	if len(raw) > pages.MaxServerResult {
		c.log("error", fmt.Sprintf("%s: returned %d bytes, over the %d KiB cap", c.what, len(raw), pages.MaxServerResult>>10))
		return nil, dataErrorf("server.js %s returned more than %d KiB", c.what, pages.MaxServerResult>>10)
	}
	var result any
	_ = json.Unmarshal(raw, &result)
	return result, nil
}

// jsDataObject is ctx.data: get/set/increment/append/reset over a working
// copy, each change checked against the schema as it is made -- so a get
// after a set reads what was set, and a bad value throws at the line that
// wrote it -- and recorded into ops for the caller to commit. guardSet, when
// not nil, may refuse a set with a message: a visitor action's writes list.
func jsDataObject(vm *goja.Runtime, specs map[string]*pages.DataSpec, working map[string]any, ops *[]pageDataOp,
	guardSet func(key string) string) *goja.Object {
	throw := func(msg string) { panic(vm.NewTypeError(msg)) }
	record := func(op pageDataOp) {
		spec := specs[op.Key]
		if spec == nil {
			throw("data." + op.Key + " is not declared")
		}
		switch op.Kind {
		case "set":
			if guardSet != nil {
				if msg := guardSet(op.Key); msg != "" {
					throw(msg)
				}
			}
			if spec.Type == pages.DataLog {
				throw("data." + op.Key + " is a log: append to it")
			}
			clean, err := spec.Check(op.Value, false)
			if err != nil {
				throw("data." + op.Key + " " + err.Error())
			}
			working[op.Key] = clean
		case "increment":
			if spec.Type != pages.DataCounter {
				throw("data." + op.Key + " is not a counter")
			}
			n, _ := working[op.Key].(float64)
			if n += op.By; n < 0 {
				n = 0
			}
			working[op.Key] = n
		case "append":
			if spec.Type != pages.DataLog {
				throw("data." + op.Key + " is not a log")
			}
			entries, _ := working[op.Key].([]any)
			working[op.Key] = append(append([]any{}, entries...), pages.NewLogEntry(op.Item, time.Now().Unix()))
		case "reset":
			working[op.Key] = spec.Zero()
		}
		*ops = append(*ops, op)
	}

	data := vm.NewObject()
	_ = data.Set("get", func(key string) goja.Value {
		v, ok := working[key]
		if !ok {
			return goja.Undefined()
		}
		return vm.ToValue(jsonRoundTrip(v))
	})
	_ = data.Set("set", func(key string, value goja.Value) {
		record(pageDataOp{Kind: "set", Key: key, Value: exportJSON(value)})
	})
	_ = data.Set("increment", func(key string, by goja.Value) {
		n := 1.0
		if by != nil && !goja.IsUndefined(by) {
			n = by.ToFloat()
		}
		record(pageDataOp{Kind: "increment", Key: key, By: n})
	})
	_ = data.Set("append", func(key string, item goja.Value) {
		fields, ok := exportJSON(item).(map[string]any)
		if !ok {
			throw("data." + key + ": append takes an object")
		}
		record(pageDataOp{Kind: "append", Key: key, Item: fields})
	})
	_ = data.Set("reset", func(key string) { record(pageDataOp{Kind: "reset", Key: key}) })
	return data
}

// jsCommon sets the members every ctx has whatever runs it: data, sources,
// now and log.
func jsCommon(vm *goja.Runtime, obj, data *goja.Object, sources any, log func(text string)) {
	_ = obj.Set("data", data)
	_ = obj.Set("sources", vm.ToValue(jsonRoundTrip(sources)))
	_ = obj.Set("now", func() goja.Value {
		d, _ := vm.New(vm.Get("Date"), vm.ToValue(time.Now().UnixMilli()))
		return d
	})
	_ = obj.Set("log", func(call goja.FunctionCall) goja.Value {
		parts := make([]string, 0, len(call.Arguments))
		for _, a := range call.Arguments {
			if _, isString := a.Export().(string); !isString {
				if b, err := json.Marshal(a.Export()); err == nil {
					parts = append(parts, string(b))
					continue
				}
			}
			parts = append(parts, a.String())
		}
		log(strings.Join(parts, " "))
		return goja.Undefined()
	})
}
