package privileged

import (
	"context"
	"strings"
	"testing"
)

func TestRequestCannotCarryRootCommandsOrMultipleTargets(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"shell","argv":["/bin/sh"]}`,
		`{"kind":"package","package":{},"environment":{"PATH":"/tmp"}}`,
		`{"kind":"system","system":{},"package":{}}`,
		`{"kind":"system","system":{}} {}`,
	} {
		if _, err := Decode(strings.NewReader(raw)); err == nil {
			t.Fatal("unsafe request accepted", raw)
		}
	}
}
func TestNativeClientCannotUseDevelopmentPathOrWrongConfirmation(t *testing.T) {
	r := Request{Kind: "restart", RestartOf: "example"}
	result, err := (NativeClient{}).Check(context.Background(), r)
	if err != nil || result.Status != "blocked" || result.Reason != "native_bridge_not_packaged" {
		t.Fatal(result, err)
	}
	if _, err := (NativeClient{}).Execute(context.Background(), r, "yes"); err == nil {
		t.Fatal("confirmation accepted")
	}
}

func TestMissingNativeResponseRetainsKnownOperationKey(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, data := range [][]byte{nil, []byte("partial JSON")} {
		result, err := decodeResponse(data, false, context.Canceled, id)
		if err == nil || result.PlanID != id || result.Status != "verification_unknown" {
			t.Fatal(result, err)
		}
	}
	result, err := decodeResponse([]byte(`{"planID":"different","status":"installed","reason":"ok","executed":true,"commandSucceeded":true}`), false, nil, id)
	if err == nil || result.PlanID != id || result.Status != "verification_unknown" {
		t.Fatal("wrong reply associated with operation", result, err)
	}
}
