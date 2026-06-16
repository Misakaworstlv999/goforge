package builtin

import (
	"context"
	"fmt"
	"math"

	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

// CalcArgs defines the input for the calculator tool.
type CalcArgs struct {
	A  float64 `json:"a" jsonschema:"description=First number,required"`
	B  float64 `json:"b" jsonschema:"description=Second number,required"`
	Op string  `json:"op" jsonschema:"description=Operation,enum=add,enum=subtract,enum=multiply,enum=divide,required"`
}

// NewCalculator returns a tool that performs basic arithmetic.
func NewCalculator() tool.Tool {
	return tool.NewTool[CalcArgs]("calculator", "Basic arithmetic operations", calcFn)
}

func calcFn(_ context.Context, args CalcArgs) (string, error) {
	var result float64
	switch args.Op {
	case "add":
		result = args.A + args.B
	case "subtract":
		result = args.A - args.B
	case "multiply":
		result = args.A * args.B
	case "divide":
		if args.B == 0 {
			return "", fmt.Errorf("division by zero")
		}
		result = args.A / args.B
	default:
		return "", fmt.Errorf("unknown operation: %s", args.Op)
	}

	if result == math.Trunc(result) && !math.IsInf(result, 0) {
		return fmt.Sprintf("%d", int64(result)), nil
	}
	return fmt.Sprintf("%g", result), nil
}
