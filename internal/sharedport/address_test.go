package sharedport

import "testing"

func TestNormalizeAddress(t *testing.T) {
	cases := []struct {
		name        string
		address     string
		wantKey     string
		wantDial    string
		wantInvalid bool
	}{
		{name: "bare port wildcard", address: ":80", wantKey: "*:80", wantDial: ":80"},
		{name: "explicit ipv4 wildcard", address: "0.0.0.0:80", wantKey: "*:80", wantDial: ":80"},
		{name: "explicit ipv6 wildcard", address: "[::]:80", wantKey: "*:80", wantDial: ":80"},
		{name: "star wildcard", address: "*:80", wantKey: "*:80", wantDial: ":80"},
		{name: "specific ipv4", address: "127.0.0.1:8080", wantKey: "127.0.0.1:8080", wantDial: "127.0.0.1:8080"},
		{name: "specific hostname is lowercased", address: "Example.COM:443", wantKey: "example.com:443", wantDial: "example.com:443"},
		{name: "ipv6 loopback normalizes", address: "[0:0:0:0:0:0:0:1]:443", wantKey: "[::1]:443", wantDial: "[::1]:443"},
		{name: "missing port", address: "127.0.0.1", wantInvalid: true},
		{name: "port out of range", address: "127.0.0.1:70000", wantInvalid: true},
		{name: "non numeric port", address: "127.0.0.1:https", wantInvalid: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, dial, err := NormalizeAddress(tc.address)
			if tc.wantInvalid {
				if err == nil {
					t.Fatalf("expected an error for %q, got key=%q dial=%q", tc.address, key, dial)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeAddress(%q): %v", tc.address, err)
			}
			if key != tc.wantKey {
				t.Errorf("key = %q, want %q", key, tc.wantKey)
			}
			if dial != tc.wantDial {
				t.Errorf("dial = %q, want %q", dial, tc.wantDial)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want Relation
	}{
		{name: "identical wildcard spellings reuse", a: ":80", b: "0.0.0.0:80", want: Same},
		{name: "identical wildcard spellings reuse ipv6 form", a: ":80", b: "[::]:80", want: Same},
		{name: "identical specific address reuses", a: "127.0.0.1:8080", b: "127.0.0.1:8080", want: Same},
		{name: "wildcard vs specific same port conflicts", a: "127.0.0.1:8080", b: ":8080", want: Conflicting},
		{name: "wildcard vs specific same port conflicts reversed", a: ":80", b: "10.0.0.5:80", want: Conflicting},
		{name: "different specific hosts same port are distinct", a: "127.0.0.1:8080", b: "10.0.0.5:8080", want: Distinct},
		{name: "different ports are distinct", a: "127.0.0.1:8080", b: "127.0.0.1:8081", want: Distinct},
		{name: "different wildcard ports are distinct", a: ":80", b: ":443", want: Distinct},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Classify(tc.a, tc.b)
			if err != nil {
				t.Fatalf("Classify(%q, %q): %v", tc.a, tc.b, err)
			}
			if got != tc.want {
				t.Errorf("Classify(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestClassifyRejectsInvalidAddresses(t *testing.T) {
	if _, err := Classify("not-an-address", ":80"); err == nil {
		t.Fatal("expected an error for an invalid first address")
	}
	if _, err := Classify(":80", "not-an-address"); err == nil {
		t.Fatal("expected an error for an invalid second address")
	}
}
