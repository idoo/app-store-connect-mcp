# Common App Review rejections and how to fix them

Summaries are paraphrased. Always read the current guideline text at
https://developer.apple.com/app-store/review/guidelines/ before replying.

## 2.1 App completeness
Reviewers hit a crash, a placeholder, a broken link or could not sign in.
- Test the exact submitted build on the latest iOS on a real device.
- Provide a working demo account and notes for any hardware or region gating.
- Remove "coming soon" screens and placeholder text.
- Link: https://developer.apple.com/app-store/review/guidelines/#app-completeness

## 2.3 Accurate metadata
Screenshots, description or keywords do not match what the app does.
- Screenshots must show the app in use, not only marketing art.
- Do not mention other platforms, prices or competitor names in metadata.
- Link: https://developer.apple.com/app-store/review/guidelines/#accurate-metadata

## 2.3.10 Other platforms in metadata
Metadata or screenshots reference another mobile platform or its devices.
- Remove other platform names and device frames from text and images.
- Link: https://developer.apple.com/app-store/review/guidelines/#accurate-metadata

## 3.1.1 In-app purchase
Digital content or features are unlocked without Apple in-app purchase.
- Sell digital goods with StoreKit; remove external purchase buttons unless an entitlement allows them.
- Include a restore purchases control.
- Attach new IAPs/subscriptions to the submission.
- Link: https://developer.apple.com/app-store/review/guidelines/#in-app-purchase

## 4.0 Design
The interface is broken, hard to use or does not follow platform conventions.
- Check layouts on all supported devices, including iPad when enabled.
- Link: https://developer.apple.com/app-store/review/guidelines/#design

## 4.2 Minimum functionality
The app is too thin, a repackaged website or mostly static content.
- Add native features that justify an app; explain them in the review notes.
- Link: https://developer.apple.com/app-store/review/guidelines/#minimum-functionality

## 4.3 Spam
The app duplicates another app (yours or others) or a saturated template.
- Merge similar apps, or explain what is unique in the review notes.
- Link: https://developer.apple.com/app-store/review/guidelines/#spam

## 5.1.1 Data collection and storage
Privacy policy, consent or account deletion issues.
- Link a privacy policy in metadata and in the app.
- Request only needed permissions with clear purpose strings.
- Offer in-app account deletion when the app supports account creation.
- Link: https://developer.apple.com/app-store/review/guidelines/#data-collection-and-storage

## 5.1.2 Data use and sharing
Data is shared or tracked without permission.
- Use App Tracking Transparency before tracking; keep App Privacy labels accurate (web only).
- Link: https://developer.apple.com/app-store/review/guidelines/#data-use-and-sharing

## Replying
Use release_review_status to read what the API exposes, then answer in the
Resolution Center on the ASC web site (the API cannot reply there).
