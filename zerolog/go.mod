// The zerolog adapter is a separate module so the core SDK stays
// dependency-free: applications that use slog (or no logger at all) never pull
// zerolog into their build.
module github.com/InsightRecorder/insight-recorder-go/zerolog

go 1.22

require (
	github.com/InsightRecorder/insight-recorder-go v0.0.0
	github.com/rs/zerolog v1.34.0
)

require (
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.19 // indirect
	golang.org/x/sys v0.12.0 // indirect
)

replace github.com/InsightRecorder/insight-recorder-go => ../
