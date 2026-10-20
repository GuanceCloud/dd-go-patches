//go:build tools

package tools

import (
	_ "github.com/DataDog/dd-trace-go/contrib/Shopify/sarama/v2" // integration
	_ "github.com/DataDog/dd-trace-go/contrib/net/http/v2"       // integration
	_ "github.com/DataDog/dd-trace-go/orchestrion/all/v2"        // integration
	_ "github.com/DataDog/orchestrion"                           // integration
)
