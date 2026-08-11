// Package oss provides private-bucket object operations for servercli.
//
// The V1 security boundary assumes the OSS bucket is private and that the
// access key is loaded from a root-owned 0600 file or equivalent secret store.
// V1 does not provide client-side encryption; callers must treat provider-side
// encryption and access controls as part of the deployment boundary. Secrets
// are used only to sign requests and must never be logged or serialized.
package oss
