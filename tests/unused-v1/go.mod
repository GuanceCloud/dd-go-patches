module example.com/unused-v1-graph

go 1.26.0

require (
	github.com/DataDog/dd-trace-go/contrib/Shopify/sarama/v2 v2.10.1
	github.com/DataDog/dd-trace-go/contrib/net/http/v2 v2.10.1
	github.com/DataDog/dd-trace-go/orchestrion/all/v2 v2.10.1
	github.com/DataDog/dd-trace-go/v2 v2.10.1
	github.com/DataDog/orchestrion v1.12.1
	github.com/Shopify/sarama v1.38.1
	gopkg.in/DataDog/dd-trace-go.v1 v1.74.8
	example.com/commonfixture v0.0.0
)

replace example.com/commonfixture => ./commonfixture
