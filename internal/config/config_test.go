package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultsAreValid(t *testing.T) {
	if err := DefaultClient().Validate(); err != nil {
		t.Fatalf("client defaults invalid: %v", err)
	}
	if err := DefaultServer().Validate(); err != nil {
		t.Fatalf("server defaults invalid: %v", err)
	}
}

func TestValidationCatchesBadValues(t *testing.T) {
	c := DefaultClient()
	c.Bonding.Mode = "turbo"
	c.Bonding.Redundancy = 99
	c.Bonding.RealtimePorts = "x"
	c.Server.Address = "nohost"
	err := c.Validate()
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"bonding.mode", "bonding.redundancy", "realtimePorts", "server.address"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
	s := DefaultServer()
	s.Network.Pool = "fd00::/64"
	if s.Validate() == nil {
		t.Fatal("ipv6 pool should be rejected")
	}
}

// Every leaf setting must appear in the schema: "no hidden settings".
func TestSchemaCoversEverySetting(t *testing.T) {
	for _, v := range []any{DefaultClient(), DefaultServer()} {
		keys := map[string]bool{}
		for _, s := range Schema(v) {
			for _, f := range s.Fields {
				keys[f.Key] = true
				if f.Label == "" {
					t.Errorf("%s has no label", f.Key)
				}
			}
		}
		walk(reflect.ValueOf(v).Elem(), "", func(key string, _ reflect.StructField, _ reflect.Value) {
			if !keys[key] {
				t.Errorf("setting %s missing from schema", key)
			}
		})
	}
}

func TestSchemaEnumAndRange(t *testing.T) {
	var mode, red *Field
	for _, s := range Schema(DefaultClient()) {
		for i := range s.Fields {
			switch s.Fields[i].Key {
			case "bonding.mode":
				mode = &s.Fields[i]
			case "bonding.redundancy":
				red = &s.Fields[i]
			}
		}
	}
	if mode == nil || mode.Type != "enum" || len(mode.Options) != 4 {
		t.Fatalf("bad mode field %+v", mode)
	}
	if red == nil || red.Type != "int" || *red.Min != 1 || *red.Max != 8 {
		t.Fatalf("bad redundancy field %+v", red)
	}
}

func TestStorePersistsAndNotifies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "client.json")
	st, err := Load(path, DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("default config not written")
	}
	var got string
	st.Subscribe(func(old, new *Client) { got = old.Bonding.Mode + "->" + new.Bonding.Mode })
	if err := st.Mutate(func(c *Client) { c.Bonding.Mode = "mirror" }); err != nil {
		t.Fatal(err)
	}
	if got != "smart->mirror" {
		t.Fatalf("notify: %q", got)
	}
	// Invalid update is rejected and not persisted.
	if err := st.Mutate(func(c *Client) { c.Bonding.Mode = "bogus" }); err == nil {
		t.Fatal("expected validation error")
	}
	st2, err := Load(path, DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	if st2.Get().Bonding.Mode != "mirror" {
		t.Fatalf("not persisted: %s", st2.Get().Bonding.Mode)
	}
}

func TestNormalizeFillsMissingFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(path, []byte(`{"bonding":{"mode":"","redundancy":0},"health":{}}`), 0o600)
	st, err := Load(path, DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	c := st.Get()
	if c.Bonding.Mode != "smart" || c.Bonding.Redundancy != 2 || c.Health.ProbeIntervalMs != 250 {
		t.Fatalf("not normalized: %+v %+v", c.Bonding, c.Health)
	}
}

func TestMatchPattern(t *testing.T) {
	pats := "docker*, veth*,lo, *Loopback*"
	for name, want := range map[string]bool{
		"docker0": true, "veth12ab": true, "lo": true, "lo0": false,
		"wlan0": false, "Loopback Pseudo-Interface 1": true, "eth0": false,
	} {
		if got := MatchPattern(pats, name); got != want {
			t.Errorf("%s: got %v want %v", name, got, want)
		}
	}
}

func TestParseCIDRs(t *testing.T) {
	ps, err := ParseCIDRs("10.0.0.0/8, 1.2.3.4 ,192.168.1.77/24")
	if err != nil || len(ps) != 3 || ps[1].Bits() != 32 || ps[2].String() != "192.168.1.0/24" {
		t.Fatalf("%v %v", ps, err)
	}
	if _, err := ParseCIDRs("nope"); err == nil {
		t.Fatal("expected error")
	}
}
