package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	saramatrace "github.com/DataDog/dd-trace-go/contrib/Shopify/sarama/v2"
	"github.com/DataDog/dd-trace-go/v2/ddtrace/mocktracer"
	"github.com/DataDog/dd-trace-go/v2/ddtrace/tracer"
	"github.com/Shopify/sarama"
	"google.golang.org/grpc"
	v1grpc "gopkg.in/DataDog/dd-trace-go.v1/contrib/google.golang.org/grpc"
	v1mux "gopkg.in/DataDog/dd-trace-go.v1/contrib/gorilla/mux"
	v1tracer "gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
)

func check(ctx context.Context) {
	a, ok := v1tracer.SpanFromContext(ctx)
	if !ok {
		panic("v1 span missing")
	}
	b, ok := tracer.SpanFromContext(ctx)
	if !ok || a.Context().SpanID() != b.Context().SpanID() {
		panic("v1/v2 context mismatch")
	}
	child, _ := tracer.StartSpanFromContext(ctx, "smoke.business")
	child.Finish()
}

func main() {
	mt := mocktracer.Start()
	defer mt.Stop()
	router := v1mux.NewRouter()
	router.HandleFunc("/marketing", func(w http.ResponseWriter, r *http.Request) { check(r.Context()) })
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/marketing", nil))
	if w.Code != 200 {
		panic("HTTP failed")
	}
	_, err := v1grpc.UnaryServerInterceptor()(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/marketing/List"},
		func(ctx context.Context, _ interface{}) (interface{}, error) { check(ctx); return nil, nil })
	if err != nil {
		panic(err)
	}
	cfg := sarama.NewConfig()
	cfg.Version = sarama.V2_8_0_0
	cfg.Producer.Return.Successes = true
	p, err := sarama.NewAsyncProducer([]string{"127.0.0.1:19092"}, cfg)
	if err != nil {
		panic(err)
	}
	parent, _ := v1tracer.StartSpanFromContext(context.Background(), "smoke.kafka.parent")
	msg := &sarama.ProducerMessage{Topic: "inventory-item-delete", Value: sarama.StringEncoder("official-smoke")}
	if err := v1tracer.Inject(parent.Context(), saramatrace.NewProducerMessageCarrier(msg)); err != nil {
		panic(err)
	}
	select {
	case p.Input() <- msg:
	case <-time.After(10 * time.Second):
		panic("input blocked")
	}
	select {
	case <-p.Successes():
	case err := <-p.Errors():
		panic(err)
	case <-time.After(10 * time.Second):
		panic("produce blocked")
	}
	p.AsyncClose()
	s, e := p.Successes(), p.Errors()
	for s != nil || e != nil {
		select {
		case _, ok := <-s:
			if !ok {
				s = nil
			}
		case _, ok := <-e:
			if !ok {
				e = nil
			}
		case <-time.After(10 * time.Second):
			panic("shutdown blocked")
		}
	}
	parentID := parent.Context().SpanID()
	parent.Finish()
	found := false
	for _, span := range mt.FinishedSpans() {
		if span.OperationName() == "kafka.produce" && span.ParentID() == parentID {
			found = true
		}
	}
	if !found {
		panic("automatic Kafka producer span missing")
	}
	fmt.Printf("SMOKE_OK manual_v1_http=ok manual_v1_grpc=ok automatic_kafka=ok spans=%d\n", len(mt.FinishedSpans()))
}
