# Common App Review rejections

Paraphrased. Check the current text at https://developer.apple.com/app-store/review/guidelines/ before you reply.

## 2.1 App completeness
A crash, a placeholder, a dead link, or the reviewer couldn't sign in.
- Test the exact submitted build on the latest OS, on a real device.
- Give a working demo account, plus notes for any hardware or region gating.
- Remove "coming soon" screens and placeholder text.
- https://developer.apple.com/app-store/review/guidelines/#app-completeness

## 2.3 Accurate metadata
Screenshots, description or keywords don't match the app.
- Screenshots show the app in use, not just marketing art.
- No other platforms, prices or competitor names in metadata.
- https://developer.apple.com/app-store/review/guidelines/#accurate-metadata

## 2.3.10 Other platforms
Text or screenshots mention another mobile platform or its devices.
- Remove the names and device frames.
- https://developer.apple.com/app-store/review/guidelines/#accurate-metadata

## 3.1.1 In-app purchase
Digital content unlocked without Apple's in-app purchase.
- Sell digital goods with StoreKit. Drop external purchase buttons unless an entitlement allows them.
- Add a restore purchases control.
- Attach new IAPs and subscriptions to the submission.
- https://developer.apple.com/app-store/review/guidelines/#in-app-purchase

## 4.0 Design
Broken, hard to use, or ignores platform conventions.
- Check layouts on every supported device, iPad included when enabled.
- https://developer.apple.com/app-store/review/guidelines/#design

## 4.2 Minimum functionality
Too thin, a wrapped website, or mostly static content.
- Add native features worth an app and explain them in the review notes.
- https://developer.apple.com/app-store/review/guidelines/#minimum-functionality

## 4.3 Spam
Duplicates another app (yours or someone's) or a saturated template.
- Merge similar apps, or explain what's unique in the review notes.
- https://developer.apple.com/app-store/review/guidelines/#spam

## 5.1.1 Data collection and storage
Privacy policy, consent or account deletion problems.
- Link a privacy policy in the metadata and in the app.
- Ask only for permissions you need, with clear purpose strings.
- If users can create an account, they can delete it in the app.
- https://developer.apple.com/app-store/review/guidelines/#data-collection-and-storage

## 5.1.2 Data use and sharing
Tracking or sharing without permission.
- Use App Tracking Transparency before tracking. Keep App Privacy labels accurate (web only).
- https://developer.apple.com/app-store/review/guidelines/#data-use-and-sharing

## Replying
release_review_status shows what the API exposes. The reply itself goes in the Resolution Center on the ASC web site; the API can't post there.
