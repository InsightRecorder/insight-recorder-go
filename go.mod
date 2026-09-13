module github.com/InsightRecorder/insight-recorder-go

// The SDK targets a conservative Go floor so customer applications are not
// forced onto the server's toolchain. 1.22 is the oldest release with the
// log/slog API this package integrates with.
go 1.22
