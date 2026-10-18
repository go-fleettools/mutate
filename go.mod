module github.com/go-fleettools/mutate

// ⛔ The PATCH matters, and `go 1.27` does not name one. govulncheck judges the
// standard library by the version the module claims, so with `go 1.27` it
// answered for 1.27.1 and reported nine advisories whose fix is RELEASED in
// 1.27.2. The rest of this fleet says 1.27.2; this module said less. And the
// CI lane reads this very line through `go-version-file: go.mod`, so saying
// less here also meant a lane running an older toolchain than anybody
// intended.
go 1.27.2
