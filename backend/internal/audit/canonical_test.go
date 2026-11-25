package audit

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCanonicalNumber(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "0", want: "0"},
		{in: "-0", want: "0"},
		{in: "0.000", want: "0"},
		{in: "100", want: "100"},
		{in: "1e2", want: "100"},
		{in: "1E+2", want: "100"},
		{in: "1.0e0", want: "1"},
		{in: "2.50", want: "2.5"},
		{in: "-2.50", want: "-2.5"},
		{in: "1e-2", want: "0.01"},
		{in: "0.00120", want: "0.0012"},
		{in: "12.5e1", want: "125"},
		{in: "007", want: "7"},
		{in: "9007199254740993", want: "9007199254740993"},
		{in: "123456789012345678901234567890.000000000000000001", want: "123456789012345678901234567890.000000000000000001"},
		{in: "1e9999999", wantErr: true},
		{in: "1x", wantErr: true},
		{in: ".5", wantErr: true},
	}
	for _, tt := range tests {
		got, err := CanonicalNumber(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("CanonicalNumber(%q) = %q, %v; want %q (err %v)", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestBigIntegersAreTamperEvident(t *testing.T) {
	a, b := sampleEvent(1), sampleEvent(1)
	a.Payload = json.RawMessage(`{"amount": 9007199254740993}`)
	b.Payload = json.RawMessage(`{"amount": 9007199254740992}`) // equal as float64
	ha, err := ChainHash(GenesisHash, a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := ChainHash(GenesisHash, b)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(ha, hb) {
		t.Fatal("payloads differing beyond float64 precision must hash differently")
	}
}

func TestActorIPIsHashed(t *testing.T) {
	a, b := sampleEvent(1), sampleEvent(1)
	b.ActorIP = "203.0.113.7"
	ha, _ := ChainHash(GenesisHash, a)
	hb, err := ChainHash(GenesisHash, b)
	if err != nil || bytes.Equal(ha, hb) {
		t.Fatalf("actor_ip must be part of the hash (err %v)", err)
	}
	mapped := b
	mapped.ActorIP = "::ffff:203.0.113.7"
	hm, err := ChainHash(GenesisHash, mapped)
	if err != nil || !bytes.Equal(hb, hm) {
		t.Fatal("IPv4-mapped addresses must canonicalise to the IPv4 form")
	}
	bad := b
	bad.ActorIP = "not-an-ip"
	if _, err := ChainHash(GenesisHash, bad); err == nil {
		t.Fatal("invalid actor ip accepted")
	}
}

func TestCanonicalPayload(t *testing.T) {
	got, err := CanonicalPayload(json.RawMessage(`{"b":[1.50,{"z":1e1,"a":null}],"a":"<x>"}`))
	if err != nil {
		t.Fatal(err)
	}
	// encoding/json escapes <, > and & as < etc.; the escaping is part of the canonical form.
	if want := "{\"a\":\"\\u003cx\\u003e\",\"b\":[1.5,{\"a\":null,\"z\":10}]}"; string(got) != want {
		t.Fatalf("CanonicalPayload = %s, want %s", got, want)
	}
	if got, _ := CanonicalPayload(nil); string(got) != "{}" {
		t.Fatalf("empty payload = %s", got)
	}
	if _, err := CanonicalPayload(json.RawMessage(`{} {}`)); err == nil {
		t.Fatal("trailing data accepted")
	}
}
