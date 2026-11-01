package env

// Semver of the verification-envelope contract. MAJOR = breaking field
// change, MINOR = additive only. The committed schema file carries the
// same version (schema/kubox-envelope-<v>.json).
//
// 0.1.0: initial contract — Report {status, environment, checks} with
//
//	check types load, drill:*, diagnosis.
const SchemaVersion = "0.1.0"
