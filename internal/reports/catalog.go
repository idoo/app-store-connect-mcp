package reports

// Analytics report names and columns used by the funnel. The names and
// column labels are inferred from Apple's documentation [INFERENCE]; a name
// Apple does not list makes [LoadAnalytics] return a
// [*ReportUnavailableError] carrying the names it does list, so a mismatch
// costs one listing, never a wrong number.
const (
	// ReportDiscovery holds impressions and product page views.
	ReportDiscovery = "App Store Discovery and Engagement Detailed"
	// ReportDownloads holds first-time downloads and redownloads by source.
	ReportDownloads = "App Downloads Detailed"
	// ReportInstalls holds installations and deletions.
	ReportInstalls = "App Store Installation and Deletion Detailed"
	// ReportSessions holds sessions and active devices.
	ReportSessions = "App Sessions Detailed"
	// ReportCrashes holds crash counts.
	ReportCrashes = "App Crashes Expanded"
)

// Analytics segment columns.
const (
	ColDate          = "Date"
	ColEvent         = "Event"
	ColCounts        = "Counts"
	ColSourceType    = "Source Type"
	ColTerritory     = "Territory"
	ColDevice        = "Device"
	ColSessions      = "Sessions"
	ColUniqueDevices = "Unique Devices"
	ColCrashes       = "Crashes"
)

// Event values in the discovery and installation reports.
const (
	EventImpression = "Impression"
	EventPageView   = "Page view"
	EventInstall    = "Install"
	EventDelete     = "Delete"
)

// BreakdownColumns maps a funnel breakdown name to its segment column.
var BreakdownColumns = map[string]string{
	"source":    ColSourceType,
	"territory": ColTerritory,
	"device":    ColDevice,
}

// AnalyticsMaxSegments is the most segments one funnel call downloads.
const AnalyticsMaxSegments = 60
