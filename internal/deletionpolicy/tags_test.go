package deletionpolicy

import (
	"reflect"
	"testing"
)

func TestDeliveryTagsAddOwnershipWithoutMutatingInput(t *testing.T) {
	rule := []string{"brush", "vertex-policy-abc"}
	got := DeliveryTags(rule, "hdsky")
	if !reflect.DeepEqual(got, []string{"brush", "vertex-policy-abc", ManagedTag, "vertex-site-hdsky"}) {
		t.Fatalf("tags = %v", got)
	}
	if len(rule) != 2 {
		t.Fatal("input tags were mutated")
	}
	// A site that cannot be carried in a tag stays unattributed, which keeps
	// the torrent out of every deletion policy.
	for _, site := range []string{"", "a,b", "bad site", "../x"} {
		if got := DeliveryTags(nil, site); !reflect.DeepEqual(got, []string{ManagedTag}) {
			t.Fatalf("site %q tags = %v", site, got)
		}
	}
}
