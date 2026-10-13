package compat

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	saramatrace "github.com/DataDog/dd-trace-go/contrib/Shopify/sarama/v2"
	"github.com/DataDog/dd-trace-go/v2/ddtrace/mocktracer"
	"github.com/DataDog/dd-trace-go/v2/ddtrace/tracer"
	"github.com/Shopify/sarama"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
	v1grpc "gopkg.in/DataDog/dd-trace-go.v1/contrib/google.golang.org/grpc"
	v1mux "gopkg.in/DataDog/dd-trace-go.v1/contrib/gorilla/mux"
	v1tracer "gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
)

// Exercise the customer's unchanged v1 imports under official automatic weaving.
func checkContexts(ctx context.Context) error {
	v1span, ok := v1tracer.SpanFromContext(ctx)
	if !ok {
		return fmt.Errorf("manual v1 span missing")
	}
	v2span, ok := tracer.SpanFromContext(ctx)
	if !ok || v2span.Context().SpanID() != v1span.Context().SpanID() {
		return fmt.Errorf("v1 and v2 do not share the active span")
	}
	// Moving the context into another goroutine exercises the woven GLS paths.
	done := make(chan error, 1)
	go func() {
		child, _ := tracer.StartSpanFromContext(ctx, "compat.business")
		defer child.Finish()
		if child.Context().TraceID() != v2span.Context().TraceID() {
			done <- fmt.Errorf("cross-goroutine trace disconnected")
			return
		}
		done <- nil
	}()
	return <-done
}

func assertBusinessParents(t *testing.T, mt mocktracer.Tracer, want int) {
	t.Helper()
	spans := mt.FinishedSpans()
	ids := make(map[uint64]*mocktracer.Span)
	for _, s := range spans {
		ids[s.SpanID()] = s
	}
	count := 0
	for _, s := range spans {
		if s.OperationName() != "compat.business" {
			continue
		}
		count++
		parent := ids[s.ParentID()]
		if parent == nil || parent.TraceID() != s.TraceID() {
			t.Fatalf("business span has no matching parent: %+v", s)
		}
	}
	if count != want {
		t.Fatalf("business spans=%d, want %d", count, want)
	}
	t.Logf("COMPAT_RESULT business_spans=%d parent_links=ok", count)
}

func TestManualV1MuxWithOfficialWeaving(t *testing.T) {
	mt := mocktracer.Start()
	defer mt.Stop()
	router := v1mux.NewRouter()
	router.HandleFunc("/marketing", func(w http.ResponseWriter, r *http.Request) {
		if err := checkContexts(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	const calls = 64
	var wg sync.WaitGroup
	for range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/marketing", nil))
			if w.Code != http.StatusOK {
				t.Errorf("HTTP status=%d: %s", w.Code, w.Body.String())
			}
		}()
	}
	wg.Wait()
	assertBusinessParents(t, mt, calls)
}

type marketingService interface{}

func TestManualV1GRPCWithOfficialWeaving(t *testing.T) {
	mt := mocktracer.Start()
	defer mt.Stop()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(v1grpc.UnaryServerInterceptor()))
	server.RegisterService(&grpc.ServiceDesc{
		ServiceName: "compat.Marketing", HandlerType: (*marketingService)(nil),
		Methods: []grpc.MethodDesc{{MethodName: "List", Handler: func(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
			in := new(emptypb.Empty)
			if err := dec(in); err != nil {
				return nil, err
			}
			handler := func(ctx context.Context, _ interface{}) (interface{}, error) {
				return &emptypb.Empty{}, checkContexts(ctx)
			}
			if interceptor == nil {
				return handler(ctx, in)
			}
			return interceptor(ctx, in, &grpc.UnaryServerInfo{Server: srv, FullMethod: "/compat.Marketing/List"}, handler)
		}}},
	}, struct{}{})
	go server.Serve(lis)
	defer server.Stop()
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	const calls = 64
	var wg sync.WaitGroup
	for range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := conn.Invoke(ctx, "/compat.Marketing/List", &emptypb.Empty{}, &emptypb.Empty{}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	assertBusinessParents(t, mt, calls)
}

func TestAutoSaramaUsesOfficialPatchedWrapper(t *testing.T) {
	mt := mocktracer.Start()
	defer mt.Stop()
	cfg := sarama.NewConfig()
	cfg.Version = sarama.V2_3_0_0 // Sarama v1.23.0's maximum supported protocol.
	cfg.Producer.Return.Successes = true
	p, err := sarama.NewAsyncProducer([]string{"127.0.0.1:19092"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	typ := reflect.TypeOf(p)
	if typ.Kind() != reflect.Pointer || typ.Elem().PkgPath() != "github.com/DataDog/dd-trace-go/contrib/Shopify/sarama/v2" {
		p.AsyncClose()
		t.Fatalf("official auto wrapper missing: %T", p)
	}
	parent, _ := v1tracer.StartSpanFromContext(context.Background(), "compat.kafka.parent")
	msg := &sarama.ProducerMessage{Topic: "inventory-item-delete", Value: sarama.StringEncoder("official-compat")}
	if err := v1tracer.Inject(parent.Context(), saramatrace.NewProducerMessageCarrier(msg)); err != nil {
		t.Fatal(err)
	}
	select {
	case p.Input() <- msg:
	case <-time.After(10 * time.Second):
		t.Fatal("input blocked")
	}
	select {
	case <-p.Successes():
	case err := <-p.Errors():
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("produce blocked")
	}
	p.AsyncClose()
	// Drain both channels during shutdown; never synchronously Close a stuck producer.
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
			t.Fatal("shutdown blocked")
		}
	}
	parent.Finish()
	found := false
	for _, s := range mt.FinishedSpans() {
		if s.OperationName() == "kafka.produce" {
			found = true
			if s.ParentID() != parent.Context().SpanID() {
				t.Fatal("Kafka parent disconnected")
			}
		}
	}
	if !found {
		t.Fatal("auto producer span missing")
	}
	t.Logf("COMPAT_RESULT auto_wrapper=%s producer_parent=ok", typ)
}
