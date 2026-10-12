package terminal

import (
	"encoding/json"
	"testing"
	"unicode/utf8"
)

func FuzzParseSessionSet_roundtrip(f *testing.F) {
	f.Add([]byte(`{"version":1,"sessions":[{"alias":"sess_a","pinnedTitle":"fix build","resume":"r"},{"alias":"k3q7m2xa"}],` +
		`"layout":{"left":0,"right":1,"selected":"right","handle":0.5,"open":true}}`))
	f.Add([]byte(`{"version":1,"sessions":[],"layout":{"left":null,"right":null,"selected":"left","handle":0.5,"open":false}}`))
	f.Add([]byte(`{"version":1,"sessions":[],"layout":{"selected":"left","handle":0.5}}{"version":1}`))
	f.Add([]byte(`{"version":1,"sessions":[{"alias":"a","resume":"NaN"}],"layout":{"left":0,"selected":"left","handle":NaN}}`))
	f.Add([]byte("{\"version\":1,\"sessions\":[{\"alias\":\"a\",\"resume\":\"r\xffr\"}],\"layout\":{\"left\":0,\"selected\":\"left\",\"handle\":0.5}}"))
	f.Fuzz(func(t *testing.T, data []byte) {
		set, err := ParseSessionSet(data)
		if err != nil {
			return
		}
		if !json.Valid(data) {
			t.Fatalf("ParseSessionSet accepted %q, which is not one JSON value", data)
		}
		if !utf8.Valid(data) {
			t.Fatalf("ParseSessionSet accepted %q, which is not valid UTF-8", data)
		}
		again, err := json.Marshal(set)
		if err != nil {
			t.Fatalf("json.Marshal(parsed) = %v", err)
		}
		round, err := ParseSessionSet(again)
		if err != nil {
			t.Fatalf("ParseSessionSet(re-marshalled %s) = %v, want nil", again, err)
		}
		if !sameSessionSet(round, set) {
			t.Fatalf("round trip = %+v, want %+v", round, set)
		}
	})
}
