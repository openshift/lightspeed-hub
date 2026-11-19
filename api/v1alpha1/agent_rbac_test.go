package v1alpha1

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFleetRolePresenceRoundTrip(t *testing.T) {
	for _, input := range []string{`{"spec":{"clusterRegistryMode":"secret"}}`, `{"spec":{"clusterRegistryMode":"secret","agentRBAC":{}}}`, `{"spec":{"clusterRegistryMode":"secret","agentRBAC":{"defaultClusterRoles":[]}}}`, `{"spec":{"clusterRegistryMode":"secret","agentRBAC":{"defaultClusterRoles":["custom:reader","custom:reader"]}}}`} {
		t.Run(input, func(t *testing.T) {
			var original HubConfig
			if err := json.Unmarshal([]byte(input), &original); err != nil {
				t.Fatal(err)
			}
			copy := original.DeepCopy()
			if !reflect.DeepEqual(original, *copy) {
				t.Fatal("DeepCopy changed role presence")
			}
			data, err := json.Marshal(copy)
			if err != nil {
				t.Fatal(err)
			}
			var roundtrip HubConfig
			if err := json.Unmarshal(data, &roundtrip); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(original, roundtrip) {
				t.Fatalf("roundtrip changed role presence: %s", data)
			}
			if copy.Spec.AgentRBAC != nil && copy.Spec.AgentRBAC.DefaultClusterRoles != nil && len(*copy.Spec.AgentRBAC.DefaultClusterRoles) > 0 {
				(*copy.Spec.AgentRBAC.DefaultClusterRoles)[0] = "changed"
				if (*original.Spec.AgentRBAC.DefaultClusterRoles)[0] == "changed" {
					t.Fatal("DeepCopy aliases default roles")
				}
			}
		})
	}
}
