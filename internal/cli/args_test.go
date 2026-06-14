package cli

import (
	"flag"
	"reflect"
	"testing"
)

func TestSplit_Basic(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	var s string
	var b bool
	fs.StringVar(&s, "method", "GET", "HTTP method")
	fs.BoolVar(&b, "json", false, "json output")

	flagArgs, pos := Split([]string{"url", "--method", "POST", "-json"}, fs)
	if !reflect.DeepEqual(flagArgs, []string{"-method=POST", "-json"}) {
		t.Errorf("flagArgs = %v", flagArgs)
	}
	if !reflect.DeepEqual(pos, []string{"url"}) {
		t.Errorf("pos = %v", pos)
	}
}

func TestSplit_FlagBeforePositional(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	var s string
	fs.StringVar(&s, "method", "GET", "")

	flagArgs, pos := Split([]string{"--method", "POST", "url1", "url2"}, fs)
	if !reflect.DeepEqual(flagArgs, []string{"-method=POST"}) {
		t.Errorf("flagArgs = %v", flagArgs)
	}
	if !reflect.DeepEqual(pos, []string{"url1", "url2"}) {
		t.Errorf("pos = %v", pos)
	}
}

func TestSplit_EqualsForm(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	var s string
	fs.StringVar(&s, "method", "GET", "")

	flagArgs, pos := Split([]string{"--method=POST", "url"}, fs)
	if !reflect.DeepEqual(flagArgs, []string{"-method=POST"}) {
		t.Errorf("flagArgs = %v", flagArgs)
	}
	if !reflect.DeepEqual(pos, []string{"url"}) {
		t.Errorf("pos = %v", pos)
	}
}

func TestSplit_UnknownFlag(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	var s string
	fs.StringVar(&s, "method", "GET", "")

	flagArgs, pos := Split([]string{"--unknown", "x", "url"}, fs)
	if !reflect.DeepEqual(pos, []string{"--unknown", "x", "url"}) {
		t.Errorf("pos = %v", pos)
	}
	if len(flagArgs) != 0 {
		t.Errorf("flagArgs = %v, want []", flagArgs)
	}
}

func TestSplit_DashAlone(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	flagArgs, pos := Split([]string{"-", "url"}, fs)
	if !reflect.DeepEqual(pos, []string{"-", "url"}) {
		t.Errorf("pos = %v", pos)
	}
	if len(flagArgs) != 0 {
		t.Errorf("flagArgs = %v, want []", flagArgs)
	}
}
