package session

import (
	"encoding/json"
	"strings"
	"testing"
)

// ToJSON is the single Info -> wire conversion, and for environment entries
// it is the filter that keeps secret values off the wire.
func TestToJSONEnv(t *testing.T) {
	const sentinel = "SENTINEL-do-not-leak"
	info := Info{
		ID: "s1", DotEnv: true,
		Env: []EnvVar{
			{Name: "A", Value: "plain"},
			{Name: "B", Value: ""},
			{Name: "C", Value: sentinel, Secret: true},
			{Name: "D", Value: sentinel, Secret: true, Default: true},
			{Name: "E", Value: "inherited", Default: true},
		},
	}
	j := ToJSON(info)
	if !j.LoadDotenv || len(j.Env) != 5 {
		t.Fatalf("ToJSON = %+v", j)
	}
	b, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), sentinel) {
		t.Fatalf("secret value on the wire: %s", b)
	}

	var back struct {
		Env []map[string]any `json:"env"`
	}
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		name      string
		value     any // nil = key absent
		secret    bool
		inherited bool
	}{
		{"A", "plain", false, false},
		{"B", "", false, false}, // an empty non-secret value is still sent
		{"C", nil, true, false},
		{"D", nil, true, true},
		{"E", "inherited", false, true},
	}
	for i, w := range want {
		e := back.Env[i]
		v, has := e["value"]
		if e["name"] != w.name || e["secret"] != w.secret || e["inherited"] != w.inherited || e["isSet"] != true {
			t.Errorf("entry %d = %v", i, e)
		}
		if (w.value == nil) == has || (has && v != w.value) {
			t.Errorf("entry %s value = %v (present=%v), want %v", w.name, v, has, w.value)
		}
	}
}

// env is always an array, never null, and the flag defaults to false.
func TestToJSONEnvEmptyIsArray(t *testing.T) {
	b, err := json.Marshal(ToJSON(Info{ID: "s"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"env":[]`) || !strings.Contains(string(b), `"loadDotenv":false`) {
		t.Errorf("empty env = %s", b)
	}
}
