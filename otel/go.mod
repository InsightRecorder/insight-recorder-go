// The OpenTelemetry adapter is a separate module so the core SDK stays
// dependency-free: an application that does not use OpenTelemetry never pulls
// it into the build. Same arrangement as the zerolog adapter.
module github.com/InsightRecorder/insight-recorder-go/otel

go 1.22

require go.opentelemetry.io/otel/trace v1.28.0

require go.opentelemetry.io/otel v1.28.0 // indirect

replace github.com/InsightRecorder/insight-recorder-go => ../
