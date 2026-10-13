package sarama

import (
	"context"
	"testing"
	"time"

	"github.com/DataDog/dd-trace-go/v2/ddtrace/ext"
	"github.com/DataDog/dd-trace-go/v2/ddtrace/mocktracer"
	"github.com/IBM/sarama"
	"github.com/stretchr/testify/require"
)

// This producer only accepts input when the test explicitly reads it.
type patchBlockedProducer struct {
	sarama.AsyncProducer
	input     chan *sarama.ProducerMessage
	successes chan *sarama.ProducerMessage
	errors    chan *sarama.ProducerError
}

func newPatchBlockedProducer() *patchBlockedProducer {
	return &patchBlockedProducer{
		input:     make(chan *sarama.ProducerMessage),
		successes: make(chan *sarama.ProducerMessage),
		errors:    make(chan *sarama.ProducerError),
	}
}

func (p *patchBlockedProducer) Input() chan<- *sarama.ProducerMessage     { return p.input }
func (p *patchBlockedProducer) Successes() <-chan *sarama.ProducerMessage { return p.successes }
func (p *patchBlockedProducer) Errors() <-chan *sarama.ProducerError      { return p.errors }

func patchSend(t *testing.T, ch chan<- *sarama.ProducerMessage, msg *sarama.ProducerMessage) {
	t.Helper()
	select {
	case ch <- msg:
	case <-time.After(time.Second):
		t.Fatal("input blocked")
	}
}

func patchReceive(t *testing.T, ch <-chan *sarama.ProducerMessage, want *sarama.ProducerMessage) {
	t.Helper()
	select {
	case got := <-ch:
		require.Same(t, want, got)
	case <-time.After(time.Second):
		t.Fatal("message not forwarded")
	}
}

func patchWaitClosed(t *testing.T, p sarama.AsyncProducer) {
	t.Helper()
	select {
	case _, ok := <-p.Successes():
		require.False(t, ok)
	case <-time.After(time.Second):
		t.Fatal("wrapper did not exit")
	}
	select {
	case _, ok := <-p.Errors():
		require.False(t, ok)
	case <-time.After(time.Second):
		t.Fatal("wrapper errors not closed")
	}
}

func TestPatchDrainsSuccessWhileInputBlocked(t *testing.T) {
	mt := mocktracer.Start()
	defer mt.Stop()
	raw := newPatchBlockedProducer()
	cfg := sarama.NewConfig()
	cfg.Version = sarama.V0_11_0_0
	cfg.Producer.Return.Successes = true
	p := WrapAsyncProducer(cfg, raw)
	first := &sarama.ProducerMessage{Topic: "first"}
	second := &sarama.ProducerMessage{Topic: "second"}
	patchSend(t, p.Input(), first)
	patchReceive(t, raw.input, first)
	patchSend(t, p.Input(), second)
	// Sarama cannot accept second until its first success has been drained.
	sent := make(chan struct{})
	go func() { raw.successes <- first; close(sent) }()
	patchReceive(t, p.Successes(), first)
	<-sent
	patchReceive(t, raw.input, second)
	go func() { raw.successes <- second }()
	patchReceive(t, p.Successes(), second)
	close(raw.successes)
	patchWaitClosed(t, p)
	require.Len(t, mt.FinishedSpans(), 2)
}

func TestPatchDrainsErrorWhileInputBlocked(t *testing.T) {
	mt := mocktracer.Start()
	defer mt.Stop()
	raw := newPatchBlockedProducer()
	cfg := sarama.NewConfig()
	cfg.Version = sarama.V0_11_0_0
	cfg.Producer.Return.Successes = true
	p := WrapAsyncProducer(cfg, raw)
	first := &sarama.ProducerMessage{Topic: "first"}
	second := &sarama.ProducerMessage{Topic: "second"}
	patchSend(t, p.Input(), first)
	patchReceive(t, raw.input, first)
	patchSend(t, p.Input(), second)
	producerErr := &sarama.ProducerError{Msg: first, Err: context.Canceled}
	go func() { raw.errors <- producerErr }()
	select {
	case got := <-p.Errors():
		require.Same(t, producerErr, got)
	case <-time.After(time.Second):
		t.Fatal("error was not drained while input was blocked")
	}
	require.Len(t, mt.FinishedSpans(), 1)
	require.Equal(t, producerErr.Error(), mt.FinishedSpans()[0].Tag(ext.ErrorMsg))
	patchReceive(t, raw.input, second)
	go func() { raw.successes <- second }()
	patchReceive(t, p.Successes(), second)
	close(raw.errors)
	patchWaitClosed(t, p)
	require.Len(t, mt.FinishedSpans(), 2)
}

func TestPatchBoundedPendingInputPreservesOrder(t *testing.T) {
	mt := mocktracer.Start()
	defer mt.Stop()
	raw := newPatchBlockedProducer()
	cfg := sarama.NewConfig()
	cfg.Version = sarama.V0_11_0_0
	p := WrapAsyncProducer(cfg, raw)
	first := &sarama.ProducerMessage{Topic: "first"}
	second := &sarama.ProducerMessage{Topic: "second"}
	patchSend(t, p.Input(), first)
	require.Empty(t, mt.FinishedSpans(), "span must not finish before Sarama accepts input")
	select {
	case p.Input() <- second:
		t.Fatal("wrapper accepted more than one pending input")
	case <-time.After(25 * time.Millisecond):
	}
	patchReceive(t, raw.input, first)
	patchSend(t, p.Input(), second)
	patchReceive(t, raw.input, second)
	require.Eventually(t, func() bool { return len(mt.FinishedSpans()) == 2 }, time.Second, time.Millisecond)
	close(raw.successes)
	patchWaitClosed(t, p)
}

func TestPatchFinishesPendingSpanOnShutdown(t *testing.T) {
	for _, closeErrors := range []bool{false, true} {
		name := "successes"
		if closeErrors {
			name = "errors"
		}
		t.Run(name, func(t *testing.T) {
			mt := mocktracer.Start()
			defer mt.Stop()
			raw := newPatchBlockedProducer()
			cfg := sarama.NewConfig()
			cfg.Version = sarama.V0_11_0_0
			cfg.Producer.Return.Successes = true
			p := WrapAsyncProducer(cfg, raw)
			patchSend(t, p.Input(), &sarama.ProducerMessage{Topic: "pending"})
			if closeErrors {
				close(raw.errors)
			} else {
				close(raw.successes)
			}
			patchWaitClosed(t, p)
			require.Len(t, mt.FinishedSpans(), 1)
			require.Equal(t, sarama.ErrShuttingDown.Error(), mt.FinishedSpans()[0].Tag(ext.ErrorMsg))
		})
	}
}
