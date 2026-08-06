package eval

import "encoding/json"

// Parameters holds the resolved parameter values for a single eval run.
//
// When an eval is driven from the Braintrust playground via the remote eval
// server, the controls a user configures (a model picker, a numeric threshold,
// and so on) arrive as a flat map of values. The server merges them over the
// declared defaults and hands them to the task on [TaskHooks.Parameters]. For a
// local run, whatever is passed in [RunOpts.Parameters] is what the task sees.
//
// It is a map[string]any, so you can range over it directly, but the typed
// accessors below are the idiomatic way to read a value: they hide the fact
// that JSON numbers decode as float64 and never panic on a type mismatch
// (returning the zero value instead).
//
//	model := hooks.Parameters.String("model")
//	max   := hooks.Parameters.Int("max_length")
type Parameters map[string]any

// Get returns the raw value for name and whether it was present.
func (p Parameters) Get(name string) (any, bool) {
	v, ok := p[name]
	return v, ok
}

// Has reports whether a value for name is present.
func (p Parameters) Has(name string) bool {
	_, ok := p[name]
	return ok
}

// String returns the value for name as a string, or "" if it is absent or not
// a string.
func (p Parameters) String(name string) string {
	if s, ok := p[name].(string); ok {
		return s
	}
	return ""
}

// Int returns the value for name as an int, or 0 if it is absent or not a
// number. JSON numbers decode as float64, so those are converted here; int and
// json.Number are also accepted.
func (p Parameters) Int(name string) int {
	switch v := p[name].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i)
		}
		if f, err := v.Float64(); err == nil {
			return int(f)
		}
	}
	return 0
}

// Float returns the value for name as a float64, or 0 if it is absent or not a
// number.
func (p Parameters) Float(name string) float64 {
	switch v := p[name].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return f
		}
	}
	return 0
}

// Bool returns the value for name as a bool, or false if it is absent or not a
// bool.
func (p Parameters) Bool(name string) bool {
	if b, ok := p[name].(bool); ok {
		return b
	}
	return false
}
