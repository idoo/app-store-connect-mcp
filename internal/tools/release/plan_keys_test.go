package release

import "testing"

// Apple's list order of ready products never changes the submit fingerprint
// (RB-3): a reordered page between preview and confirm is not a stale plan.
func TestSubmitFingerprintIgnoresReadyProductOrder(t *testing.T) {
	a := readyProducts(&statusSnapshot{ReadyIAPs: []string{"i2", "i1"}, ReadySubscriptions: []string{"s2", "s1"}})
	b := readyProducts(&statusSnapshot{ReadyIAPs: []string{"i1", "i2"}, ReadySubscriptions: []string{"s1", "s2"}})
	fa, err := submitFingerprint(releaseState{}, a)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := submitFingerprint(releaseState{}, b)
	if err != nil {
		t.Fatal(err)
	}
	if fa != fb {
		t.Fatalf("fingerprints differ by order: %v vs %v", a, b)
	}
}
