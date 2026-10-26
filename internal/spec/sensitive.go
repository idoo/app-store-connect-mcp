package spec

import (
	"net/http"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// rule classifies non-GET operations whose path template is Path (Exact) or
// starts with Path at a segment boundary. Methods empty means every non-GET
// method.
type rule struct {
	Methods []string
	Path    string
	Exact   bool
	Class   tool.Class
}

// destructiveRoots are resources where every mutation deletes, submits for
// review, publishes, changes money/availability, or affects people or
// external endpoints. All their non-GET operations are Destructive.
var destructiveRoots = []string{
	// Review and release: makes a version public or moves it through review.
	"/v1/reviewSubmissions", "/v1/reviewSubmissionItems",
	"/v1/appStoreVersionReleaseRequests", "/v1/appStoreVersionPhasedReleases",
	"/v1/appStoreVersionPromotions", "/v1/betaAppReviewSubmissions",
	"/v1/inAppPurchaseSubmissions", "/v1/subscriptionSubmissions",
	"/v1/subscriptionGroupSubmissions",
	// Public text.
	"/v1/customerReviewResponses",
	// People and access.
	"/v1/userInvitations", "/v1/users",
	// External endpoints receiving data.
	"/v1/webhooks", "/v1/marketplaceWebhooks",
	// Money and availability.
	"/v1/appPriceSchedules", "/v1/inAppPurchasePriceSchedules", "/v1/subscriptionPrices",
	"/v1/territoryAvailabilities", "/v2/appAvailabilities", "/v1/endAppAvailabilityPreOrders",
	"/v1/inAppPurchaseAvailabilities", "/v1/subscriptionAvailabilities",
	"/v1/subscriptionPlanAvailabilities", "/v1/endUserLicenseAgreements",
	// Game Center: publishing to live players and editing live player data.
	"/v1/gameCenterAchievementReleases", "/v1/gameCenterActivityVersionReleases",
	"/v1/gameCenterChallengeVersionReleases", "/v1/gameCenterLeaderboardReleases",
	"/v1/gameCenterLeaderboardSetReleases", "/v1/gameCenterLeaderboardEntrySubmissions",
	"/v1/gameCenterPlayerAchievementSubmissions", "/v1/gameCenterScoreModerations",
	// Irreversible sandbox reset.
	"/v2/sandboxTestersClearPurchaseHistoryRequest",
	// Signing identity and distribution keys: credentials that sign or ship builds.
	"/v1/certificates", "/v1/alternativeDistributionKeys",
	// Testers: emails real people and grants access to unreleased builds.
	"/v1/betaTesters", "/v1/betaTesterInvitations",
	// Offers and codes: money (free or discounted access) handed out.
	"/v1/inAppPurchaseOfferCodes", "/v1/inAppPurchaseOfferCodeCustomCodes", "/v1/inAppPurchaseOfferCodeOneTimeUseCodes",
	"/v1/subscriptionOfferCodes", "/v1/subscriptionOfferCodeCustomCodes", "/v1/subscriptionOfferCodeOneTimeUseCodes",
	"/v1/subscriptionIntroductoryOffers", "/v1/subscriptionPromotionalOffers", "/v1/winBackOffers",
}

// writeRoots are resources whose POST/PATCH operations (including their
// relationship endpoints) create or update state without deleting or
// publishing. A resource missing from both lists is unclassified, which
// fails generation and tests so new Apple endpoints get a deliberate class.
var writeRoots = []string{
	"/v1/accessibilityDeclarations", "/v1/ageRatingDeclarations", "/v1/alternativeDistributionDomains",
	"/v1/alternativeDistributionPackages", "/v1/analyticsReportRequests",
	"/v1/androidToIosAppMappingDetails", "/v1/appAssetLibraryImages", "/v1/appAssetLibraryPlacementOrderingRequests",
	"/v1/appAssetLibraryPlacements", "/v1/appAssetLibraryVideos", "/v1/appClipAdvancedExperienceImages",
	"/v1/appClipAdvancedExperiences", "/v1/appClipAppStoreReviewDetails", "/v1/appClipDefaultExperienceLocalizations",
	"/v1/appClipDefaultExperiences", "/v1/appClipHeaderImages", "/v1/appCustomProductPageLocalizations",
	"/v1/appCustomProductPages", "/v1/appCustomProductPageVersions", "/v1/appEncryptionDeclarationDocuments",
	"/v1/appEncryptionDeclarations", "/v1/appEventLocalizations", "/v1/appEvents",
	"/v1/appEventScreenshots", "/v1/appEventVideoClips", "/v1/appInfoLocalizations",
	"/v1/appInfos", "/v1/appPreviews", "/v1/appPreviewSets",
	"/v1/apps", "/v1/appScreenshots", "/v1/appScreenshotSets",
	"/v1/appStoreReviewAttachments", "/v1/appStoreReviewDetails", "/v1/appStoreVersionExperiments",
	"/v1/appStoreVersionExperimentTreatmentLocalizations", "/v1/appStoreVersionExperimentTreatments",
	"/v1/appStoreVersionLocalizations", "/v1/appStoreVersions", "/v1/appTags",
	"/v1/backgroundAssets", "/v1/backgroundAssetUploadFiles", "/v1/backgroundAssetVersions",
	"/v1/betaAppClipInvocationLocalizations", "/v1/betaAppClipInvocations", "/v1/betaAppLocalizations",
	"/v1/betaAppReviewDetails", "/v1/betaBuildLocalizations", "/v1/betaGroups",
	"/v1/betaLicenseAgreements", "/v1/betaRecruitmentCriteria",
	"/v1/buildBetaDetails", "/v1/buildBetaNotifications",
	"/v1/builds", "/v1/buildUploadFiles", "/v1/buildUploads",
	"/v1/bundleIdCapabilities", "/v1/bundleIds",
	"/v1/ciBuildRuns", "/v1/ciWorkflows", "/v1/devices",
	"/v1/gameCenterAchievementImages", "/v1/gameCenterAchievementLocalizations", "/v1/gameCenterAchievements",
	"/v1/gameCenterActivities", "/v1/gameCenterActivityImages", "/v1/gameCenterActivityLocalizations",
	"/v1/gameCenterActivityVersions", "/v1/gameCenterAppVersions", "/v1/gameCenterChallengeImages",
	"/v1/gameCenterChallengeLocalizations", "/v1/gameCenterChallenges", "/v1/gameCenterChallengeVersions",
	"/v1/gameCenterDetailPlayers", "/v1/gameCenterDetails", "/v1/gameCenterEnabledVersions",
	"/v1/gameCenterGroups", "/v1/gameCenterLeaderboardImages", "/v1/gameCenterLeaderboardLocalizations",
	"/v1/gameCenterLeaderboards", "/v1/gameCenterLeaderboardSetImages", "/v1/gameCenterLeaderboardSetLocalizations",
	"/v1/gameCenterLeaderboardSetMemberLocalizations", "/v1/gameCenterLeaderboardSets", "/v1/gameCenterMatchmakingQueues",
	"/v1/gameCenterMatchmakingRules", "/v1/gameCenterMatchmakingRuleSets", "/v1/gameCenterMatchmakingRuleSetTests",
	"/v1/gameCenterMatchmakingTeams", "/v1/inAppPurchaseAppStoreReviewScreenshots", "/v1/inAppPurchaseImages",
	"/v1/inAppPurchaseLocalizations",
	"/v1/inAppPurchaseVersions", "/v1/marketplaceSearchDetails",
	"/v1/merchantIds", "/v1/nominations", "/v1/passTypeIds",
	"/v1/profiles", "/v1/promotedPurchases", "/v1/routingAppCoverages",
	"/v1/subscriptionAppStoreReviewScreenshots", "/v1/subscriptionGracePeriods", "/v1/subscriptionGroupLocalizations",
	"/v1/subscriptionGroups", "/v1/subscriptionGroupVersions", "/v1/subscriptionImages",
	"/v1/subscriptionLocalizations",
	"/v1/subscriptions", "/v1/subscriptionVersions", "/v1/webhookDeliveries",
	"/v1/webhookPings", "/v2/appStoreVersionExperiments",
	"/v2/gameCenterAchievementImages", "/v2/gameCenterAchievementLocalizations", "/v2/gameCenterAchievements",
	"/v2/gameCenterAchievementVersions", "/v2/gameCenterLeaderboardImages", "/v2/gameCenterLeaderboardLocalizations",
	"/v2/gameCenterLeaderboards", "/v2/gameCenterLeaderboardSetImages", "/v2/gameCenterLeaderboardSetLocalizations",
	"/v2/gameCenterLeaderboardSets", "/v2/gameCenterLeaderboardSetVersions", "/v2/gameCenterLeaderboardVersions",
	"/v2/inAppPurchaseImages", "/v2/inAppPurchaseLocalizations", "/v2/inAppPurchases",
	"/v2/sandboxTesters", "/v2/subscriptionGroupLocalizations", "/v2/subscriptionImages",
	"/v2/subscriptionLocalizations",
}

// rules is evaluated in order; the first match wins.
var rules = buildRules()

func buildRules() []rule {
	rs := []rule{
		// Every DELETE removes something.
		{Methods: []string{http.MethodDelete}, Path: "/", Class: tool.Destructive},
		// builds update is how builds are expired.
		{Methods: []string{http.MethodPatch}, Path: "/v1/builds/{id}", Exact: true, Class: tool.Destructive},
		// PATCH /v1/certificates/{id} deactivates a certificate (covered by its destructive root).
	}
	for _, p := range destructiveRoots {
		rs = append(rs, rule{Path: p, Class: tool.Destructive})
	}
	for _, p := range writeRoots {
		rs = append(rs, rule{Methods: []string{http.MethodPost, http.MethodPatch}, Path: p, Class: tool.Write})
	}
	return rs
}

func (r rule) matches(method, template string) bool {
	if len(r.Methods) > 0 && !containsFold(r.Methods, method) {
		return false
	}
	if r.Exact {
		return template == r.Path
	}
	if r.Path == "/" {
		return true
	}
	return template == r.Path || strings.HasPrefix(template, r.Path+"/")
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// Classify returns the safety class of method on a spec path template
// (e.g. "/v1/builds/{id}"). GET and HEAD are Read. For other methods ok is
// false when no hand-kept rule covers the operation; callers must then treat
// it as Destructive.
func Classify(method, template string) (class tool.Class, ok bool) {
	m := strings.ToUpper(method)
	if m == http.MethodGet || m == http.MethodHead {
		return tool.Read, true
	}
	for _, r := range rules {
		if r.matches(m, template) {
			return r.Class, true
		}
	}
	return 0, false
}

// Class resolves a concrete method and path to its safety class. Non-GET
// requests that match no spec operation, or no rule, are Destructive (fail
// closed).
func (x *Index) Class(method, path string) tool.Class {
	m := strings.ToUpper(method)
	if m == http.MethodGet || m == http.MethodHead {
		return tool.Read
	}
	op, found := x.match(m, path)
	if !found {
		return tool.Destructive
	}
	c, ok := Classify(m, op.Path)
	if !ok {
		return tool.Destructive
	}
	return c
}

// Sensitive reports whether method+path must be confirmed as a destructive
// or public action. It implements [tool.Spec].
func (x *Index) Sensitive(method, path string) bool {
	return x.Class(method, path) == tool.Destructive
}
