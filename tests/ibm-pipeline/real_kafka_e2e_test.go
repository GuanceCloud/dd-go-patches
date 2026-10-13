package deadlockrepro

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	saramatrace "github.com/DataDog/dd-trace-go/contrib/IBM/sarama/v2"
	"github.com/DataDog/dd-trace-go/v2/ddtrace/tracer"
	"github.com/IBM/sarama"
)

type productConsumerHandler struct {
	producer         sarama.AsyncProducer
	workerSlots      chan struct{}
	ready            chan struct{}
	readyOnce        sync.Once
	traceEnabled     bool
	groupID          string
	runID            string
	sourceReceived   atomic.Int64
	handlersStarted  atomic.Int64
	handlersComplete atomic.Int64
}

type traceEvidence struct {
	runID          string
	sourceOffset   int64
	consumeTraceID string
	consumeSpanID  uint64
	businessSpanID uint64
	topic          string
}

func (h *productConsumerHandler) Setup(sarama.ConsumerGroupSession) error {
	h.readyOnce.Do(func() { close(h.ready) })
	return nil
}

func (h *productConsumerHandler) Cleanup(sarama.ConsumerGroupSession) error { return nil }

func (h *productConsumerHandler) ConsumeClaim(
	session sarama.ConsumerGroupSession,
	claim sarama.ConsumerGroupClaim,
) error {
	for message := range claim.Messages() {
		h.sourceReceived.Add(1)
		h.workerSlots <- struct{}{}
		h.handlersStarted.Add(1)
		go func(message *sarama.ConsumerMessage) {
			defer func() { <-h.workerSlots }()
			var consumeSpan *tracer.Span
			var businessSpan *tracer.Span
			if h.traceEnabled {
				spanOpts := []tracer.StartSpanOption{
					tracer.ServiceName("sarama-ibm-product-listener-repro"),
					tracer.ResourceName("Consume Topic " + message.Topic),
					tracer.SpanType("queue"),
					tracer.Tag("component", "IBM/sarama"),
					tracer.Tag("span.kind", "consumer"),
					tracer.Tag("messaging.system", "kafka"),
					tracer.Tag("messaging.destination.name", message.Topic),
					tracer.Tag("messaging.kafka.consumer.group", h.groupID),
					tracer.Tag("messaging.kafka.partition", message.Partition),
					tracer.Tag("offset", message.Offset),
					tracer.Tag("repro.run_id", h.runID),
				}
				carrier := saramatrace.NewConsumerMessageCarrier(message)
				if parent, err := tracer.Extract(carrier); err == nil {
					spanOpts = append(spanOpts, tracer.ChildOf(parent))
				}
				consumeSpan = tracer.StartSpan("kafka.consume", spanOpts...)
				businessSpan = tracer.StartSpan(
					"product.delete.handle",
					tracer.ServiceName("sarama-ibm-product-listener-repro"),
					tracer.ResourceName("Delete inventory for product"),
					tracer.SpanType("worker"),
					tracer.ChildOf(consumeSpan.Context()),
					tracer.Tag("repro.run_id", h.runID),
					tracer.Tag("messaging.kafka.source_offset", message.Offset),
				)
			}
			for _, topic := range []string{"inventory-item-delete", "inventory-level-delete"} {
				outgoing := &sarama.ProducerMessage{
					Topic: topic,
					Key:   sarama.StringEncoder(strconv.FormatInt(message.Offset, 10)),
					Value: sarama.ByteEncoder(message.Value),
				}
				if businessSpan != nil {
					_ = tracer.Inject(businessSpan.Context(), saramatrace.NewProducerMessageCarrier(outgoing))
					if message.Offset == 0 {
						outgoing.Metadata = &traceEvidence{
							runID:          h.runID,
							sourceOffset:   message.Offset,
							consumeTraceID: consumeSpan.Context().TraceID(),
							consumeSpanID:  consumeSpan.Context().SpanID(),
							businessSpanID: businessSpan.Context().SpanID(),
							topic:          topic,
						}
					}
				}
				h.producer.Input() <- outgoing
			}
			if businessSpan != nil {
				businessSpan.Finish()
			}
			if consumeSpan != nil {
				consumeSpan.SetTag("worker.pool.capacity", cap(h.workerSlots))
				consumeSpan.Finish()
			}
			session.MarkMessage(message, "")
			h.handlersComplete.Add(1)
		}(message)
	}
	return nil
}

