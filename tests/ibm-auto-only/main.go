// This fixture deliberately does not import the Sarama tracing integration.
// The only application import of that integration must come from weaving.
package main

import (
	"context"
	"fmt"

	"github.com/IBM/sarama"
	v1tracer "gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
)

func main() {
	_, _ = v1tracer.SpanFromContext(context.Background())
	// No brokers are supplied: exercise compilation without connecting to Kafka.
	p, err := sarama.NewAsyncProducer(nil, sarama.NewConfig())
	fmt.Printf("producer=%T err=%v\n", p, err)
	if p != nil {
		p.AsyncClose()
	}
}
