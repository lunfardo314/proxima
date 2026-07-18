package seqdata

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// A build that predates a key must not erase it. The sequencer updates its data
// by parsing the current output, changing one field and writing it back, so an
// unrecognised key has to survive that round trip verbatim.
func TestUnknownKeysSurviveUpdate(t *testing.T) {
	original := []byte(`{"name":"oseq1","future_flag":true,"future_obj":{"a":1,"b":[2,3]}}`)

	sd, err := FromBytes(original)
	require.NoError(t, err)
	require.Equal(t, "oseq1", sd.Name())

	// update a known field, exactly as `proxi node seq set-params` does
	updated := sd.Clone(func(u *SequencerData) { u.SetMinimumFee(500) })

	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(updated.Bytes(), &m))

	require.JSONEq(t, `"oseq1"`, string(m["name"]))
	require.JSONEq(t, `500`, string(m["fee"]))
	require.JSONEq(t, `true`, string(m["future_flag"]), "unknown scalar must survive")
	require.JSONEq(t, `{"a":1,"b":[2,3]}`, string(m["future_obj"]), "unknown object must survive verbatim")
}

// Clone must deep-copy the unknown keys, otherwise mutating a clone would reach
// back into the original's map.
func TestCloneDoesNotShareUnknownKeys(t *testing.T) {
	sd, err := FromBytes([]byte(`{"name":"a","future":1}`))
	require.NoError(t, err)

	clone := sd.Clone()
	require.NotNil(t, clone.extra)
	clone.extra["future"] = json.RawMessage(`999`)

	require.JSONEq(t, `1`, string(sd.extra["future"]), "original must be untouched")
}

// A known key always wins over a same-named leftover, so a stale unknown entry
// can never shadow a field this build owns.
func TestKnownKeyWinsOverExtra(t *testing.T) {
	sd, err := FromBytes([]byte(`{"fee":10}`))
	require.NoError(t, err)
	require.EqualValues(t, 10, sd.MinimumFee())

	// inject a colliding leftover directly; serialization must ignore it
	sd.extra = map[string]json.RawMessage{"fee": json.RawMessage(`77`)}

	back, err := FromBytes(sd.Bytes())
	require.NoError(t, err)
	require.EqualValues(t, 10, back.MinimumFee())
}

// These bytes live in a UTXO, so the same logical value must always serialize
// identically regardless of whether unknown keys are present.
func TestSerializationIsDeterministic(t *testing.T) {
	for _, src := range []string{
		`{"name":"x","fee":1,"greedy":true}`,
		`{"future_b":2,"name":"x","future_a":1,"fee":1,"greedy":true}`,
	} {
		sd, err := FromBytes([]byte(src))
		require.NoError(t, err)
		first := sd.Bytes()
		for i := 0; i < 20; i++ {
			require.Equal(t, string(first), string(sd.Bytes()), "unstable for %s", src)
		}
		// and stable across a full decode/encode cycle
		back, err := FromBytes(first)
		require.NoError(t, err)
		require.Equal(t, string(first), string(back.Bytes()))
	}
}

// Absent freeze_bounds means "not enforced" — the flag is opt-in.
func TestFreezeBoundsAreOptIn(t *testing.T) {
	sd, err := FromBytes([]byte(`{"name":"x"}`))
	require.NoError(t, err)
	require.False(t, sd.IsFreezeBoundsEnforced())

	decode := func(b []byte) map[string]json.RawMessage {
		// a fresh map each time: unmarshalling into an existing one merges
		// keys rather than replacing them
		m := map[string]json.RawMessage{}
		require.NoError(t, json.Unmarshal(b, &m))
		return m
	}

	sd.SetEnforceFreezeBounds(true)
	require.JSONEq(t, `true`, string(decode(sd.Bytes())["freeze_bounds"]))

	// false is the default and is omitted again
	sd.SetEnforceFreezeBounds(false)
	require.NotContains(t, decode(sd.Bytes()), "freeze_bounds")
}