func emitKafkaReproMetrics(
	t *testing.T,
	scenario, runID string,
	handler *productConsumerHandler,
	produceSuccesses, produceErrors, downstreamConsumed int64,
	totalMessages int,
	stalled int64,
) {
	t.Helper()
	conn, err := net.Dial("udp", "127.0.0.1:8125")
	if err != nil {
		t.Fatalf("connect DataKit StatsD: %v", err)
	}
	defer conn.Close()
	tags := fmt.Sprintf("#service:sarama-ibm-product-listener-repro,env:local-repro,scenario:%s,run_id:%s", scenario, runID)
	values := map[string]int64{
		"kafka_repro_source_received":     handler.sourceReceived.Load(),
		"kafka_repro_handlers_started":    handler.handlersStarted.Load(),
		"kafka_repro_handlers_complete":   handler.handlersComplete.Load(),
		"kafka_repro_worker_slots_used":   handler.handlersStarted.Load() - handler.handlersComplete.Load(),
		"kafka_repro_consumer_lag":        int64(totalMessages) - handler.handlersComplete.Load(),
		"kafka_repro_produce_successes":   produceSuccesses,
		"kafka_repro_produce_errors":      produceErrors,
		"kafka_repro_downstream_consumed": downstreamConsumed,
		"kafka_repro_pipeline_stalled":    stalled,
	}
	var payload strings.Builder
	for name, value := range values {
		fmt.Fprintf(&payload, "%s:%d|g|%s\n", name, value, tags)
	}
	if _, err := conn.Write([]byte(payload.String())); err != nil {
		t.Fatalf("send Kafka reproduction metrics to DataKit: %v", err)
	}
}

func kafkaConfig() *sarama.Config {
	cfg := sarama.NewConfig()
	cfg.Version = sarama.V2_3_0_0 // Use the same Kafka 2.3 protocol as the original fault-recovery fixture.
	cfg.ClientID = "ddtrace-sarama-deadlock-repro"
	cfg.ChannelBufferSize = 1
	cfg.Net.ReadTimeout = 750 * time.Millisecond
	cfg.Net.WriteTimeout = 750 * time.Millisecond
	cfg.Net.DialTimeout = 2 * time.Second
	cfg.Producer.RequiredAcks = sarama.WaitForLocal
	cfg.Producer.Return.Successes = true
	cfg.Producer.Return.Errors = true
	cfg.Producer.Retry.Max = 30
	cfg.Producer.Retry.Backoff = 100 * time.Millisecond
	cfg.Consumer.Offsets.Initial = sarama.OffsetOldest
	cfg.Consumer.Group.Rebalance.Strategy = sarama.BalanceStrategyRange
	return cfg
}

func createTopics(t *testing.T, brokers []string, topics ...string) {
	t.Helper()
	admin, err := sarama.NewClusterAdmin(brokers, kafkaConfig())
	if err != nil {
		t.Fatalf("create Kafka admin: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	for _, topic := range topics {
		err := admin.CreateTopic(topic, &sarama.TopicDetail{NumPartitions: 1, ReplicationFactor: 1}, false)
		if err != nil && !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("create topic %s: %v", topic, err)
		}
	}
}

func seedDeleteMessages(t *testing.T, brokers []string, topic string, total int) {
	t.Helper()
	producer, err := sarama.NewSyncProducer(brokers, kafkaConfig())
	if err != nil {
		t.Fatalf("create source producer: %v", err)
	}
	defer producer.Close()

	messages := make([]*sarama.ProducerMessage, total)
	for i := range messages {
		messages[i] = &sarama.ProducerMessage{
			Topic: topic,
			Key:   sarama.StringEncoder(strconv.Itoa(i)),
			Value: sarama.StringEncoder(fmt.Sprintf("delete-%d", i)),
		}
	}
	if err := producer.SendMessages(messages); err != nil {
		t.Fatalf("seed source messages: %v", err)
	}
}

