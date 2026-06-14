// Package cli is a tiny helper for parsing argv. The old project
// had a parseflags package that let flags go anywhere in argv; this
// package folds that one function down to a few helpers, used by
// main.go and the harvest driver.
package cli

import (
	"flag"
	"fmt"
	"strings"
)

// Split walks args, identifies which values are flag arguments vs
// positionals, and returns (flagArgs, positionals) in canonical
// order. The flag.FlagSet passed in is used to recognize which
// names are flags and which are not.
//
// The output is suitable for fs.Parse(flagArgs) followed by
// append(fs.Args(), positionals...) to reconstruct a positional
// argv.
//
// The function tolerates:
//   - flags anywhere in the argv
//   - `-flag value` and `-flag=value` forms
//   - bool flags with no value (e.g. -json)
func Split(args []string, fs *flag.FlagSet) (flagArgs, positionals []string) {
	known := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		known[f.Name] = true
	})

	i := 0
	for i < len(args) {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" || a == "--" {
			positionals = append(positionals, a)
			i++
			continue
		}
		// Strip leading dashes.
		name := strings.TrimLeft(a, "-")
		var value string
		var hasValue bool

		if eq := strings.IndexByte(name, '='); eq >= 0 {
			value = name[eq+1:]
			name = name[:eq]
			hasValue = true
		}

		if !known[name] {
			// Unknown flag — treat as positional so the user
			// sees a sensible error.
			positionals = append(positionals, a)
			i++
			continue
		}

		f := fs.Lookup(name)
		if f == nil {
			positionals = append(positionals, a)
			i++
			continue
		}
		isBool := f.Value.String() == "false" || f.Value.String() == "true" || isBoolFlag(f)

		if hasValue {
			flagArgs = append(flagArgs, "-"+name+"="+value)
			i++
			continue
		}
		if isBool {
			flagArgs = append(flagArgs, "-"+name)
			i++
			continue
		}
		// Next arg is the value.
		if i+1 < len(args) {
			flagArgs = append(flagArgs, "-"+name+"="+args[i+1])
			i += 2
			continue
		}
		// No value provided — let flag.Parse error.
		flagArgs = append(flagArgs, "-"+name)
		i++
	}
	return flagArgs, positionals
}

// isBoolFlag returns true if the flag was registered via BoolVar /
// Bool. The flag package exposes the underlying value's type via
// the optional IsBoolFlag method; fall back to a structural check
// (bool values render as exactly "true" or "false"). An unset
// string flag renders as "" — that's *not* a bool, so the
// structural check is more reliable than DefValue.
func isBoolFlag(f *flag.Flag) bool {
	if bv, ok := f.Value.(interface{ IsBoolFlag() bool }); ok {
		return bv.IsBoolFlag()
	}
	if f.Value != nil {
		s := f.Value.String()
		return s == "true" || s == "false"
	}
	return false
}

// Usage returns a help string showing all flags.
func Usage(fs *flag.FlagSet) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Flags:\n")
	fs.VisitAll(func(f *flag.Flag) {
		fmt.Fprintf(&b, "  -%s\n    %s\n", f.Name, f.Usage)
		if f.DefValue != "" && f.DefValue != "false" {
			fmt.Fprintf(&b, "    default: %s\n", f.DefValue)
		}
	})
	return b.String()
}
