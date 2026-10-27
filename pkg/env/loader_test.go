package env_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/norlis/hydra/pkg/env"
)

type nested struct {
	Inner string `env:"T_INNER" envDefault:"in"`
}

type allKinds struct {
	Str      string        `env:"T_STR"   envDefault:"def"`
	Bool     bool          `env:"T_BOOL"  envDefault:"true"`
	Int      int           `env:"T_INT"   envDefault:"7"`
	Int64    int64         `env:"T_INT64" envDefault:"-9"`
	Uint     uint          `env:"T_UINT"  envDefault:"3"`
	Float    float64       `env:"T_FLOAT" envDefault:"1.5"`
	Dur      time.Duration `env:"T_DUR"   envDefault:"90s"`
	Strs     []string      `env:"T_STRS"  envDefault:"a,b"  envSeparator:","`
	Ints     []int         `env:"T_INTS"  envDefault:"1;2"  envSeparator:";"`
	Nested   nested
	NestedP  *nested
	Required string `env:"T_REQ" required:"true"`
}

func setAll(t *testing.T, values map[string]string) {
	t.Helper()
	for _, k := range []string{"T_STR", "T_BOOL", "T_INT", "T_INT64", "T_UINT", "T_FLOAT", "T_DUR", "T_STRS", "T_INTS", "T_INNER", "T_REQ"} {
		t.Setenv(k, "")
	}
	for k, v := range values {
		t.Setenv(k, v)
	}
}

//nolint:paralleltest // t.Setenv forbids t.Parallel
func TestParse(t *testing.T) {
	t.Run("applies envDefault to every kind and initializes nested structs", func(t *testing.T) {
		setAll(t, map[string]string{"T_REQ": "x"})
		var c allKinds
		if err := env.Parse(&c); err != nil {
			t.Fatal(err)
		}
		if c.Str != "def" || !c.Bool || c.Int != 7 || c.Int64 != -9 || c.Uint != 3 || c.Float != 1.5 || c.Dur != 90*time.Second {
			t.Errorf("scalars = %+v", c)
		}
		if strings.Join(c.Strs, "|") != "a|b" || len(c.Ints) != 2 || c.Ints[1] != 2 {
			t.Errorf("slices = %v %v", c.Strs, c.Ints)
		}
		if c.Nested.Inner != "in" || c.NestedP == nil || c.NestedP.Inner != "in" {
			t.Errorf("nested = %+v %+v", c.Nested, c.NestedP)
		}
	})

	t.Run("empty slice elements from trailing or doubled separators are dropped", func(t *testing.T) {
		setAll(t, map[string]string{"T_REQ": "x", "T_STRS": "a,, b ,", "T_INTS": ";5;;6;"})
		var c allKinds
		if err := env.Parse(&c); err != nil {
			t.Fatal(err)
		}
		if strings.Join(c.Strs, "|") != "a|b" || len(c.Ints) != 2 || c.Ints[0] != 5 || c.Ints[1] != 6 {
			t.Errorf("slices = %q %v", c.Strs, c.Ints)
		}
	})

	t.Run("the variable overrides the default and custom separators are honoured", func(t *testing.T) {
		setAll(t, map[string]string{"T_REQ": "x", "T_INT": "42", "T_INTS": "5; 6 ;7", "T_BOOL": "false"})
		var c allKinds
		if err := env.Parse(&c); err != nil {
			t.Fatal(err)
		}
		if c.Int != 42 || c.Bool || len(c.Ints) != 3 || c.Ints[2] != 7 {
			t.Errorf("parsed = int:%d bool:%v ints:%v", c.Int, c.Bool, c.Ints)
		}
	})

	errCases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"missing required variable errors with its name", map[string]string{}, "T_REQ"},
		{"invalid int is rejected", map[string]string{"T_REQ": "x", "T_INT": "nope"}, "T_INT"},
		{"int with surrounding spaces is rejected", map[string]string{"T_REQ": "x", "T_INT": " 42 "}, "T_INT"},
		{"invalid bool is rejected", map[string]string{"T_REQ": "x", "T_BOOL": "yes"}, "T_BOOL"},
		{"invalid duration is rejected", map[string]string{"T_REQ": "x", "T_DUR": "10 minutes"}, "T_DUR"},
		{"invalid slice element is rejected", map[string]string{"T_REQ": "x", "T_INTS": "1;x"}, "T_INTS"},
	}
	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			setAll(t, tc.env)
			var c allKinds
			err := env.Parse(&c)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want containing %q", err, tc.want)
			}
		})
	}

	t.Run("non-pointer-to-struct argument is rejected", func(t *testing.T) {
		var c allKinds
		if err := env.Parse(c); err == nil {
			t.Error("Parse(value) = nil, want error")
		}
		if err := env.Parse(new(int)); err == nil {
			t.Error("Parse(*int) = nil, want error")
		}
	})

	t.Run("unsupported kind is rejected", func(t *testing.T) {
		t.Setenv("T_CH", "1")
		var c struct {
			Ch chan int `env:"T_CH"`
		}
		if err := env.Parse(&c); err == nil {
			t.Error("Parse with chan field = nil, want error")
		}
	})
}

type fuzzTarget struct {
	Int  int   `env:"F_INT"`
	Ints []int `env:"F_INTS" envSeparator:","`
}

// FuzzParse feeds the same string to a scalar and a slice int field. The
// loader must never panic, and whatever it accepts as an int must be the
// exact decimal value strconv would parse (empty means unset → zero).
func FuzzParse(f *testing.F) {
	for _, s := range []string{"", " 7 ", "-0", "1e3", "0x10", "+5", "1,2,,3", "9223372036854775808", "٣"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		if strings.ContainsRune(v, 0) {
			t.Skip("the OS rejects NUL in environment values; not a loader input")
		}
		t.Setenv("F_INT", v)
		t.Setenv("F_INTS", v)
		var c fuzzTarget
		if err := env.Parse(&c); err != nil {
			return // rejected input: fine, as long as it did not panic
		}
		if v == "" {
			if c.Int != 0 || len(c.Ints) != 0 {
				t.Errorf("empty value parsed as %d / %v", c.Int, c.Ints)
			}
			return
		}
		want, err := strconv.ParseInt(v, 10, 64)
		if err != nil || want != int64(c.Int) {
			t.Errorf("accepted %q as int %d (strconv: %v, %v)", v, c.Int, want, err)
		}
	})
}