// exportCompleteTrace uses real Kafka I/O but a synchronous producer so the
// observability proof is deterministic and independent from the async wrapper
// deadlock that the rest of the test intentionally creates.
func exportCompleteTrace(t *testing.T, brokers []string, sourceTopic, groupID, runID string) string {
	t.Helper()
	consumer, err := sarama.NewConsumer(brokers, kafkaConfig())
	if err != nil {
		t.Fatalf("create trace-evidence consumer: %v", err)
	}
	defer consumer.Close()
	partitionConsumer, err := consumer.ConsumePartition(sourceTopic, 0, sarama.OffsetOldest)
	if err != nil {
		t.Fatalf("consume trace-evidence source: %v", err)
	}
	defer partitionConsumer.Close()
	var source *sarama.ConsumerMessage
	select {
	case source = <-partitionConsumer.Messages():
	case err := <-partitionConsumer.Errors():
		t.Fatalf("read trace-evidence source: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out reading trace-evidence source message")
	}

	consumeSpan := tracer.StartSpan(
		"kafka.consume",
		tracer.ServiceName("sarama-ibm-product-listener-repro"),
		tracer.ResourceName("Consume Topic "+source.Topic),
		tracer.SpanType("queue"),
		tracer.Tag("component", "IBM/sarama"),
		tracer.Tag("span.kind", "consumer"),
		tracer.Tag("messaging.system", "kafka"),
		tracer.Tag("messaging.destination.name", source.Topic),
		tracer.Tag("messaging.kafka.consumer.group", groupID),
		tracer.Tag("messaging.kafka.partition", source.Partition),
		tracer.Tag("offset", source.Offset),
		tracer.Tag("repro.run_id", runID),
	)
	businessSpan := tracer.StartSpan(
		"product.delete.handle",
		tracer.ServiceName("sarama-ibm-product-listener-repro"),
		tracer.ResourceName("Delete inventory for product"),
		tracer.SpanType("worker"),
		tracer.ChildOf(consumeSpan.Context()),
		tracer.Tag("repro.run_id", runID),
		tracer.Tag("messaging.kafka.source_offset", source.Offset),
	)

	rawProducer, err := sarama.NewSyncProducer(brokers, kafkaConfig())
	if err != nil {
		t.Fatalf("create trace-evidence producer: %v", err)
	}
	producer := saramatrace.WrapSyncProducer(
		kafkaConfig(), rawProducer, saramatrace.WithService("kafka"),
	)
	for _, topic := range []string{"inventory-item-delete", "inventory-level-delete"} {
		outgoing := &sarama.ProducerMessage{
			Topic: topic,
			Key:   sarama.StringEncoder(strconv.FormatInt(source.Offset, 10)),
			Value: sarama.ByteEncoder(source.Value),
		}
		if err := tracer.Inject(businessSpan.Context(), saramatrace.NewProducerMessageCarrier(outgoing)); err != nil {
			t.Fatalf("inject trace-evidence producer context: %v", err)
		}
		if _, _, err := producer.SendMessage(outgoing); err != nil {
			t.Fatalf("publish trace-evidence message to %s: %v", topic, err)
		}
		producerContext, err := tracer.Extract(saramatrace.NewProducerMessageCarrier(outgoing))
		if err != nil {
			t.Fatalf("extract trace-evidence producer context: %v", err)
		}
		t.Logf(
			"TRACE_EVIDENCE run_id=%s source_offset=%d trace_id=%s consume_span_id=%d business_span_id=%d business_parent_id=%d producer_topic=%s producer_trace_id=%s producer_span_id=%d producer_parent_id=%d",
			runID, source.Offset, consumeSpan.Context().TraceID(), consumeSpan.Context().SpanID(),
			businessSpan.Context().SpanID(), consumeSpan.Context().SpanID(), topic,
			producerContext.TraceID(), producerContext.SpanID(), businessSpan.Context().SpanID(),
		)
	}
	if err := producer.Close(); err != nil {
		t.Fatalf("close trace-evidence producer: %v", err)
	}
	businessSpan.Finish()
	consumeSpan.Finish()
	tracer.Flush()
	return consumeSpan.Context().TraceID()
}

func dockerContainer(t *testing.T, action, container string) {
	t.Helper()
	output, err := exec.Command("docker", action, container).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s %s: %v: %s", action, container, err, output)
	}
}

