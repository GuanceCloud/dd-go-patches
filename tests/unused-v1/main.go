package main

import (
	"fmt"

	"example.com/commonfixture"
	"github.com/Shopify/sarama"
)

func main() {
	fmt.Println(commonfixture.Name())
	// Compiles the real constructor call without connecting to a Kafka broker.
	p, err := sarama.NewAsyncProducer(nil, sarama.NewConfig())
	fmt.Printf("producer=%T err=%v\n", p, err)
	if p != nil {
		p.AsyncClose()
	}
}
