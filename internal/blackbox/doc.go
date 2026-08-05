// Package blackbox contains a black-box API test suite that runs against a
// deployed Omnara control plane over the public HTTP API only.
//
// Unlike internal/e2e (which builds and runs the service binaries locally),
// this suite targets an already-running deployment such as a hosted release
// candidate, and exercises it exactly the way an external API client would:
// no database access, no internal packages, only documented endpoints.
//
// The suite is excluded from normal builds and tests by the "blackbox" build
// tag. Run it with:
//
//	OMNARA_BLACKBOX_API_URL=https://api.example.com \
//	OMNARA_BLACKBOX_TOKEN=omnara_pat_... \
//	make test-blackbox
package blackbox
