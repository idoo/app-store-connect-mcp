// Package upload implements App Store Connect uploads: the asset flow
// (screenshots, previews, review screenshots, IAP/subscription images) and
// the BuildUpload flow for IPA/PKG files with resumable parts. Parts are sent
// through the unauthenticated [tool.Fetcher] under the URL policy; it also
// validates screenshot images and reads IPA Info.plist files.
package upload
