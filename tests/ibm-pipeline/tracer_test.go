package deadlockrepro

import (
	"os"
	"testing"

	"github.com/DataDog/dd-trace-go/v2/ddtrace/tracer"
)

func startTracer(t *testing.T) {
	t.Helper()
	if os.Getenv("DD_TRACE_AGENT_URL") == "" {
		t.Setenv("DD_TRACE_AGENT_URL", "http://127.0.0.1:9529")
	}
	t.Setenv("DD_SERVICE", "sarama-ibm-product-listener-repro")
	t.Setenv("DD_ENV", "local-repro")
	tracer.Start(tracer.WithLogStartup(false))
	t.Cleanup(tracer.Stop)
}
