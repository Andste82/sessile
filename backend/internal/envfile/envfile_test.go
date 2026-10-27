package envfile

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const sentinel = "S3NTINEL-VALUE"

func TestParse(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		want     map[string]string
		warnings int
	}{
		{"plain", "A=1\nB=two", map[string]string{"A": "1", "B": "two"}, 0},
		{"export prefix", "export A=1\nexport  B = 2", map[string]string{"A": "1", "B": "2"}, 0},
		{"comments and blanks", "# c\n\n  # indented\nA=1", map[string]string{"A": "1"}, 0},
		{"inline comment", "A=1 # note\nB=x#y\nC= #only", map[string]string{"A": "1", "B": "x#y", "C": ""}, 0},
		{"single quotes literal", `A='a b $X \n # c'`, map[string]string{"A": `a b $X \n # c`}, 0},
		{"double quotes escapes", `A="l1\nl2\t\"q\" \\ \$X \z"`, map[string]string{"A": "l1\nl2\t\"q\" \\ $X \\z"}, 0},
		{"quoted then comment", `A="x" # c`, map[string]string{"A": "x"}, 0},
		{"no expansion", "A=$HOME\nB=`id`\nC=${X}", map[string]string{"A": "$HOME", "B": "`id`", "C": "${X}"}, 0},
		{"CRLF", "A=1\r\nB='2'\r\n", map[string]string{"A": "1", "B": "2"}, 0},
		{"BOM", "\xef\xbb\xbfA=1", map[string]string{"A": "1"}, 0},
		{"last duplicate wins", "A=1\nA=2", map[string]string{"A": "2"}, 0},
		{"empty value", "A=", map[string]string{"A": ""}, 0},
		{"value with equals", "A=b=c", map[string]string{"A": "b=c"}, 0},
		{"bad name", "1A=x\nA-B=y\nOK=1", map[string]string{"OK": "1"}, 2},
		{"no equals", "justtext\nOK=1", map[string]string{"OK": "1"}, 1},
		{"reserved", "TERM=x\nLANG=y\nHISTFILE=z\nfish_history=q\nPATH=/x\nLC_ALL=C", map[string]string{"PATH": "/x", "LC_ALL": "C"}, 4},
		{"unterminated quote", "A=\"abc\nB='abc\nOK=1", map[string]string{"OK": "1"}, 2},
		{"text after quote", `A="x" y`, map[string]string{}, 1},
		{"NUL", "A=a\x00b\nOK=1", map[string]string{"OK": "1"}, 1},
		{"value too long", "A=" + strings.Repeat("x", MaxValueLen+1) + "\nB=" + strings.Repeat("x", MaxValueLen), map[string]string{"B": strings.Repeat("x", MaxValueLen)}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, warns := Parse([]byte(c.in))
			m := map[string]string{}
			for _, e := range got {
				if e.Secret || e.Default {
					t.Errorf("%s: flags set on dotenv entry", e.Name)
				}
				m[e.Name] = e.Value
			}
			if !reflect.DeepEqual(m, c.want) {
				t.Errorf("entries = %q, want %q", m, c.want)
			}
			if len(warns) != c.warnings {
				t.Errorf("warnings = %q, want %d", warns, c.warnings)
			}
			for _, w := range warns {
				if !strings.HasPrefix(w, ".env line ") {
					t.Errorf("warning %q lacks the line prefix", w)
				}
			}
		})
	}
}

func TestParseWarningLineNumbers(t *testing.T) {
	_, warns := Parse([]byte("A=1\n\nTERM=x\n"))
	if len(warns) != 1 || !strings.HasPrefix(warns[0], ".env line 3:") {
		t.Fatalf("warnings = %q", warns)
	}
}

func TestParseNeverLeaksValues(t *testing.T) {
	in := strings.Join([]string{
		"TERM=" + sentinel,
		"1BAD=" + sentinel,
		"A=\"" + sentinel,
		"B='" + sentinel,
		"C=\"" + sentinel + "\" trailing",
		"D=" + sentinel + "\x00",
		"E=" + strings.Repeat(sentinel, 1000),
		"F " + sentinel,
	}, "\n")
	_, warns := Parse([]byte(in))
	if len(warns) != 8 {
		t.Fatalf("warnings = %q", warns)
	}
	for _, w := range warns {
		if strings.Contains(w, sentinel) {
			t.Errorf("warning leaks a value: %q", w)
		}
	}
}

