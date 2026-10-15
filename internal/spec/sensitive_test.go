package spec

import (
	"net/http"
	"testing"

	"github.com/idoo/hamn/internal/tool"
)

func TestEveryMutationIsClassified(t *testing.T) {
	x := load(t)
	for _, op := range x.Operations() {
		if op.Method == http.MethodGet {
			continue
		}
		if _, ok := Classify(op.Method, op.Path); !ok {
			t.Errorf("unclassified: %s %s (%s)", op.Method, op.Path, op.ID)
		}
	}
}

func TestEveryRuleMatchesAnOperation(t *testing.T) {
	x := load(t)
	ops := x.Operations()
	for _, r := range rules {
		found := false
		for _, op := range ops {
			if op.Method != http.MethodGet && r.matches(op.Method, op.Path) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("rule %v %s matches no operation in the index", r.Methods, r.Path)
		}
	}
}

func TestAllDeletesAreDestructive(t *testing.T) {
	x := load(t)
	for _, op := range x.Operations() {
		if op.Method == http.MethodDelete {
			if c, _ := Classify(op.Method, op.Path); c != tool.Destructive {
				t.Errorf("%s %s = %v, want D", op.Method, op.Path, c)
			}
		}
	}
}

func TestSensitive(t *testing.T) {
	x := load(t)
	tests := []struct {
		method, path string
		want         tool.Class
	}{
		{"GET", "/v1/apps", tool.Read},
		{"POST", "/v1/appStoreVersions", tool.Write},
		{"PATCH", "/v1/appStoreVersionLocalizations/1", tool.Write},
		{"PATCH", "/v1/builds/1", tool.Destructive},
		{"PATCH", "/v1/builds/1/relationships/appEncryptionDeclaration", tool.Write},
		{"POST", "/v1/builds/1/relationships/betaGroups", tool.Write},
		{"DELETE", "/v1/betaGroups/1/relationships/betaTesters", tool.Destructive},
		{"DELETE", "/v1/certificates/1", tool.Destructive},
		{"DELETE", "/v1/profiles/1", tool.Destructive},
		{"PATCH", "/v1/certificates/1", tool.Destructive},
		{"POST", "/v1/certificates", tool.Destructive},
		{"POST", "/v1/reviewSubmissions", tool.Destructive},
		{"PATCH", "/v1/reviewSubmissions/1", tool.Destructive},
		{"POST", "/v1/reviewSubmissionItems", tool.Destructive},
		{"POST", "/v1/appStoreVersionReleaseRequests", tool.Destructive},
		{"POST", "/v1/customerReviewResponses", tool.Destructive},
		{"PATCH", "/v1/appStoreVersionPhasedReleases/1", tool.Destructive},
		{"POST", "/v1/userInvitations", tool.Destructive},
		{"PATCH", "/v1/users/1", tool.Destructive},
		{"POST", "/v1/webhooks", tool.Destructive},
		{"PATCH", "/v1/webhooks/1", tool.Destructive},
		{"POST", "/v1/subscriptionPrices", tool.Destructive},
		{"POST", "/v1/appPriceSchedules", tool.Destructive},
		{"POST", "/v1/inAppPurchasePriceSchedules", tool.Destructive},
		{"PATCH", "/v1/territoryAvailabilities/1", tool.Destructive},
		{"POST", "/v2/appAvailabilities", tool.Destructive},
		{"PATCH", "/v1/endUserLicenseAgreements/1", tool.Destructive},
		{"POST", "/v1/betaTesters", tool.Destructive},
		{"POST", "/v1/betaTesterInvitations", tool.Destructive},
		{"POST", "/v1/subscriptionOfferCodes", tool.Destructive},
		{"POST", "/v1/subscriptionOfferCodeOneTimeUseCodes", tool.Destructive},
		{"POST", "/v1/winBackOffers", tool.Destructive},
		{"POST", "/v1/alternativeDistributionKeys", tool.Destructive},
		{"PATCH", "/v1/subscriptions/1", tool.Write},
		// Unknown non-GET paths fail closed.
		{"POST", "/v1/futureThings", tool.Destructive},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			if got := x.Class(tt.method, tt.path); got != tt.want {
				t.Errorf("Class = %v, want %v", got, tt.want)
			}
			if got := x.Sensitive(tt.method, tt.path); got != (tt.want == tool.Destructive) {
				t.Errorf("Sensitive = %v", got)
			}
		})
	}
}

func TestClassifyUnknownTemplate(t *testing.T) {
	if _, ok := Classify(http.MethodPost, "/v1/notARealResource"); ok {
		t.Error("unknown resource must be unclassified")
	}
	if c, ok := Classify("get", "/v1/anything"); !ok || c != tool.Read {
		t.Errorf("GET = %v,%v, want R", c, ok)
	}
}
