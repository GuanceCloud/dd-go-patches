//go:build tools

package tools

import (
	_ "github.com/DataDog/dd-trace-go/contrib/IBM/sarama/v2" // integration
	_ "github.com/DataDog/orchestrion"                       // integration
)