func TestParseLimits(t *testing.T) {
	t.Run("max entries", func(t *testing.T) {
		var b strings.Builder
		for i := 0; i < MaxEntries+5; i++ {
			b.WriteString("V" + strings.Repeat("x", 3) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "=1\n")
		}
		got, warns := Parse([]byte(b.String()))
		if len(got) != MaxEntries || len(warns) != 5 {
			t.Fatalf("entries=%d warnings=%d", len(got), len(warns))
		}
	})
	t.Run("max dotenv bytes", func(t *testing.T) {
		var b strings.Builder
		val := strings.Repeat("v", 4000)
		for _, n := range []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "J"} {
			b.WriteString(n + "=" + val + "\n")
		}
		got, warns := Parse([]byte(b.String()))
		if len(got) != 8 || len(warns) != 2 {
			t.Fatalf("entries=%d warnings=%q", len(got), warns)
		}
	})
	t.Run("replacing a duplicate does not double count", func(t *testing.T) {
		val := strings.Repeat("v", 4000)
		in := ""
		for i := 0; i < 20; i++ {
			in += "A=" + val + "\n"
		}
		got, warns := Parse([]byte(in))
		if len(got) != 1 || len(warns) != 0 {
			t.Fatalf("entries=%d warnings=%q", len(got), warns)
		}
	})
}

func TestValidate(t *testing.T) {
	long := strings.Repeat("a", MaxNameLen+1)
	var many []Entry
	for i := 0; i <= MaxEntries; i++ {
		many = append(many, Entry{Name: "V" + strings.Repeat("a", i/26) + string(rune('a'+i%26))})
	}
	var big []Entry
	for _, n := range []string{"A", "B", "C", "D", "E"} {
		big = append(big, Entry{Name: n, Value: strings.Repeat("v", MaxValueLen)})
	}
	cases := []struct {
		name string
		in   []Entry
		ok   bool
	}{
		{"empty", nil, true},
		{"fine", []Entry{{Name: "A_1", Value: "x\ny"}, {Name: "_b"}}, true},
		{"empty name", []Entry{{Name: ""}}, false},
		{"digit first", []Entry{{Name: "1A"}}, false},
		{"dash", []Entry{{Name: "A-B"}}, false},
		{"space", []Entry{{Name: "A B"}}, false},
		{"name too long", []Entry{{Name: long}}, false},
		{"reserved", []Entry{{Name: "TERM"}}, false},
		{"reserved fish", []Entry{{Name: "fish_history"}}, false},
		{"case sensitive reserved", []Entry{{Name: "term"}}, true},
		{"LC_ALL allowed", []Entry{{Name: "LC_ALL"}, {Name: "PATH"}, {Name: "LD_PRELOAD"}}, true},
		{"duplicate", []Entry{{Name: "A"}, {Name: "A"}}, false},
		{"case differs", []Entry{{Name: "A"}, {Name: "a"}}, true},
		{"NUL", []Entry{{Name: "A", Value: "a\x00"}}, false},
		{"value too long", []Entry{{Name: "A", Value: strings.Repeat("x", MaxValueLen+1)}}, false},
		{"value at limit", []Entry{{Name: "A", Value: strings.Repeat("x", MaxValueLen)}}, true},
		{"too many", many, false},
		{"total too large", big, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Validate(c.in)
			if (err == nil) != c.ok {
				t.Fatalf("err = %v, ok want %v", err, c.ok)
			}
			if err != nil && !errors.Is(err, ErrInvalid) {
				t.Errorf("error does not wrap ErrInvalid: %v", err)
			}
		})
	}
}

func TestValidateNeverLeaksValues(t *testing.T) {
	cases := [][]Entry{
		{{Name: "A", Value: sentinel + "\x00"}},
		{{Name: "A", Value: strings.Repeat(sentinel, 1000)}},
		{{Name: "TERM", Value: sentinel}},
		{{Name: "A", Value: sentinel}, {Name: "A", Value: sentinel}},
		{{Name: "bad name", Value: sentinel}},
	}
	for _, in := range cases {
		err := Validate(in)
		if err == nil || strings.Contains(err.Error(), sentinel) {
			t.Errorf("err = %v", err)
		}
	}
}

func TestReservedList(t *testing.T) {
	want := []string{"TERM", "LANG", "HISTFILE", "HISTSIZE", "HISTFILESIZE", "SAVEHIST", "PROMPT_COMMAND", "fish_history"}
	if !reflect.DeepEqual(ReservedNames, want) {
		t.Fatalf("ReservedNames = %v", ReservedNames)
	}
	for _, n := range want {
		if !IsReserved(n) {
			t.Errorf("%s not reserved", n)
		}
	}
	for _, n := range []string{"LC_ALL", "LC_CTYPE", "PATH", "LD_LIBRARY_PATH", "term"} {
		if IsReserved(n) {
			t.Errorf("%s must be allowed", n)
		}
	}
}

