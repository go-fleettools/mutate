module github.com/go-fleettools/mutate

// ⛔ The PATCH matters, and `go 1.27` does not name one. govulncheck judges the
// standard library by the version the module claims, so with `go 1.27` it
// answered for 1.27.1 and reported nine advisories whose fix is RELEASED in
// 1.27.2. The rest of this fleet says 1.27.2; this module said less. And the
// CI lane reads this very line through `go-version-file: go.mod`, so saying
// less here also meant a lane running an older toolchain than anybody
// intended.
//
// Raised to 1.27.3 on 2026-10-25 for GO-2026-6809 — crypto/x509, where
// compromised intermediate certificates are no longer refused by
// Certificate.Verify — fix RELEASED in 1.27.3. Still within the same minor.
go 1.27.3
