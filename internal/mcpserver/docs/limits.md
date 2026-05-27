# App Store Connect limits

## Character limits (per locale)
| Field | Limit |
|---|---|
| App name | 30 |
| Subtitle | 30 |
| Promotional text | 170 |
| Keywords (comma separated, bytes) | 100 |
| Description | 4000 |
| What's New | 4000 |
| TestFlight What to Test | 4000 |
| Review reply | 5970 |

## Screenshots
- iPhone: provide 6.9" (1320×2868 or 1290×2796) or 6.5" (1284×2778 / 1242×2688); smaller sizes scale down.
- iPad (when the app supports iPad): 13" (2064×2752 or 2048×2732).
- Mac: 16:10 at 1280×800, 1440×900, 2560×1600 or 2880×1800.
- 1–10 screenshots per set, PNG or JPEG, no alpha channel.

## Not available through the API
- Creating a new app record (use the ASC web site: Apps → +).
- App Privacy nutrition labels (App → App Privacy).
- Web dashboard analytics such as impressions and conversion rate (use analytics_setup + analytics_report instead).
- Account holder agreements, tax and banking.

## Timing
- Builds take 5–60 min to process before they are VALID.
- Analytics report instances appear 24–48 h after the first request.
- Daily sales reports are usually available the next day after about 08:00 Pacific.
- The API allows about 3600 requests per hour per key.