func TestMerge(t *testing.T) {
	session := []Entry{
		{Name: "D_ONLY", Value: "d", Default: true},
		{Name: "D_ENV", Value: "d", Default: true},
		{Name: "D_OWN", Value: "d", Default: true, Secret: true},
		{Name: "OWN_ENV", Value: "own"},
		{Name: "OWN_ONLY", Value: "own", Secret: true},
	}
	dotenv := []Entry{
		{Name: "D_ENV", Value: "env"},
		{Name: "D_OWN", Value: "env"},
		{Name: "OWN_ENV", Value: "env"},
		{Name: "ENV_ONLY", Value: "env", Secret: true, Default: true}, // flags must be cleared
	}
	got := Merge(session, dotenv)
	want := []Entry{
		{Name: "D_ENV", Value: "env"},
		{Name: "D_ONLY", Value: "d", Default: true},
		{Name: "D_OWN", Value: "env"},
		{Name: "ENV_ONLY", Value: "env"},
		{Name: "OWN_ENV", Value: "own"},
		{Name: "OWN_ONLY", Value: "own", Secret: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	if session[0].Name != "D_ONLY" || dotenv[3].Secret != true {
		t.Error("inputs were modified")
	}
	if len(Merge(nil, nil)) != 0 {
		t.Error("empty merge not empty")
	}
}

func TestInherit(t *testing.T) {
	defaults := []Entry{{Name: "A", Value: "1"}, {Name: "B", Value: "2", Secret: true}, {Name: "C", Value: "3"}}
	own := []Entry{{Name: "C", Value: "own"}, {Name: "Z", Value: "z", Default: true}}
	got := Inherit(defaults, own, []string{"A", "unknown"})
	want := []Entry{
		{Name: "B", Value: "2", Secret: true, Default: true},
		{Name: "C", Value: "own"},
		{Name: "Z", Value: "z"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if got := Inherit(nil, nil, nil); len(got) != 0 {
		t.Errorf("got %v", got)
	}
}

func sp(s string) *string { return &s }

func TestApplyPatch(t *testing.T) {
	existing := []Entry{
		{Name: "PLAIN", Value: "p"},
		{Name: "SEC", Value: "s", Secret: true},
		{Name: "DEF", Value: "d", Default: true},
		{Name: "DEFSEC", Value: "ds", Default: true, Secret: true},
	}
	cases := []struct {
		name  string
		patch []PatchEntry
		want  []Entry
		err   bool
	}{
		{"delete all", nil, []Entry{}, false},
		{"set new value", []PatchEntry{{Name: "PLAIN", Value: sp("q")}}, []Entry{{Name: "PLAIN", Value: "q"}}, false},
		{"add", []PatchEntry{{Name: "NEW", Value: sp("n")}}, []Entry{{Name: "NEW", Value: "n"}}, false},
		{"secret keep", []PatchEntry{{Name: "SEC", Secret: true}}, []Entry{{Name: "SEC", Value: "s", Secret: true}}, false},
		{"default secret keep keeps default flag", []PatchEntry{{Name: "DEFSEC", Secret: true}}, []Entry{{Name: "DEFSEC", Value: "ds", Secret: true, Default: true}}, false},
		{"non-secret becomes secret keeping value", []PatchEntry{{Name: "PLAIN", Secret: true}}, []Entry{{Name: "PLAIN", Value: "p", Secret: true}}, false},
		{"default same value keeps default", []PatchEntry{{Name: "DEF", Value: sp("d")}}, []Entry{{Name: "DEF", Value: "d", Default: true}}, false},
		{"default changed value drops default", []PatchEntry{{Name: "DEF", Value: sp("x")}}, []Entry{{Name: "DEF", Value: "x"}}, false},
		{"default same value but now secret drops default", []PatchEntry{{Name: "DEF", Value: sp("d"), Secret: true}}, []Entry{{Name: "DEF", Value: "d", Secret: true}}, false},
		{"secret retyped", []PatchEntry{{Name: "SEC", Value: sp("new"), Secret: true}}, []Entry{{Name: "SEC", Value: "new", Secret: true}}, false},
		{"nil value non-secret", []PatchEntry{{Name: "PLAIN"}}, nil, true},
		{"secret without existing", []PatchEntry{{Name: "NEW", Secret: true}}, nil, true},
		{"existing secret to non-secret without value", []PatchEntry{{Name: "SEC"}}, nil, true},
		{"rename", []PatchEntry{{Name: "RENAMED", Secret: true}}, nil, true},
		{"reserved", []PatchEntry{{Name: "TERM", Value: sp("x")}}, nil, true},
		{"duplicate", []PatchEntry{{Name: "A", Value: sp("1")}, {Name: "A", Value: sp("2")}}, nil, true},
		{"NUL", []PatchEntry{{Name: "A", Value: sp("a\x00")}}, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ApplyPatch(existing, c.patch)
			if c.err {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("err = %v, want ErrInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %+v want %+v", got, c.want)
			}
		})
	}
	if existing[1].Value != "s" || len(existing) != 4 {
		t.Error("existing was modified")
	}
}

func TestApplyPatchErrorsNameOnly(t *testing.T) {
	existing := []Entry{{Name: "SEC", Value: sentinel, Secret: true}}
	_, err := ApplyPatch(existing, []PatchEntry{{Name: "SEC"}, {Name: "X", Value: sp(sentinel + "\x00")}})
	if err == nil || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("err = %v", err)
	}
}
