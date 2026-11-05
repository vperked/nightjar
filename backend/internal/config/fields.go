package config

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// Kind is how a setting is parsed and edited.
type Kind string

const (
	KindBool   Kind = "bool"
	KindInt    Kind = "int"
	KindNumber Kind = "number"
	KindText   Kind = "text"
	KindSecret Kind = "secret" // never sent back to the dashboard
	KindList   Kind = "list"   // comma-separated
)

// Field is one setting: its environment key, default and bounds, how the dashboard presents it,
// and where its value lands in Settings. Settings.Fields is the single declaration of all of them.
type Field struct {
	Key     string   `json:"key"`
	Group   string   `json:"group"`
	Label   string   `json:"label"`
	Help    string   `json:"help,omitempty"`
	Unit    string   `json:"unit,omitempty"`
	Kind    Kind     `json:"kind"`
	Default string   `json:"default"`
	Min     *float64 `json:"min,omitempty"`
	Max     *float64 `json:"max,omitempty"`
	Options []string `json:"options,omitempty"`
	// Startup settings decide how the dashboard itself is reached or stored, so they are read
	// from the environment only and shown read-only.
	Startup bool `json:"startup,omitempty"`
	// Live settings take effect on the next monitoring cycle; the rest restart the monitor.
	Live bool `json:"live,omitempty"`
	// Advanced settings are tuning knobs the dashboard folds away until asked.
	Advanced bool `json:"advanced,omitempty"`

	parse   func(raw string) error // assigns the parsed value; "" assigns the default
	format  func() string
	present func() bool
}

// Value is the field's current value as text; secrets always read as empty.
func (f Field) Value() string { return f.format() }

// IsSet reports whether the field has a non-empty value, which for a secret is all that is shown.
func (f Field) IsSet() bool { return f.present() }

// Set parses raw ("" means the default) into the Settings the field was built from.
func (f Field) Set(raw string) error {
	if len(raw) > maxEnvValueLength {
		return fmt.Errorf("longer than %d characters", maxEnvValueLength)
	}
	return f.parse(strings.TrimSpace(raw))
}

func (f Field) withFormat(format func() string) Field {
	f.format = format
	f.present = func() bool { return format() != "" }
	return f
}

func (f Field) describe(label, unit, help string) Field {
	f.Label, f.Unit, f.Help = label, unit, help
	return f
}

func (f Field) startup() Field {
	f.Startup = true
	return f
}

func bounds(min, max *float64, v float64) error {
	if min != nil && v < *min {
		return fmt.Errorf("must be at least %g", *min)
	}
	if max != nil && v > *max {
		return fmt.Errorf("must be at most %g", *max)
	}
	return nil
}

func limits(b []float64) (min, max *float64) {
	if len(b) > 0 {
		min = &b[0]
	}
	if len(b) > 1 {
		max = &b[1]
	}
	return min, max
}

func number(dst *float64, key string, def float64, b ...float64) Field {
	f := Field{Key: key, Kind: KindNumber, Default: strconv.FormatFloat(def, 'f', -1, 64)}
	f.Min, f.Max = limits(b)
	f.parse = func(raw string) error {
		if raw == "" {
			*dst = def
			return nil
		}
		v, err := strconv.ParseFloat(raw, 64)
		// ParseFloat takes "NaN" and "Inf", which slip past any bounds and break what uses them.
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("%q is not a number", raw)
		}
		if err := bounds(f.Min, f.Max, v); err != nil {
			return err
		}
		*dst = v
		return nil
	}
	return f.withFormat(func() string { return strconv.FormatFloat(*dst, 'f', -1, 64) })
}

func integer(dst *int, key string, def int, b ...float64) Field {
	f := Field{Key: key, Kind: KindInt, Default: strconv.Itoa(def)}
	f.Min, f.Max = limits(b)
	f.parse = func(raw string) error {
		if raw == "" {
			*dst = def
			return nil
		}
		v, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("%q is not a whole number", raw)
		}
		if err := bounds(f.Min, f.Max, float64(v)); err != nil {
			return err
		}
		*dst = v
		return nil
	}
	return f.withFormat(func() string { return strconv.Itoa(*dst) })
}

func boolean(dst *bool, key string, def bool) Field {
	f := Field{Key: key, Kind: KindBool, Default: strconv.FormatBool(def)}
	f.parse = func(raw string) error {
		switch strings.ToLower(raw) {
		case "":
			*dst = def
		case "true", "1", "yes", "on":
			*dst = true
		case "false", "0", "no", "off":
			*dst = false
		default:
			return fmt.Errorf("%q is not true or false", raw)
		}
		return nil
	}
	return f.withFormat(func() string { return strconv.FormatBool(*dst) })
}

func text(dst *string, key, def string) Field {
	f := Field{Key: key, Kind: KindText, Default: def}
	f.parse = func(raw string) error {
		if raw == "" {
			raw = def
		}
		*dst = raw
		return nil
	}
	return f.withFormat(func() string { return *dst })
}

// choice is a text setting limited to options (matched case-insensitively); aliases map old spellings.
func choice(dst *string, key, def string, options []string, aliases map[string]string) Field {
	f := text(dst, key, def)
	f.Options = options
	f.parse = func(raw string) error {
		if raw == "" {
			raw = def
		}
		if a, ok := aliases[strings.ToUpper(raw)]; ok {
			raw = a
		}
		// The option's own spelling is kept, whichever case it was given in.
		i := slices.IndexFunc(options, func(o string) bool { return strings.EqualFold(o, raw) })
		if i < 0 {
			return fmt.Errorf("must be one of %s", strings.Join(options, ", "))
		}
		*dst = options[i]
		return nil
	}
	return f
}

func secret(dst *string, key string) Field {
	f := text(dst, key, "")
	f.Kind = KindSecret
	f.format = func() string { return "" }
	f.present = func() bool { return *dst != "" }
	return f
}

func list(dst *[]string, key, def string) Field {
	f := Field{Key: key, Kind: KindList, Default: def}
	f.parse = func(raw string) error {
		if raw == "" {
			raw = def
		}
		*dst = splitList(raw)
		return nil
	}
	return f.withFormat(func() string { return strings.Join(*dst, ", ") })
}

func group(name string, fields ...Field) []Field {
	for i := range fields {
		fields[i].Group = name
	}
	return fields
}

func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
