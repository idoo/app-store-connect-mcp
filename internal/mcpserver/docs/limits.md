# App Store Connect limits

## Characters per locale
| Field | Max |
|---|---|
| App name | 30 |
| Subtitle | 30 |
| Promotional text | 170 |
| Keywords (comma-separated, counted in bytes) | 100 |
| Description | 4000 |
| What's New | 4000 |
| TestFlight What to Test | 4000 |
| Review reply | 5970 |

## Screenshots
- iPhone: 6.9" (1320×2868 or 1290×2796) or 6.5" (1284×2778 or 1242×2688). Smaller sizes scale down from these.
- iPad, if the app supports it: 13" (2064×2752 or 2048×2732).
- Mac: 16:10 at 1280×800, 1440×900, 2560×1600 or 2880×1800.
- 1–10 per set. PNG or JPEG, no alpha channel.

## Web only (the API can't)
- Create a new app record: Apps → +, or apps_create in the web toolset after the user signs in (web_login opens a browser window; or `app-store-connect-mcp web login` in a terminal).
- App Privacy labels: App → App Privacy, or privacy_get / privacy_plan / privacy_apply in the web toolset (same sign-in).
- Dashboard analytics like impressions and conversion rate. Use analytics_setup and analytics_report instead.
- Agreements, tax and banking.

## Waiting
- Builds take 5–60 min to become VALID.
- Analytics reports show up 24–48 h after the first request.
- Daily sales usually land the next day after about 08:00 Pacific.
- About 3600 API requests per hour per key.