func waitAtomicAtLeast(t *testing.T, value *atomic.Int64, target int64, timeout time.Duration, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if value.Load() >= target {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s: got %d, want >= %d", what, value.Load(), target)
}

// runRealKafkaConsumerPipeline reproduces the production topology with a real
// Kafka broker and a real client consumer. The fault injection pauses the
// single-node broker long enough to fill the producer's tiny channel, then
// resumes it. A vulnerable wrapper can remain in circular wait even though the
// broker has recovered, poisoning all ten shared workers.
func runRealKafkaConsumerPipeline(t *testing.T, wrapWithDDTrace bool) {
	t.Helper()
	if os.Getenv("REAL_KAFKA_REPRO") != "1" {
		t.Skip("set REAL_KAFKA_REPRO=1 and start docker-compose.yml")
	}
	if wrapWithDDTrace {
		startTracer(t)
	}

	brokers := []string{"127.0.0.1:19092"}
	scenario := "ddtrace_disabled"
	if wrapWithDDTrace {
		scenario = "ddtrace_enabled"
	}
	runID := os.Getenv("REPRO_RUN_ID")
	if runID == "" {
		runID = strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	sourceTopic := "products-delete-" + scenario + "-" + runID
	groupID := "sarama-ibm-product-change-group-v2-" + scenario + "-" + runID
	container := "ddtrace-sarama-repro-kafka"
	const totalMessages = 5000
	runStarted := time.Now()
	t.Logf("RUN_START run_id=%s scenario=%s time=%s", runID, scenario, runStarted.Format(time.RFC3339Nano))
	createTopics(t, brokers, sourceTopic, "inventory-item-delete", "inventory-level-delete")
	seedDeleteMessages(t, brokers, sourceTopic, totalMessages)
	if wrapWithDDTrace {
		traceID := exportCompleteTrace(t, brokers, sourceTopic, groupID, runID)
		t.Logf("TRACE_EXPORTED run_id=%s trace_id=%s", runID, traceID)
	}

	producerCfg := kafkaConfig()
	producerCfg.ChannelBufferSize = 10
	rawProducer, err := sarama.NewAsyncProducer(brokers, producerCfg)
	if err != nil {
		t.Fatalf("create downstream async producer: %v", err)
	}
	var businessProducer sarama.AsyncProducer = rawProducer
	if wrapWithDDTrace {
		businessProducer = saramatrace.WrapAsyncProducer(
			producerCfg,
			rawProducer,
			saramatrace.WithService("kafka"),
		)
	}
	// Do not call AsyncClose after the expected deadlock: it waits for the same
	// blocked wrapper goroutine that this test is proving. The short-lived test
	// process owns the producer and releases it when the process exits.

	var produceSuccesses atomic.Int64
	var produceErrors atomic.Int64
	go func() {
		for message := range businessProducer.Successes() {
			produceSuccesses.Add(1)
			if evidence, ok := message.Metadata.(*traceEvidence); ok {
				if producerContext, err := tracer.Extract(saramatrace.NewProducerMessageCarrier(message)); err == nil {
					fmt.Printf(
						"TRACE_EVIDENCE run_id=%s source_offset=%d trace_id=%s consume_span_id=%d business_span_id=%d producer_topic=%s producer_trace_id=%s producer_span_id=%d producer_parent_id=%d\n",
						evidence.runID, evidence.sourceOffset, evidence.consumeTraceID,
						evidence.consumeSpanID, evidence.businessSpanID, evidence.topic,
						producerContext.TraceID(), producerContext.SpanID(), evidence.businessSpanID,
					)
				}
			}
		}
	}()
	go func() {
		for producerError := range businessProducer.Errors() {
			produceErrors.Add(1)
			t.Logf("downstream producer error: %v", producerError)
		}
	}()

	// A real client consumer observes the two downstream topics. Its count is
	// independent evidence that published messages stop after the deadlock.
	downstreamConsumer, err := sarama.NewConsumer(brokers, kafkaConfig())
	if err != nil {
		t.Fatalf("create downstream client consumer: %v", err)
	}
	t.Cleanup(func() { _ = downstreamConsumer.Close() })
	var downstreamConsumed atomic.Int64
	for _, topic := range []string{"inventory-item-delete", "inventory-level-delete"} {
		partitionConsumer, err := downstreamConsumer.ConsumePartition(topic, 0, sarama.OffsetNewest)
		if err != nil {
			t.Fatalf("consume downstream topic %s: %v", topic, err)
		}
		t.Cleanup(func() { _ = partitionConsumer.Close() })
		go func(consumer sarama.PartitionConsumer) {
			for range consumer.Messages() {
				downstreamConsumed.Add(1)
			}
		}(partitionConsumer)
	}

	group, err := sarama.NewConsumerGroup(brokers, groupID, kafkaConfig())
	if err != nil {
		t.Fatalf("create source consumer group: %v", err)
	}
	// Do not call group.Close after the expected stall. Sarama waits for
	// ConsumeClaim, which is intentionally blocked at the exhausted worker pool.
	handler := &productConsumerHandler{
		producer:     businessProducer,
		workerSlots:  make(chan struct{}, 10),
		ready:        make(chan struct{}),
		traceEnabled: wrapWithDDTrace,
		groupID:      groupID,
		runID:        runID,
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for ctx.Err() == nil {
			if err := group.Consume(ctx, []string{sourceTopic}, handler); err != nil && ctx.Err() == nil {
				t.Logf("source consumer error: %v", err)
			}
		}
	}()
	select {
	case <-handler.ready:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for source consumer group setup")
	}
	waitAtomicAtLeast(t, &handler.handlersStarted, 10, 10*time.Second, "ten active handlers")
	if wrapWithDDTrace {
		// Export a few complete consume -> produce traces before fault injection,
		// so the APM UI has healthy end-to-end chains as well as the later stall.
		waitAtomicAtLeast(t, &produceSuccesses, 10, 10*time.Second, "healthy traced producer successes")
	}

	// Pause and resume the broker while delete handlers are publishing. Cleanup
	// always unpauses it, including if the assertion below fails.
	dockerContainer(t, "pause", container)
	t.Cleanup(func() { _ = exec.Command("docker", "unpause", container).Run() })
	time.Sleep(2 * time.Second)
	dockerContainer(t, "unpause", container)
	expectRecovery := !wrapWithDDTrace || os.Getenv("EXPECT_DDTRACE_RECOVERY") == "1"
	if expectRecovery {
		recoveryDescription := "without ddtrace wrapper"
		if wrapWithDDTrace {
			recoveryDescription = "with fixed ddtrace wrapper"
		}
		waitAtomicAtLeast(
			t, &handler.handlersComplete, totalMessages, 30*time.Second,
			"all handlers to recover "+recoveryDescription,
		)
		waitAtomicAtLeast(
			t, &produceSuccesses, totalMessages*2, 30*time.Second,
			"all downstream producer successes "+recoveryDescription,
		)
		waitAtomicAtLeast(
			t, &downstreamConsumed, totalMessages*2, 30*time.Second,
			"all downstream messages to be consumed "+recoveryDescription,
		)
		if got := produceErrors.Load(); got != 0 {
			t.Fatalf("recovered pipeline had %d producer errors, want 0", got)
		}
		t.Logf(
			"pipeline recovered %s: source_received=%d handlers_complete=%d/%d produce_successes=%d produce_errors=%d downstream_consumed=%d",
			recoveryDescription,
			handler.sourceReceived.Load(), handler.handlersComplete.Load(), totalMessages,
			produceSuccesses.Load(), produceErrors.Load(), downstreamConsumed.Load(),
		)
		emitKafkaReproMetrics(
			t, scenario, runID, handler,
			produceSuccesses.Load(), produceErrors.Load(), downstreamConsumed.Load(),
			totalMessages, 0,
		)
		t.Logf(
			"RUN_RESULT run_id=%s scenario=%s source_received=%d handlers_started=%d handlers_complete=%d worker_slots_used=%d consumer_lag=%d produce_successes=%d produce_errors=%d downstream_consumed=%d pipeline_stalled=0 time=%s",
			runID, scenario, handler.sourceReceived.Load(), handler.handlersStarted.Load(),
			handler.handlersComplete.Load(), handler.handlersStarted.Load()-handler.handlersComplete.Load(),
			int64(totalMessages)-handler.handlersComplete.Load(), produceSuccesses.Load(),
			produceErrors.Load(), downstreamConsumed.Load(), time.Now().Format(time.RFC3339Nano),
		)
		return
	}

	// Give Kafka and Sarama time to recover. If progress then remains unchanged
	// while work is outstanding, capture stacks and verify the wrapper is stuck.
	time.Sleep(3 * time.Second)
	completedBefore := handler.handlersComplete.Load()
	consumedBefore := downstreamConsumed.Load()
	time.Sleep(3 * time.Second)
	completedAfter := handler.handlersComplete.Load()
	consumedAfter := downstreamConsumed.Load()

	t.Logf(
		"source_received=%d handlers_started=%d handlers_complete=%d/%d produce_successes=%d produce_errors=%d downstream_consumed=%d",
		handler.sourceReceived.Load(), handler.handlersStarted.Load(), completedAfter, totalMessages,
		produceSuccesses.Load(), produceErrors.Load(), consumedAfter,
	)
	emitKafkaReproMetrics(
		t, "ddtrace_enabled", runID, handler,
		produceSuccesses.Load(), produceErrors.Load(), downstreamConsumed.Load(),
		totalMessages, 1,
	)
	t.Logf(
		"RUN_RESULT run_id=%s scenario=%s source_received=%d handlers_started=%d handlers_complete=%d worker_slots_used=%d consumer_lag=%d produce_successes=%d produce_errors=%d downstream_consumed=%d pipeline_stalled=1 time=%s",
		runID, scenario, handler.sourceReceived.Load(), handler.handlersStarted.Load(),
		handler.handlersComplete.Load(), handler.handlersStarted.Load()-handler.handlersComplete.Load(),
		int64(totalMessages)-handler.handlersComplete.Load(), produceSuccesses.Load(),
		produceErrors.Load(), downstreamConsumed.Load(), time.Now().Format(time.RFC3339Nano),
	)
	if completedAfter >= totalMessages {
		t.Fatalf("all handlers completed; real-broker fault did not reproduce the stall")
	}
	if completedAfter != completedBefore || consumedAfter != consumedBefore {
		t.Fatalf(
			"pipeline still progressing after broker recovery: handlers %d->%d, downstream %d->%d",
			completedBefore, completedAfter, consumedBefore, consumedAfter,
		)
	}
	blockedWorkers := handler.handlersStarted.Load() - completedAfter
	if blockedWorkers != int64(cap(handler.workerSlots)) {
		t.Fatalf("expected all %d workers blocked, got %d", cap(handler.workerSlots), blockedWorkers)
	}
	if handler.sourceReceived.Load()-handler.handlersStarted.Load() != 1 {
		t.Fatalf(
			"expected consumer loop blocked on exactly one undispatched message: received=%d started=%d",
			handler.sourceReceived.Load(), handler.handlersStarted.Load(),
		)
	}

	var stacks bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&stacks, 2); err != nil {
		t.Fatalf("capture goroutine stacks: %v", err)
	}
	stackText := stacks.String()
	if !strings.Contains(stackText, "contrib/IBM/sarama/v2.WrapAsyncProducer.func") ||
		!strings.Contains(stackText, "producer.go:137") {
		t.Fatalf("pipeline stalled, but expected wrapper stack was not found:\n%s", stackText)
	}
	t.Logf("reproduced wrapper deadlock after broker recovery; wrapper stack contains producer.go:137")
}

// TestRealKafkaConsumerPipeline is opt-in because it requires
// docker-compose.yml to be running:
//
//	REAL_KAFKA_REPRO=1 go test -run TestRealKafkaConsumerPipeline -v -timeout 90s
func TestRealKafkaConsumerPipeline(t *testing.T) {
	runRealKafkaConsumerPipeline(t, true)
}

// TestRealKafkaConsumerPipelineWithoutDDTrace is the A/B control. It uses the
// same broker pause, real client consumer, and 10-worker topology, but sends
// through the original Sarama AsyncProducer without the ddtrace wrapper.
func TestRealKafkaConsumerPipelineWithoutDDTrace(t *testing.T) {
	runRealKafkaConsumerPipeline(t, false)
}

// TestRealKafkaCompleteTraceExport exports exactly one complete business trace
// through real Kafka without entering the async-producer deadlock scenario.
// It is useful after a DataKit restart when intake counters need a clean,
// unambiguous four-span verification.
func TestRealKafkaCompleteTraceExport(t *testing.T) {
	if os.Getenv("REAL_KAFKA_REPRO") != "1" {
		t.Skip("set REAL_KAFKA_REPRO=1 and start docker-compose.yml")
	}
	startTracer(t)

	runID := os.Getenv("REPRO_RUN_ID")
	if runID == "" {
		runID = strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	brokers := []string{"127.0.0.1:19092"}
	sourceTopic := "products-delete-trace-only-" + runID
	groupID := "sarama-ibm-product-change-trace-only-" + runID
	createTopics(t, brokers, sourceTopic, "inventory-item-delete", "inventory-level-delete")
	seedDeleteMessages(t, brokers, sourceTopic, 1)
	traceID := exportCompleteTrace(t, brokers, sourceTopic, groupID, runID)
	t.Logf("TRACE_ONLY_RESULT run_id=%s trace_id=%s spans=4", runID, traceID)
	// Let the trace writer observe the intake response before the test exits.
	time.Sleep(250 * time.Millisecond)
}
